package screen

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/appapi"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/scene"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst/fake"
)

func TestMain(m *testing.M) {
	elog.SetOutput(io.Discard)
	splashPoll, displayPoll, backoffMin, backoffMax = 5*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond,
		40*time.Millisecond
	os.Exit(m.Run())
}

const fontDir = "ui/testdata/fonts"

// The screen pipeline is the Python engine's (Screen.describe), token by token. One deliberate change: the
// snapshot's appsink is pulled from Go (emit-signals=false), not a signal.
func TestDescribeLikePython(t *testing.T) {
	b, err := os.ReadFile("testdata/describe-python.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSpace(string(b)), "\n")
	got := []string{
		Describe(pipeline.Tegra, ui.NewLayout(1024, 600, 4), Pipeline{Width: 1920, Height: 1080, FPS: 60,
			Latency: 60 * time.Millisecond, Snapshot: true}),
		Describe(pipeline.Tegra, ui.NewLayout(1280, 720, 4), Pipeline{Width: 1280, Height: 720, FPS: 30,
			Latency: 100 * time.Millisecond}),
	}
	for i := range want {
		w := strings.Fields(strings.Replace(want[i], "appsink name=snap emit-signals=true", "appsink name=snap emit-signals=false", 1))
		g := strings.Fields(got[i])
		if len(w) != len(g) {
			t.Fatalf("case %d: %d tokens, want %d\n%s", i, len(g), len(w), got[i])
		}
		for j := range w {
			if w[j] != g[j] {
				t.Fatalf("case %d, token %d: %q, want %q", i, j, g[j], w[j])
			}
		}
	}
}

// MultiviewRateTest (the screen's side): the multiview runs at most at 30 per second, a source's frames go to
// its tile at most one every MVGap (the engine's bridges, SetScreen), and stills always pass (in C).
func TestMultiviewGetsAtMost30PerSecond(t *testing.T) {
	lay := ui.NewLayout(1024, 600, 4)
	d60 := Describe(pipeline.Tegra, lay, Pipeline{Width: 1920, Height: 1080, FPS: 60})
	if strings.Count(d60, "framerate=30/1") != 6 || strings.Contains(d60, "framerate=60/1") { // mvp, 4 tiles, compositor
		t.Fatal(d60)
	}
	d25 := Describe(pipeline.Tegra, lay, Pipeline{Width: 1920, Height: 1080, FPS: 25})
	if strings.Count(d25, "framerate=25/1") != 6 {
		t.Fatal(d25)
	}
	if MVGap != 25*time.Millisecond || MVMargin != 40*time.Millisecond || pipeline.ScreenFPS != 30 {
		t.Fatal("multiview timing")
	}
}

// --- the lifecycle, with fake GStreamer, board and engine ---

type fakeSys struct {
	mu     sync.Mutex
	w, h   int
	ok     bool
	splash bool
	up     time.Duration
}

func (f *fakeSys) Mode() (int, int, bool) { f.mu.Lock(); defer f.mu.Unlock(); return f.w, f.h, f.ok }
func (f *fakeSys) Uptime() (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.up, true
}
func (f *fakeSys) SplashActive() bool      { f.mu.Lock(); defer f.mu.Unlock(); return f.splash }
func (f *fakeSys) set(fn func(f *fakeSys)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

type fakeHost struct {
	mu       sync.Mutex
	cmds     []map[string]any
	attached []string
	detached int
	changed  int
	reply    string
}

func (h *fakeHost) Command(cmd string, msg map[string]any) (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cmds = append(h.cmds, msg)
	if h.reply != "" {
		return false, h.reply
	}
	return true, ""
}
func (h *fakeHost) ProgramPeak(int) (float64, bool)  { return -10, true }
func (h *fakeHost) InputPeak(string) (float64, bool) { return 0, false }
func (h *fakeHost) LinkInfo() (string, int)          { return "srt", 0 }
func (h *fakeHost) BaseTime() uint64                 { return 4242 }
func (h *fakeHost) Changed()                         { h.mu.Lock(); h.changed++; h.mu.Unlock() }
func (h *fakeHost) DetachScreen()                    { h.mu.Lock(); h.detached++; h.mu.Unlock() }
func (h *fakeHost) AttachScreen(mvs []gst.Element, mvp gst.Element) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attached = nil
	for _, e := range append(mvs, mvp) {
		if e == nil {
			h.attached = append(h.attached, "<nil>")
		} else {
			h.attached = append(h.attached, e.Name())
		}
	}
}

// media records the feeders it makes and preloads the snapshot's JPEGs.
type media struct {
	*fake.Media
	mu      sync.Mutex
	feeders []*countFeeder
	jpegs   [][]byte
}

// countFeeder counts what goes through a fake feeder, safely from the test.
type countFeeder struct {
	f      *fake.Feeder
	dst    string
	size   int
	pushes atomic.Int64
	freed  atomic.Bool
}

func (c *countFeeder) Push(frame []byte) bool {
	ok := c.f.Push(frame)
	if ok {
		c.pushes.Add(1)
	}
	return ok
}

func (c *countFeeder) Free() { c.f.Free(); c.freed.Store(true) }

func (m *media) NewFeeder(appsrc gst.Element, size, buffers int) gst.Feeder {
	f := &countFeeder{f: m.Media.NewFeeder(appsrc, size, buffers).(*fake.Feeder), dst: appsrc.Name(), size: size}
	m.mu.Lock()
	m.feeders = append(m.feeders, f)
	m.mu.Unlock()
	return f
}

func (m *media) Parse(label, desc string) (gst.Pipeline, error) {
	p, err := m.Media.Parse(label, desc)
	if err == nil {
		if snap := p.(*fake.Pipeline).El("snap"); snap != nil {
			snap.Pulls = m.jpegs // before any goroutine reads it
		}
	}
	return p, err
}

func (m *media) feeder(i int) *countFeeder {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i >= len(m.feeders) {
		return nil
	}
	return m.feeders[i]
}

func pushed(f *countFeeder) int64 {
	if f == nil {
		return 0
	}
	return f.pushes.Load()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestScreenLifecycle(t *testing.T) {
	run := t.TempDir()
	m := &media{Media: fake.New(), jpegs: [][]byte{[]byte("JPEG1")}}
	sys := &fakeSys{splash: true, up: time.Hour}
	host := &fakeHost{}
	s := New(Options{Media: m, Platform: pipeline.Tegra, Host: host, Slots: 4, Width: 1920, Height: 1080, FPS: 60,
		Latency: 60 * time.Millisecond, RunDir: run, PauseSupported: true, FontDirs: []string{fontDir}, Sys: sys,
		SysRoot: t.TempDir()})
	s.Start()
	defer s.Stop(3 * time.Second)
	// The splash has the display: waiting
	waitFor(t, "waiting", func() bool { return s.Info().State == StateWaiting })
	// Let go, with the display there: on at once (no ScreenStable after the splash)
	sys.set(func(f *fakeSys) { f.splash, f.w, f.h, f.ok = false, 1024, 600, true })
	waitFor(t, "on", func() bool { return s.Info().State == StateOn })
	p := m.Find("screen")
	if p == nil || p.State != gst.StatePlaying || p.BaseTime() != 4242 {
		t.Fatalf("pipeline %+v", p)
	}
	if !s.Ready() {
		t.Fatal("not ready")
	}
	in := s.Info()
	if in.Width == nil || *in.Width != 1024 || *in.Height != 600 || in.Touch {
		t.Fatalf("info %+v", in)
	}
	host.mu.Lock()
	att := strings.Join(host.attached, " ")
	host.mu.Unlock()
	if att != "mvs0 mvs1 mvs2 mvs3 mvp" {
		t.Fatalf("attached %q", att)
	}
	// The UI layer goes through the feeder, full frames
	waitFor(t, "a UI frame", func() bool { return pushed(m.feeder(0)) > 0 })
	if f := m.feeder(0); f.dst != "ui" || f.size != 1024*600*4 {
		t.Fatalf("feeder %+v", f)
	}
	// The snapshot: written while nothing secret is on screen
	waitFor(t, "screen.jpg", func() bool { b, _ := os.ReadFile(filepath.Join(run, "screen.jpg")); return string(b) == "JPEG1" })
	// The tiles letterbox like the program
	s.SetAspects(map[int]scene.Aspect{1: {W: 4, H: 3}})
	if x := p.El("mv").Pad("sink_2", "width"); x == "" || x == "240" {
		t.Fatalf("tile 2 width %q", x)
	}
	// A touch on tile 2 goes to the engine as the socket's command, asynchronously
	st := &ui.Status{}
	st.Sources = []ui.SourceStatus{{ID: "hdmi", Slot: 1, Name: "HDMI", State: "live"}, {ID: "usb", Slot: 2, Name: "USB", State: "live"}}
	s.SetStatus(st)
	lay := ui.NewLayout(1024, 600, 4)
	r := lay.Tiles[1]
	fx, fy := float64(r.X+r.W/2)/1024, float64(r.Y+r.H/2)/600
	s.touches <- TouchEvent{Ev: "down", X: fx, Y: fy}
	s.touches <- TouchEvent{Ev: "up", X: fx, Y: fy, Held: 100 * time.Millisecond}
	waitFor(t, "the take command", func() bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return len(host.cmds) == 1 && host.cmds[0]["cmd"] == "take" && host.cmds[0]["source"] == "usb"
	})
	// Another mode: off, detached, closed, screen.jpg gone; then back on
	sys.set(func(f *fakeSys) { f.w, f.h = 1280, 720 })
	waitFor(t, "a new pipeline", func() bool { q := m.Find("screen"); return q != p && q != nil && s.Info().State == StateOn })
	host.mu.Lock()
	det := host.detached
	host.mu.Unlock()
	if det != 1 {
		t.Fatalf("detached %d", det)
	}
	if f := m.feeder(0); !f.freed.Load() {
		t.Fatal("the old feeder is not freed")
	}
	if !s.Stop(3 * time.Second) {
		t.Fatal("Stop over budget")
	}
	if _, err := os.Stat(filepath.Join(run, "screen.jpg")); err == nil {
		t.Fatal("screen.jpg left behind")
	}
	if q := m.Find("screen"); !q.Closed {
		t.Fatal("the pipeline is not closed")
	}
}

// A pipeline error: state error, then a retry after the backoff.
func TestScreenErrorRetries(t *testing.T) {
	m := &media{Media: fake.New()}
	sys := &fakeSys{w: 1024, h: 600, ok: true, up: time.Minute} // just booted: no ScreenStable
	host := &fakeHost{}
	s := New(Options{Media: m, Platform: pipeline.Tegra, Host: host, Slots: 4, Width: 1920, Height: 1080, FPS: 60,
		FontDirs: []string{fontDir}, Sys: sys, SysRoot: t.TempDir()})
	s.Start()
	defer s.Stop(3 * time.Second)
	waitFor(t, "on", func() bool { return s.Info().State == StateOn })
	p := m.Find("screen")
	p.Post(&gst.Message{Type: gst.MessageError, Src: "nvdrmvideosink0", Text: "Failed to set plane"})
	waitFor(t, "error", func() bool { return s.Info().State == StateError })
	waitFor(t, "the retry", func() bool { q := m.Find("screen"); return q != p && s.Info().State == StateOn })
}

// Fonts missing: the multiview without the interface (no feeder), like the Python engine without pycairo.
func TestScreenWithoutFonts(t *testing.T) {
	m := &media{Media: fake.New()}
	sys := &fakeSys{w: 1024, h: 600, ok: true, up: time.Minute}
	s := New(Options{Media: m, Platform: pipeline.Tegra, Host: &fakeHost{}, Slots: 4, Width: 1920, Height: 1080, FPS: 60,
		FontDirs: []string{t.TempDir()}, Sys: sys, SysRoot: t.TempDir()})
	if s.fonts != nil && !fileExists(ui.BoardFontDir) {
		t.Fatal("fonts from nowhere")
	}
	s.Start()
	defer s.Stop(3 * time.Second)
	waitFor(t, "on", func() bool { return s.Info().State == StateOn })
	if s.fonts == nil && m.feeder(0) != nil {
		t.Fatal("a feeder without fonts")
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// screen.jpg is frozen while something secret is on screen (Settings, the keyboard; WIFI.md §6).
func TestSnapshotFrozenWithSettings(t *testing.T) {
	run := t.TempDir()
	s := &Screen{o: Options{RunDir: run}}
	el := fakeSnap(t, [][]byte{[]byte("A")})
	s.secret.Store(true)
	runSnap(s, el)
	if _, err := os.Stat(filepath.Join(run, "screen.jpg")); err == nil {
		t.Fatal("written with a secret on screen")
	}
	el = fakeSnap(t, [][]byte{[]byte("B")})
	s.secret.Store(false)
	runSnap(s, el)
	if b, _ := os.ReadFile(filepath.Join(run, "screen.jpg")); string(b) != "B" {
		t.Fatalf("%q", b)
	}
}

func fakeSnap(t *testing.T, pulls [][]byte) gst.Element {
	p, _ := fake.New().Parse("snap", "appsink name=snap")
	el := p.(*fake.Pipeline).El("snap")
	el.Pulls = pulls
	return el
}

func runSnap(s *Screen, el gst.Element) {
	quit, done := make(chan struct{}), make(chan struct{})
	go s.snapshots(el, quit, done)
	time.Sleep(60 * time.Millisecond)
	close(quit)
	<-done
}

// --- the board ---

func TestTouchDecoder(t *testing.T) {
	d := touchDecoder{maxX: 1023, maxY: 599}
	var got []TouchEvent
	feed := func(typ, code uint16, val int32, ms int) {
		if ev, ok := d.event(typ, code, val, time.Duration(ms)*time.Millisecond); ok {
			got = append(got, ev)
		}
	}
	feed(evKey, btnTouch, 1, 0) // down before any position: waits for the report
	feed(evSyn, synReport, 0, 0)
	feed(evAbs, absMTPositionX, 512, 1)
	feed(evAbs, absMTPositionY, 300, 1)
	feed(evSyn, synReport, 0, 1)        // the position when it went down
	feed(evAbs, absMTPositionX, 900, 5) // moves: the down's position stays
	feed(evSyn, synReport, 0, 5)
	feed(evKey, btnTouch, 0, 701)
	if len(got) != 2 || got[0].Ev != "down" || got[1].Ev != "up" || got[1].X != got[0].X ||
		got[1].Held != 701*time.Millisecond || got[0].X < 0.49 || got[0].X > 0.51 {
		t.Fatalf("%+v", got)
	}
	// Lifted without a position: no up
	got = nil
	d2 := touchDecoder{maxX: 100, maxY: 100}
	d2.event(evKey, btnTouch, 1, 0)
	if _, ok := d2.event(evKey, btnTouch, 0, 10); ok {
		t.Fatal("up without a position")
	}
}

func TestTouchReaderEvents(t *testing.T) {
	// The raw input_event layout (64-bit): timeval, type, code, value
	ev := make([]byte, eventSize)
	binary.LittleEndian.PutUint16(ev[16:], evKey)
	binary.LittleEndian.PutUint16(ev[18:], btnTouch)
	binary.LittleEndian.PutUint32(ev[20:], 1)
	if binary.LittleEndian.Uint16(ev[16:]) != 1 || int32(binary.LittleEndian.Uint32(ev[20:])) != 1 {
		t.Fatal("layout")
	}
}

func TestFindTouch(t *testing.T) {
	root := t.TempDir()
	mk := func(ev, abs, name string) {
		d := filepath.Join(root, "sys/class/input", ev, "device/capabilities")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "abs"), []byte(abs+"\n"), 0o644)
		os.WriteFile(filepath.Join(root, "sys/class/input", ev, "device/name"), []byte(name+"\n"), 0o644)
	}
	mk("event0", "3", "keyboard")
	mk("event1", "0 260800000000003", "touchscreen") // ABS_MT_POSITION_X (bit 53) in the last word
	path, name := findTouch(root)
	if !strings.HasSuffix(path, "/dev/input/event1") || name != "touchscreen" {
		t.Fatalf("%q %q", path, name)
	}
}

func TestBoardSys(t *testing.T) {
	root := t.TempDir()
	write := func(p, v string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(v), 0o644)
	}
	b := BoardSys{Root: root}
	if _, _, ok := b.Mode(); ok {
		t.Fatal("a mode without files")
	}
	write("sys/class/switch/hdmi/state", "1\n")
	write("sys/class/graphics/fb0/virtual_size", "1024,600\n")
	if w, h, ok := b.Mode(); !ok || w != 1024 || h != 600 {
		t.Fatal(w, h, ok)
	}
	write("sys/class/graphics/fb0/virtual_size", "640,480\n") // no EDID yet
	if _, _, ok := b.Mode(); ok {
		t.Fatal("640x480 is no screen")
	}
	write("sys/class/switch/hdmi/state", "0\n")
	write("sys/class/graphics/fb0/virtual_size", "1024,600\n")
	if _, _, ok := b.Mode(); ok {
		t.Fatal("unplugged")
	}
	write("proc/uptime", "42.5 100.0\n")
	if !justBooted(b) {
		t.Fatal("just booted")
	}
	write("proc/uptime", "4200.5 100.0\n")
	if justBooted(b) {
		t.Fatal("not just booted")
	}
	// The splash: systemctl's ActiveState
	for _, c := range []struct {
		out    string
		rc     int
		active bool
	}{{"active\n", 0, true}, {"deactivating\n", 0, true}, {"inactive\n", 0, false}, {"failed\n", 0, false}, {"", 0, false},
		{"active\n", 1, false}} {
		b.Run = func(time.Duration, string, ...string) sysx.Result { return sysx.Result{RC: c.rc, Out: c.out} }
		if b.SplashActive() != c.active {
			t.Errorf("%q rc %d", c.out, c.rc)
		}
	}
}

func TestSensors(t *testing.T) {
	root := t.TempDir()
	write := func(p, v string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(v), 0o644)
	}
	write("sys/devices/virtual/thermal/thermal_zone0/type", "AO-therm\n")
	write("sys/devices/virtual/thermal/thermal_zone1/type", "CPU-therm\n")
	write("sys/devices/virtual/thermal/thermal_zone1/temp", "36500\n")
	write("sys/bus/i2c/drivers/ina3221x/6-0040/iio:device0/in_voltage0_input", "5180\n")
	s := &Sensors{Root: root}
	temp, okT, volts, okV := s.Read()
	if !okT || !okV || temp != 36.5 || volts != 5.18 {
		t.Fatal(temp, okT, volts, okV)
	}
	if _, okT, _, okV := (&Sensors{Root: t.TempDir()}).Read(); okT || okV {
		t.Fatal("sensors from nowhere")
	}
}

// The app's snapshot in the UI's terms.
func TestAppNet(t *testing.T) {
	ch := 6
	s := appapi.Snapshot{API: appapi.APIUp, Status: &appapi.Status{Enabled: true,
		AccessPoint: appapi.AccessPoint{Enabled: true, Policy: "always", State: "on", SSID: "X", Channel: &ch},
		LAN:         appapi.LAN{Enabled: true}, Cameras: appapi.Cameras{Items: []appapi.Camera{{ID: "net:cam1"}}}},
		Client:  &appapi.Client{Enabled: true, State: "connected", SSID: "Bar"},
		Scan:    []appapi.Network{{SSID: "Bar", Security: "wpa2", Known: true}},
		Pending: &appapi.Change{What: "connect", Value: "Bar"},
		Message: appapi.Message{Text: "hola"}}
	n := fromApp(s)
	if n.API != ui.Yes || n.Status.Cameras != 1 || !n.Status.LANEnabled || *n.Status.AccessPoint.Enabled != true ||
		*n.Status.AccessPoint.Channel != 6 || n.Client.SSID != "Bar" || len(n.Scan) != 1 || n.Pending.SSID != "Bar" ||
		n.Message != "hola" {
		t.Fatalf("%+v", n)
	}
	if n := fromApp(appapi.Snapshot{API: appapi.APIGone}); n.API != ui.No || n.Status != nil || n.Scan != nil {
		t.Fatalf("%+v", n)
	}
}

// test_engine_phones.py's test_screen_ready and test_screen_delay (the screen's side): the headphones open the
// HDMI's audio only with the screen on; the screen pipeline's latency is unknown until gst has a query.
func TestScreenReadyAndLatency(t *testing.T) {
	sys := &fakeSys{splash: true}
	s := New(Options{Media: &media{Media: fake.New()}, Platform: pipeline.Tegra, Host: &fakeHost{}, Slots: 4, Width: 1920,
		Height: 1080, FPS: 60, FontDirs: []string{fontDir}, Sys: sys, SysRoot: t.TempDir()})
	if s.Ready() {
		t.Fatal("ready before starting")
	}
	s.Start()
	defer s.Stop(3 * time.Second)
	waitFor(t, "waiting", func() bool { return s.Info().State == StateWaiting })
	if s.Ready() {
		t.Fatal("ready while the splash has the display")
	}
	sys.set(func(f *fakeSys) { f.splash, f.w, f.h, f.ok = false, 1024, 600, true })
	waitFor(t, "ready", s.Ready)
	if _, ok := s.Latency(); ok {
		t.Fatal("a latency without a query")
	}
}

// The app's device API reaches the UI through the Net (screen: hook for the app's device API).
func TestAppNetCarriesApp(t *testing.T) {
	n := appapi.New(nil, "/nonexistent/api.sock")
	p, ok := AppNet(n).(ui.AppProvider)
	if !ok || p.App() == nil || p.App() != ui.App(n.Device()) {
		t.Fatal("the device API does not reach the UI")
	}
}
