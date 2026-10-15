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

	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/network"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/sources"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
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
	// Outside the allowlist: the QR images (the screen draws its own), the service other than restart, the logs, the
	// mixer and the output (the engine is the mixer), the preview and the web session
	for _, r := range [][2]string{{"GET", "/api/v1/cameras/cam1/qr.svg"}, {"GET", "/api/v1/network/qr/wifi.svg"},
		{"POST", "/api/v1/service/reboot"}, {"POST", "/api/v1/service/start"}, {"POST", "/api/v1/service/stop"},
		{"GET", "/api/v1/logs"}, {"GET", "/api/v1/events"}, {"POST", "/api/v1/mixer"}, {"PUT", "/api/v1/output"},
		{"POST", "/api/v1/live"}, {"POST", "/api/v1/login"}, {"GET", "/api/v1/info"}, {"GET", "/"}} {
		if code, body := do(r[0], r[1], "{}"); code != 403 || !strings.Contains(body, "local_forbidden") {
			t.Errorf("%s %s over the socket: %d %s", r[0], r[1], code, body)
		}
	}
	if len(e.unit.actions) != 0 {
		t.Errorf("a forbidden service action reached the unit: %v", e.unit.actions)
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

// Oct 14, everything the web configures also from the screen: the new routes over the socket, the secrets in clear
// only there (never on the web), and the password change without a session
func TestSocketScreenParity(t *testing.T) {
	e := setup(t, true)
	e.srv.Network = &network.Manager{Dir: e.dir, StatePath: filepath.Join(e.dir, "ap-state.json"), HardwareID: "abcdef0123",
		Apply:   func(context.Context) error { return nil },
		Write:   func(p string, b []byte, m os.FileMode) error { return config.WriteAtomic(p, b, m) },
		LANAddr: func() string { return "192.168.1.232" }}
	e.srv.Uplinks = &uplinks.Reader{Available: filepath.Join(e.dir, "available"), Disabled: filepath.Join(e.dir, "uplinks-disabled"), SysNet: e.dir}
	os.WriteFile(e.srv.Uplinks.Available, []byte("eth0 192.168.1.232\nenx1 192.168.8.100\n"), 0o644)
	e.srv.Sources = &sources.Scanner{SysRoot: filepath.Join(e.dir, "sys"), DevRoot: filepath.Join(e.dir, "dev")}
	fc := &fakeCloud{view: cloud.View{State: "linked", AccountLabel: "Ana"}}
	e.srv.Cloud = fc
	do := localClient(t, e.srv)
	lh := e.srv.LocalHandler()
	tok := e.login(t) // a web session: the screen's password change closes it
	const pass = "secretpassphrase"

	// The destination: GET masks the key, as on the web
	code, body := do("GET", "/api/v1/destination", "")
	if code != 200 || !strings.Contains(body, `"masked":"publish:`) || strings.Contains(body, key) || strings.Contains(body, pass) {
		t.Errorf("GET /destination over the socket: %d %s", code, body)
	}
	// Its secrets in clear, only over the socket, no-store
	secrets := func() (int, map[string]string, http.Header) {
		t.Helper()
		w := httptest.NewRecorder()
		lh.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/destination/secrets", nil))
		var m map[string]string
		json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m, w.Header()
	}
	if code, m, h := secrets(); code != 200 || m["streamId"] != "publish:"+key || m["passphrase"] != pass || len(m) != 4 ||
		h.Get("Cache-Control") != "no-store" {
		t.Errorf("secrets over the socket: %d %v %v", code, m, h)
	}
	for _, c := range []string{tok, ""} { // the web, with a session (404) or without one (401): never the values
		if w := e.do("GET", "/api/v1/destination/secrets", "", c); (w.Code != 404 && w.Code != 401) || strings.Contains(w.Body.String(), key) {
			t.Errorf("secrets on the web (session %t): %d %s", c != "", w.Code, w.Body)
		}
	}
	// Managed by EnhancedIRL: not editable (as on the web), but the screen still reveals the key
	e.setManaged(t, true)
	if code, body := do("PUT", "/api/v1/destination", `{"latencyMs":800}`); code != 409 || !strings.Contains(body, `"error":"managed"`) {
		t.Errorf("PUT /destination managed: %d %s", code, body)
	}
	if code, m, _ := secrets(); code != 200 || m["streamId"] != "publish:"+key {
		t.Errorf("secrets with the destination managed: %d %v", code, m)
	}
	e.setManaged(t, false)
	// PUT: the same validation and the same answer (the key masked); apply restarts the engine
	if code, body := do("PUT", "/api/v1/destination", `{"latencyMs":5}`); code != 400 || !strings.Contains(body, `"latencyMs"`) {
		t.Errorf("invalid PUT /destination: %d %s", code, body)
	}
	if code, body := do("PUT", "/api/v1/destination", `{"latencyMs":800,"apply":true}`); code != 200 || !strings.Contains(body, `"latencyMs":800`) ||
		!strings.Contains(body, `"restarted":true`) || strings.Contains(body, key) {
		t.Errorf("PUT /destination over the socket: %d %s", code, body)
	}
	// A new key, then cleared: the secrets follow what is saved ("" when unset)
	if code, body := do("PUT", "/api/v1/destination", `{"streamId":"publish:otra-clave","passphrase":null}`); code != 200 {
		t.Errorf("new key: %d %s", code, body)
	}
	if _, m, _ := secrets(); m["streamId"] != "publish:otra-clave" || m["passphrase"] != "" {
		t.Errorf("secrets after the change: %v", m)
	}
	// The body limit of the web's mutations, over the socket too (valid JSON, padded to 20 KB)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/v1/destination", strings.NewReader(`{"latencyMs":700`+strings.Repeat(" ", 20<<10)+`}`))
	lh.ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"error":"bad_request"`) {
		t.Errorf("a 20 KB body over the socket: %d %s", w.Code, w.Body)
	}

	// Uplinks, input and sources
	if code, body := do("PUT", "/api/v1/uplinks/enx1", `{"disabled":true}`); code != 200 || body != `{"disabled":true,"name":"enx1"}`+"\n" {
		t.Errorf("PUT /uplinks over the socket: %d %q", code, body)
	}
	if code, body := do("PUT", "/api/v1/input", `{"source":"usb","device":"/dev/video1"}`); code != 400 {
		t.Errorf("invalid PUT /input: %d %s", code, body)
	}
	if code, body := do("PUT", "/api/v1/input", `{"source":"test"}`); code != 200 || !strings.Contains(body, `"pendingRestart"`) {
		t.Errorf("PUT /input over the socket: %d %s", code, body)
	}
	if code, body := do("GET", "/api/v1/sources", ""); code != 200 || !strings.Contains(body, `"kind":"test"`) {
		t.Errorf("GET /sources over the socket: %d %s", code, body)
	}

	// Apply: only restart. start and stop stay out
	e.unit.mu.Lock()
	e.unit.actions = nil
	e.unit.mu.Unlock()
	if code, body := do("POST", "/api/v1/service/restart", "{}"); code != 202 || !strings.Contains(body, `"state":"active"`) {
		t.Errorf("restart over the socket: %d %s", code, body)
	}
	for _, a := range []string{"start", "stop"} {
		if code, body := do("POST", "/api/v1/service/"+a, "{}"); code != 403 || !strings.Contains(body, "local_forbidden") {
			t.Errorf("%s over the socket: %d %s", a, code, body)
		}
	}
	e.unit.mu.Lock()
	if strings.Join(e.unit.actions, ",") != "restart" {
		t.Errorf("unit actions: %v", e.unit.actions)
	}
	e.unit.mu.Unlock()

	// Network cameras: add, credentials with ?cameras=1, new key, rename, remove
	code, body = do("POST", "/api/v1/cameras", `{"name":"Móvil de Ana"}`)
	var cr struct {
		Connect struct {
			RTMPKey string `json:"rtmpKey"`
		} `json:"connect"`
	}
	if json.Unmarshal([]byte(body), &cr); code != 201 || cr.Connect.RTMPKey == "" {
		t.Fatalf("POST /cameras over the socket: %d %s", code, body)
	}
	camKey := cr.Connect.RTMPKey
	creds := func(q string) (int, string, network.Credentials) {
		t.Helper()
		code, body := do("GET", "/api/v1/network/credentials"+q, "")
		var c network.Credentials
		json.Unmarshal([]byte(body), &c)
		return code, body, c
	}
	if code, body, c := creds(""); code != 200 || c.WiFi == nil || c.WiFi.Password == "" || strings.Contains(body, "cameras") || strings.Contains(body, camKey) {
		t.Errorf("credentials without ?cameras: %d %s", code, body)
	}
	code, body, c := creds("?cameras=1")
	if code != 200 || c.WiFi == nil || len(c.Cameras) != 1 || c.Cameras[0].ID != "cam1" || c.Cameras[0].RTMPKey != camKey ||
		!strings.Contains(c.Cameras[0].SRT, "srt://10.42.0.1:") || c.WiFi.QRRows != nil {
		t.Errorf("credentials with ?cameras=1: %d %s", code, body)
	}
	wifiPass := c.WiFi.Password
	if code, body, c := creds("?cameras=1&qr=rows"); code != 200 || len(c.WiFi.QRRows) < 21 || len(c.Cameras) != 1 {
		t.Errorf("credentials with ?cameras=1&qr=rows: %d %s", code, body)
	}
	for _, q := range []string{"?cameras=yes", "?cameras=0", "?cameras=1&qr=svg"} {
		if code, _, _ := creds(q); code != 400 {
			t.Errorf("credentials%s: %d", q, code)
		}
	}
	if code, body := do("PATCH", "/api/v1/cameras/cam1", `{"rotateKey":true}`); code != 200 || strings.Contains(body, camKey) || !strings.Contains(body, `"rtmpKey":"cam1?`) {
		t.Errorf("new key over the socket: %d %s", code, body)
	}
	if _, body, c := creds("?cameras=1"); len(c.Cameras) != 1 || c.Cameras[0].RTMPKey == camKey {
		t.Errorf("credentials after the new key: %s", body)
	}
	if code, body := do("PATCH", "/api/v1/cameras/cam1", `{"name":"Cámara 2"}`); code != 200 || !strings.Contains(body, `"name":"Cámara 2"`) {
		t.Errorf("rename over the socket: %d %s", code, body)
	}
	if code, body := do("PATCH", "/api/v1/cameras/cam1", `{"name":""}`); code != 400 {
		t.Errorf("invalid name over the socket: %d %s", code, body)
	}

	// The status, the same as the web's SSE: the uplinks and the saved input, no secret
	code, body = do("GET", "/api/v1/status", "")
	if code != 200 || !strings.Contains(body, `"name":"enx1"`) || !strings.Contains(body, `"source":"test"`) ||
		!strings.Contains(body, `"pendingRestart"`) || !strings.Contains(body, `"transport":"srt"`) {
		t.Errorf("GET /status over the socket: %d %s", code, body)
	}
	for _, secret := range []string{key, "otra-clave", pass, wifiPass, camKey} {
		if strings.Contains(body, secret) {
			t.Errorf("GET /status carries a secret (%s): %s", secret, body)
		}
	}
	if code, body := do("DELETE", "/api/v1/cameras/cam1", ""); code != 204 || body != "" {
		t.Errorf("DELETE /cameras over the socket: %d %s", code, body)
	}
	if code, _ := do("DELETE", "/api/v1/cameras/cam1", ""); code != 404 {
		t.Errorf("DELETE an unknown camera: %d", code)
	}

	// Unlinking from the screen
	if code, body := do("DELETE", "/api/v1/cloud", ""); code != 200 || !strings.Contains(body, `"state":"unlinked"`) ||
		!strings.Contains(body, `"cloudNotified":false`) || fc.unlinks != 1 {
		t.Errorf("DELETE /cloud over the socket: %d %s", code, body)
	}

	// The password: the same validation and errors as the web; a change closes EVERY web session
	if code, body := do("PUT", "/api/v1/password", `{"current":"wrong-one-123","new":"pantalla-456"}`); code != 403 || !strings.Contains(body, `"error":"bad_password"`) {
		t.Errorf("wrong current password: %d %s", code, body)
	}
	if code, body := do("PUT", "/api/v1/password", `{"current":"correcto-123","new":"short"}`); code != 400 || !strings.Contains(body, `"error":"validation"`) {
		t.Errorf("short new password: %d %s", code, body)
	}
	// Even with a web session's cookie over the socket, none is kept
	if code, body := do("PUT", "/api/v1/password", `{"current":"correcto-123","new":"pantalla-456"}`, "Cookie", cookieName+"="+tok); code != 204 || body != "" {
		t.Fatalf("password change over the socket: %d %s", code, body)
	}
	if w := e.do("GET", "/api/v1/session", "", tok); w.Code != 401 {
		t.Errorf("the web session survived the screen's change: %d", w.Code)
	}
	login := e.do("POST", "/api/v1/login", `{"password":"pantalla-456"}`, "")
	if login.Code != 204 || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login with the new password: %d %s", login.Code, login.Body)
	}
	tok = login.Result().Cookies()[0].Value
	// Attempts at the current password: 5 per minute for the screen (a right one clears them), then 429
	for i := 1; i <= 6; i++ {
		code, body := do("PUT", "/api/v1/password", `{"current":"wrong-one-123","new":"pantalla-789"}`)
		if want := map[bool]int{true: 403, false: 429}[i <= 5]; code != want {
			t.Errorf("wrong attempt %d: %d %s, want %d", i, code, body, want)
		}
	}
	// The web's attempts are counted apart (per IP)
	origin := []string{"Origin", "http://example.com", "Host", "example.com"}
	if w := e.do("PUT", "/api/v1/password", `{"current":"wrong-one-123","new":"pantalla-789"}`, tok, origin...); w.Code != 403 {
		t.Errorf("web, wrong current password after the screen's limit: %d %s", w.Code, w.Body)
	}
}
