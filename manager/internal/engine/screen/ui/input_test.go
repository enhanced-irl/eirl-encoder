package ui

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	inpCam  = "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0001-video-index0"
	inpUSB2 = "/dev/v4l/by-path/platform-70090000.xusb-usb-0:2.4:1.0-video-index0"
	inpGone = "/dev/v4l/by-id/usb-MACROSILICON_USB_Video_0002-video-index0"
)

// inputCatalogue is a GET /sources: the board's HDMI, a USB 3 Cam Link (three modes, one taking most of the bus), a
// USB 2 card with warnings and audio, the test pattern; the audio inputs (a network camera's among them).
func inputCatalogue() *SourcesInfo {
	cam := []CaptureOption{{Format: "NV12", Mode: "1920x1080@60", FPS: 60, OutWidth: 1920, OutHeight: 1080, BusShare: 0.74},
		{Format: "NV12", Mode: "3840x2160@30", FPS: 30, OutWidth: 1920, OutHeight: 1080, BusShare: 0.9},
		{Format: "YUY2", Mode: "1280x720@60000/1001", FPS: 59.94, OutWidth: 1280, OutHeight: 720, BusShare: 0.3}}
	usb2 := []CaptureOption{{Format: "MJPG", Mode: "1920x1080@30", FPS: 30, OutWidth: 1920, OutHeight: 1080}}
	return &SourcesInfo{
		Sources: []SourceChoice{{ID: "hdmi", Kind: "hdmi-csi", Name: "HDMI de la placa"},
			{ID: inpCam, Kind: "usb", Name: "Cam Link 4K", USB3: true, USBKnown: true, Auto: &cam[0], Options: cam},
			{ID: inpUSB2, Kind: "usb", Name: "USB3. 0 capture", USBKnown: true, Auto: &usb2[0], Options: usb2, AudioName: "USB3. 0 capture",
				Warnings: []string{"usb2", "only_4k", "something_new"}},
			{ID: "test", Kind: "test", Name: "Patrón de prueba"}},
		Audio: []AudioChoice{{ID: "i2s", Kind: "i2s", Name: "HDMI de la placa"},
			{ID: "alsa:capture", Kind: "usb", Name: "USB3. 0 capture"}, {ID: "net:cam1", Kind: "network", Name: "Móvil"}}}
}

// inputData gives the app the saved input (the HDMI, 1080p30, automatic audio) and the catalogue.
func inputData(a *AppSnapshot) {
	a.Status, a.StatusAPI = &StatusInfo{Output: "off", Input: InputInfo{Source: "hdmi", Device: "auto",
		Format: "auto", Mode: "auto", Audio: "auto", EDIDPreferred: "1080p30", EDIDActive: "1080p30"}}, Yes
	a.Sources, a.SourcesAPI = inputCatalogue(), Yes
}

func inputHarness(t *testing.T) (*harness, *fakeApp) {
	h, app := pagesHarness(t, "sources")
	inputData(&app.snap)
	h.openPage("input")
	if h.u.settingsPage != "input" {
		t.Fatal(h.u.settingsPage)
	}
	return h, app
}

// pickChoice chooses id in the open picker, as a finger on its row would (the row's choice is fixed on the way down).
func pickChoice(t *testing.T, h *harness, id string) {
	t.Helper()
	if h.u.settingsView != "pick" || h.u.chooser == nil {
		t.Fatalf("no picker: %q", h.u.settingsView)
	}
	if !slices.ContainsFunc(h.u.chooser.opts, func(o pickOpt) bool { return o.ID == id }) {
		t.Fatalf("%s is not a choice: %+v", id, h.u.chooser.opts)
	}
	h.u.pageTouch("s_pick_0", id, true)
	if h.u.settingsView != "main" {
		t.Fatal("the picker stays open")
	}
}

func optIDs(opts []pickOpt) []string {
	var out []string
	for _, o := range opts {
		out = append(out, o.ID)
	}
	return out
}

func setOutput(h *harness, mode string) {
	st := *h.u.st
	st.Output.Mode = mode
	h.u.SetStatus(&st)
}

func TestInputPage(t *testing.T) {
	h, app := inputHarness(t)
	h.u.Tick()
	if w := app.watch(); !w.Status || !w.Sources || w.Slots {
		t.Fatalf("watch %+v", w)
	}
	keys := h.u.pageKeys("input", &h.net.snap)
	if want := []string{"s_inp_source", "s_inp_scan", "s_inp_edid", "s_inp_audio", "s_inp_save", "s_inp_apply"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("HDMI keys %v", keys)
	}
	// Nothing changed: neither button sends
	h.press("s_inp_save", 0.1)
	h.press("s_inp_apply", 0.1)
	if len(app.did) != 0 {
		t.Fatal(app.did)
	}
	// The source's choices: the HDMI, each USB with its bus, any USB, the test pattern
	h.press("s_inp_source", 0.1)
	if got, want := optIDs(h.u.chooser.opts), []string{"hdmi", "usb|" + inpCam, "usb|" + inpUSB2, "usb|auto", "test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sources %v", got)
	}
	if o := h.u.chooser.opts; o[0].Title != "HDMI de la placa" || o[1].Title != "Cam Link 4K · USB 3" ||
		o[2].Title != "USB3. 0 capture · USB 2" || o[2].Sub != "trae audio (USB3. 0 capture)" ||
		o[3].Sub != "Ahora: Cam Link 4K · USB 3" || o[4].Title != "Patrón de prueba" || h.u.chooser.cur != "hdmi" {
		t.Fatalf("%+v", o)
	}
	pickChoice(t, h, "usb|"+inpCam)
	if d := h.u.inp.draft; d == nil || d.Source != "usb|"+inpCam || d.Mode != "auto" {
		t.Fatalf("%+v", d)
	}
	keys = h.u.pageKeys("input", &h.net.snap)
	if !slices.Contains(keys, "s_inp_mode") || slices.Contains(keys, "s_inp_edid") {
		t.Fatalf("USB keys %v", keys)
	}
	// Its modes: automatic (with what it takes), then each one; a heavy one says so
	h.press("s_inp_mode", 0.1)
	opts := h.u.chooser.opts
	if got := optIDs(opts); !reflect.DeepEqual(got, []string{"auto", "NV12|1920x1080@60", "NV12|3840x2160@30",
		"YUY2|1280x720@60000/1001"}) {
		t.Fatalf("modes %v", got)
	}
	if opts[0].Title != "Automático (1920×1080 @ 60 · sin comprimir)" ||
		opts[2].Title != "3840×2160 @ 30 · sin comprimir → 1920×1080" || !opts[2].Warn ||
		!strings.Contains(opts[2].Sub, "90 %") || opts[3].Title != "1280×720 @ 59,94 · sin comprimir" || opts[3].Warn {
		t.Fatalf("%+v", opts)
	}
	pickChoice(t, h, "YUY2|1280x720@60000/1001")
	// The audio: the source's, none, each one but the network cameras'
	h.press("s_inp_audio", 0.1)
	if got := optIDs(h.u.chooser.opts); !reflect.DeepEqual(got, []string{"auto", "none", "i2s", "alsa:capture"}) {
		t.Fatalf("audio %v", got)
	}
	pickChoice(t, h, "alsa:capture")
	// Save: the web's body
	h.press("s_inp_save", 0.1)
	a := app.lastDid()
	want := map[string]any{"source": "usb", "device": inpCam, "format": "YUY2", "mode": "1280x720@60000/1001",
		"audio": "alsa:capture", "apply": false}
	if a.What != "input_save" || !reflect.DeepEqual(a.Body, want) {
		t.Fatalf("%+v", a)
	}
	// The app takes it: the draft stays until the status shows it, then goes
	h.advance(time.Second)
	app.snap.Last = &AppResult{What: "input_save", OK: true, At: h.now()}
	h.u.inputSync(&app.snap)
	if h.u.inp.draft == nil {
		t.Fatal("the draft went before the status showed it")
	}
	if txt, _ := h.u.inputNote(&app.snap); txt != Spanish.SavedLater {
		t.Fatalf("note %q", txt)
	}
	app.snap.Status.Input = InputInfo{Source: "usb", Device: inpCam, Format: "YUY2", Mode: "1280x720@60000/1001",
		Audio: "alsa:capture", EDIDPreferred: "1080p30"}
	h.u.inputSync(&app.snap)
	if h.u.inp.draft != nil || h.u.inpDirty(&app.snap) {
		t.Fatal("the draft stays")
	}
	// The saved one again in the picker: its mode comes back (another source starts automatic)
	h.press("s_inp_source", 0.1)
	pickChoice(t, h, "usb|"+inpUSB2)
	if h.u.inp.draft.Mode != "auto" {
		t.Fatal(h.u.inp.draft)
	}
	h.press("s_inp_source", 0.1)
	pickChoice(t, h, "usb|"+inpCam)
	if h.u.inp.draft.Mode != "YUY2|1280x720@60000/1001" || h.u.inpDirty(&app.snap) {
		t.Fatalf("%+v", h.u.inp.draft)
	}
	// Leaving the page drops the draft
	h.press("s_inp_audio", 0.1)
	pickChoice(t, h, "none")
	h.openPage("sources")
	if h.u.inp.draft != nil {
		t.Fatal("the draft stays on another page")
	}
}

// «Save and apply» while streaming cuts the stream a few seconds: a second tap; not streaming, at once. It also
// applies what is saved and not running (another EDID in use) without changes.
func TestInputApply(t *testing.T) {
	h, app := inputHarness(t)
	h.press("s_inp_edid", 0.1)
	if got := optIDs(h.u.chooser.opts); !reflect.DeepEqual(got, EDIDModes) || h.u.chooser.opts[0].Title != "1080p30 (recomendado)" {
		t.Fatalf("%+v", h.u.chooser.opts)
	}
	pickChoice(t, h, "720p60")
	setOutput(h, "on")
	h.press("s_inp_apply", 0.1)
	if len(app.did) != 0 || !h.u.confirming("input_apply") {
		t.Fatal("applied at the first tap while streaming")
	}
	if txt, warn := h.u.inputNote(&app.snap); txt != Spanish.ApplyCuts || !warn {
		t.Fatalf("%q", txt)
	}
	h.press("s_inp_apply", 0.1)
	want := map[string]any{"source": "hdmi", "audio": "auto", "edidPreferred": "720p60", "apply": true}
	if a := app.lastDid(); a.What != "input_save" || !reflect.DeepEqual(a.Body, want) {
		t.Fatalf("%+v", a)
	}
	// While it is under way, nothing more; it says so
	app.snap.Pending = "input_save"
	h.press("s_inp_save", 0.1)
	if len(app.did) != 1 {
		t.Fatal("a second save while one is under way")
	}
	if txt, _ := h.u.inputNote(&app.snap); txt != Spanish.Input.Saving {
		t.Fatal(txt)
	}
	// The stream restarted to take it
	app.snap.Pending = ""
	yes := true
	h.advance(time.Second)
	app.snap.Last = &AppResult{What: "input_save", OK: true, Restarted: &yes, At: h.now()}
	if txt, _ := h.u.inputNote(&app.snap); txt != Spanish.SavedRestarting {
		t.Fatal(txt)
	}
	// The confirmation expires
	h.u.inp = inputState{}
	h.press("s_inp_edid", 0.1)
	pickChoice(t, h, "1080p50")
	h.press("s_inp_apply", 0.1)
	h.advance(ConfirmFor + time.Second)
	h.press("s_inp_apply", 0.1)
	if len(app.did) != 1 {
		t.Fatal("a confirmation that expired applied")
	}
	// Not streaming: at once (and the note says it applies when the stream starts)
	setOutput(h, "off")
	h.u.confirm = nil
	h.press("s_inp_apply", 0.1)
	if a := app.lastDid(); len(app.did) != 2 || a.Body["edidPreferred"] != "1080p50" || a.Body["apply"] != true {
		t.Fatalf("%+v", app.did)
	}
	h.advance(time.Second)
	app.snap.Last = &AppResult{What: "input_save", OK: true, At: h.now()}
	if txt, _ := h.u.inputNote(&app.snap); txt != Spanish.SavedStopped {
		t.Fatal(txt)
	}
	// Nothing changed but another EDID is running: «Save and apply» applies it; «Save» does nothing
	h.u.inp = inputState{}
	app.snap.Status.Input.EDIDActive = "1080p50"
	n := len(app.did)
	h.press("s_inp_save", 0.1)
	if len(app.did) != n {
		t.Fatal("saved without changes")
	}
	h.press("s_inp_apply", 0.1)
	if a := app.lastDid(); len(app.did) != n+1 || a.Body["edidPreferred"] != "1080p30" || a.Body["apply"] != true {
		t.Fatalf("%+v", a)
	}
	// The app's queue is full: it says so
	h.u.inp = inputState{}
	app.refuse = true
	h.press("s_inp_edid", 0.1)
	pickChoice(t, h, "720p60")
	h.press("s_inp_save", 0.1)
	if h.u.hint != Spanish.BusyChange || h.u.inp.sent != nil {
		t.Fatalf("%q", h.u.hint)
	}
}

// The body of each source, exactly the web's; a USB «the first one found» is automatic all through.
func TestInputBodies(t *testing.T) {
	for _, c := range []struct {
		d    inputDraft
		want map[string]any
	}{
		{inputDraft{Source: "hdmi", Mode: "auto", EDID: "1080p50", Audio: "i2s"},
			map[string]any{"source": "hdmi", "audio": "i2s", "edidPreferred": "1080p50", "apply": false}},
		{inputDraft{Source: "usb|auto", Mode: "auto", EDID: "1080p30", Audio: "auto"},
			map[string]any{"source": "usb", "device": "auto", "format": "auto", "mode": "auto", "audio": "auto", "apply": false}},
		{inputDraft{Source: "usb|" + inpCam, Mode: "MJPG|1920x1080@30", EDID: "1080p30", Audio: "none"},
			map[string]any{"source": "usb", "device": inpCam, "format": "MJPG", "mode": "1920x1080@30", "audio": "none",
				"apply": false}},
		{inputDraft{Source: "test", Mode: "auto", EDID: "720p60", Audio: "none"},
			map[string]any{"source": "test", "audio": "none", "apply": false}},
	} {
		if got := c.d.body(false); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: %v", c.d, got)
		}
	}
	// What a source does not send does not make it a change
	a := inputDraft{Source: "test", Mode: "auto", EDID: "720p60", Audio: "none"}
	b := inputDraft{Source: "test", Mode: "MJPG|x", EDID: "1080p30", Audio: "none"}
	if a.norm() != b.norm() {
		t.Error("norm")
	}
}

// What is saved and the catalogue does not have shows as such: a USB unplugged, the HDMI not detected, a mode
// or an audio that is not there. The test pattern has no second field: the audio goes up.
func TestInputSavedChoices(t *testing.T) {
	h, app := inputHarness(t)
	app.snap.Status.Input = InputInfo{Source: "usb", Device: inpGone, Format: "MJPG", Mode: "1280x720@30",
		Audio: "alsa:Gone", ActiveState: "waiting_device"}
	opts := h.u.sourceOpts(&app.snap)
	if i := slices.IndexFunc(opts, func(o pickOpt) bool { return o.ID == "usb|"+inpGone }); i < 0 ||
		opts[i].Title != Spanish.Input.USBSaved || opts[i].Sub != "usb-MACROSILICON_USB_Video_0002-video-index0" {
		t.Fatalf("%+v", opts)
	}
	cur := h.u.inpShown(&app.snap)
	if l, missing := h.u.sourceLabel(&app.snap, cur.Source); l != Spanish.Input.USBSaved || !missing {
		t.Fatal(l)
	}
	if m := h.u.modeOpts(&app.snap, cur); len(m) != 2 || m[1].ID != "MJPG|1280x720@30" ||
		m[1].Title != "MJPG · 1280x720@30 (guardado)" || m[0].Title != Spanish.Input.ModeAuto {
		t.Fatalf("%+v", m)
	}
	if a := h.u.audioOpts(&app.snap, cur); a[len(a)-1].ID != "alsa:Gone" || a[len(a)-1].Title != "Gone (no conectada)" {
		t.Fatalf("%+v", a)
	}
	if txt, warn := h.u.inputActive(&app.snap); txt != Spanish.Input.WaitingUSB || !warn {
		t.Fatal(txt)
	}
	// The HDMI saved but not detected: first, as not detected
	app.snap.Status.Input = InputInfo{Source: "hdmi", Audio: "auto", EDIDPreferred: "1080p30"}
	app.snap.Sources.Sources = app.snap.Sources.Sources[1:]
	if o := h.u.sourceOpts(&app.snap); o[0].ID != "hdmi" || o[0].Title != Spanish.Input.HDMIMissing {
		t.Fatalf("%+v", o)
	}
	// «The first USB»: its mode shown, not chosen
	app.snap.Status.Input = InputInfo{Source: "usb", Device: "auto", Format: "auto", Mode: "auto", Audio: "auto"}
	keys := h.u.pageKeys("input", &h.net.snap)
	if slices.Contains(keys, "s_inp_mode") || !slices.Contains(keys, "s_inp_audio") {
		t.Fatal(keys)
	}
	if got := h.u.inputInfo(h.u.inpShown(&app.snap), catalogSource(&app.snap, "usb|auto")); got !=
		"Ahora: Cam Link 4K · USB 3 · ocupa el 74 % del USB: no caben dos así" {
		t.Fatalf("%q", got)
	}
	// The test pattern: the audio a row up
	app.snap.Status.Input = InputInfo{Source: "test", Audio: "none"}
	keys = h.u.pageKeys("input", &h.net.snap)
	if !slices.Contains(keys, "s_inp_audio_up") || slices.Contains(keys, "s_inp_audio") {
		t.Fatal(keys)
	}
	h.press("s_inp_audio_up", 0.1)
	if h.u.settingsView != "pick" || h.u.chooser.title != Spanish.Input.Audio {
		t.Fatal("the audio a row up does not open its picker")
	}
	// What runs now: a USB capture in use; another EDID than the saved one
	app.snap.Status.Input = InputInfo{Source: "usb", Device: inpCam, ActiveState: "ready", ActiveName: "Cam Link 4K",
		ActiveFormat: "NV12", ActiveMode: "1920x1080@60"}
	if txt, _ := h.u.inputActive(&app.snap); txt != "En uso: Cam Link 4K · sin comprimir 1920×1080" {
		t.Fatal(txt)
	}
	app.snap.Status.Input = InputInfo{Source: "hdmi", EDIDPreferred: "1080p30", EDIDActive: "1080p50", ActiveState: "ready"}
	if txt, _ := h.u.inputActive(&app.snap); txt != "EDID en uso: 1080p50. Lo guardado se aplicará al reiniciar la emisión." {
		t.Fatal(txt)
	}
}

// The app's field errors go on their fields and are worded under them; the destination's say so.
func TestInputErrors(t *testing.T) {
	h, app := inputHarness(t)
	h.press("s_inp_edid", 0.1)
	pickChoice(t, h, "1080p50")
	h.press("s_inp_save", 0.1)
	h.advance(time.Second)
	app.snap.Last = &AppResult{What: "input_save", Status: 400, Code: "validation", Message: "Revisa los campos", At: h.now(),
		Fields: map[string]AppFieldError{"edidPreferred": {Code: "one_of", Message: "debe ser uno de: 1080p30"},
			"url": {Code: "url_missing", Message: "falta"}, "latencyMs": {Code: "range", Message: "fuera de rango"},
			"audio": {Code: "invalid"}}}
	h.u.inputSync(&app.snap)
	if h.u.inp.draft == nil || h.u.inp.sent != nil {
		t.Fatal("a refused save dropped the draft")
	}
	fields, words := h.u.inputErrors(&app.snap)
	if !fields["edid"] || !fields["audio"] || fields["source"] {
		t.Fatal(fields)
	}
	want := []string{"Audio · No se ha podido (invalid)", "Modo que pide el aparato (EDID) · debe ser uno de: 1080p30",
		"Revisa el destino: fuera de rango", Spanish.Input.NeedDest}
	if !reflect.DeepEqual(words, want) {
		t.Fatalf("%q", words)
	}
	if txt, warn := h.u.inputNote(&app.snap); txt != "Revisa los campos" || !warn {
		t.Fatal(txt)
	}
	// An app without the route over the socket: not available, not its technical message
	app.snap.Last = &AppResult{What: "input_save", Status: 403, Code: "local_forbidden", Message: "x", At: h.now()}
	if txt, _ := h.u.inputNote(&app.snap); txt != Spanish.PageUnavailable {
		t.Fatal(txt)
	}
}

// «Scan» asks for the catalogue again (once at a time) and says it is done.
func TestInputScan(t *testing.T) {
	h, app := inputHarness(t)
	h.press("s_inp_scan", 0.1)
	if a := app.lastDid(); a.What != "sources_scan" || a.Body != nil {
		t.Fatalf("%+v", a)
	}
	app.snap.Pending = "sources_scan"
	h.press("s_inp_scan", 0.1)
	if len(app.did) != 1 {
		t.Fatal("two scans at once")
	}
	app.snap.Pending = ""
	h.advance(time.Second)
	app.snap.Last = &AppResult{What: "sources_scan", OK: true, At: h.now()}
	if txt, _ := h.u.inputNote(&app.snap); txt != Spanish.Input.Scanned {
		t.Fatal(txt)
	}
	h.advance(5 * time.Second)
	if txt, _ := h.u.inputNote(&app.snap); txt != "" {
		t.Fatal(txt)
	}
}

// Asking the app, the app not answering, an older app without /status or /sources: no buttons, it says why.
func TestInputUnavailable(t *testing.T) {
	h, app := inputHarness(t)
	for _, c := range []struct {
		status, sources Tri
		have            bool
	}{{Unknown, Unknown, false}, {Yes, Unknown, false}, {Yes, Yes, false}, {No, Yes, false}, {Yes, No, true}} {
		app.snap.StatusAPI, app.snap.SourcesAPI = c.status, c.sources
		if !c.have {
			app.snap.Sources = nil
		} else {
			app.snap.Sources = inputCatalogue()
		}
		if keys := h.u.pageKeys("input", &h.net.snap); len(keys) != 0 {
			t.Errorf("%+v: %v", c, keys)
		}
	}
	if api, _ := inputAPI(&AppSnapshot{StatusAPI: Yes, SourcesAPI: No}); api != No {
		t.Error("an older app")
	}
}

// Each slot with a source in it has Hide (Show while hidden): the engine's command, live, not the slots' draft; the
// engine's refusal (the source on air) says so on the page.
func TestSlotsHide(t *testing.T) {
	h, app := pagesHarness(t, "sources")
	app.snap.Slots, app.snap.SlotsAPI = slotsApp(), Yes
	keys := h.u.pageKeys("sources", &h.net.snap)
	for _, k := range []string{"s_hide_1", "s_hide_2", "s_hide_3"} {
		if !slices.Contains(keys, k) {
			t.Fatalf("no %s in %v", k, keys)
		}
	}
	if slices.Contains(keys, "s_hide_4") {
		t.Fatal("Hide on an empty slot")
	}
	h.cmds = nil
	h.press("s_hide_2", 0.1)
	if len(h.cmds) != 1 || !reflect.DeepEqual(h.cmds[0], map[string]any{"cmd": "hide", "source": "usb1", "hidden": true}) {
		t.Fatal(h.cmds)
	}
	if h.u.srcDraft != nil || len(app.did) != 0 {
		t.Fatal("Hide went into the slots' draft or to the app")
	}
	// Hidden: Show
	st := *h.u.st
	st.Sources = append([]SourceStatus(nil), st.Sources...)
	st.Sources[1].State = "hidden"
	h.u.SetStatus(&st)
	h.cmds = nil
	h.press("s_hide_2", 0.1)
	if len(h.cmds) != 1 || h.cmds[0]["hidden"] != false {
		t.Fatal(h.cmds)
	}
	// The one on air: the engine refuses it, the page says so
	h.cmds = nil
	h.press("s_hide_1", 0.1)
	if len(h.cmds) != 1 || h.cmds[0]["source"] != "hdmi" {
		t.Fatal(h.cmds)
	}
	h.u.CommandDone(Command{Msg: h.cmds[0], Origin: "page"}, false, "on_air")
	if txt, warn := h.u.pageNoteText(); !warn || txt != "No se puede: está al aire" {
		t.Fatalf("%q %v", txt, warn)
	}
	// Drawn: Show in the hidden slot, «hidden» under its number, the note at the bottom
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	d, _ := drawHarness(t)
	d.u.SetStatus(&st)
	pageApp(d, "sources", func(a *AppSnapshot) { a.Slots, a.SlotsAPI = slotsApp(), Yes })
	d.u.CommandDone(Command{Msg: map[string]any{"cmd": "hide"}, Origin: "page"}, false, "on_air")
	d.u.Dirty = true
	d.u.Tick()
	for _, s := range []string{"Mostrar", "Ocultar", "oculta", "No se puede: está al aire", Spanish.Input.HideHelp} {
		if !slices.Contains(shown, s) && !slices.ContainsFunc(shown, func(x string) bool {
			return s == Spanish.Input.HideHelp && strings.HasPrefix(s, x) && x != ""
		}) {
			t.Errorf("%q not drawn: %q", s, shown)
		}
	}
}

// The Input page draws its notes (the app's answer while saving) and never cuts a text of the table.
func TestInputDrawsNote(t *testing.T) {
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	h, _ := drawHarness(t)
	yes := true
	pageApp(h, "input", func(a *AppSnapshot) {
		inputData(a)
		a.Last = &AppResult{What: "input_save", OK: true, Restarted: &yes, At: h.now().Add(time.Second)}
	})
	h.u.Dirty = true
	h.u.Tick()
	if !slices.Contains(shown, "Guardado: la emisión se está reiniciando…") || !slices.Contains(shown, "HDMI de la placa") ||
		!slices.Contains(shown, "1080p30 (recomendado)") {
		t.Fatalf("%q", shown)
	}
}

func TestRenderInput(t *testing.T) {
	input := func(mod func(h *harness, a *AppSnapshot)) func(h *harness) {
		return func(h *harness) {
			a := pageApp(h, "input", func(a *AppSnapshot) { inputData(a) })
			if mod != nil {
				mod(h, &a.snap)
			}
		}
	}
	usb := func(dev, format, mode, audio string) func(h *harness, a *AppSnapshot) {
		return func(h *harness, a *AppSnapshot) {
			a.Status.Input = InputInfo{Source: "usb", Device: dev, Format: format, Mode: mode, Audio: audio,
				EDIDPreferred: "1080p30", ActiveState: "ready", ActiveName: "Cam Link 4K", ActiveFormat: "NV12",
				ActiveMode: "3840x2160@30"}
		}
	}
	long := func(a *AppSnapshot) { // long data: a French device name, the USB 2 card with every warning
		a.Sources.Sources[2].Name = "Carte de capture vidéo USB très longue de démonstration"
		a.Sources.Sources[2].Warnings = []string{"usb2", "only_4k", "no_format", "unreadable"}
	}
	checkRenders(t, []renderView{
		{"input-hdmi", input(nil)},
		{"input-hdmi-edid", input(func(h *harness, a *AppSnapshot) { a.Status.Input.EDIDActive = "1080p50" })},
		{"input-usb", input(usb(inpCam, "NV12", "3840x2160@30", "auto"))},
		{"input-usb-warn", input(func(h *harness, a *AppSnapshot) {
			long(a)
			usb(inpUSB2, "auto", "auto", "alsa:capture")(h, a)
			a.Status.Input.ActiveState = "waiting_device"
		})},
		{"input-usb-auto", input(usb("auto", "auto", "auto", "auto"))},
		{"input-usb-gone", input(usb(inpGone, "MJPG", "1280x720@30", "alsa:Gone"))},
		{"input-test", input(func(h *harness, a *AppSnapshot) { a.Status.Input = InputInfo{Source: "test", Audio: "none"} })},
		{"input-hdmi-missing", input(func(h *harness, a *AppSnapshot) { a.Sources.Sources = a.Sources.Sources[1:] })},
		{"input-edited", input(func(h *harness, a *AppSnapshot) {
			h.u.Preview().InputEdit("usb|"+inpCam, "NV12|3840x2160@30", "", "none")
		})},
		{"input-confirm", input(func(h *harness, a *AppSnapshot) {
			setOutput(h, "on")
			h.u.Preview().InputEdit("", "", "720p60", "")
			h.u.Preview().InputConfirmApply()
		})},
		{"input-saving", input(func(h *harness, a *AppSnapshot) {
			h.u.Preview().InputEdit("", "", "720p60", "")
			a.Pending = "input_save"
		})},
		{"input-errors", input(func(h *harness, a *AppSnapshot) {
			long(a)
			h.u.Preview().InputEdit("usb|"+inpUSB2, "", "", "")
			a.Last = &AppResult{What: "input_save", Status: 400, Code: "validation", At: h.now().Add(time.Second),
				Message: "Certains champs sont incorrects, vérifiez-les avant d'enregistrer de nouveau",
				Fields: map[string]AppFieldError{"device": {Message: "appareil inconnu"}, "url": {Message: "x"},
					"latencyMs": {Message: "entre 20 et 8000 ms"}}}
		})},
		{"input-saved-stopped", input(func(h *harness, a *AppSnapshot) {
			h.u.inp.apply = true
			a.Last = &AppResult{What: "input_save", OK: true, At: h.now().Add(time.Second)}
		})},
		{"input-saved-later", input(func(h *harness, a *AppSnapshot) {
			a.Last = &AppResult{What: "input_save", OK: true, At: h.now().Add(time.Second)}
		})},
		{"input-scanned", input(func(h *harness, a *AppSnapshot) {
			a.Last = &AppResult{What: "sources_scan", OK: true, At: h.now().Add(time.Second)}
		})},
		{"input-pick-source", input(func(h *harness, a *AppSnapshot) { long(a); h.u.Preview().InputPick("s_inp_source") })},
		{"input-pick-mode", input(func(h *harness, a *AppSnapshot) {
			usb(inpCam, "NV12", "3840x2160@30", "auto")(h, a)
			h.u.Preview().InputPick("s_inp_mode")
		})},
		{"input-pick-edid", input(func(h *harness, a *AppSnapshot) { h.u.Preview().InputPick("s_inp_edid") })},
		{"input-pick-audio", input(func(h *harness, a *AppSnapshot) { h.u.Preview().InputPick("s_inp_audio") })},
		{"input-loading", func(h *harness) { pageApp(h, "input", nil) }},
		{"input-no-answer", func(h *harness) {
			pageApp(h, "input", func(a *AppSnapshot) { a.StatusAPI, a.SourcesAPI = Yes, Yes })
		}},
		{"input-old", func(h *harness) {
			pageApp(h, "input", func(a *AppSnapshot) { a.StatusAPI, a.SourcesAPI = No, No })
		}},
		{"sources-hidden", func(h *harness) {
			st := *h.u.st
			st.Sources = append([]SourceStatus(nil), st.Sources...)
			st.Sources[1].State = "hidden"
			h.u.SetStatus(&st)
			pageApp(h, "sources", func(a *AppSnapshot) { a.Slots, a.SlotsAPI = slotsApp(), Yes })
			h.u.CommandDone(Command{Msg: map[string]any{"cmd": "hide"}, Origin: "page"}, false, "on_air")
		}},
	})
}

// The Input page's and the slots' Hide buttons: inside the page, ≥ 48 px, not overlapping.
func TestInputLayout(t *testing.T) {
	for _, sz := range [][2]int{{1024, 600}, {1280, 720}, {1920, 1080}} {
		lay := NewLayout(sz[0], sz[1], 4)
		in := func(r, box Rect) bool {
			return r.X >= box.X && r.Y >= box.Y && r.X+r.W <= box.X+box.W && r.Y+r.H <= box.Y+box.H
		}
		groups := [][]string{{"s_inp_source", "s_inp_scan", "s_inp_mode", "s_inp_audio", "s_inp_save", "s_inp_apply"},
			{"s_inp_source", "s_inp_scan", "s_inp_edid", "s_inp_audio", "s_inp_save", "s_inp_apply"},
			{"s_inp_source", "s_inp_scan", "s_inp_audio_up", "s_inp_save", "s_inp_apply"}, {"s_src_save"}}
		for i := range lay.N {
			n := string(rune('1' + i))
			groups[3] = append(groups[3], "s_src_"+n, "s_alias_"+n, "s_hide_"+n)
		}
		for _, keys := range groups {
			for i, k := range keys {
				r, ok := lay.Buttons[k]
				if !ok {
					t.Fatalf("%v: no %s", sz, k)
				}
				if !in(r, lay.SettingsPage) || r.H < 48 || r.W < 48 {
					t.Errorf("%v: %s %v (page %v)", sz, k, r, lay.SettingsPage)
				}
				for _, k2 := range keys[i+1:] {
					if overlaps(r, lay.Buttons[k2]) {
						t.Errorf("%v: %s overlaps %s", sz, k, k2)
					}
				}
			}
		}
	}
}
