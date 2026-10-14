package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/network"
)

// The local socket (the screen): no session, only the allowlist, the Wi-Fi without the camera keys, and the
// network PUTs with the full state
func TestSocketLocal(t *testing.T) {
	e := setup(t, true)
	e.srv.Network = &network.Manager{Dir: e.dir, StatePath: filepath.Join(e.dir, "ap-state.json"), HardwareID: "abcdef0123",
		Apply:   func(context.Context) error { return nil },
		Write:   func(p string, b []byte, m os.FileMode) error { return config.WriteAtomic(p, b, m) },
		LANAddr: func() string { return "192.168.1.232" }}
	cr, err := e.srv.Network.CreateCamera(context.Background(), "Móvil de Ana")
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimPrefix(cr.Connect.RTMPKey, "cam1?user=cam1&pass=")

	// A real socket (short path: on the Mac, t.TempDir paths exceed the sun_path limit)
	dir, err := os.MkdirTemp("/tmp", "eirl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: e.srv.LocalHandler(), ConnContext: LocalConnContext}
	go hs.Serve(ln)
	defer hs.Close()
	cl := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	do := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// No session and no CSRF
	if code, body := do("GET", "/api/v1/network", ""); code != 200 || !strings.Contains(body, `"id":"cam1"`) {
		t.Fatalf("GET /network: %d %s", code, body)
	}
	// The Wi-Fi, without the cameras or their keys; with ?qr=rows, the QR as rows
	code, body := do("GET", "/api/v1/network/credentials?qr=rows", "")
	var c struct {
		WiFi *struct {
			SSID, Password string
			QRRows         []string `json:"qrRows"`
		} `json:"wifi"`
	}
	json.Unmarshal([]byte(body), &c)
	if code != 200 || c.WiFi == nil || c.WiFi.Password == "" || len(c.WiFi.QRRows) < 21 || strings.Contains(body, "cameras") || strings.Contains(body, key) {
		t.Fatalf("credentials over the socket: %d %s", code, body)
	}
	if code, _ := do("GET", "/api/v1/network/credentials?qr=svg", ""); code != 400 {
		t.Errorf("invalid qr: %d", code)
	}
	// Outside the allowlist: no camera keys, no restart, no password, no general status
	for _, r := range [][2]string{{"GET", "/api/v1/status"}, {"POST", "/api/v1/cameras"}, {"GET", "/api/v1/cameras/cam1/qr.svg"},
		{"GET", "/api/v1/network/qr/wifi.svg"}, {"PUT", "/api/v1/password"}, {"POST", "/api/v1/service/reboot"},
		{"DELETE", "/api/v1/cloud"}, {"GET", "/api/v1/logs"}, {"POST", "/api/v1/mixer"}, {"GET", "/"}} {
		if code, body := do(r[0], r[1], "{}"); code != 403 || !strings.Contains(body, "local_forbidden") {
			t.Errorf("%s %s over the socket: %d %s", r[0], r[1], code, body)
		}
	}
	// The network PUTs: the same validation and the full state (like GET /network)
	if code, _ := do("PUT", "/api/v1/network", `{"enabled":"no"}`); code != 400 {
		t.Errorf("invalid PUT /network: %d", code)
	}
	if code, body := do("PUT", "/api/v1/network", `{"enabled":false}`); code != 200 || !strings.HasPrefix(body, `{"enabled":false,"accessPoint"`) {
		t.Errorf("turn reception off: %d %s", code, body)
	}
	if code, body := do("PUT", "/api/v1/network/ap", `{"channel":"6"}`); code != 200 || !strings.Contains(body, `"cameras":{"max"`) {
		t.Errorf("PUT /network/ap: %d %s", code, body)
	}
	if code, body := do("PUT", "/api/v1/network/lan", `{"enabled":true}`); code != 200 || !strings.Contains(body, `"lan":{"enabled":true`) || !strings.Contains(body, `"accessPoint"`) {
		t.Errorf("PUT /network/lan: %d %s", code, body)
	}
	// The board's Wi-Fi (WIFI.md §5), over the socket too
	if code, body := do("GET", "/api/v1/network/wifi", ""); code != 200 || !strings.Contains(body, `"client":null,"ap":{"policy":"always"`) {
		t.Errorf("GET /network/wifi: %d %s", code, body)
	}
	if code, _ := do("PUT", "/api/v1/network/wifi", `{"ap":"siempre"}`); code != 400 {
		t.Errorf("invalid PUT /network/wifi: %d", code)
	}
	// The Wi-Fi client, without NetworkManager on this device: 503; a bad body, 400
	if code, body := do("PUT", "/api/v1/network/wifi", `{"client":true}`); code != 503 || !strings.Contains(body, "wifi_unavailable") {
		t.Errorf("PUT /network/wifi with client and no Wi-Fi client: %d %s", code, body)
	}
	for _, r := range [][3]string{{"GET", "/api/v1/network/wifi/scan", ""}, {"POST", "/api/v1/network/wifi/connect", `{"ssid":"Bar","password":"secreto123"}`},
		{"POST", "/api/v1/network/wifi/forget", `{"ssid":"Bar"}`}} {
		if code, _ := do(r[0], r[1], r[2]); code != 503 {
			t.Errorf("%s %s without Wi-Fi client: %d", r[0], r[1], code)
		}
	}
	for _, b := range []string{`{}`, `{"ssid":"Bar","pass":"x"}`, `{"ssid":1}`} {
		if code, _ := do("POST", "/api/v1/network/wifi/connect", b); code != 400 {
			t.Errorf("connect %s: %d", b, code)
		}
	}
	if code, body := do("PUT", "/api/v1/network/wifi", `{"ap":"rescue"}`); code != 200 || !strings.Contains(body, `"policy":"rescue"`) {
		t.Errorf("PUT /network/wifi: %d %s", code, body)
	}
	if code, body := do("GET", "/api/v1/network", ""); code != 200 || !strings.Contains(body, `"policy":"rescue"`) {
		t.Errorf("GET /network without the policy: %s", body)
	}
	// Turning off “Create my Wi-Fi” while streaming with an on-air camera coming in through it: 409; over cable, OK.
	// With reception on (when off, no camera comes in)
	if code, _ := do("PUT", "/api/v1/network/wifi", `{"ap":"always"}`); code != 200 {
		t.Fatalf("always: %d", code)
	}
	if code, body := do("PUT", "/api/v1/network", `{"enabled":true}`); code != 200 {
		t.Fatalf("reception: %d %s", code, body)
	}
	remote := "10.42.0.74"
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/paths/list":
			w.Write([]byte(`{"items":[{"name":"cam1","ready":true,"source":{"type":"srtConn","id":"c1"},"tracks":["H264"],"bytesReceived":1}]}`))
		case "/v3/srtconns/get/c1":
			w.Write([]byte(`{"remoteAddr":"` + remote + `:5000"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer media.Close()
	e.srv.Network.MediaAPI = media.URL
	os.WriteFile(filepath.Join(e.dir, "ap-state.json"), []byte(`{"state":"on","mode":"normal","ssid":"EnhancedIRL-0123","channel":6,"lan":"on"}`), 0o644)
	runDir := t.TempDir()
	e.srv.Mixer = &mixer.Client{StatePath: filepath.Join(runDir, "mixer.json")}
	os.WriteFile(filepath.Join(runDir, "mixer.json"), []byte(`{"v":1,"program":{"main":"net:cam1","layout":"full"},"sources":[{"id":"net:cam1","name":"Móvil de Ana"}],"output":{"mode":"on","state":"on"}}`), 0o644)
	if code, body := do("PUT", "/api/v1/network/wifi", `{"ap":"rescue"}`); code != 409 || !strings.Contains(body, "on_air") {
		t.Errorf("rescue with an on-air camera over the Wi-Fi: %d %s", code, body)
	}
	// Renaming it restarts it: the same rule, with its own reason, and the name does not change. The screen renames it
	// over the socket with this route (already on the whitelist: no new one)
	if code, body := do("PUT", "/api/v1/network/ap", `{"ssid":"Studio A"}`); code != 409 || !strings.Contains(body, `"error":"on_air"`) ||
		!strings.Contains(body, "Móvil de Ana") {
		t.Errorf("rename with an on-air camera over the Wi-Fi: %d %s", code, body)
	}
	if _, body := do("GET", "/api/v1/network", ""); strings.Contains(body, `"name":"Studio A"`) {
		t.Errorf("a refused rename changed the name: %s", body)
	}
	remote = "192.168.1.50"
	if code, body := do("PUT", "/api/v1/network/ap", `{"ssid":"Studio A"}`); code != 200 || !strings.Contains(body, `"name":"Studio A"`) {
		t.Errorf("rename with the on-air camera over cable: %d %s", code, body)
	}
	if code, body := do("GET", "/api/v1/network/credentials?qr=rows", ""); code != 200 || !strings.Contains(body, `"ssid":"Studio A"`) {
		t.Errorf("the screen's credentials after the rename: %d %s", code, body)
	}
	if code, body := do("PUT", "/api/v1/network/wifi", `{"ap":"rescue"}`); code != 200 {
		t.Errorf("rescue with the on-air camera over cable: %d %s", code, body)
	}
	// And the web, with its session, still reads accessPoint and lan in those responses, and the camera keys
	tok := e.login(t)
	origin := []string{"Origin", "http://example.com", "Host", "example.com"}
	if w := e.do("PUT", "/api/v1/network/lan", `{"enabled":false}`, tok, origin...); w.Code != 200 || !strings.Contains(w.Body.String(), `"lan":{"enabled":false`) {
		t.Errorf("web, PUT /network/lan: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/v1/network/credentials", "", tok); !strings.Contains(w.Body.String(), key) {
		t.Errorf("web, credentials without the cameras: %s", w.Body)
	}
}

type wrapped struct{ net.Conn }

func (w wrapped) NetConn() net.Conn { return w.Conn }

// eirl-encoderd's connection limit wraps the connection: the caller is taken from the underlying socket
func TestUnixConnDesenvuelve(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "eirl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if unixConn(wrapped{wrapped{c}}) != c.(*net.UnixConn) {
		t.Error("does not reach the underlying socket")
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if unixConn(wrapped{a}) != nil || unixConn(nil) != nil {
		t.Error("something that is not a Unix socket")
	}
}
