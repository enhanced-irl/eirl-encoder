package ui

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The Input page (Sources → Input, MIXER.md §21): the web's «Main source» card. Which source goes on air when the
// stream starts and gives the audio (with the mixer every source is captured at once), how a USB one is captured,
// the EDID the board's HDMI asks for and the audio. The choices come from the app's catalogue (GET /sources: what
// is connected and what each USB source can be captured with, the same rule the stream applies: nothing is
// computed here) and what is saved from GET /status. Edits stay on the screen (a draft) until «Save» or «Save and
// apply»: PUT /input with the web's body.

// EDIDModes are the modes the board's HDMI can ask for (the app's config.EDIDModes), the recommended one first.
var EDIDModes = []string{"1080p30", "1080p50", "720p60"}

// inputWarnings are the catalogue's warnings the page words (the web's SOURCE_WARN); others are not shown.
var inputWarnings = []string{"usb2", "only_4k", "no_format", "unreadable"}

// rawFormats are the uncompressed capture formats (the web's RAW_FORMATS).
var rawFormats = []string{"NV12", "YUY2", "I420", "UYVY"}

// inputState is what the screen keeps of the Input tab (Sources → Input) and the slots' Hide: its draft, its picker… (MIXER.md §21).
type inputState struct {
	draft   *inputDraft // the fields being edited (nil: what is saved)
	sent    *inputDraft // the draft last saved: it goes when the app's status shows it
	savedAt time.Time   // when the last save was asked: results after it are its own
	apply   bool        // the last save was «Save and apply»
	scanAt  time.Time   // when «Scan» was asked
}

// inputDraft is the page's fields, as the web's selects hold them.
type inputDraft struct {
	Source string // hdmi | test | usb|<device> | usb|auto
	Mode   string // usb: auto | <format>|<mode>
	EDID   string // hdmi
	Audio  string // auto | none | i2s | alsa:<card>
}

// norm blanks what the source does not use (it is not sent): a draft only differs from what is saved in what
// would be sent.
func (d inputDraft) norm() inputDraft {
	kind, _ := splitSource(d.Source)
	if kind != "hdmi" {
		d.EDID = ""
	}
	if kind != "usb" {
		d.Mode = ""
	}
	return d
}

// body is PUT /input's body, the web's: source and audio (and apply); edidPreferred for the HDMI; device, format
// and mode for a USB source.
func (d inputDraft) body(apply bool) map[string]any {
	kind, dev := splitSource(d.Source)
	b := map[string]any{"source": kind, "audio": d.Audio, "apply": apply}
	switch kind {
	case "hdmi":
		b["edidPreferred"] = d.EDID
	case "usb":
		f, m := "auto", "auto"
		if d.Mode != "auto" && d.Mode != "" {
			f, m = splitSource(d.Mode)
		}
		b["device"], b["format"], b["mode"] = dev, f, m
	}
	return b
}

// splitSource splits a choice at its first «|» (usb|<device>, <format>|<mode>).
func splitSource(v string) (string, string) {
	a, b, _ := strings.Cut(v, "|")
	return a, b
}

func orAuto(v string) string {
	if v == "" {
		return "auto"
	}
	return v
}

func init() {
	registerPage("input", &pageDef{
		layout: func(l *Layout) {
			rows, bottom := l.FormRows(4)
			b := l.Buttons
			sw := max(140, rows[0].W/5)
			b["s_inp_source"] = Rect{rows[0].X, rows[0].Y, rows[0].W - sw - l.M, rows[0].H}
			b["s_inp_scan"] = Rect{rows[0].X + rows[0].W - sw, rows[0].Y, sw, rows[0].H}
			b["s_inp_mode"] = rows[1]
			b["s_inp_edid"] = rows[1]
			b["s_inp_audio"] = rows[2]
			b["s_inp_audio_up"] = rows[1] // a source without a second field (the test pattern): the audio goes up
			aw := max(260, bottom.W*38/100)
			vw := max(150, bottom.W/5)
			b["s_inp_apply"] = Rect{bottom.X + bottom.W - aw, bottom.Y, aw, bottom.H}
			b["s_inp_save"] = Rect{bottom.X + bottom.W - aw - l.M - vw, bottom.Y, vw, bottom.H}
		},
		draw:  (*UI).drawInputPage,
		keys:  (*UI).inputKeys,
		touch: (*UI).inputTouch,
		watch: func(_ *UI, w *AppWatch) { w.Status, w.Sources = true, true },
		leave: func(u *UI) { u.inp = inputState{} },
	})
}

// inputAPI is the page's data: the saved input (GET /status) and the catalogue (GET /sources); an app without
// either route is an older one.
func inputAPI(app *AppSnapshot) (api Tri, have bool) {
	api = Yes
	switch {
	case app.StatusAPI == No || app.SourcesAPI == No:
		api = No
	case app.StatusAPI == Unknown || app.SourcesAPI == Unknown:
		api = Unknown
	}
	return api, app.Status != nil && app.Sources != nil
}

func inputReady(app *AppSnapshot) bool {
	api, have := inputAPI(app)
	return api != No && have
}

// inputSaved is what is saved, as the page's fields (the web's savedInput: the HDMI and automatic by default).
func inputSaved(app *AppSnapshot) inputDraft {
	var in InputInfo
	if app.Status != nil {
		in = app.Status.Input
	}
	d := inputDraft{Source: in.Source, Mode: "auto", EDID: in.EDIDPreferred, Audio: orAuto(in.Audio)}
	if d.Source == "" {
		d.Source = "hdmi"
	}
	if d.Source == "usb" {
		d.Source = "usb|" + orAuto(in.Device)
		if f, m := orAuto(in.Format), orAuto(in.Mode); f != "auto" || m != "auto" {
			d.Mode = f + "|" + m
		}
	}
	if d.EDID == "" {
		d.EDID = EDIDModes[0]
	}
	return d
}

// inpShown is what the page shows: the draft, else what is saved.
func (u *UI) inpShown(app *AppSnapshot) inputDraft {
	if u.inp.draft != nil {
		return *u.inp.draft
	}
	return inputSaved(app)
}

// inpEdit starts editing (a copy of what is saved).
func (u *UI) inpEdit() *inputDraft {
	if u.inp.draft == nil {
		app := u.app().AppSnapshot()
		d := inputSaved(&app)
		u.inp.draft = &d
	}
	return u.inp.draft
}

// inpDirty: the draft would send something else than what is saved.
func (u *UI) inpDirty(app *AppSnapshot) bool {
	return u.inp.draft != nil && u.inp.draft.norm() != inputSaved(app).norm()
}

// inputUnapplied: something saved is not running yet (a restart pending, or another EDID in use): «Save and apply»
// applies it even without changes.
func inputUnapplied(app *AppSnapshot) bool {
	st := app.Status
	if st == nil {
		return false
	}
	in := st.Input
	return st.PendingRestart || (in.Source == "hdmi" && in.EDIDActive != "" && in.EDIDActive != in.EDIDPreferred)
}

// inputSync: once a save went through and the app's status shows it, the draft goes (until then the page keeps
// showing what was saved, not the status of before).
func (u *UI) inputSync(app *AppSnapshot) {
	st := &u.inp
	l := app.Last
	if st.draft == nil || st.sent == nil || l == nil || l.What != "input_save" || !l.At.After(st.savedAt) {
		return
	}
	switch {
	case !l.OK || *st.draft != *st.sent: // refused, or edited since: it stays a draft
		st.sent = nil
	case !u.inpDirty(app) || u.now().Sub(l.At) > noteFor:
		st.draft, st.sent = nil, nil
	}
}

// catalogSource is a choice's source in the catalogue (the web's catalogSource): the board's HDMI, the test pattern,
// a USB device, or for usb|auto the first USB the automatic rule takes. nil: not there.
func catalogSource(app *AppSnapshot, v string) *SourceChoice {
	if app.Sources == nil {
		return nil
	}
	kind, id := splitSource(v)
	for i := range app.Sources.Sources {
		s := &app.Sources.Sources[i]
		switch {
		case kind == "hdmi" && s.Kind == "hdmi-csi", kind == "test" && s.Kind == "test":
			return s
		case kind == "usb" && id == "auto" && s.Kind == "usb" && s.Auto != nil:
			return s
		case kind == "usb" && id != "auto" && s.Kind == "usb" && s.ID == id:
			return s
		}
	}
	return nil
}

// usbSpeed is a USB source's bus: USB 3 or USB 2.
func (u *UI) usbSpeed(s *SourceChoice) string {
	switch {
	case !s.USBKnown:
		return u.Tx.Input.USB
	case s.USB3:
		return u.Tx.Input.USB3
	}
	return u.Tx.Input.USB2
}

// usbName is a USB source as the choice says it: its name and its bus.
func (u *UI) usbName(s *SourceChoice) string {
	return s.Name + u.Tx.Sep + u.usbSpeed(s)
}

// sourceOpts are the source's choices (the web's: the board's HDMI if detected, each USB source, «the first USB
// there is», a saved USB that is not connected, the test pattern; the HDMI saved but not detected first).
func (u *UI) sourceOpts(app *AppSnapshot) []pickOpt {
	tx := u.Tx.Input
	saved := inputSaved(app)
	var out []pickOpt
	hdmi := false
	for i := range app.Sources.Sources {
		s := &app.Sources.Sources[i]
		switch s.Kind {
		case "hdmi-csi":
			hdmi = true
			out = append(out, pickOpt{ID: "hdmi", Title: u.sourceName("hdmi", s.Name)})
		case "usb":
			o := pickOpt{ID: "usb|" + s.ID, Title: u.usbName(s)}
			if s.AudioName != "" {
				o.Sub = sprintf(tx.HasAudioF, s.AudioName)
			}
			out = append(out, o)
		}
	}
	auto := pickOpt{ID: "usb|auto", Title: tx.USBAuto}
	if s := catalogSource(app, "usb|auto"); s != nil {
		auto.Sub = sprintf(tx.AutoNowF, u.usbName(s))
	}
	out = append(out, auto)
	if kind, dev := splitSource(saved.Source); kind == "usb" && dev != "auto" && catalogSource(app, saved.Source) == nil {
		out = append(out, pickOpt{ID: saved.Source, Title: tx.USBSaved, Sub: deviceTail(dev), Warn: true})
	}
	out = append(out, pickOpt{ID: "test", Title: tx.Test, Sub: tx.TestSub})
	if saved.Source == "hdmi" && !hdmi {
		out = append([]pickOpt{{ID: "hdmi", Title: tx.HDMIMissing}}, out...)
	}
	return out
}

// deviceTail is a device path's last part (by-id/usb-Elgato_…-video-index0): what tells it apart.
func deviceTail(dev string) string {
	if i := strings.LastIndexByte(dev, '/'); i >= 0 {
		return dev[i+1:]
	}
	return dev
}

// sourceLabel is the source field's value, and whether it is missing.
func (u *UI) sourceLabel(app *AppSnapshot, v string) (string, bool) {
	tx := u.Tx.Input
	kind, id := splitSource(v)
	s := catalogSource(app, v)
	switch {
	case kind == "hdmi" && s != nil:
		return u.sourceName("hdmi", s.Name), false
	case kind == "hdmi":
		return tx.HDMIMissing, true
	case kind == "test":
		return tx.Test, false
	case id == "auto":
		return tx.USBAuto, false
	case s != nil:
		return u.usbName(s), false
	}
	return tx.USBSaved, true
}

// formatLabel is a capture format as the web says it: uncompressed, MJPEG, or its name.
func (u *UI) formatLabel(f string) string {
	switch {
	case slices.Contains(rawFormats, f):
		return u.Tx.Input.Raw
	case f == "MJPG":
		return "MJPEG"
	}
	return f
}

// fpsLabel is a frame rate: whole, or with two decimals in the language's way (29,97).
func (u *UI) fpsLabel(f float64) string {
	if f == math.Trunc(f) {
		return strconv.Itoa(int(f))
	}
	return strings.Replace(strconv.FormatFloat(f, 'f', 2, 64), ".", u.Tx.Decimal, 1)
}

// choiceLabel is a capture option (the web's choiceLabel): 1920×1080 @ 30 · uncompressed → 1280×720.
func (u *UI) choiceLabel(c CaptureOption) string {
	tx := u.Tx.Input
	wh, rate, _ := strings.Cut(c.Mode, "@")
	w, h, _ := strings.Cut(wh, "x")
	fps := rate
	if c.FPS > 0 {
		fps = u.fpsLabel(c.FPS)
	}
	t := sprintf(tx.ChoiceF, w+"×"+h, fps, u.formatLabel(c.Format))
	if hh, err := strconv.Atoi(h); err == nil && c.OutHeight > 0 && c.OutHeight < hh {
		t = sprintf(tx.ResizedF, t, strconv.Itoa(c.OutWidth)+"×"+strconv.Itoa(c.OutHeight))
	}
	return t
}

// modeOpts are a USB source's capture modes: automatic (with what it takes), and each option of a device chosen by
// name (not «the first USB there is»); a saved one the catalogue does not list, as saved.
func (u *UI) modeOpts(app *AppSnapshot, d inputDraft) []pickOpt {
	tx := u.Tx.Input
	src := catalogSource(app, d.Source)
	_, dev := splitSource(d.Source)
	auto := pickOpt{ID: "auto", Title: tx.ModeAuto}
	if src != nil && src.Auto != nil {
		auto.Title = sprintf(tx.ModeAutoF, u.choiceLabel(*src.Auto))
	}
	out := []pickOpt{auto}
	if src != nil && dev != "auto" {
		for _, c := range src.Options {
			o := pickOpt{ID: c.Format + "|" + c.Mode, Title: u.choiceLabel(c)}
			if c.BusShare > 0.5 {
				o.Sub, o.Warn = sprintf(tx.BusShareF, int(math.Round(c.BusShare*100))), true
			}
			out = append(out, o)
		}
	}
	return withSaved(out, d.Mode, sprintf(tx.SavedF, strings.Replace(d.Mode, "|", u.Tx.Sep, 1)))
}

// edidOpts are the EDID modes (the recommended one says so).
func (u *UI) edidOpts(d inputDraft) []pickOpt {
	var out []pickOpt
	for _, m := range EDIDModes {
		t := m
		if m == EDIDModes[0] {
			t = u.Tx.Input.EDIDRecommended
		}
		out = append(out, pickOpt{ID: m, Title: t})
	}
	return withSaved(out, d.EDID, sprintf(u.Tx.Input.SavedF, d.EDID))
}

// audioOpts are the audio's choices: the source's, none, and each audio input but the network cameras' (they cannot
// be the startup one: only in the mixer); a saved one that is not connected, as such.
func (u *UI) audioOpts(app *AppSnapshot, d inputDraft) []pickOpt {
	tx := u.Tx.Input
	out := []pickOpt{{ID: "auto", Title: tx.AudioAuto}, {ID: "none", Title: tx.AudioNone}}
	for _, a := range app.Sources.Audio {
		if a.Kind == "network" {
			continue
		}
		o := pickOpt{ID: a.ID, Title: u.sourceName(a.ID, a.Name)}
		if o.Title == "" {
			o.Title = a.ID
		}
		if k := u.Tx.KindName[a.Kind]; k != "" && k != o.Title {
			o.Sub = k
		}
		out = append(out, o)
	}
	return withSaved(out, d.Audio, sprintf(tx.NotConnectedF, strings.TrimPrefix(d.Audio, "alsa:")))
}

// withSaved adds the current value as its own choice if the list does not have it.
func withSaved(opts []pickOpt, cur, title string) []pickOpt {
	for _, o := range opts {
		if o.ID == cur {
			return opts
		}
	}
	return append(opts, pickOpt{ID: cur, Title: title})
}

// optTitle is the title of the choice id ("" if none).
func optTitle(opts []pickOpt, id string) string {
	for _, o := range opts {
		if o.ID == id {
			return o.Title
		}
	}
	return ""
}

// inputMid is the field under the source: the capture mode of a USB device (mode), the automatic one of «the first
// USB there is» (modeauto: shown, not chosen), the HDMI's EDID (edid), or none (the test pattern).
func inputMid(d inputDraft) string {
	kind, dev := splitSource(d.Source)
	switch {
	case kind == "usb" && dev == "auto":
		return "modeauto"
	case kind == "usb":
		return "mode"
	case kind == "hdmi":
		return "edid"
	}
	return ""
}

// inputKeys are the page's buttons on view.
func (u *UI) inputKeys(app *AppSnapshot) []string {
	if !inputReady(app) {
		return nil
	}
	keys := []string{"s_inp_source", "s_inp_scan"}
	switch inputMid(u.inpShown(app)) {
	case "mode":
		keys = append(keys, "s_inp_mode", "s_inp_audio")
	case "modeauto":
		keys = append(keys, "s_inp_audio")
	case "edid":
		keys = append(keys, "s_inp_edid", "s_inp_audio")
	default:
		keys = append(keys, "s_inp_audio_up")
	}
	return append(keys, "s_inp_save", "s_inp_apply")
}

func (u *UI) inputTouch(what string) bool {
	app := u.app().AppSnapshot()
	u.inputSync(&app) // a save the app took and the status shows: the draft goes before it is edited again
	tx := u.Tx.Input
	switch what {
	case "s_inp_source":
		u.openPicker(tx.Source, u.inpShown(&app).Source, u.sourceOpts(&app), func(u *UI, id string) {
			d := u.inpEdit()
			if d.Source == id {
				return
			}
			d.Source, d.Mode = id, "auto"
			app := u.app().AppSnapshot()
			if saved := inputSaved(&app); saved.Source == id { // the saved one: its mode (the web's)
				d.Mode = saved.Mode
			}
		})
	case "s_inp_mode":
		d := u.inpShown(&app)
		u.openPicker(tx.CaptureMode, d.Mode, u.modeOpts(&app, d), func(u *UI, id string) { u.inpEdit().Mode = id })
	case "s_inp_edid":
		d := u.inpShown(&app)
		u.openPicker(tx.EDID, d.EDID, u.edidOpts(d), func(u *UI, id string) { u.inpEdit().EDID = id })
	case "s_inp_audio", "s_inp_audio_up":
		d := u.inpShown(&app)
		u.openPicker(tx.Audio, d.Audio, u.audioOpts(&app, d), func(u *UI, id string) { u.inpEdit().Audio = id })
	case "s_inp_scan":
		if app.Pending != "sources_scan" {
			u.inp.scanAt = u.now().Add(-time.Millisecond) // results from now on are this scan's
			u.appBusy(u.app().Do(AppAction{What: "sources_scan"}))
		}
	case "s_inp_save":
		u.inputSave(&app, false)
	case "s_inp_apply":
		u.inputSave(&app, true)
	default:
		return false
	}
	return true
}

// inputSave sends the fields (PUT /input). «Save» only with changes; «Save and apply» also applies what is saved
// and not running yet, and while streaming (a few seconds' cut) needs a second tap.
func (u *UI) inputSave(app *AppSnapshot, apply bool) {
	if !inputReady(app) || app.Pending == "input_save" {
		return
	}
	if !u.inpDirty(app) && !(apply && inputUnapplied(app)) {
		return
	}
	if apply && u.streaming() && !u.confirmTap("input_apply") {
		return
	}
	d := u.inpShown(app)
	u.inp.savedAt, u.inp.apply = u.now().Add(-time.Millisecond), apply // results from now on are this save's
	if !u.app().Do(AppAction{What: "input_save", Body: d.body(apply)}) {
		u.appBusy(false)
		return
	}
	if u.inp.draft != nil {
		sent := *u.inp.draft
		u.inp.sent = &sent
	}
}

// inputResult is this page's last save, if it was answered after it was asked (and after the page opened).
func (u *UI) inputResult(app *AppSnapshot) *AppResult {
	l := app.Last
	if l == nil || l.What != "input_save" || !l.At.After(u.inp.savedAt) || l.At.Before(u.pageSince) {
		return nil
	}
	return l
}

// inputFields maps the app's field errors to the page's fields.
var inputFields = map[string]string{"source": "source", "device": "source", "videoMode": "mode",
	"videoFormat": "mode", "edidPreferred": "edid", "audio": "audio"}

// inputErrors are the last save's field errors: by the page's field, and the words of each (those of the
// destination too: the whole configuration is saved at once, so a wrong destination stops the input from saving).
func (u *UI) inputErrors(app *AppSnapshot) (fields map[string]bool, words []string) {
	l := u.inputResult(app)
	if l == nil || l.OK || len(l.Fields) == 0 {
		return nil, nil
	}
	tx := u.Tx.Input
	fields = map[string]bool{}
	keys := make([]string, 0, len(l.Fields))
	for k := range l.Fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		f := l.Fields[k]
		msg := f.Message
		if msg == "" {
			msg = sprintf(u.Tx.AppMessageF, f.Code)
		}
		switch field, ok := inputFields[k]; {
		case ok:
			fields[field] = true
			msg = map[string]string{"source": tx.Source, "mode": tx.CaptureMode, "edid": tx.EDID,
				"audio": tx.Audio}[field] + u.Tx.Sep + msg
		case k == "url":
			msg = tx.NeedDest
		default:
			msg = sprintf(tx.CheckDestF, msg)
		}
		if !slices.Contains(words, msg) {
			words = append(words, msg)
		}
	}
	return fields, words
}

// inputNote is the line next to the buttons: saving, the second tap, the app's answer, unsaved, a scan.
func (u *UI) inputNote(app *AppSnapshot) (string, bool) {
	tx, itx := u.Tx, u.Tx.Input
	l := u.inputResult(app)
	fresh := l != nil && u.now().Sub(l.At) < 8*time.Second
	sentShown := u.inp.draft == nil || u.inp.sent != nil
	switch {
	case app.Pending == "input_save":
		return itx.Saving, false
	case u.confirming("input_apply"):
		return tx.ApplyCuts, true
	case fresh && !l.OK:
		return u.resultText(l), true
	case fresh && sentShown && l.Restarted != nil && *l.Restarted:
		return tx.SavedRestarting, false
	case fresh && sentShown && u.inp.apply:
		return tx.SavedStopped, false
	case fresh && sentShown:
		return tx.SavedLater, false
	case u.inpDirty(app):
		return tx.Unsaved, false
	}
	if s := app.Last; s != nil && s.What == "sources_scan" && s.At.After(u.inp.scanAt) && !s.At.Before(u.pageSince) &&
		u.now().Sub(s.At) < 4*time.Second {
		if !s.OK {
			return u.resultText(s), true
		}
		return itx.Scanned, false
	}
	return "", false
}

func (u *UI) drawInputPage(d *dc, app *AppSnapshot) {
	api, have := inputAPI(app)
	if !u.devicePageReady(d, api, have) {
		return
	}
	u.inputSync(app)
	tx, s := u.Tx.Input, d.s
	lay := u.Lay
	cur, saved := u.inpShown(app), inputSaved(app)
	errs, words := u.inputErrors(app)
	// The source, and «Scan»
	label, missing := u.sourceLabel(app, cur.Source)
	u.field(d, "s_inp_source", tx.Source, label, fieldOpt{chevron: true, warn: missing || errs["source"],
		changed: cur.Source != saved.Source})
	d.button("s_inp_scan", btn{label: tx.Scan, dim: app.Pending == "sources_scan"})
	// The capture mode or the EDID, and the audio (up a row without either)
	audioKey, last := "s_inp_audio", "s_inp_audio"
	switch mid := inputMid(cur); mid {
	case "mode", "modeauto":
		u.field(d, "s_inp_mode", tx.CaptureMode, optTitle(u.modeOpts(app, cur), cur.Mode), fieldOpt{chevron: true,
			dim: mid == "modeauto", warn: errs["mode"], changed: mid == "mode" && cur.Mode != saved.Mode})
	case "edid":
		u.field(d, "s_inp_edid", tx.EDID, optTitle(u.edidOpts(cur), cur.EDID), fieldOpt{chevron: true,
			warn: errs["edid"], changed: cur.EDID != saved.EDID})
	default:
		audioKey, last = "s_inp_audio_up", "s_inp_audio_up"
	}
	u.field(d, audioKey, tx.Audio, optTitle(u.audioOpts(app, cur), cur.Audio), fieldOpt{chevron: true,
		warn: errs["audio"], changed: cur.Audio != saved.Audio})
	// Under the fields, what matters most first: the app's errors, the source's warnings, what runs now, the
	// source's facts, then the hints (as many as fit, whole)
	type para struct {
		text string
		warn bool
	}
	var ps []para
	for _, w := range words {
		ps = append(ps, para{w, true})
	}
	src := catalogSource(app, cur.Source)
	if kind, _ := splitSource(cur.Source); kind == "usb" && src != nil {
		for _, w := range src.Warnings {
			if t, ok := tx.Warn[w]; ok && slices.Contains(inputWarnings, w) {
				ps = append(ps, para{t, true})
			}
		}
	}
	if t, warn := u.inputActive(app); t != "" {
		ps = append(ps, para{t, warn})
	}
	if t := u.inputInfo(cur, src); t != "" {
		ps = append(ps, para{t, false})
	}
	switch inputMid(cur) {
	case "mode":
		ps = append(ps, para{tx.CaptureHint, false})
	case "edid":
		ps = append(ps, para{tx.EDIDHint, false})
	case "":
		ps = append(ps, para{tx.TestSub, false})
	}
	ps = append(ps, para{tx.MixerHint, false})
	pg := lay.SettingsPage
	lr := lay.Buttons[last]
	sv := lay.Buttons["s_inp_save"]
	y := float64(lr.Y+lr.H) + 26*s
	bottom := float64(sv.Y) - 4*s
	w := float64(pg.W) - 8*s
	for _, p := range ps {
		style := Sans
		if p.warn {
			style = SansBold
		}
		lines := d.wrap(p.text, 15*s, style, w)
		if y+float64(len(lines)-1)*19*s > bottom {
			continue // a whole paragraph or none
		}
		col := cQuiet
		if p.warn {
			col = cWarn
		}
		for _, line := range lines {
			d.text(float64(pg.X)+4*s, y, line, 15*s, textOpt{col: col, bold: p.warn, width: w})
			y += 19 * s
		}
		y += 7 * s
	}
	// «Save», «Save and apply» and the note at their left
	dirty := u.inpDirty(app)
	busy := app.Pending == "input_save"
	d.button("s_inp_save", btn{label: u.Tx.Save, active: dirty, dim: !dirty || busy})
	conf := u.confirming("input_apply")
	ab := btn{label: u.Tx.SaveApply, active: dirty || conf, dim: (!dirty && !inputUnapplied(app)) || busy}
	if conf {
		ab.col = colp(cWarn)
	}
	d.button("s_inp_apply", ab)
	if t, warn := u.inputNote(app); t != "" {
		u.sideNote(d, Rect{pg.X, sv.Y, sv.X - pg.X - lay.M, sv.H}, t, warn)
	}
}

// sideNote is a page's note in a box (at the left of its buttons): wrapped, centred on its height.
func (u *UI) sideNote(d *dc, r Rect, t string, warn bool) {
	s := d.s
	col := cQuiet
	if warn {
		col = cWarn
	}
	w := float64(r.W) - 4*s
	style := Sans
	if warn {
		style = SansBold
	}
	lines := d.wrap(t, 15*s, style, w)
	if len(lines) > 3 {
		lines = lines[:3]
	}
	lh := 17 * s
	y := float64(r.Y) + float64(r.H)/2 - float64(len(lines)-1)*lh/2 + 5*s
	for _, line := range lines {
		d.text(float64(r.X)+4*s, y, line, 15*s, textOpt{col: col, bold: warn, width: w})
		y += lh
	}
}

// inputActive is what runs now (the web's renderInputStatus): a USB source in use, the stream waiting for its USB
// device, or another EDID in use than the saved one.
func (u *UI) inputActive(app *AppSnapshot) (string, bool) {
	tx := u.Tx.Input
	in := app.Status.Input
	switch {
	case in.ActiveState == "ready" && in.ActiveFormat != "" && in.ActiveMode != "" &&
		(in.ActiveSource == "usb" || in.ActiveSource == ""): // a USB capture (an older app does not say: HDMI has neither)
		name := in.ActiveName
		if name == "" {
			name = "USB"
		}
		wh, _, _ := strings.Cut(in.ActiveMode, "@")
		return sprintf(tx.InUseF, name, u.formatLabel(in.ActiveFormat), strings.Replace(wh, "x", "×", 1)), false
	case in.ActiveState == "waiting_device":
		return tx.WaitingUSB, true
	case in.Source == "hdmi" && in.EDIDActive != "" && in.EDIDActive != in.EDIDPreferred:
		return sprintf(tx.EDIDActiveF, in.EDIDActive), false
	}
	return "", false
}

// inputInfo is the chosen USB source's facts (the web's source-info): with «the first USB there is», which one
// and its bus (a device chosen by name says its bus in its field); its audio; and if the capture it takes uses more
// than half the bus.
func (u *UI) inputInfo(cur inputDraft, src *SourceChoice) string {
	kind, dev := splitSource(cur.Source)
	if kind != "usb" || src == nil {
		return ""
	}
	tx := u.Tx.Input
	var parts []string
	if dev == "auto" {
		parts = append(parts, sprintf(tx.AutoNowF, u.usbName(src)))
	}
	if src.AudioName != "" {
		parts = append(parts, sprintf(tx.HasAudioF, src.AudioName))
	}
	if c := chosenOption(src, cur); c != nil && c.BusShare > 0.5 {
		parts = append(parts, sprintf(tx.BusShareF, int(math.Round(c.BusShare*100))))
	}
	return strings.Join(parts, u.Tx.Sep)
}

// chosenOption is the capture option the draft takes: the one chosen, else the automatic one (nil: not known).
func chosenOption(src *SourceChoice, cur inputDraft) *CaptureOption {
	for i, c := range src.Options {
		if cur.Mode == c.Format+"|"+c.Mode {
			return &src.Options[i]
		}
	}
	return src.Auto
}

// InputEdit (ui-render) edits the Input page's draft: what is not "" changes.
func (p Preview) InputEdit(source, mode, edid, audio string) {
	d := p.u.inpEdit()
	if source != "" {
		d.Source = source
	}
	if mode != "" {
		d.Mode = mode
	}
	if edid != "" {
		d.EDID = edid
	}
	if audio != "" {
		d.Audio = audio
	}
}

// InputPick (ui-render) opens one of the Input page's choices: s_inp_source | s_inp_mode | s_inp_edid | s_inp_audio.
func (p Preview) InputPick(key string) { p.u.inputTouch(key) }

// InputConfirmApply (ui-render) shows «Save and apply» waiting for its second tap (streaming).
func (p Preview) InputConfirmApply() {
	p.u.confirm = &confirm{what: "input_apply", until: p.u.now().Add(ConfirmFor)}
}
