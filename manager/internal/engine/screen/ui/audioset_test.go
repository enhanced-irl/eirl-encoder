package ui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The audio view's settings (audioset.go): each input's delay and each camera's audio.

func sp(s string) *string { return &s }

// asetInputs are four audio inputs as the engine publishes them: the board's HDMI, two cameras' and a standalone mic.
func asetInputs() []AudioInput {
	return []AudioInput{{ID: "i2s", Name: "HDMI de la placa", State: "live", VideoSlot: 1},
		{ID: "alsa:A", Name: "Cam A", State: "live", VideoSlot: 2, DelayMs: 120},
		{ID: "alsa:B", Name: "Cam B", State: "no_signal", VideoSlot: 3},
		{ID: "alsa:Mic", Name: "USB Mic", State: "live"}}
}

// setAudio gives the harness's engine state n inputs and the cameras' links (hdmi → i2s, usb1 → alsa:A, usb2 →
// none of its own), then lets change modify it.
func setAudio(h *harness, n int, change func(au *AudioStatus)) {
	st := *h.u.st
	main := "i2s"
	st.Audio = &AudioStatus{Main: &main, Mode: "single", Follow: true, Sources: asetInputs()[:n],
		Links: map[string]*string{"hdmi": sp("i2s"), "usb1": sp("alsa:A"), "usb2": nil}}
	if change != nil {
		change(st.Audio)
	}
	h.u.SetStatus(&st)
}

// asetHarness is a harness in the audio settings, with n inputs.
func asetHarness(t *testing.T, n int) *harness {
	h := newHarness(t)
	setAudio(h, n, nil)
	h.u.view = "audio"
	h.press("a_settings", 0.1)
	if h.u.view != "audioset" || len(h.cmds) != 0 {
		t.Fatalf("view %q, cmds %v", h.u.view, h.cmds)
	}
	return h
}

// lastCmd is the last command sent, checked against want (and ms an int, as the engine takes it).
func lastCmd(t *testing.T, h *harness, want map[string]any) {
	t.Helper()
	if len(h.cmds) == 0 {
		t.Fatalf("no command, want %v", want)
	}
	if got := h.cmds[len(h.cmds)-1]; !reflect.DeepEqual(got, want) {
		t.Fatalf("%v (%T), want %v", got, got["ms"], want)
	}
}

func TestAudioSetDelay(t *testing.T) {
	h := asetHarness(t, 4)
	delay := func(id string, ms int) map[string]any {
		return map[string]any{"cmd": "audio_delay", "source": id, "ms": ms}
	}
	// A tap: 10 ms; again before the engine publishes it, from what was sent (not the state's 120)
	h.press("a_s_dp_1", 0.1)
	lastCmd(t, h, delay("alsa:A", 130))
	h.press("a_s_dp_1", 0.1)
	lastCmd(t, h, delay("alsa:A", 140))
	if got := h.u.audioDelay(&h.u.st.Audio.Sources[1]); got != 140 {
		t.Fatalf("shown %d", got)
	}
	// The first one published: still the last one sent
	setAudio(h, 4, func(au *AudioStatus) { au.Sources[1].DelayMs = 130 })
	if got := h.u.audioDelay(&h.u.st.Audio.Sources[1]); got != 140 {
		t.Fatalf("shown %d", got)
	}
	// The engine publishes it: the state's again; a long press, 100 ms to the grid
	setAudio(h, 4, func(au *AudioStatus) { au.Sources[1].DelayMs = 140 })
	h.press("a_s_dp_1", TapLong.Seconds()+0.1)
	lastCmd(t, h, delay("alsa:A", 200))
	setAudio(h, 4, func(au *AudioStatus) { au.Sources[1].DelayMs = 200 })
	h.press("a_s_dm_1", TapLong.Seconds()+0.1)
	lastCmd(t, h, delay("alsa:A", 100))
	// A value off the grid (set on the web) goes to the grid in the direction asked
	setAudio(h, 4, func(au *AudioStatus) { au.Sources[1].DelayMs = 125 })
	h.press("a_s_dp_1", 0.1)
	lastCmd(t, h, delay("alsa:A", 130))
	setAudio(h, 4, func(au *AudioStatus) { au.Sources[1].DelayMs = 125 })
	h.advance(audioSetSent + time.Second) // what was sent and never published is too old: the state's
	h.press("a_s_dm_1", 0.1)
	lastCmd(t, h, delay("alsa:A", 120))
	// The bounds: 0 and 500 send nothing; a long press near them stops at them
	n := len(h.cmds)
	h.press("a_s_dm_0", 0.1)
	h.press("a_s_dm_0", TapLong.Seconds()+0.1)
	if len(h.cmds) != n {
		t.Fatalf("below 0: %v", h.cmds[n:])
	}
	setAudio(h, 4, func(au *AudioStatus) { au.Sources[3].DelayMs = 460; au.Sources[0].DelayMs = 30 })
	h.press("a_s_dp_3", TapLong.Seconds()+0.1)
	lastCmd(t, h, delay("alsa:Mic", AudioDelayMax))
	setAudio(h, 4, func(au *AudioStatus) { au.Sources[3].DelayMs = AudioDelayMax; au.Sources[0].DelayMs = 30 })
	n = len(h.cmds)
	h.press("a_s_dp_3", 0.1)
	if len(h.cmds) != n {
		t.Fatalf("over 500: %v", h.cmds[n:])
	}
	h.press("a_s_dm_0", TapLong.Seconds()+0.1)
	lastCmd(t, h, delay("i2s", 0))
	for _, c := range h.cmds {
		if ms := c["ms"].(int); ms < 0 || ms > AudioDelayMax || ms%AudioDelayStep != 0 {
			t.Errorf("out of bounds: %v", c)
		}
	}
	// A row without an input does nothing
	h2 := asetHarness(t, 2)
	h2.press("a_s_dp_2", 0.1)
	h2.press("a_s_dm_3", 0.1)
	if len(h2.cmds) != 0 {
		t.Fatal(h2.cmds)
	}
}

func TestDelayStep(t *testing.T) {
	for _, c := range []struct{ v, step, dir, want int }{
		{0, 10, 1, 10}, {0, 10, -1, 0}, {120, 100, 1, 200}, {120, 100, -1, 100}, {125, 10, 1, 130},
		{125, 10, -1, 120}, {495, 10, 1, 500}, {500, 10, 1, 500}, {460, 100, 1, 500}, {30, 100, -1, 0}, {5, 10, -1, 0}} {
		if got := delayStep(c.v, c.step, c.dir); got != c.want {
			t.Errorf("delayStep(%d, %d, %d) = %d, want %d", c.v, c.step, c.dir, got, c.want)
		}
	}
}

func TestAudioSetLinks(t *testing.T) {
	h := asetHarness(t, 4)
	// The cameras: each slot's video source that the engine links (hdmi, usb1, usb2), in slot order
	var ids []string
	for _, c := range h.u.audioSetCameras() {
		ids = append(ids, c.ID)
	}
	if !eqStrings(ids, []string{"hdmi", "usb1", "usb2"}) {
		t.Fatal(ids)
	}
	// Camera 2's choice: the inputs as the strips name them, «no audio of its own»; its current one marked
	h.press("a_s_link_1", 0.1)
	p := h.u.aset.pick
	if p == nil || p.video != "usb1" || p.cur != "alsa:A" || p.label != "2 · usb1" || h.u.hitView() != "audiopick" {
		t.Fatalf("pick %+v", p)
	}
	var titles []string
	for _, o := range p.opts {
		titles = append(titles, o.ID+"="+o.Title)
	}
	want := []string{"i2s=1 · HDMI de la placa", "alsa:A=2 · Cam A", "alsa:B=3 · Cam B", "alsa:Mic=USB Mic",
		"none=Sin audio propio"}
	if !eqStrings(titles, want) {
		t.Fatalf("%q", titles)
	}
	if !p.opts[2].Warn || p.opts[2].Sub != "sin señal" || p.opts[0].Warn {
		t.Errorf("states: %+v", p.opts)
	}
	// The current one: closes, sends nothing
	h.press("a_s_opt_1", 0.1)
	if h.u.aset.pick != nil || len(h.cmds) != 0 {
		t.Fatalf("pick %v, cmds %v", h.u.aset.pick, h.cmds)
	}
	// Another one: audio_link with its id; «no audio of its own» is "none"
	h.press("a_s_link_1", 0.1)
	h.press("a_s_opt_3", 0.1)
	lastCmd(t, h, map[string]any{"cmd": "audio_link", "video": "usb1", "source": "alsa:Mic"})
	h.press("a_s_link_0", 0.1)
	h.press("a_s_opt_4", 0.1)
	lastCmd(t, h, map[string]any{"cmd": "audio_link", "video": "hdmi", "source": "none"})
	if h.u.view != "audioset" || h.u.aset.pick != nil {
		t.Fatal("after choosing: back to the settings")
	}
	// A camera without audio of its own: «none» is its current one
	h.press("a_s_link_2", 0.1)
	if h.u.aset.pick.cur != "none" {
		t.Fatal(h.u.aset.pick.cur)
	}
	// ← closes the choice without choosing; ← again, back to the strips
	n := len(h.cmds)
	h.press("a_s_back", 0.1)
	if h.u.view != "audioset" || h.u.aset.pick != nil || len(h.cmds) != n {
		t.Fatal("← in the choice")
	}
	h.press("a_s_back", 0.1)
	if h.u.view != "audio" {
		t.Fatal(h.u.view)
	}
	// Linked to an input that is not there: it stays in the list (unplugged), and in its row
	setAudio(h, 4, func(au *AudioStatus) { au.Links["usb2"] = sp("alsa:Gone") })
	h.press("a_settings", 0.1)
	if name, gone := h.u.linkName(h.u.cameraLink("usb2")); name != "Gone (desconectada)" || !gone {
		t.Fatal(name, gone)
	}
	if name, _ := h.u.linkName("alsa:Gone_Microphone_Long_Name_That_Is_Very_Long"); name != "Gone_Microphone_Long_Na… (desconectada)" {
		t.Fatal(name)
	}
	h.press("a_s_link_2", 0.1)
	last := h.u.aset.pick.opts[len(h.u.aset.pick.opts)-1]
	if len(h.u.aset.pick.opts) != audioSetOpts || last.ID != "alsa:Gone" || !last.Warn {
		t.Fatalf("%+v", h.u.aset.pick.opts)
	}
	// A row without a camera does nothing; an engine without links has no camera rows
	h.press("a_s_back", 0.1)
	h.press("a_s_link_3", 0.1)
	if h.u.aset.pick != nil {
		t.Fatal("row 4 has no camera")
	}
	setAudio(h, 4, func(au *AudioStatus) { au.Links = nil })
	if len(h.u.audioSetCameras()) != 0 {
		t.Fatal("no links, no cameras")
	}
}

// The views' buttons do not leak into each other: the strips' are not found in the settings and the other way round;
// with a choice open, only its rows and ←.
func TestAudioSetHit(t *testing.T) {
	for _, sz := range [][2]int{{1024, 600}, {1280, 720}, {1920, 1080}} {
		lay := NewLayout(sz[0], sz[1], 4)
		at := func(k string, view string) string {
			b := lay.Buttons[k]
			return lay.Hit(b.X+b.W/2, b.Y+b.H/2, false, view, false, nil).Key
		}
		for k := range lay.Buttons {
			for _, view := range audioViews {
				got := at(k, view)
				if got != "" && strings.HasPrefix(got, "a_") && !audioKeyIn(got, view) {
					t.Errorf("%v %s: %s answers at %s", sz, view, got, k)
				}
			}
		}
		if at("a_settings", "audio") != "a_settings" || at("a_s_link_0", "audiopick") == "a_s_link_0" {
			t.Errorf("%v: keys", sz)
		}
		// The settings' button between «Mix» and «Follows video», apart from both
		st, mx, fl := lay.Buttons["a_settings"], lay.Buttons["a_mix"], lay.Buttons["a_follow"]
		if st.X < mx.X+mx.W+lay.M || st.X+st.W+lay.M > fl.X || st.Y != fl.Y {
			t.Errorf("%v: a_settings %v between %v and %v", sz, st, mx, fl)
		}
		// Fingers: 48 px or more
		for k, b := range lay.Buttons {
			if strings.HasPrefix(k, "a_s_") && (b.H < 48 || b.W < 48) {
				t.Errorf("%v: %s %v", sz, k, b)
			}
		}
		// The rows end above the note line
		for i := range audioSetOpts {
			if b := lay.Buttons[fmt.Sprintf("a_s_opt_%d", i)]; b.Y+b.H > lay.ASet.Note.Y {
				t.Errorf("%v: option %d %v under the note %v", sz, i, b, lay.ASet.Note)
			}
		}
		for i := range lay.N {
			if b := lay.Buttons[fmt.Sprintf("a_s_link_%d", i)]; b.Y+b.H > lay.ASet.Note.Y {
				t.Errorf("%v: link %d %v", sz, i, b)
			}
		}
	}
}

// A refusal shows at the bottom of the view (for a while) and the delay goes back to the state's.
func TestAudioSetRefused(t *testing.T) {
	h := asetHarness(t, 2)
	h.press("a_s_dp_1", 0.1)
	c := Command{Msg: h.cmds[0], Origin: originAudioSet}
	h.u.CommandDone(c, true, "")
	if h.u.aset.note != "" {
		t.Fatal("an OK is not a refusal")
	}
	h.u.CommandDone(c, false, "bad_request")
	if h.u.aset.note != "No se puede: valor no válido" {
		t.Fatalf("%q", h.u.aset.note)
	}
	if got := h.u.audioDelay(&h.u.st.Audio.Sources[1]); got != 120 {
		t.Fatalf("after the refusal: %d", got)
	}
	h.press("a_s_dp_1", 0.1)
	lastCmd(t, h, map[string]any{"cmd": "audio_delay", "source": "alsa:A", "ms": 130})
	// A refusal of another origin is not this view's
	h.u.aset.note = ""
	h.u.CommandDone(Command{Msg: map[string]any{"cmd": "audio"}, Origin: "audio"}, false, "unknown_source")
	if h.u.aset.note != "" || h.u.hint != "" {
		t.Fatal("audio's refusal")
	}
	// Leaving the view drops it
	h.u.CommandDone(c, false, "unknown_source")
	h.press("a_s_back", 0.1)
	h.press("a_settings", 0.1)
	if h.u.aset.note != "" {
		t.Fatal("note after leaving")
	}
}

// Locked: nothing in the settings (nor in a choice) answers but holding the padlock; the state machine goes on.
func TestAudioSetLocked(t *testing.T) {
	h := asetHarness(t, 4)
	h.press("a_s_link_1", 0.1)
	h.setLocked(true)
	for _, k := range []string{"a_s_back", "a_s_opt_0", "settings", "power"} {
		h.press(k, 0.1)
	}
	h.press("a_s_back", 0.1)
	if h.u.view != "audioset" || h.u.aset.pick == nil || h.u.menuOpen || h.u.settingsOpen || len(h.cmds) != 0 {
		t.Fatalf("locked: view %q pick %v cmds %v", h.u.view, h.u.aset.pick, h.cmds)
	}
	h.u.aset.pick = nil
	for _, k := range []string{"a_s_dp_0", "a_s_dm_1", "a_s_link_0"} {
		h.press(k, 0.1)
	}
	if len(h.cmds) != 0 || h.u.aset.pick != nil {
		t.Fatal(h.cmds)
	}
	h.press("lock", Hold.Seconds()+0.1)
	if got := h.cmdNames(); !eqStrings(got, []string{"map[cmd:lock on:false]"}) {
		t.Fatal(got)
	}
	// The engine's state changes under the view (an input unplugged, no audio at all): it keeps working
	h.setLocked(false)
	setAudio(h, 1, nil)
	h.u.Tick()
	h.press("a_s_dp_3", 0.1)
	st := *h.u.st
	st.Audio = nil
	h.u.SetStatus(&st)
	h.u.Tick()
	h.press("a_s_dp_0", 0.1)
	h.press("a_s_link_0", 0.1)
	if len(h.cmds) != 1 || h.u.aset.pick != nil {
		t.Fatal(h.cmds)
	}
	// Settings from the header and back: still in the audio settings
	h.press("settings", 0.1)
	if !h.u.settingsOpen {
		t.Fatal("settings")
	}
	h.press("s_close", 0.1)
	if h.u.settingsOpen || h.u.view != "audioset" {
		t.Fatal(h.u.view)
	}
}

func TestAudioSetRenders(t *testing.T) {
	long := func(h *harness) { // long names (aliases at their longest, a device's long name)
		st := *h.u.st
		st.Sources = append([]SourceStatus(nil), st.Sources...)
		st.Sources[0].Alias = "Plano general con grúa X"
		st.Sources[1].Alias = "Cámara de moto trasera"
		st.Sources = append(st.Sources, SourceStatus{ID: "net:cam1", Slot: 4, Kind: "network", Name: "Móvil de Ana",
			State: "live"})
		h.u.SetStatus(&st)
	}
	audio := func(n int, change func(au *AudioStatus)) func(h *harness) {
		return func(h *harness) {
			setAudio(h, n, func(au *AudioStatus) {
				au.Sources[0].Name = "Logi 4K Stream Edition Pro Max USB-C"
				au.Sources[len(au.Sources)-1].VideoSlot = 0 // a standalone input, by its device's long name
				au.Sources[len(au.Sources)-1].Name = "Razer Seiren Mini USB Microphone Analog Stereo"
				au.Links["net:cam1"] = sp("alsa:Gone_Microphone_Long_Name")
				if change != nil {
					change(au)
				}
			})
			h.u.Preview().AudioSettings(-1)
		}
	}
	var views []renderView
	for n := 1; n <= 4; n++ {
		views = append(views, renderView{fmt.Sprintf("inputs%d", n), audio(n, nil)},
			renderView{fmt.Sprintf("long%d", n), func(h *harness) { long(h); audio(n, nil)(h) }})
	}
	views = append(views,
		renderView{"mix", audio(3, func(au *AudioStatus) { au.Mode = "mix" })},
		renderView{"followoff", audio(3, func(au *AudioStatus) { au.Follow = false })},
		renderView{"delays", audio(4, func(au *AudioStatus) {
			for i := range au.Sources {
				au.Sources[i].DelayMs = []int{0, 120, 500, 35}[i]
				au.Sources[i].State = []string{"live", "gone", "error", "starting"}[i]
			}
		})},
		renderView{"noaudio", func(h *harness) {
			st := *h.u.st
			st.Audio = nil
			h.u.SetStatus(&st)
			h.u.Preview().AudioSettings(-1)
		}},
		renderView{"nolinks", audio(2, func(au *AudioStatus) { au.Links = nil })},
		renderView{"pick", func(h *harness) { long(h); audio(4, nil)(h); h.u.openAudioPick(1) }},
		renderView{"pickgone", func(h *harness) { long(h); audio(4, nil)(h); h.u.openAudioPick(3) }},
		renderView{"refused", func(h *harness) {
			audio(2, nil)(h)
			h.u.CommandDone(Command{Msg: map[string]any{"cmd": "audio_link"}, Origin: originAudioSet}, false, "unknown_source")
		}},
		renderView{"locked", func(h *harness) { audio(4, nil)(h); h.setLocked(true) }},
		renderView{"press", func(h *harness) {
			audio(4, nil)(h)
			h.u.Preview().Press(HitButton, "a_s_dp_1", 0)
		}},
		renderView{"strips", func(h *harness) { audio(4, nil)(h); h.u.view = "audio" }},
	)
	checkRenders(t, views)
	// The pick's title names the camera; the strips' header has the settings' button
	h, _ := drawHarness(t)
	long(h)
	audio(4, nil)(h)
	h.u.openAudioPick(0)
	if h.u.aset.pick.label != "1 · Plano general con grúa X" {
		t.Fatal(h.u.aset.pick.label)
	}
	h.u.Tick()
}
