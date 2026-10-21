// Package output is the encoder's hot output (MIXER.md §9, F3): emitting is hooking the output onto the
// program while it runs, and stopping is letting it go, without touching the program.
//
//	program ─ tee vt ─▶ appsink outv ┐  bridges   ┌─▶ FIFO ─▶ srt-live-transmit ─▶ SRT   (─▶ srtla_send ─▶ uplinks)
//	audio   ─ tee at ─▶ appsink outa ┘──(C)──▶ [output pipeline] ├─▶ FIFO ─▶ eirl-send ─▶ RTMP / RTMPS
//	                                       appsrc ov/oa ─▶       ├─▶ UDP ─▶ ristsender ─▶ RIST
//	                                       NVENC + AAC ─▶ TS     └─▶ UDP / RTP to the network
//
// The output pipeline runs on the program's clock and base_time: buffers arrive with their timestamps
// as they are, and audio and video stay together. Frames cross in C (gst.BridgePass); Go only opens,
// watches and closes. The pipeline (codec, size, rate), the sender of the protocol (OUTPUTS.md §4), the
// statistics for the app and the adaptive bitrate start and end with each emission, with the
// configuration of that moment: a new one (SetConfig) only restarts the session. If the destination does
// not answer or goes away, the output retries by itself; the program and the preview never notice.
//
// It is a port of scripts/engine_output.py (and the Controller of engine-run.py), which is the
// reference: the state machine, reasons, details, retry plans and timings are the same.
package output

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// Timings (engine_output.py). Variables so that the tests can shorten them.
var (
	unreachableAfter = 10 * time.Second // without connecting: the relay does not answer (srt-live-transmit retries silently)
	measureEvery     = 10 * time.Second // fps into and out of the encoder, and the 10 s «encoder produces nothing» rule
	busEvery         = 250 * time.Millisecond
	idleWake         = time.Second
	retryTick        = 500 * time.Millisecond
	longSession      = 60.0 // s: after a session this long, a failure counts as the first
	logEvery         = 30.0 // s between two equal lines of srt-live-transmit in the journal
	statsEvery       = 0.5  // s between two writes of srt-stats.json
	rttFresh         = 3.0  // s: an older RTT is not shown
	readerJoin       = time.Second
	pipelineClose    = 3 * time.Second
	uplinkPoll       = time.Second
)

// Programs, variables for the tests (which run fakes of them).
var (
	sltBin     = "srt-live-transmit"
	stdbufArgv = []string{"stdbuf", "-oL"} // srtla_send writes with printf: without a terminal, nothing until its buffer fills
)

const (
	detailMax     = 200
	trailingSpace = " \t\r\n\v\f" // Python's rstrip()
)

// Run files the session leaves (and removes at its end).
const (
	fileABR      = "abr"
	fileUplinks  = "uplinks"
	fileSRTLAIPs = "srtla-ips"
	fileSRTStats = "srt-stats.json"
)

// Options is what the output needs from the engine.
type Options struct {
	Config *config.Config
	Media  gst.Media
	// Program is the program pipeline, with the appsinks outv and outa. Its base_time must be set (it
	// is PLAYING) before the first session.
	Program gst.Pipeline
	// Desc builds the output pipeline of a session for its configuration and sink
	// (pipeline.Platform.Output); a fifo sink carries pipeline.FIFOPlaceholder.
	Desc func(c *config.Config, sink pipeline.Sink) string
	// Width, Height and FPS are the program's: the output's size and rate derive from them.
	Width, Height, FPS int
	// RunDir is where the FIFO and the run files go: RUN_DIR, or /tmp when it is not usable (the
	// launcher passed --run-dir only when it was a writable directory).
	RunDir string
	// OnChange is called when Info may have changed, from the output's goroutines. It must not block
	// and must not call back into the output synchronously.
	OnChange func()
}

// Info is the state's `output` object without by/seq, which the engine owns. Same keys and order as
// engine.OutputState (MIXER.md §5): the engine copies it field by field.
type Info struct {
	Mode     string   `json:"mode"`
	State    string   `json:"state"`
	Reason   string   `json:"reason,omitempty"`
	Detail   string   `json:"detail,omitempty"`
	Kbps     *int     `json:"kbps,omitempty"`
	InFPS    *float64 `json:"inFps,omitempty"`
	EncFPS   *float64 `json:"encFps,omitempty"`
	EncMbps  *float64 `json:"encMbps,omitempty"`
	SentKbps *int     `json:"sentKbps,omitempty"`
	RTTMs    *int     `json:"rttMs,omitempty"`
	// OUTPUTS.md §8: the protocol and codec of the configuration (also while off), the size and rate
	// while on
	Protocol string `json:"protocol,omitempty"`
	Codec    string `json:"codec,omitempty"`
	Size     string `json:"size,omitempty"`
	FPS      *int   `json:"fps,omitempty"`
}

// failure is why a session ended: a reason of the contract and a detail for the user (Spanish, as the
// Python engine: it is contract, translated by C5).
type failure struct{ reason, detail string }

func (f *failure) Error() string {
	if f.detail != "" {
		return f.detail
	}
	return f.reason
}

func fail(reason, detail string) error { return &failure{reason, detail} }

type rates struct{ inFPS, encFPS, encMbps float64 }

// Output is the hot output. One goroutine runs it (opens, watches, retries, closes): at most one live
// session at a time. Set from any goroutine.
type Output struct {
	media    gst.Media
	pgm      gst.Pipeline
	desc     func(*config.Config, pipeline.Sink) string
	width    int // the program's
	height   int
	fps      int
	runDir   string
	onChange func()

	// The configuration: each session takes the one of its start (snapshot); SetConfig swaps it and bumps
	// gen, which ends a live session so that the next one takes it.
	cfgMu   sync.Mutex
	cfg     *config.Config
	gen     int
	secrets []config.Secret // the configuration's secrets, to mask their values anywhere

	taps [2]gst.Element // outv, outa
	// One bridge per tap for the output's whole life, retargeted to each session's appsrcs (SetDst).
	// While not emitting it has no destination: frames are dropped in C and nothing piles up in the
	// appsink (the Python engine turned emit-signals off and drained the queue on each start, or audio
	// went ~0.5 s behind the video)
	bridges [2]gst.Bridge

	wake chan struct{}
	done chan struct{}

	// session is the session body; the tests replace it
	session func() error

	mu       sync.Mutex
	want     bool
	stopping bool
	started  bool
	state    string
	reason   string
	detail   string
	kbps     int
	rates    *rates
	lastErr  *failure // the last failure: still shown while retrying
	sent     *rateWindow
	rtt      float64
	rttAt    float64
	hasRTT   bool

	vencMu sync.Mutex
	venc   gst.Element // the live session's encoder, nil outside a session
}

// New prepares the output on the program. It fails if the program has no outv/outa taps.
func New(o Options) (*Output, error) {
	out := &Output{
		cfg: o.Config, media: o.Media, pgm: o.Program, desc: o.Desc, runDir: o.RunDir, onChange: o.OnChange,
		width: o.Width, height: o.Height, fps: o.FPS, secrets: o.Config.Secrets(),
		state: StateOff, kbps: o.Config.VBitrate, sent: newRateWindow(2),
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	if out.onChange == nil {
		out.onChange = func() {}
	}
	if out.runDir == "" {
		out.runDir = "/tmp"
	}
	out.session = out.runSession
	for i, name := range []string{"outv", "outa"} {
		snk := o.Program.ByName(name)
		if snk == nil {
			return nil, fmt.Errorf("the program has no %s: no hot output", name)
		}
		out.taps[i] = snk
	}
	for i := range out.taps {
		out.bridges[i] = o.Media.NewBridge(gst.BridgePass, o.Program, nil)
		out.bridges[i].Attach(out.taps[i])
	}
	survivePipes()
	return out, nil
}

// mask hides the secrets of a line: the stream id and passphrase parameters, and the values of every
// secret of the configuration (and of the previous one: a session may still log after a swap) wherever
// they appear.
func (o *Output) mask(s string) string {
	s = maskRIST(mask(s))
	o.cfgMu.Lock()
	secrets := o.secrets
	o.cfgMu.Unlock()
	for _, v := range secrets {
		s = strings.ReplaceAll(s, v.Reveal(), "***")
	}
	return s
}

// Config is the output's configuration now.
func (o *Output) Config() *config.Config {
	o.cfgMu.Lock()
	defer o.cfgMu.Unlock()
	return o.cfg
}

// SetConfig takes a new configuration (output_config, OUTPUTS.md §8): the next session uses it, and a live
// one ends at once to restart with it. From any goroutine.
func (o *Output) SetConfig(c *config.Config) {
	o.cfgMu.Lock()
	// The old secrets stay masked too: a session still running logs with them until it ends
	o.secrets = append(c.Secrets(), o.cfg.Secrets()...)
	o.cfg = c
	o.gen++
	o.cfgMu.Unlock()
	o.mu.Lock()
	if o.state != StateOn {
		o.kbps = c.VBitrate
	}
	o.mu.Unlock()
	elog.Event("engine", "output_config", "protocol", c.Transport, "codec", c.VCodec)
	o.onChange()
	o.poke()
}

// config is the configuration and its generation, for a session's start.
func (o *Output) config() (*config.Config, int) {
	o.cfgMu.Lock()
	defer o.cfgMu.Unlock()
	return o.cfg, o.gen
}

func (o *Output) genNow() int {
	o.cfgMu.Lock()
	defer o.cfgMu.Unlock()
	return o.gen
}

// --- state ---

func (o *Output) setState(state, reason, detail string) {
	detail = cut(o.mask(detail), detailMax)
	o.mu.Lock()
	same := state == o.state && reason == o.reason && detail == o.detail
	if !same {
		o.state, o.reason, o.detail = state, reason, detail
	}
	o.mu.Unlock()
	if !same {
		o.onChange()
	}
}

// Info is the output for the state. kbps, the rates, sentKbps and rttMs only while on.
func (o *Output) Info() Info {
	o.mu.Lock()
	defer o.mu.Unlock()
	i := Info{Mode: "off", State: o.state, Reason: o.reason, Detail: o.detail}
	if o.want {
		i.Mode = "on"
	}
	cfg := o.Config()
	i.Protocol, i.Codec = cfg.Transport, cfg.VCodec
	if o.state != StateOn {
		return i
	}
	w, h := cfg.OutputDims(o.width, o.height)
	if w > 0 && h > 0 {
		i.Size = strconv.Itoa(w) + "x" + strconv.Itoa(h)
	}
	if o.fps > 0 {
		f := o.fps
		if cfg.OutputHalf {
			f = max(1, f/2)
		}
		i.FPS = &f
	}
	k := o.kbps
	i.Kbps = &k
	if r := o.rates; r != nil {
		in, enc, mb := r.inFPS, r.encFPS, r.encMbps
		i.InFPS, i.EncFPS, i.EncMbps = &in, &enc, &mb
	}
	now := mono()
	if s, ok := o.sent.kbps(now); ok {
		i.SentKbps = &s // what really leaves through the network, 2 s average
	}
	if o.hasRTT && now-o.rttAt < rttFresh {
		r := int(math.RoundToEven(o.rtt)) // Python's int(round(rtt))
		i.RTTMs = &r
	}
	return i
}

// Emitting is whether emission is wanted (the mode; the USB recovery asks before cutting a hub).
func (o *Output) Emitting() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.want
}

// Set starts (on) or stops emitting. It returns at once: the output's goroutine does the work.
func (o *Output) Set(on bool) {
	o.mu.Lock()
	if o.stopping {
		o.mu.Unlock()
		return
	}
	if on != o.want {
		code := "output_off_requested"
		if on {
			code = "output_on_requested"
		}
		elog.Event("engine", code)
	}
	o.want = on
	start := on && !o.started
	if start {
		o.started = true
	}
	o.mu.Unlock()
	if start {
		go o.run()
	}
	o.poke()
}

func (o *Output) poke() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Close lets the output go when the engine stops: the session and its processes end within budget
// (the Python engine joined for 4 s). It reports whether the goroutine finished.
func (o *Output) Close(budget time.Duration) bool {
	o.mu.Lock()
	o.stopping, o.want = true, false
	started := o.started
	o.mu.Unlock()
	o.poke()
	if !started {
		return true
	}
	select {
	case <-o.done:
		return true
	case <-time.After(budget):
		elog.Event("engine", "output_close_timeout", "budget_s", budget.Seconds())
		return false
	}
}

// Release frees the bridges. Only once the output is closed and the program pipeline is in NULL
// (its appsinks still call into them until then).
func (o *Output) Release() {
	for i, b := range o.bridges {
		if b != nil {
			b.Free()
			o.bridges[i] = nil
		}
	}
}

func (o *Output) flags() (want, stopping bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.want, o.stopping
}

func (o *Output) running() bool {
	w, s := o.flags()
	return w && !s
}

// --- the loop ---

func (o *Output) run() {
	defer close(o.done)
	fails := 0
	for {
		want, stopping := o.flags()
		if stopping {
			return
		}
		if !want {
			o.mu.Lock()
			wasOn := o.state != StateOff
			if wasOn { // stopped while waiting for a retry: no failure to show
				o.lastErr = nil
			}
			o.mu.Unlock()
			if wasOn {
				o.setState(StateOff, "", "")
				fails = 0
			}
			o.sleep(idleWake)
			continue
		}
		t0 := mono()
		err := o.safeSession()
		if errors.Is(err, errReconfig) && o.running() {
			// A new configuration: straight to the next session, which takes it (not a failure)
			o.mu.Lock()
			o.lastErr = nil
			o.mu.Unlock()
			fails = 0
			continue
		}
		if !o.running() {
			o.mu.Lock()
			o.lastErr = nil
			o.mu.Unlock()
			o.setState(StateOff, "", "")
			fails = 0
			continue
		}
		var f *failure
		if !errors.As(err, &f) {
			if err == nil { // the session ended without a known reason
				f = &failure{ReasonConnectionLost, "la conexión se ha cerrado"}
			} else {
				f = &failure{ReasonInternal, err.Error()}
			}
		}
		f.detail = cut(o.mask(f.detail), detailMax)
		o.mu.Lock()
		o.lastErr = f
		o.mu.Unlock()
		// After a long session the failure counts as the first (it used to be 0: the longest wait)
		if mono()-t0 > longSession {
			fails = 1
		} else {
			fails++
		}
		state, wait := retryPlan(f.reason, fails)
		elog.Event("engine", "output_retry", "reason", f.reason, "detail", f.detail, "wait_s", int(wait.Seconds()))
		o.setState(state, f.reason, f.detail)
		o.drainWake()
		end := time.Now().Add(wait)
		for o.running() && time.Now().Before(end) {
			o.sleep(min(retryTick, time.Until(end)))
		}
	}
}

func (o *Output) sleep(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-o.wake:
	case <-t.C:
	}
}

func (o *Output) drainWake() {
	select {
	case <-o.wake:
	default:
	}
}

// safeSession runs a session; a panic in it cannot take the engine (and the preview) down.
func (o *Output) safeSession() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fail(ReasonInternal, fmt.Sprint(r))
		}
	}()
	return o.session()
}

func mono() float64 { return time.Since(epoch).Seconds() }

var epoch = time.Now()

// round is Python's round(x, n) for a measurement (ties may differ, which is harmless).
func round(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(x*p) / p
}

// --- a session ---

// errReconfig ends a session whose configuration was replaced (SetConfig).
var errReconfig = errors.New("output configuration replaced")

// result is what the helpers of a session learnt: why it should end, when a process does.
type result struct {
	mu        sync.Mutex
	reason    string // from srt-live-transmit's lines
	detail    string
	srtla     *failure // what srtla_send said when giving up
	noUplinks *failure // the last uplink went away
}

func (r *result) setConn(reason, detail string) {
	r.mu.Lock()
	r.reason, r.detail = reason, detail
	r.mu.Unlock()
}

func (r *result) setSRTLA(reason, detail string) {
	r.mu.Lock()
	r.srtla = &failure{reason, detail}
	r.mu.Unlock()
}

func (r *result) setNoUplinks(reason, detail string) {
	r.mu.Lock()
	r.noUplinks = &failure{reason, detail}
	r.mu.Unlock()
}

// why is the failure when a process ended: without uplinks (watchUplinks stopped srtla_send) wins,
// whichever process died first; then what srtla_send said when giving up; else srt-live-transmit's.
func (r *result) why(c *child, isSRTLA bool) *failure {
	r.mu.Lock()
	defer r.mu.Unlock()
	var f failure
	switch {
	case r.noUplinks != nil:
		f = *r.noUplinks
	case isSRTLA && r.srtla != nil:
		f = *r.srtla
	default:
		f = failure{r.reason, r.detail}
	}
	if f.reason == "" {
		f.reason = ReasonConnectionLost
	}
	if f.detail == "" {
		f.detail = fmt.Sprintf("%s ha terminado (%d)", c.name, c.code)
	}
	return &f
}

// session is one emission: its processes, its pipeline and the goroutines that read them.
type session struct {
	o       *Output
	cfg     *config.Config // the configuration of its start
	gen     int
	res     *result
	procs   []*child
	slt     *child
	srtla   *child
	readers sync.WaitGroup
	srtlaRd chan struct{} // closed when srtla_send's reader has read its last line
	ready   *oneShot      // the SRTLA sender registered its uplinks with the relay
	statsUp *oneShot      // the Rust sender's statistics are coming (so ready can be trusted to come too)
	stop    chan struct{} // closed by teardown: the helpers that are not readers of a pipe end
	files   []string
	pipe    gst.Pipeline
	feeding bool   // the bridges send to this session's appsrcs
	frames  uint64 // the video bridge's frame count at the last measure
	counter gst.Counter
}

func (o *Output) runSession() error {
	cfg, gen := o.config()
	if !cfg.HasDestination() {
		if cfg.Transport == config.ProtoSRT || cfg.Transport == config.ProtoSRTLA {
			return fail(ReasonNoDestination, "falta el destino SRT")
		}
		return fail(ReasonNoDestination, "falta el destino")
	}
	o.mu.Lock()
	prev := o.lastErr
	o.mu.Unlock()
	if prev != nil {
		o.setState(StateConnecting, prev.reason, prev.detail)
	} else {
		o.setState(StateConnecting, "", "")
	}
	s := &session{o: o, cfg: cfg, gen: gen, res: &result{}}
	defer s.teardown()
	switch cfg.Transport {
	case config.ProtoRTMP, config.ProtoRTMPS:
		return s.runSend()
	case config.ProtoRIST:
		return s.runRIST()
	case config.ProtoUDP:
		return s.runUDP()
	}
	// srt names the relay in its target; srtla's goes to its sender (startSRTLA)
	addr := ""
	if host := cfg.RelayHost(); host != "" && cfg.Transport == config.ProtoSRT {
		if a, ok := relayAddr(host, cfg.StateDir); ok {
			addr = a
		}
	}
	return s.run(cfg.SRTTargetAt(addr))
}

// fifo makes the session's FIFO and opens it O_RDWR: opening a FIFO like this waits for nobody, and the
// sender never sees EOF from a writer that has not arrived yet. The caller keeps the descriptor open until
// the filesink has opened the FIFO for writing: if the sender died at once, that open would wait for ever.
func (s *session) fifo() (string, *os.File, error) {
	fifo := filepath.Join(s.o.runDir, fmt.Sprintf("eirl-engine-out.%d.ts", os.Getpid()))
	_ = os.Remove(fifo)
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		return "", nil, fail(ReasonInternal, "mkfifo: "+err.Error())
	}
	s.files = append(s.files, fifo)
	fd, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		return "", nil, fail(ReasonInternal, "fifo: "+err.Error())
	}
	return fifo, fd, nil
}

// statsPipe is a pipe for a sender's statistics (its fd 3), when someone wants them: the app (SRT_STATS)
// or the ABR. The read end goes to readStats; the caller closes the write end once the child has it.
func (s *session) statsPipe() (r, w *os.File, err error) {
	if s.cfg.SRTStats == "" && !s.cfg.ABR {
		return nil, nil, nil
	}
	r, w, err = os.Pipe()
	if err != nil {
		return nil, nil, fail(ReasonInternal, "pipe: "+err.Error())
	}
	return r, w, nil
}

func (s *session) run(target config.Secret) error {
	o, cfg := s.o, s.cfg
	if cfg.Transport == config.ProtoSRTLA {
		if err := s.startSRTLA(); err != nil {
			return err
		}
		if err := s.waitRegistered(); err != nil {
			return err
		}
	}
	fifo, fd, err := s.fifo()
	if err != nil {
		return err
	}
	defer fd.Close()
	args := []string{"-chunk:" + strconv.Itoa(cfg.SRTPayloadBytes())}
	var extra []*os.File
	statsR, statsW, err := s.statsPipe()
	if err != nil {
		return err
	}
	if statsW != nil {
		defer statsW.Close()
		extra = []*os.File{statsW}
		kbps, perSecond := cfg.VBitrate, 1
		if cfg.ABR {
			kbps, perSecond = cfg.VBitrateMin, 4
		}
		args = append(args, "-s:"+strconv.Itoa(statsInterval(kbps, cfg.ABitrate, perSecond, cfg.SRTPayloadBytes())), "-pf:json",
			"-statsout:/dev/fd/3")
	}
	// The target holds the stream id and the passphrase: it goes to srt-live-transmit's argv because it
	// has no other way to take it (as with the Python engine), and nowhere else
	args = append(args, "file://con", target.Reveal())
	slt, stderr, err := startChild("srt-live-transmit", sltBin, args, fd, false, extra, nil)
	if err != nil {
		if statsR != nil {
			statsR.Close()
		}
		return fail(ReasonInternal, err.Error())
	}
	for _, w := range extra {
		w.Close()
	}
	s.slt = slt
	s.procs = append(s.procs, slt)
	s.goRead(func() { o.readSLT(stderr, s.res) }, stderr)
	if statsR != nil {
		s.goRead(func() { o.readStats(cfg, statsR) }, statsR)
	}
	if err := s.startPipeline(pipeline.Sink{}, fifo); err != nil {
		return err
	}
	fd.Close()
	return s.watch()
}

// goRead runs a reader of a process' pipe; teardown waits for it.
func (s *session) goRead(fn func(), f *os.File) {
	s.readers.Add(1)
	go func() {
		defer s.readers.Done()
		defer f.Close()
		fn()
	}()
}

// watch waits until stopping is asked or something falls. A cut with the relay does NOT end the
// session: srt-live-transmit reconnects by itself and the output stays «connecting» with the reason.
func (s *session) watch() error {
	o := s.o
	t0 := mono()
	tMeas := t0
	for o.running() {
		if o.genNow() != s.gen {
			return errReconfig
		}
		if now := mono(); now-tMeas >= measureEvery.Seconds() {
			on := s.measure(now - tMeas)
			tMeas = mono()
			if on {
				// NVENC or the mux stopped with SRT connected: the screen would keep saying «EN
				// DIRECTO». Only connected: while connecting, srt-live-transmit does not read and the
				// mux waits
				return fail(ReasonEncoderError, "el codificador lleva 10 s sin producir")
			}
		}
		if m := s.pipe.Pop(busEvery, gst.MessageError); m != nil {
			dbg := m.Debug
			if i := strings.LastIndexByte(strings.TrimRight(dbg, "\n"), '\n'); i >= 0 {
				dbg = dbg[i+1:]
			}
			elog.Event("engine", "output_encoder_error", "element", m.Src, "error", o.mask(m.Text),
				"debug", cut(o.mask(strings.TrimSpace(dbg)), 300))
			return fail(ReasonEncoderError, m.Text)
		}
		for _, c := range s.procs {
			if !c.exited() {
				continue
			}
			isSRTLA := c == s.srtla
			if isSRTLA && s.srtlaRd != nil {
				// Its last line, the one that says why
				select {
				case <-s.srtlaRd:
				case <-time.After(readerJoin):
				}
			}
			return s.res.why(c, isSRTLA)
		}
		o.mu.Lock()
		silent := o.state == StateConnecting && o.reason == ""
		o.mu.Unlock()
		if silent && mono()-t0 > unreachableAfter.Seconds() {
			o.setState(StateConnecting, ReasonRelayUnreachable, "el relay no responde")
		}
	}
	return nil
}

// measure is, every 10 s with the output hooked: fps into and out of the encoder and the real Mbit/s.
// If fewer go in than the program's, frames are lost before encoding (and the bitrate drops). It
// reports whether the output is on with an encoder that produced nothing.
func (s *session) measure(dt float64) bool {
	o := s.o
	var nIn, nEnc, bytes uint64
	if s.feeding {
		f := o.bridges[0].Stats(false, false).Frames
		nIn, s.frames = f-s.frames, f
	}
	if s.counter != nil {
		nEnc, bytes = s.counter.Take()
	}
	r := &rates{inFPS: round(float64(nIn)/dt, 1), encFPS: round(float64(nEnc)/dt, 1),
		encMbps: round(float64(bytes)*8/dt/1e6, 2)}
	o.mu.Lock()
	o.rates = r
	stuck := o.state == StateOn && r.encFPS == 0
	o.mu.Unlock()
	elog.Event("engine", "output_rates", "in_fps", r.inFPS, "enc_fps", r.encFPS, "enc_mbps", r.encMbps)
	return stuck
}

// startPipeline builds and plays the session's pipeline, sending to sink (fifo: to the FIFO's path).
func (s *session) startPipeline(sink pipeline.Sink, fifo string) error {
	o, cfg := s.o, s.cfg
	p, err := o.media.Parse("output", strings.ReplaceAll(o.desc(cfg, sink), pipeline.FIFOPlaceholder, fifo))
	if err != nil {
		return fail(ReasonEncoderError, "no se puede construir la salida: "+err.Error())
	}
	s.pipe = p
	// The program's clock and base_time: buffers arrive with their timestamps as they are
	p.FixBase(o.pgm.BaseTime())
	if venc := p.ByName("venc"); venc != nil {
		// VBITRATE, which is also the ABR's maximum: the controller starts there
		venc.Set("bitrate", strconv.Itoa(cfg.VBitrate*1000))
		s.counter = o.media.NewCounter(venc, "src")
		o.vencMu.Lock()
		o.venc = venc
		o.vencMu.Unlock()
	}
	if err := p.SetState(gst.StatePlaying); err != nil {
		detail := "la salida no arranca"
		if m := p.Pop(0, gst.MessageError); m != nil {
			detail = m.Text
		}
		return fail(ReasonEncoderError, detail)
	}
	// The frames count across sessions: this one's start from here
	s.frames = o.bridges[0].Stats(false, false).Frames
	every := 1
	if cfg.OutputHalf {
		every = 2 // half the program's rate (OUTPUTS.md §7): the bridge drops one frame of two
	}
	o.bridges[0].SetEvery(every)
	for i, name := range []string{"ov", "oa"} {
		o.bridges[i].SetDst(p.ByName(name)) // nil (no such appsrc) drops
	}
	s.feeding = true
	return nil
}

// teardown ends the session: nothing more into the output, the processes stopped (their death also
// unblocks a filesink writing to a full FIFO), the pipeline closed, the readers joined and the run
// files removed.
func (s *session) teardown() {
	o := s.o
	if s.feeding {
		for _, b := range o.bridges {
			b.SetDst(nil)
		}
	}
	o.vencMu.Lock()
	o.venc = nil
	o.vencMu.Unlock()
	o.mu.Lock()
	o.rates = nil
	o.mu.Unlock()
	if s.stop != nil {
		close(s.stop)
	}
	stopChildren(s.procs)
	if s.pipe != nil {
		closed := make(chan struct{})
		go func() {
			s.pipe.Close()
			close(closed)
		}()
		select {
		case <-closed:
			// Its probe goes once the pipeline is in NULL (a stuck pipeline keeps it: a few bytes)
			if s.counter != nil {
				s.counter.Free()
			}
		case <-time.After(pipelineClose):
			elog.Event("engine", "output_close_stuck", "after_s", pipelineClose.Seconds())
		}
	}
	joined := make(chan struct{})
	go func() {
		s.readers.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(readerJoin):
		elog.Event("engine", "output_readers_stuck")
	}
	for _, f := range s.files {
		_ = os.Remove(f)
	}
	for _, n := range []string{fileABR, fileUplinks, fileSRTLAIPs, fileSRTStats, fileSRTLALinks, fileSRTLASock} {
		_ = os.Remove(filepath.Join(o.runDir, n))
	}
	// What the session's pipeline freed stays in glibc's arenas: each reconnection added ~1 MB of RSS
	// (ENGINE_GO.md §14)
	gst.TrimMalloc()
}

// --- srt-live-transmit ---

// readSLT sends its stderr to the journal (masked, without the «bytes lost» it repeats every 5 s
// without a relay, and without repeating a line more than once every logEvery s: a rejection gives
// dozens per second) and follows the connection's state.
func (o *Output) readSLT(r io.Reader, res *result) {
	seen := map[string]float64{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		line := o.mask(strings.TrimRight(sc.Text(), trailingSpace))
		if line == "" {
			continue
		}
		key, now := normalize(line), mono()
		last, ok := seen[key]
		if !strings.Contains(line, " bytes lost, ") && (!ok || now-last >= logEvery) {
			seen[key] = now
			elog.Event("engine", sltEvent(line), "line", line)
		}
		switch st, reason := classify(line); st {
		case connConnected:
			res.setConn("", "")
			o.mu.Lock()
			o.lastErr = nil
			o.mu.Unlock()
			o.setState(StateOn, "", "")
		case connError:
			// Without the time or the socket: each line of libsrt would change the state and drag a
			// publish and a redraw of the screen (a rejection gives dozens per second)
			d := cut(normalize(line), detailMax)
			res.setConn(reason, d)
			o.setState(StateConnecting, reason, d)
		}
	}
}

// sltEvent is the journal code of a line of srt-live-transmit (internal/service/journal.go).
func sltEvent(line string) string {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "target disconnected"):
		return "srt_disconnected"
	case strings.Contains(l, "target connected"), strings.Contains(l, "accepted srt target connection"):
		return "srt_connected"
	}
	return "srt_log"
}

// readStats reads a sender's JSON statistics (srt-live-transmit's shape, OUTPUTS.md §4) and hands each
// one to the session's statistics.
func (o *Output) readStats(cfg *config.Config, r io.Reader) {
	st := o.newStats(cfg)
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line == "" && err != nil {
			return
		}
		smp, ok := parseSample(line)
		st.take([]byte(line), smp, ok)
	}
}

// stats is a session's statistics: the last line for the app (SRT_STATS, through a rename, at most every
// 0.5 s), the real bitrate and the RTT for the state and, with ABR, the adaptive bitrate.
type stats struct {
	o       *Output
	cfg     *config.Config
	ctl     *controller
	lastOut float64
	logged  map[string]float64
}

func (o *Output) newStats(cfg *config.Config) *stats {
	st := &stats{o: o, cfg: cfg, lastOut: -1e9, logged: map[string]float64{"severe": -1e9, "congestion": -1e9}}
	if cfg.ABR {
		st.ctl = newController(cfg.VBitrateMin, cfg.VBitrate, cfg.ABRLatencyMs())
		o.mu.Lock()
		o.kbps = st.ctl.kbps
		o.mu.Unlock()
		o.writeABR(st.ctl)
	}
	return st
}

// take is one statistic: line is what goes to SRT_STATS, smp what it says (ok false: nothing usable).
func (st *stats) take(line []byte, smp sample, ok bool) {
	o, cfg, ctl := st.o, st.cfg, st.ctl
	now := mono()
	if cfg.SRTStats != "" && now-st.lastOut >= statsEvery {
		if sysx.WriteAtomic(cfg.SRTStats, line, 0o644) == nil {
			st.lastOut = now
		}
	}
	if !ok || !smp.hasBytes {
		return
	}
	o.mu.Lock()
	o.sent.add(now, smp.bytes)
	o.rtt, o.rttAt, o.hasRTT = smp.rtt, now, true
	o.mu.Unlock()
	if ctl == nil {
		return
	}
	prev := ctl.kbps
	next, changed := ctl.update(now, smp.rtt, smp.bufMs, smp.dropped)
	if !changed {
		return
	}
	o.setBitrate(next, ctl)
	why := ctl.lastReason
	every := 30.0
	if why.Kind == "severe" {
		every = 10
	}
	if next < prev && now-st.logged[why.Kind] >= every {
		elog.Event("abr", "abr_down", "from_kbps", prev, "to_kbps", next, "cause", why.Kind,
			"buffer_ms", int(why.BufMs), "rtt_ms", int(why.RTTMs), "base_ms", int(why.BaseMs), "dropped", why.Dropped)
		st.logged[why.Kind] = now
	} else if next == ctl.max && prev < next {
		elog.Event("abr", "abr_max", "kbps", next)
	}
}

// setBitrate changes the encoder's bitrate (nvv4l2h264enc takes it in PLAYING without an IDR) and
// leaves the value for the app.
func (o *Output) setBitrate(kbps int, ctl *controller) {
	o.vencMu.Lock()
	if o.venc != nil {
		o.venc.Set("bitrate", strconv.Itoa(kbps*1000))
	}
	o.vencMu.Unlock()
	o.mu.Lock()
	o.kbps = kbps
	o.mu.Unlock()
	o.writeABR(ctl)
	// No publish of its own (as in Python): the engine's periodic publish carries it; with a bad
	// network the controller moves twice a second
}

func (o *Output) writeABR(ctl *controller) {
	_ = sysx.WriteAtomic(filepath.Join(o.runDir, fileABR),
		[]byte(fmt.Sprintf("KBPS=%d\nMIN=%d\nMAX=%d\n", ctl.kbps, ctl.min, ctl.max)), 0o644)
}

// LinkInfo is what the header says of the link, for the screen to word in its language: the protocol (srt,
// srtla, rtmp, rtmps, rist, udp) and, with SRTLA, the uplinks the output would use now (without the disabled
// ones nor the wifi without internet; 0 for the others, which do not bond). From any goroutine.
func (o *Output) LinkInfo() (transport string, links int) {
	cfg := o.Config()
	if cfg.Transport != config.ProtoSRTLA {
		return cfg.Transport, 0
	}
	return config.ProtoSRTLA, len(o.uplinkFiles(cfg).current())
}

// --- SRTLA ---

func (o *Output) uplinkFiles(c *config.Config) uplinkFiles {
	return uplinkFiles{runDir: o.runDir, available: c.UplinksFile, disabled: c.UplinksDisabled, nonet: c.UplinksNoInternet}
}

// startSRTLA starts srtla_send with the uplinks of now (waiting for one, up to UPLINK_WAIT) and a
// goroutine that passes it those that come or go with SIGHUP, without cutting.
func (s *session) startSRTLA() error {
	o, cfg := s.o, s.cfg
	uf := o.uplinkFiles(cfg)
	sel := uf.current()
	if len(sel) == 0 {
		elog.Event("engine", "srtla_waiting_uplink", "file", cfg.UplinksFile, "wait_s", cfg.UplinkWait)
		o.setState(StateConnecting, ReasonNoUplinks, "esperando un enlace a internet")
		end := time.Now().Add(time.Duration(cfg.UplinkWait) * time.Second)
		for len(sel) == 0 && o.running() && time.Now().Before(end) {
			o.sleep(min(uplinkPoll, time.Until(end)))
			sel = uf.current()
		}
		if len(sel) == 0 {
			return fail(ReasonNoUplinks, "sin enlaces a internet")
		}
	}
	addr, _ := relayAddr(cfg.SRTLAHost(), cfg.StateDir) // the name itself if it does not resolve: the sender tries
	snd, err := pickSender(cfg, o.runDir, addr, uf.ipsPath())
	if err != nil {
		return fail(ReasonInternal, err.Error())
	}
	uf.write(sel)
	elog.Event("engine", "srtla_uplinks", "links", uplinksDesc(sel))
	if len(sel) < 2 {
		elog.Event("engine", "srtla_single_uplink", "note", "no bonding: if it goes, the emission goes")
	}
	if snd.sock != "" {
		_ = os.Remove(snd.sock) // a socket left by a sender that was killed
	}
	if snd.kind == senderIRLServer {
		elog.Event("engine", "srtla_sender", "sender", snd.kind, "mode", cfg.SRTLAMode, "relay", addr)
	} else {
		elog.Event("engine", "srtla_sender", "sender", snd.kind, "relay", addr)
	}
	p, out, err := startChild("srtla_send", snd.bin, snd.argv, nil, true, nil, snd.env)
	if err != nil {
		return fail(ReasonInternal, err.Error())
	}
	s.srtla = p
	s.procs = append(s.procs, p)
	s.ready, s.statsUp = newOneShot(), newOneShot()
	s.srtlaRd = make(chan struct{})
	ready, statsUp := s.ready, s.statsUp
	s.goRead(func() {
		defer close(s.srtlaRd)
		o.readSRTLA(out, s.res, ready, p.Terminate)
	}, out)
	s.readers.Add(1)
	go func() {
		defer s.readers.Done()
		watchUplinks(uf, p, sel, s.res)
	}()
	if snd.sock != "" {
		s.stop = make(chan struct{})
		done := s.stop
		s.readers.Add(1)
		go func() {
			defer s.readers.Done()
			o.followStats(snd.sock, cfg.SRTLAMode, mergeDone(done, p.Done()), ready, statsUp)
		}()
	}
	return nil
}

// mergeDone closes when either closes.
func mergeDone(a, b <-chan struct{}) <-chan struct{} {
	c := make(chan struct{})
	go func() {
		select {
		case <-a:
		case <-b:
		}
		close(c)
	}()
	return c
}

// srtlaDeadWait is how long the Rust sender may say it has lost every uplink before the engine stops it (SRTLA.md
// §9.1). A variable for the tests.
var srtlaDeadWait = 60 * time.Second

var reDigits = regexp.MustCompile(`[0-9]+`)

// readSRTLA sends srtla_send's output to the journal and keeps why it gives up. The C sender's «connection
// established» is its registration (the Rust one says so in its statistics, followStats).
//
// Every line is classified, but the journal gets each kind of line (its numbers aside) at most once every logEvery
// s, with how many were left out: the Rust sender prints a warning per NAK, and per packet while it has no uplink.
// And it never exits when it has lost every uplink (it says «Failed to re-establish any connections» every second
// and keeps trying, which saves rebuilding the session after a short cut): after srtlaDeadWait of that, giveUp stops
// it, so that the session ends and the next one resolves the relay's name again, as the C sender's exit does.
func (o *Output) readSRTLA(r io.Reader, res *result, ready *oneShot, giveUp func()) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	type seen struct {
		last float64
		n    int
	}
	logged := map[string]*seen{}
	var deadSince, deadLast float64
	gaveUp := false
	for sc.Scan() {
		line := o.mask(reSRTLAAddr.ReplaceAllString(plainSRTLA(strings.TrimRight(sc.Text(), trailingSpace)), ""))
		if line == "" {
			continue
		}
		now := mono()
		key := reDigits.ReplaceAllString(line, "#")
		if len(logged) > 256 {
			logged = map[string]*seen{}
		}
		if e, ok := logged[key]; !ok || now-e.last >= logEvery {
			if ok && e.n > 0 {
				elog.Event("srtla", srtlaEvent(line), "line", line, "repeats", e.n)
			} else {
				elog.Event("srtla", srtlaEvent(line), "line", line)
			}
			logged[key] = &seen{last: now}
		} else {
			e.n++
		}
		if strings.HasSuffix(line, ": connection established") {
			ready.fire()
		}
		if why := classifySRTLA(line); why != "" {
			res.setSRTLA(why, cut(line, detailMax))
		}
		if strings.Contains(line, "Failed to re-establish any connections") {
			if now-deadLast > 5 {
				deadSince = now // a new outage
			}
			deadLast = now
			if !gaveUp && now-deadSince >= srtlaDeadWait.Seconds() {
				gaveUp = true
				elog.Event("engine", "srtla_gave_up", "after_s", srtlaDeadWait.Seconds())
				giveUp()
			}
		}
	}
}

// srtlaEvent is the journal code of a line of srtla_send (the phrases journal.go knew).
func srtlaEvent(line string) string {
	switch {
	case strings.Contains(line, "connection failed, attempting to reconnect"),
		strings.Contains(line, "attempting full socket reconnection"):
		return "srtla_link_down"
	case strings.Contains(line, "no available connections"):
		return "srtla_no_uplinks"
	}
	return "srtla_log"
}

// srtlaRegisterWait is how long srt-live-transmit waits for the SRTLA sender to register before starting anyway
// (it then waits up to its conntimeo, as before); the C sender gives up after 10 s of its own. A variable for the
// tests.
var srtlaRegisterWait = 15 * time.Second

// waitRegistered holds srt-live-transmit back until the SRTLA sender has registered its uplinks with the relay
// (SRTLA.md §9.1). The relays' SRT (gosrt, in MediaMTX) starts its delivery clock when the handshake arrives
// without discounting how long the caller had been connecting: every second srt-live-transmit spent waiting for
// the bond would be a second more of latency for the whole session (the Rust sender takes ~2 s to register).
//
// Unlike the C sender, the Rust one never gives up its first registration: with a relay that does not answer
// (down, or a cached address that is no longer its own) it would retry for ever and the session would never
// resolve the name again. So when its statistics come and say it has not registered in srtlaRegisterWait, the
// session ends with relay_unreachable, as the C sender does by exiting; the retry resolves the name again.
func (s *session) waitRegistered() error {
	o := s.o
	t0 := time.Now()
	deadline := time.NewTimer(srtlaRegisterWait)
	defer deadline.Stop()
	tick := time.NewTicker(retryTick)
	defer tick.Stop()
	for {
		select {
		case <-s.ready.C():
			elog.Event("engine", "srtla_registered", "ms", time.Since(t0).Milliseconds())
			return nil
		case <-s.srtla.Done():
			return nil // the session ends with what it said (watch)
		case <-deadline.C:
			elog.Event("engine", "srtla_register_slow", "after_s", srtlaRegisterWait.Seconds())
			if s.statsUp.fired() {
				return fail(ReasonRelayUnreachable, "the relay does not answer: the SRTLA sender did not register")
			}
			return nil
		case <-tick.C:
			if !o.running() {
				return nil
			}
		}
	}
}

// oneShot is an event that may fire from several goroutines.
type oneShot struct {
	once sync.Once
	c    chan struct{}
}

func newOneShot() *oneShot { return &oneShot{c: make(chan struct{})} }

func (g *oneShot) fire() { g.once.Do(func() { close(g.c) }) }

func (g *oneShot) C() <-chan struct{} { return g.c }

func (g *oneShot) fired() bool {
	select {
	case <-g.c:
		return true
	default:
		return false
	}
}
