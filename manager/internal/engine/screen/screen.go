// Package screen is the encoder's touch screen at run time (ENGINE_GO.md §3, M3): the multiview pipeline on
// the board's HDMI, the display's detection, the evdev touch, and the goroutine that draws the UI (package
// ui) and pushes it into the pipeline. Nothing here holds the engine up: the screen has its own goroutines,
// its commands go to the engine asynchronously, and if the screen fails or is unplugged, the broadcast goes
// on (it is another pipeline).
//
// Goroutines: the lifecycle (splash → display → pipeline → retries), the UI (touches, ticks, drawing: it owns
// the ui.UI), the commands (to the engine, one at a time), the touch reader, and one per pipeline for the
// screen.jpg snapshots.
package screen

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/scene"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// Timing of the display's detection and the retries (engine_mixer.py).
const (
	ScreenStable = 3 // readings (one a second) with the screen plugged and the same mode before painting
	busPoll      = 200 * time.Millisecond
	closeBudget  = 2 * time.Second
	uiBuffers    = 3 // the UI layer's buffer pool (no allocation per image)
	snapshotName = "screen.jpg"
)

// Variables only so the tests can run them fast.
var (
	splashPoll  = 250 * time.Millisecond
	displayPoll = time.Second // a reading of the display's mode while waiting for it, and while it is shown
	backoffMin  = 3 * time.Second
	backoffMax  = 30 * time.Second
)

// Screen states (the state's screen.state).
const (
	StateOff     = "off"
	StateWaiting = "waiting" // the boot splash has the display
	StateOn      = "on"
	StateError   = "error"
)

// Host is the engine as the screen sees it.
type Host interface {
	// Command runs a command like the socket's (MIXER.md §5) on the engine's actor and returns its result.
	// It is called from the screen's command goroutine, one at a time and in order, never from the drawing.
	// It must return (unsupported, or any code) once the engine is stopping. The held «Emitir» sends
	// {"cmd":"output","on":…,"by":"screen"}: the engine records by=screen (out_by/out_seq), which the socket
	// never sends.
	Command(cmd string, msg map[string]any) (bool, string)
	// ProgramPeak and InputPeak: the VU meters' levels, read 20 times a second from the UI goroutine.
	ui.Levels
	// LinkInfo: the output's transport ("srt" | "srtla") and, with SRTLA, the uplinks it uses, read at each
	// drawing (the screen words it in its language: «SRTLA · 2 links»).
	LinkInfo() (transport string, links int)
	// BaseTime: the program pipeline's base_time; the screen's pipeline shares its timeline.
	BaseTime() uint64
	// AttachScreen: the screen's pipeline is PLAYING. The engine points each slot's bridge at mvs[i]
	// (SetScreen(mvs[i], MVGap)), pushes black into those without a source (attach_screen), and sends pgmtap to
	// mvp through a BridgeShared bridge with Extra = latency + MVMargin.
	AttachScreen(mvs []gst.Element, mvp gst.Element)
	// DetachScreen: before the pipeline closes; once it returns no bridge pushes into it. Neither this nor
	// AttachScreen may wait for the engine's actor (Stop may be called from it).
	DetachScreen()
	// Changed: the screen's part of the state changed (Info): publish. From any goroutine.
	Changed()
}

// Options is what the screen needs.
type Options struct {
	Media    gst.Media
	Platform pipeline.Platform
	Host     Host
	// The program: slots, size, rate and latency (MIXER_LATENCY)
	Slots, Width, Height, FPS int
	Latency                   time.Duration
	// RunDir: where screen.jpg goes ("" = no snapshot)
	RunDir string
	// PauseSupported: the program has the Pause layer
	PauseSupported bool
	// Net: the app's local API (or SetNet before Start), started by the engine. nil = none
	Net ui.Net
	// FontDirs: DejaVu is looked for here first, then in ui.BoardFontDir
	FontDirs []string
	// Strings: the texts until the app says the device language (nil = English)
	Strings *ui.Strings
	// Sys: the board's display, uptime and splash (nil = BoardSys); SysRoot prefixes sysfs and /dev for the
	// touch screen and the sensors (tests)
	Sys     Sys
	SysRoot string
	// WifiFile: wifi.json for an older app ("" = appapi.WiFiFile)
	WifiFile string
}

// Info is the screen's part of the published state.
type Info struct {
	State         string
	Width, Height *int
	Touch         bool
	Power         *ui.PowerRequest
}

type attachReq struct {
	lay    *ui.Layout
	feeder gst.Feeder
	done   chan struct{}
}

type cmdResult struct {
	c    ui.Command
	ok   bool
	code string
}

// Screen is the running screen.
type Screen struct {
	o     Options
	host  Host
	sys   Sys
	fonts *ui.Fonts
	u     *ui.UI // owned by the UI goroutine
	touch *touchReader

	displayWait time.Duration // the last wait for a stable display and the readings it needed (lifecycle
	displayNeed int           // goroutine only)
	displayBad  int           // readings without a screen in that wait

	mu        sync.Mutex
	state     string
	w, h      int
	power     *ui.PowerRequest
	mv        gst.Element
	pipe      gst.Pipeline // while the multiview is on, for the latency query
	lay       *ui.Layout
	aspects   map[int]scene.Aspect
	status    *ui.Status
	statusNew bool
	drawMax   time.Duration
	touchMax  time.Duration

	secret  atomic.Bool
	wake    chan struct{}
	touches chan TouchEvent
	cmds    chan ui.Command
	results chan cmdResult
	attach  chan attachReq
	uiDone  chan struct{} // closed when the UI goroutine has ended: it pushes no more
	stop    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

// New builds the screen; Start runs it.
func New(o Options) *Screen {
	s := &Screen{o: o, host: o.Host, sys: o.Sys, state: StateOff,
		wake: make(chan struct{}, 1), touches: make(chan TouchEvent, 64), cmds: make(chan ui.Command, 32),
		results: make(chan cmdResult, 32), attach: make(chan attachReq), uiDone: make(chan struct{}),
		stop: make(chan struct{})}
	if s.sys == nil {
		s.sys = BoardSys{Root: o.SysRoot}
	}
	fonts, err := ui.LoadFonts(o.FontDirs...)
	if err != nil {
		elog.Event("mixer", "screen_fonts_missing", "err", err) // the multiview without the interface
	}
	s.fonts = fonts
	sensors := &Sensors{Root: o.SysRoot}
	u := ui.New(nil, o.Strings, o.Net)
	u.Fonts, u.Levels, u.Logo, u.PauseSupported = fonts, o.Host, ui.DefaultLogo(), o.PauseSupported
	u.LinkInfo, u.Sensors, u.Legacy = o.Host.LinkInfo, sensors.Read, legacyWifi(o.WifiFile)
	u.State = StateOff
	u.Send = func(c ui.Command) {
		select {
		case s.cmds <- c:
		default:
			cmd, _ := c.Msg["cmd"].(string)
			elog.Event("mixer", "screen_command_dropped", "cmd", cmd)
		}
	}
	u.Changed = func() {
		s.mu.Lock()
		s.power = u.Power()
		s.mu.Unlock()
		o.Host.Changed()
	}
	s.u = u
	s.touch = &touchReader{root: o.SysRoot, out: s.touches, stop: s.stop}
	return s
}

// SetNet plugs the app's local API in, before Start (its changed callback is NetChanged, so it is built
// after the screen): s.SetNet(AppNet(appapi.New(s.NetChanged, ""))).
func (s *Screen) SetNet(n ui.Net) {
	if n != nil {
		s.u.Net = n
	}
}

// Start runs the screen's goroutines.
func (s *Screen) Start() {
	s.wg.Add(4)
	go func() { defer s.wg.Done(); s.run() }()
	go func() { defer s.wg.Done(); s.uiLoop() }()
	go func() { defer s.wg.Done(); s.cmdLoop() }()
	go func() { defer s.wg.Done(); s.touch.run() }()
}

// Stop ends the screen within budget (the pipeline closes with its own deadline; the goroutines are left
// behind if one is stuck). True if everything ended in time.
func (s *Screen) Stop(budget time.Duration) bool {
	s.once.Do(func() { close(s.stop) })
	s.touch.close()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(budget):
		elog.Event("mixer", "screen_stop_slow", "budget_s", budget.Seconds())
		return false
	}
}

func (s *Screen) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *Screen) sleep(d time.Duration) bool {
	select {
	case <-s.stop:
		return false
	case <-time.After(d):
		return true
	}
}

func (s *Screen) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// SetStatus hands the screen the engine's newly published state (from the actor, after each publish).
func (s *Screen) SetStatus(st *ui.Status) {
	s.mu.Lock()
	s.status, s.statusNew = st, true
	s.mu.Unlock()
	s.poke()
}

// NetChanged: the app's network state changed (Net's callback): redraw.
func (s *Screen) NetChanged() { s.poke() }

// SetAspects: each slot's source aspect (the tiles letterbox like the program).
func (s *Screen) SetAspects(a map[int]scene.Aspect) {
	cp := make(map[int]scene.Aspect, len(a))
	for k, v := range a {
		cp[k] = v
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aspects = cp
	s.fitTiles()
}

// fitTiles (with mu held): each tile with its source's aspect.
func (s *Screen) fitTiles() {
	if s.mv == nil || s.lay == nil {
		return
	}
	for i, r := range s.lay.Tiles {
		f := scene.FitRect(scene.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H}, s.aspects[i])
		pad := "sink_" + strconv.Itoa(i+1)
		s.mv.SetPad(pad, "xpos", strconv.Itoa(f.X))
		s.mv.SetPad(pad, "ypos", strconv.Itoa(f.Y))
		s.mv.SetPad(pad, "width", strconv.Itoa(f.W))
		s.mv.SetPad(pad, "height", strconv.Itoa(f.H))
	}
}

// Info is the screen's part of the state: off/waiting/on/error, the last size, the touch, a power request.
func (s *Screen) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := Info{State: s.state, Touch: s.touch.present.Load()}
	if s.w > 0 {
		w, h := s.w, s.h
		in.Width, in.Height = &w, &h
	}
	if s.power != nil {
		p := *s.power
		in.Power = &p
	}
	return in
}

// Ready: the screen shows the multiview (the headphones open the HDMI's audio only then).
func (s *Screen) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == StateOn
}

// Latency is the screen pipeline's latency, for the headphones' delay (Python's latency_ns); false
// without a multiview or if it does not answer.
func (s *Screen) Latency() (time.Duration, bool) {
	s.mu.Lock()
	p := s.pipe
	s.mu.Unlock()
	if p == nil {
		return 0, false
	}
	return p.Latency()
}

// TakeTouchMax is the slowest touch since the last call, from the kernel's event to the UI frame that
// shows it pushed into the screen (the engine's timings line).
func (s *Screen) TakeTouchMax() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.touchMax
	s.touchMax = 0
	return d
}

// TakeDrawMax is the slowest full drawing since the last call (the engine's timings line: «dibujo»).
func (s *Screen) TakeDrawMax() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.drawMax
	s.drawMax = 0
	return d
}

func (s *Screen) setState(st string) {
	s.mu.Lock()
	changed := s.state != st
	s.state = st
	s.mu.Unlock()
	if changed {
		s.host.Changed()
		s.poke()
	}
}

// --- the lifecycle ---

func (s *Screen) run() {
	waited := false
	for s.sys.SplashActive() {
		if s.stopped() {
			return
		}
		waited = true
		s.setState(StateWaiting)
		if !s.sleep(splashPoll) {
			return
		}
	}
	backoff := backoffMin
	for {
		// Just let go by the splash, or just after boot, the display has been stable since tegradc set its mode
		// (~1.4 s): no need to wait ScreenStable (yes after a failure, a mode change or plugging it)
		need := ScreenStable
		if waited || justBooted(s.sys) {
			need = 1
		}
		waited = false
		tw := time.Now()
		w, h, ok := s.waitDisplay(need)
		if !ok {
			return
		}
		s.displayWait, s.displayNeed = time.Since(tw), need // for screen_on (lifecycle goroutine only)
		t0 := time.Now()
		if s.show(w, h) == "error" {
			s.setState(StateError)
			// After a failure the HDMI output goes off and the panel «unplugs» for a few seconds: retrying at
			// once fails again with «Failed to set plane» (trap 11)
			if time.Since(t0) > time.Minute {
				backoff = backoffMin
			} else {
				backoff = min(backoff*2, backoffMax)
			}
			if !s.sleep(backoff) {
				return
			}
		}
		if s.stopped() {
			return
		}
	}
}

// waitDisplay is the display's mode once it has read the same `need` times in a row (one a second).
func (s *Screen) waitDisplay(need int) (int, int, bool) {
	stable, lw, lh := 0, -1, -1
	s.displayBad = 0
	for !s.stopped() {
		w, h, ok := s.sys.Mode()
		if !ok {
			s.displayBad++
			s.setState(StateOff)
		}
		if ok && (w == lw && h == lh || need <= 1) {
			stable++
		} else {
			stable = 0
		}
		if ok {
			lw, lh = w, h
		} else {
			lw, lh = -1, -1
		}
		if stable >= need {
			return w, h, true
		}
		if !s.sleep(displayPoll) {
			break
		}
	}
	return 0, 0, false
}

// show paints until the pipeline fails, the mode changes, the display is unplugged or the screen stops.
// The reason: error | mode | "".
func (s *Screen) show(w, h int) string {
	tShow := time.Now()
	o := s.o
	lay := ui.NewLayout(w, h, o.Slots)
	desc := Describe(o.Platform, lay, Pipeline{Width: o.Width, Height: o.Height, FPS: o.FPS, Latency: o.Latency,
		Snapshot: o.RunDir != ""})
	p, err := o.Media.Parse("screen", desc)
	if err != nil {
		elog.Event("mixer", "screen_build_failed", "err", err)
		return "error"
	}
	p.FixBase(s.host.BaseTime())
	var feeder gst.Feeder
	if src := p.ByName("ui"); src != nil && s.fonts != nil {
		feeder = o.Media.NewFeeder(src, w*h*4, uiBuffers)
	}
	mvs := make([]gst.Element, o.Slots)
	for i := range mvs {
		mvs[i] = p.ByName(fmt.Sprintf("mvs%d", i))
	}
	s.mu.Lock()
	s.w, s.h = w, h
	s.mu.Unlock()
	if err := p.SetState(gst.StatePlaying); err != nil {
		text := err.Error()
		if m := p.Pop(0, gst.MessageError); m != nil {
			text = m.Text
		}
		elog.Event("mixer", "screen_start_failed", "err", text)
		gst.CloseWithin(p, closeBudget)
		if feeder != nil {
			feeder.Free()
		}
		return "error"
	}
	s.mu.Lock()
	s.mv, s.lay, s.pipe = p.ByName("mv"), lay, p
	s.fitTiles()
	s.mu.Unlock()
	if !s.toUI(attachReq{lay: lay, feeder: feeder}) {
		s.teardown(p, feeder, nil)
		return ""
	}
	s.host.AttachScreen(mvs, p.ByName("mvp"))
	s.setState(StateOn)
	elog.Event("mixer", "screen_on", "width", w, "height", h, "display_wait_ms", s.displayWait.Milliseconds(),
		"need", s.displayNeed, "no_display", s.displayBad, "show_ms", time.Since(tShow).Milliseconds())
	var snapDone chan struct{}
	quit := make(chan struct{})
	if snk := p.ByName("snap"); snk != nil && o.RunDir != "" {
		snapDone = make(chan struct{})
		go s.snapshots(snk, quit, snapDone)
	}
	reason := ""
	next := time.Now().Add(displayPoll)
	for !s.stopped() {
		if m := p.Pop(busPoll, gst.MessageError); m != nil {
			dbg := m.Debug
			if len(dbg) > 200 {
				dbg = dbg[len(dbg)-200:]
			}
			elog.Event("mixer", "screen_error", "element", m.Src, "error", m.Text, "debug", dbg)
			reason = "error"
			break
		}
		if now := time.Now(); !now.Before(next) {
			next = now.Add(displayPoll)
			if mw, mh, ok := s.sys.Mode(); !ok || mw != w || mh != h {
				elog.Event("mixer", "screen_mode_changed")
				reason = "mode"
				break
			}
		}
	}
	s.host.DetachScreen()
	close(quit)
	s.teardown(p, feeder, snapDone)
	return reason
}

// toUI hands the UI goroutine a new layer (or none) and waits until it has it; false if it has ended
// (stopping). Either way, once it returns the UI pushes nothing into the previous feeder.
func (s *Screen) toUI(a attachReq) bool {
	a.done = make(chan struct{})
	select {
	case s.attach <- a:
	case <-s.uiDone:
		return false
	}
	<-a.done
	return true
}

func (s *Screen) teardown(p gst.Pipeline, feeder gst.Feeder, snapDone chan struct{}) {
	s.toUI(attachReq{}) // the UI stops pushing before the feeder goes
	if snapDone != nil {
		<-snapDone
	}
	s.mu.Lock()
	s.mv, s.lay, s.pipe = nil, nil, nil
	s.mu.Unlock()
	if !gst.CloseWithin(p, closeBudget) {
		elog.Event("mixer", "screen_close_stuck", "budget_s", closeBudget.Seconds())
	}
	if feeder != nil {
		feeder.Free()
	}
	if s.o.RunDir != "" {
		os.Remove(filepath.Join(s.o.RunDir, snapshotName)) // the last image is no longer what is shown
	}
	s.setState(StateOff)
}

// snapshots writes what the screen shows, once a second, to RUN_DIR/screen.jpg (the web can show it). With
// something secret on screen (Settings: the wifi password and QR, the keyboard) it is not updated.
func (s *Screen) snapshots(snk gst.Element, quit, done chan struct{}) {
	defer close(done)
	path := filepath.Join(s.o.RunDir, snapshotName)
	for {
		select {
		case <-quit:
			return
		default:
		}
		if jpg := snk.PullBytes(500 * time.Millisecond); jpg != nil && !s.secret.Load() {
			_ = sysx.WriteAtomic(path, jpg, 0o644)
		}
	}
}

// --- the UI goroutine ---

func (s *Screen) uiLoop() {
	defer close(s.uiDone)
	tick := time.NewTicker(ui.MeterEvery)
	defer tick.Stop()
	u := s.u
	var feeder gst.Feeder
	var touchAt time.Time // the oldest touch not on screen yet
	for {
		select {
		case <-s.stop:
			s.secret.Store(true)
			return
		case a := <-s.attach:
			if a.lay != nil {
				u.SetLayout(a.lay)
			}
			feeder = a.feeder
			touchAt = time.Time{} // a touch on a dark or rebuilding screen is not a latency
			close(a.done)
		case <-tick.C:
		case <-s.wake:
		case r := <-s.results:
			u.CommandDone(r.c, r.ok, r.code)
		case ev := <-s.touches:
			s.syncIn()
			u.OnTouch(ev.Ev, ev.X, ev.Y, ev.Held)
			if touchAt.IsZero() && !ev.At.IsZero() && feeder != nil {
				touchAt = ev.At
			}
		}
		s.syncIn()
		if u.Tick() && feeder != nil {
			if !feeder.Push(u.Image().Pix) {
				u.PushFailed()
			} else if !touchAt.IsZero() {
				// From the finger to the UI layer's appsrc; the screen's compositor adds about a frame
				s.mu.Lock()
				s.touchMax = max(s.touchMax, time.Since(touchAt))
				s.mu.Unlock()
				touchAt = time.Time{}
			}
		}
		s.secret.Store(u.SecretOnScreen())
		if d := u.TakeDrawMax(); d > 0 {
			s.mu.Lock()
			s.drawMax = max(s.drawMax, d)
			s.mu.Unlock()
		}
	}
}

// syncIn gives the UI the screen's state and the last published status.
func (s *Screen) syncIn() {
	s.mu.Lock()
	st, isNew, state := s.status, s.statusNew, s.state
	s.statusNew = false
	s.mu.Unlock()
	s.u.State = state
	if isNew {
		s.u.SetStatus(st)
	}
}

// cmdLoop runs the screen's commands on the engine, in order, and gives the results back to the UI.
func (s *Screen) cmdLoop() {
	for {
		select {
		case <-s.stop:
			return
		case c := <-s.cmds:
			cmd, _ := c.Msg["cmd"].(string)
			ok, code := s.host.Command(cmd, c.Msg)
			select {
			case s.results <- cmdResult{c, ok, code}:
			case <-s.stop:
				return
			}
		}
	}
}
