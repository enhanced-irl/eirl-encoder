package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// The local recording on the screen (RECORDING.md §6, MIXER.md §23): REC, the header's line, Go live with it, and the
// Recording page.

func i64p(v int64) *int64 { return &v }

// recReady is the engine's record object: REC_MODE setting, an exFAT disk ready with 3 h 20 min left at High.
func recReady(setting string) *RecordStatus {
	return &RecordStatus{Mode: "off", State: "off", Setting: setting, Quality: "high", By: "start",
		Disk: RecordDisk{State: "ready", FS: "exfat", Label: "EIRL-REC", SizeBytes: i64p(62008590336),
			FreeBytes: i64p(41_200_000_000), MinutesLeft: ip(200)}}
}

// recording turns it on: 1:12:34 recorded.
func recording(r *RecordStatus) *RecordStatus {
	r.Mode, r.State, r.By, r.Seq, r.ElapsedS = "on", "on", "screen", 3, ip(4354)
	return r
}

// setRecord gives the harness's engine state a record object (and the output on or off).
func (h *harness) setRecord(r *RecordStatus, live bool) {
	st := *h.u.st
	st.Record = r
	st.Output = OutputStatus{Mode: "off", State: "off"}
	if live {
		st.Output = OutputStatus{Mode: "on", State: "on"}
	}
	h.u.SetStatus(&st)
}

func TestRecButtonState(t *testing.T) {
	none, full, ejected, checking, bad := recReady("manual"), recReady("manual"), recReady("manual"),
		recReady("manual"), recReady("manual")
	none.Disk = RecordDisk{State: "none"}
	full.Disk.MinutesLeft = ip(0)
	ejected.Disk.State, checking.Disk.State, bad.Disk.State = "ejected", "checking", "unsupported"
	offRecording := recording(recReady("off"))
	for _, c := range []struct {
		name string
		r    *RecordStatus
		want RecButton
	}{
		{"no recorder", nil, RecButton{}},
		{"off", recReady("off"), RecButton{}},
		{"off but still recording: it can be stopped", offRecording, RecButton{Shown: true, On: true, Ready: true}},
		{"manual, ready", recReady("manual"), RecButton{Shown: true, Ready: true}},
		{"with_output, ready", recReady("with_output"), RecButton{Shown: true, Ready: true}},
		{"recording", recording(recReady("manual")), RecButton{Shown: true, On: true, Ready: true}},
		{"no disk", none, RecButton{Shown: true, Why: "none"}},
		{"full", full, RecButton{Shown: true, Why: "full"}},
		{"ejected", ejected, RecButton{Shown: true, Why: "ejected"}},
		{"checking", checking, RecButton{Shown: true, Why: "checking"}},
		{"unusable", bad, RecButton{Shown: true, Why: "bad"}},
	} {
		if got := RecButtonState(c.r); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestEmitRecText(t *testing.T) {
	none, full, ejected, checking := recReady("with_output"), recReady("with_output"), recReady("with_output"),
		recReady("with_output")
	none.Disk = RecordDisk{State: "none"}
	full.Disk.MinutesLeft = ip(0)
	ejected.Disk.State, checking.Disk.State = "ejected", "checking"
	for _, c := range []struct {
		name  string
		r     *RecordStatus
		rec   bool
		noRec string
	}{
		{"no recorder", nil, false, ""},
		{"manual", recReady("manual"), false, ""},
		{"with_output, ready: Go live + record", recReady("with_output"), true, ""},
		{"already recording (REC)", recording(recReady("with_output")), false, ""},
		{"no disk: it streams, not records", none, false, "none"},
		{"ejected", ejected, false, "none"},
		{"full", full, false, "full"},
		{"checking: nothing yet", checking, false, ""},
	} {
		if rec, noRec := EmitRecText(c.r); rec != c.rec || noRec != c.noRec {
			t.Errorf("%s: %v %q, want %v %q", c.name, rec, noRec, c.rec, c.noRec)
		}
	}
}

// REC is held like Go live: a short press says so; held, the engine's record command by=screen; again, it stops.
func TestRecNeedsHold(t *testing.T) {
	h := newHarness(t)
	h.setRecord(recReady("manual"), false)
	h.lay.RecRow(h.u.recShown())
	if _, ok := h.lay.Buttons["rec"]; !ok || h.lay.Buttons["emit"].W >= h.lay.emitRow.W {
		t.Fatal("no REC at Go live's right")
	}
	h.press("rec", 0.3)
	if len(h.cmds) != 0 || !strings.Contains(h.u.hint, "Mantén") {
		t.Fatalf("a short press records: %v %q", h.cmds, h.u.hint)
	}
	h.press("rec", Hold.Seconds()+0.1)
	h.setRecord(recording(recReady("manual")), false)
	h.press("rec", Hold.Seconds()+0.1)
	want := []string{"map[by:screen cmd:record on:true]", "map[by:screen cmd:record on:false]"}
	if got := h.cmdNames(); !eqStrings(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
	// Locked: only the padlock answers
	h.cmds = nil
	h.setLocked(true)
	h.press("rec", Hold.Seconds()+0.1)
	if len(h.cmds) != 0 {
		t.Fatalf("REC while locked: %v", h.cmds)
	}
}

// Without a disk REC cannot start: holding it sends nothing, a tap says why.
func TestRecDisabled(t *testing.T) {
	h := newHarness(t)
	r := recReady("manual")
	r.Disk = RecordDisk{State: "none"}
	h.setRecord(r, false)
	h.lay.RecRow(true)
	h.press("rec", Hold.Seconds()+0.1)
	if len(h.cmds) != 0 || h.u.hint != Spanish.Rec.WhyTap["none"] {
		t.Fatalf("%v %q", h.cmds, h.u.hint)
	}
}

// REC_MODE=off: no REC, Go live takes the whole row again.
func TestRecHiddenWhenOff(t *testing.T) {
	h := newHarness(t)
	h.setRecord(recReady("manual"), false)
	h.lay.RecRow(h.u.recShown())
	h.setRecord(recReady("off"), false)
	x, y := h.at("emit")
	h.u.OnTouch("down", x, y, 0) // the layout follows the state before the hit
	h.u.OnTouch("up", x, y, 0)
	if _, ok := h.lay.Buttons["rec"]; ok || h.lay.Buttons["emit"] != h.lay.emitRow {
		t.Fatalf("REC with the recording off: %+v", h.lay.Buttons["emit"])
	}
}

// The engine's refusals, in words.
func TestRecRefused(t *testing.T) {
	h := newHarness(t)
	for code, want := range map[string]string{"disk_full": "No se puede: el disco está lleno",
		"disabled": "No se puede: la grabación está desactivada", "no_disk": "No se puede: no hay un disco listo",
		"unsupported": "No se puede: el motor no lo admite"} {
		h.u.CommandDone(Command{Msg: map[string]any{"cmd": "record", "on": true}, Origin: originRec}, false, code)
		if h.u.hint != want {
			t.Errorf("%s: %q", code, h.u.hint)
		}
	}
}

// The header's line: the time recorded (counted on between states, never more than 10 s past the engine's) and the
// time left; why a recording stopped, for RecNoticeFor; still asked to record, until it records again.
func TestRecHeadLine(t *testing.T) {
	h := newHarness(t)
	if l := h.u.recHeadLine(h.now()); l.main != "" {
		t.Fatalf("no recorder: %+v", l)
	}
	h.setRecord(recording(recReady("manual")), true)
	if l := h.u.recHeadLine(h.now()); l.main != "REC 01:12:34" || l.tail != "3 h 20 min libres" || l.tailColor != cQuiet {
		t.Fatalf("recording: %+v", l)
	}
	h.advance(3 * time.Second)
	if l := h.u.recHeadLine(h.now()); l.main != "REC 01:12:37" {
		t.Fatalf("counted on: %+v", l)
	}
	h.advance(time.Minute)
	if l := h.u.recHeadLine(h.now()); l.main != "REC 01:12:44" {
		t.Fatalf("past the engine's figure: %+v", l)
	}
	r := recording(recReady("manual"))
	r.ElapsedS, r.Disk.MinutesLeft = ip(4400), ip(8)
	h.setRecord(r, true)
	if l := h.u.recHeadLine(h.now()); l.main != "REC 01:13:20" || l.tail != "8 min libres" || l.tailColor != cWarn {
		t.Fatalf("8 min left: %+v", l)
	}
	// A retry in a new file (the figure goes back): its own clock
	r = recording(recReady("manual"))
	r.ElapsedS = ip(2)
	h.setRecord(r, true)
	if l := h.u.recHeadLine(h.now()); l.main != "REC 00:00:02" {
		t.Fatalf("a new file: %+v", l)
	}
	r.Reason = "disk_slow"
	h.setRecord(r, true)
	if l := h.u.recHeadLine(h.now()); l.tail != "el disco no da abasto" || l.tailColor != cWarn {
		t.Fatalf("slow: %+v", l)
	}
	// After a power cut: said for RecNoticeFor
	r = recReady("manual")
	r.Reason = "power_lost"
	h.setRecord(r, false)
	if l := h.u.recHeadLine(h.now()); l.main != "La grabación se paró por un corte de corriente" || l.col != cWarn {
		t.Fatalf("power lost: %+v", l)
	}
	h.advance(RecNoticeFor)
	h.setRecord(r, false) // the same state again: the notice does not start over
	if l := h.u.recHeadLine(h.now()); l.main != "" {
		t.Fatalf("still said after %s: %+v", RecNoticeFor, l)
	}
	// Still asked to record, the disk pulled out: red, for as long as it lasts
	r = recReady("manual")
	r.Mode, r.State, r.Reason, r.Seq = "on", "error", "disk_removed", 4
	r.Disk = RecordDisk{State: "none"}
	h.setRecord(r, true)
	h.advance(2 * RecNoticeFor)
	if l := h.u.recHeadLine(h.now()); l.main != "Se desconectó el disco mientras grababa" || l.col != cBad {
		t.Fatalf("disk removed: %+v", l)
	}
	r.State, r.Reason = "starting", ""
	h.setRecord(r, true)
	if l := h.u.recHeadLine(h.now()); l.main != "REC · empezando…" {
		t.Fatalf("starting: %+v", l)
	}
}

// A disk plugged in while live: the warning, for PlugWarnFor; not when one was already there or when not live.
func TestRecPlugWhileLive(t *testing.T) {
	h := newHarness(t)
	none := recReady("manual")
	none.Disk = RecordDisk{State: "none"}
	h.setRecord(recReady("manual"), true) // there at start: nothing to say
	if !h.u.rec.plugWarnUntil.IsZero() {
		t.Fatal("a warning for a disk already there")
	}
	h.setRecord(none, false)
	h.setRecord(recReady("manual"), false)
	if !h.u.rec.plugWarnUntil.IsZero() {
		t.Fatal("a warning while not live")
	}
	h.setRecord(none, true)
	checking := recReady("manual")
	checking.Disk = RecordDisk{State: "checking"}
	h.setRecord(checking, true)
	if !h.u.rec.plugWarnUntil.Equal(h.now().Add(PlugWarnFor)) {
		t.Fatalf("no warning: %v", h.u.rec.plugWarnUntil)
	}
}

func TestRecSpansAndSizes(t *testing.T) {
	for m, want := range map[int]string{0: "0 min", 45: "45 min", 60: "1 h", 200: "3 h 20 min", 720: "12 h", -3: "0 min"} {
		if got := recSpan(m, &Spanish.Rec); got != want {
			t.Errorf("%d: %q", m, got)
		}
	}
	for b, want := range map[int64]string{0: "", 740_000_000: "740 MB", 1_500_000_000: "1,5 GB", 62008590336: "62 GB",
		1_000_204_886_016: "1,0 TB"} {
		if got := diskSize(b, Spanish); got != want {
			t.Errorf("%d: %q", b, got)
		}
	}
	if got := diskSize(31_000_000_000, French); got != "31 Go" {
		t.Errorf("fr: %q", got)
	}
	for in, want := range map[string]string{"Alta": "alta", "High": "high", "NTFS": "NTFS", "": ""} {
		if got := inSentence(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// --- the Recording page ---

func fakeRecording() *RecordingInfo {
	return &RecordingInfo{Mode: "manual", Quality: "high",
		Qualities: []RecQuality{{ID: "standard", Codec: "h264", VideoKbps: 15000, AudioKbps: 192},
			{ID: "high", Codec: "h264", VideoKbps: 25000, AudioKbps: 256},
			{ID: "max", Codec: "h264", VideoKbps: 40000, AudioKbps: 256},
			{ID: "efficient", Codec: "h265", VideoKbps: 20000, AudioKbps: 256}},
		Storage: StorageInfo{State: "ready", Seq: 7, Device: "sda", Model: "DataTraveler 3.0", FS: "exfat",
			Label: "EIRL-REC", SizeBytes: 62008590336, WriteMBps: 31.2}}
}

// recHarness: Settings open on Stream › Recording, the app's view and the engine's state (manual, a disk ready), drawn.
func recHarness(t *testing.T) (*harness, *fakeApp) {
	t.Helper()
	h, app := pagesHarness(t, "recording")
	app.snap.Recording, app.snap.RecordingAPI = fakeRecording(), Yes
	h.setRecord(recReady("manual"), false)
	h.u.Fonts, h.u.Levels = fonts(t), &levels{pgm: -9, in: -12}
	h.u.LinkInfo = func() (string, int) { return "srt", 0 }
	h.u.SetLayout(h.lay)
	h.redraw() // the pickers' rows come from what was drawn
	return h, app
}

func TestRecordingInStream(t *testing.T) {
	if len(SettingsGroups) != 7 {
		t.Fatalf("%d groups: the list takes seven", len(SettingsGroups))
	}
	g := groupOf("recording")
	if g == nil || g.ID != "stream" || !slices.Equal(g.Pages, []string{"dest", "uplinks", "pause", "recording"}) {
		t.Fatalf("group %+v", g)
	}
	h, app := recHarness(t)
	if !app.watch().Recording {
		t.Fatal("the page does not ask for the recording")
	}
	h.press("s_tab_pause", 0.1)
	h.u.Tick()
	if app.watch().Recording {
		t.Fatal("still asked for on another page")
	}
}

func TestRecordingPageKeys(t *testing.T) {
	h, app := recHarness(t)
	keys := h.u.recKeys(&app.snap)
	if !slices.Equal(keys, []string{"s_rec_mode", "s_rec_quality", "s_rec_eject", "s_rec_format"}) {
		t.Fatalf("ready: %v", keys)
	}
	for _, c := range []struct {
		name string
		disk StorageInfo
		want []string
	}{
		{"none", StorageInfo{State: "none", Seq: 8}, []string{"s_rec_mode", "s_rec_quality"}},
		{"checking", StorageInfo{State: "checking", Seq: 8}, []string{"s_rec_mode", "s_rec_quality"}},
		{"ntfs", StorageInfo{State: "unsupported", Reason: "fs_ntfs", FS: "ntfs", Seq: 8, SizeBytes: 1e12},
			[]string{"s_rec_mode", "s_rec_quality", "s_rec_format"}},
		{"a tiny disk is not worth formatting", StorageInfo{State: "unsupported", Reason: "too_small", Seq: 8,
			SizeBytes: 500e6}, []string{"s_rec_mode", "s_rec_quality"}},
		{"write-protected", StorageInfo{State: "error", Reason: "readonly", Seq: 8, SizeBytes: 62e9},
			[]string{"s_rec_mode", "s_rec_quality"}},
		{"ejected", StorageInfo{State: "ejected", Seq: 8}, []string{"s_rec_mode", "s_rec_quality", "s_rec_format"}},
	} {
		app.snap.Recording.Storage = c.disk
		r := recReady("manual")
		r.Disk = RecordDisk{State: c.disk.State, Reason: c.disk.Reason}
		h.setRecord(r, false)
		if got := h.u.recKeys(&app.snap); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	app.snap.Recording, app.snap.RecordingAPI = nil, No
	if got := h.u.recKeys(&app.snap); got != nil {
		t.Errorf("an older app: %v", got)
	}
}

// The mode and the quality: their pickers (each mode with its line), PUT /recording; the quality waits while recording.
func TestRecordingPageSettings(t *testing.T) {
	h, app := recHarness(t)
	h.press("s_rec_mode", 0.1)
	if h.u.settingsView != "pick" || len(h.u.chooser.opts) != 3 || h.u.chooser.opts[2].Sub == "" {
		t.Fatalf("the mode's picker: %q %+v", h.u.settingsView, h.u.chooser)
	}
	h.redraw()
	h.press("s_pick_2", 0.1) // Con el directo
	h.press("s_rec_quality", 0.1)
	h.redraw()
	if h.u.chooser == nil || h.u.chooser.opts[3].Sub != "H.265 20 Mbps · 9,1 GB/h" {
		t.Fatalf("the quality's picker: %+v", h.u.chooser)
	}
	h.press("s_pick_1", 0.1) // the same one: nothing is sent
	h.press("s_rec_quality", 0.1)
	h.redraw()
	h.press("s_pick_2", 0.1)
	want := []string{"rec_settings map[mode:with_output]", "rec_settings map[quality:max]"}
	if got := didNames(app); !eqStrings(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
	// While recording the quality does not open (the page says why)
	h.setRecord(recording(recReady("manual")), false)
	h.press("s_rec_quality", 0.1)
	if h.u.settingsView != "main" || !strings.Contains(h.u.pnote.text, "mientras grabas") {
		t.Fatalf("quality while recording: %q %q", h.u.settingsView, h.u.pnote.text)
	}
	// Off while recording is asked first; Cancel sends nothing, the answer turns it off
	h.press("s_rec_mode", 0.1)
	h.redraw()
	h.press("s_pick_0", 0.1)
	if h.u.rp.ask != "off" || len(app.did) != 2 {
		t.Fatalf("off while recording: %+v %v", h.u.rp, didNames(app))
	}
	h.redraw()
	h.press("s_rec_no", 0.1)
	if h.u.rp.ask != "" || len(app.did) != 2 {
		t.Fatalf("cancel: %+v %v", h.u.rp, didNames(app))
	}
	h.press("s_rec_mode", 0.1)
	h.redraw()
	h.press("s_pick_0", 0.1)
	h.redraw()
	h.press("s_rec_yes", 0.1)
	if got := didNames(app); got[len(got)-1] != "rec_settings map[mode:off]" {
		t.Fatalf("%v", got)
	}
}

// Eject, and Format behind its question (it names the disk; the seq it names goes with it); the question goes by
// itself when the disk changes or a recording starts; neither while recording.
func TestRecordingPageDisk(t *testing.T) {
	h, app := recHarness(t)
	h.press("s_rec_eject", 0.1)
	h.press("s_rec_format", 0.1)
	if h.u.rp.ask != "format" || h.u.rp.seq != 7 || h.u.rp.who != "«EIRL-REC» (DataTraveler 3.0 · 62 GB)" {
		t.Fatalf("the question: %+v", h.u.rp)
	}
	h.redraw()
	if keys := h.u.settingsKeys(&h.net.snap); !slices.Contains(keys, "s_rec_yes") || slices.Contains(keys, "s_rec_mode") {
		t.Fatalf("the question's keys: %v", keys)
	}
	h.press("s_rec_yes", 0.1)
	want := []string{"rec_eject map[]", "rec_format map[seq:7]"}
	if got := didNames(app); !eqStrings(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
	// Another disk meanwhile: the question goes
	h.press("s_rec_format", 0.1)
	app.snap.Recording.Storage.Seq = 8
	h.redraw()
	if h.u.rp.ask != "" {
		t.Fatal("the question stayed for another disk")
	}
	// While recording: neither (the page says why)
	h.setRecord(recording(recReady("manual")), false)
	h.press("s_rec_eject", 0.1)
	h.press("s_rec_format", 0.1)
	if len(app.did) != 2 || h.u.rp.ask != "" || !strings.Contains(h.u.pnote.text, "Deja de grabar") {
		t.Fatalf("while recording: %v %+v %q", didNames(app), h.u.rp, h.u.pnote.text)
	}
	// Leaving the page drops a question
	h.setRecord(recReady("manual"), false)
	h.press("s_rec_format", 0.1)
	h.press("s_tab_dest", 0.1)
	if h.u.rp.ask != "" {
		t.Fatal("the question outlived its page")
	}
}

// didNames: the actions the page asked of the app, with their bodies.
func didNames(app *fakeApp) []string {
	var out []string
	for _, a := range app.did {
		out = append(out, fmt.Sprintf("%s %v", a.What, a.Body))
	}
	return out
}

func TestRecWarnings(t *testing.T) {
	h, _ := recHarness(t)
	w := func(d recDisk) []string { return h.u.recWarnings(&d) }
	tx := &Spanish.Rec
	ready := func(s StorageInfo) recDisk { return recDisk{StorageInfo: s, MinutesLeft: ip(200)} }
	got := w(ready(StorageInfo{State: "ready", FS: "fat32", Dirty: true, AnotherDisk: "sdb"}))
	if len(got) != 3 || !strings.HasPrefix(got[0], "FAT32: ") || got[1] != tx.Warns["dirty"] || got[2] != tx.Warns["another"] {
		t.Errorf("fat32: %q", got)
	}
	low := ready(StorageInfo{State: "ready", FS: "exfat"})
	low.MinutesLeft = ip(9)
	if got := w(low); !slices.Equal(got, []string{tx.Warns["low"]}) {
		t.Errorf("low: %q", got)
	}
	for _, c := range []struct {
		d    StorageInfo
		want string
	}{
		{StorageInfo{State: "unsupported", Reason: "fs_ntfs"}, "ntfs"},
		{StorageInfo{State: "unsupported", Reason: "fs_unknown"}, "unknownFs"},
		{StorageInfo{State: "unsupported", Reason: "too_small", SizeBytes: 500e6}, "tooSmall"},
		{StorageInfo{State: "unsupported", Reason: "too_small", SizeBytes: 8e9}, "tooSmallPart"},
		{StorageInfo{State: "unformatted"}, "unformatted"},
		{StorageInfo{State: "error", Reason: "readonly"}, "readonly"},
		{StorageInfo{State: "error", Reason: "check_failed"}, "checkFailed"},
		{StorageInfo{State: "error", Reason: "mount_failed"}, "mountFailed"},
		{StorageInfo{State: "ready", FS: "exfat", ReadOnly: true}, "readonly"},
	} {
		if got := w(recDisk{StorageInfo: c.d}); !slices.Equal(got, []string{tx.Warns[c.want]}) {
			t.Errorf("%+v: %q", c.d, got)
		}
	}
}

// Every state of the recording, on the main screen and on its page, in every language: no text of the tables cut.
func TestRecRenders(t *testing.T) {
	main := func(r func() *RecordStatus, live bool, mod func(h *harness)) func(h *harness) {
		return func(h *harness) {
			h.setRecord(r(), live)
			if mod != nil {
				mod(h)
			}
		}
	}
	disk := func(setting string, d RecordDisk) func() *RecordStatus {
		return func() *RecordStatus { r := recReady(setting); r.Disk = d; return r }
	}
	stopped := func(reason, mode string) func() *RecordStatus {
		return func() *RecordStatus { r := recReady("manual"); r.Reason, r.Mode = reason, mode; return r }
	}
	views := []renderView{
		{"rec ready", main(func() *RecordStatus { return recReady("manual") }, false, nil)},
		{"rec held", main(func() *RecordStatus { return recReady("manual") }, false,
			func(h *harness) { h.u.Preview().Holding("rec", 500*time.Millisecond) })},
		{"recording live", main(func() *RecordStatus { return recording(recReady("manual")) }, true, nil)},
		{"recording slow", main(func() *RecordStatus {
			r := recording(recReady("manual"))
			r.Reason = "disk_slow"
			return r
		}, true, nil)},
		{"starting", main(func() *RecordStatus { r := recReady("manual"); r.Mode, r.State = "on", "starting"; return r }, false, nil)},
		{"with_output", main(func() *RecordStatus { return recReady("with_output") }, false, nil)},
		{"with_output live", main(func() *RecordStatus { return recording(recReady("with_output")) }, true, nil)},
		{"with_output full", main(disk("with_output", RecordDisk{State: "ready", MinutesLeft: ip(0)}), false, nil)},
		{"with_output bad", main(disk("with_output", RecordDisk{State: "unsupported", Reason: "fs_ntfs"}), false, nil)},
		{"plug while live", main(func() *RecordStatus { return recReady("manual") }, true, func(h *harness) {
			h.u.rec.plugWarnUntil = h.now().Add(PlugWarnFor)
		})},
	}
	for _, why := range []string{"none", "ejected", "checking"} {
		views = append(views, renderView{"no disk " + why, main(disk("with_output", RecordDisk{State: why}), false, nil)})
	}
	for _, code := range []string{"disabled", "no_disk", "disk_full"} {
		views = append(views, renderView{"refused " + code, main(func() *RecordStatus { return recReady("manual") }, false,
			func(h *harness) { h.u.recRefused(code) })})
	}
	for _, why := range []string{"none", "ejected", "checking", "full", "bad"} {
		views = append(views, renderView{"why " + why, main(func() *RecordStatus { return recReady("manual") }, false,
			func(h *harness) { h.u.say(h.u.Tx.Rec.WhyTap[why], time.Minute) })})
	}
	for reason := range Spanish.Rec.HeadStopped {
		views = append(views, renderView{"stopped " + reason, main(stopped(reason, "off"), true, nil)})
		views = append(views, renderView{"waiting " + reason, main(stopped(reason, "on"), false, nil)})
	}
	page := func(r func() *RecordStatus, s StorageInfo, mod func(h *harness)) func(h *harness) {
		return func(h *harness) {
			a := pageApp(h, "recording", func(a *AppSnapshot) {
				a.Recording, a.RecordingAPI = fakeRecording(), Yes
				if s.State != "" {
					a.Recording.Storage = s
				}
			})
			h.setRecord(r(), false)
			if mod != nil {
				mod(h)
			}
			_ = a
		}
	}
	ready := func() *RecordStatus { return recReady("manual") }
	views = append(views,
		renderView{"page", page(ready, StorageInfo{}, nil)},
		renderView{"page recording", page(func() *RecordStatus { return recording(recReady("manual")) }, StorageInfo{}, nil)},
		renderView{"page fat32", page(ready, StorageInfo{State: "ready", Seq: 7, FS: "fat32", Label: "SANDISK",
			Model: "Ultra Fit", SizeBytes: 31e9, Dirty: true, AnotherDisk: "sdb", WriteMBps: 18.4}, nil)},
		renderView{"page low", page(func() *RecordStatus { r := recReady("manual"); r.Disk.MinutesLeft = ip(8); return r },
			StorageInfo{}, nil)},
		renderView{"page format", page(ready, StorageInfo{}, func(h *harness) { h.u.Preview().RecAsk("format") })},
		renderView{"page off", page(func() *RecordStatus { return recording(recReady("manual")) }, StorageInfo{},
			func(h *harness) { h.u.Preview().RecAsk("off") })},
		renderView{"page modes", page(ready, StorageInfo{}, func(h *harness) { h.u.Preview().Touch("s_rec_mode") })},
		renderView{"page qualities", page(ready, StorageInfo{}, func(h *harness) { h.u.Preview().Touch("s_rec_quality") })},
		renderView{"page formatting", page(ready, StorageInfo{}, func(h *harness) {
			h.u.App.(*fakeApp).snap.Pending = "rec_format"
		})},
		renderView{"page ejecting", page(ready, StorageInfo{}, func(h *harness) {
			h.u.App.(*fakeApp).snap.Pending = "rec_eject"
		})},
		renderView{"page formatted", page(ready, StorageInfo{}, func(h *harness) {
			h.u.App.(*fakeApp).snap.Last = &AppResult{What: "rec_format", OK: true, At: h.now()}
		})},
		renderView{"page ejected", page(disk("manual", RecordDisk{State: "ejected"}), StorageInfo{State: "ejected", Seq: 8},
			func(h *harness) { h.u.App.(*fakeApp).snap.Last = &AppResult{What: "rec_eject", OK: true, At: h.now()} })},
		renderView{"page none", page(disk("manual", RecordDisk{State: "none"}), StorageInfo{State: "none", Seq: 8}, nil)},
		renderView{"page power lost", page(stopped("power_lost", "off"), StorageInfo{}, nil)},
		renderView{"page disk removed", page(func() *RecordStatus {
			r := recReady("manual")
			r.Mode, r.State, r.Reason, r.Disk = "on", "error", "disk_removed", RecordDisk{State: "none"}
			return r
		}, StorageInfo{State: "none", Seq: 8}, nil)},
		renderView{"page quality locked note", page(func() *RecordStatus { return recording(recReady("manual")) },
			StorageInfo{}, func(h *harness) { h.u.notePage(h.u.Tx.Rec.QualityLocked, true) })},
		renderView{"page old app", func(h *harness) {
			pageApp(h, "recording", func(a *AppSnapshot) { a.RecordingAPI = No })
		}},
	)
	for _, st := range []StorageInfo{{State: "unsupported", Reason: "fs_ntfs", FS: "ntfs", SizeBytes: 1e12},
		{State: "unsupported", Reason: "fs_unknown", SizeBytes: 1e11}, {State: "unsupported", Reason: "too_small", SizeBytes: 5e8},
		{State: "unsupported", Reason: "too_small", SizeBytes: 8e9}, {State: "unformatted", SizeBytes: 128e9},
		{State: "error", Reason: "readonly", FS: "exfat", SizeBytes: 62e9},
		{State: "error", Reason: "check_failed", FS: "exfat", SizeBytes: 62e9},
		{State: "error", Reason: "mount_failed", FS: "ext4", SizeBytes: 62e9}, {State: "checking"}} {
		st.Seq = 8
		r := RecordDisk{State: st.State, Reason: st.Reason, FS: st.FS}
		views = append(views, renderView{"page " + st.State + " " + st.Reason, page(disk("manual", r), st, nil)})
	}
	checkRenders(t, views)
}
