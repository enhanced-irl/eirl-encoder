// Package fake is an in-memory gst.Media for the engine's tests: pipelines that record what is done to
// them, bridges whose counters the test moves, and messages the test posts. No GStreamer.
package fake

import (
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// Media is the fake GStreamer.
type Media struct {
	mu         sync.Mutex
	Pipelines  []*Pipeline
	Bridges    []*Bridge
	Animators  []*Animator
	Elements   map[string]bool // factories that exist (HasElement)
	ParseErr   map[string]error
	mono       time.Duration
	clock      uint64
	StreamOffs []string
	// BlockClose makes Close of pipelines with this label block until the channel is closed
	BlockClose map[string]chan struct{}
}

// New is an empty fake with the Tegra elements.
func New() *Media {
	return &Media{Elements: map[string]bool{"nvv4l2camerasrc": true}, ParseErr: map[string]error{},
		BlockClose: map[string]chan struct{}{}, mono: time.Hour, clock: uint64(time.Hour)}
}

var reName = regexp.MustCompile(`name=([A-Za-z0-9_]+)`)

func (m *Media) Parse(label, desc string) (gst.Pipeline, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.ParseErr[label]; ok {
		return nil, err
	}
	p := &Pipeline{media: m, label: label, Desc: desc, elems: map[string]*Element{}}
	for _, n := range reName.FindAllStringSubmatch(desc, -1) {
		p.elems[n[1]] = &Element{name: n[1], Props: map[string]string{}, Pads: map[string]map[string]string{}}
	}
	m.Pipelines = append(m.Pipelines, p)
	return p, nil
}

func (m *Media) Now() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clock
}

func (m *Media) Monotonic() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mono
}

// Advance moves the monotonic clock (frame ages, watchdogs).
func (m *Media) Advance(d time.Duration) {
	m.mu.Lock()
	m.mono += d
	m.clock += uint64(d)
	m.mu.Unlock()
}

func (m *Media) HasElement(f string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Elements[f]
}

func (m *Media) NewBridge(kind gst.BridgeKind, pgm gst.Pipeline, dst gst.Element) gst.Bridge {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := &Bridge{Kind: kind, media: m}
	if dst != nil {
		b.Dst = dst.(*Element).name
	}
	m.Bridges = append(m.Bridges, b)
	return b
}

func (m *Media) NewCadence(appsink gst.Element) gst.Cadence { return &Cadence{} }

func (m *Media) NewCounter(el gst.Element, pad string) gst.Counter { return &Counter{} }

func (m *Media) StreamOff(node string) int {
	m.mu.Lock()
	m.StreamOffs = append(m.StreamOffs, node)
	ch := m.BlockClose["unblock-on-streamoff"]
	m.mu.Unlock()
	if ch != nil {
		close(ch)
	}
	return 1
}

func (m *Media) NewFeeder(appsrc gst.Element, size, buffers int) gst.Feeder {
	return &Feeder{Dst: appsrc.(*Element).name, Size: size}
}

// Feeder is a fake feeder: it counts and keeps the last frame.
type Feeder struct {
	mu     sync.Mutex
	Dst    string
	Size   int
	Pushed int
	Last   []byte
	Freed  bool
}

func (f *Feeder) Push(frame []byte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(frame) != f.Size {
		return false
	}
	f.Pushed++
	f.Last = append(f.Last[:0], frame...)
	return true
}

func (f *Feeder) Free() { f.mu.Lock(); f.Freed = true; f.mu.Unlock() }

func (m *Media) NewTap(appsink gst.Element) gst.Tap { return &Tap{} }

func (m *Media) NewAnimator(comp gst.Element, pads int, frame time.Duration) gst.Animator {
	a := &Animator{comp: comp.(*Element), NPads: pads, Frame: frame}
	m.mu.Lock()
	m.Animators = append(m.Animators, a)
	m.mu.Unlock()
	return a
}

// Animator is a fake animator: the last step of each table lands on the compositor's pads at once (as
// strings, like SetPad), unless Hold keeps it busy.
type Animator struct {
	mu    sync.Mutex
	comp  *Element
	NPads int
	Frame time.Duration
	Plays [][][]gst.Layer
	Hold  bool
	Freed bool
}

func (a *Animator) Play(steps [][]gst.Layer) {
	if len(steps) == 0 {
		return
	}
	a.mu.Lock()
	a.Plays = append(a.Plays, steps)
	a.mu.Unlock()
	for i, l := range steps[len(steps)-1] {
		for _, kv := range l.Props() {
			a.comp.SetPad("sink_"+strconv.Itoa(i), kv[0], kv[1])
		}
	}
}

func (a *Animator) Busy() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.Hold }

func (a *Animator) Free() { a.mu.Lock(); a.Freed = true; a.mu.Unlock() }

// Last is the last table played (nil if none).
func (a *Animator) Last() [][]gst.Layer {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.Plays) == 0 {
		return nil
	}
	return a.Plays[len(a.Plays)-1]
}

// Tap is a fake tap.
type Tap struct {
	mu   sync.Mutex
	Dst  string
	Late time.Duration
}

func (t *Tap) Connect(appsrc gst.Element) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Dst = ""
	if appsrc != nil {
		t.Dst = appsrc.(*Element).name
	}
}

func (t *Tap) TakeLate() time.Duration { t.mu.Lock(); defer t.mu.Unlock(); return t.Late }

func (t *Tap) Free() {}

// Find is the last pipeline with this label.
func (m *Media) Find(label string) *Pipeline {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.Pipelines) - 1; i >= 0; i-- {
		if m.Pipelines[i].label == label {
			return m.Pipelines[i]
		}
	}
	return nil
}

// Pipeline is a fake pipeline.
type Pipeline struct {
	media  *Media
	label  string
	Desc   string
	mu     sync.Mutex
	elems  map[string]*Element
	State  gst.State
	Base   uint64
	Closed bool
	EOS    bool          // SendEOS was called
	NoEOS  bool          // SendEOS does not post MessageEOS (a stuck pipeline)
	Zero   [2]string     // ZeroStart's video and audio appsrcs
	Forced time.Duration // what SetLatency set
	Query  time.Duration // what a latency query answers (0 = no answer)
	msgs   []*gst.Message
}

func (p *Pipeline) Label() string { return p.label }

func (p *Pipeline) ByName(name string) gst.Element {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.elems[name]; ok {
		return e
	}
	return nil
}

// El is the element with that name, for assertions (nil if none).
func (p *Pipeline) El(name string) *Element {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.elems[name]
}

func (p *Pipeline) SetState(s gst.State) error {
	p.mu.Lock()
	p.State = s
	p.mu.Unlock()
	return nil
}

func (p *Pipeline) FixBase(base uint64) { p.mu.Lock(); p.Base = base; p.mu.Unlock() }

func (p *Pipeline) BaseTime() uint64 { p.mu.Lock(); defer p.mu.Unlock(); return p.Base }

// Post queues a bus message for Pop.
func (p *Pipeline) Post(m *gst.Message) { p.mu.Lock(); p.msgs = append(p.msgs, m); p.mu.Unlock() }

func (p *Pipeline) Pop(timeout time.Duration, types gst.MessageType) *gst.Message {
	deadline := time.Now().Add(timeout)
	for {
		p.mu.Lock()
		for i, m := range p.msgs {
			if m.Type&types != 0 {
				p.msgs = append(p.msgs[:i], p.msgs[i+1:]...)
				p.mu.Unlock()
				return m
			}
		}
		p.mu.Unlock()
		if !time.Now().Before(deadline) {
			return nil
		}
		time.Sleep(min(10*time.Millisecond, time.Until(deadline)+time.Millisecond))
	}
}

// SendEOS records it and, unless NoEOS, posts MessageEOS as the sinks would once they got it.
func (p *Pipeline) SendEOS() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Closed {
		return false
	}
	p.EOS = true
	if !p.NoEOS {
		p.msgs = append(p.msgs, &gst.Message{Type: gst.MessageEOS})
	}
	return true
}

// ZeroStart records the two appsrcs in Zero; false if either is not an element of the pipeline.
func (p *Pipeline) ZeroStart(video, audio string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.elems[video] == nil || p.elems[audio] == nil {
		return false
	}
	p.Zero = [2]string{video, audio}
	return true
}

// SetLatency records the forced latency in Forced (0 = never).
func (p *Pipeline) SetLatency(d time.Duration) { p.mu.Lock(); p.Forced = d; p.mu.Unlock() }

// Latency answers what the test put in Query (ok when it is not 0).
func (p *Pipeline) Latency() (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Query, p.Query != 0
}

func (p *Pipeline) Close() {
	p.media.mu.Lock()
	ch := p.media.BlockClose[p.label]
	p.media.mu.Unlock()
	if ch != nil {
		<-ch
	}
	p.mu.Lock()
	p.Closed, p.State = true, gst.StateNull
	p.mu.Unlock()
}

// Element is a fake element: it records properties and pad properties.
type Element struct {
	name    string
	mu      sync.Mutex
	Props   map[string]string
	Pads    map[string]map[string]string
	Caps    map[string]string
	Pulls   [][]byte
	Select  string
	Missing map[string]bool // properties that do not exist
	Offsets map[string]time.Duration
}

func (e *Element) Name() string { return e.name }

func (e *Element) Set(prop, value string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Missing[prop] {
		return false
	}
	e.Props[prop] = value
	return true
}

func (e *Element) SetPad(pad, prop, value string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Pads[pad] == nil {
		e.Pads[pad] = map[string]string{}
	}
	e.Pads[pad][prop] = value
	return true
}

func (e *Element) SetPadOffset(pad string, d time.Duration) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Offsets == nil {
		e.Offsets = map[string]time.Duration{}
	}
	e.Offsets[pad] = d
	return true
}

// Offset is a pad's offset as set.
func (e *Element) Offset(pad string) time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Offsets[pad]
}

// Pad is a pad's property as set.
func (e *Element) Pad(pad, prop string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Pads[pad][prop]
}

// Prop is a property as set.
func (e *Element) Prop(prop string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Props[prop]
}

func (e *Element) SetBool(prop string, v bool) { e.Set(prop, fmt.Sprint(v)) }

func (e *Element) SetFloat(prop string, v float64) { e.Set(prop, fmt.Sprint(v)) }

func (e *Element) PullBytes(timeout time.Duration) []byte {
	e.mu.Lock()
	if len(e.Pulls) > 0 {
		b := e.Pulls[0]
		e.Pulls = e.Pulls[1:]
		e.mu.Unlock()
		return b
	}
	e.mu.Unlock()
	time.Sleep(min(timeout, 20*time.Millisecond))
	return nil
}

// Feed queues samples for PullBytes (an appsink's output, from a test while a goroutine pulls).
func (e *Element) Feed(b ...[]byte) {
	e.mu.Lock()
	e.Pulls = append(e.Pulls, b...)
	e.mu.Unlock()
}

// Pending is how many fed samples were not pulled yet.
func (e *Element) Pending() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.Pulls) }

func (e *Element) PullBuffer(time.Duration) gst.Buffer { return &Buffer{From: e.name} }

func (e *Element) CapsString(pad string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Caps[pad]
}

func (e *Element) SelectStream(media string) bool {
	e.mu.Lock()
	e.Select = media
	e.mu.Unlock()
	return true
}

// Buffer is a fake still.
type Buffer struct {
	From    string
	Unrefed bool
	W, H    int
}

func (b *Buffer) Size() (int, int) { return b.W, b.H }

func (b *Buffer) Unref() { b.Unrefed = true }

// Bridge is a fake bridge: the test sets its frames and reads what was pushed.
type Bridge struct {
	media    *Media
	Kind     gst.BridgeKind
	Dst      string
	mu       sync.Mutex
	Params   gst.BridgeParams
	Screen   string
	Thumb    string
	Attached string
	Stills   []string // From of each still pushed
	stats    gst.BridgeStats
	Freed    bool
	Every    int
	Held     bool
}

func (b *Bridge) SetParams(p gst.BridgeParams) { b.mu.Lock(); b.Params = p; b.mu.Unlock() }

func (b *Bridge) SetEvery(n int) { b.mu.Lock(); b.Every = n; b.mu.Unlock() }

func (b *Bridge) SetScreen(dst gst.Element, _ time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Screen = ""
	if dst != nil {
		b.Screen = dst.(*Element).name
	}
}

func (b *Bridge) SetThumb(dst gst.Element, _ time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Thumb = ""
	if dst != nil {
		b.Thumb = dst.(*Element).name
	}
}

func (b *Bridge) Attach(appsink gst.Element) {
	b.mu.Lock()
	b.Attached = appsink.(*Element).name
	b.Held = false
	b.stats.Judge, b.stats.Lag = -1, -1
	b.mu.Unlock()
}

func (b *Bridge) SetDst(dst gst.Element) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Dst = ""
	if dst != nil {
		b.Dst = dst.(*Element).name
	}
}

func (b *Bridge) HoldIf(appsink gst.Element, hold bool) {
	b.mu.Lock()
	if appsink != nil && b.Attached == appsink.(*Element).name {
		b.Held = hold
	}
	b.mu.Unlock()
}

// IsHeld says whether the input's frames are held.
func (b *Bridge) IsHeld() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.Held }

func (b *Bridge) Detach() { b.mu.Lock(); b.Attached = ""; b.mu.Unlock() }

func (b *Bridge) DetachIf(appsink gst.Element) {
	b.mu.Lock()
	if appsink != nil && b.Attached == appsink.(*Element).name {
		b.Attached = ""
	}
	b.mu.Unlock()
}

func (b *Bridge) PushStill(s gst.Buffer) {
	if s == nil {
		return
	}
	b.mu.Lock()
	b.Stills = append(b.Stills, s.(*Buffer).From)
	b.mu.Unlock()
}

// Frames simulates n frames crossing now.
func (b *Bridge) Frames(n uint64) {
	mono := b.media.Monotonic()
	b.mu.Lock()
	b.stats.Frames += n
	b.stats.LastFrame = mono
	b.mu.Unlock()
}

// SetStats replaces the timing part of the stats.
func (b *Bridge) SetStats(s gst.BridgeStats) {
	b.mu.Lock()
	f, l := b.stats.Frames, b.stats.LastFrame
	b.stats = s
	b.stats.Frames, b.stats.LastFrame = f, l
	b.mu.Unlock()
}

// LastStill is the last still pushed ("" if none).
func (b *Bridge) LastStill() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.Stills) == 0 {
		return ""
	}
	return b.Stills[len(b.Stills)-1]
}

func (b *Bridge) Stats(takeTiming, takeLag bool) gst.BridgeStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stats
	if takeLag {
		b.stats.Lag = -1
	}
	return s
}

func (b *Bridge) Free() { b.mu.Lock(); b.Freed = true; b.mu.Unlock() }

// Cadence is a fake cadence counter.
type Cadence struct {
	mu         sync.Mutex
	All, Fresh uint64
}

func (c *Cadence) Take() (uint64, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, f := c.All, c.Fresh
	c.All, c.Fresh = 0, 0
	return a, f
}

// Counter is a fake buffer counter.
type Counter struct {
	mu   sync.Mutex
	N, B uint64
}

func (c *Counter) Free() {}

func (c *Counter) Take() (uint64, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, b := c.N, c.B
	c.N, c.B = 0, 0
	return n, b
}

var _ gst.Media = (*Media)(nil)
