package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeNet is the app's state, fake (NetState's thread does not run in the tests): what the Python tests set
// on NetState and the calls they replace with lambdas.
type fakeNet struct {
	snap     NetSnapshot
	panel    bool
	changeOK bool
	calls    []string // ("what", value) of the switches
	sent     []string // connect / forget / scan, like the Python tests' `sent`
	now      func() time.Time
}

func newFakeNet() *fakeNet { return &fakeNet{changeOK: true} }

func (f *fakeNet) Snapshot() NetSnapshot { return f.snap }
func (f *fakeNet) SetPanel(on bool)      { f.panel = on }
func (f *fakeNet) SetScan(on bool)       { f.sent = append(f.sent, fmt.Sprintf("scan %v", on)) }
func (f *fakeNet) change(what string, v any) bool {
	if f.changeOK {
		f.calls = append(f.calls, fmt.Sprintf("%s %v", what, v))
	}
	return f.changeOK
}
func (f *fakeNet) SetEnabled(on bool) bool { return f.change("net", on) }
func (f *fakeNet) SetAP(on bool) bool {
	if on {
		return f.change("ap", "always")
	}
	return f.change("ap", "rescue")
}
func (f *fakeNet) SetClient(on bool) bool   { return f.change("client", on) }
func (f *fakeNet) SetSSID(name string) bool { return f.change("ssid", name) }
func (f *fakeNet) Connect(ssid string, pw []byte) bool {
	if pw == nil {
		f.sent = append(f.sent, ssid+" <none>")
	} else {
		f.sent = append(f.sent, ssid+" "+string(pw))
	}
	return true
}
func (f *fakeNet) Forget(ssid string) bool { f.sent = append(f.sent, "forget "+ssid); return true }
func (f *fakeNet) Say(text string) {
	f.snap.Message, f.snap.MsgUntil = text, f.now().Add(6*time.Second)
}

func (f *fakeNet) last(xs []string, n int) []string { return xs[max(0, len(xs)-n):] }

// harness is ScreenTouchTest's setUp: a UI on a 1024×600 layout, a fake engine (the commands it gets) and a
// fake clock.
type harness struct {
	t    *testing.T
	u    *UI
	lay  *Layout
	net  *fakeNet
	cmds []map[string]any
	clk  time.Time
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, net: newFakeNet(), clk: time.Unix(100000, 0)}
	h.net.now = h.now
	h.lay = NewLayout(1024, 600, 4)
	h.u = New(h.lay, Spanish, h.net)
	h.u.Clock = h.now
	h.u.Send = func(c Command) { h.cmds = append(h.cmds, c.Msg) }
	h.u.State = "on"
	// The engine's slots: sources in slots 1-3 so a tile's tap has a source
	st := &Status{}
	st.Program.Layout = "full"
	for i, id := range []string{"hdmi", "usb1", "usb2"} {
		st.Sources = append(st.Sources, SourceStatus{ID: id, Slot: i + 1, Name: id, State: "live"})
	}
	main := "hdmi"
	st.Program.Main = &main
	h.u.SetStatus(st)
	return h
}

func (h *harness) now() time.Time { return h.clk }

func (h *harness) advance(d time.Duration) { h.clk = h.clk.Add(d) }

func secs(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func (h *harness) at(key any) (float64, float64) {
	var r Rect
	switch k := key.(type) {
	case string:
		r = h.lay.Buttons[k]
	case int:
		r = h.lay.Tiles[k]
	}
	return (float64(r.X) + float64(r.W)/2) / 1024, (float64(r.Y) + float64(r.H)/2) / 600
}

// press: down, held `seconds` (the hold's start moved back), a tick, and up.
func (h *harness) press(key any, seconds float64) {
	x, y := h.at(key)
	h.u.OnTouch("down", x, y, 0)
	if h.u.hold != nil {
		h.u.hold.since = h.u.hold.since.Add(-secs(seconds))
	}
	h.u.Tick()
	h.u.OnTouch("up", x, y, secs(seconds))
}

// openPage goes to a Settings page as a finger would: its group's entry in the list, then its tab (Settings open).
func (h *harness) openPage(page string) {
	h.t.Helper()
	g := groupOf(page)
	if g == nil {
		h.t.Fatalf("no group for %s", page)
	}
	h.press("s_page_"+g.ID, 0.1)
	if len(g.Pages) > 1 {
		h.press("s_tab_"+page, 0.1)
	}
}

func (h *harness) tapKey(key string, held float64) {
	var r Rect
	if b, ok := h.lay.Buttons[key]; ok {
		r = b
	} else {
		r, _ = h.lay.KeyRect(h.u.kb.layer, key)
	}
	x, y := (float64(r.X)+float64(r.W)/2)/1024, (float64(r.Y)+float64(r.H)/2)/600
	h.u.OnTouch("down", x, y, 0)
	h.u.OnTouch("up", x, y, secs(held))
}

func (h *harness) setLocked(on bool) {
	st := *h.u.st
	st.Screen.Locked = on
	h.u.SetStatus(&st)
}

func (h *harness) cmdNames() []string {
	var out []string
	for _, m := range h.cmds {
		out = append(out, fmt.Sprint(m))
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ScreenTouchTest.test_tap_and_long_press
func TestTapAndLongPress(t *testing.T) {
	h := newHarness(t)
	h.press(1, 0.1)
	h.press(1, 0.8)
	want := []string{"map[cmd:take source:usb1]", "map[cmd:secondary source:usb1]"}
	if got := h.cmdNames(); !eqStrings(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

// ScreenTouchTest.test_emit_needs_hold
func TestEmitNeedsHold(t *testing.T) {
	h := newHarness(t)
	h.press("emit", 0.3)
	if len(h.cmds) != 0 || !strings.Contains(h.u.hint, "Mantén") {
		t.Fatalf("a short press emits: %v %q", h.cmds, h.u.hint)
	}
	h.press("emit", Hold.Seconds()+0.1)
	want := []string{"map[by:screen cmd:output on:true]"}
	if got := h.cmdNames(); !eqStrings(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

// ScreenTouchTest.test_power_menu
func TestPowerMenu(t *testing.T) {
	h := newHarness(t)
	changed := 0
	h.u.Changed = func() { changed++ }
	h.press("power", 0.1)
	if !h.u.menuOpen {
		t.Fatal("the menu does not open")
	}
	h.press(0, 0.1) // outside the panel: closes it, without touching the tile
	if h.u.menuOpen || len(h.cmds) != 0 {
		t.Fatal("outside the menu")
	}
	h.press("power", 0.1)
	h.press("m_reboot", 0.5) // a short press does not reboot
	if h.u.Power() != nil || len(h.cmds) != 0 {
		t.Fatal("a short press reboots")
	}
	h.press("m_cancel", 0.1)
	if h.u.menuOpen {
		t.Fatal("cancel")
	}
	h.press("power", 0.1)
	h.press("m_poweroff", HoldPower.Seconds()+0.1)
	p := h.u.Power()
	if p == nil || p.Want != "poweroff" || p.Seq != 1 || changed != 1 || len(h.cmds) != 0 {
		t.Fatalf("power request %+v (changed %d, cmds %v)", p, changed, h.cmds)
	}
	if h.u.powering != "poweroff" || h.u.menuOpen {
		t.Fatal("powering")
	}
	// Not done in PowerWait: the request goes and the screen comes back
	h.advance(PowerWait + time.Second)
	h.u.Tick()
	if h.u.Power() != nil || h.u.powering != "" || changed != 2 || !strings.Contains(h.u.hint, "apagar") {
		t.Fatalf("after PowerWait: %+v %q %d", h.u.Power(), h.u.hint, changed)
	}
}

// ScreenTouchTest.test_press_highlight
func TestPressHighlight(t *testing.T) {
	// What is touched stays highlighted while the finger is on it and at least PressMin after lifting it
	h := newHarness(t)
	x, y := h.at(2)
	h.u.OnTouch("down", x, y, 0)
	if h.u.press == nil || h.u.press.hit != (Hit{Kind: HitTile, Slot: 2}) {
		t.Fatalf("press %+v", h.u.press)
	}
	h.u.OnTouch("up", x, y, secs(0.05))
	h.u.Tick()
	if h.u.press == nil { // lifted at once: still seen
		t.Fatal("the highlight went at once")
	}
	h.u.press.since = h.u.press.since.Add(-PressMin)
	h.u.press.until = h.u.press.until.Add(-PressMin)
	h.u.Dirty = false
	h.u.Tick()
	if h.u.press != nil || !h.u.Dirty { // and redrawn without it
		t.Fatal("the highlight stays")
	}
	// Locked, only the padlock lights up
	h.setLocked(true)
	x, y = h.at("full")
	h.u.OnTouch("down", x, y, 0)
	if h.u.press != nil {
		t.Fatal("locked: full lights up")
	}
	x, y = h.at("lock")
	h.u.OnTouch("down", x, y, 0)
	if h.u.press == nil || h.u.press.hit != (Hit{Kind: HitButton, Key: "lock"}) {
		t.Fatal("the padlock does not light up")
	}
	h.u.OnTouch("up", x, y, secs(0.1))
}

func camStatus() *NetStatus {
	t := true
	return &NetStatus{Enabled: true, Cameras: 1, AccessPoint: AccessPoint{Enabled: &t, Policy: "always", State: "on"}}
}

// ScreenTouchTest.test_settings_panel
func TestSettingsPanel(t *testing.T) {
	h := newHarness(t)
	net := h.net
	net.snap.Status, net.snap.API = camStatus(), Yes
	h.press("settings", 0.1)
	if !h.u.settingsOpen || !net.panel || h.u.settingsPage != "wifi" {
		t.Fatal("Settings does not open")
	}
	h.press("lock", 0.1) // outside the panel (the padlock): closes it and nothing else
	if h.u.settingsOpen || net.panel || len(h.cmds) != 0 {
		t.Fatal("outside the panel")
	}
	h.press("settings", 0.1)
	// Wifi tab: «Crear mi wifi» (the app: PUT /network/wifi); without the client wifi, no client row
	h.press("s_ap", 0.1)
	if !eqStrings(net.calls, []string{"ap rescue"}) {
		t.Fatal(net.calls)
	}
	net.snap.Status.AccessPoint.Policy, net.snap.Status.AccessPoint.State = "rescue", "off"
	h.press("s_ap", 0.1)
	if net.calls[len(net.calls)-1] != "ap always" {
		t.Fatal(net.calls)
	}
	if strings.Contains(fmt.Sprint(h.u.settingsKeys(&net.snap)), "s_client") {
		t.Fatal("s_client without the client wifi")
	}
	h.press("s_net", 0.1) // the reception is not in this tab
	for _, c := range net.calls {
		if strings.HasPrefix(c, "net") {
			t.Fatal("s_net in the wifi tab")
		}
	}
	net.calls = net.calls[:2]
	// Cameras tab: the reception switch (off and on)
	h.press("s_page_cams", 0.1)
	if h.u.settingsPage != "cams" {
		t.Fatal("tab")
	}
	h.press("s_net", 0.1)
	if net.calls[len(net.calls)-1] != "net false" || !h.u.settingsOpen { // tapping inside does not close
		t.Fatal(net.calls)
	}
	net.snap.Status.Enabled = false
	h.press("s_net", 0.1)
	if net.calls[len(net.calls)-1] != "net true" {
		t.Fatal(net.calls)
	}
	h.press("s_ap", 0.1) // «Crear mi wifi» is not in this tab
	for _, c := range net.calls[2:] {
		if strings.HasPrefix(c, "ap") {
			t.Fatal("s_ap in the cams tab")
		}
	}
	net.calls = net.calls[:4]
	// Without cameras and the reception on, or without the API (an older app), no switch; without cameras and the
	// reception off, the switch only to turn it on (the app adds a camera only with the reception on)
	net.snap.Status.Cameras, net.snap.Status.Enabled = 0, true
	h.press("s_net", 0.1)
	if len(net.calls) != 4 {
		t.Fatal(net.calls)
	}
	net.snap.Status.Enabled = false
	h.press("s_net", 0.1)
	if len(net.calls) != 5 || net.calls[4] != "net true" {
		t.Fatal(net.calls)
	}
	net.calls = net.calls[:4]
	net.snap.Status, net.snap.API = nil, No
	h.press("s_net", 0.1)
	h.press("s_page_wifi", 0.1)
	h.press("s_ap", 0.1)
	if len(net.calls) != 4 {
		t.Fatal(net.calls)
	}
	if !h.u.SecretOnScreen() { // open: screen.jpg is not updated
		t.Fatal("secret")
	}
	h.press("s_close", 0.1)
	if h.u.settingsOpen || !h.u.SecretOnScreen() { // nor right after closing (the compositor lags)
		t.Fatal("after closing")
	}
	h.u.secretUntil = time.Time{}
	if h.u.SecretOnScreen() {
		t.Fatal("secret after the grace")
	}
	// Locked: Settings does not open
	h.setLocked(true)
	h.press("settings", 0.1)
	if h.u.settingsOpen {
		t.Fatal("Settings opens locked")
	}
}

func ip(v int) *int   { return &v }
func bp(v bool) *bool { return &v }

// ScreenTouchTest.test_settings_rows_text
func TestSettingsRowsText(t *testing.T) {
	snap := func(ap AccessPoint, pend *Pending) *NetSnapshot {
		return &NetSnapshot{API: Yes, Status: &NetStatus{AccessPoint: ap}, Pending: pend}
	}
	on := AccessPoint{Policy: "always", State: "on", SSID: "EnhancedIRL-A1B2", Channel: ip(6), Clients: ip(2)}
	check := func(got, want RowState) {
		t.Helper()
		if got != want {
			t.Errorf("%+v, want %+v", got, want)
		}
	}
	check(APRow(snap(on, nil), nil, Spanish), RowState{KnobOn, false, "EnhancedIRL-A1B2 · canal 6 · 2 aparatos", "good"})
	check(APRow(snap(AccessPoint{Policy: "rescue", State: "off"}, nil), nil, Spanish), RowState{KnobOff, false, "Apagada", "quiet"})
	check(APRow(snap(AccessPoint{Policy: "rescue", State: "off"}, &Pending{What: "ap", On: true}), nil, Spanish),
		RowState{KnobOn, true, "Encendiendo…", "quiet"})
	if APRow(snap(on, &Pending{What: "net"}), nil, Spanish).Knob != KnobOn { // another change: not this one
		t.Error("another change")
	}
	check(APRow(snap(AccessPoint{Policy: "always", State: "error", Error: "ap0 no sube"}, nil), nil, Spanish),
		RowState{KnobOn, false, "No se ha podido encender: ap0 no sube", "warn"})
	// An app without «Crear mi wifi» (no policy): no switch, says how it is
	r := APRow(snap(AccessPoint{Enabled: bp(true), State: "on", SSID: "X", Clients: ip(0)}, nil), nil, Spanish)
	if r.Knob != KnobNone || r.Tone != "good" {
		t.Errorf("%+v", r)
	}
	cl := func(mod func(c *WifiClient)) *NetSnapshot {
		c := &WifiClient{Enabled: true, State: "connected", SSID: "Bar", Signal: ip(70), Band: "5", Internet: bp(true)}
		if mod != nil {
			mod(c)
		}
		return &NetSnapshot{Status: &NetStatus{}, Client: c}
	}
	check(ClientRow(cl(nil), Spanish), RowState{KnobOn, false, "Bar · señal buena · 5 GHz", "good"})
	r = ClientRow(cl(func(c *WifiClient) { c.Internet, c.Signal, c.Band = bp(false), ip(20), "2.4" }), Spanish)
	if r.Sub != "Bar · señal débil · 2,4 GHz · sin internet" || r.Tone != "warn" {
		t.Errorf("%+v", r)
	}
	if r := ClientRow(cl(func(c *WifiClient) { c.State, c.Error = "error", "wrong_password" }), Spanish); r.Sub != "Contraseña incorrecta para Bar" {
		t.Errorf("%+v", r)
	}
	if r := ClientRow(cl(func(c *WifiClient) { c.Enabled, c.State = false, "off" }), Spanish); r.Knob != KnobOff || r.Busy || r.Sub != "Apagada" {
		t.Errorf("%+v", r)
	}
	if r := ClientRow(cl(func(c *WifiClient) { c.State = "connecting" }), Spanish); r.Sub != "Conectando a Bar…" {
		t.Errorf("%+v", r)
	}
	// The header's antenna: only with the client on
	ant := func(mod func(c *WifiClient)) string {
		n := cl(mod)
		l, tone, ok := Antenna(n)
		if !ok {
			return "none"
		}
		return fmt.Sprintf("%d %s", l, tone)
	}
	for _, c := range []struct {
		mod  func(c *WifiClient)
		want string
	}{{nil, "3 ink"}, {func(c *WifiClient) { c.Signal = ip(20) }, "1 ink"}, {func(c *WifiClient) { c.Internet = bp(false) }, "3 warn"},
		{func(c *WifiClient) { c.State = "disconnected" }, "0 quiet"}, {func(c *WifiClient) { c.State = "connecting" }, "0 quiet"},
		{func(c *WifiClient) { c.State = "error" }, "0 warn"}, {func(c *WifiClient) { c.Enabled, c.State = false, "off" }, "none"}} {
		if got := ant(c.mod); got != c.want {
			t.Errorf("antenna %q, want %q", got, c.want)
		}
	}
	if _, _, ok := Antenna(&NetSnapshot{}); ok {
		t.Error("antenna without a client")
	}
	n := cl(nil)
	n.Status = nil // the app does not answer: the last one is no longer valid
	if _, _, ok := Antenna(n); ok {
		t.Error("antenna without the app")
	}
}

// wifiState is the app's state with the client wifi on and a list of networks (ScreenTouchTest.wifi_state).
func (h *harness) wifiState() *fakeNet {
	net := h.net
	net.snap.API = Yes
	net.snap.Status = &NetStatus{Enabled: true, AccessPoint: AccessPoint{Enabled: bp(true), Policy: "always", State: "on"}}
	net.snap.Client = &WifiClient{Enabled: true, State: "disconnected"}
	net.snap.Scan = []ScanNet{{SSID: "Conocida", Signal: 80, Security: "wpa2", Band: "5", Known: true},
		{SSID: "Nueva", Signal: 60, Security: "wpa2", Band: "2.4"}, {SSID: "Abierta", Signal: 50, Security: "open", Band: "2.4"},
		{SSID: "Moderna", Signal: 40, Security: "wpa3", Band: "5"}}
	return net
}

// ScreenTouchTest.test_networks_list
func TestNetworksList(t *testing.T) {
	h := newHarness(t)
	net := h.wifiState()
	h.press("settings", 0.1)
	if !strings.Contains(fmt.Sprint(h.u.settingsKeys(&net.snap)), "s_networks") { // «Redes ›», with the client on
		t.Fatal("no Redes")
	}
	h.press("s_networks", 0.1)
	if h.u.settingsView != "networks" || net.sent[len(net.sent)-1] != "scan true" {
		t.Fatal(h.u.settingsView, net.sent)
	}
	h.u.listShown, h.u.listForget = []string{"Conocida", "Nueva", "Abierta", "Moderna"}, map[int]bool{0: true}
	h.press("s_row_0", 0.1) // known: with the saved profile, no keyboard
	if !eqStrings(net.last(net.sent, 2), []string{"Conocida <none>", "scan false"}) || h.u.settingsView != "main" ||
		h.u.connecting.ssid != "Conocida" {
		t.Fatal(net.sent, h.u.settingsView)
	}
	h.press("s_networks", 0.1)
	h.press("s_row_2", 0.1) // open: also at once
	if net.sent[len(net.sent)-2] != "Abierta <none>" {
		t.Fatal(net.sent)
	}
	h.press("s_networks", 0.1)
	h.press("s_row_3", 0.1) // WPA3: not possible, says so
	if h.u.settingsView != "networks" || !strings.Contains(net.snap.Message, "WPA3") {
		t.Fatal(h.u.settingsView, net.snap.Message)
	}
	// The list reorders between down and up: the network seen when the finger went down counts
	x, y := h.at("s_row_1")
	h.u.OnTouch("down", x, y, 0)
	h.u.listShown = []string{"Abierta", "Nueva"}
	h.u.OnTouch("up", x, y, secs(0.1))
	if h.u.settingsView != "keyboard" || h.u.kb.ssid != "Nueva" { // with a password: the keyboard
		t.Fatal(h.u.settingsView)
	}
	h.press("k_cancel", 0.1)
	if h.u.settingsView != "networks" || h.u.kb != nil {
		t.Fatal("cancel")
	}
	// Forget: with a second tap before ConfirmFor
	h.u.listShown = []string{"Conocida", "Nueva", "Abierta", "Moderna"}
	h.press("s_forget_0", 0.1)
	if h.u.confirm == nil || h.u.confirm.ssid != "Conocida" || strings.Contains(fmt.Sprint(net.sent), "forget Conocida") {
		t.Fatal("first tap")
	}
	h.press("s_forget_0", 0.1)
	if net.sent[len(net.sent)-1] != "forget Conocida" {
		t.Fatal(net.sent)
	}
	h.press("s_forget_0", 0.1) // expired: asks for the second tap again
	h.u.confirm.until = time.Time{}
	h.u.Tick()
	if h.u.confirm != nil {
		t.Fatal("confirm stays")
	}
	// With the saved password just failed, the known one opens the keyboard
	net.snap.Client = &WifiClient{Enabled: true, State: "error", Error: "wrong_password", SSID: "Conocida"}
	h.press("s_row_0", 0.1)
	if h.u.settingsView != "keyboard" || h.u.kb.ssid != "Conocida" || len(h.cmds) != 0 { // none of this goes to the engine
		t.Fatal(h.u.settingsView, h.cmds)
	}
}

// ScreenTouchTest.test_keyboard_secret
func TestKeyboardSecret(t *testing.T) {
	h := newHarness(t)
	net := h.wifiState()
	h.press("settings", 0.1)
	h.u.openKeyboard("Nueva", "")
	if !h.u.SecretOnScreen() { // the keyboard is inside Settings: screen.jpg frozen
		t.Fatal("secret")
	}
	for _, ch := range []string{"a", "b", "c", "SHIFT"} {
		h.tapKey(ch, 0.1)
	}
	if h.u.kb.layer != "upper" {
		t.Fatal(h.u.kb.layer)
	}
	for _, k := range []string{"D", "SYM", "1", "SYM2", "{", "ABC", " "} {
		h.tapKey(k, 0.1)
	}
	if string(h.u.kb.chars()) != "abcD1{ " {
		t.Fatalf("%q", h.u.kb.chars())
	}
	h.tapKey("BKSP", 0.1)
	if h.u.kb.n != 6 {
		t.Fatal(h.u.kb.n)
	}
	h.tapKey("OK", 0.1) // fewer than 8: not sent
	if !eqStrings(net.sent, []string{"scan false"}) || !strings.Contains(h.u.kb.err, "8") {
		t.Fatal(net.sent, h.u.kb.err)
	}
	kb := h.u.kb
	for _, ch := range []string{"x", "y", "z"} {
		h.tapKey(ch, 0.1)
	}
	h.tapKey("OK", 0.1)
	if net.sent[len(net.sent)-1] != "Nueva abcD1{xyz" { // straight to the app (Net), not to the engine
		t.Fatal(net.sent)
	}
	if kb.n != 0 || !allZero(kb.buf) || h.u.kb != nil || h.u.settingsView != "main" || len(h.cmds) != 0 {
		t.Fatal("the password stays in memory") // out of memory
	}
	// At most PSKMax; holding erase empties it
	h.u.openKeyboard("Nueva", "")
	for range PSKMax + 5 {
		h.u.kbKey("a", 0)
	}
	if h.u.kb.n != PSKMax {
		t.Fatal(h.u.kb.n)
	}
	h.tapKey("BKSP", TapLong.Seconds()+0.1)
	if h.u.kb.n != 0 {
		t.Fatal(h.u.kb.n)
	}
	// Also wiped when Settings closes and when locking
	h.u.kbKey("q", 0)
	kb = h.u.kb
	h.u.openSettings(false)
	if !allZero(kb.buf) || h.u.kb != nil || h.u.settingsView != "main" {
		t.Fatal("closing Settings keeps the password")
	}
	h.press("settings", 0.1)
	h.u.openKeyboard("Nueva", "")
	h.u.kbKey("q", 0)
	kb = h.u.kb
	h.setLocked(true) // locked from the web
	h.u.Tick()
	if !allZero(kb.buf) || h.u.kb != nil || h.u.settingsOpen {
		t.Fatal("locking keeps the password")
	}
}

// ScreenTouchTest.test_wrong_password_reopens
func TestWrongPasswordReopens(t *testing.T) {
	h := newHarness(t)
	net := h.wifiState()
	h.press("settings", 0.1)
	h.u.connecting = &connecting{ssid: "Nueva", since: h.now()}
	// The previous attempt's error (before «conectando»): it does not count
	net.snap.Client = &WifiClient{Enabled: true, State: "error", Error: "wrong_password", SSID: "Nueva"}
	h.u.Tick()
	if h.u.settingsView != "main" {
		t.Fatal(h.u.settingsView)
	}
	net.snap.Client = &WifiClient{Enabled: true, State: "connecting", SSID: "Nueva"}
	h.u.Tick()
	net.snap.Client = &WifiClient{Enabled: true, State: "error", Error: "wrong_password", SSID: "Nueva"}
	h.u.Tick()
	if h.u.settingsView != "keyboard" || h.u.kb.n != 0 || h.u.connecting != nil || !strings.Contains(h.u.kb.err, "incorrecta") {
		t.Fatal(h.u.settingsView)
	}
	// Connected: no longer watched
	h.u.openSettings(false)
	h.u.connecting = &connecting{ssid: "Nueva", since: h.now(), seen: true}
	net.snap.Client = &WifiClient{Enabled: true, State: "connected", SSID: "Nueva"}
	h.u.Tick()
	if h.u.connecting != nil {
		t.Fatal("still watching")
	}
}

// ScreenTouchTest.test_client_switch
func TestClientSwitch(t *testing.T) {
	h := newHarness(t)
	net := h.wifiState()
	h.press("settings", 0.1)
	h.press("s_client", 0.1)
	if !eqStrings(net.calls, []string{"client false"}) {
		t.Fatal(net.calls)
	}
	net.snap.Client = nil // an app without the client wifi (or without wlan0): no row
	if strings.Contains(fmt.Sprint(h.u.settingsKeys(&net.snap)), "s_client") {
		t.Fatal("s_client")
	}
	if h.lay.Buttons["s_ap"].Y != h.lay.Buttons["s_client"].Y { // and «Crear mi wifi» moves up
		t.Fatal("s_ap does not move up")
	}
}

// ScreenTouchTest.test_review_fixes
func TestReviewFixes(t *testing.T) {
	h := newHarness(t)
	net := h.wifiState()
	net.snap.Client = nil
	h.press("settings", 0.1)
	// The rows move between down and up (the client row appears): nothing is touched on release
	x, y := h.at("s_ap")
	h.u.OnTouch("down", x, y, 0)
	net.snap.Client = &WifiClient{Enabled: true, State: "connected", SSID: "Bar"}
	h.u.OnTouch("up", x, y, secs(0.1))
	if len(net.calls) != 0 {
		t.Fatal(net.calls)
	}
	// The list's row changes from known to unknown: neither forget nor connect
	h.u.openNetworks()
	h.u.listShown, h.u.listForget = []string{"Conocida"}, map[int]bool{0: true}
	x, y = h.at("s_forget_0")
	h.u.OnTouch("down", x, y, 0)
	h.u.listShown, h.u.listForget = []string{"Nueva"}, map[int]bool{}
	h.u.OnTouch("up", x, y, secs(0.1))
	if h.u.confirm != nil || h.u.settingsView != "networks" {
		t.Fatal("forget/connect on a moved row")
	}
	for _, s := range net.sent {
		if strings.HasPrefix(s, "forget") || strings.HasPrefix(s, "Conocida") || strings.HasPrefix(s, "Nueva") {
			t.Fatal(net.sent)
		}
	}
	// The list's entries: the top edge of «Cámaras de red» is its own, not «Wifi»'s
	h.u.closeNetworks()
	r := h.lay.Buttons["s_page_cams"]
	fx, fy := float64(r.X+10)/1024, float64(r.Y+1)/600
	h.u.OnTouch("down", fx, fy, 0)
	h.u.OnTouch("up", fx, fy, secs(0.1))
	if h.u.settingsPage != "cams" {
		t.Fatal(h.u.settingsPage)
	}
	// The connect POST fails (404, 409…): the old wrong_password does not reopen the keyboard
	h.u.settingsPage = "wifi"
	t0 := h.now().Add(-10 * time.Second)
	h.u.connecting = &connecting{ssid: "Nueva", since: t0}
	net.snap.Client = &WifiClient{Enabled: true, State: "error", Error: "wrong_password", SSID: "Nueva"}
	net.snap.LastPost = &LastPost{What: "connect", SSID: "Nueva", OK: false, At: t0.Add(time.Second)}
	h.u.Tick()
	if h.u.connecting != nil || h.u.settingsView != "main" {
		t.Fatal("a failed POST reopens")
	}
	// Taken (202) but without seeing «conectando»: the error counts 5 s after the POST
	h.u.connecting = &connecting{ssid: "Nueva", since: t0}
	net.snap.LastPost = &LastPost{What: "connect", SSID: "Nueva", OK: true, At: t0.Add(time.Second)}
	h.u.Tick()
	if h.u.settingsView != "keyboard" {
		t.Fatal(h.u.settingsView)
	}
	// With the keyboard open, the screen is unplugged: Settings closes and what was typed goes
	h.u.kbKey("q", 0)
	kb := h.u.kb
	h.u.State = "off"
	h.u.Tick()
	if !allZero(kb.buf) || h.u.kb != nil || h.u.settingsOpen {
		t.Fatal("unplugged with the keyboard")
	}
	h.u.State = "on"
	// A switch the app cannot take now says so
	net.changeOK = false
	h.press("settings", 0.1)
	h.press("s_ap", 0.1)
	if !strings.Contains(net.snap.Message, "otro cambio") {
		t.Fatal(net.snap.Message)
	}
}

// ScreenTouchTest.test_audio_view_follow_only_single
func TestAudioViewFollowOnlySingle(t *testing.T) {
	h := newHarness(t)
	h.u.view = "audio"
	st := *h.u.st
	st.Audio = &AudioStatus{Mode: "mix"}
	h.u.SetStatus(&st)
	h.press("a_follow", 0.1) // not shown in the mix: does nothing
	if len(h.cmds) != 0 {
		t.Fatal(h.cmds)
	}
	st.Audio.Mode = "single"
	h.press("a_follow", 0.1)
	if got := h.cmdNames(); !eqStrings(got, []string{"map[cmd:audio_follow on:true]"}) {
		t.Fatal(got)
	}
	h.press("a_back", 0.1) // the arrow goes back to the video view
	if h.u.view != "video" {
		t.Fatal(h.u.view)
	}
	sg, mx := h.lay.Buttons["a_single"], h.lay.Buttons["a_mix"]
	if mx.X-(sg.X+sg.W) < h.lay.M { // Una fuente | Mezcla, apart
		t.Fatal("too close")
	}
}

// ScreenTouchTest.test_wifi_down_dot
func TestWifiDownDot(t *testing.T) {
	snap := func(st *NetStatus) *NetSnapshot { return &NetSnapshot{API: Yes, Status: st} }
	// With «Crear mi wifi» (policy): asked (always) and down, with cameras or not
	for _, c := range []struct {
		state string
		down  bool
	}{{"off", true}, {"error", true}, {"starting", false}, {"on", false}} {
		if got := WifiDown(snap(&NetStatus{AccessPoint: AccessPoint{Policy: "always", State: c.state}}), nil); got != c.down {
			t.Errorf("%s: %v", c.state, got)
		}
	}
	if WifiDown(snap(&NetStatus{AccessPoint: AccessPoint{Policy: "rescue", State: "off"}}), nil) {
		t.Error("rescue")
	}
	// An older app (no policy): the cameras' wifi
	st := func(ap AccessPoint, cams int) *NetStatus {
		return &NetStatus{Enabled: true, AccessPoint: ap, Cameras: cams}
	}
	if !WifiDown(snap(st(AccessPoint{Enabled: bp(true), State: "error"}, 1)), nil) {
		t.Error("cameras' wifi down")
	}
	if WifiDown(snap(st(AccessPoint{Enabled: bp(true), State: "on"}, 1)), nil) ||
		WifiDown(snap(st(AccessPoint{Enabled: bp(false), State: "off"}, 1)), nil) ||
		WifiDown(snap(st(AccessPoint{Enabled: bp(true), State: "error"}, 0)), nil) { // without cameras there is no AP
		t.Error("cameras' wifi")
	}
	// No API (wifi.json): its «on»
	if !WifiDown(&NetSnapshot{API: No}, &WifiCreds{On: false}) || WifiDown(&NetSnapshot{API: No}, &WifiCreds{On: true}) {
		t.Error("wifi.json")
	}
}

// ScreenTouchTest.test_locked_only_unlocks
func TestLockedOnlyUnlocks(t *testing.T) {
	h := newHarness(t)
	h.setLocked(true)
	for _, k := range []any{0, "full", "pause", "emit"} {
		h.press(k, 0.1)
	}
	h.press("emit", Hold.Seconds()+0.1)
	h.press("lock", 0.2)
	if len(h.cmds) != 0 {
		t.Fatal(h.cmds)
	}
	h.press("lock", Hold.Seconds()+0.1)
	if got := h.cmdNames(); !eqStrings(got, []string{"map[cmd:lock on:false]"}) {
		t.Fatal(got)
	}
}

// A held command the engine refuses is shown (Python: «No se puede: …»); a tap's is only logged.
func TestCommandDone(t *testing.T) {
	h := newHarness(t)
	h.u.CommandDone(Command{Msg: map[string]any{"cmd": "output"}, Origin: "hold"}, false, "unsupported")
	if h.u.hint != "No se puede: unsupported" {
		t.Fatal(h.u.hint)
	}
	h.u.hint = ""
	h.u.CommandDone(Command{Msg: map[string]any{"cmd": "take"}, Origin: "touch"}, false, "hidden_source")
	if h.u.hint != "" {
		t.Fatal(h.u.hint)
	}
}

// allZero: every byte of b is zero (a wiped secret).
func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
