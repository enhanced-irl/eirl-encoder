package ui

import (
	"testing"
	"time"
)

func fv(v float64) *float64 { return &v }

// modemsStatus: Ethernet and two modems on air, and a third whose SIM asks for its PIN (without a link).
func modemsStatus(transport string) *StatusInfo {
	all := []string{"apn", "roaming", "mode", "pin", "reset"}
	return &StatusInfo{Transport: transport, HasUplinks: true,
		Uplinks: []UplinkInfo{
			{Name: "eth0", IP: "192.168.1.20", Kind: "ethernet", InUse: true, TxKbps: 2100, Health: "good", HealthCode: "ok"},
			{Name: "wwan0", IP: "10.64.12.7", Kind: "modem", ModemID: "m-5a1d0c3e9b21", Label: "Movistar", InUse: true,
				TxKbps: 2400, Health: "good", HealthCode: "ok"},
			{Name: "wwan1", IP: "10.70.3.9", Kind: "modem", ModemID: "m-0c7e44a19f02", Label: "Telefónica Empresas", InUse: true,
				TxKbps: 700, Health: "weak", HealthCode: "signal_weak"},
		},
		Modems: []ModemInfo{
			{ID: "m-5a1d0c3e9b21", Iface: "wwan0", Manufacturer: "Quectel", Model: "RM520N-GL", State: "connected", SIM: "ready",
				Registration: "home", Operator: "Movistar", Tech: "5g-nsa", RSRP: fv(-88), RSRQ: fv(-9), SINR: fv(14),
				APNAuto: "telefonica.es", Mode: "auto", Modes: []string{"auto", "4g", "5g"}, Can: all},
			{ID: "m-0c7e44a19f02", Iface: "wwan1", Manufacturer: "Quectel", Model: "EG25", State: "connected", SIM: "ready",
				Registration: "roaming", Operator: "Telefónica Empresas", Tech: "4g", RSRP: fv(-121), RSRQ: fv(-19.5), SINR: fv(-6),
				APN: "internet.empresas.example", Roaming: true, Mode: "4g", Modes: []string{"auto", "4g"}, Can: all},
			{ID: "m-7d3e2a1b9c04", Backend: "mm", Manufacturer: "Quectel", Model: "EC25", State: "locked", SIM: "pin", Mode: "auto",
				Modes: []string{"auto", "4g"}, Can: all},
		},
		Links: []LinkDot{{Name: "m-7d3e2a1b9c04", Kind: "modem", Health: "bad", Code: "sim_pin"}},
	}
}

func modemsHarness(t *testing.T, transport string) (*harness, *fakeApp) {
	h, app := pagesHarness(t, "uplinks")
	app.snap.Status, app.snap.StatusAPI = modemsStatus(transport), Yes
	h.u.Dirty = true
	h.u.Tick()
	return h, app
}

// Streaming › Connections with modems, and each modem's page, in every language (no table text cut)
func TestModemPagesRender(t *testing.T) {
	open := func(id string) func(h *harness) {
		return func(h *harness) {
			pageApp(h, "uplinks", func(a *AppSnapshot) { a.Status, a.StatusAPI = modemsStatus("srtla"), Yes })
			if id != "" {
				h.u.openModem(id)
			}
		}
	}
	checkRenders(t, []renderView{
		{"modems", open("")},
		{"modem-5g", open("m-5a1d0c3e9b21")},
		{"modem-roaming", open("m-0c7e44a19f02")},
		{"modem-pin", open("m-7d3e2a1b9c04")},
		{"modem-gone", open("m-000000000000")},
		{"modem-reset", func(h *harness) {
			open("m-5a1d0c3e9b21")(h)
			h.u.confirmTap("modem_reset")
		}},
		{"modem-force", func(h *harness) {
			open("m-5a1d0c3e9b21")(h)
			h.u.confirmTap("modem_force:roam")
		}},
		{"modem-unlock-picker", func(h *harness) {
			open("m-7d3e2a1b9c04")(h)
			h.u.App.(*fakeApp).snap.Status.Modems[2].Backend = "hilink"
			h.u.modemTouch("s_mdm_pin")
		}},
		{"modem-twin-subnet", func(h *harness) {
			open("")(h)
			st := h.u.App.(*fakeApp).snap.Status
			st.Uplinks[2].Conflict = true
			st.Modems[1].Can = []string{"subnet"}
			h.u.openModem("m-0c7e44a19f02")
			h.u.confirmTap("modem_subnet")
		}},
		{"modem-saved", func(h *harness) {
			open("m-5a1d0c3e9b21")(h)
			h.advance(time.Millisecond)
			h.u.App.(*fakeApp).snap.Last = &AppResult{What: "modem_subnet", OK: true, At: h.now()}
		}},
	})
}

// «›» opens the modem's page; its title is its name and technology; ← goes back to the list
func TestModemOpenAndBack(t *testing.T) {
	h, _ := modemsHarness(t, "srt")
	keys := h.u.settingsKeys(nil)
	if !hasKey(keys, "s_uplm_1") || !hasKey(keys, "s_uplm_2") || hasKey(keys, "s_uplm_0") {
		t.Fatalf("«›» only on the modems' rows: %v", keys)
	}
	h.press("s_uplm_1", 0.1)
	if h.u.modemView != "m-5a1d0c3e9b21" {
		t.Fatalf("view %q", h.u.modemView)
	}
	if title, back, ok := h.u.modemTitle(); !ok || !back || title != "Movistar · 5G" {
		t.Fatalf("%q %v %v", title, back, ok)
	}
	h.u.Tick()
	h.press("s_back", 0.1)
	if h.u.modemView != "" || h.u.settingsPage != "uplinks" {
		t.Fatalf("view %q page %q", h.u.modemView, h.u.settingsPage)
	}
	// The row of a modem without a link opens its page too
	h.u.Tick()
	h.press("s_upl_3", 0.1)
	if h.u.modemView != "m-7d3e2a1b9c04" {
		t.Fatalf("view %q", h.u.modemView)
	}
	// Leaving the page closes the modem's
	h.press("s_close", 0.1)
	if h.u.modemView != "" {
		t.Fatal("the modem's page stays open after Settings closed")
	}
}

// The switch of a modem's row still takes it out of the share (the «›» is not the row)
func TestModemRowSwitch(t *testing.T) {
	h, app := modemsHarness(t, "srt")
	h.press("s_upl_1", 0.1)
	if a := app.lastDid(); a.What != "uplink" || a.ID != "wwan0" {
		t.Fatalf("%+v", a)
	}
	if h.u.modemView != "" {
		t.Fatal("the row's switch opened the page")
	}
}

// The PIN: a secret, digits only, at least 4; it reaches the app as a secret and leaves the screen's memory
func TestModemUnlock(t *testing.T) {
	h, app := modemsHarness(t, "srt")
	h.u.openModem("m-7d3e2a1b9c04")
	h.u.Tick()
	h.press("s_mdm_pin", 0.1)
	if h.u.chooser == nil || len(h.u.chooser.opts) != 2 || h.u.chooser.opts[0].Sub != Spanish.Modem.UnlockKeep {
		t.Fatalf("no unlock picker: %+v", h.u.chooser)
	}
	h.u.pickerTouch("s_pick_0", "keep", true)
	if h.u.kb == nil || !h.u.kb.secret() || h.u.kb.layer != "sym1" {
		t.Fatalf("no secret numeric keyboard: %+v", h.u.kb)
	}
	for _, c := range "12a" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	if len(app.did) != 0 {
		t.Fatal("a 2-digit PIN was sent")
	}
	for _, c := range "34" {
		h.tapKey(string(c), 0.1)
	}
	buf := h.u.kb.buf
	h.tapKey("OK", 0.1)
	a := app.lastDid()
	if a.What != "modem_pin" || a.ID != "m-7d3e2a1b9c04" || app.secrets[len(app.secrets)-1]["pin"] != "1234" {
		t.Fatalf("%+v %v", a, app.secrets)
	}
	if _, inBody := a.Body["pin"]; inBody {
		t.Fatal("the PIN went in the body")
	}
	if !allZero(buf) || h.u.kb != nil || h.u.modemView != "m-7d3e2a1b9c04" {
		t.Fatalf("the PIN stays in memory, or the page closed: %+v %q", h.u.kb, h.u.modemView)
	}
	// Unlock and remove: the same PIN with remove; a HiLink says it will ask again
	h.u.Tick()
	app.snap.Status.Modems[2].Backend = "hilink"
	h.press("s_mdm_pin", 0.1)
	if h.u.chooser == nil || h.u.chooser.opts[0].Sub != Spanish.Modem.UnlockOnce {
		t.Fatalf("%+v", h.u.chooser)
	}
	h.u.pickerTouch("s_pick_1", "remove", true)
	for _, c := range "1234" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	if a := app.lastDid(); a.What != "modem_pin" || a.Body["remove"] != true || app.secrets[len(app.secrets)-1]["pin"] != "1234" {
		t.Fatalf("%+v", a)
	}
	// Done: said on the page for a while
	app.snap.Last = &AppResult{What: "modem_pin", OK: true, At: h.now().Add(time.Millisecond)}
	h.advance(10 * time.Millisecond)
	if got := modemDone(&app.snap, Spanish.Modem, h.u.pageSince, h.now()); got != Spanish.Modem.Unlocked {
		t.Fatalf("%q", got)
	}
	h.advance(9 * time.Second)
	if got := modemDone(&app.snap, Spanish.Modem, h.u.pageSince, h.now()); got != "" {
		t.Fatalf("still %q", got)
	}
	// A wrong PIN: the app's message on the page
	app.snap.Last = &AppResult{What: "modem_pin", OK: false, Status: 409, Code: "pin_wrong", Message: "wrong PIN", At: h.now().Add(time.Millisecond)}
	h.advance(10 * time.Millisecond)
	if h.u.resultNote(&app.snap, "modem_pin") == "" {
		t.Fatal("no note for a wrong PIN")
	}
	// A ready SIM has no PIN button
	h.press("s_back", 0.1)
	h.u.openModem("m-5a1d0c3e9b21")
	if hasKey(h.u.settingsKeys(nil), "s_mdm_pin") {
		t.Fatal("a PIN button for a ready SIM")
	}
}

// Network mode (a picker), APN and name (keyboards) and roaming (a tap) reach the app as modem_save
func TestModemSave(t *testing.T) {
	h, app := modemsHarness(t, "srt")
	h.u.openModem("m-5a1d0c3e9b21")
	h.u.Tick()
	h.press("s_mdm_mode", 0.1)
	if h.u.chooser == nil || len(h.u.chooser.opts) != 3 || h.u.chooser.cur != "auto" {
		t.Fatalf("%+v", h.u.chooser)
	}
	h.u.pickerTouch("s_pick_1", "4g", true) // the row seen when the finger went down (the picker is drawn)
	if a := app.lastDid(); a.What != "modem_save" || a.ID != "m-5a1d0c3e9b21" || a.Body["mode"] != "4g" {
		t.Fatalf("%+v", a)
	}
	if h.u.modemView != "m-5a1d0c3e9b21" || h.u.settingsView != "main" {
		t.Fatalf("after the picker: view %q settings %q", h.u.modemView, h.u.settingsView)
	}
	h.u.Tick()
	h.press("s_mdm_apn", 0.1)
	if h.u.kb == nil || string(h.u.kb.text) != "" {
		t.Fatalf("%+v", h.u.kb)
	}
	for _, k := range []string{"w", "e", "b", "SYM", ".", "ABC", "e", "s"} { // «.» is on the symbols
		h.tapKey(k, 0.1)
	}
	h.tapKey("OK", 0.1)
	if a := app.lastDid(); a.What != "modem_save" || a.Body["apn"] != "web.es" {
		t.Fatalf("%+v", a)
	}
	h.u.Tick()
	h.press("s_mdm_roam", 0.1)
	if a := app.lastDid(); a.What != "modem_save" || a.Body["roaming"] != true {
		t.Fatalf("%+v", a)
	}
	h.u.Tick()
	h.press("s_mdm_name", 0.1)
	for _, c := range "mov" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	if a := app.lastDid(); a.What != "modem_save" || a.Body["alias"] != "mov" || len(a.Body) != 1 {
		t.Fatalf("%+v", a)
	}
	// While an order runs, the buttons wait
	n := len(app.did)
	app.snap.Pending = "modem_save"
	h.u.Tick()
	h.press("s_mdm_roam", 0.1)
	if len(app.did) != n {
		t.Fatal("an order while another one runs")
	}
}

// The only connection on air: a change that reconnects it needs a second tap and goes with force
func TestModemOnlyOnAir(t *testing.T) {
	h, app := modemsHarness(t, "srtla")
	st := app.snap.Status
	st.Uplinks[0].InUse, st.Uplinks[2].InUse = false, false
	o := *h.u.st
	o.Output = OutputStatus{Mode: "on", State: "on"}
	h.u.SetStatus(&o)
	h.u.openModem("m-5a1d0c3e9b21")
	h.u.Tick()
	if !h.u.onlyOnAir(st, "m-5a1d0c3e9b21") {
		t.Fatal("not the only one on air")
	}
	h.press("s_mdm_roam", 0.1)
	if len(app.did) != 0 || !h.u.confirmingPrefix("modem_force:") {
		t.Fatalf("the first tap did it: %+v", app.did)
	}
	h.u.Tick()
	h.press("s_mdm_roam", 0.1)
	if a := app.lastDid(); a.What != "modem_save" || a.Body["force"] != true {
		t.Fatalf("%+v", a)
	}
	// The APN: the second tap opens the keyboard (nothing typed is lost), and OK sends it with force
	n := len(app.did)
	h.u.Tick()
	h.press("s_mdm_apn", 0.1)
	if h.u.kb != nil || !h.u.confirming("modem_force:apn") {
		t.Fatal("the first tap opened the keyboard")
	}
	h.press("s_mdm_apn", 0.1)
	if h.u.kb == nil {
		t.Fatal("no keyboard after the second tap")
	}
	for _, c := range "web" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	if a := app.lastDid(); len(app.did) != n+1 || a.Body["apn"] != "web" || a.Body["force"] != true {
		t.Fatalf("%+v", a)
	}
	// Restart: the note says it cuts the stream, and it goes with force
	h.u.Tick()
	h.press("s_mdm_reset", 0.1)
	if a := app.lastDid(); len(app.did) != n+1 || a.What != "modem_save" {
		t.Fatal("the first tap restarted it")
	}
	h.press("s_mdm_reset", 0.1)
	if a := app.lastDid(); a.What != "modem_reset" || a.Body["force"] != true {
		t.Fatalf("%+v", a)
	}
	// The name never reconnects: no confirmation
	h.u.Tick()
	h.press("s_mdm_name", 0.1)
	h.tapKey("x", 0.1)
	h.tapKey("OK", 0.1)
	if a := app.lastDid(); a.Body["alias"] != "x" || a.Body["force"] != nil {
		t.Fatalf("%+v", a)
	}
}

// Restart: always a second tap
func TestModemReset(t *testing.T) {
	h, app := modemsHarness(t, "srt")
	h.u.openModem("m-0c7e44a19f02")
	h.u.Tick()
	h.press("s_mdm_reset", 0.1)
	if len(app.did) != 0 || !h.u.confirming("modem_reset") {
		t.Fatal("the first tap restarted it")
	}
	h.u.Tick()
	h.press("s_mdm_reset", 0.1)
	if a := app.lastDid(); a.What != "modem_reset" || a.ID != "m-0c7e44a19f02" || a.Body["force"] != nil {
		t.Fatalf("%+v", a)
	}
}

// Two identical HiLink sticks: the one in conflict offers to move its network (a second tap), the other does not
func TestModemSubnet(t *testing.T) {
	h, app := modemsHarness(t, "srt")
	st := app.snap.Status
	st.Modems[1].Can = append(st.Modems[1].Can, "subnet")
	st.Modems[0].Can = append(st.Modems[0].Can, "subnet")
	st.Uplinks[2].Conflict = true
	h.u.openModem("m-5a1d0c3e9b21")
	if hasKey(h.u.settingsKeys(nil), "s_mdm_pin") {
		t.Fatal("«change its network» on a modem without a conflict")
	}
	h.press("s_back", 0.1)
	h.u.openModem("m-0c7e44a19f02")
	h.u.Tick()
	h.press("s_mdm_pin", 0.1)
	if len(app.did) != 0 || !h.u.confirming("modem_subnet") {
		t.Fatal("the first tap moved it")
	}
	h.u.Tick()
	h.press("s_mdm_pin", 0.1)
	if a := app.lastDid(); a.What != "modem_subnet" || a.ID != "m-0c7e44a19f02" {
		t.Fatalf("%+v", a)
	}
}

// The twin in conflict: separating the networks cuts its routed twin; if that is the only link on air, the second tap
// says so and the order goes with force
func TestModemSubnetCutsTheTwin(t *testing.T) {
	h, app := modemsHarness(t, "srtla")
	st := app.snap.Status
	st.Uplinks = []UplinkInfo{
		{Name: "enx1", IP: "192.168.8.100", Kind: "modem", ModemID: "h-9b2c41d07e5a", InUse: true, Health: "good"},
		{Name: "enx2", IP: "192.168.8.100", Kind: "modem", ModemID: "h-1a2b3c4d5e6f", Conflict: true, Health: "bad", HealthCode: "ip_conflict"}}
	st.Modems = append(st.Modems, ModemInfo{ID: "h-9b2c41d07e5a", Backend: "hilink", Iface: "enx1", Can: []string{"apn", "subnet"}},
		ModemInfo{ID: "h-1a2b3c4d5e6f", Backend: "hilink", Iface: "enx2", Can: []string{"subnet"}})
	o := *h.u.st
	o.Output = OutputStatus{Mode: "on", State: "on"}
	h.u.SetStatus(&o)
	h.u.openModem("h-1a2b3c4d5e6f")
	m := h.u.curModem(&app.snap)
	if !h.u.subnetCuts(st, m) {
		t.Fatal("the twin's move does not cut the only link")
	}
	h.u.Tick()
	h.press("s_mdm_pin", 0.1)
	h.press("s_mdm_pin", 0.1)
	if a := app.lastDid(); a.What != "modem_subnet" || a.Body["force"] != true {
		t.Fatalf("%+v", a)
	}
	// With another link on air it does not cut the stream
	st.Uplinks = append(st.Uplinks, UplinkInfo{Name: "eth0", IP: "192.168.1.20", Kind: "ethernet", InUse: true})
	if h.u.subnetCuts(st, m) {
		t.Fatal("a cut with another link on air")
	}
}

// What the modem cannot do says so instead of opening anything
func TestModemCannot(t *testing.T) {
	h, app := modemsHarness(t, "srt")
	app.snap.Status.Modems[0].Can = nil
	h.u.openModem("m-5a1d0c3e9b21")
	h.u.Tick()
	for _, k := range []string{"s_mdm_mode", "s_mdm_apn", "s_mdm_roam", "s_mdm_reset"} {
		h.press(k, 0.1)
		if h.u.chooser != nil || h.u.kb != nil || len(app.did) != 0 {
			t.Fatalf("%s did something", k)
		}
		if note, _ := h.u.pageNoteText(); note != Spanish.Modem.NotSupported {
			t.Fatalf("%s: note %q", k, note)
		}
	}
}

func hasKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}
