package ui

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// wifiPw is the board's FAKE Wi-Fi password in these tests.
const wifiPw = "Ejempl0lI1NoEsLaReal"

// wifiUp is the app's state with «Crear mi wifi» up and the Wi-Fi to join known (its channel saved as auto, 6 in use).
func (h *harness) wifiUp() *fakeNet {
	net := h.wifiState()
	net.snap.Status.AccessPoint.SSID, net.snap.Status.AccessPoint.Channel = "EnhancedIRL-0000", ip(6)
	net.snap.Status.AccessPoint.Clients = ip(1)
	net.snap.Wifi = &WifiCreds{SSID: "EnhancedIRL-0000", Password: wifiPw, QR: testQR(), Channel: "auto"}
	return net
}

// The Wi-Fi page's own buttons: the password's eye (with the QR on view), the channel and «New password» (an app with
// the board's Wi-Fi, in Settings); none of them without the QR.
func TestWifiPageKeys(t *testing.T) {
	h, _ := pagesHarness(t, "wifi")
	net := h.wifiUp()
	keys := h.u.settingsKeys(&net.snap)
	for _, k := range []string{wifiPwEye, "s_wifi_ch", "s_wifi_rotate", "s_ssid"} {
		if !slices.Contains(keys, k) {
			t.Errorf("no %s in %v", k, keys)
		}
	}
	// The AP off: no QR, no password, no channel (its name and «Rename» stay)
	net.snap.Status.AccessPoint.Policy, net.snap.Status.AccessPoint.State = "rescue", "off"
	keys = h.u.settingsKeys(&net.snap)
	if slices.Contains(keys, wifiPwEye) || slices.Contains(keys, "s_wifi_ch") || !slices.Contains(keys, "s_ssid") {
		t.Errorf("AP off: %v", keys)
	}
	// An older app (wifi.json): the eye, but nothing the app would have to do
	h.net.snap = NetSnapshot{API: No}
	legacy := &WifiCreds{SSID: "EnhancedIRL-0000", Password: wifiPw, QR: testQR(), On: true}
	h.u.Legacy = func() *WifiCreds { return legacy }
	keys = h.u.settingsKeys(&h.net.snap)
	if !slices.Contains(keys, wifiPwEye) || slices.Contains(keys, "s_wifi_ch") || slices.Contains(keys, "s_wifi_rotate") {
		t.Errorf("older app: %v", keys)
	}
}

// The password is drawn as dots until its eye is opened; the eye closes when the page changes or Settings closes.
// The QR (which carries it: it is how cameras and phones join) is drawn either way.
func TestWifiPasswordEye(t *testing.T) {
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	h, _ := drawHarness(t)
	h.wifiUp()
	h.press("settings", 0.1)
	draw := func() bool {
		shown = shown[:0]
		h.u.Dirty = true
		h.u.Tick()
		return slices.ContainsFunc(shown, func(s string) bool { return strings.Contains(s, "NoEsLaReal") })
	}
	if draw() || h.u.wifiPwShown() {
		t.Fatalf("the password drawn with its eye closed: %q", shown)
	}
	h.press(wifiPwEye, 0.1)
	if !draw() {
		t.Fatalf("the eye open, no password: %q", shown)
	}
	h.press(wifiPwEye, 0.1)
	if draw() {
		t.Fatal("closed again, still drawn")
	}
	h.press(wifiPwEye, 0.1)
	h.press("s_page_cams", 0.1)
	h.press("s_page_wifi", 0.1)
	if draw() {
		t.Fatal("another page and back: still open")
	}
	h.press(wifiPwEye, 0.1)
	h.press("s_close", 0.1)
	h.press("settings", 0.1)
	if draw() {
		t.Fatal("Settings closed and opened: still open")
	}
	// Opened in the wizard, it does not stay open in Settings (nor the other way round)
	h.u.toggleWifiPw()
	h.u.camp.wifiEyeWiz = true
	if h.u.wifiPwShown() {
		t.Fatal("an eye of the wizard open in Settings")
	}
	h.u.toggleWifiPw() // here: it starts closed, and this tap opens it
	if !h.u.wifiPwShown() || h.u.camp.wifiEyeWiz {
		t.Fatal("the tap in Settings")
	}
}

// The channel: the picker (Automatic, 1-11) on the saved one; a choice goes to the app (PUT /network/ap {"channel"})
// and says the cameras disconnect for a moment; the same one does nothing.
func TestWifiChannel(t *testing.T) {
	h, app := pagesHarness(t, "wifi")
	net := h.wifiUp()
	h.press("s_wifi_ch", 0.1)
	p := h.u.chooser
	if h.u.settingsView != "pick" || p == nil || p.cur != "auto" || len(p.opts) != 12 || p.opts[0].ID != "auto" ||
		p.opts[11].ID != "11" || p.title != Spanish.NetX.Channel {
		t.Fatalf("%q %+v", h.u.settingsView, p)
	}
	h.u.pickerTouch("s_pick_5", "6", true)
	a := app.lastDid()
	if a.What != "ap_channel" || a.Body["channel"] != "6" || len(a.Body) != 1 || h.u.settingsView != "main" {
		t.Fatalf("%+v %q", a, h.u.settingsView)
	}
	if t0, warn := h.u.pageNoteText(); t0 != Spanish.NetX.ChannelApplying || warn {
		t.Errorf("notice %q", t0)
	}
	n := len(app.did)
	net.snap.Wifi.Channel = "6"
	h.press("s_wifi_ch", 0.1)
	if h.u.chooser.cur != "6" {
		t.Fatal(h.u.chooser.cur)
	}
	h.u.pickerTouch("s_pick_0", "6", true) // the same: nothing
	if len(app.did) != n {
		t.Fatal("the same channel sent")
	}
	// ← closes it without choosing
	h.press("s_wifi_ch", 0.1)
	h.press("s_back", 0.1)
	if h.u.settingsView != "main" || len(app.did) != n {
		t.Fatal("back")
	}
	// A change under way: no picker
	app.snap.Pending = "ap_channel"
	h.press("s_wifi_ch", 0.1)
	if h.u.settingsView != "main" {
		t.Fatal("a picker while the channel is changing")
	}
	// What the field says: the saved one and the one in use
	cases := []struct {
		saved string
		inUse *int
		want  string
	}{{"auto", ip(6), "Automático · en uso: 6"}, {"", ip(11), "Automático · en uso: 11"}, {"6", ip(6), "6"},
		{"11", ip(6), "11 · en uso: 6"}, {"auto", nil, "Automático"}}
	for _, c := range cases {
		net.snap.Wifi.Channel, net.snap.Status.AccessPoint.Channel = c.saved, c.inUse
		if got := h.u.channelText(&net.snap); got != c.want {
			t.Errorf("%q %v: %q, want %q", c.saved, c.inUse, got, c.want)
		}
	}
}

// «New password»: a second tap within ConfirmFor sends it (PUT /network/ap {"rotatePassword"}); the first one says
// what it does.
func TestWifiNewPassword(t *testing.T) {
	h, app := pagesHarness(t, "wifi")
	h.wifiUp()
	h.press("s_wifi_rotate", 0.1)
	if len(app.did) != 0 || !h.u.confirming("ap_rotate") || !slices.Contains(h.u.wifiNotes(), Spanish.NetX.NewPasswordAsk) {
		t.Fatalf("the first tap: %v %v", app.did, h.u.wifiNotes())
	}
	h.press("s_wifi_rotate", 0.1)
	if a := app.lastDid(); a.What != "ap_rotate" || a.Body != nil || len(app.did) != 1 {
		t.Fatalf("%+v", app.did)
	}
	// Taken: what to do now; refused: the app's message
	app.snap.Last = &AppResult{What: "ap_rotate", OK: true, At: h.now()}
	if !slices.Contains(h.u.wifiNotes(), Spanish.NetX.NewPasswordDone) {
		t.Errorf("done: %v", h.u.wifiNotes())
	}
	app.snap.Last = &AppResult{What: "ap_rotate", Status: 409, Code: "on_air", Message: "una cámara al aire", At: h.now()}
	if !slices.Contains(h.u.wifiNotes(), "una cámara al aire") {
		t.Errorf("refused: %v", h.u.wifiNotes())
	}
	// The confirmation expires
	h.press("s_wifi_rotate", 0.1)
	h.advance(ConfirmFor + time.Second)
	h.u.Tick()
	h.press("s_wifi_rotate", 0.1)
	if len(app.did) != 1 {
		t.Fatal("an expired confirmation sent it")
	}
	// Under way: nothing
	app.snap.Pending = "ap_rotate"
	h.press("s_wifi_rotate", 0.1)
	if len(app.did) != 1 {
		t.Fatal("sent while under way")
	}
}

// The wizard's network step keeps its page as it was: the eye, but neither the channel nor «New password».
func TestWizardWifiExtras(t *testing.T) {
	h := newHarness(t)
	app := wizardApp(h)
	net := h.wifiUp()
	app.snap.Setup.Steps[0].State, app.snap.Setup.Steps[1].State = "done", "done"
	h.u.Tick()
	keys := h.u.wizardKeys(&net.snap)
	if h.u.wizardStep() != "network" || !slices.Contains(keys, wifiPwEye) || slices.Contains(keys, "s_wifi_ch") ||
		slices.Contains(keys, "s_wifi_rotate") {
		t.Fatalf("%q %v", h.u.wizardStep(), keys)
	}
	h.press(wifiPwEye, 0.1)
	if !h.u.wifiPwShown() || !h.u.camp.wifiEyeWiz {
		t.Fatal("the eye in the wizard")
	}
	h.u.rotateWifi()
	h.u.rotateWifi()
	h.u.openChannelPicker()
	for _, a := range app.did {
		if a.What == "ap_rotate" || a.What == "ap_channel" {
			t.Fatalf("in the wizard: %+v", a)
		}
	}
}

// The column beside the QR: inside the QR area, beside the QR (at most as wide as the area is high), each ≥ 48 px
// high, none over another, on every screen and with or without the client row.
func TestWifiColumnLayout(t *testing.T) {
	keys := []string{"s_wifi_pw", wifiPwEye, "s_wifi_ch", "s_wifi_rotate", "s_ssid"}
	for _, sz := range [][2]int{{1024, 600}, {1280, 720}, {1920, 1080}} {
		lay := NewLayout(sz[0], sz[1], 4)
		for _, client := range []bool{true, false} {
			lay.SettingsRows(client)
			q := lay.SettingsQR
			for i, k := range keys {
				r := lay.Buttons[k]
				if !q.Inside(r.X, r.Y, 0) || !q.Inside(r.X+r.W-1, r.Y+r.H-1, 0) || r.X < q.X+q.H+lay.M {
					t.Errorf("%v client=%v: %s %v outside the column of %v", sz, client, k, r, q)
				}
				if r.H < 48 || r.W < 48 {
					t.Errorf("%v: %s %v too small", sz, k, r)
				}
				for _, k2 := range keys[i+1:] {
					if overlaps(r, lay.Buttons[k2]) {
						t.Errorf("%v client=%v: %s overlaps %s", sz, client, k, k2)
					}
				}
			}
			if pw := lay.Buttons["s_wifi_pw"]; pw.Y-q.Y < 48 {
				t.Errorf("%v client=%v: no room for the name above the password (%d px)", sz, client, pw.Y-q.Y)
			}
		}
	}
}

// The Wi-Fi page drawn in every language and state: no text of the table cut.
func TestWifiPageRenders(t *testing.T) {
	up := func(mod func(h *harness)) func(h *harness) {
		return func(h *harness) {
			h.wifiUp()
			h.u.App = &fakeApp{snap: AppSnapshot{DeviceAPI: Yes, Device: device(h.u.Tx.Code)}}
			h.u.settingsOpen = true
			h.u.openPage("wifi")
			if mod != nil {
				mod(h)
			}
		}
	}
	checkRenders(t, []renderView{
		{"hidden", up(nil)},
		{"revealed", up(func(h *harness) { h.u.toggleWifiPw() })},
		{"noclient", up(func(h *harness) { h.net.snap.Client = nil; h.u.toggleWifiPw() })},
		{"picker", up(func(h *harness) { h.u.openChannelPicker() })},
		{"rotate armed", up(func(h *harness) { h.u.rotateWifi() })},
		{"rotate armed noclient", up(func(h *harness) { h.net.snap.Client = nil; h.u.rotateWifi() })},
		{"applying", up(func(h *harness) { h.u.App.(*fakeApp).snap.Pending = "ap_channel" })},
		{"rotating", up(func(h *harness) { h.u.App.(*fakeApp).snap.Pending = "ap_rotate" })},
		{"rotated", up(func(h *harness) {
			h.u.App.(*fakeApp).snap.Last = &AppResult{What: "ap_rotate", OK: true, At: h.now()}
		})},
		{"refused", up(func(h *harness) {
			h.u.App.(*fakeApp).snap.Last = &AppResult{What: "ap_channel", Status: 409, Code: "on_air", At: h.now(),
				Message: "«Móvil de Ana» entra por esta wifi y está al aire"}
		})},
		{"channel set", up(func(h *harness) { h.net.snap.Wifi.Channel = "11" })},
		{"older app", func(h *harness) {
			h.net.snap = NetSnapshot{API: No}
			legacy := &WifiCreds{SSID: "EnhancedIRL-0000", Password: wifiPw, QR: testQR(), On: true}
			h.u.Legacy = func() *WifiCreds { return legacy }
			h.u.settingsOpen = true
			h.u.toggleWifiPw()
		}},
		{"ap off", up(func(h *harness) {
			h.net.snap.Status.AccessPoint.Policy, h.net.snap.Status.AccessPoint.State = "rescue", "off"
		})},
	})
}
