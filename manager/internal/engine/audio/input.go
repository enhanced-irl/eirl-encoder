package audio

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/btaudio"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/detail"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/netcam"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// Timings of an input's life (engine_audio.py).
const (
	popEvery    = 250 * time.Millisecond // bus poll
	liveWithin  = time.Second            // a sample this recent: live
	retryNoWait = 500 * time.Millisecond
	stableAfter = 30 * time.Second // an opening that lasted this long resets the backoff
)

var backoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// Input is one audio input in its slot: one goroutine with at most one live pipeline at a time. Its
// buffers cross to the program in C, through the slot's bridge; the goroutine opens, watches (level,
// errors, watchdog) and closes.
type Input struct {
	bus    *Bus
	slot   int // 0-based
	id     string
	kind   string // i2s | usb | network
	device string // ALSA device, or the camera's RTSP URL
	link   *slotLink

	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}

	mu      sync.Mutex
	name    string
	state   string
	detail  detail.D
	since   time.Time
	peak    float64
	hasPeak bool

	// Only the goroutine
	codec string // network camera: its audio codec when it was opened
	judge int    // the bridge's judge as last logged (-1 undecided)
}

func newInput(b *Bus, slot int, id, kind, name, device string) *Input {
	return &Input{bus: b, slot: slot, id: id, kind: kind, name: name, device: device, link: b.links[slot],
		stopCh: make(chan struct{}), done: make(chan struct{}), state: StateStarting, since: b.now(), judge: -1}
}

// Name is the input's name (it can change: a renamed network camera).
func (in *Input) Name() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.name
}

func (in *Input) rename(name string) {
	in.mu.Lock()
	in.name = name
	in.mu.Unlock()
}

func (in *Input) snapshot() (state string, since time.Time) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.state, in.since
}

func (in *Input) rawPeak() float64 {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.peak
}

// info is its part of the state; the bus fills in its settings.
func (in *Input) info() InputState {
	in.mu.Lock()
	defer in.mu.Unlock()
	d := InputState{ID: in.id, Slot: in.slot + 1, Kind: in.kind, Name: in.name, State: in.state, Detail: in.detail.Text,
		DetailCode: in.detail.Code, DetailArgs: in.detail.Args}
	if in.hasPeak && in.state == StateLive {
		p := pyRound1(max(in.peak, -90))
		d.PeakDb = &p
	}
	return d
}

func (in *Input) setPeak(v float64, ok bool) {
	in.mu.Lock()
	in.peak, in.hasPeak = v, ok
	in.mu.Unlock()
}

func (in *Input) setState(state string, d detail.D) {
	in.mu.Lock()
	if state == in.state && d.Same(in.detail) {
		in.mu.Unlock()
		return
	}
	changed := state != in.state
	if changed {
		in.since = in.bus.now()
	}
	in.state, in.detail = state, d
	name := in.name
	in.mu.Unlock()
	if changed {
		code := "audio_input_state"
		if state == StateError {
			code = "audio_input_error"
		}
		elog.Event("audio", code, "slot", in.slot+1, "input", name, "state", stateText[state], "detail", d.Text, "code", d.Code)
	}
	in.bus.changed()
}

func (in *Input) stop() { in.stopOnce.Do(func() { close(in.stopCh) }) }

// wait sleeps d or until stopped; true if stopped.
func (in *Input) wait(d time.Duration) bool {
	if d <= 0 {
		select {
		case <-in.stopCh:
			return true
		default:
			return false
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-in.stopCh:
		return true
	case <-t.C:
		return false
	}
}

func (in *Input) stopping() bool { return in.wait(0) }

// present: a USB card is there while /proc/asound/<card> exists; a Bluetooth device while the app's file lists it
// with the same profile (connected, its PCM up and still used as an input).
func (in *Input) present() bool {
	switch in.kind {
	case KindUSB:
		_, err := os.Stat(filepath.Join(in.bus.asound, in.id[len("alsa:"):]))
		return err == nil
	case KindBluetooth:
		for _, d := range in.bus.bt.Get().In {
			if btaudio.ID(d.Addr) == in.id && btaudio.ALSADevice(d) == in.device {
				return true
			}
		}
		return false
	}
	return true
}

func (in *Input) run() {
	defer close(in.done)
	fails := 0
	for !in.stopping() {
		if !in.present() {
			in.setState(StateGone, detail.New("gone"))
			in.wait(time.Second)
			continue
		}
		if in.kind == KindNetwork && !in.netReady() {
			in.wait(time.Second)
			continue
		}
		t0 := time.Now()
		reason := in.stream()
		if in.stopping() {
			break
		}
		if time.Since(t0) > stableAfter {
			fails = 0
		} else {
			fails++
		}
		if reason.Code != "" {
			in.setState(StateError, reason)
		}
		d := retryNoWait
		if fails > 0 {
			d = backoff[min(fails, len(backoff))-1]
		}
		in.wait(d)
	}
}

// netReady: is the network camera publishing, with audio that can be read? If not, "no signal" and wait.
func (in *Input) netReady() bool {
	if in.bus.net == nil {
		in.setState(StateNoSignal, detail.New("net_receiver_down"))
		return false
	}
	st, err := in.bus.net.State(netcam.PathOf(in.id))
	if err != nil {
		in.setState(StateNoSignal, detail.New("net_receiver_down"))
		return false
	}
	if !st.Ready {
		in.setState(StateNoSignal, detail.New("net_waiting"))
		return false
	}
	if st.Audio == "" {
		in.setState(StateNoSignal, detail.New("net_no_audio"))
		return false
	}
	in.codec = st.Audio
	return true
}

// stream opens the input and watches it until it fails; the reason ("" when asked to stop). A panic is
// an internal failure of this input only.
func (in *Input) stream() (reason detail.D) {
	defer func() {
		if r := recover(); r != nil {
			elog.Event("audio", "audio_input_panic", "input", in.Name(), "err", fmt.Sprint(r))
			reason = detail.New("internal_error", "error", fmt.Sprint(r))
		}
	}()
	m := in.bus.media
	pipe, err := m.Parse(fmt.Sprintf("audio%d", in.slot+1), in.bus.descFor(in.kind, in.device, in.codec))
	if err != nil {
		return detail.New("build_failed", "error", err.Error())
	}
	if in.kind == KindNetwork {
		// Only the audio track (the video goes in its own session)
		if c := pipe.ByName("cap"); c != nil {
			c.SelectStream("audio")
		}
	}
	snk := pipe.ByName("snk")
	if snk == nil {
		pipe.Close()
		return detail.New("no_sink")
	}
	bridge := in.link.bridge
	start := bridge.Stats(false, false).Frames
	in.link.attach(in, snk) // a new judge for each opening
	in.judge = -1
	if err := pipe.SetState(gst.StatePlaying); err != nil {
		reason := detail.New("start_failed")
		if msg := pipe.Pop(0, gst.MessageError); msg != nil && msg.Text != "" {
			reason = detail.New("stream_error", "error", msg.Text)
		}
		in.close(pipe)
		return reason
	}
	defer in.close(pipe)
	t0 := m.Monotonic()
	for !in.stopping() {
		msg := pipe.Pop(popEvery, gst.MessageError|gst.MessageEOS|gst.MessageElement)
		now := m.Monotonic()
		if msg != nil && msg.Type == gst.MessageElement {
			// The level, and on to the usual checks: skipping them on every level message left the input
			// "opening" forever (MIXER.md §3, trap 26)
			if msg.Text == "level" {
				in.setPeak(ParsePeak(msg.Debug))
			}
			msg = nil
		}
		if msg != nil {
			if msg.Type == gst.MessageError {
				return detail.StreamError(msg.Text)
			}
			return detail.New("audio_ended")
		}
		st := bridge.Stats(false, false)
		in.logJudge(st)
		switch {
		case st.Frames > start && now-st.LastFrame < liveWithin:
			in.setState(StateLive, detail.D{})
		case in.kind == KindI2S:
			// The TC358743 stops the I2S clock without a camera or without sound: normal, not reopened
			in.setState(StateNoSignal, detail.New("hdmi_no_audio"))
		case in.kind == KindBluetooth:
			// A phone that is not playing sends nothing, and BlueALSA's capture just waits: normal, not reopened
			// (reopening would only queue behind the read that waits, BLUETOOTH.md §4)
			in.setState(StateNoSignal, detail.New("bt_no_audio"))
		case now-max(st.LastFrame, t0) > Watchdog:
			return detail.New("no_samples", "seconds", fmt.Sprintf("%.0f", Watchdog.Seconds()))
		}
		if !in.present() {
			return detail.New("gone")
		}
	}
	return detail.D{}
}

// logJudge logs each decision of the bridge's capture-time judge for this opening (CaptureJudge's log).
func (in *Input) logJudge(st gst.BridgeStats) {
	if st.Judge < 0 || st.Judge == in.judge {
		return
	}
	in.judge = st.Judge
	elog.Event("audio", "capture_judge", "input", in.Name(), "valid", st.Judge == 1, "min_age_ms", fmtMs(st.JudgeMin))
}

// close detaches the bridge and sets the pipeline to NULL here, in the input's goroutine: closing the
// I2S without clock takes ~5 s (MIXER.md §3, trap 19), never on the engine's goroutine.
func (in *Input) close(pipe gst.Pipeline) {
	in.link.detach(in)
	pipe.Close()
}
