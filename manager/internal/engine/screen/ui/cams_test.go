package ui

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
)

// camKey is a camera's FAKE key in these tests.
const camKey = "ClaveSecreta12345678"

// camsUp is the app's state with one camera (on air over the Wi-Fi) and the cable off (with an address).
func (h *harness) camsUp() *fakeNet {
	net := h.wifiUp()
	st := net.snap.Status
	st.Cameras, st.CamMax = 1, 1
	st.LAN = LANStatus{State: "off", Iface: "eth0", IP: "192.168.1.20"}
	st.CamItems = []NetCamera{{ID: "cam1", Name: "Móvil de Ana", Connected: true, Via: "wifi", Protocol: "srt",
		Codec: "h265", Kbps: ip(4600)}}
	return net
}

// fakeURLs are a camera's FAKE URLs over an address.
func fakeURLs(addr string) CamURLs {
	srt := "srt://" + addr + ":8890?streamid=publish:cam1:cam1:" + camKey
	return CamURLs{SRT: srt, RTMP: "rtmp://" + addr + ":1935/cam1?user=cam1&pass=" + camKey,
		RTMPServer: "rtmp://" + addr + ":1935/", RTMPKey: "cam1?user=cam1&pass=" + camKey, SRTQR: testQR()}
}

// camCreds are the cameras' URLs as cam_creds leaves them.
func camCreds() []CamConnect {
	lan := fakeURLs("192.168.1.20")
	return []CamConnect{{ID: "cam1", Wifi: fakeURLs("10.42.0.1"), LAN: &lan}}
}

// camsPage is the Network cameras page open with one camera; the list's rows as if drawn.
func camsPage(t *testing.T) (*harness, *fakeApp, *fakeNet) {
	h, app := pagesHarness(t, "cams")
	net := h.camsUp()
	h.u.camp.shown = []string{"cam1"}
	return h, app, net
}

// The screen checks a camera's name with the app's rule before sending it.
func TestCamNameOKMatchesTheApp(t *testing.T) {
	for _, s := range []string{"", "a", "Móvil de Ana", "GoPro (casco)", "cam_1.v2", "O'Brien-2", " lead", "trail ",
		"a<b", "x#1", "a&b", "12345678901234567890123456789012", "123456789012345678901234567890123", "a\tb", "Ünïcödé"} {
		if CamNameOK(s) != netcfg.ValidName(s) {
			t.Errorf("%q: screen %v, app %v", s, CamNameOK(s), netcfg.ValidName(s))
		}
	}
	for _, r := range "<>&#\"" {
		if camNameRune(r) {
			t.Errorf("the keyboard types %q", r)
		}
	}
}

// The page's buttons: the reception (with cameras), the two ways in, each camera, «Add camera»; an older app or an
// app that does not list them: only what was there before.
func TestCamsKeys(t *testing.T) {
	h, _, net := camsPage(t)
	keys := h.u.settingsKeys(&net.snap)
	for _, k := range []string{"s_net", "s_cams_wifi", "s_cams_lan", "s_cam_0", "s_cams_add"} {
		if !slices.Contains(keys, k) {
			t.Errorf("no %s in %v", k, keys)
		}
	}
	if slices.Contains(keys, "s_cams_prev") || slices.Contains(keys, "s_back") {
		t.Errorf("pages or ← in the list: %v", keys)
	}
	net.snap.Status.CamItems = nil // an app that does not list them
	keys = h.u.settingsKeys(&net.snap)
	if slices.Contains(keys, "s_cams_wifi") || slices.Contains(keys, "s_cams_add") || !slices.Contains(keys, "s_net") {
		t.Errorf("without the list: %v", keys)
	}
	net.snap = NetSnapshot{API: No}
	if keys := h.u.pageKeys("cams", &net.snap); len(keys) != 0 {
		t.Errorf("older app: %v", keys)
	}
}

// The two ways in: each a switch done by the app (PUT /network/ap or /network/lan {"enabled"}); off with cameras, a
// second tap; one under way, nothing else.
func TestCamsWaysIn(t *testing.T) {
	h, app, net := camsPage(t)
	h.press("s_cams_lan", 0.1) // off → on: at once
	if a := app.lastDid(); a.What != "lan_enabled" || a.Body["enabled"] != true || !h.u.camp.viaOn {
		t.Fatalf("%+v", app.did)
	}
	app.snap.Pending = "lan_enabled"
	if rs := h.u.viaLANRow(net.snap.Status, &app.snap, 1); !rs.Busy || rs.Knob != KnobOn || rs.Sub != Spanish.TurningOn {
		t.Errorf("under way: %+v", rs)
	}
	h.press("s_cams_wifi", 0.1)
	if len(app.did) != 1 {
		t.Fatal("another switch while one is under way")
	}
	app.snap.Pending = ""
	h.press("s_cams_wifi", 0.1) // on → off with a camera: asks first
	if len(app.did) != 1 || !h.u.confirming("via_wifi") {
		t.Fatalf("the first tap: %+v", app.did)
	}
	if n, warn := h.u.camsNote(&app.snap, 1, true); n != Spanish.NetX.ViaWifiOffAsk || !warn {
		t.Errorf("its note: %q", n)
	}
	if rs := h.u.viaWifiRow(net.snap.Status, &app.snap); rs.Sub != Spanish.TapAgain || rs.Tone != "warn" {
		t.Errorf("its row: %+v", rs)
	}
	h.press("s_cams_wifi", 0.1)
	if a := app.lastDid(); a.What != "ap_enabled" || a.Body["enabled"] != false || h.u.camp.viaOn {
		t.Fatalf("%+v", app.did)
	}
	// Without cameras nothing is cut: at once
	net.snap.Status.CamItems, net.snap.Status.LANEnabled = []NetCamera{}, true
	h.press("s_cams_lan", 0.1)
	if a := app.lastDid(); a.What != "lan_enabled" || a.Body["enabled"] != false {
		t.Fatalf("%+v", app.did)
	}
	// The app refuses (both off: 409 no_input): its message is the page's note
	app.snap.Last = &AppResult{What: "lan_enabled", Status: 409, Code: "no_input", Message: "ni por wifi ni por cable",
		At: h.now()}
	if n, warn := h.u.camsNote(&app.snap, 0, false); n != "ni por wifi ni por cable" || !warn {
		t.Errorf("refused: %q", n)
	}
	// The reception switch is still the Net's (PUT /network), never an action of the app
	n := len(app.did)
	net.snap.Status.Cameras = 1
	h.press("s_net", 0.1)
	if len(app.did) != n || net.calls[len(net.calls)-1] != "net false" {
		t.Fatalf("%v %v", app.did, net.calls)
	}
}

// What a way in says: off, receiving (with the Wi-Fi's name / the cable's address), «Crear mi wifi» off, the
// cable without an address, an error.
func TestCamsWaysText(t *testing.T) {
	h, app, net := camsPage(t)
	st := net.snap.Status
	nx := Spanish.NetX
	if rs := h.u.viaWifiRow(st, &app.snap); rs.Knob != KnobOn || rs.Sub != "Recibiendo · EnhancedIRL-0000" || rs.Tone != "good" {
		t.Errorf("%+v", rs)
	}
	st.AccessPoint.Policy, st.AccessPoint.State = "rescue", "off"
	if rs := h.u.viaWifiRow(st, &app.snap); rs.Sub != nx.ViaAPOff || rs.Tone != "warn" {
		t.Errorf("%+v", rs)
	}
	st.AccessPoint.Enabled = bp(false)
	if rs := h.u.viaWifiRow(st, &app.snap); rs.Knob != KnobOff || rs.Sub != nx.ViaOff {
		t.Errorf("%+v", rs)
	}
	if rs := h.u.viaLANRow(st, &app.snap, 1); rs.Knob != KnobOff || rs.Sub != "Apagado · eth0 · 192.168.1.20" {
		t.Errorf("%+v", rs)
	}
	st.LANEnabled, st.LAN.State = true, "on"
	if rs := h.u.viaLANRow(st, &app.snap, 1); rs.Sub != "Recibiendo · eth0 · 192.168.1.20" || rs.Tone != "good" {
		t.Errorf("%+v", rs)
	}
	st.LAN.IP = ""
	if rs := h.u.viaLANRow(st, &app.snap, 1); rs.Sub != "Sin IP en eth0: conecta su cable de red" || rs.Tone != "warn" {
		t.Errorf("%+v", rs)
	}
	st.LAN.State, st.LAN.Error = "error", "no se pudo abrir el puerto"
	if rs := h.u.viaLANRow(st, &app.snap, 1); rs.Sub != "Con error: no se pudo abrir el puerto" {
		t.Errorf("%+v", rs)
	}
	st.LAN = LANStatus{State: "off", Iface: "eth0", IP: "192.168.1.20"}
	if rs := h.u.viaLANRow(st, &app.snap, 0); rs.Sub != "eth0 · 192.168.1.20 · al añadir una cámara" {
		t.Errorf("%+v", rs)
	}
}

// A camera's state: the app's (connected, how, how much), or why the mixer does not get it (the engine's detail).
func TestCamStateText(t *testing.T) {
	h, _, net := camsPage(t)
	c := camItem{NetCamera: net.snap.Status.CamItems[0]}
	c.LossPct = new(float64)
	*c.LossPct = 0.25
	if s, tone := h.u.camStateText(c, false); s != "Conectada · SRT · por wifi · H.265 · 4,6 Mbps · 0,2 % perdido" || tone != "good" {
		t.Errorf("%q %q", s, tone)
	}
	if s, _ := h.u.camStateText(c, true); s != "Conectada · SRT · 4,6 Mbps · 0,2 % perdido" {
		t.Errorf("short: %q", s)
	}
	c.src = &SourceStatus{ID: "net:cam1", Kind: "network", State: "no_signal", DetailCode: "net_behind",
		DetailArgs: map[string]string{"seconds": "2.4"}}
	if s, tone := h.u.camStateText(c, false); s != "Va 2,4 s atrasada" || tone != "warn" {
		t.Errorf("%q %q", s, tone)
	}
	c.src.DetailCode = "net_waiting" // the engine waits: the app's word counts
	c.Connected = false
	if s, tone := h.u.camStateText(c, false); s != Spanish.NetX.CamWaiting || tone != "quiet" {
		t.Errorf("%q %q", s, tone)
	}
}

// «Add camera»: the name's keyboard (the app's characters only); OK sends it (POST /cameras {"name"}); at the most
// cameras, it says so instead.
func TestCamsAdd(t *testing.T) {
	h, app, net := camsPage(t)
	h.press("s_cams_add", 0.1) // one camera, at most one
	if h.u.kb != nil || len(app.did) != 0 {
		t.Fatal("a keyboard with no room")
	}
	if n, _ := h.u.pageNoteText(); n != "Ya hay 1: es el máximo de cámaras" {
		t.Errorf("%q", n)
	}
	net.snap.Status.CamMax = 2
	h.press("s_cams_add", 0.1)
	if h.u.kb == nil || h.u.kb.spec == nil || h.u.kbTitle() != Spanish.NetX.CamNameTitle || h.u.kb.max != CamNameMax ||
		len(h.u.kb.text) != 0 {
		t.Fatalf("%+v", h.u.kb)
	}
	for _, k := range []string{" ", " "} {
		h.u.kbKey(k, 0)
	}
	h.tapKey("OK", 0.1) // only spaces
	if h.u.kb == nil || h.u.kb.err != Spanish.NetX.CamNameBad || len(app.did) != 0 {
		t.Fatalf("%+v %v", h.u.kb, app.did)
	}
	h.tapKey("BKSP", TapLong.Seconds()+0.1)
	for _, k := range "GoPro <#1>" {
		h.u.kbKey(string(k), 0)
	}
	if string(h.u.kb.text) != "GoPro 1" {
		t.Fatalf("typed %q", string(h.u.kb.text))
	}
	h.tapKey("OK", 0.1)
	a := app.lastDid()
	if a.What != "cam_add" || a.Body["name"] != "GoPro 1" || len(a.Body) != 1 || h.u.kb != nil || h.u.camp.added != "GoPro 1" {
		t.Fatalf("%+v %v", a, h.u.kb)
	}
	app.snap.Last = &AppResult{What: "cam_add", OK: true, At: h.now()}
	if n, warn := h.u.camsNote(&app.snap, 2, true); n != "«GoPro 1» añadida: tócala para ver cómo conectarla" || warn {
		t.Errorf("%q", n)
	}
	// Under way: nothing
	app.snap.Pending = "cam_add"
	h.press("s_cams_add", 0.1)
	if h.u.kb != nil {
		t.Fatal("a keyboard while adding")
	}
}

// A camera's view: its row opens it (its name the title, with ←); an eye asks the app for the URLs (once), ← closes
// the eyes and drops them.
func TestCamView(t *testing.T) {
	h, app, net := camsPage(t)
	h.press("s_cam_0", 0.1)
	if h.u.camp.view != "cam1" {
		t.Fatal("not opened")
	}
	if title, back, ok := h.u.camsTitle(); title != "Móvil de Ana" || !back || !ok {
		t.Errorf("%q %v %v", title, back, ok)
	}
	keys := h.u.settingsKeys(&net.snap)
	want := []string{"s_close", "s_back", "s_camv_name", "s_camv_key", "s_camv_remove", camSRTEye, camRTMPEye}
	if !slices.Equal(keys, want) {
		t.Errorf("%v, want %v", keys, want)
	}
	h.press(camSRTEye, 0.1)
	if a := app.lastDid(); a.What != "cam_creds" || !h.u.revealed(camSRTEye) {
		t.Fatalf("%+v", app.did)
	}
	app.snap.Pending = "cam_creds"
	h.press(camRTMPEye, 0.1) // asked already: not twice
	if len(app.did) != 1 || !h.u.revealed(camRTMPEye) {
		t.Fatalf("%v", app.did)
	}
	app.snap.Pending, app.snap.CamCreds = "", camCreds()
	h.press(camRTMPEye, 0.1)
	h.press(camRTMPEye, 0.1) // closed and opened again: asked again (a new key may have changed them)
	if len(app.did) != 2 || app.did[1].What != "cam_creds" {
		t.Fatalf("%v", app.did)
	}
	app.did = app.did[:1]
	// Both ways on: a choice; the cable without an address has no URLs (no eyes)
	net.snap.Status.LANEnabled = true
	keys = h.u.settingsKeys(&net.snap)
	if !slices.Contains(keys, "s_camv_wifi") || !slices.Contains(keys, "s_camv_lan") {
		t.Fatalf("%v", keys)
	}
	h.press("s_camv_lan", 0.1)
	if h.u.camp.way != "lan" || h.u.camWay(net.snap.Status) != "lan" {
		t.Fatal(h.u.camp.way)
	}
	net.snap.Status.LAN.IP = ""
	if keys := h.u.settingsKeys(&net.snap); slices.Contains(keys, camSRTEye) {
		t.Errorf("eyes without URLs: %v", keys)
	}
	// ←: the list, the eyes closed and the URLs dropped
	dropped := app.dropped
	h.press("s_back", 0.1)
	if h.u.camp.view != "" || h.u.revealed(camSRTEye) || app.dropped != dropped+1 || !h.u.settingsOpen {
		t.Fatalf("back: %q %v %d", h.u.camp.view, h.u.reveal, app.dropped)
	}
	// The camera removed from the web while its view is open: it says so, only ← answers
	h.press("s_cam_0", 0.1)
	net.snap.Status.CamItems = []NetCamera{}
	if keys := h.u.settingsKeys(&net.snap); !slices.Equal(keys, []string{"s_close", "s_back"}) {
		t.Errorf("gone: %v", keys)
	}
	if title, _, _ := h.u.camsTitle(); title != Spanish.PageTitle["cams"] {
		t.Errorf("gone: %q", title)
	}
}

// Its name, a new key and removing it: the name's keyboard on its name; «New key» and «Remove» with a second tap.
func TestCamViewActions(t *testing.T) {
	h, app, _ := camsPage(t)
	h.press("s_cam_0", 0.1)
	h.press("s_camv_name", 0.1)
	if h.u.kb == nil || string(h.u.kb.text) != "Móvil de Ana" {
		t.Fatalf("%+v", h.u.kb)
	}
	h.tapKey("OK", 0.1) // the same name: nothing
	if len(app.did) != 0 || h.u.kb != nil || h.u.camp.view != "cam1" {
		t.Fatalf("%v %q", app.did, h.u.camp.view)
	}
	h.press("s_camv_name", 0.1)
	h.u.kbKey("2", 0)
	h.tapKey("OK", 0.1)
	if a := app.lastDid(); a.What != "cam_rename" || a.ID != "cam1" || a.Body["name"] != "Móvil de Ana2" || h.u.camp.view != "cam1" {
		t.Fatalf("%+v %q", a, h.u.camp.view)
	}
	// «New key»: the first tap says what it does; the second sends it and closes the eyes (they showed the old key)
	app.snap.CamCreds = camCreds()
	h.press(camSRTEye, 0.1)
	n := len(app.did)
	h.press("s_camv_key", 0.1)
	if len(app.did) != n || !h.u.confirming("cam_rotate") {
		t.Fatal("the first tap sent it")
	}
	if note, warn := h.u.camViewNote(&app.snap, &h.net.snap, camItem{}, "wifi"); note != Spanish.NetX.NewKeyAsk || !warn {
		t.Errorf("%q", note)
	}
	dropped := app.dropped
	h.press("s_camv_key", 0.1)
	if a := app.lastDid(); a.What != "cam_rotate" || a.ID != "cam1" || a.Body != nil || h.u.revealed(camSRTEye) ||
		app.dropped != dropped+1 {
		t.Fatalf("%+v %v %d", a, h.u.reveal, app.dropped)
	}
	// «Remove»: the last camera says so; the second tap removes it and goes back to the list
	h.press("s_camv_remove", 0.1)
	if note, _ := h.u.camViewNote(&app.snap, &h.net.snap, camItem{}, "wifi"); note != Spanish.NetX.RemoveLastAsk {
		t.Errorf("%q", note)
	}
	h.press("s_camv_remove", 0.1)
	if a := app.lastDid(); a.What != "cam_remove" || a.ID != "cam1" || h.u.camp.view != "" {
		t.Fatalf("%+v %q", a, h.u.camp.view)
	}
}

// Leaving the page or Settings (or locking the screen) closes the camera's view and drops its URLs.
func TestCamsLeaveDropsSecrets(t *testing.T) {
	h, app, _ := camsPage(t)
	open := func() {
		h.press("s_cam_0", 0.1)
		app.snap.CamCreds = camCreds()
		h.press(camSRTEye, 0.1)
		if h.u.camp.view != "cam1" || !h.u.revealed(camSRTEye) {
			t.Fatal("not open")
		}
	}
	for _, leave := range []struct {
		name string
		do   func()
	}{
		{"close", func() { h.press("s_close", 0.1) }},
		{"lock", func() { h.setLocked(true); h.u.Tick(); h.setLocked(false) }},
		{"another page", func() { h.u.openPage("wifi") }},
	} {
		if !h.u.settingsOpen {
			h.press("settings", 0.1)
		}
		h.u.openPage("cams")
		h.u.camp.shown = []string{"cam1"}
		open()
		dropped := app.dropped
		leave.do()
		if h.u.camp.view != "" || h.u.revealed(camSRTEye) || app.dropped <= dropped {
			t.Errorf("%s: view %q eyes %v dropped %d→%d", leave.name, h.u.camp.view, h.u.reveal, dropped, app.dropped)
		}
	}
}

// What is drawn as text: never a camera's key nor its URLs while their eye is closed; with the SRT eye open, its URL
// (and its QR instead of the note); with the RTMP one, the server and the key.
func TestCamSecretsDrawnOnlyRevealed(t *testing.T) {
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	h, _ := drawHarness(t)
	h.camsUp()
	app := &fakeApp{snap: AppSnapshot{DeviceAPI: Yes, Device: device("es"), CamCreds: camCreds()}}
	h.u.App = app
	h.press("settings", 0.1)
	h.u.openPage("cams")
	draw := func() string {
		shown = shown[:0]
		h.u.Dirty = true
		h.u.Tick()
		return strings.Join(shown, "")
	}
	all := draw()
	if !strings.Contains(all, "Móvil de Ana") || strings.Contains(all, camKey) {
		t.Fatalf("the list: %q", shown)
	}
	h.press("s_cam_0", 0.1)
	all = draw()
	if strings.Contains(all, camKey) || strings.Contains(all, "srt://") || strings.Contains(all, "rtmp://") ||
		!slices.Contains(shown, Spanish.NetX.QRClosed) {
		t.Fatalf("eyes closed: %q", shown)
	}
	h.press(camSRTEye, 0.1)
	all = draw()
	if !strings.Contains(all, "srt://10.42.0.1:8890?streamid=publish:cam1:cam1:"+camKey) || strings.Contains(all, "rtmp://") ||
		slices.Contains(shown, Spanish.NetX.QRClosed) {
		t.Fatalf("SRT open: %q", shown)
	}
	h.press(camRTMPEye, 0.1)
	all = draw()
	if !strings.Contains(all, "rtmp://10.42.0.1:1935/") || !strings.Contains(all, "cam1?user=cam1&pass="+camKey) {
		t.Fatalf("RTMP open: %q", shown)
	}
	h.press("s_back", 0.1)
	if all := draw(); strings.Contains(all, camKey) {
		t.Fatalf("back in the list: %q", shown)
	}
}

// The page drawn in every language and state: no text of the table cut.
func TestCamsPageRenders(t *testing.T) {
	page := func(mod func(h *harness, app *fakeApp)) func(h *harness) {
		return func(h *harness) {
			h.camsUp()
			app := &fakeApp{snap: AppSnapshot{DeviceAPI: Yes, Device: device(h.u.Tx.Code)}}
			h.u.App = app
			h.u.settingsOpen = true
			h.u.openPage("cams")
			if mod != nil {
				mod(h, app)
			}
		}
	}
	view := func(mod func(h *harness, app *fakeApp)) func(h *harness) {
		return page(func(h *harness, app *fakeApp) {
			h.u.openCam("cam1")
			if mod != nil {
				mod(h, app)
			}
		})
	}
	several := func(h *harness, n int) {
		st := h.net.snap.Status
		st.LANEnabled, st.LAN.State, st.CamMax = true, "on", 8
		names := []string{"Cámara del escenario", "GoPro (casco)", "Dron", "Móvil de Leo", "Cámara 7"}
		for i := range n - 1 {
			st.CamItems = append(st.CamItems, NetCamera{ID: "cam" + string(rune('2'+i)), Name: names[i%len(names)],
				Connected: i == 0, Via: "lan", Protocol: "rtmp", Kbps: ip(2900)})
		}
		st.Cameras = len(st.CamItems)
	}
	reveal := func(h *harness, app *fakeApp) {
		app.snap.CamCreds = camCreds()
		h.u.toggleReveal(camSRTEye)
		h.u.toggleReveal(camRTMPEye)
	}
	checkRenders(t, []renderView{
		{"one", page(nil)},
		{"none", page(func(h *harness, _ *fakeApp) {
			h.net.snap.Status.CamItems, h.net.snap.Status.Cameras = []NetCamera{}, 0
		})},
		{"several", page(func(h *harness, _ *fakeApp) { several(h, 3) })},
		{"pages", page(func(h *harness, _ *fakeApp) { several(h, 6) })},
		{"wifi off asked", page(func(h *harness, _ *fakeApp) { h.u.toggleVia("ap_enabled") })},
		{"cable off asked", page(func(h *harness, _ *fakeApp) {
			h.net.snap.Status.LANEnabled = true
			h.u.toggleVia("lan_enabled")
		})},
		{"switching", page(func(h *harness, app *fakeApp) { app.snap.Pending = "ap_enabled" })},
		{"reception off", page(func(h *harness, _ *fakeApp) { h.net.snap.Status.Enabled = false })},
		{"reception busy", page(func(h *harness, _ *fakeApp) { h.net.snap.Pending = &Pending{What: "net", On: false} })},
		{"ap off", page(func(h *harness, _ *fakeApp) {
			h.net.snap.Status.AccessPoint.Policy, h.net.snap.Status.AccessPoint.State = "rescue", "off"
		})},
		{"cable error", page(func(h *harness, _ *fakeApp) {
			h.net.snap.Status.LANEnabled, h.net.snap.Status.LAN.State = true, "error"
			h.net.snap.Status.LAN.Error, h.net.snap.Status.LAN.IP = "", ""
		})},
		{"adding", page(func(h *harness, app *fakeApp) { app.snap.Pending = "cam_add" })},
		{"added", page(func(h *harness, app *fakeApp) {
			h.u.camp.added = "GoPro (casco)"
			app.snap.Last = &AppResult{What: "cam_add", OK: true, At: h.now()}
		})},
		{"removed", page(func(h *harness, app *fakeApp) {
			app.snap.Last = &AppResult{What: "cam_remove", OK: true, At: h.now()}
		})},
		{"refused", page(func(h *harness, app *fakeApp) {
			app.snap.Last = &AppResult{What: "lan_enabled", Status: 409, Code: "no_input", At: h.now(),
				Message: "Las cámaras tienen que poder entrar por la wifi o por cable: enciende una antes de apagar la otra"}
		})},
		{"no list", page(func(h *harness, _ *fakeApp) { h.net.snap.Status.CamItems = nil })},
		{"older app", func(h *harness) {
			h.net.snap = NetSnapshot{API: No}
			legacy := &WifiCreds{SSID: "EnhancedIRL-0000", Password: wifiPw, QR: testQR(), On: true}
			h.u.Legacy = func() *WifiCreds { return legacy }
			h.u.settingsOpen, h.u.settingsPage = true, "cams"
		}},
		{"asking", func(h *harness) { h.net.snap = NetSnapshot{}; h.u.settingsOpen, h.u.settingsPage = true, "cams" }},
		{"not answering", func(h *harness) {
			h.net.snap = NetSnapshot{API: Yes}
			h.u.settingsOpen, h.u.settingsPage = true, "cams"
		}},
		{"view", view(nil)},
		{"view revealed", view(reveal)},
		{"view reading", view(func(h *harness, app *fakeApp) {
			h.u.toggleReveal(camSRTEye)
			app.snap.Pending = "cam_creds"
		})},
		{"view cable", view(func(h *harness, app *fakeApp) {
			h.net.snap.Status.LANEnabled, h.net.snap.Status.LAN.State = true, "on"
			h.u.camp.way = "lan"
			reveal(h, app)
		})},
		{"view no cable address", view(func(h *harness, _ *fakeApp) {
			h.net.snap.Status.LANEnabled, h.net.snap.Status.LAN.IP = true, ""
			h.u.camp.way = "lan"
		})},
		{"view ap off", view(func(h *harness, _ *fakeApp) {
			h.net.snap.Status.AccessPoint.Policy, h.net.snap.Status.AccessPoint.State = "rescue", "off"
		})},
		// The view's «New key» and «Remove» waiting for their second tap say the short TapAgain
		{"view new key armed", view(func(h *harness, _ *fakeApp) { h.u.confirmTap("cam_rotate") })},
		{"view remove armed", view(func(h *harness, _ *fakeApp) { h.u.confirmTap("cam_remove") })},
		{"view new key", view(func(h *harness, app *fakeApp) {
			app.snap.Last = &AppResult{What: "cam_rotate", OK: true, At: h.now()}
		})},
		{"view refused", view(func(h *harness, app *fakeApp) {
			h.u.toggleReveal(camSRTEye)
			app.snap.Last = &AppResult{What: "cam_creds", Status: 500, Code: "internal", At: h.now(),
				Message: "No se han podido leer las credenciales"}
		})},
		{"view waiting", view(func(h *harness, _ *fakeApp) {
			c := &h.net.snap.Status.CamItems[0]
			c.Connected, c.Protocol, c.Kbps = false, "", nil
		})},
		{"view gone", view(func(h *harness, _ *fakeApp) { h.net.snap.Status.CamItems = []NetCamera{} })},
	})
}

// The page's buttons and the camera view's: inside the page, ≥ 48 px, none over another in the same view, on every
// screen; the list shows at least three cameras at 1024×600.
func TestCamsLayout(t *testing.T) {
	for _, sz := range [][2]int{{1024, 600}, {1280, 720}, {1920, 1080}} {
		lay := NewLayout(sz[0], sz[1], 4)
		list := []string{"s_net", "s_cams_wifi", "s_cams_lan", "s_cams_prev", "s_cams_next", "s_cams_add"}
		for i := range lay.camRows() {
			list = append(list, "s_cam_"+strconv.Itoa(i))
		}
		camv := []string{"s_camv_name", "s_camv_key", "s_camv_remove", "s_camv_wifi", "s_camv_lan", "s_camv_srt",
			camSRTEye, "s_camv_rtmp", camRTMPEye, "s_camv_qr", "s_camv_note"}
		for _, keys := range [][]string{list, camv} {
			for i, k := range keys {
				r, ok := lay.Buttons[k]
				c := lay.SettingsPage
				if !ok || r.X < c.X || r.Y < c.Y || r.X+r.W > c.X+c.W || r.Y+r.H > c.Y+c.H {
					t.Errorf("%v: %s %v outside %v", sz, k, r, c)
				}
				if r.H < 48 || r.W < 48 {
					t.Errorf("%v: %s %v too small", sz, k, r)
				}
				for _, k2 := range keys[i+1:] {
					if overlaps(r, lay.Buttons[k2]) {
						t.Errorf("%v: %s overlaps %s", sz, k, k2)
					}
				}
			}
		}
		if sz[0] == 1024 && lay.camRows() < 3 {
			t.Errorf("only %d cameras at once", lay.camRows())
		}
		if q := lay.Buttons["s_camv_qr"]; q.W != q.H || q.W < 240*sz[0]/1024 {
			t.Errorf("%v: the QR's square %v", sz, q)
		}
	}
}
