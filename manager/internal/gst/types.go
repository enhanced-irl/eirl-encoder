package gst

import (
	"strconv"
	"time"
)

// The engine works against these interfaces: the real implementation is cgo (gst.go, linux only) and
// the tests use internal/gst/fake. The constants are GStreamer's ABI values (checked against the C
// headers in gst_test.go).

// State is a GstState.
type State int

const (
	StateNull    State = 1
	StateReady   State = 2
	StatePaused  State = 3
	StatePlaying State = 4
)

// ClockTimeNone is GST_CLOCK_TIME_NONE.
const ClockTimeNone = ^uint64(0)

// MessageType is a GstMessageType mask.
type MessageType int

const (
	MessageEOS     MessageType = 1 << 0
	MessageError   MessageType = 1 << 1
	MessageWarning MessageType = 1 << 2
	MessageElement MessageType = 1 << 15
)

// Message is a bus message, copied out of GStreamer.
type Message struct {
	Type  MessageType
	Src   string // element name
	Text  string // error/warning text, or the structure name of an element message
	Debug string // error/warning debug, or the structure of an element message as a string
}

// ParseError is a description that does not build.
type ParseError struct{ Msg string }

func (e *ParseError) Error() string { return e.Msg }

// Media is GStreamer as the engine sees it.
type Media interface {
	// Parse builds a pipeline from gst-launch syntax. label is only for logs.
	Parse(label, desc string) (Pipeline, error)
	// Now is the system clock: the clock of every pipeline the engine runs.
	Now() uint64
	// Monotonic is the clock of BridgeStats.LastFrame.
	Monotonic() time.Duration
	HasElement(factory string) bool
	NewBridge(kind BridgeKind, pgm Pipeline, dst Element) Bridge
	NewCadence(appsink Element) Cadence
	// NewCounter probes a pad; nil if there is no such pad.
	NewCounter(el Element, pad string) Counter
	// StreamOff unblocks a stuck capture: VIDIOC_STREAMOFF on this process' descriptors of node.
	StreamOff(node string) int
	// NewFeeder pushes frames made in Go (size bytes each) into an appsrc through a pool of buffers.
	NewFeeder(appsrc Element, size, buffers int) Feeder
	// NewTap attaches to an appsink for good: measures lateness and passes buffers untouched (headphones).
	NewTap(appsink Element) Tap
	// NewAnimator sets the compositor's pads sink_0 … sink_<pads-1> from its own thread (frame: the
	// program's frame duration).
	NewAnimator(comp Element, pads int, frame time.Duration) Animator
}

// Layer is where one compositor pad goes: its rectangle, alpha and zorder.
type Layer struct {
	X, Y, W, H int
	Alpha      float64
	Z          int
}

// Props are the compositor pad's properties for l, in their string form (Element.SetPad).
func (l Layer) Props() [][2]string {
	return [][2]string{{"xpos", strconv.Itoa(l.X)}, {"ypos", strconv.Itoa(l.Y)}, {"width", strconv.Itoa(l.W)},
		{"height", strconv.Itoa(l.H)}, {"alpha", strconv.FormatFloat(l.Alpha, 'g', -1, 64)}, {"zorder", strconv.Itoa(l.Z)}}
}

// Animator sets the compositor's pads between two output frames, from the compositor's own thread (C, a
// probe on its src pad): every property of every pad changes at once, and a fade moves one step per
// output frame by the frame's timestamp, whatever the load on the engine's goroutines.
type Animator interface {
	// Play replaces whatever is playing. The probe after the next frame out of the compositor sets
	// steps[0] (one Layer per pad) for the frame after it, then steps[k] for each following frame by its
	// timestamp; the last step stays. One step is a cut.
	Play(steps [][]Layer)
	// Busy: a table is still being played (the program is not producing frames, or a fade is running).
	Busy() bool
	// Free once the compositor's pipeline is in NULL.
	Free()
}

// Tap passes what reaches an appsink, untouched and by reference, to the current destination, and
// measures how late it arrives (clock now − (base_time + pts)). The headphones' listening needs it to
// stay behind what ALSA has already read (HEADPHONES.md §1.3). C, per buffer.
type Tap interface {
	// Connect sends what arrives to appsrc from now on; nil = only measure (and drop).
	Connect(appsrc Element)
	// TakeLate is the P90 of the lateness since the previous take, which starts a new window; 0 without
	// samples.
	TakeLate() time.Duration
	// Free once the appsink's pipeline is in NULL.
	Free()
}

// Feeder pushes frames made in Go (the screen's UI layer) into an appsrc. No allocation per frame: a
// pool of a few buffers (M0: a new 2.4 MB buffer per drawing took the RAM to ~100 MB).
type Feeder interface {
	// Push copies one frame in; false if all buffers are still in use downstream (frame dropped) or the
	// size is wrong. The appsrc stamps it on arrival.
	Push(frame []byte) bool
	Free()
}

// Pipeline is a top-level pipeline.
type Pipeline interface {
	Label() string
	// ByName returns the element, or nil. It stays valid as long as the pipeline.
	ByName(name string) Element
	SetState(s State) error
	// FixBase: system clock and this base_time, so pipelines with the same base share a timeline.
	FixBase(base uint64)
	BaseTime() uint64
	// Pop waits up to timeout for a message of the given types; nil if none.
	Pop(timeout time.Duration, types MessageType) *Message
	// SetLatency forces the pipeline's latency (gst_pipeline_set_latency).
	SetLatency(d time.Duration)
	// Latency asks the pipeline its (minimum) latency; false if it does not answer.
	Latency() (time.Duration, bool)
	// Close sets NULL and releases the pipeline. NULL can block (an I2S capture without clock ~5 s, a
	// stuck nvv4l2camerasrc forever): callers that cannot wait run it with a deadline.
	Close()
}

// Element is an element of a pipeline.
type Element interface {
	Name() string
	// Set sets a property from its string form (ints, enums, bools); false if there is no such property.
	Set(prop, value string) bool
	// SetPad sets a property of one of its pads (the compositor's sink_N: xpos, alpha…).
	SetPad(pad, prop, value string) bool
	// SetPadOffset shifts what goes through one of its pads that much later on the running time
	// (gst_pad_set_offset); false if there is no such pad.
	SetPadOffset(pad string, d time.Duration) bool
	SetBool(prop string, v bool)
	SetFloat(prop string, v float64)
	// PullBytes takes the next appsink sample as bytes (JPEGs); nil after the timeout.
	PullBytes(timeout time.Duration) []byte
	// PullBuffer takes the next appsink sample as a held buffer without timestamps (stills).
	PullBuffer(timeout time.Duration) Buffer
	// CapsString is the current caps of one of its pads, "" if none (the decoded size of a camera).
	CapsString(pad string) string
	// SelectStream makes an rtspsrc keep only the streams of one media ("video", "audio").
	SelectStream(media string) bool
}

// Buffer is a held buffer (a still image).
type Buffer interface {
	Size() (width, height int)
	Unref()
}

// BridgeKind says how a bridge stamps what crosses it.
type BridgeKind int

const (
	// BridgeSource: a source in its own pipeline. Capture time (judged) + sync, on the program timeline.
	BridgeSource BridgeKind = 0
	// BridgeShared: same timeline as the program (program -> screen): pts + extra.
	BridgeShared BridgeKind = 1
	// BridgePass: as is (program -> output, which shares the program's base_time).
	BridgePass BridgeKind = 2
)

// BridgeParams are a bridge's timing parameters, changed at any time.
type BridgeParams struct {
	Sync       time.Duration // capture -> program (MIXER_SYNC); 0 = arrival time
	Extra      time.Duration // on top (network cameras' cushion; screen: latency + margin)
	Early      time.Duration // how far the source's pts is behind the capture (nvv4l2camerasrc: 2 frames)
	RawClock   bool          // pts from CLOCK_MONOTONIC_RAW (v4l2src on the Tegra CSI)
	JudgeLimit time.Duration // largest believable capture age (0 = 150 ms)
}

// BridgeStats is what a bridge measured.
type BridgeStats struct {
	Frames    uint64
	LastFrame time.Duration // Media.Monotonic of the last frame (0 = none yet)
	// Since the last take of the timing window
	N                      uint64
	AgeMin, AgeAvg, AgeMax time.Duration
	MaxGap                 time.Duration
	Judge                  int // capture times usable: -1 undecided, 0 no, 1 yes
	JudgeMin               time.Duration
	// Minimum age since the last take of the lag; -1 = no frame with a timestamp
	Lag time.Duration
}

// Bridge moves buffers from an appsink to an appsrc of another pipeline, in C. It lives as long as its
// slot: Free only once no pipeline can call into it.
type Bridge interface {
	SetParams(p BridgeParams)
	// SetScreen also sends a frame to the screen, at most one every gap (nil = none).
	SetScreen(dst Element, gap time.Duration)
	// SetThumb sends a frame to a thumbnail appsrc every interval (nil = none).
	SetThumb(dst Element, every time.Duration)
	// SetDst changes where the frames go (the hot output's appsrc of each session); nil drops them.
	SetDst(dst Element)
	// Attach makes appsink the bridge's input; frames of a previous appsink are dropped.
	Attach(appsink Element)
	Detach()
	// DetachIf detaches only if appsink is still the input: a source that is closing must not take the
	// bridge from the one that replaced it in the slot.
	DetachIf(appsink Element)
	PushStill(b Buffer)
	// Stats reads the counters; takeTiming starts a new timing window, takeLag a new lag measure.
	Stats(takeTiming, takeLag bool) BridgeStats
	Free()
}

// Cadence counts the program's frames and how many are new (a repeated frame is identical).
type Cadence interface {
	Take() (all, fresh uint64)
}

// Counter counts buffers and bytes through a pad.
type Counter interface {
	Take() (buffers, bytes uint64)
	// Free once its pipeline is in NULL.
	Free()
}

// CloseWithin closes a pipeline waiting at most d (setting NULL can block: an I2S capture without clock,
// a stuck nvv4l2camerasrc); true if it closed in time. The close goes on in the background otherwise.
func CloseWithin(p Pipeline, d time.Duration) bool {
	if p == nil {
		return true
	}
	done := make(chan struct{})
	go func() { p.Close(); close(done) }()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}
