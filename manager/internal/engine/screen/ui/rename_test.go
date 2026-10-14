package ui

import (
	"slices"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
)

// The screen checks the AP's name with the app's rule before sending it
func TestSSIDOKMatchesTheApp(t *testing.T) {
	for _, s := range []string{"", "a", "EnhancedIRL-A1B2", "Studio 2", "my_net.v2", " lead", "trail ", "a  b", "Plató",
		"a;b", "x#1", "12345678901234567890123456789012", "123456789012345678901234567890123", "-", "a\tb", "ü"} {
		if SSIDOK(s) != netcfg.ValidSSID(s) {
			t.Errorf("%q: screen %v, app %v", s, SSIDOK(s), netcfg.ValidSSID(s))
		}
	}
}

// Renaming «Crear mi wifi»: «Cambiar nombre» on the Wi-Fi page opens the keyboard with the name; Done sends it to
// the app (Net.SetSSID, never the engine) once it is valid and different
func TestRenameAP(t *testing.T) {
	h := newHarness(t)
	net := h.wifiState()
	h.press("settings", 0.1)
	h.u.openPage("wifi")
	// Without the name (an app that does not write it at its first start, with the AP off): no button
	if slices.Contains(h.u.settingsKeys(&net.snap), "s_ssid") {
		t.Fatal("rename without a name")
	}
	net.snap.Wifi = &WifiCreds{SSID: "EnhancedIRL-A1B2", Password: "abcdEFGH1234abcdEFGH"}
	if !slices.Contains(h.u.settingsKeys(&net.snap), "s_ssid") {
		t.Fatal(h.u.settingsKeys(&net.snap))
	}
	h.press("s_ssid", 0.1)
	kb := h.u.kb
	if kb == nil || kb.mode != "ssid" || string(kb.text) != "EnhancedIRL-A1B2" || kb.max != SSIDMax || kb.secret() {
		t.Fatalf("keyboard: %+v", kb)
	}
	if h.u.kbTitle() != Spanish.SSIDKeyboard || h.u.kbHelp() != Spanish.SSIDHelp {
		t.Errorf("title %q help %q", h.u.kbTitle(), h.u.kbHelp())
	}
	h.tapKey("BKSP", TapLong.Seconds()+0.1) // held: all of it
	for _, ch := range "Studio #2" {
		h.u.kbKey(string(ch), 0)
	}
	h.tapKey("OK", 0.1) // «#» is not in a name: says why, sends nothing
	if h.u.kb == nil || h.u.kb.err != Spanish.SSIDBad || len(net.calls) != 0 {
		t.Fatalf("invalid: %+v %v", h.u.kb, net.calls)
	}
	for _, k := range []string{"BKSP", "BKSP", "2"} { // «Studio #2» → «Studio 2»
		h.u.kbKey(k, 0)
	}
	h.tapKey("OK", 0.1)
	if !eqStrings(net.calls, []string{"ssid Studio 2"}) || h.u.kb != nil || h.u.settingsView != "main" || len(h.cmds) != 0 {
		t.Fatalf("rename: %v kb %v view %q cmds %v", net.calls, h.u.kb, h.u.settingsView, h.cmds)
	}
	// The same name (spaces around are trimmed): closes without sending
	net.snap.Wifi.SSID = "Studio 2"
	h.press("s_ssid", 0.1)
	h.u.kbKey(" ", 0)
	h.tapKey("OK", 0.1)
	if len(net.calls) != 1 || h.u.kb != nil {
		t.Fatalf("same name: %v %v", net.calls, h.u.kb)
	}
	// The app busy with another change: says so and keeps what was typed
	h.press("s_ssid", 0.1)
	h.u.kbKey("X", 0)
	net.changeOK = false
	h.tapKey("OK", 0.1)
	if h.u.kb == nil || h.u.kb.err != Spanish.BusyChange || string(h.u.kb.text) != "Studio 2X" {
		t.Fatalf("busy: %+v", h.u.kb)
	}
	net.changeOK = true
	h.tapKey("k_cancel", 0.1)
	if h.u.kb != nil || len(net.calls) != 1 {
		t.Fatal("cancel")
	}
	// A change under way: no keyboard, the notice
	net.snap.Pending = &Pending{What: "ssid", SSID: "Studio 3"}
	h.press("s_ssid", 0.1)
	if h.u.kb != nil || net.snap.Message != Spanish.BusyChange {
		t.Fatalf("pending: %+v %q", h.u.kb, net.snap.Message)
	}
}

// The wizard's network step is the Wi-Fi page: the name and «Cambiar nombre» are there too, also with the AP off
func TestWizardRenameAP(t *testing.T) {
	h := newHarness(t)
	app := wizardApp(h)
	net := h.wifiState()
	net.snap.Status.AccessPoint = AccessPoint{Enabled: bp(true), Policy: "rescue", State: "off"}
	net.snap.Wifi = &WifiCreds{SSID: "EnhancedIRL-A1B2", Password: "abcdEFGH1234abcdEFGH"}
	app.snap.Setup.Steps[0].State, app.snap.Setup.Steps[1].State = "done", "done"
	h.u.Tick()
	if h.u.wizardStep() != "network" || !slices.Contains(h.u.wizardKeys(&net.snap), "s_ssid") {
		t.Fatalf("%q %v", h.u.wizardStep(), h.u.wizardKeys(&net.snap))
	}
	h.press("s_ssid", 0.1)
	if h.u.kb == nil || h.u.kb.mode != "ssid" {
		t.Fatal("keyboard in the wizard")
	}
	h.u.kbKey("X", 0)
	h.tapKey("OK", 0.1)
	if !eqStrings(net.calls, []string{"ssid EnhancedIRL-A1B2X"}) || h.u.kb != nil || h.u.wizardStep() != "network" {
		t.Fatalf("%v %q", net.calls, h.u.wizardStep())
	}
	for _, a := range app.did {
		if a.What == "setup_step" {
			t.Errorf("renaming moved the wizard on: %+v", a)
		}
	}
}

// «Rename» fits a finger, stays in the QR area, beside the QR (at most as wide as the area is high) and under the
// rows, on every screen and with or without the client row
func TestRenameLayout(t *testing.T) {
	for _, sz := range [][2]int{{1024, 600}, {1280, 720}, {1920, 1080}} {
		lay := NewLayout(sz[0], sz[1], 4)
		for _, client := range []bool{true, false} {
			lay.SettingsRows(client)
			b, q := lay.Buttons["s_ssid"], lay.SettingsQR
			if b.H < 44 || b.W < 200 {
				t.Errorf("%v: %v too small", sz, b)
			}
			if !q.Inside(b.X, b.Y, 0) || !q.Inside(b.X+b.W-1, b.Y+b.H-1, 0) {
				t.Errorf("%v client=%v: %v outside the QR area %v", sz, client, b, q)
			}
			if b.X < q.X+q.H+lay.M {
				t.Errorf("%v: %v on the QR (its side up to %d)", sz, b, q.H)
			}
			for _, k := range []string{"s_ap", "s_client", "s_networks"} {
				if (client || k == "s_ap") && b.overlaps(lay.Buttons[k]) {
					t.Errorf("%v: %v overlaps %s", sz, b, k)
				}
			}
		}
	}
}
