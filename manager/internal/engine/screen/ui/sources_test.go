package ui

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func slotsApp() *SlotsInfo {
	cam := "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0001-video-index0"
	return &SlotsInfo{EngineSupports: true,
		Slots: []SlotInfo{{Slot: 1, Assign: "hdmi", Alias: "Principal", Current: &SlotSource{ID: "hdmi", Name: "HDMI"}},
			{Slot: 2, Assign: "auto"}, {Slot: 3, Assign: "auto"}},
		Candidates: []SlotCandidate{{ID: "hdmi", Name: "HDMI", Kind: "hdmi-csi", Connected: true},
			{ID: cam, Name: "Cam Link", Kind: "usb", Connected: false}}}
}

func TestSourcesPage(t *testing.T) {
	h, app := pagesHarness(t, "wifi")
	app.snap.Slots, app.snap.SlotsAPI = slotsApp(), Yes
	h.press("s_page_sources", 0.1)
	if !app.watch().Slots && h.u.Tick() && !app.watch().Slots {
		t.Fatal("slots not watched")
	}
	// What the page shows: the app's, slot 4 missing = auto
	base := h.u.slotsBase(&app.snap)
	if len(base) != 4 || base[3] != (SlotAssign{Slot: 4, Assign: "auto"}) || base[0].Alias != "Principal" {
		t.Fatal(base)
	}
	// The picker: automatic, off and the candidates
	h.press("s_src_2", 0.1)
	if h.u.settingsView != "cands" || h.u.srcPick != 2 {
		t.Fatal(h.u.settingsView, h.u.srcPick)
	}
	if got := h.u.candidates(&app.snap); !reflect.DeepEqual(got[:2], []string{"auto", "off"}) || len(got) != 4 {
		t.Fatal(got)
	}
	h.u.candShown = h.u.candidates(&app.snap)
	h.press("s_cand_1", 0.1) // off
	if h.u.settingsView != "main" || h.u.srcDraft[1].Assign != "off" {
		t.Fatal(h.u.srcDraft)
	}
	// A source already pinned to another slot: not twice, it says so
	h.press("s_src_3", 0.1)
	h.u.candShown = h.u.candidates(&app.snap)
	h.press("s_cand_2", 0.1) // hdmi, pinned to 1
	if h.u.srcDraft[2].Assign != "auto" || h.u.srcNote == "" {
		t.Fatal(h.u.srcDraft, h.u.srcNote)
	}
	// The alias: the keyboard with what it has; Done puts it in the draft (trimmed); a bad one says why
	h.press("s_alias_2", 0.1)
	if h.u.settingsView != "keyboard" || h.u.kb.mode != "alias" || h.u.kb.slot != 2 || h.u.kb.secret() {
		t.Fatal(h.u.kb)
	}
	for _, k := range []string{"SHIFT", "G", "SHIFT", "r", "u", "a", " "} { // Shift stays until touched again
		h.tapKey(k, 0.1)
	}
	h.tapKey("OK", 0.1)
	if h.u.settingsView != "main" || h.u.srcDraft[1].Alias != "Grua" { // trimmed
		t.Fatalf("%+v", h.u.srcDraft[1])
	}
	h.press("s_alias_1", 0.1)
	if string(h.u.kb.text) != "Principal" {
		t.Fatalf("%q", string(h.u.kb.text))
	}
	h.tapKey("SYM", 0.1)
	h.tapKey("@", 0.1)
	h.tapKey("OK", 0.1)
	if h.u.settingsView != "keyboard" || h.u.kb.err == "" {
		t.Fatal("a bad alias is taken")
	}
	h.press("k_cancel", 0.1)
	if h.u.settingsView != "main" || h.u.srcDraft[0].Alias != "Principal" {
		t.Fatal("cancel")
	}
	// Save: all N slots
	h.press("s_src_save", 0.1)
	a := app.lastDid()
	want := []SlotAssign{{1, "hdmi", "Principal"}, {2, "off", "Grua"}, {3, "auto", ""}, {4, "auto", ""}}
	if a.What != "slots" || !reflect.DeepEqual(a.Slots, want) {
		t.Fatalf("%+v", a)
	}
	// The app refuses one field: its message on the row
	h.advance(time.Second)
	app.snap.Last = &AppResult{What: "slots", Status: 400, Code: "validation", At: h.now(),
		Fields: map[string]AppFieldError{"slots.2.alias": {Code: "invalid", Message: "mal"}}}
	if e := h.u.srcFieldError(&app.snap, 2, "alias"); e != "mal" {
		t.Fatalf("%q", e)
	}
	// The app takes it: its answer is the new state; the draft goes
	h.advance(time.Second)
	app.snap.Slots.Slots = []SlotInfo{{Slot: 1, Assign: "hdmi", Alias: "Principal"}, {Slot: 2, Assign: "off", Alias: "Grua"},
		{Slot: 3, Assign: "auto"}, {Slot: 4, Assign: "auto"}}
	app.snap.Last = &AppResult{What: "slots", OK: true, At: h.now()}
	h.u.srcSync(&app.snap)
	if h.u.srcDraft != nil || h.u.srcDirty(&app.snap) {
		t.Fatal("the draft stays after saving")
	}
	// Nothing changed: Save does nothing
	n := len(app.did)
	h.press("s_src_save", 0.1)
	if len(app.did) != n {
		t.Fatal("saved without changes")
	}
	// Leaving the page drops unsaved edits
	h.u.srcEdit()[2].Assign = "off"
	h.openPage("about")
	if h.u.srcDraft != nil {
		t.Fatal("the draft stays on another page")
	}
	if aliasOK("Cámara · 1") || !aliasOK("Cámara (1) #2 + B&W/x") || aliasOK("1234567890123456789012345") ||
		!aliasOK("Cámara d’Ana") || !nameOK("Estudio d’Ana") {
		t.Error("aliasOK")
	}
}

// The tiles (and the program's name) use the slot's alias, else the device's name.
func TestTileAlias(t *testing.T) {
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	h, _ := drawHarness(t)
	st := *h.u.st
	st.Sources = append([]SourceStatus(nil), st.Sources...)
	st.Sources[0].Alias = "Cámara principal"
	h.u.SetStatus(&st)
	h.u.Dirty = true
	h.u.Tick()
	var tile, usb bool
	for _, s := range shown {
		tile = tile || s == "1 · Cámara principal"
		usb = usb || s == "2 · usb1"
	}
	if !tile || !usb {
		t.Fatalf("%q", shown)
	}
}

// The engine's own sources (hdmi, i2s, test: it names them in Spanish) are named in the screen's language
// everywhere a source name is drawn, unless the slot has an alias; the other names stay as they are.
func TestSourceNamesTranslated(t *testing.T) {
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	h, _ := drawHarness(t)
	h.u.SetLanguage("fr")
	st := &Status{}
	st.Program.Layout = "full"
	main := "hdmi"
	st.Program.Main = &main
	st.Sources = []SourceStatus{{ID: "hdmi", Slot: 1, Kind: "hdmi-csi", Name: "HDMI de la placa", State: "live"},
		{ID: "test", Slot: 2, Kind: "test", Name: "Barras", State: "live"},
		{ID: "/dev/v4l/by-id/usb-Cam", Slot: 3, Kind: "usb", Name: "Cam Link 4K", State: "live"}}
	g := 0.0
	st.Audio = &AudioStatus{Mode: "single", Sources: []AudioInput{{ID: "i2s", Name: "HDMI de la placa", State: "live",
		VideoSlot: 1, GainDb: &g}}}
	h.u.SetStatus(st)
	draw := func() {
		shown = shown[:0]
		h.u.Dirty = true
		h.u.Tick()
	}
	has := func(s string) bool { return slices.Contains(shown, s) }
	draw()
	if !has("1 · HDMI de la carte") || !has("2 · Mire") || !has("3 · Cam Link 4K") || !has("HDMI de la carte") {
		t.Fatalf("tiles and program: %q", shown)
	}
	for _, s := range shown {
		if strings.Contains(s, "placa") || strings.Contains(s, "Barras") {
			t.Fatalf("a Spanish name on the French screen: %q", s)
		}
	}
	h.u.view = "audio"
	draw()
	if !has("1 · HDMI de la carte") {
		t.Fatalf("audio strip: %q", shown)
	}
	// An alias wins (also on the audio strip of its slot)
	st2 := *st
	st2.Sources = append([]SourceStatus(nil), st.Sources...)
	st2.Sources[0].Alias = "Plateau"
	h.u.SetStatus(&st2)
	draw()
	if !has("1 · Plateau") || has("1 · HDMI de la carte") {
		t.Fatalf("alias: %q", shown)
	}
	// The Sources page and its picker
	h.u.view = "video"
	app := &fakeApp{snap: AppSnapshot{DeviceAPI: Yes, SlotsAPI: Yes, Device: device("fr"), Slots: &SlotsInfo{EngineSupports: true,
		Slots: []SlotInfo{{Slot: 1, Assign: "auto", Current: &SlotSource{ID: "hdmi", Name: "HDMI de la placa"}},
			{Slot: 2, Assign: "test"}},
		Candidates: []SlotCandidate{{ID: "hdmi", Name: "HDMI de la placa", Kind: "hdmi-csi", Connected: true},
			{ID: "test", Name: "Barras", Kind: "test", Connected: true}}}}}
	h.u.App = app
	h.u.settingsOpen, h.u.settingsPage = true, "sources"
	draw()
	if !has("HDMI de la carte") || !has("Mire") {
		t.Fatalf("Sources page: %q", shown)
	}
	h.u.srcTouch("s_src_2", "", false)
	draw()
	if !has("HDMI de la carte") || !has("Mire") {
		t.Fatalf("picker: %q", shown)
	}
	// Spanish stays as the engine says it
	h.u.SetLanguage("es")
	if h.u.sourceName("hdmi", "x") != "HDMI de la placa" || h.u.sourceName("test", "x") != "Barras" ||
		h.u.sourceName("net:cam1", "Móvil") != "Móvil" {
		t.Fatal("Spanish names")
	}
}
