// Package engine is the encoder's engine in Go (ENGINE_GO.md): the sources, the scene, the program and
// the command socket around GStreamer. It replaces eirl-engine.sh + engine-run.py + engine_mixer.py
// with the same contract (socket, mixer.json, run files, configuration).
//
// One goroutine, the actor (Run), owns the scene and the program: commands, timers, discovery and
// publishing all run there, one at a time, like the Python engine's GLib loop. Each source has its own
// goroutine. No frame ever enters Go: they cross between pipelines through bridges in C
// (internal/gst), and the goroutines only read their counters.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/appapi"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/audio"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/camera"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/hdmi"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/netcam"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/output"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/phones"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/probe"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/scene"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/server"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/tcaudio"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/usbhub"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

const (
	slateEvery    = 250 * time.Millisecond // «Sin señal» in slots whose source gives no image
	periodicEvery = 2 * time.Second
	discoverEvery = 2 * time.Second
	fadeStep      = 20 * time.Millisecond   // the actor's check of a running fade (the audio's steps too)
	fadeGrace     = time.Second             // a video fade's table no longer holds the actor after its length + this
	mvGap         = 25 * time.Millisecond   // one frame per source to the screen every MV_GAP (30/s)
	stopBudget    = 4 * time.Second         // for the sources to end
	stopTotal     = 9500 * time.Millisecond // all of Stop: the unit gives 10 (TimeoutStopSec)
)

// Options is what the engine needs from outside.
type Options struct {
	Config   *config.Config
	Media    gst.Media
	Platform pipeline.Platform
	Run      sysx.Runner
	// MixerMain is the source on air at start (hdmi, test, usb:auto or a by-id path) and FPS the
	// program's rate, both chosen by the caller like the launcher's mixer_prepare
	MixerMain string
	FPS       int
	// TC358743: the board has the HDMI input (Prepare), for the HDMI audio watcher
	TC358743 bool
	// InputALSA is the main USB source's audio card (Prepare), for AUDIO_SOURCE=auto
	InputALSA string
	// USB is where the USB recovery finds sysfs and the hubs (zero value = the real system)
	USB usbhub.Sys
}

// Engine is the running engine.
type Engine struct {
	cfg      *config.Config
	media    gst.Media
	plat     pipeline.Platform
	run      sysx.Runner
	net      *netcam.Client
	srv      *server.Server
	hubs     *usbhub.Hubs
	nslots   int
	tc       bool
	out      *output.Output // the hot output (F3): SRT/SRTLA with ABR
	scr      *screen.Screen // the touch screen (nil with MIXER_SCREEN=0)
	appNet   *appapi.NetState
	scrMu    sync.Mutex
	mvBridge gst.Bridge     // program → screen (pgmtap → mvp)
	audio    *audio.Bus     // nil without audio slots
	phones   *phones.Phones // nil without phtap
	inALSA   string
	cams     *camera.Store // the image settings given to each camera (SOURCES.md §7.4)

	width, height, fps int
	latency, sync      time.Duration
	netSync            time.Duration
	nvcam              bool
	runDir, sceneFile  string
	configMain         string

	// The program
	pgm         gst.Pipeline
	comp        gst.Element  // the compositor: its sink pads are the layers
	anim        gst.Animator // sets those pads from the compositor's thread once it plays
	playing     bool         // the program reached PLAYING: layers go through anim
	bridges     []gst.Bridge
	pauseBridge gst.Bridge
	thumbsMu    sync.Mutex // thumbs is set by its own goroutine at start (startThumbs) and taken by Stop
	thumbs      gst.Pipeline
	slate       gst.Buffer
	black       gst.Buffer
	pauseBuf    gst.Buffer
	cadence     gst.Cadence
	cadValve    gst.Element
	vsrc        gst.Counter
	liveValves  []gst.Element
	pauseSlot   int

	// Owned by the actor
	scene     scene.Scene
	locked    bool
	extra     scene.Extra // the audio settings of mixer-scene.json, kept for the audio phase
	sources   []*Source
	slotCfg   []SlotConfig    // slots.go: what each slot takes, and its alias
	lastCat   probe.Catalogue // the last good catalogue (the state's catalog, re-placing on a slots change)
	applied   map[int]scene.Layer
	aspects   map[int]scene.Aspect
	fading    *fadeState
	previewOn bool
	pauseSeq  int // the newest Pause text whose image is being made
	outBy     string
	outSeq    int
	newFPS    *float64
	measured  *float64
	per       periodicState
	disc      discoverState

	hiddenMu sync.RWMutex
	hidden   map[string]bool // a copy of the scene's hidden set, for the source goroutines

	events  chan func()
	changes chan struct{}
	fatal   chan error
	wg      sync.WaitGroup
	stopped chan struct{}
	ctx     context.Context // ends at Stop: the engine's helpers (HDMI audio watcher…)
	cancel  context.CancelFunc
}

type fadeState struct {
	t0     time.Time
	length time.Duration
}

// New builds the engine. Start makes the program and the sources; Run is the actor.
func New(o Options) *Engine {
	c := o.Config
	run := o.Run
	if run == nil {
		run = sysx.Run
	}
	prog := c.Program(o.FPS)
	e := &Engine{
		cfg: c, media: o.Media, plat: o.Platform, run: run, nslots: c.MixerSlots,
		width: prog.Width, height: prog.Height, fps: prog.FPS,
		latency: time.Duration(c.MixerLatencyMs) * time.Millisecond,
		sync:    time.Duration(max(0, c.MixerSyncMs)) * time.Millisecond,
		netSync: time.Duration(c.MixerNetSyncMs) * time.Millisecond,
		nvcam:   c.MixerHDMICapture == "nvcam" && o.Media.HasElement(o.Platform.CSICapture),
		runDir:  c.RunDir, configMain: o.MixerMain,
		net:       &netcam.Client{KeyFile: netcam.KeyFile(c.NetConfDir)},
		previewOn: true, outBy: "start",
		hidden:    map[string]bool{},
		events:    make(chan func(), 64),
		changes:   make(chan struct{}, 1),
		fatal:     make(chan error, 1),
		stopped:   make(chan struct{}),
		pauseSlot: c.MixerSlots,
		hubs:      usbhub.NewHubs(o.USB),
		tc:        o.TC358743,
		inALSA:    o.InputALSA,
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	camFile := ""
	if c.StateDir != "" && c.PrepareStateDir() {
		e.sceneFile = filepath.Join(c.StateDir, scene.FileName)
		camFile = filepath.Join(c.StateDir, camera.FileName)
	}
	e.cams = camera.NewStore(camFile)
	if !c.RunDirUsable() {
		e.runDir = ""
	}
	e.sources = make([]*Source, e.nslots)
	e.loadSlots()
	e.loadScene()
	return e
}

// programDesc is the program pipeline for this configuration.
func (e *Engine) programDesc(screen bool) string {
	c := e.cfg
	lw, lh := splitSize(c.LiveSize, 640, 360)
	tw, th := splitSize(c.ThumbSize, 320, 180)
	return e.plat.Program(pipeline.Program{
		Width: e.width, Height: e.height, FPS: e.fps, Slots: e.nslots, AudioSlots: c.MixerAudioSlots,
		LatencyNs: e.latency.Nanoseconds(),
		Live:      c.Live, LiveWidth: lw, LiveHeight: lh, LiveVKbps: c.LiveVBitrate, LiveAKbps: c.LiveABitrate,
		LiveVPort: c.LiveVPort, LiveAPort: c.LiveAPort,
		Levels: c.Levels, LevelsPort: c.LevelsPort,
		Thumb: c.Thumb && c.ThumbFile != "", ThumbWidth: tw, ThumbHeight: th, ThumbEvery: c.ThumbEvery, ThumbFile: c.ThumbFile,
		Screen: screen,
	})
}

func splitSize(s string, dw, dh int) (int, int) {
	var w, h int
	if n, _ := fmt.Sscanf(s, "%dx%d", &w, &h); n == 2 {
		return w, h
	}
	return dw, dh
}

// ErrProgram is a program pipeline that cannot be built or does not start: the engine ends (exit 1)
// and systemd restarts it, like engine-run.py.
var ErrProgram = errors.New("program pipeline")

// Start builds the program and its bridges, the stills, the thumbnails and the socket, and finds the
// sources. The program's base_time is the common timeline of every pipeline that shares it.
func (e *Engine) Start(withScreen bool) error {
	tStart := time.Now()
	var phases []any // name, ms since Start: the start_timings event (M6, ENGINE_GO.md §5: start to multiview)
	mark := func(name string) { phases = append(phases, name, time.Since(tStart).Milliseconds()) }
	if e.cfg.MixerAudioSlots > 0 {
		// The I2S4 to the capture ADMAIF: the «HDMI de la placa» audio input needs it
		audio.MuxI2S(e.run, "", e.cfg.ALSACard, e.cfg.ALSAAdmaif)
	}
	desc := e.programDesc(withScreen)
	pgm, err := e.media.Parse("program", desc)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProgram, err)
	}
	e.pgm = pgm
	e.comp = pgm.ByName("pgm")
	if e.comp == nil {
		return fmt.Errorf("%w: no compositor pgm", ErrProgram)
	}
	pgm.FixBase(e.media.Now())
	e.anim = e.media.NewAnimator(e.comp, e.npads(), time.Second/time.Duration(max(1, e.fps)))
	e.bridges = make([]gst.Bridge, e.nslots)
	for i := range e.bridges {
		src := pgm.ByName(fmt.Sprintf("m%d", i))
		if src == nil {
			return fmt.Errorf("%w: no appsrc m%d", ErrProgram, i)
		}
		b := e.media.NewBridge(gst.BridgeSource, pgm, src)
		b.SetParams(gst.BridgeParams{Sync: e.sync})
		e.bridges[i] = b
	}
	if mp := pgm.ByName("mp"); mp != nil {
		e.pauseBridge = e.media.NewBridge(gst.BridgeSource, pgm, mp)
		e.pauseBridge.SetParams(gst.BridgeParams{Sync: e.sync})
	} else {
		e.pauseSlot = scene.NoPauseSlot
	}
	if cad := pgm.ByName("cadence"); cad != nil {
		e.cadence = e.media.NewCadence(cad)
	}
	e.cadValve = pgm.ByName("cadvalve")
	if v := pgm.ByName("vsrc"); v != nil {
		e.vsrc = e.media.NewCounter(v, "src")
	}
	for _, n := range []string{"livevalve", "liveavalve"} {
		if v := pgm.ByName(n); v != nil {
			e.liveValves = append(e.liveValves, v)
		}
	}
	e.startAudio(pgm)
	e.applyScene() // before PLAYING: no slot visible until there are sources
	if err := pgm.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("%w: does not start", ErrProgram)
	}
	e.playing = true
	mark("program_ms")
	if e.audio != nil {
		e.audio.Start(e.scene.Pause.On && e.scene.Pause.Mute)
	}
	e.startOutput(pgm)
	go e.watchProgram()
	mark("audio_output_ms")

	t0 := time.Now()
	if e.slate, err = e.still(pipeline.StillSlate, scene.SlateText); err != nil {
		return err
	}
	elog.Event("mixer", "slate_ready", "ms", time.Since(t0).Milliseconds())
	if e.black, err = e.still(pipeline.StillBlack, ""); err != nil {
		return err
	}
	for _, b := range e.bridges {
		b.PushStill(e.black)
	}
	if e.pauseBridge != nil {
		if e.pauseBuf, err = e.still(pipeline.StillPause, e.scene.Pause.Text); err != nil {
			return err
		}
		e.pauseBridge.PushStill(e.pauseBuf)
	}
	mark("stills_ms")
	e.applyScene()
	e.startThumbs()
	mark("thumbs_ms")
	e.startTCAudio()
	mark("tcaudio_ms")
	if withScreen {
		e.startScreen()
	}
	mark("screen_ms")
	if e.phones != nil {
		e.phones.Start() // waits for the screen to be stable before opening the HDMI
	}
	mark("phones_ms")
	e.discoverNow()
	mark("discover_ms")
	if e.runDir != "" {
		e.srv = server.New(server.Config{SocketPath: server.SocketPath(e.runDir), StatePath: server.StatePath(e.runDir)})
	} else {
		e.srv = server.New(server.Config{})
	}
	if e.runDir != "" {
		_ = e.srv.Listen() // logs socket_unavailable itself; the engine goes on without commands
	}
	e.publish()
	main := e.scene.Main
	if main == "" {
		main = "-"
	}
	elog.Event("mixer", "engine_started", "slots", e.nslots, "program", fmt.Sprintf("%dx%d@%d", e.width, e.height, e.fps), "on_air", main)
	mark("total_ms")
	elog.Event("mixer", "start_timings", phases...)
	return nil
}

// still builds a still image in GPU memory at the program size: the slate, black or the Pause with its
// text. The text is a property, never part of the description (the Pause text is typed by the user).
func (e *Engine) still(kind, text string) (gst.Buffer, error) {
	desc := pipeline.Still(kind, e.width, e.height, text != "") + e.plat.StillTail(e.width, e.height)
	p, err := e.media.Parse("still-"+kind, desc)
	if err != nil {
		return nil, fmt.Errorf("%w: still %s: %v", ErrProgram, kind, err)
	}
	defer p.Close()
	if text != "" {
		if txt := p.ByName("txt"); txt != nil {
			txt.Set("text", text)
		}
	}
	if err := p.SetState(gst.StatePlaying); err != nil {
		return nil, fmt.Errorf("%w: still %s does not start", ErrProgram, kind)
	}
	snk := p.ByName("snk")
	if snk == nil {
		return nil, fmt.Errorf("%w: still %s without appsink", ErrProgram, kind)
	}
	b := snk.PullBuffer(5 * time.Second)
	if b == nil {
		return nil, fmt.Errorf("%w: still %s gave no image", ErrProgram, kind)
	}
	return b, nil
}

// watchProgram ends the engine on an error of the program pipeline (engine-run.py: rc 1, systemd
// restarts it). Warnings are ignored, like there.
func (e *Engine) watchProgram() {
	for {
		select {
		case <-e.stopped:
			return
		default:
		}
		m := e.pgm.Pop(200*time.Millisecond, gst.MessageError|gst.MessageEOS|gst.MessageElement)
		if m == nil {
			continue
		}
		if m.Type == gst.MessageElement {
			if e.audio != nil {
				e.audio.OnProgramMessage(m) // pgmlevel: the program's peaks for the screen
			}
			continue
		}
		if m.Type == gst.MessageEOS {
			e.die(fmt.Errorf("%w: end of stream from %s", ErrProgram, m.Src))
			return
		}
		elog.Event("engine", "pipeline_error", "element", m.Src, "error", m.Text)
		e.die(fmt.Errorf("%w: error from %s: %s", ErrProgram, m.Src, m.Text))
		return
	}
}

func (e *Engine) die(err error) {
	select {
	case e.fatal <- err:
	default:
	}
}

// startThumbs: a JPEG of each source, one per second, in RUN_DIR/source-<slot>.jpg for the web. Its own
// pipeline with an appsrc per slot fed by the bridges; the JPEGs are written with a rename from Go.
func (e *Engine) startThumbs() {
	if e.runDir == "" {
		return
	}
	// Four VIC converters and JPEG encoders take ~3 s to build on the board: off the start, which they
	// held back (with the screen behind them; M6, ENGINE_GO.md §15.4). The web's thumbnails come a moment later.
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		p, err := e.media.Parse("thumbs", e.plat.Thumbs(e.nslots, e.width, e.height))
		if err == nil {
			if err = p.SetState(gst.StatePlaying); err != nil {
				p.Close()
			}
		}
		if err != nil {
			elog.Event("mixer", "thumbs_disabled", "err", err)
			return
		}
		e.thumbsMu.Lock()
		defer e.thumbsMu.Unlock()
		select {
		case <-e.stopped: // Stop has taken (or will take) no thumbs: close it here
			p.Close()
			return
		default:
		}
		e.thumbs = p
		for i, b := range e.bridges {
			b.SetThumb(p.ByName(fmt.Sprintf("th%d", i)), thumbEvery)
			snk := p.ByName(fmt.Sprintf("thj%d", i))
			path := filepath.Join(e.runDir, fmt.Sprintf("source-%d.jpg", i+1))
			e.wg.Add(1) // this goroutine still counts: never from zero
			go func() {
				defer e.wg.Done()
				for {
					select {
					case <-e.stopped:
						return
					default:
					}
					if jpg := snk.PullBytes(500 * time.Millisecond); jpg != nil {
						_ = sysx.WriteAtomic(path, jpg, 0o644)
					}
				}
			}()
		}
	}()
}

// --- the actor ---

// post runs fn on the actor (Python's idle_add).
func (e *Engine) post(fn func()) {
	select {
	case e.events <- fn:
	case <-e.stopped:
	}
}

// changed asks for a publish, from any goroutine; several in a row become one.
func (e *Engine) changed() {
	select {
	case e.changes <- struct{}{}:
	default:
	}
}

func (e *Engine) isHidden(id string) bool {
	e.hiddenMu.RLock()
	defer e.hiddenMu.RUnlock()
	return e.hidden[id]
}

func (e *Engine) syncHidden() {
	h := map[string]bool{}
	for _, id := range e.scene.Hidden() {
		h[id] = true
	}
	e.hiddenMu.Lock()
	e.hidden = h
	e.hiddenMu.Unlock()
}

// Run is the actor: commands, timers, discovery results and publishing, one at a time, until ctx ends
// or the program fails.
func (e *Engine) Run(ctx context.Context) error {
	slate := time.NewTicker(slateEvery)
	periodic := time.NewTicker(periodicEvery)
	discover := time.NewTicker(discoverEvery)
	meter := time.NewTicker(time.Second)
	defer slate.Stop()
	defer periodic.Stop()
	defer discover.Stop()
	defer meter.Stop()
	var fade *time.Ticker
	fadeC := func() <-chan time.Time {
		if fade == nil {
			return nil
		}
		return fade.C
	}
	for {
		fading := e.fading != nil || (e.audio != nil && e.audio.Fading())
		if fading && fade == nil {
			fade = time.NewTicker(fadeStep)
		} else if !fading && fade != nil {
			fade.Stop()
			fade = nil
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-e.fatal:
			return err
		case req := <-e.srv.Requests():
			if camera.IsCommand(req.Cmd) {
				e.cameraCommand(req.Cmd, req.Msg, req.Reply) // answered once the camera has taken it
			} else {
				req.Run(e.command)
			}
		case fn := <-e.events:
			fn()
		case <-e.changes:
			e.publish()
		case <-slate.C:
			e.slateTick()
		case <-periodic.C:
			e.periodic()
		case <-discover.C:
			e.discoverTick()
		case <-meter.C:
			e.videoMeter()
		case <-fadeC():
			e.fadeTick()
		}
	}
}

// slateTick: «Sin señal» in the slots whose source gives no image (also one that stops without an
// error, before its watchdog reopens it at 3 s). Here and not in the source's goroutine: opening a hung
// device blocks that goroutine for seconds. Black for a hidden source, or one opening for < 2 s.
func (e *Engine) slateTick() {
	now := e.media.Monotonic()
	for i, s := range e.sources {
		if s == nil {
			continue
		}
		state, since, _ := s.snapshot()
		last := e.bridges[i].Stats(false, false).LastFrame
		if state != StateLive || last == 0 || now-last > 500*time.Millisecond {
			black := state == StateHidden || (state == StateStarting && time.Since(since) < 2*time.Second)
			if black {
				e.bridges[i].PushStill(e.black)
			} else {
				e.bridges[i].PushStill(e.slate)
			}
		}
	}
	// Always, also when off: a pad without buffers would make the compositor wait at every frame
	if e.pauseBridge != nil {
		e.pauseBridge.PushStill(e.pauseBuf)
	}
}

// startTCAudio: the launcher's HDMI audio watcher (TC358743 PLL, I2S stalls). A stalled capture or a
// hardware reset to do ends the engine (exit 1): systemd restarts it and the reset happens at start.
func (e *Engine) startTCAudio() {
	c := e.cfg
	sys := tcaudio.Sys{Run: e.run}
	if !tcaudio.Enabled(sys, e.tc, c.ALSACard) {
		return
	}
	hs := hdmiSys(e.run)
	sys.Hotplug = func() bool { return hs.Replug(c.EDID) }
	sys.MayHotplug = e.mayReplugHDMI
	w := tcaudio.New(tcaudio.Config{I2CBus: c.TCI2C, LockTimeout: c.AudioLockTimeout, ResetInterval: c.HWResetInterval,
		StateDir: c.StateDir, ALSACard: c.ALSACard, ADMAIF: c.ALSAAdmaif, ResetPin: hs.HasResetPin(hs.FindSubdev())}, sys)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if err := w.Run(e.ctx); err != nil {
			e.die(err)
		}
	}()
}

// startAudio builds the audio bus on the program's aa0…aaN and amix (F4), and the headphones on phtap.
// Without audio slots, neither: the state has no `audio`, like the Python engine.
func (e *Engine) startAudio(pgm gst.Pipeline) {
	c := e.cfg
	if c.MixerAudioSlots == 0 {
		return
	}
	bus, err := audio.New(audio.Host{Media: e.media, Program: pgm, Net: e.net, Changed: e.changed},
		audio.Config{Slots: c.MixerAudioSlots, I2SDevice: c.I2SDevice(), ConfigMain: c.AudioMain(e.inALSA), Sync: e.sync, NetSync: e.netSync})
	if err != nil {
		elog.Event("audio", "audio_bus_unavailable", "err", err)
		return
	}
	e.audio = bus
	saved := bus.Load(e.extra)
	p, err := phones.New(phones.Options{Media: e.media, Program: pgm, Saved: saved, Changed: e.changed,
		CompositorLatency: e.latency, Run: e.run, Screen: e.phonesScreen,
		NewTap: func(m gst.Media, _ gst.Pipeline, phtap gst.Element) phones.Tap { return m.NewTap(phtap) }})
	if err == nil {
		e.phones = p
	}
}

// audioView is what the audio bus needs of the video side: each source's slot, kind and audio card,
// and what is on air.
func (e *Engine) audioView() audio.View {
	v := audio.View{Main: e.scene.Main}
	for i, s := range e.sources {
		if s == nil {
			continue
		}
		s.mu.Lock()
		v.Videos = append(v.Videos, audio.Video{ID: s.id, Slot: i + 1, Kind: s.kind, Card: s.audioCard})
		s.mu.Unlock()
	}
	return v
}

// startOutput builds the hot output on the program's outv/outa (F3): nothing goes out until it is
// switched on, at start when OUTPUT=on.
func (e *Engine) startOutput(pgm gst.Pipeline) {
	c := e.cfg
	prog := c.Program(e.fps)
	runDir := e.runDir
	if runDir == "" {
		runDir = os.TempDir() // the launcher's /tmp when RUN_DIR is not usable
	}
	desc := e.plat.Output(pipeline.Output{Width: e.width, Height: e.height, FPS: e.fps, VKbps: c.VBitrate, AKbps: c.ABitrate,
		GOP: prog.GOP, Profile: c.Profile, Preset: c.H264Preset, TwoPass: c.H264TwoPass, QPRange: c.H264QPRange,
		AEnc: c.AENC, Record: c.Record})
	o, err := output.New(output.Options{Config: c, Media: e.media, Program: pgm, Desc: desc, RunDir: runDir, OnChange: e.changed})
	if err != nil {
		elog.Event("engine", "output_unavailable", "err", err)
		return
	}
	e.out = o
	if c.OutputAtBoot {
		o.Set(true)
	}
}

// setOutput: emit or stop, live (F3). by: socket (the app, which also saves OUTPUT) or screen (the app
// sees it through by/seq and saves it).
func (e *Engine) setOutput(on bool, by string) (bool, string) {
	if e.out == nil {
		return false, "unsupported"
	}
	if e.out.Emitting() != on {
		e.outBy, e.outSeq = by, e.outSeq+1
		elog.Event("mixer", "output_toggled", "on", on, "by", by)
	}
	e.out.Set(on)
	e.publish()
	return true, ""
}

// hdmiID is the board's HDMI in the catalogue (probe).
const hdmiID = "hdmi"

// mayReplugHDMI: may the HDMI renegotiate now (the audio watcher's hotplug pulse, ≈2 s without its
// video)? Only when not broadcasting and with the HDMI not on the program. Asked from the watcher's
// goroutine; the actor answers, and no answer is a no.
func (e *Engine) mayReplugHDMI() bool {
	ch := make(chan bool, 1)
	e.post(func() { ch <- e.replugAllowed() })
	select {
	case ok := <-ch:
		return ok
	case <-time.After(2 * time.Second):
		return false
	case <-e.stopped:
		return false
	}
}

// replugAllowed is mayReplugHDMI's answer, on the actor.
func (e *Engine) replugAllowed() bool {
	if e.emitting() {
		return false
	}
	for _, id := range e.onAirIDs() {
		if id == hdmiID {
			return false
		}
	}
	return true
}

// emitting: the hot output wants to be on air.
func (e *Engine) emitting() bool { return e.out != nil && e.out.Emitting() }

// onAirFor is usb_on_air for source me while emitting (nil when not): what a power cut of its hub must
// not switch off. Asked from the source's goroutine; the actor answers.
func (e *Engine) onAirFor(me *Source) (map[string]usbhub.OnAir, error) {
	type answer struct {
		air map[string]usbhub.OnAir
	}
	ch := make(chan answer, 1)
	e.post(func() {
		if !e.emitting() {
			ch <- answer{}
			return
		}
		air := usbhub.Air{Program: usbhub.Program{Main: e.scene.Main, Secondary: e.scene.Secondary, Layout: e.scene.Layout}, Now: time.Now()}
		for _, s := range e.sources {
			if s == nil {
				continue
			}
			s.mu.Lock()
			air.Videos = append(air.Videos, usbhub.VideoSlot{ID: s.id, Name: s.name, Dev: s.usbDev, State: s.state, LastFrame: s.lastFrame})
			s.mu.Unlock()
		}
		if e.audio != nil {
			for _, in := range e.audio.Inputs() {
				air.Audios = append(air.Audios, usbhub.AudioSlot{ID: in.ID, Kind: in.Kind, Name: in.Name, State: in.State, Volume: in.Volume})
			}
		}
		me.mu.Lock()
		dev := me.usbDev
		me.mu.Unlock()
		ch <- answer{air.Devices(me.id, dev, e.hubs.Sys.DeviceOfCard)}
	})
	select {
	case a := <-ch:
		return a.air, nil
	case <-time.After(2 * time.Second):
		return nil, errors.New("engine does not answer")
	case <-e.stopped:
		return nil, errors.New("stopping")
	}
}

// Stop ends everything within stopTotal, under the unit's TimeoutStopSec=10: past it systemd kills the
// engine halfway and the camera and NVENC are not released cleanly. The sources (≤ stopBudget), the
// screen and the output close at the same time; the program pipeline goes last, with what is left.
func (e *Engine) Stop() {
	t0 := time.Now()
	end := t0.Add(stopTotal)
	e.cancel()
	close(e.stopped)
	for _, s := range e.sources {
		if s != nil {
			s.stop()
		}
	}
	if e.audio != nil {
		e.audio.Stop()
	}
	if e.phones != nil {
		e.phones.Stop() // lowers the volume and closes the HDMI in ≤ 3 s, at the same time as the rest
	}
	side := make(chan struct{}) // the screen and the output, in parallel with the sources
	go func() {
		defer close(side)
		if e.scr != nil {
			e.scr.Stop(2 * time.Second) // detaches its bridges before its pipeline closes
		}
		if e.appNet != nil {
			e.appNet.Stop()
		}
		if e.out != nil && !e.out.Close(min(stopBudget, time.Until(end)-3*time.Second)) {
			elog.Event("engine", "output_close_timeout")
		}
	}()
	until := t0.Add(stopBudget)
	for _, s := range e.sources {
		if s != nil {
			waitDone(s.done, time.Until(until))
		}
	}
	e.cams.Flush() // the last second of camera settings changed
	if e.audio != nil {
		e.audio.Wait(until)
	}
	phonesDone := e.phones == nil
	if e.phones != nil {
		phonesDone = waitDone(e.phones.Done(), time.Until(until))
	}
	srcSecs := time.Since(t0).Seconds()
	sideDone := waitDone(side, time.Until(end.Add(-3*time.Second)))
	waitGroupWithin(&e.wg, time.Until(end.Add(-2500*time.Millisecond))) // thumbnails, HDMI audio watcher
	if e.srv != nil {
		_ = e.srv.Close()
	}
	e.thumbsMu.Lock()
	thumbs := e.thumbs
	e.thumbsMu.Unlock()
	gst.CloseWithin(thumbs, min(500*time.Millisecond, time.Until(end.Add(-2*time.Second))))
	// The headphones' tap and the output's and screen's bridges can only go once nothing calls into them
	if gst.CloseWithin(e.pgm, time.Until(end)) {
		if phonesDone && e.phones != nil {
			e.phones.Free()
		}
		if sideDone && e.out != nil {
			e.out.Release()
		}
		if sideDone && e.mvBridge != nil {
			e.mvBridge.Free()
		}
		if e.anim != nil {
			e.anim.Free()
		}
	}
	elog.Event("mixer", "engine_stopped", "sources_s", fmt.Sprintf("%.1f", srcSecs), "total_s", fmt.Sprintf("%.1f", time.Since(t0).Seconds()))
}

// waitGroupWithin waits for wg at most d; true if it finished.
func waitGroupWithin(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	return waitDone(done, d)
}

// readFPS is RUN_DIR/video's FPS for measuredFps (null if missing or unreadable).
func readFPS(runDir string) *float64 {
	if runDir == "" {
		return nil
	}
	var f float64
	if _, err := fmt.Sscanf(sysx.ReadKV(filepath.Join(runDir, "video"))["FPS"], "%g", &f); err != nil {
		return nil
	}
	f = round1(f)
	return &f
}

func hdmiSys(run sysx.Runner) hdmi.Sys { return hdmi.Sys{Run: run, Sleep: time.Sleep} }
