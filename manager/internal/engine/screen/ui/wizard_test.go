package ui

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"
)

func wizardApp(h *harness) *fakeApp {
	app := &fakeApp{snap: AppSnapshot{DeviceAPI: Yes, SetupAPI: Yes, UpdateAPI: Yes, CloudAPI: Yes,
		Setup: &SetupInfo{NeedsPassword: true, Code: "ABCD2345", Steps: []SetupStep{{"language", "todo"},
			{"time", "todo"}, {"network", "todo"}, {"password", "todo"}, {"name", "todo"}, {"cloud", "todo"}}},
		Cloud: &CloudInfo{State: "unlinked"}}}
	app.snap.Device = device("es")
	app.snap.Device.At = h.now()
	h.u.App = app
	return app
}

// The wizard shows while the app says the setup is not done, and only then.
func TestWizardShows(t *testing.T) {
	h := newHarness(t)
	app := wizardApp(h)
	h.u.Tick()
	if !h.u.wizardOn() || h.u.wizardStep() != "language" || !h.u.SecretOnScreen() || !app.watch().Setup {
		t.Fatalf("on %v step %q watch %+v", h.u.wizardOn(), h.u.wizardStep(), app.watch())
	}
	// The normal screen does not answer under it
	h.press(1, 0.1)
	h.press("emit", Hold.Seconds()+0.1)
	if len(h.cmds) != 0 {
		t.Fatal(h.cmdNames())
	}
	// It starts at the first step still to do
	h2 := newHarness(t)
	a2 := wizardApp(h2)
	a2.snap.Setup.Steps[0].State, a2.snap.Setup.Steps[1].State, a2.snap.Setup.Steps[2].State = "done", "done", "skipped"
	h2.u.Tick()
	if h2.u.wizardStep() != "password" {
		t.Fatal(h2.u.wizardStep())
	}
	// Locked from the web: the normal (locked) screen
	h.setLocked(true)
	h.u.Tick()
	if h.u.wizardOn() {
		t.Fatal("the wizard while locked")
	}
	h.setLocked(false)
	h.u.Tick()
	// Finished from the web: it goes
	app.snap.Setup = &SetupInfo{Done: true}
	h.u.Tick()
	if h.u.wizardOn() {
		t.Fatal("the wizard after done")
	}
	// An older app (no /setup): never
	h3 := newHarness(t)
	a3 := wizardApp(h3)
	a3.snap.SetupAPI, a3.snap.Setup = No, nil
	h3.u.Tick()
	if h3.u.wizardOn() {
		t.Fatal("the wizard with an older app")
	}
}

func TestWizardFlow(t *testing.T) {
	h := newHarness(t)
	app := wizardApp(h)
	h.wifiState()
	h.u.Tick()
	tap := func(key string) {
		t.Helper()
		h.press(key, 0.1)
		h.u.Tick()
	}
	// 1 language: the screen changes at once; Next marks it
	if slices.Contains(h.u.wizardKeys(&h.net.snap), "w_back") || slices.Contains(h.u.wizardKeys(&h.net.snap), "w_later") {
		t.Fatal("Back or Later on the first step")
	}
	tap("s_lang_fr")
	if h.u.Tx != French || app.lastDid().What != "language" {
		t.Fatal("language")
	}
	app.snap.Device = device("fr")
	tap("w_next")
	if a := app.lastDid(); a.What != "setup_step" || a.Value != "language" || a.State != "done" || h.u.wizardStep() != "time" {
		t.Fatalf("%+v %q", a, h.u.wizardStep())
	}
	// 2 time: the page's buttons; back and forth
	if !slices.Contains(h.u.wizardKeys(&h.net.snap), "s_ntp") {
		t.Fatal(h.u.wizardKeys(&h.net.snap))
	}
	tap("w_back")
	tap("w_next")
	tap("w_next")
	// 3 network: the wifi page, with the panel open (credentials, fast polling); Skip marks it skipped
	if h.u.wizardStep() != "network" || !h.net.panel || !slices.Contains(h.u.wizardKeys(&h.net.snap), "s_networks") {
		t.Fatalf("%q panel %v", h.u.wizardStep(), h.net.panel)
	}
	tap("s_networks")
	if h.u.settingsView != "networks" {
		t.Fatal("networks")
	}
	tap("s_back")
	tap("w_skip")
	if a := app.lastDid(); a.Value != "network" || a.State != "skipped" || h.net.panel {
		t.Fatalf("%+v panel %v", a, h.net.panel)
	}
	// 4 password: Next waits for it; typed twice, compared, then to the app; never kept
	if h.u.wizardNextOK(&app.snap) {
		t.Fatal("Next without the password")
	}
	n := len(app.did)
	tap("w_next")
	if len(app.did) != n || h.u.wizardStep() != "password" {
		t.Fatal("Next went on without the password")
	}
	tap("w_pw")
	if h.u.settingsView != "keyboard" || h.u.kb.mode != "webpw" || !h.u.kb.secret() {
		t.Fatal("keyboard")
	}
	typeIt := func(s string) {
		for _, c := range s {
			h.tapKey(string(c), 0.1)
		}
	}
	typeIt("short")
	h.tapKey("OK", 0.1)
	if h.u.kb.mode != "webpw" {
		t.Fatal("a short password was taken")
	}
	h.tapKey("BKSP", 1) // held: all of it
	typeIt("secretoabcd")
	h.tapKey("OK", 0.1)
	if h.u.kb.mode != "webpw2" || h.u.kb.n != 0 || h.u.kb.firstN != 11 {
		t.Fatalf("%q %d %d", h.u.kb.mode, h.u.kb.n, h.u.kb.firstN)
	}
	typeIt("secretoabce")
	h.tapKey("OK", 0.1)
	if h.u.kb.mode != "webpw" || h.u.kb.err == "" || h.u.kb.firstN != 0 || len(app.pw) != 0 {
		t.Fatal("a mismatch was taken")
	}
	typeIt("secretoabcd")
	h.tapKey("OK", 0.1)
	kb := h.u.kb
	typeIt("secretoabcd")
	h.tapKey("OK", 0.1)
	if len(app.pw) != 1 || app.pw[0] != "secretoabcd" || h.u.kb != nil || h.u.settingsView != "main" {
		t.Fatalf("%q %v", app.pw, h.u.kb)
	}
	if !bytes.Equal(kb.buf[:], make([]byte, PSKMax)) || !bytes.Equal(kb.first[:], make([]byte, PSKMax)) {
		t.Fatal("the password is still in the keyboard")
	}
	app.snap.Setup.NeedsPassword, app.snap.Setup.Code = false, ""
	tap("w_next")
	if a := app.lastDid(); a.Value != "password" || a.State != "done" || h.u.wizardStep() != "name" {
		t.Fatalf("%+v", a)
	}
	// 5 name: the keyboard with the name; a bad one says why; Done sends it
	tap("w_name")
	if h.u.kb == nil || h.u.kb.mode != "name" || string(h.u.kb.text) != "Estudio 2" {
		t.Fatal("name keyboard")
	}
	h.tapKey("SYM", 0.1)
	h.tapKey("@", 0.1)
	h.tapKey("OK", 0.1)
	if h.u.kb == nil || h.u.kb.err == "" {
		t.Fatal("a bad name was taken")
	}
	h.tapKey("BKSP", 0.1)
	h.tapKey("BKSP", 0.1)
	h.tapKey("3", 0.1)
	h.tapKey("OK", 0.1)
	if a := app.lastDid(); a.What != "name" || a.Value != "Estudio 3" {
		t.Fatalf("%+v", a)
	}
	tap("w_next")
	// 6 cloud: a code; Next without linking is «skipped»
	if !app.watch().Cloud {
		t.Fatal("cloud not watched")
	}
	tap("w_pair")
	if app.lastDid().What != "cloud_pair" {
		t.Fatal(app.lastDid())
	}
	tap("w_next")
	if a := app.lastDid(); a.Value != "cloud" || a.State != "skipped" || h.u.wizardStep() != "done" {
		t.Fatalf("%+v", a)
	}
	// 7 done: Finish completes it
	tap("w_next")
	if app.lastDid().What != "setup_complete" {
		t.Fatal(app.lastDid())
	}
	app.snap.Setup.Done = true
	h.u.Tick()
	if h.u.wizardOn() || len(h.cmds) != 0 {
		t.Fatal("still on")
	}
}

// «Later»: the normal screen (its controls answer) until «Continue the setup».
func TestWizardLater(t *testing.T) {
	h := newHarness(t)
	app := wizardApp(h)
	h.u.Tick()
	h.press("w_next", 0.1)
	h.u.Tick()
	h.press("w_later", 0.1)
	h.u.Tick()
	if h.u.wizardOn() || !h.u.wizardPostponed {
		t.Fatal("later")
	}
	h.press(1, 0.1)
	if len(h.cmds) != 1 {
		t.Fatal("the normal screen does not answer")
	}
	if !setupPending(&app.snap) {
		t.Fatal("no reminder")
	}
	h.press("settings", 0.1)
	h.press("s_page_about", 0.1)
	h.press("s_setup_again", 0.1) // «Continue the setup»: at once
	h.u.Tick()
	if !h.u.wizardOn() || h.u.settingsOpen {
		t.Fatal("continue")
	}
}

// Nothing typed on the wizard's password keyboard is drawn as text; the setup code is.
func TestWizardDrawsNoPassword(t *testing.T) {
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	h, _ := drawHarness(t)
	app := wizardApp(h)
	h.u.Tick()
	h.u.wizardGo(3)
	h.u.Dirty = true
	h.u.Tick()
	if !slices.Contains(shown, "ABCD 2345") {
		t.Fatalf("no code: %q", shown)
	}
	h.press("w_pw", 0.1)
	for _, c := range "SECRETOx91" {
		h.tapKey(string(c), 0.1)
	}
	h.u.Dirty = true
	h.u.Tick()
	h.tapKey("OK", 0.1)
	for _, c := range "SECRETOx91" {
		h.tapKey(string(c), 0.1)
	}
	h.u.Dirty = true
	h.u.Tick()
	for _, s := range shown {
		if strings.Contains(s, "SECRETO") || strings.Contains(s, "x91") {
			t.Fatalf("the password drawn: %q", s)
		}
	}
	_ = app
	h.advance(time.Second)
}

// The wizard's buttons: on the screen, ≥ 48 px, and not overlapping each other or the page's.
func TestWizardLayout(t *testing.T) {
	for _, sz := range [][2]int{{1024, 600}, {1280, 720}, {1920, 1080}} {
		lay := NewLayout(sz[0], sz[1], 4)
		top := []string{"w_back", "w_skip", "w_next"}
		for i, k := range top {
			r := lay.Buttons[k]
			if r.H < 44 || r.Y+r.H > lay.Status.H {
				t.Errorf("%v: %s %v", sz, k, r)
			}
			for _, k2 := range top[i+1:] {
				if overlaps(r, lay.Buttons[k2]) {
					t.Errorf("%v: %s overlaps %s", sz, k, k2)
				}
			}
		}
		l := lay.Buttons["w_later"]
		if l.H < 48 || overlaps(l, lay.WizardList) || !lay.SettingsPanel.Inside(l.X, l.Y+l.H-1, 0) {
			t.Errorf("%v: later %v", sz, l)
		}
		for _, k := range []string{"w_pw", "w_name", "w_pair"} {
			r := lay.Buttons[k]
			pg := lay.SettingsPage
			if r.H < 48 || !pg.Inside(r.X, r.Y, 0) || !pg.Inside(r.X+r.W-1, r.Y+r.H-1, 0) {
				t.Errorf("%v: %s %v outside the page", sz, k, r)
			}
		}
	}
}
