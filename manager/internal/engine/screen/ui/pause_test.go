package ui

import (
	"testing"
)

// The Pause page: the card's text through the keyboard (the engine's rule) and the mute switch, straight to the
// engine; a refusal says so on the page.
func TestPausePage(t *testing.T) {
	h, _ := pagesHarness(t, "pause")
	h.u.PauseSupported = true
	st := *h.u.st
	st.Program.Pause = &struct {
		On   bool   `json:"on"`
		Text string `json:"text"`
		Mute bool   `json:"mute"`
	}{Text: "Volvemos enseguida", Mute: true}
	h.u.SetStatus(&st)
	h.cmds = nil
	h.press("s_pause_mute", 0.1)
	if len(h.cmds) != 1 || h.cmds[0]["cmd"] != "pause" || h.cmds[0]["mute"] != false {
		t.Fatalf("mute: %v", h.cmds)
	}
	h.press("s_pause_text", 0.1)
	if h.u.settingsView != "keyboard" || h.u.kb.spec == nil || string(h.u.kb.text) != "Volvemos enseguida" {
		t.Fatalf("keyboard: %q %+v", h.u.settingsView, h.u.kb)
	}
	h.tapKey("BKSP", 1)                 // held: all of it
	h.u.kb.text = []rune("Vuelvo <ya>") // (the keys are on another layer)
	h.tapKey("OK", 0.1)
	if h.u.kb == nil || h.u.kb.err == "" {
		t.Fatal("< > were taken")
	}
	h.tapKey("BKSP", 1)
	for _, c := range "vuelvo en un rato" {
		h.tapKey(string(c), 0.1)
	}
	h.cmds = nil
	h.tapKey("OK", 0.1)
	if h.u.kb != nil || len(h.cmds) != 1 || h.cmds[0]["text"] != "vuelvo en un rato" {
		t.Fatalf("text: %v %+v", h.cmds, h.u.kb)
	}
	// The engine refuses: the page says it
	h.u.CommandDone(Command{Msg: h.cmds[0], Origin: "page"}, false, "bad_request")
	if txt, warn := h.u.pageNoteText(); !warn || txt == "" {
		t.Fatalf("note %q %v", txt, warn)
	}
	// Without a pause in the engine, nothing answers
	h.u.PauseSupported = false
	h.cmds = nil
	h.press("s_pause_mute", 0.1)
	if len(h.cmds) != 0 {
		t.Fatal("an engine without pause got a command")
	}
}

func TestPauseTextOK(t *testing.T) {
	for _, s := range []string{"Volvemos enseguida", "  Vuelvo en 5 min. ¡Ya! ", "Café #2 @ 20:00"} {
		if !PauseTextOK(s) {
			t.Errorf("%q refused", s)
		}
	}
	for _, s := range []string{"", "   ", "<b>", "a & b", "uno\ndos", string(make([]rune, 61))} {
		if PauseTextOK(s) {
			t.Errorf("%q taken", s)
		}
	}
}

func TestRenderStream(t *testing.T) {
	checkRenders(t, []renderView{
		{"pause", func(h *harness) {
			pageApp(h, "pause", nil)
		}},
		{"pause-on", func(h *harness) {
			st := *h.u.st
			st.Program.Pause = &struct {
				On   bool   `json:"on"`
				Text string `json:"text"`
				Mute bool   `json:"mute"`
			}{On: true, Text: "Volvemos enseguida", Mute: true}
			h.u.SetStatus(&st)
			pageApp(h, "pause", nil)
		}},
		{"pause-keyboard", func(h *harness) {
			pageApp(h, "pause", nil)
			h.u.pageTouch("s_pause_text", "", false)
		}},
	})
}

// The generic picker answers real taps: the option under the finger when it went down, ← without choosing, pages.
func TestPickerTaps(t *testing.T) {
	h, _ := drawHarness(t)
	pageApp(h, "pause", nil)
	var got []string
	opts := []pickOpt{{ID: "a", Title: "A"}, {ID: "b", Title: "B", Sub: "dos"}}
	for i := range 12 {
		opts = append(opts, pickOpt{ID: "x" + itoa(i), Title: "X" + itoa(i)})
	}
	h.u.openPicker("Elige", "b", opts, func(u *UI, id string) { got = append(got, id) })
	h.u.Dirty = true
	h.u.Tick() // drawn: the rows as seen
	if h.u.settingsView != "pick" || len(h.u.chooser.shown) == 0 {
		t.Fatalf("%q %+v", h.u.settingsView, h.u.chooser)
	}
	h.press("s_pick_0", 0.1)
	if len(got) != 1 || got[0] != "a" || h.u.settingsView != "main" {
		t.Fatalf("%v %q", got, h.u.settingsView)
	}
	// ← goes back without choosing; the second page's first row is the next option
	h.u.openPicker("Elige", "", opts, func(u *UI, id string) { got = append(got, id) })
	h.u.Dirty = true
	h.u.Tick()
	h.press("s_next", 0.1)
	h.u.Dirty = true
	h.u.Tick()
	h.press("s_pick_0", 0.1)
	if len(got) != 2 || got[1] != opts[h.lay.ListRows].ID {
		t.Fatalf("%v (rows %d)", got, h.lay.ListRows)
	}
	h.u.openPicker("Elige", "", opts, func(u *UI, id string) { got = append(got, id) })
	h.press("s_back", 0.1)
	if len(got) != 2 || h.u.settingsView != "main" {
		t.Fatalf("← chose: %v", got)
	}
}
