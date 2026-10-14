package network

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
)

func testManager(t *testing.T) (*Manager, *int) {
	t.Helper()
	d := t.TempDir()
	applies := 0
	m := &Manager{Dir: d, StatePath: filepath.Join(d, "state.json"), HardwareID: "0123456789abcdef", Max: 1,
		Apply:   func(context.Context) error { applies++; return nil },
		Write:   func(p string, b []byte, mode os.FileMode) error { return config.WriteAtomic(p, b, mode) },
		Clients: func(context.Context) (int, bool) { return 1, true },
		LANAddr: func() string { return "192.168.1.232" }}
	return m, &applies
}

func mode(t *testing.T, p string) os.FileMode {
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestCreateUpdateDelete(t *testing.T) {
	m, applies := testManager(t)
	ctx := context.Background()
	if _, err := m.CreateCamera(ctx, "<b>"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid name: %v", err)
	}
	c, err := m.CreateCamera(ctx, "Ana's phone")
	if err != nil || c.Camera.ID != "cam1" || !strings.HasPrefix(c.Connect.SRT, "srt://10.42.0.1:8890?streamid=publish:cam1:cam1:") {
		t.Fatalf("create: %v %+v", err, c)
	}
	if *applies != 1 {
		t.Fatalf("applies: %d", *applies)
	}
	// Keys and password, only for the app's user; the keyless list, for the group
	for f, want := range map[string]os.FileMode{netcfg.FileCameras: 0o600, netcfg.FilePSK: 0o600, netcfg.FileNetwork: 0o640, netcfg.FilePublic: 0o640} {
		if got := mode(t, filepath.Join(m.Dir, f)); got != want {
			t.Errorf("%s: %o, expected %o", f, got, want)
		}
	}
	pub, _ := os.ReadFile(filepath.Join(m.Dir, netcfg.FilePublic))
	if strings.Contains(string(pub), "key") {
		t.Error("the public list carries keys")
	}
	if _, err := m.CreateCamera(ctx, "Other"); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit of 1: %v", err)
	}
	cr, _ := m.Credentials()
	if cr.WiFi == nil || cr.WiFi.SSID != "EnhancedIRL-CDEF" || len(cr.WiFi.Password) != netcfg.PSKLen || len(cr.Cameras) != 1 {
		t.Fatalf("credentials: %+v", cr)
	}
	old := cr.Cameras[0].SRT
	name := "Camera 2 (Zoë)"
	u, err := m.UpdateCamera(ctx, "cam1", &name, false)
	if err != nil || u.Camera.Name != name || u.Connect.SRT != old || *applies != 1 {
		t.Fatalf("renaming neither changes the key nor applies: %v %+v (%d)", err, u, *applies)
	}
	u, _ = m.UpdateCamera(ctx, "cam1", nil, true)
	if u.Connect.SRT == old || *applies != 2 {
		t.Fatal("changing the key")
	}
	if _, err := m.UpdateCamera(ctx, "cam2", &name, false); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown: %v", err)
	}
	if err := m.DeleteCamera(ctx, "cam1"); err != nil || m.HasCameras() || *applies != 3 {
		t.Fatalf("delete: %v", err)
	}
}

// The access point's name and password from the app's first start: create-only, idempotent, never over a file that is
// there (valid or not), and nothing is applied
func TestEnsureNetwork(t *testing.T) {
	m, applies := testManager(t)
	created, err := m.EnsureNetwork()
	if err != nil || !created || *applies != 0 {
		t.Fatalf("first start: %v %v %d", created, err, *applies)
	}
	n := m.network()
	if n.AP.SSID != "EnhancedIRL-CDEF" || n.AP.Channel != "auto" || m.APPolicy() != netcfg.APRescue {
		t.Fatalf("generated network: %+v (%s)", n, m.APPolicy())
	}
	for f, want := range map[string]os.FileMode{netcfg.FilePSK: 0o600, netcfg.FileNetwork: 0o640} {
		if got := mode(t, filepath.Join(m.Dir, f)); got != want {
			t.Errorf("%s: %o, expected %o", f, got, want)
		}
	}
	cr, _ := m.Credentials()
	if cr.WiFi == nil || cr.WiFi.SSID != "EnhancedIRL-CDEF" || len(cr.WiFi.Password) != netcfg.PSKLen {
		t.Fatalf("credentials from the first start: %+v", cr.WiFi)
	}
	// Again: nothing changes (another hardwareId would give another name: the saved one stays)
	before, _ := os.ReadFile(filepath.Join(m.Dir, netcfg.FileNetwork))
	psk, _ := os.ReadFile(filepath.Join(m.Dir, netcfg.FilePSK))
	m.HardwareID = "ffffffffffff9999"
	if created, err := m.EnsureNetwork(); created || err != nil {
		t.Fatalf("second start: %v %v", created, err)
	}
	after, _ := os.ReadFile(filepath.Join(m.Dir, netcfg.FileNetwork))
	psk2, _ := os.ReadFile(filepath.Join(m.Dir, netcfg.FilePSK))
	if string(before) != string(after) || string(psk) != string(psk2) {
		t.Fatal("the second start rewrote the network")
	}
	// A name the user chose is kept
	name := "Studio A"
	if _, err := m.PutAP(context.Background(), &name, nil, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	m.EnsureNetwork()
	if m.SSID() != "Studio A" {
		t.Fatalf("the start replaced the chosen name: %q", m.SSID())
	}
	// A file another version wrote (ParseNetwork rejects it): left alone at start…
	invalid := `{"v":1,"ap":{"ssid":"Studio B","channel":"auto"},"future":{}}` + "\n"
	os.WriteFile(filepath.Join(m.Dir, netcfg.FileNetwork), []byte(invalid), 0o640)
	if created, err := m.EnsureNetwork(); created || err != nil {
		t.Fatalf("invalid file: %v %v", created, err)
	}
	if b, _ := os.ReadFile(filepath.Join(m.Dir, netcfg.FileNetwork)); string(b) != invalid {
		t.Fatalf("the start rewrote an invalid file: %s", b)
	}
	// …and the next network change rebuilds it with its name, not the default one
	ch := "6"
	if _, err := m.PutAP(context.Background(), nil, &ch, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	if n := m.network(); n.AP.SSID != "Studio B" || n.AP.Channel != "6" {
		t.Fatalf("rebuilt without its name: %+v", n.AP)
	}
}

func TestPutAP(t *testing.T) {
	m, applies := testManager(t)
	ctx := context.Background()
	bad := "a;b"
	if _, err := m.PutAP(ctx, &bad, nil, nil, false, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid SSID")
	}
	ssid, ch := "My network", "6"
	if _, err := m.PutAP(ctx, &ssid, &ch, nil, false, nil); err != nil || *applies != 0 {
		t.Fatalf("without cameras it does not apply: %v %d", err, *applies)
	}
	cr, _ := m.Credentials()
	if cr.WiFi.SSID != "My network" {
		t.Fatalf("SSID: %+v", cr.WiFi)
	}
	pw := cr.WiFi.Password
	m.CreateCamera(ctx, "Phone")
	if _, err := m.PutAP(ctx, nil, nil, nil, true, nil); err != nil {
		t.Fatal(err)
	}
	cr, _ = m.Credentials()
	if cr.WiFi.Password == pw || *applies != 2 {
		t.Fatal("new password applied")
	}
}

// Renaming the AP: the check runs (with the lock held) only when the name changes; its refusal changes nothing. The
// new name reaches the status (name, and ssid once eirl-netsetup brings it up with it), the credentials, the QR and the
// screen's file
func TestRenameAP(t *testing.T) {
	m, applies := testManager(t)
	m.ScreenFile = filepath.Join(m.Dir, "wifi.json")
	ctx := context.Background()
	if st := m.Status(ctx); st.AccessPoint.Name != nil {
		t.Fatalf("name before network.json exists: %q", *st.AccessPoint.Name)
	}
	m.EnsureNetwork()
	if st := m.Status(ctx); st.AccessPoint.Name == nil || *st.AccessPoint.Name != "EnhancedIRL-CDEF" || st.AccessPoint.SSID != nil {
		t.Fatalf("generated name with the AP off: %+v", st.AccessPoint)
	}
	always := netcfg.APAlways
	if _, err := m.PutWiFi(ctx, WiFiChange{AP: &always}, nil); err != nil {
		t.Fatal(err)
	}
	m.Apply = func(context.Context) error {
		*applies++
		os.WriteFile(m.StatePath, []byte(`{"state":"on","mode":"normal","ssid":"`+m.network().AP.SSID+`","channel":6}`), 0o644)
		return nil
	}
	calls := 0
	onAir := errors.New("on air")
	refuse := func() error { calls++; return onAir }
	name := "Studio A"
	n0 := *applies
	if _, err := m.PutAP(ctx, &name, nil, nil, false, refuse); err != onAir || calls != 1 || *applies != n0 {
		t.Fatalf("refused rename: %v, %d calls, %d applies", err, calls, *applies-n0)
	}
	if m.SSID() != "EnhancedIRL-CDEF" {
		t.Fatalf("a refused rename changed the name: %q", m.SSID())
	}
	// The same name, the channel or the password: no check
	same, ch := "EnhancedIRL-CDEF", "6"
	if _, err := m.PutAP(ctx, &same, &ch, nil, false, refuse); err != nil || calls != 1 {
		t.Fatalf("same name: %v %d", err, calls)
	}
	ok := func() error { calls++; return nil }
	ap, err := m.PutAP(ctx, &name, nil, nil, false, ok)
	if err != nil || calls != 2 || ap.SSID == nil || *ap.SSID != name || ap.Name == nil || *ap.Name != name {
		t.Fatalf("rename: %v %d %+v", err, calls, ap)
	}
	cr, _ := m.Credentials()
	if cr.WiFi == nil || cr.WiFi.SSID != name || !strings.Contains(cr.WiFi.QR, "S:Studio A;") {
		t.Fatalf("credentials after the rename: %+v", cr.WiFi)
	}
	var scr ScreenWiFi
	b, _ := os.ReadFile(m.ScreenFile)
	if json.Unmarshal(b, &scr) != nil || scr.SSID != name || !scr.On {
		t.Fatalf("the screen's file after the rename: %s", b)
	}
}

// Wired cameras (§6.11): the switch, its state, the wired URLs and that both are never off
func TestWired(t *testing.T) {
	m, applies := testManager(t)
	m.ScreenFile = filepath.Join(m.Dir, "wifi.json")
	ctx := context.Background()
	st := m.Status(ctx)
	if !st.AccessPoint.Enabled || st.LAN.Enabled || st.LAN.State != "off" || st.LAN.Iface != "eth0" || st.LAN.IP == nil || *st.LAN.IP != "192.168.1.232" {
		t.Fatalf("by default, wifi yes and cable no: %+v", st)
	}
	// Without cameras, it is only saved
	if _, err := m.PutLAN(ctx, true); err != nil || *applies != 0 {
		t.Fatalf("cable without cameras: %v %d", err, *applies)
	}
	c, err := m.CreateCamera(ctx, "PTZ camera")
	if err != nil || c.Connect.LAN == nil || c.Connect.LAN.SRT != "srt://192.168.1.232:8890?streamid=publish:cam1:cam1:"+strings.TrimPrefix(c.Connect.RTMPKey, "cam1?user=cam1&pass=") {
		t.Fatalf("create with the cable: %v %+v", err, c.Connect.LAN)
	}
	os.WriteFile(m.StatePath, []byte(`{"state":"on","mode":"cameras","ssid":"EnhancedIRL-CDEF","channel":6,"lan":"on"}`), 0o644)
	if st := m.Status(ctx); !st.LAN.Enabled || st.LAN.State != "on" {
		t.Fatalf("cable on: %+v", st.LAN)
	}
	// Cable only: the cameras no longer come in over the wifi, but the AP (and the screen's «Wifi» button) stay: «over
	// wifi» belongs to the reception (WIFI.md W3)
	off := false
	if _, err := m.PutAP(ctx, nil, nil, &off, false, nil); err != nil || *applies != 2 {
		t.Fatalf("switching «over wifi» off: %v %d", err, *applies)
	}
	if _, err := os.Stat(m.ScreenFile); err != nil {
		t.Error("with «over wifi» off, the screen loses the «Wifi» button")
	}
	if st := m.Status(ctx); st.AccessPoint.Enabled || st.AccessPoint.Policy != netcfg.APAlways {
		t.Errorf("status: %+v", st.AccessPoint)
	}
	// Both off: no
	if _, err := m.PutLAN(ctx, false); !errors.Is(err, ErrNoInput) {
		t.Fatalf("switching the cable off without wifi: %v", err)
	}
	on := true
	m.PutAP(ctx, nil, nil, &on, false, nil)
	if _, err := m.PutLAN(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutAP(ctx, nil, nil, &off, false, nil); !errors.Is(err, ErrNoInput) {
		t.Fatalf("switching the wifi off without cable: %v", err)
	}
	cr, _ := m.Credentials()
	if cr.Cameras[0].LAN != nil {
		t.Error("with the cable off, no wired URL")
	}
	// With the cable on but eth0 without an IP, neither
	m.PutLAN(ctx, true)
	m.LANAddr = func() string { return "" }
	if cr, _ := m.Credentials(); cr.Cameras[0].LAN != nil || m.Status(ctx).LAN.IP != nil {
		t.Error("eth0 without an IP")
	}
	os.WriteFile(m.StatePath, []byte(`{"state":"on","lan":"error","lanError":"could not set up the wired cameras firewall"}`), 0o644)
	if l := m.Status(ctx).LAN; l.State != "error" || l.Error == "" {
		t.Fatalf("cable in error: %+v", l)
	}
}

func TestApplyError(t *testing.T) {
	m, _ := testManager(t)
	m.Apply = func(context.Context) error { return errors.New("no wifi (wlan0)") }
	_, err := m.CreateCamera(context.Background(), "Phone")
	if !errors.Is(err, ErrApply) || !m.HasCameras() {
		t.Fatalf("the camera is saved even if the AP fails: %v", err)
	}
}

func TestStatusFromMediaMTX(t *testing.T) {
	m, _ := testManager(t)
	m.CreateCamera(context.Background(), "Phone")
	os.WriteFile(m.StatePath, []byte(`{"state":"on","mode":"cameras","ssid":"EnhancedIRL-CDEF","channel":6}`), 0o644)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/paths/list":
			w.Write([]byte(`{"items":[{"name":"cam1","ready":true,"source":{"type":"srtConn","id":"d50c2c99"},"tracks":["H265","MPEG-4 Audio"],"bytesReceived":1000000},
				{"name":"other","ready":true,"source":{"type":"srtConn","id":"x"},"tracks":["H264"]}]}`))
		case "/v3/srtconns/get/d50c2c99":
			w.Write([]byte(`{"remoteAddr":"10.42.0.74:50867","msRTT":6.66,"packetsReceived":990,"packetsReceivedLoss":10,"query":"user=cam1&pass=SECRET"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m.MediaAPI = srv.URL
	t0 := time.Now()
	m.Now = func() time.Time { return t0 }
	st := m.Status(context.Background())
	m.Now = func() time.Time { return t0.Add(time.Second) }
	st = m.Status(context.Background())
	if st.AccessPoint.State != "on" || *st.AccessPoint.Channel != 6 || st.AccessPoint.Clients != 1 || st.Cameras.Max != 1 {
		t.Fatalf("AP: %+v", st.AccessPoint)
	}
	if len(st.Cameras.Items) != 1 {
		t.Fatalf("cameras: %+v", st.Cameras)
	}
	c := st.Cameras.Items[0]
	if !c.Connected || *c.Protocol != "srt" || *c.Codec != "h265" || *c.Remote != "10.42.0.74" || *c.RTTMs != 6.7 || *c.LossPct != 1 || c.Kbps == nil || *c.Kbps != 0 {
		t.Fatalf("camera: %+v", c)
	}
	if c.Via == nil || *c.Via != "wifi" {
		t.Fatalf("over the wifi: %v", c.Via)
	}
	// With the wifi off, a sender from 10.42.0.0/24 comes in over the cable (eth0 on that network)
	os.WriteFile(m.StatePath, []byte(`{"state":"off","lan":"on"}`), 0o644)
	if c := m.Status(context.Background()).Cameras.Items[0]; c.Via == nil || *c.Via != "lan" {
		t.Fatalf("over the cable: %v", c.Via)
	}
}

func TestScreenFile(t *testing.T) {
	m, _ := testManager(t)
	m.ScreenFile = filepath.Join(m.Dir, "wifi.json")
	m.CreateCamera(context.Background(), "Phone")
	b, err := os.ReadFile(m.ScreenFile)
	if err != nil || !strings.Contains(string(b), `"qr":["1111111`) || mode(t, m.ScreenFile) != 0o640 || !strings.Contains(string(b), `"on":false`) {
		t.Fatalf("screen file (no state.json: AP off): %v %s", err, b)
	}
	// With the AP on (what eirl-netsetup leaves), the screen knows it
	os.WriteFile(m.StatePath, []byte(`{"state":"on","mode":"cameras","ssid":"EnhancedIRL-CDEF","channel":6}`), 0o644)
	m.Reapply(context.Background())
	if b, _ = os.ReadFile(m.ScreenFile); !strings.Contains(string(b), `"on":true`) {
		t.Fatalf("AP on: %s", b)
	}
	cr, _ := m.Credentials()
	if strings.Contains(string(b), cr.Cameras[0].SRT[len(cr.Cameras[0].SRT)-20:]) {
		t.Error("the screen receives a camera's key")
	}
	m.DeleteCamera(context.Background(), "cam1")
	if _, err := os.Stat(m.ScreenFile); !os.IsNotExist(err) {
		t.Error("no cameras, no file")
	}
}

func TestNewKeyKicksPublisher(t *testing.T) {
	m, _ := testManager(t)
	ctx := context.Background()
	if _, err := m.CreateCamera(ctx, "Phone"); err != nil {
		t.Fatal(err)
	}
	var kicked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v3/srtconns/list":
			w.Write([]byte(`{"items":[{"id":"a1","path":"cam1","state":"publish"},{"id":"b2","path":"cam2","state":"publish"}]}`))
		case r.Method == "GET" && r.URL.Path == "/v3/rtmpconns/list":
			w.Write([]byte(`{"items":[{"id":"../x","path":"cam1"}]}`))
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v3/"):
			kicked = append(kicked, r.URL.Path)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m.MediaAPI = srv.URL
	old := kickDelay
	kickDelay = 0
	defer func() { kickDelay = old }()
	n := "Another name"
	if _, err := m.UpdateCamera(ctx, "cam1", &n, false); err != nil || len(kicked) != 0 {
		t.Fatalf("changing the name kicks nobody: %v %v", err, kicked)
	}
	if _, err := m.UpdateCamera(ctx, "cam1", nil, true); err != nil {
		t.Fatal(err)
	}
	if len(kicked) != 1 || kicked[0] != "/v3/srtconns/kick/a1" {
		t.Fatalf("kicked: %v", kicked)
	}
}

func TestAPIKey(t *testing.T) {
	m, _ := testManager(t)
	ctx := context.Background()
	m.CreateCamera(ctx, "Phone")
	if got := mode(t, filepath.Join(m.Dir, netcfg.FileMediaKey)); got != 0o640 {
		t.Fatalf("media-api.key: %o", got)
	}
	k, ok, _ := netcfg.ParseMediaKey(m.read(netcfg.FileMediaKey))
	if !ok {
		t.Fatal("no API key")
	}
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		auth = append(auth, u+":"+p)
		w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	m.MediaAPI = srv.URL
	m.Status(ctx)
	if len(auth) == 0 || auth[0] != netcfg.APIUser+":"+k {
		t.Fatalf("the API does not carry the key: %v", auth)
	}
	// A second apply does not change it
	m.Reapply(ctx)
	if k2, _, _ := netcfg.ParseMediaKey(m.read(netcfg.FileMediaKey)); k2 != k {
		t.Fatal("the key has changed")
	}
}

func TestRetryOnlyOnError(t *testing.T) {
	m, _ := testManager(t)
	var applies atomic.Int32
	m.Apply = func(context.Context) error { applies.Add(1); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.CreateCamera(ctx, "Phone")
	base := applies.Load()
	os.WriteFile(m.StatePath, []byte(`{"state":"on"}`), 0o644)
	done := make(chan struct{})
	// Before finishing, let Retry end: otherwise it keeps writing into the TempDir while it is being removed
	defer func() { cancel(); <-done }()
	go func() { m.Retry(ctx, 10*time.Millisecond, nil); close(done) }()
	time.Sleep(60 * time.Millisecond)
	if n := applies.Load(); n != base {
		t.Fatalf("with the AP up there is no retry: %d", n-base)
	}
	os.WriteFile(m.StatePath, []byte(`{"state":"error","error":"no wifi (wlan0)"}`), 0o644)
	time.Sleep(60 * time.Millisecond)
	if applies.Load() == base {
		t.Fatal("with the AP in error there was no retry")
	}
	// Also with the cable in error
	os.WriteFile(m.StatePath, []byte(`{"state":"on","lan":"error"}`), 0o644)
	time.Sleep(30 * time.Millisecond)
	base = applies.Load()
	time.Sleep(60 * time.Millisecond)
	if applies.Load() == base {
		t.Fatal("with the cable in error there was no retry")
	}
}

// The reception (§6.12): off, the engine's catalogue without cameras and without querying MediaMTX; the board's wifi
// (and the screen's file, with its «Wifi» button) stays; the cameras and their keys are kept. On, everything comes back
func TestMainSwitch(t *testing.T) {
	m, applies := testManager(t)
	ctx := context.Background()
	m.ScreenFile = filepath.Join(m.Dir, "wifi.json")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	m.MediaAPI = srv.URL
	cr, _ := m.CreateCamera(ctx, "Phone")
	key, _ := m.Camera("cam1")
	pub := func() string { b, _ := os.ReadFile(filepath.Join(m.Dir, netcfg.FilePublic)); return string(b) }
	if !m.Status(ctx).Enabled || !strings.Contains(pub(), `"cam1"`) {
		t.Fatalf("just added: %s", pub())
	}
	n0 := *applies
	st, err := m.PutEnabled(ctx, false, nil)
	if err != nil || st.Enabled || *applies != n0+1 {
		t.Fatalf("switch off: %+v %v (%d)", st, err, *applies)
	}
	if strings.Contains(pub(), "cam1") || mode(t, filepath.Join(m.Dir, netcfg.FilePublic)) != 0o640 {
		t.Errorf("off, the engine still sees the camera: %s", pub())
	}
	if _, err := os.Stat(m.ScreenFile); err != nil {
		t.Error("with the reception off, the screen loses the «Wifi» button (the wifi stays)")
	}
	if c, err := m.Camera("cam1"); err != nil || c.Key != key.Key {
		t.Errorf("the camera or its key were not kept: %+v %v", c, err)
	}
	if b, _ := os.ReadFile(filepath.Join(m.Dir, netcfg.FileNetwork)); !strings.Contains(string(b), `"enabled": false`) {
		t.Errorf("network.json: %s", b)
	}
	// Off: MediaMTX is not queried and the camera shows without a sender; the wifi, up and with its devices. A name
	// change does not give it back to the engine
	hits.Store(0)
	os.WriteFile(m.StatePath, []byte(`{"state":"on","mode":"cameras","ssid":"EnhancedIRL-CDEF","channel":6,"lan":"off"}`), 0o644)
	st = m.Status(ctx)
	if hits.Load() != 0 || len(st.Cameras.Items) != 1 || st.Cameras.Items[0].Connected || st.AccessPoint.State != "on" || st.AccessPoint.Clients != 1 {
		t.Errorf("status off: %+v (MediaMTX %d)", st, hits.Load())
	}
	name := "Zoë's phone"
	m.UpdateCamera(ctx, "cam1", &name, false)
	if strings.Contains(pub(), "cam1") {
		t.Error("renaming with everything off gives the camera back to the engine")
	}
	// Switching on: the engine sees it again (with its new name) and the screen its button
	if st, err := m.PutEnabled(ctx, true, nil); err != nil || !st.Enabled {
		t.Fatalf("switch on: %+v %v", st, err)
	}
	if !strings.Contains(pub(), `"Zoë's phone"`) {
		t.Errorf("on, the engine does not see the camera: %s", pub())
	}
	if _, err := os.Stat(m.ScreenFile); err != nil {
		t.Error("on, without the screen's file")
	}
	// Without wifi or cable there is no way in: it does not switch on
	m.PutEnabled(ctx, false, nil)
	no := false
	m.PutAP(ctx, nil, nil, &no, false, nil)
	if _, err := m.PutEnabled(ctx, true, nil); !errors.Is(err, ErrNoInput) || m.network().On() {
		t.Errorf("switching on without wifi or cable: %v", err)
	}
	yes := true
	m.PutAP(ctx, nil, nil, &yes, false, nil)
	m.PutEnabled(ctx, true, nil)
	if cr2, _ := m.CameraConnect("cam1"); cr2.SRT != cr.Connect.SRT {
		t.Error("on coming back, the camera has another URL")
	}
	// Without cameras, without «Create my wifi» (and with the network already down, as eirl-netsetup leaves it) it is
	// only saved: there is nothing to apply
	m.DeleteCamera(ctx, "cam1")
	rescue := netcfg.APRescue
	m.PutWiFi(ctx, WiFiChange{AP: &rescue}, nil)
	os.WriteFile(m.StatePath, []byte(`{"state":"off","lan":"off"}`), 0o644)
	n0 = *applies
	if _, err := m.PutEnabled(ctx, false, nil); err != nil || *applies != n0 {
		t.Errorf("without cameras: %v (%d applies)", err, *applies-n0)
	}
}

// While the reception is switching off, neither the wifi (which stays up) nor the cable show as «Starting…»
func TestSwitchingOffIsNotStarting(t *testing.T) {
	m, _ := testManager(t)
	ctx := context.Background()
	m.CreateCamera(ctx, "Phone")
	m.PutLAN(ctx, true)
	os.WriteFile(m.StatePath, []byte(`{"state":"on","lan":"on"}`), 0o644)
	m.Apply = func(context.Context) error {
		if ap, lan := m.states(); ap.State == "starting" || lan.State == "starting" {
			t.Errorf("switching off, the state says starting: %s %s", ap.State, lan.State)
		}
		return nil
	}
	m.PutEnabled(ctx, false, nil)
}

// The app's check (on air) runs with the lock held and, if it fails, nothing is touched
func TestSwitchWithCheck(t *testing.T) {
	m, applies := testManager(t)
	ctx := context.Background()
	m.CreateCamera(ctx, "Phone")
	n0 := *applies
	onAir := errors.New("on air")
	if _, err := m.PutEnabled(ctx, false, func() error {
		if m.mu.TryLock() {
			m.mu.Unlock()
			t.Error("the check runs without the lock")
		}
		return onAir
	}); err != onAir {
		t.Fatalf("error: %v", err)
	}
	if !m.Status(ctx).Enabled || *applies != n0 {
		t.Error("with the check failing, it was switched off anyway")
	}
	// Switching on checks nothing
	if _, err := m.PutEnabled(ctx, true, func() error { return onAir }); err != nil {
		t.Errorf("switch on: %v", err)
	}
}

// With the reception off, if the cable is still up (the apply never got to run and its state stayed «on»),
// Retry takes it down; the wifi up is what was requested; and once down, it does not insist
func TestRetryOffWithNetworkUp(t *testing.T) {
	m, _ := testManager(t)
	var applies atomic.Int32
	m.Apply = func(context.Context) error { applies.Add(1); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.CreateCamera(ctx, "Phone")
	m.PutLAN(ctx, true)
	m.PutEnabled(ctx, false, nil)
	os.WriteFile(m.StatePath, []byte(`{"state":"on","lan":"on"}`), 0o644)
	base := applies.Load()
	done := make(chan struct{})
	// Before finishing, let Retry end: otherwise it keeps writing into the TempDir while it is being removed
	defer func() { cancel(); <-done }()
	go func() { m.Retry(ctx, 10*time.Millisecond, nil); close(done) }()
	time.Sleep(60 * time.Millisecond)
	if applies.Load() == base {
		t.Fatal("reception off with the cable up: no retry")
	}
	os.WriteFile(m.StatePath, []byte(`{"state":"on","lan":"off"}`), 0o644)
	time.Sleep(30 * time.Millisecond)
	base = applies.Load()
	time.Sleep(60 * time.Millisecond)
	if applies.Load() != base {
		t.Fatal("off and down, it keeps retrying")
	}
}

// With everything off a camera is not added (it would never get in) and, if the engine's list cannot be
// written, the switch goes back to how it was
func TestSwitchCreateAndFailure(t *testing.T) {
	m, applies := testManager(t)
	ctx := context.Background()
	m.CreateCamera(ctx, "Phone")
	m.PutEnabled(ctx, false, nil)
	if _, err := m.CreateCamera(ctx, "Other"); !errors.Is(err, ErrDisabled) {
		t.Errorf("create with everything off: %v", err)
	}
	write := m.Write
	m.Write = func(p string, b []byte, mode os.FileMode) error {
		if filepath.Base(p) == netcfg.FilePublic {
			return errors.New("disk full")
		}
		return write(p, b, mode)
	}
	n0 := *applies
	if _, err := m.PutEnabled(ctx, true, nil); err == nil || errors.Is(err, ErrApply) {
		t.Fatalf("unable to write the list: %v", err)
	}
	if m.network().On() || *applies != n0 {
		t.Errorf("half-applied: on %v, %d applies", m.network().On(), *applies-n0)
	}
}

// «Create my wifi» (WIFI.md W3 and W7): without cameras, it comes up (with the network and its password generated);
// rescue takes it down; the cameras do not force it
func TestCreateMyWifi(t *testing.T) {
	m, applies := testManager(t)
	ctx := context.Background()
	m.ScreenFile = filepath.Join(m.Dir, "wifi.json")
	if m.APPolicy() != netcfg.APRescue || m.Wanted() {
		t.Fatalf("factory default: %s %v", m.APPolicy(), m.Wanted())
	}
	bad := "sometimes"
	if _, err := m.PutWiFi(ctx, WiFiChange{AP: &bad}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid policy: %v", err)
	}
	always, rescue := netcfg.APAlways, netcfg.APRescue
	st, err := m.PutWiFi(ctx, WiFiChange{AP: &always}, nil)
	if err != nil || st.AP.Policy != netcfg.APAlways || *applies != 1 || !m.Wanted() {
		t.Fatalf("always without cameras: %+v %v (%d)", st, err, *applies)
	}
	if _, ok, _ := netcfg.ParsePSK(m.read(netcfg.FilePSK)); !ok {
		t.Error("without the AP password")
	}
	if _, err := os.Stat(m.ScreenFile); err != nil {
		t.Error("with the AP requested, the screen does not have its «Wifi» button")
	}
	if b, _ := os.ReadFile(filepath.Join(m.Dir, netcfg.FileNetwork)); !strings.Contains(string(b), `"ap": "always"`) {
		t.Errorf("network.json: %s", b)
	}
	// With cameras and rescue, the AP is not requested (W3)
	m.CreateCamera(ctx, "Phone")
	if _, err := m.PutWiFi(ctx, WiFiChange{AP: &rescue}, nil); err != nil || m.APPolicy() != netcfg.APRescue {
		t.Errorf("rescue with cameras: %v %s", err, m.APPolicy())
	}
	if _, err := os.Stat(m.ScreenFile); !os.IsNotExist(err) {
		t.Error("with the AP not requested, the screen still has the «Wifi» button")
	}
	if st := m.Status(ctx); st.AccessPoint.Policy != netcfg.APRescue {
		t.Errorf("GET /network without the policy: %+v", st.AccessPoint)
	}
	// The reception keeps its own: «over wifi» stays on even though the AP is not up
	if !m.network().AP.On() {
		t.Error("rescue has switched «over wifi» off")
	}
}

// On an older board (no «Create my wifi» chosen), switching «over wifi» off no longer switches the AP off: the
// current policy is pinned before changing it (W3)
func TestOverWifiDoesNotSwitchOffAP(t *testing.T) {
	m, _ := testManager(t)
	ctx := context.Background()
	m.ScreenFile = filepath.Join(m.Dir, "wifi.json")
	m.CreateCamera(ctx, "Phone")
	m.PutLAN(ctx, true)
	if m.APPolicy() != netcfg.APAlways {
		t.Fatalf("older board, with a camera and «over wifi»: %s", m.APPolicy())
	}
	off := false
	if _, err := m.PutAP(ctx, nil, nil, &off, false, nil); err != nil {
		t.Fatal(err)
	}
	if m.APPolicy() != netcfg.APAlways || m.network().AP.On() {
		t.Errorf("switching «over wifi» off has switched the AP off: %s %v", m.APPolicy(), m.network().AP.On())
	}
	if _, err := os.Stat(m.ScreenFile); err != nil {
		t.Error("the screen loses the «Wifi» button")
	}
}

// With «Create my wifi» and no cameras, an AP in error is retried
func TestRetryAPWithoutCameras(t *testing.T) {
	m, _ := testManager(t)
	var applies atomic.Int32
	m.Apply = func(context.Context) error { applies.Add(1); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	always := netcfg.APAlways
	m.PutWiFi(ctx, WiFiChange{AP: &always}, nil)
	os.WriteFile(m.StatePath, []byte(`{"state":"error","error":"no wifi (wlan0)"}`), 0o644)
	base := applies.Load()
	done := make(chan struct{})
	// Before finishing, let Retry end: otherwise it keeps writing into the TempDir while it is being removed
	defer func() { cancel(); <-done }()
	go func() { m.Retry(ctx, 10*time.Millisecond, nil); close(done) }()
	// Up to 2 s: with a fixed 60 ms it sometimes failed on macOS (WriteAtomic's fsync and the scheduler)
	for deadline := time.Now().Add(2 * time.Second); applies.Load() == base && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if applies.Load() == base {
		t.Fatal("the AP requested and in error, without cameras, was not retried")
	}
}
