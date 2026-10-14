package appapi

import (
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// want is the queued change (tests call put/post themselves, as Python's do with n._want).
func (n *NetState) wanted(t *testing.T) Change {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.want == nil {
		t.Fatal("no change queued")
	}
	return *n.want
}

// AppApiTest.test_netstate
func TestNetstate(t *testing.T) {
	a := newFakeApp(t)
	n := New(nil, a.sock)
	n.PollStatus()
	if s := n.Snapshot(); s.API != APIUp || s.Status == nil || !s.Status.Enabled {
		t.Fatalf("%+v", s)
	}
	n.SetPanel(true)
	n.PollWiFi()
	if w := n.Snapshot().WiFi; w == nil || w.SSID != "EnhancedIRL-A1B2" {
		t.Fatalf("wifi %+v", w)
	}
	n.SetPanel(false) // closed: the password out of memory
	if n.Snapshot().WiFi != nil {
		t.Fatal("wifi kept with the panel closed")
	}
	// The change: 409 → the app's message; and one at a time
	if !n.SetEnabled(false) || n.SetEnabled(true) {
		t.Fatal("one change at a time")
	}
	n.put(n.wanted(t))
	s := n.Snapshot()
	if s.Pending != nil || s.Message.Text != "hay una cámara de red al aire" {
		t.Fatalf("%+v %+v", s.Pending, s.Message)
	}
	// 202: saved but not applied, with the reason; the new Status stays
	a.route("PUT /network", 202, map[string]any{"enabled": false, "accessPoint": map[string]any{"error": "ap0 no sube"},
		"lan": map[string]any{}, "cameras": map[string]any{"items": []any{}}})
	n.SetEnabled(false)
	n.put(Change{What: "net", On: false})
	s = n.Snapshot()
	if s.Status.Enabled || !strings.Contains(s.Message.Text, "ap0 no sube") {
		t.Fatalf("%+v %q", s.Status, s.Message.Text)
	}
	// «Crear mi wifi»: PUT /network/wifi {"ap"}; what it returns (GET /network/wifi) goes into accessPoint at once
	a.route("PUT /network/wifi", 200, map[string]any{"client": nil, "ap": map[string]any{"policy": "rescue", "state": "off",
		"ssid": nil, "channel": nil, "clients": 0}, "warning": nil})
	n.mu.Lock()
	st := *n.status
	st.AccessPoint = AccessPoint{Enabled: true, Policy: "always", State: "error", Error: "viejo"}
	n.status = &st
	n.mu.Unlock()
	if !n.SetAP(false) {
		t.Fatal("SetAP")
	}
	if p := n.Snapshot().Pending; p == nil || *p != (Change{What: "ap", Value: "rescue"}) {
		t.Fatalf("pending %+v", p)
	}
	if n.SetEnabled(true) { // one at a time, also across switches
		t.Fatal("two changes at once")
	}
	n.put(n.wanted(t))
	if got := a.last(); !reflect.DeepEqual(got, seenReq{"PUT", "/api/v1/network/wifi", map[string]any{"ap": "rescue"}}) {
		t.Errorf("seen %#v", got)
	}
	ap := n.Snapshot().Status.AccessPoint
	if ap.Policy != "rescue" || ap.State != "off" || !ap.Enabled || ap.Error != "" {
		t.Errorf("ap %+v", ap)
	}
	if n.Snapshot().Pending != nil {
		t.Error("pending kept")
	}
	// 409 on_air turning it off: the app's message as is; 202: the AP's reason
	a.route("PUT /network/wifi", 409, map[string]any{"error": "on_air", "message": "«Móvil» entra por esta wifi"})
	n.SetAP(false)
	n.put(n.wanted(t))
	if m := n.Snapshot().Message.Text; m != "«Móvil» entra por esta wifi" {
		t.Errorf("message %q", m)
	}
	a.route("PUT /network/wifi", 202, map[string]any{"client": nil, "ap": map[string]any{"policy": "always", "state": "error",
		"error": "ap0 no sube", "clients": 0}})
	n.SetAP(true)
	n.put(n.wanted(t))
	if s := n.Snapshot(); !strings.Contains(s.Message.Text, "ap0 no sube") || s.Status.AccessPoint.Policy != "always" {
		t.Errorf("%q %+v", s.Message.Text, s.Status.AccessPoint)
	}
	// No app (a socket that is not there): APIGone (an older app: wifi.json), no state
	gone := New(nil, filepath.Join(a.dir, "no"))
	gone.PollStatus()
	if s := gone.Snapshot(); s.API != APIGone || s.Status != nil {
		t.Errorf("gone %+v", s)
	}
	// A socket that is there but nobody serves (app hung or restarting): «no responde», without wifi.json
	dead := filepath.Join(a.dir, "colgada.sock")
	ls, err := net.Listen("unix", dead)
	if err != nil {
		t.Fatal(err)
	}
	defer ls.Close()
	hung := New(nil, dead)
	hung.lang.set("es") // the device's language: the client's own texts follow it
	hung.getTimeout = 300 * time.Millisecond
	t0 := time.Now()
	hung.PollStatus()
	if time.Since(t0) >= 2*time.Second {
		t.Errorf("took %v", time.Since(t0))
	}
	if s := hung.Snapshot(); s.API != APIUp || s.Status != nil || s.Error != "La app del codificador no responde" {
		t.Errorf("hung %+v", s)
	}
	hung.lang.set("fr")
	if hung.PollStatus(); hung.Snapshot().Error != "L'app de l'encodeur ne répond pas" {
		t.Errorf("hung in French: %q", hung.Snapshot().Error)
	}
	// The goroutine: it asks by itself and tells
	var hits atomic.Int32
	g := New(func() { hits.Add(1) }, a.sock)
	g.Start()
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	g.Stop()
	if hits.Load() == 0 || g.Snapshot().API != APIUp {
		t.Errorf("hits %d, api %v", hits.Load(), g.Snapshot().API)
	}
}

// AppApiTest.test_wifi_client
func TestWifiClient(t *testing.T) {
	a := newFakeApp(t)
	n := New(nil, a.sock)
	n.PollStatus()
	n.SetPanel(true)
	// GET /network/wifi: the client validated (what does not fit, out) and the AP into accessPoint
	a.route("GET /network/wifi", 200, map[string]any{
		"client": map[string]any{"enabled": true, "state": "connected", "ssid": "Bar", "signal": 70, "band": "5", "channel": 36,
			"internet": false, "error": "raro"},
		"ap":      map[string]any{"policy": "always", "state": "on", "ssid": "EnhancedIRL-A1B2", "channel": 6, "clients": 1},
		"warning": "two_channels"})
	n.PollWiFiStatus()
	s := n.Snapshot()
	want := &Client{Enabled: true, State: "connected", SSID: "Bar", Signal: intp(70), Band: "5", Internet: boolp(false)}
	if !reflect.DeepEqual(s.Client, want) {
		t.Errorf("client %+v", s.Client)
	}
	if s.Warning != "two_channels" || s.Status.AccessPoint.Clients == nil || *s.Status.AccessPoint.Clients != 1 {
		t.Errorf("warning %q, ap %+v", s.Warning, s.Status.AccessPoint)
	}
	for _, bad := range []string{`{"client": {"state": "raro"}}`, `{"client": null}`, `{"client": "x"}`} {
		if c := clientState(js(t, bad)); c != nil {
			t.Errorf("%s → %+v", bad, c)
		}
	}
	if c := clientState(js(t, `{"client": {"state": "error", "ssid": "a\nb"}}`)); c == nil || c.SSID != "" {
		t.Errorf("ssid with a newline: %+v", c)
	}
	// An app without the route (403 of its whitelist): no client row, and not asked again until reopening
	a.drop("GET /network/wifi")
	n.PollWiFiStatus()
	n.mu.Lock()
	wifiAPI := n.wifiAPI
	n.mu.Unlock()
	if n.Snapshot().Client != nil || wifiAPI != APIGone {
		t.Errorf("client %+v, wifiAPI %v", n.Snapshot().Client, wifiAPI)
	}
	n.SetPanel(true)
	n.mu.Lock()
	wifiAPI = n.wifiAPI
	n.mu.Unlock()
	if wifiAPI != APIUnknown {
		t.Errorf("wifiAPI %v after reopening", wifiAPI)
	}
}

// AppApiTest.test_scan
func TestScan(t *testing.T) {
	a := newFakeApp(t)
	n := New(nil, a.sock)
	n.PollStatus()
	n.SetPanel(true)
	n.SetScan(true)
	a.route("GET /network/wifi/scan", 503, map[string]any{"error": "scan_busy", "message": "buscando"})
	n.PollScan()
	if s := n.Snapshot(); s.Scan != nil || !s.ScanBusy || s.ScanError != "" { // «Buscando redes…»
		t.Fatalf("%+v", s)
	}
	nets := []any{
		map[string]any{"ssid": "A", "signal": 80, "security": "wpa2", "band": "5", "channel": 36, "known": true, "connected": true},
		map[string]any{"ssid": "", "signal": 50, "security": "wpa2"},
		map[string]any{"ssid": "B", "signal": 999, "security": "open"},
		map[string]any{"ssid": "C", "security": "raro"},
		map[string]any{"ssid": strings.Repeat("x", 33), "security": "open"}}
	a.route("GET /network/wifi/scan", 200, map[string]any{"networks": nets, "at": "2026-10-13T19:00:00Z"})
	n.PollScan()
	s := n.Snapshot()
	if len(s.Scan) != 2 || s.Scan[0].SSID != "A" || s.Scan[1].SSID != "B" || s.ScanBusy {
		t.Fatalf("%+v", s.Scan)
	}
	if !s.Scan[0].Connected || s.Scan[1].Signal != 0 || s.Scan[1].Band != "" {
		t.Errorf("%+v", s.Scan)
	}
	a.route("GET /network/wifi/scan", 500, map[string]any{"error": "internal", "message": "no va"})
	n.PollScan()
	if s := n.Snapshot(); len(s.Scan) != 2 || s.ScanError != "no va" { // the latest stays
		t.Errorf("%+v %q", s.Scan, s.ScanError)
	}
	n.SetPanel(false) // Settings closed: the list goes
	if n.Snapshot().Scan != nil {
		t.Error("scan kept with the panel closed")
	}
}

// AppApiTest.test_connect_and_forget
func TestConnectAndForget(t *testing.T) {
	a := newFakeApp(t)
	n := New(nil, a.sock)
	n.PollStatus()
	wifi := map[string]any{"client": map[string]any{"enabled": true, "state": "connecting", "ssid": "Bar"},
		"ap": map[string]any{"policy": "always", "state": "on"}}
	a.route("POST /network/wifi/connect", 202, wifi)
	if !n.Connect("Bar", []byte("Secreto-1234")) {
		t.Fatal("connect")
	}
	if n.Connect("Otra", []byte(strings.Repeat("x", 8))) { // one at a time
		t.Fatal("two changes at once")
	}
	s := n.Snapshot()
	if s.Pending == nil || *s.Pending != (Change{What: "connect", Value: "Bar"}) {
		t.Fatalf("pending %+v", s.Pending)
	}
	noLeak(t, "Secreto", s) // the password is not in what the screen reads
	n.post(n.wanted(t))
	if got := a.last(); !reflect.DeepEqual(got, seenReq{"POST", "/api/v1/network/wifi/connect",
		map[string]any{"ssid": "Bar", "password": "Secreto-1234"}}) {
		t.Errorf("seen %#v", got)
	}
	if !n.secret.empty() {
		t.Error("secret kept after sending")
	}
	if s := n.Snapshot(); s.Pending != nil || s.Client == nil || s.Client.State != "connecting" {
		t.Errorf("%+v %+v", s.Pending, s.Client)
	}
	// Known or open: no password (the saved profile)
	n.Connect("Bar", nil)
	n.post(n.wanted(t))
	if got := a.last().body; !reflect.DeepEqual(got, map[string]any{"ssid": "Bar"}) {
		t.Errorf("body %#v", got)
	}
	// An error: the app's message, nothing of the request
	a.route("POST /network/wifi/connect", 409, map[string]any{"error": "uplink_in_use", "message": "la wifi lleva la emisión"})
	n.Connect("Otra", []byte("Secreto-5678"))
	n.post(n.wanted(t))
	s = n.Snapshot()
	if s.Message.Text != "la wifi lleva la emisión" {
		t.Errorf("message %q", s.Message.Text)
	}
	noLeak(t, "Secreto", s)
	if !n.secret.empty() {
		t.Error("secret kept after an error")
	}
	// No app: neither
	gone := New(nil, filepath.Join(a.dir, "no"))
	gone.status = &Status{Enabled: true}
	gone.Connect("Bar", []byte("Secreto-9999"))
	gone.post(gone.wanted(t))
	noLeak(t, "Secreto", gone.Snapshot())
	// Closing Settings (or locking) with a connect still queued: it does not go out, and the password goes
	if !n.Connect("Bar", []byte("Secreto-0000")) {
		t.Fatal("connect")
	}
	n.SetPanel(false)
	if n.want != nil || !n.secret.empty() || n.Snapshot().Pending != nil {
		t.Errorf("want %+v, secret kept %v, pending %+v", n.want, !n.secret.empty(), n.Snapshot().Pending)
	}
	// Forget
	a.route("POST /network/wifi/forget", 204, map[string]any{})
	n.Forget("Bar")
	n.post(n.wanted(t))
	if got := a.last(); !reflect.DeepEqual(got, seenReq{"POST", "/api/v1/network/wifi/forget", map[string]any{"ssid": "Bar"}}) {
		t.Errorf("seen %#v", got)
	}
	if !strings.Contains(n.Snapshot().Message.Text, "forgotten") { // English until the app says the device's
		t.Errorf("message %q", n.Snapshot().Message.Text)
	}
	// The client: PUT /network/wifi {"client"}
	off := map[string]any{"client": map[string]any{"enabled": false, "state": "off"}, "ap": wifi["ap"]}
	a.route("PUT /network/wifi", 200, off)
	n.SetClient(false)
	n.put(n.wanted(t))
	if got := a.last(); !reflect.DeepEqual(got, seenReq{"PUT", "/api/v1/network/wifi", map[string]any{"client": false}}) {
		t.Errorf("seen %#v", got)
	}
	if c := n.Snapshot().Client; c == nil || c.State != "off" {
		t.Errorf("client %+v", c)
	}
}

// AppApiTest.test_review_fixes
func TestReviewFixes(t *testing.T) {
	// Odd data from the app: what does not fit stays out (drawing cannot fail on it)
	if ap := apFields(js(t, `{"clients": "2", "ssid": 5, "channel": true, "state": "raro", "policy": "always", "error": "x\ny"}`)); !reflect.DeepEqual(ap, AccessPoint{Policy: "always"}) {
		t.Errorf("ap %+v", ap)
	}
	if networkStatus(js(t, `{"enabled": "sí"}`)) != nil {
		t.Error("enabled must be a bool")
	}
	st := networkStatus(js(t, `{"enabled": true, "accessPoint": [1], "lan": null, "cameras": {"items": ["x", {"id": "c"}]}}`))
	if st == nil || st.AccessPoint != (AccessPoint{}) || st.LAN != (LAN{}) || !reflect.DeepEqual(st.Cameras.Items, []Camera{{ID: "c"}}) {
		t.Errorf("%+v", st)
	}
	if textOK("a\xed\xa0\x80b", 32) { // a lone surrogate (WTF-8): Cairo would fail
		t.Error("textOK took a lone surrogate")
	}
	a := newFakeApp(t)
	n := New(nil, a.sock)
	n.PollStatus()
	n.SetPanel(true)
	a.route("GET /network/wifi", 200, map[string]any{"client": map[string]any{"enabled": true, "state": "connected", "ssid": "Bar"},
		"ap": map[string]any{"clients": "muchos", "ssid": nil}})
	n.PollWiFiStatus()
	if c := n.Snapshot().Status.AccessPoint.Clients; c != nil {
		t.Errorf("clients %d", *c)
	}
	// A passing error (5xx, or no answer) keeps the client row; without wlan0 (503 wifi_unavailable), not
	a.route("GET /network/wifi", 500, map[string]any{"error": "internal", "message": "x"})
	n.PollWiFiStatus()
	if c := n.Snapshot().Client; c == nil || c.SSID != "Bar" {
		t.Errorf("client %+v", c)
	}
	a.route("GET /network/wifi", 503, map[string]any{"error": "wifi_unavailable", "message": "x"})
	n.PollWiFiStatus()
	if n.Snapshot().Client != nil {
		t.Error("client kept without wlan0")
	}
	// The app goes (no socket): the client of before does not stay either (the antenna)
	n.client = &Client{Enabled: true, State: "connected"}
	n.sock = filepath.Join(a.dir, "no")
	n.PollStatus()
	if s := n.Snapshot(); s.API != APIGone || s.Client != nil {
		t.Errorf("%+v", s)
	}
	// An unexpected failure does not leave the goroutine dead nor a change «in progress» forever (nor the password)
	var logs syncBuf
	elog.SetOutput(&logs)
	defer elog.SetOutput(nil)
	m := New(nil, a.sock)
	m.PollStatus()
	m.Connect("Bar", []byte("Secreto-1234"))
	var boom atomic.Int32
	m.postFn = func(Change) {
		boom.Add(1)
		panic("x Secreto-1234") // even a panic carrying it: only its type is logged
	}
	m.Start()
	defer m.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for (boom.Load() == 0 || m.Snapshot().Pending != nil) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case <-m.done:
		t.Fatal("the goroutine died")
	default:
	}
	m.mu.Lock()
	secretKept := !m.secret.empty()
	m.mu.Unlock()
	if m.Snapshot().Pending != nil || secretKept {
		t.Errorf("pending %+v, secret kept %v", m.Snapshot().Pending, secretKept)
	}
	if got := logs.String(); got != "[screen] event=app_api_panic type=string\n" {
		t.Errorf("log %q", got)
	}
}

// Renaming «Crear mi wifi»: PUT /network/ap {"ssid"} (it answers like GET /network, which becomes the state), a
// notice in the device language that says what to do now, and the app's refusal as is
func TestSetSSID(t *testing.T) {
	a := newFakeApp(t)
	n := New(nil, a.sock)
	n.lang.set("es")
	n.PollStatus()
	netWith := func(state, ssid string) map[string]any {
		return map[string]any{"enabled": true, "accessPoint": map[string]any{"enabled": true, "policy": "always",
			"state": state, "ssid": ssid, "name": ssid, "channel": 6, "clients": 1}, "lan": map[string]any{},
			"cameras": map[string]any{"max": 1, "items": []any{}}}
	}
	a.route("PUT /network/ap", 200, netWith("on", "Studio 2"))
	if !n.SetSSID("Studio 2") || n.SetSSID("Otra") {
		t.Fatal("one change at a time")
	}
	if p := n.Snapshot().Pending; p == nil || *p != (Change{What: "ssid", Value: "Studio 2"}) {
		t.Fatalf("pending %+v", p)
	}
	n.put(n.wanted(t))
	if got := a.last(); !reflect.DeepEqual(got, seenReq{"PUT", "/api/v1/network/ap", map[string]any{"ssid": "Studio 2"}}) {
		t.Errorf("seen %#v", got)
	}
	s := n.Snapshot()
	if s.Pending != nil || s.Status.AccessPoint.SSID != "Studio 2" ||
		s.Message.Text != "Wifi renombrada: une de nuevo las cámaras y los móviles a «Studio 2»" {
		t.Fatalf("%+v %q", s.Status.AccessPoint, s.Message.Text)
	}
	// With the AP off it is only saved
	a.route("PUT /network/ap", 200, netWith("off", ""))
	n.SetSSID("Studio 3")
	n.put(n.wanted(t))
	if m := n.Snapshot().Message.Text; m != "Guardado: la wifi se llamará «Studio 3» al encenderla" {
		t.Errorf("off: %q", m)
	}
	// A camera on air over that Wi-Fi: the app's 409 as is; 400: its field text
	a.route("PUT /network/ap", 409, map[string]any{"error": "on_air", "message": "«Móvil» entra por esta wifi"})
	n.SetSSID("Studio 4")
	n.put(n.wanted(t))
	if m := n.Snapshot().Message.Text; m != "«Móvil» entra por esta wifi" {
		t.Errorf("on air: %q", m)
	}
	// 202: saved, not applied, with the AP's reason
	nw := netWith("error", "Studio 5")
	nw["accessPoint"].(map[string]any)["error"] = "ap0 no sube"
	a.route("PUT /network/ap", 202, nw)
	n.SetSSID("Studio 5")
	n.put(n.wanted(t))
	if m := n.Snapshot().Message.Text; !strings.Contains(m, "ap0 no sube") {
		t.Errorf("202: %q", m)
	}
}
