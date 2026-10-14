package ui

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeApp is the app's device API, fake: what the tests set, and what the screen asked.
type fakeApp struct {
	snap    AppSnapshot
	watches []AppWatch
	did     []AppAction
	pw      []string // what SetupPassword got (only the tests look)
	refuse  bool
	// secrets: each action's Secrets as they were when Do got them (the caller wipes its own right after)
	secrets []map[string]string
	dropped int // DropSecrets calls
}

func (f *fakeApp) DropSecrets() { f.dropped++ }

func (f *fakeApp) AppSnapshot() AppSnapshot { return f.snap }
func (f *fakeApp) Watch(w AppWatch)         { f.watches = append(f.watches, w) }
func (f *fakeApp) Do(a AppAction) bool {
	if f.refuse {
		return false
	}
	f.did = append(f.did, a)
	sec := map[string]string{}
	for k, v := range a.Secrets {
		sec[k] = string(v)
	}
	f.secrets = append(f.secrets, sec)
	return true
}
func (f *fakeApp) SetupPassword(p []byte) bool {
	f.pw = append(f.pw, string(p))
	return !f.refuse
}

func (f *fakeApp) lastDid() AppAction {
	if len(f.did) == 0 {
		return AppAction{}
	}
	return f.did[len(f.did)-1]
}

func (f *fakeApp) watch() AppWatch {
	if len(f.watches) == 0 {
		return AppWatch{}
	}
	return f.watches[len(f.watches)-1]
}

func device(lang string) *DeviceInfo {
	return &DeviceInfo{Language: lang, Languages: Languages, Name: "Estudio 2", Hostname: "estudio-2",
		Timezone: "Europe/Madrid", Now: time.Date(2026, 10, 14, 1, 4, 5, 0, time.UTC), NTP: true, Synced: true,
		Release: "0.1.0", AppVer: "0.1.0", EngineVer: "0.1.0"}
}

// netWithApp is a Net that carries an App (AppProvider), like the engine's appapi client.
type netWithApp struct {
	*fakeNet
	a App
}

func (n netWithApp) App() App { return n.a }

// The screen follows the device language the app says, once per change (a change on the web, or ours); before
// any answer it is English; it finds the App through its Net.
func TestLanguageFollowsApp(t *testing.T) {
	h := newHarness(t)
	app := &fakeApp{}
	h.u = New(h.lay, nil, netWithApp{h.net, app})
	h.u.Clock = h.now
	if h.u.Tx != English {
		t.Fatal("not English before the app answers")
	}
	h.u.Tick()
	if h.u.app() != app || len(app.watches) != 1 || app.watch().Device {
		t.Fatalf("app %v, watches %+v", h.u.app() == app, app.watches)
	}
	app.snap.Device = device("fr")
	h.u.Tick()
	if h.u.Tx != French {
		t.Fatal("not French")
	}
	// The keyboard's cached keys carry labels: drawn again in the new language
	h.u.r.kbBase = newRGBA(4, 4)
	app.snap.Device = device("es")
	h.u.Tick()
	if h.u.Tx != Spanish || h.u.r.kbBase != nil {
		t.Fatal("not Spanish, or the keyboard's cache stays")
	}
	// Our own choice wins until the app says something new: an old answer does not flip it back
	h.u.SetLanguage("en")
	h.u.Tick()
	if h.u.Tx != English {
		t.Fatal("an unchanged answer replaced our choice")
	}
	// An unknown language is not applied
	app.snap.Device = device("de")
	h.u.Tick()
	if h.u.Tx != English {
		t.Fatal("an unknown language")
	}
	// Settings open: the device fast (the clock, the language)
	h.press("settings", 0.1)
	h.u.Tick()
	if !app.watch().Device {
		t.Fatalf("%+v", app.watch())
	}
	// Without an App: nothing happens (the touch tests' harness)
	h2 := newHarness(t)
	h2.u.Tick()
	if h2.u.Tx != Spanish {
		t.Fatal("the harness's language changed")
	}
}

// Every language draws every view at 1024×600 without cutting a label: what fit() shortens is only allowed
// for data (SSIDs, device names), never a text of the table.
func TestRenderEveryLanguage(t *testing.T) {
	type view struct {
		name string
		set  func(h *harness)
	}
	out := func(mode, state, reason string) func(h *harness) {
		return func(h *harness) {
			st := *h.u.st
			st.Output = OutputStatus{Mode: mode, State: state, Reason: reason}
			if state == "on" {
				k, r := 5800, 57
				st.Output.SentKbps, st.Output.RTTMs = &k, &r
			}
			h.u.SetStatus(&st)
		}
	}
	withAudio := func(mode string) func(h *harness) {
		return func(h *harness) {
			st := *h.u.st
			g := -6.0
			st.Audio = &AudioStatus{Mode: mode, Follow: true, Sources: []AudioInput{
				{ID: "i2s", Name: "HDMI", State: "live", GainDb: &g, VideoSlot: 1, InMix: true},
				{ID: "usb1", Name: "usb1", State: "no_signal", VideoSlot: 2, DelayMs: 120}},
				Phones: &PhonesStatus{On: true, GainDb: ip(-12), State: "on"}}
			h.u.SetStatus(&st)
			h.u.view = "audio"
		}
	}
	views := []view{
		{"idle", func(h *harness) {}},
		{"live", out("on", "on", "")},
		{"connecting", out("on", "connecting", "relay_unreachable")},
		{"error", out("on", "error", "rejected")},
		{"pip", func(h *harness) {
			st := *h.u.st
			sec := "usb1"
			st.Program.Layout, st.Program.Secondary = "pip", &sec
			st.Audio = &AudioStatus{Mode: "single", Follow: true}
			h.u.SetStatus(&st)
		}},
		{"audio", withAudio("single")},
		{"audiomix", withAudio("mix")},
		{"menu", func(h *harness) { h.u.menuOpen = true }},
		{"locked", func(h *harness) { h.setLocked(true) }},
		{"settings", func(h *harness) {
			h.wifiState()
			h.net.snap.Status.Cameras = 1
			h.net.snap.Wifi = &WifiCreds{SSID: "EnhancedIRL-0000", Password: "Ejempl0lI1NoEsLaReal", QR: testQR()}
			h.u.settingsOpen = true
		}},
		{"cams", func(h *harness) {
			h.wifiState()
			h.net.snap.Status.Cameras = 1
			h.u.settingsOpen, h.u.settingsPage = true, "cams"
		}},
		{"networks", func(h *harness) { h.wifiState(); h.u.settingsOpen = true; h.u.openNetworks() }},
		{"keyboard", func(h *harness) { h.wifiState(); h.u.settingsOpen = true; h.u.openKeyboard("Nueva", "") }},
	}
	page := func(name, view string, mod func(a *AppSnapshot)) func(h *harness) {
		return func(h *harness) {
			a := &fakeApp{snap: AppSnapshot{DeviceAPI: Yes, SetupAPI: Yes, UpdateAPI: Yes, Setup: &SetupInfo{Done: true},
				Update: &UpdateInfo{Current: "0.1.0", State: "idle"}, Commit: "0123abcd", IPs: []string{"192.168.1.20"},
				Zones: []TimeZone{{ID: "Europe/Madrid", Offset: "+02:00"}, {ID: "Europe/Lisbon", Offset: "+01:00"}}}}
			a.snap.Device = device(h.u.Tx.Code)
			a.snap.Device.At = h.now()
			if mod != nil {
				mod(&a.snap)
			}
			h.u.App = a
			h.wifiState()
			h.u.settingsOpen, h.u.settingsPage = true, name
			switch view {
			case "zones":
				h.u.openZones()
			case "cands":
				h.u.srcTouch("s_src_2", "", false)
			}
		}
	}
	avail := func(state string) func(a *AppSnapshot) {
		return func(a *AppSnapshot) {
			a.Update.State, a.Update.Progress = state, 0.4
			a.Update.Available = &UpdateRelease{Version: "0.2.0", Notes: "Notes", Size: 23_400_000}
		}
	}
	views = append(views,
		view{"page-language", page("language", "", nil)},
		view{"page-time", page("time", "", nil)},
		view{"page-time-manual", page("time", "", func(a *AppSnapshot) { a.Device.NTP, a.Device.Synced = false, false })},
		view{"page-time-waiting", page("time", "", func(a *AppSnapshot) { a.Device.Synced = false })},
		view{"page-zones", page("time", "zones", nil)},
		view{"page-updates", page("updates", "", nil)},
		view{"page-updates-available", page("updates", "", avail("available"))},
		view{"page-updates-downloading", page("updates", "", avail("downloading"))},
		view{"page-updates-installing", page("updates", "", avail("installing"))},
		view{"page-updates-rolledback", page("updates", "", func(a *AppSnapshot) { a.Update.State = "rolled_back" })},
		view{"page-about", page("about", "", nil)},
		view{"page-updates-app-words", page("updates", "", func(a *AppSnapshot) {
			no := false
			avail("ready")(a)
			a.Update.CanInstall, a.Update.BlockedMessage, a.Update.TestBuild = &no, "stop streaming to update the encoder", true
		})},
		view{"page-updates-undone", page("updates", "", func(a *AppSnapshot) {
			a.Update.State = "rolled_back"
			a.Update.LastResult = &UpdateRun{State: "rolled_back", To: "0.2.0",
				Message: "la nouvelle version n’a pas démarré correctement : la précédente a été restaurée"}
		})},
		view{"page-updates-installed", page("updates", "", func(a *AppSnapshot) {
			a.Update.Current, a.Update.LastResult = "0.2.0", &UpdateRun{State: "installed", To: "0.2.0"}
		})},
		view{"page-sources", page("sources", "", func(a *AppSnapshot) { a.Slots, a.SlotsAPI = slotsApp(), Yes })},
		view{"page-sources-old", page("sources", "", func(a *AppSnapshot) {
			a.Slots, a.SlotsAPI = slotsApp(), Yes
			a.Slots.EngineSupports = false
		})},
		view{"page-sources-pick", page("sources", "cands", func(a *AppSnapshot) { a.Slots, a.SlotsAPI = slotsApp(), Yes })},
		view{"page-old", page("updates", "", func(a *AppSnapshot) { a.UpdateAPI, a.Update = No, nil })},
	)
	for i, step := range WizardSteps {
		for _, cloud := range []string{"unlinked", "pairing", "linked"} {
			if step != "cloud" && cloud != "unlinked" {
				continue
			}
			views = append(views, view{"wizard-" + step + "-" + cloud, func(h *harness) {
				lang := h.u.Tx.Code
				app := wizardApp(h)
				app.snap.Device = device(lang)
				app.snap.Device.At = h.now()
				exp := h.now().Add(10 * time.Minute)
				app.snap.Cloud = &CloudInfo{State: cloud, Code: "K7QM-2XVD", ExpiresAt: &exp, AccountLabel: "estudio@example.com",
					LinkURL: "https://app.enhancedirl.com/encoders/link?pairing=K7QM-2XVD", LinkQR: testQR()}
				h.wifiState()
				h.u.Tick()
				h.u.wizardGo(i)
			}})
		}
	}
	rv := make([]renderView, len(views))
	for i, v := range views {
		rv[i] = renderView(v)
	}
	checkRenders(t, rv)
}

// pageApp opens Settings on a page with a fake app (device, setup done, updates idle) that mod may change: for the
// drawing tests of a page.
func pageApp(h *harness, page string, mod func(a *AppSnapshot)) *fakeApp {
	a := &fakeApp{snap: AppSnapshot{DeviceAPI: Yes, SetupAPI: Yes, UpdateAPI: Yes, Setup: &SetupInfo{Done: true},
		Update: &UpdateInfo{Current: "0.1.0", State: "idle"}, Commit: "0123abcd", IPs: []string{"192.168.1.20"}}}
	a.snap.Device = device(h.u.Tx.Code)
	a.snap.Device.At = h.now()
	if mod != nil {
		mod(&a.snap)
	}
	h.u.App = a
	h.wifiState()
	h.u.settingsOpen = true
	h.u.openPage(page)
	return a
}

// renderView is a state of the screen to draw (checkRenders): its name and how to get there.
type renderView struct {
	name string
	set  func(h *harness)
}

// checkRenders draws every view in every language at 1024×600 and fails if a text of the table had to be cut
// (fit() may shorten only data: SSIDs, names, URLs).
func checkRenders(t *testing.T, views []renderView) {
	t.Helper()
	for _, code := range Languages {
		for _, v := range views {
			var cut []string
			fitCut = func(s string) { cut = append(cut, s) }
			h, _ := drawHarness(t)
			h.u.SetLanguage(code)
			v.set(h)
			h.u.Dirty = true
			h.u.Tick()
			fitCut = nil
			if len(cut) > 0 {
				t.Logf("%s %s: shortened %q", code, v.name, cut)
			}
			var bad []string
			for _, s := range cut {
				if tableText(Lang(code), s) {
					bad = append(bad, fmt.Sprintf("%q", s))
				}
			}
			if len(bad) > 0 {
				sort.Strings(bad)
				t.Errorf("%s %s: cut %s", code, v.name, strings.Join(bad, ", "))
			}
		}
	}
}

// tableText: s is a text of the table (or begins with one: a row's sub-text built from parts).
func tableText(tx *Strings, s string) bool {
	found := false
	walkStrings(tx, func(t string) {
		if t == "" || found {
			return
		}
		if s == t || (len(t) > 6 && strings.HasPrefix(s, t)) {
			found = true
		}
	})
	return found
}

// walkStrings calls fn with every text of a table: strings, arrays and map values; a format, with what comes
// before its first verb.
func walkStrings(tx *Strings, fn func(string)) {
	walkValue(reflect.ValueOf(tx).Elem(), fn)
}

// walkValue is walkStrings on a table or one of its areas' (a struct inside it).
func walkValue(v reflect.Value, fn func(string)) {
	str := func(s string) {
		if i := strings.IndexByte(s, '%'); i >= 0 {
			s = s[:i]
		}
		fn(s)
	}
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Struct:
			walkValue(f, fn)
		case reflect.String:
			str(f.String())
		case reflect.Array, reflect.Slice:
			for j := range f.Len() {
				str(f.Index(j).String())
			}
		case reflect.Map:
			for _, k := range f.MapKeys() {
				str(f.MapIndex(k).String())
			}
		}
	}
}
