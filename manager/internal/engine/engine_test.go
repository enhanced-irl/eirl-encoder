package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst/fake"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// runner answers the programs the engine runs, as the test says
type runner struct {
	mu        sync.Mutex
	list      string        // probe -list's JSON
	dv        string        // --query-dv-timings' answer ("" = no signal)
	gate      chan struct{} // probe -list waits for it when set
	listCalls int
	log       []string
}

func (r *runner) run(_ time.Duration, name string, args ...string) sysx.Result {
	line := name + " " + strings.Join(args, " ")
	r.mu.Lock()
	gate := r.gate
	if strings.Contains(line, "probe -list") {
		r.listCalls++
	}
	r.mu.Unlock()
	if gate != nil && strings.Contains(line, "probe -list") {
		<-gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, line)
	switch {
	case strings.Contains(line, "probe -list"):
		return sysx.Result{Out: r.list}
	case strings.Contains(line, "--query-dv-timings") && r.dv != "":
		return sysx.Result{Out: r.dv}
	case strings.Contains(line, "--query-dv-timings"):
		return sysx.Result{RC: 1, Err: "VIDIOC_QUERY_DV_TIMINGS: failed: No link has been detected"}
	case strings.Contains(line, "probe -all"), strings.Contains(line, "probe -device"):
		return sysx.Result{RC: 3, Err: "no device"}
	}
	return sysx.Result{}
}

func testConfig(t *testing.T, env map[string]string) *config.Config {
	t.Helper()
	run := t.TempDir()
	state := t.TempDir()
	base := map[string]string{"RUN_DIR": run, "STATE_DIR": state, "SOURCE": "hdmi", "MIXER_SCREEN": "0", "OUTPUT": "off"}
	for k, v := range env {
		base[k] = v
	}
	c, err := config.Load(config.Options{
		LookupEnv:  func(k string) (string, bool) { v, ok := base[k]; return v, ok },
		ReadFile:   func(string) ([]byte, error) { return nil, os.ErrNotExist },
		Executable: func(string) bool { return true },
		FindVDEV:   func() string { return "/dev/video0" },
		ScriptDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func startEngine(t *testing.T, env map[string]string, list string) (*Engine, *fake.Media, *runner) {
	t.Helper()
	elog.SetOutput(&strings.Builder{})
	t.Cleanup(func() { elog.SetOutput(nil) })
	c := testConfig(t, env)
	m := fake.New()
	r := &runner{list: list}
	e := New(Options{Config: c, Media: m, Platform: pipeline.Tegra, Run: r.run, MixerMain: "hdmi", FPS: 60})
	if err := e.Start(false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	return e, m, r
}

const catalogue = `{"sources":[{"id":"tc","kind":"hdmi-csi","node":"/dev/video0"},
	{"id":"/dev/v4l/by-id/usb-Logi-video-index0","kind":"usb","name":"Logi 4K"}],"audio":[]}`

func TestStartPublishesAStateTheAppReads(t *testing.T) {
	e, m, _ := startEngine(t, nil, catalogue)
	pgm := m.Find("program")
	if pgm == nil || pgm.State != 4 || pgm.Base == 0 {
		t.Fatalf("program not playing with a fixed base: %+v", pgm)
	}
	if !strings.Contains(pgm.Desc, "nvcompositor name=pgm") || strings.Contains(pgm.Desc, "pgmtap") {
		t.Error("program: the compositor, and no screen branch in M1")
	}
	// The state as the app's client sees it
	var st mixer.State
	if err := readState(e, &st); err != nil {
		t.Fatal(err)
	}
	if st.V != 1 || st.Slots != 4 || st.Program.Width != 1920 || st.Program.FPS != 60 || st.Program.Layout != "full" {
		t.Errorf("state: %+v", st)
	}
	if st.Program.Main == nil || *st.Program.Main != "hdmi" || st.Program.Hidden == nil {
		t.Errorf("program main/hidden: %+v", st.Program)
	}
	if len(st.Sources) != 2 || st.Sources[0].ID != "hdmi" || st.Sources[0].Slot != 1 || st.Sources[1].Slot != 2 {
		t.Errorf("sources: %+v", st.Sources)
	}
	if st.Screen.State != "off" || st.Output == nil || st.Output.Mode != "off" {
		t.Errorf("screen/output: %+v %+v", st.Screen, st.Output)
	}
	if st.Audio == nil || st.Audio.Mode != "single" || st.Audio.Phones == nil || st.Audio.Phones.GainDb != -12 {
		t.Errorf("audio bus and headphones: %+v", st.Audio)
	}
	// Black in every slot at start, the Pause still on its layer
	dsts := []string{}
	for _, b := range m.Bridges {
		dsts = append(dsts, b.Dst)
	}
	// The output's two bridges have no destination until it emits
	if strings.Join(dsts, ",") != "m0,m1,m2,m3,mp,aa0,aa1,aa2,aa3,," || m.Bridges[0].LastStill() == "" {
		t.Errorf("bridges: %v", dsts)
	}
}

// readState reads mixer.json with the app's own client
func readState(e *Engine, st *mixer.State) error {
	got, err := (&mixer.Client{StatePath: filepath.Join(e.runDir, "mixer.json")}).Read()
	if err != nil {
		return err
	}
	*st = *got
	return nil
}

var gstMessageError = gst.Message{Type: gst.MessageError, Src: "pgm", Text: "boom"}

func TestCommands(t *testing.T) {
	e, m, _ := startEngine(t, nil, catalogue)
	cases := []struct {
		msg  map[string]any
		ok   bool
		code string
	}{
		{map[string]any{"cmd": "take", "source": "nope"}, false, "unknown_source"},
		{map[string]any{"cmd": "take", "source": "/dev/v4l/by-id/usb-Logi-video-index0"}, true, ""},
		{map[string]any{"cmd": "output", "on": false}, true, ""},
		{map[string]any{"cmd": "output", "on": "yes"}, false, "bad_request"},
		{map[string]any{"cmd": "audio", "source": "nope"}, false, "unknown_source"},
		{map[string]any{"cmd": "audio_mode", "mode": "mix"}, true, ""},
		{map[string]any{"cmd": "phones", "gain": -20}, true, ""},
		{map[string]any{"cmd": "phones", "gain": 5}, false, "bad_request"},
		{map[string]any{"cmd": "preview", "on": false}, true, ""},
		{map[string]any{"cmd": "lock", "on": true}, true, ""},
		{map[string]any{"cmd": "layout", "layout": "pip"}, true, ""},
		{map[string]any{"cmd": "pause", "on": true, "text": "Vuelvo"}, true, ""},
		{map[string]any{"cmd": "dance"}, false, "unknown_command"},
	}
	for _, c := range cases {
		ok, code := e.command(c.msg["cmd"].(string), c.msg)
		if ok != c.ok || code != c.code {
			t.Errorf("%v → %v %q, want %v %q", c.msg, ok, code, c.ok, c.code)
		}
	}
	pgm := m.Find("program")
	if pgm.El("livevalve").Prop("drop") != "true" || pgm.El("liveavalve").Prop("drop") != "true" {
		t.Error("preview off closes both valves")
	}
	var st mixer.State
	if err := readState(e, &st); err != nil {
		t.Fatal(err)
	}
	if *st.Program.Main != "/dev/v4l/by-id/usb-Logi-video-index0" || st.Program.Secondary == nil || *st.Program.Secondary != "hdmi" ||
		st.Program.Layout != "pip" || st.Screen.Locked == nil || !*st.Screen.Locked || !st.Program.Pause.On {
		t.Errorf("after the commands: %+v %+v", st.Program, st.Screen)
	}
	// The scene went to the compositor: the USB (slot 2) full screen, the HDMI (slot 1) in the PiP
	comp := pgm.El("pgm")
	if comp.Pad("sink_1", "width") != "1920" || comp.Pad("sink_1", "alpha") != "1" || comp.Pad("sink_0", "width") != "640" {
		t.Errorf("compositor: sink_1 %v sink_0 %v", comp.Pads["sink_1"], comp.Pads["sink_0"])
	}
	// A new Pause text makes a new still; the scene file has it
	// (made off the actor: under load it may take a moment)
	pauseStill := func() bool {
		p := m.Find("still-pause")
		return p != nil && strings.Contains(p.El("txt").Prop("text"), "Vuelvo")
	}
	for deadline := time.Now().Add(5 * time.Second); !pauseStill() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if !pauseStill() {
		t.Error("Pause still with its text as a property")
	}
	b, _ := os.ReadFile(e.sceneFile)
	if !strings.Contains(string(b), `"text": "Vuelvo"`) || !strings.Contains(string(b), `"locked": true`) ||
		!strings.Contains(string(b), `"mode": "mix"`) || !strings.Contains(string(b), `"gainDb": -20`) {
		t.Errorf("scene file: %s", b)
	}
}

// The audio watcher's hotplug pulse cuts the HDMI's video for a moment: never with it on the program.
func TestMayReplugHDMI(t *testing.T) {
	e, _, _ := startEngine(t, nil, catalogue)
	on := func(msg map[string]any) { e.command(msg["cmd"].(string), msg) }
	if e.replugAllowed() {
		t.Error("HDMI is the main source")
	}
	on(map[string]any{"cmd": "take", "source": "/dev/v4l/by-id/usb-Logi-video-index0"})
	if !e.replugAllowed() {
		t.Error("HDMI off the program, not broadcasting")
	}
	on(map[string]any{"cmd": "layout", "layout": "pip"})
	if e.replugAllowed() {
		t.Error("HDMI in the PiP")
	}
	on(map[string]any{"cmd": "layout", "layout": "full"})
	if !e.replugAllowed() {
		t.Error("full screen again: the PiP's HDMI is not seen")
	}
}

func TestSlateTick(t *testing.T) {
	e, m, _ := startEngine(t, nil, catalogue)
	hdmi := m.Bridges[0]
	// Opening for less than 2 s: black; then the slate while it gives no image
	e.slateTick()
	if hdmi.LastStill() != "snk" { // the fake's stills come from the still pipelines' appsink
		t.Fatalf("still: %q", hdmi.LastStill())
	}
	n := len(hdmi.Stills)
	e.sources[0].mu.Lock()
	e.sources[0].state, e.sources[0].since = StateLive, time.Now().Add(-time.Minute)
	e.sources[0].mu.Unlock()
	hdmi.Frames(1)
	e.slateTick()
	if len(hdmi.Stills) != n {
		t.Error("a live source with fresh frames gets no slate")
	}
	m.Advance(time.Second)
	e.slateTick()
	if len(hdmi.Stills) != n+1 {
		t.Error("a live source without frames for 0.5 s gets the slate")
	}
	// The Pause layer always gets its still
	if len(m.Bridges[4].Stills) < 3 {
		t.Errorf("pause stills: %d", len(m.Bridges[4].Stills))
	}
	// An empty slot gets black at every tick: its screen tile must not keep the last frame of what left it
	empty := len(m.Bridges[2].Stills)
	e.slateTick()
	if e.sources[2] != nil || len(m.Bridges[2].Stills) != empty+1 {
		t.Errorf("empty slot stills: %d → %d", empty, len(m.Bridges[2].Stills))
	}
}

func TestDiscoveryAssignsSlots(t *testing.T) {
	cat := `{"sources":[{"id":"/dev/v4l/by-id/usb-A-video-index0","kind":"usb","name":"A"},
		{"id":"net:cam1","kind":"network","node":"rtsp://127.0.0.1:8554/cam1","name":"Móvil"},
		{"id":"tc","kind":"hdmi-csi","node":"/dev/video0"}],"audio":[]}`
	e, _, r := startEngine(t, map[string]string{"SOURCE": "test"}, cat)
	got := func() string {
		var ids []string
		for _, s := range e.sources {
			if s == nil {
				ids = append(ids, "-")
			} else {
				ids = append(ids, s.id)
			}
		}
		return strings.Join(ids, ",")
	}
	// The HDMI first, the bars (SOURCE=test), then the catalogue's order
	if want := "hdmi,test,/dev/v4l/by-id/usb-A-video-index0,net:cam1"; got() != want {
		t.Fatalf("slots %s, want %s", got(), want)
	}
	// The camera is removed from the app: its slot is freed; a rename follows the app
	r.mu.Lock()
	r.list = `{"sources":[{"id":"/dev/v4l/by-id/usb-A-video-index0","kind":"usb","name":"A"},{"id":"tc","kind":"hdmi-csi","node":"/dev/video0"}],"audio":[]}`
	r.mu.Unlock()
	e.discovered(e.probeList())
	if want := "hdmi,test,/dev/v4l/by-id/usb-A-video-index0,-"; got() != want {
		t.Fatalf("after removal %s, want %s", got(), want)
	}
	// A failed probe removes nothing
	r.mu.Lock()
	r.list = "garbage"
	r.mu.Unlock()
	e.discovered(e.probeList())
	if want := "hdmi,test,/dev/v4l/by-id/usb-A-video-index0,-"; got() != want || e.disc.retryAt.IsZero() {
		t.Fatalf("after a failed probe %s", got())
	}
}

func TestScreenIsWired(t *testing.T) {
	elog.SetOutput(&strings.Builder{})
	t.Cleanup(func() { elog.SetOutput(nil) })
	c := testConfig(t, map[string]string{"MIXER_SCREEN": "1"})
	m := fake.New()
	r := &runner{list: catalogue}
	e := New(Options{Config: c, Media: m, Platform: pipeline.Tegra, Run: r.run, MixerMain: "hdmi", FPS: 60})
	if err := e.Start(true); err != nil {
		t.Fatal(err)
	}
	// Like main: Run ends, then Stop
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { e.Run(ctx); close(runDone) }()
	defer func() { cancel(); <-runDone; e.Stop() }()
	if !strings.Contains(m.Find("program").Desc, "appsink name=pgmtap") || e.scr == nil {
		t.Fatal("the program has the screen's tap and the engine a screen")
	}
	// The screen asks for commands through the engine; emitting from it says by=screen
	if ok, code := (screenHost{e}).Command("output", map[string]any{"on": true}); !ok || code != "" {
		t.Fatalf("output from the screen: %v %q", ok, code)
	}
	var st mixer.State
	if err := readState(e, &st); err != nil {
		t.Fatal(err)
	}
	if st.Output == nil || st.Output.By != "screen" || st.Output.Seq != 1 || st.Output.Mode != "on" {
		t.Errorf("output: %+v", st.Output)
	}
	if ok, code := (screenHost{e}).Command("take", map[string]any{"cmd": "take", "source": "nope"}); ok || code != "unknown_source" {
		t.Errorf("take: %v %q", ok, code)
	}
}

func TestRunEndsOnProgramError(t *testing.T) {
	e, m, _ := startEngine(t, nil, catalogue)
	done := make(chan error, 1)
	go func() { done <- e.Run(context.Background()) }()
	m.Find("program").Post(&gstMessageError)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run should end with the program's error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not end")
	}
}
