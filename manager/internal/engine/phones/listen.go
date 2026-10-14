package phones

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

const logPrefix = "headphones"

// ErrNoBranch is a program pipeline without phvalve/phtap: no headphones (the `phones` command answers
// unsupported).
var ErrNoBranch = errors.New("the program has no phvalve/phtap: no headphones")

// Settings is what is saved with the scene (mixer-scene.json → audioSettings.phones).
type Settings struct {
	On     bool `json:"on"`
	GainDb int  `json:"gainDb"`
}

// DefaultSettings: D1, on at −12 dB.
func DefaultSettings() Settings { return Settings{On: DefaultOn, GainDb: DefaultGain} }

// LoadSettings reads a saved audioSettings.phones: each field that is valid overrides its default
// (on a bool, gainDb a GainDB); anything else (missing, not an object, junk) leaves the defaults.
func LoadSettings(raw json.RawMessage) Settings {
	s := DefaultSettings()
	if len(raw) == 0 {
		return s
	}
	var d map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&d) != nil || d == nil {
		return s
	}
	if on, ok := d["on"].(bool); ok {
		s.On = on
	}
	if g, ok := GainDB(d["gainDb"]); ok {
		s.GainDb = g
	}
	return s
}

// Options is what the headphones need from the engine.
type Options struct {
	Media gst.Media
	// Program is the program pipeline, with phvalve → phtap. Its base_time is the listening's.
	Program gst.Pipeline
	// Saved is audioSettings.phones of mixer-scene.json (nil: the defaults).
	Saved json.RawMessage
	// Changed asks the engine to publish (Python engine.changed()); called from the headphones'
	// goroutine, never with a lock held.
	Changed func()
	// Screen is the touch screen right now, nil (the nil interface) without one. Called from the
	// headphones' goroutine. M2 has no screen in Go: nil, and the listening opens at once.
	Screen func() ScreenView
	// CompositorLatency is MIXER_LATENCY (Python Engine.latency_ns), for ScreenDelay.
	CompositorLatency time.Duration
	// Run runs amixer; nil = sysx.Run.
	Run sysx.Runner
	// NewTap makes the Tap on phtap; nil = NewBridgeTap.
	NewTap func(media gst.Media, pgm gst.Pipeline, phtap gst.Element) Tap
}

// Phones is the listening. One goroutine runs all of it (waits for the screen, opens, watches, retries,
// closes): at most one listening pipeline at a time. Set, Info and Settings come from the actor; Stop
// when the engine stops.
type Phones struct {
	media    gst.Media
	pgm      gst.Pipeline
	valve    gst.Element
	tap      Tap
	changed  func()
	screenOf func() ScreenView
	compLat  time.Duration
	run      sysx.Runner

	mu     sync.Mutex
	want   bool
	gain   int
	state  string
	detail string

	wake     chan struct{} // an auto-reset event: a command for the goroutine
	stopping chan struct{}
	done     chan struct{}
	once     sync.Once
	started  bool

	// The goroutine's own
	vol gst.Element // phvol of the open listening pipeline
	cur float64     // the volume phvol has now

	// Seams for tests and other boards (defaults in New)
	asound  string // /proc/asound
	sink    string // the listening's sink, as a description with name=phsink
	measure time.Duration
	retry   []time.Duration
	find    func() (Device, bool)
	session func(Device) (string, bool)
	readELD func(card int) ELD
	digital func(card int)
	noLat   sync.Once
}

// New makes the headphones on the program's phvalve/phtap (ErrNoBranch without them) and attaches the
// Tap. Nothing opens until Start.
func New(o Options) (*Phones, error) {
	tapEl, valve := o.Program.ByName("phtap"), o.Program.ByName("phvalve")
	if tapEl == nil || valve == nil {
		return nil, ErrNoBranch
	}
	s := LoadSettings(o.Saved)
	p := &Phones{
		media: o.Media, pgm: o.Program, valve: valve, changed: o.Changed, screenOf: o.Screen,
		compLat: o.CompositorLatency, run: o.Run,
		want: s.On, gain: s.GainDb, state: StateOff,
		wake: make(chan struct{}, 1), stopping: make(chan struct{}), done: make(chan struct{}),
		asound: "/proc/asound", measure: Measure, retry: Retry,
		sink: fmt.Sprintf("alsasink name=phsink provide-clock=false sync=true buffer-time=%d latency-time=10000",
			SinkBuffer.Microseconds()),
	}
	if p.changed == nil {
		p.changed = func() {}
	}
	if p.run == nil {
		p.run = sysx.Run
	}
	newTap := o.NewTap
	if newTap == nil {
		newTap = NewBridgeTap
	}
	p.tap = newTap(o.Media, o.Program, tapEl)
	p.find, p.session, p.readELD, p.digital = p.findHDMI, p.listen, p.eldOf, p.digitalOn
	return p, nil
}

// --- settings and state (any goroutine) ---

// Settings is what is saved with the scene.
func (p *Phones) Settings() Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Settings{On: p.want, GainDb: p.gain}
}

// Info is audio.phones of the state.
func (p *Phones) Info() mixer.Phones {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := DetailOf(p.detail)
	return mixer.Phones{On: p.want, GainDb: p.gain, State: p.state, Detail: p.detail, DetailCode: d.Code, DetailArgs: d.Args}
}

func (p *Phones) setStatus(state, detail string) {
	p.mu.Lock()
	changed := state != p.state || detail != p.detail
	p.state, p.detail = state, detail
	p.mu.Unlock()
	if changed {
		p.changed()
	}
}

// Set applies a change from the actor; whether something changed. A change wakes the goroutine (it
// ramps to the new volume, or opens or closes).
func (p *Phones) Set(c Change) bool {
	p.mu.Lock()
	changed := false
	if c.On != nil && *c.On != p.want {
		p.want = *c.On
		changed = true
		code := "phones_off"
		if p.want {
			code = "phones_on"
		}
		elog.Event(logPrefix, code)
	}
	if c.Gain != nil && *c.Gain != p.gain {
		p.gain = *c.Gain
		changed = true
	}
	p.mu.Unlock()
	if changed {
		p.signal()
	}
	return changed
}

// Command runs the socket's (and the screen's) `phones` command on the actor (Engine.set_phones). p nil
// = no phvalve/phtap: unsupported. changed: the engine logs the command, saves the scene and publishes,
// only then.
func Command(p *Phones, msg map[string]any) (ok bool, code string, changed bool) {
	if p == nil {
		return false, "unsupported", false
	}
	c, code := ParseCommand(msg)
	if code != "" {
		return false, code, false
	}
	return true, "", p.Set(c)
}

func (p *Phones) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Phones) wanted() (bool, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.want, p.gain
}

func (p *Phones) isStopping() bool {
	select {
	case <-p.stopping:
		return true
	default:
		return false
	}
}

// --- life ---

// Start runs the listening's goroutine. It waits for the screen to be stable before opening the HDMI.
func (p *Phones) Start() {
	p.mu.Lock()
	if p.started || p.isStopping() {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()
	go p.loop()
}

// Stop when the engine stops: the goroutine ramps down and closes its pipeline within CloseWait (+ the
// ramp and one bus poll); whoever stops waits on Done within its budget.
func (p *Phones) Stop() {
	p.once.Do(func() {
		close(p.stopping)
		p.mu.Lock()
		started := p.started
		p.mu.Unlock()
		if !started {
			close(p.done)
		}
	})
}

// Done is closed when the goroutine has ended (at once if it never started).
func (p *Phones) Done() <-chan struct{} { return p.done }

// Free releases the Tap: only once the program pipeline is in NULL.
func (p *Phones) Free() { p.tap.Free() }

// wait is interruptible: a command or the stop cut it short.
func (p *Phones) wait(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.wake:
	case <-p.stopping:
	case <-t.C:
	}
}

func (p *Phones) screen() ScreenView {
	if p.screenOf == nil {
		return nil
	}
	return p.screenOf()
}

func (p *Phones) loop() {
	defer close(p.done)
	fails, lastErr := 0, ""
	for !p.isStopping() {
		if want, _ := p.wanted(); !want {
			fails, lastErr = 0, ""
			p.setStatus(StateOff, "")
			p.wait(offWait)
			continue
		}
		if !ScreenReady(p.screen()) {
			p.setStatus(StatusOf(true, false, lastErr, ELDUnknown))
			p.wait(screenPoll)
			continue
		}
		dev, ok := p.find()
		if !ok {
			p.setStatus(StateError, NoDevice)
			p.wait(NoDeviceWait)
			continue
		}
		t0 := time.Now()
		e, failed := p.session(dev)
		if !failed { // off or stopping: no failure to show
			fails, lastErr = 0, ""
			continue
		}
		lastErr = ShortError(e)
		if time.Since(t0) > freshAfter {
			fails = 1
		} else {
			fails++
		}
		wait := retryWait(fails, p.retry)
		elog.Event(logPrefix, "phones_retry", "err", lastErr, "wait_s", int(wait/time.Second))
		p.setStatus(StateError, lastErr)
		end := time.Now().Add(wait)
		for {
			want, _ := p.wanted()
			left := time.Until(end)
			if !want || p.isStopping() || left <= 0 {
				break
			}
			p.wait(min(500*time.Millisecond, left))
		}
	}
	p.setStatus(StateOff, "")
}

// --- the system ---

func (p *Phones) findHDMI() (Device, bool) {
	pcm, err1 := os.ReadFile(filepath.Join(p.asound, "pcm"))
	cards, err2 := os.ReadFile(filepath.Join(p.asound, "cards"))
	if err1 != nil || err2 != nil {
		return Device{}, false
	}
	return FindHDMI(text(pcm), text(cards))
}

func (p *Phones) eldOf(card int) ELD {
	paths, _ := filepath.Glob(filepath.Join(p.asound, "card"+strconv.Itoa(card), "eld#*"))
	var texts []string
	for _, path := range paths { // Glob sorts them, like Python's sorted(glob)
		if b, err := os.ReadFile(path); err == nil {
			texts = append(texts, text(b))
		}
	}
	return ELDVerdict(texts)
}

// text decodes like Python's errors='replace'.
func text(b []byte) string { return strings.ToValidUTF8(string(b), "�") }

// digitalOn switches the card's digital output on (its default; without alsa-restore nobody ensures
// it). On failure it goes on: alsasink will give the error, if any.
func (p *Phones) digitalOn(card int) {
	p.run(2*time.Second, "amixer", "-q", "-c", strconv.Itoa(card), "cset", "name=IEC958 Playback Switch", "on")
}

// --- the listening ---

// Desc is the listening pipeline with this sink (named phsink). Its latency is forced (setLatency).
func Desc(sink string) string {
	return fmt.Sprintf("appsrc name=pha is-live=true format=time do-timestamp=false max-bytes=0 block=false caps=\"%s\" "+
		"! queue max-size-time=500000000 leaky=downstream ! volume name=phvol volume=0 "+
		"! audioconvert ! audioresample ! %s", ACaps, sink)
}

// LatencySetter is a pipeline whose latency can be forced (gst_pipeline_set_latency). Not in
// gst.Pipeline yet: without it the listening plays at the pipeline's own latency.
type LatencySetter interface {
	SetLatency(d time.Duration)
}

func (p *Phones) setLatency(pipe gst.Pipeline, d time.Duration) {
	if ls, ok := pipe.(LatencySetter); ok {
		ls.SetLatency(d)
		return
	}
	p.noLat.Do(func() { elog.Event(logPrefix, "phones_latency_unsupported") })
}

func (p *Phones) sleepMeasure() {
	t := time.NewTimer(p.measure)
	defer t.Stop()
	select {
	case <-p.stopping:
	case <-t.C:
	}
}

// listen opens the listening and watches it until it is switched off, the engine stops or it fails.
// The error text and whether it failed.
func (p *Phones) listen(dev Device) (string, bool) {
	p.digital(dev.Card)
	// Before opening, how late the program audio arrives (valve open, nowhere to go: the Tap only
	// measures)
	p.tap.TakeLate()
	p.valve.SetBool("drop", false)
	p.sleepMeasure()
	late := p.tap.TakeLate()
	if want, _ := p.wanted(); !want || p.isStopping() {
		p.valve.SetBool("drop", true)
		return "", false
	}
	pipe, err := p.media.Parse("headphones", Desc(p.sink))
	if err != nil {
		p.valve.SetBool("drop", true)
		return cannotBuild + err.Error(), true
	}
	if snk := pipe.ByName("phsink"); snk != nil {
		snk.Set("device", dev.Name)
	}
	// The program's clock and base_time: the buffers arrive with their marks as they are. And the screen's
	// latency (they sound when its picture shows), unless the audio arrives even later
	pipe.FixBase(p.pgm.BaseTime())
	screen, _ := ScreenDelay(p.compLat, p.screen(), false)
	screen += Trim
	delay := TargetDelay(screen, late)
	p.setLatency(pipe, delay)
	if err := pipe.SetState(gst.StatePlaying); err != nil {
		m := pipe.Pop(0, gst.MessageError)
		p.valve.SetBool("drop", true)
		p.close(pipe)
		if m != nil {
			return m.Text, true
		}
		return notOpening, true
	}
	p.tap.Connect(pipe.ByName("pha"))
	p.vol, p.cur = pipe.ByName("phvol"), 0
	defer func() {
		p.valve.SetBool("drop", true)
		p.tap.Connect(nil)
		p.vol = nil
		p.close(pipe)
	}()
	// Open, the codec is awake and the ELD up to date (not before: §1 of HEADPHONES.md)
	eld := p.readELD(dev.Card)
	p.setStatus(StatusOf(true, true, "", eld))
	_, gain := p.wanted()
	eldLog := string(eld)
	if eld == ELDUnknown {
		eldLog = "unknown"
	}
	elog.Event(logPrefix, "phones_playing", "device", dev.Name, "gain_db", gain, "delay_ms", delay.Milliseconds(),
		"screen_ms", screen.Milliseconds(), "late_ms", late.Milliseconds(), "eld", eldLog)
	p.ramp(VolumeOf(gain))
	nextELD := time.Now().Add(ELDEvery)
	for {
		want, gain := p.wanted()
		if !want || p.isStopping() {
			break
		}
		if m := pipe.Pop(busPoll, gst.MessageError); m != nil {
			dbg := ""
			if m.Debug != "" {
				lines := splitLines(m.Debug)
				if len(lines) > 0 {
					dbg = truncRunes(lines[len(lines)-1], 200)
				}
			}
			elog.Event(logPrefix, "phones_error", "element", m.Src, "error", m.Text, "debug", dbg)
			return m.Text, true // the deferred close; no ramp down after an error
		}
		select {
		case <-p.wake:
			_, gain = p.wanted()
			p.ramp(VolumeOf(gain))
		default:
		}
		if now := time.Now(); !now.Before(nextELD) {
			nextELD = now.Add(ELDEvery)
			eld = p.readELD(dev.Card)
			p.setStatus(StatusOf(true, true, "", eld))
			// The screen may have been rebuilt with another latency (only with it on; halfway through a
			// reconnection, its own is unknown) and the audio may arrive later than when opening (only up:
			// going down would cut again for nothing). It goes on playing
			if s2, ok := ScreenDelay(p.compLat, p.screen(), true); ok {
				screen = s2 + Trim
			}
			late = max(late, p.tap.TakeLate())
			if next := TargetDelay(screen, late); DelayMoved(delay, next, true) {
				elog.Event(logPrefix, "phones_delay_changed", "delay_ms", next.Milliseconds(), "was_ms", delay.Milliseconds(),
					"screen_ms", screen.Milliseconds(), "late_ms", late.Milliseconds())
				delay = next
				p.setLatency(pipe, delay)
			}
		}
	}
	p.ramp(0) // off or stopping: down before cutting
	return "", false
}

// ramp goes from the current volume to target in Ramp (in this goroutine, 10 ms steps).
func (p *Phones) ramp(target float64) {
	vol, start := p.vol, p.cur
	if vol == nil || abs(target-start) < 1e-4 {
		return
	}
	t0 := time.Now()
	for {
		t := min(1, float64(time.Since(t0))/float64(Ramp))
		v := start + (target-start)*t
		vol.SetFloat("volume", v)
		p.cur = v
		if t >= 1 {
			return
		}
		time.Sleep(rampStep)
	}
}

// close sets NULL in another goroutine with CloseWait at most: if the HDMI is gone, closing can hang.
// A pipeline that does not close is left behind.
func (p *Phones) close(pipe gst.Pipeline) {
	done := make(chan struct{})
	go func() {
		pipe.Close()
		close(done)
	}()
	t := time.NewTimer(CloseWait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		elog.Event(logPrefix, "phones_close_timeout", "wait_s", int(CloseWait/time.Second))
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
