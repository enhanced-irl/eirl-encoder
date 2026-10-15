package ui

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// pagesHarness is the touch harness with a fake device API and Settings open on a page.
func pagesHarness(t *testing.T, page string) (*harness, *fakeApp) {
	h := newHarness(t)
	app := &fakeApp{snap: AppSnapshot{DeviceAPI: Yes, SetupAPI: Yes, UpdateAPI: Yes,
		Setup: &SetupInfo{Done: true}, Update: &UpdateInfo{Current: "0.1.0", State: "idle"}}}
	dev := device("es")
	dev.At = h.now()
	dev.Now = time.Date(2026, 10, 14, 10, 42, 0, 0, time.FixedZone("CEST", 2*3600))
	app.snap.Device = dev
	h.u.App = app
	h.net.snap.API, h.net.snap.Status = Yes, camStatus()
	h.press("settings", 0.1)
	h.openPage(page)
	if h.u.settingsPage != page {
		t.Fatalf("page %q", h.u.settingsPage)
	}
	return h, app
}

func TestSettingsPagesList(t *testing.T) {
	h, app := pagesHarness(t, "wifi")
	for _, p := range SettingsPages {
		h.openPage(p)
		h.u.Tick()
		if h.u.settingsPage != p || !h.u.settingsOpen {
			t.Fatalf("%s: page %q open %v", p, h.u.settingsPage, h.u.settingsOpen)
		}
		w := app.watch()
		if !w.Device || w.Update != (p == "updates") || w.About != (p == "about") || w.Slots != (p == "sources") {
			t.Errorf("%s: watch %+v", p, w)
		}
	}
	// The network list closes when another page is chosen (its scan stops). The Wireless entry goes back to its tab
	// last seen (Bluetooth, after the loop): the Wi-Fi tab
	h.press("s_page_wifi", 0.1)
	if h.u.settingsPage != "bluetooth" {
		t.Fatalf("the Wireless entry opened %q, not its last tab", h.u.settingsPage)
	}
	h.press("s_tab_wifi", 0.1)
	h.wifiState()
	h.press("s_networks", 0.1)
	if h.u.settingsView != "networks" {
		t.Fatal("networks")
	}
	// In the network list only ← and X answer (the list's entries are not keys there)
	h.press("s_page_system", 0.1)
	if h.u.settingsPage != "wifi" || h.u.settingsView != "networks" {
		t.Fatal("an entry answered in the network list")
	}
	h.press("s_close", 0.1)
	h.u.Tick()
	if h.u.settingsOpen || app.watch().Device {
		t.Fatalf("closed: %v %+v", h.u.settingsOpen, app.watch())
	}
}

func TestLanguagePage(t *testing.T) {
	h, app := pagesHarness(t, "language")
	h.u.Tick()
	h.press("s_lang_fr", 0.1)
	if a := app.lastDid(); a.What != "language" || a.Value != "fr" || h.u.Tx != French {
		t.Fatalf("%+v %s", a, h.u.Tx.Code)
	}
	// The app refuses it: back to the device's language
	app.snap.Last = &AppResult{What: "language", OK: false, Status: 500, Code: "internal", At: h.now().Add(time.Millisecond)}
	h.advance(10 * time.Millisecond)
	h.u.Tick()
	if h.u.Tx != Spanish {
		t.Fatalf("after a failure: %s", h.u.Tx.Code)
	}
	// The app takes it (its answer is the device in French)
	app.snap.Device = device("fr")
	h.press("s_lang_fr", 0.1)
	h.u.Tick()
	if h.u.Tx != French {
		t.Fatal("not French")
	}
	// Busy: nothing changes, it says so
	app.refuse = true
	h.press("s_lang_en", 0.1)
	if h.u.Tx != French || !strings.Contains(h.u.hint, "otra") && !strings.Contains(h.u.hint, "autre") {
		t.Fatalf("%s %q", h.u.Tx.Code, h.u.hint)
	}
	// An older app: no buttons
	app.snap.DeviceAPI, app.snap.Device = No, nil
	if keys := h.u.pageKeys("language", &h.net.snap); len(keys) != 0 {
		t.Fatal(keys)
	}
}

func TestTimePage(t *testing.T) {
	h, app := pagesHarness(t, "time")
	keys := h.u.pageKeys("time", &h.net.snap)
	if slices.Contains(keys, "s_t_set") || !slices.Contains(keys, "s_ntp") {
		t.Fatalf("NTP on: %v", keys)
	}
	h.press("s_ntp", 0.1)
	if a := app.lastDid(); a.What != "ntp" || a.On {
		t.Fatalf("%+v", a)
	}
	app.snap.Device.NTP = false
	// The draft starts at the device's clock; the steps move it
	h.press("s_t_day_p", 0.1)
	h.press("s_t_day_p", 0.1)
	h.press("s_t_hour_m", 0.1)
	h.press("s_t_min_p", 0.1)
	h.press("s_t_mon_m", 0.1)
	h.press("s_t_year_p", 0.1)
	h.press("s_t_set", 0.1)
	a := app.lastDid()
	want := time.Date(2027, 9, 16, 9, 43, 0, 0, time.FixedZone("CEST", 2*3600))
	if a.What != "time" || !a.Time.Equal(want) {
		t.Fatalf("%+v, want %v", a, want)
	}
	if h.u.timeDraftSet {
		t.Error("the draft stays after setting it")
	}
	if got := addMonthsClamped(time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), 1); got.Month() != 2 || got.Day() != 28 {
		t.Errorf("31 Jan + 1 month = %v", got)
	}
	// The zone picker: on the current zone's region; a cell sets the zone
	app.snap.Zones = []TimeZone{{ID: "America/Bogota", Offset: "-05:00"}, {ID: "Europe/Lisbon", Offset: "+01:00"},
		{ID: "Europe/Madrid", Offset: "+02:00"}, {ID: "UTC", Offset: "+00:00"}}
	h.press("s_zone", 0.1)
	h.u.Dirty = true
	h.u.Tick() // the picker's cells, as drawn (without fonts: the touch harness lays them out anyway)
	if h.u.settingsView != "zones" || h.u.zoneRegion != "Europe" || !app.watch().Zones {
		t.Fatalf("%q %q %+v", h.u.settingsView, h.u.zoneRegion, app.watch())
	}
	h.u.zoneShown = []string{"Europe/Lisbon", "Europe/Madrid"}
	h.press("s_zone_0", 0.1)
	if a := app.lastDid(); a.What != "timezone" || a.Value != "Europe/Lisbon" || h.u.settingsView != "main" {
		t.Fatalf("%+v %q", a, h.u.settingsView)
	}
	h.press("s_zone", 0.1)
	h.press("s_region_1", 0.1) // America
	if h.u.zoneRegion != "America" || h.u.zonePage != 0 {
		t.Fatal(h.u.zoneRegion)
	}
	h.press("s_back", 0.1)
	if h.u.settingsView != "main" || !h.u.settingsOpen {
		t.Fatal("back")
	}
	if regionOf("UTC") != "Other" || regionOf("Etc/GMT+1") != "Other" || zoneLabel("America/Argentina/Buenos_Aires") !=
		"Argentina/Buenos Aires" {
		t.Error("regions")
	}
}

func TestUpdatesPage(t *testing.T) {
	h, app := pagesHarness(t, "updates")
	if keys := h.u.pageKeys("updates", &h.net.snap); slices.Contains(keys, "s_up_install") {
		t.Fatal("install without an update")
	}
	h.press("s_up_check", 0.1)
	if a := app.lastDid(); a.What != "update_check" {
		t.Fatal(a)
	}
	app.snap.Update = &UpdateInfo{Current: "0.1.0", State: "available", Available: &UpdateRelease{Version: "0.2.0"}}
	n := len(app.did)
	h.press("s_up_install", 0.1) // the first tap asks for the second
	if len(app.did) != n || h.u.confirm == nil || h.u.confirm.what != "update" {
		t.Fatal("installs at the first tap")
	}
	h.press("s_up_install", 0.1)
	if a := app.lastDid(); a.What != "update_install" {
		t.Fatal(a)
	}
	// Streaming: never
	st := *h.u.st
	st.Output.Mode = "on"
	h.u.SetStatus(&st)
	n = len(app.did)
	h.press("s_up_install", 0.1)
	h.press("s_up_install", 0.1)
	if len(app.did) != n {
		t.Fatal("installs while streaming")
	}
	// The confirmation expires
	st.Output.Mode = "off"
	h.u.SetStatus(&st)
	h.press("s_up_install", 0.1)
	h.advance(ConfirmUpdate + time.Second)
	h.u.Tick()
	if h.u.confirm != nil {
		t.Fatal("the confirmation stays")
	}
	if !updateAvailable(&app.snap) {
		t.Error("the dot")
	}
}

func TestAboutSetupAgain(t *testing.T) {
	h, app := pagesHarness(t, "about")
	h.press("s_setup_again", 0.1)
	if len(app.did) != 0 {
		t.Fatal("the first tap runs it")
	}
	h.press("s_setup_again", 0.1)
	if a := app.lastDid(); a.What != "setup_restart" {
		t.Fatal(a)
	}
	// Setup not done (postponed in the wizard): «Continue» shows it at once, without asking the app
	app.snap.Setup = &SetupInfo{Done: false}
	h.u.wizardPostponed = true
	n := len(app.did)
	h.press("s_setup_again", 0.1)
	if len(app.did) != n || h.u.wizardPostponed || h.u.settingsOpen {
		t.Fatal("continue")
	}
}

// The new pages' buttons: inside the page, ≥ 48 px, and not overlapping within a page.
func TestSettingsPagesLayout(t *testing.T) {
	for _, sz := range [][2]int{{1024, 600}, {1280, 720}, {1920, 1080}} {
		lay := NewLayout(sz[0], sz[1], 4)
		in := func(r, box Rect) bool {
			return r.X >= box.X && r.Y >= box.Y && r.X+r.W <= box.X+box.W && r.Y+r.H <= box.Y+box.H
		}
		groups := map[string][]string{"list": nil, "language": nil, "time": nil, "zones": {"s_prev", "s_next"},
			"updates": {"s_up_check", "s_up_install"}, "about": {"s_setup_again"}}
		for _, g := range SettingsGroups {
			groups["list"] = append(groups["list"], "s_page_"+g.ID)
		}
		for _, c := range Languages {
			groups["language"] = append(groups["language"], "s_lang_"+c)
		}
		groups["time"] = append(groups["time"], "s_ntp", "s_zone", "s_t_set")
		for _, f := range TimeFields {
			groups["time"] = append(groups["time"], "s_t_"+f+"_m", "s_t_"+f+"_p")
		}
		for k := range lay.Buttons {
			if strings.HasPrefix(k, "s_region_") || strings.HasPrefix(k, "s_zone_") {
				groups["zones"] = append(groups["zones"], k)
			}
		}
		for name, keys := range groups {
			for i, k := range keys {
				r, ok := lay.Buttons[k]
				if !ok {
					t.Fatalf("%v: no %s", sz, k)
				}
				box := lay.SettingsPage
				if name == "list" {
					box = lay.SettingsList
				}
				if !in(r, box) {
					t.Errorf("%v %s: %s %v outside %v", sz, name, k, r, box)
				}
				if r.H < 44 || r.W < 48 || (name != "zones" && r.H < 48) {
					t.Errorf("%v %s: %s %v too small", sz, name, k, r)
				}
				for _, k2 := range keys[i+1:] {
					if overlaps(r, lay.Buttons[k2]) {
						t.Errorf("%v %s: %s overlaps %s", sz, name, k, k2)
					}
				}
			}
		}
		if lay.UpdateNotes.Y < lay.Buttons["s_up_check"].Y+lay.Buttons["s_up_check"].H {
			t.Errorf("%v: the notes over the buttons", sz)
		}
	}
}

// An action the app does not take over the socket (403 local_forbidden) says it is not available, not the app's
// technical message.
func TestResultForbidden(t *testing.T) {
	h, _ := pagesHarness(t, "about")
	l := &AppResult{What: "setup_restart", Status: 403, Code: "local_forbidden", Message: "route not available over the local socket"}
	if got := h.u.resultText(l); got != Spanish.PageUnavailable {
		t.Fatal(got)
	}
	l = &AppResult{What: "slots", Status: 409, Code: "on_air", Message: "Deja de emitir"}
	if got := h.u.resultText(l); got != "Deja de emitir" {
		t.Fatal(got)
	}
}

// The Updates page says what the app says: the error's message (not its code), why «Update» cannot be pressed
// (canInstall, blockedMessage), the installer's last run; an older app without them: the code and the screen's
// own rule.
func TestUpdatesAppWords(t *testing.T) {
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	h, _ := drawHarness(t)
	app := &fakeApp{snap: AppSnapshot{DeviceAPI: Yes, UpdateAPI: Yes, Device: device("es")}}
	app.snap.Device.At = h.now()
	h.u.App = app
	h.u.settingsOpen, h.u.settingsPage = true, "updates"
	draw := func(up *UpdateInfo) {
		app.snap.Update = up
		shown = shown[:0]
		h.u.Dirty = true
		h.u.Tick()
	}
	has := func(s string) bool { return slices.Contains(shown, s) }
	draw(&UpdateInfo{Current: "0.1.0", State: "error", Error: "network",
		Message: "no se puede contactar con el servidor de actualizaciones"})
	if !has("No se ha podido actualizar: no se puede contactar con el servidor de actualizaciones") {
		t.Fatalf("the message: %q", shown)
	}
	draw(&UpdateInfo{Current: "0.1.0", State: "error", Error: "network"}) // an older app: its code
	if !has("No se ha podido actualizar: network") {
		t.Fatalf("the code: %q", shown)
	}
	// Blocked: the app's reason, capitalised; two taps do nothing
	no := false
	avail := &UpdateRelease{Version: "0.2.0"}
	draw(&UpdateInfo{Current: "0.1.0", State: "ready", Available: avail, CanInstall: &no, Blocked: "streaming",
		BlockedMessage: "deja de emitir para actualizar el codificador"})
	if !has("Deja de emitir para actualizar el codificador") {
		t.Fatalf("blocked: %q", shown)
	}
	h.press("s_up_install", 0.1)
	h.press("s_up_install", 0.1)
	if len(app.did) != 0 {
		t.Fatal("installs while blocked")
	}
	// The app says it can: two taps install (its word wins over the screen's own rule)
	yes := true
	draw(&UpdateInfo{Current: "0.1.0", State: "ready", Available: avail, CanInstall: &yes})
	h.press("s_up_install", 0.1)
	h.press("s_up_install", 0.1)
	if app.lastDid().What != "update_install" {
		t.Fatal(app.did)
	}
	// The installer's last run: undone with its reason; failed; installed
	draw(&UpdateInfo{Current: "0.1.0", State: "rolled_back", LastResult: &UpdateRun{State: "rolled_back", To: "0.2.0",
		Message: "la versión nueva no arrancó bien: se ha vuelto a la anterior"}})
	if !strings.Contains(strings.Join(shown, " "),
		"La actualización a la 0.2.0 se ha deshecho: la versión nueva no arrancó bien: se ha vuelto a la anterior") {
		t.Fatalf("undone: %q", shown)
	}
	draw(&UpdateInfo{Current: "0.1.0", State: "idle", LastResult: &UpdateRun{State: "failed", To: "0.2.0",
		Message: "el instalador no terminó"}})
	if !has("La actualización a la 0.2.0 ha fallado: el instalador no terminó") {
		t.Fatalf("failed: %q", shown)
	}
	draw(&UpdateInfo{Current: "0.2.0", State: "idle", LastResult: &UpdateRun{State: "installed", From: "0.1.0", To: "0.2.0"}})
	if !has("Actualizado a la 0.2.0") {
		t.Fatalf("installed: %q", shown)
	}
	draw(&UpdateInfo{Current: "0.2.0", State: "idle", TestBuild: true})
	if !has("Al día") || !has(Spanish.UpdTestBuild) {
		t.Fatalf("test build: %q", shown)
	}
	if capFirst("éste") != "Éste" || capFirst("") != "" {
		t.Error("capFirst")
	}
}
