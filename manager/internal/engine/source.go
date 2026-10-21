package engine

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/camera"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/detail"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/hdmi"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/netcam"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/probe"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/scene"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/usbhub"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// Timings of a source's life (engine_mixer.py).
const (
	watchdog   = 3 * time.Second // without frames while live: reopen it
	firstFrame = 6 * time.Second // for the first frame after opening
	checkEvery = 2 * time.Second // still the same HDMI mode / device / publisher?
	thumbEvery = time.Second     // a JPEG thumbnail of each source for the web
)

var backoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// closeWait is how long nvv4l2camerasrc gets to close before it is unstuck (a variable for the tests).
var closeWait = 2 * time.Second

// notReady is why a source cannot open now: the state to show and how long to wait.
type notReady struct {
	state  string
	detail detail.D
	wait   time.Duration
}

func (n *notReady) Error() string { return n.detail.Text }

// Source is one source in its slot: one goroutine with at most one live pipeline at a time, so at most
// one retry is ever pending (MIXER.md §3, trap 10). Its frames cross to the program in C, through the
// slot's bridge; the goroutine only opens, watches and closes.
type Source struct {
	e      *Engine
	slot   int // 0-based
	id     string
	kind   string // hdmi-csi | usb | network | test
	node   string // HDMI: /dev/videoN; network: the RTSP URL; USB: from the probe
	bridge gst.Bridge

	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}
	// prev are the sources this one must outlive: the one it replaced in its slot (a closing source
	// never takes the slot's bridge back from its successor) and itself in the slot it was moved out
	// of (a device is opened once). It starts only once their goroutines have ended.
	prev []*Source

	mu        sync.Mutex // the fields below are read by the engine (state, periodic, scene)
	name      string
	state     string
	detail    detail.D
	format    string
	mode      string
	fps       float64
	since     time.Time // in the current state since
	aspect    scene.Aspect
	audioCard string // the audio input of the same device
	choice    *probe.Result
	usbDev    string    // USB: its device (1-2.1), to cut its port's power if it hangs
	usbIdent  string    // USB: which device that is (usbhub.Sys.Ident), to tell it from another on its port
	unplugged time.Time // USB: since when it has been unplugged (zero = it is not): the actor frees its slot
	held      bool      // USB: streaming with its frames held, no signal at its input (signalWatch)
	lastFrame time.Time // the last time it gave frames (usb_on_air's "recently live")
	// USB: the camera's image settings as last read (SOURCES.md §7) and the node they were read from
	controls []camera.Control
	camNode  string

	// USB: one request to the camera at a time (its opening's and the commands'); the commands wait in camJobs
	// for the camera's own goroutine (cameraLoop)
	camMu   sync.Mutex
	camJobs chan camJob
	camWG   sync.WaitGroup // cameraLoop: done waits for it, so a successor never opens the camera mid-command

	// Only the goroutine
	timings   pipeline.Timings
	nvcam     bool
	stuckDone chan struct{} // the close of an nvv4l2camerasrc that never returned; from then on, v4l2src
	jpegParse bool
	noImage   int
	net       netcam.Path
	recovery  *usbhub.Recovery // USB: power cut of a hung capture card (MIXER.md §16)
	sig       *signalWatch     // USB: its card's signal flag, while it streams (SOURCES.md §9)
	sigLogged string           // what openSignal last logged
}

func newSource(e *Engine, slot int, id, kind, name, node string) *Source {
	s := &Source{e: e, slot: slot, id: id, kind: kind, name: name, node: node, bridge: e.bridges[slot],
		stopCh: make(chan struct{}), done: make(chan struct{}), state: StateStarting, since: time.Now()}
	if kind == "network" {
		s.audioCard = id // its audio is the input net:camN
	}
	if kind == "usb" {
		s.recovery = usbhub.NewRecovery(e.hubs)
		s.camJobs = make(chan camJob, camQueue)
	}
	return s
}

// sourceHost is what the USB recovery needs from a source and the engine.
type sourceHost struct{ s *Source }

func (h sourceHost) SetState(state string, d detail.D) { h.s.setState(state, d) }

func (h sourceHost) OnAirIfEmitting() (map[string]usbhub.OnAir, error) { return h.s.e.onAirFor(h.s) }

func (h sourceHost) Wait(d time.Duration) bool { return h.s.wait(d) }

// noteFor: what the USB recovery says of a hung source (waiting or given up) rules over each retry's
// error; the zero detail when it has nothing to say.
func (s *Source) noteFor(state string) detail.D {
	if s.recovery == nil {
		return detail.D{}
	}
	return s.recovery.NoteFor(state)
}

// stateText is how the log names each state (STATE_TEXT).
var stateText = map[string]string{StateStarting: "opening", StateLive: "live", StateNoSignal: "no_signal",
	StateUnsupported: "unsupported", StateGone: "gone", StateError: "error", StateHidden: "hidden"}

func (s *Source) setState(state string, d detail.D) {
	s.mu.Lock()
	if state == s.state && d.Same(s.detail) {
		s.mu.Unlock()
		return
	}
	changed := state != s.state
	if changed {
		s.since = time.Now()
	}
	s.state, s.detail = state, d
	name, kind := s.name, s.kind
	s.mu.Unlock()
	if changed {
		elog.Event("mixer", sourceEvent(kind, state, d.Code), "slot", s.slot+1, "source", name, "state", stateText[state],
			"detail", d.Text, "code", d.Code)
	}
	s.e.changed()
}

// sourceEvent is the stable code of a source's state change. The app turns some of them into the
// kinds the cloud forwards (signal_lost, mode_changed), as it did with the Python engine's phrases.
func sourceEvent(kind, state, code string) string {
	switch {
	case code == "hdmi_signal_lost", kind == "hdmi-csi" && state == StateNoSignal && code == "hdmi_no_signal":
		return "signal_lost"
	case code == "hdmi_mode_changed":
		return "mode_changed"
	case state == StateUnsupported && (code == "hdmi_interlaced" || code == "hdmi_too_fast"):
		return "signal_unsupported"
	case state == StateError:
		return "source_error"
	}
	return "source_state"
}

// Info is what goes into the state.
func (s *Source) Info() SourceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SourceState{ID: s.id, Slot: s.slot + 1, Kind: s.kind, Name: s.name, State: s.state, Detail: s.detail.Text,
		DetailCode: s.detail.Code, DetailArgs: s.detail.Args,
		Format: s.format, Mode: s.mode, FPS: round1(s.fps), Controls: slices.Clone(s.controls)}
}

func (s *Source) snapshot() (state string, since time.Time, aspect scene.Aspect) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.since, s.aspect
}

func (s *Source) stop() { s.stopOnce.Do(func() { close(s.stopCh) }) }

// wait sleeps d or until stopped; true if stopped.
func (s *Source) wait(d time.Duration) bool {
	if d <= 0 {
		select {
		case <-s.stopCh:
			return true
		default:
			return false
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.stopCh:
		return true
	case <-t.C:
		return false
	}
}

func (s *Source) stopping() bool { return s.wait(0) }

func (s *Source) run() {
	defer close(s.done)
	// The camera's command in flight ends before done closes (deferred after it: runs first)
	defer s.camWG.Wait()
	for _, p := range s.prev { // read-only after newSource: whoever waits on it holds it, no more
		select {
		case <-p.done:
		case <-s.stopCh:
			return
		}
	}
	if s.camJobs != nil {
		s.camWG.Add(1)
		go func() {
			defer s.camWG.Done()
			s.cameraLoop()
		}()
	}
	fails := 0
	for !s.stopping() {
		if s.e.isHidden(s.id) {
			// Hidden from the web: not captured (frees the USB bus and the VIC) until shown
			s.setState(StateHidden, detail.D{})
			s.clearControls()
			if s.recovery != nil {
				s.recovery.Forget()
			}
			if s.kind == "usb" {
				_, err := os.Stat(s.id)
				s.noteUnplugged(err != nil && s.physicallyGone())
			}
			s.wait(500 * time.Millisecond)
			continue
		}
		if s.recovery != nil {
			state, _, _ := s.snapshot()
			s.mu.Lock()
			name, dev := s.name, s.usbDev
			s.mu.Unlock()
			if s.recovery.Recover(sourceHost{s}, name, state, dev, time.Now()) {
				continue
			}
		}
		desc, err := s.prepare()
		if err != nil {
			var nr *notReady
			if errors.As(err, &nr) {
				d := nr.detail
				if n := s.noteFor(nr.state); n.Code != "" {
					d = n
				}
				s.setState(nr.state, d)
				s.wait(nr.wait)
			} else {
				elog.Event("mixer", "source_prepare_failed", "source", s.name, "err", err)
				s.setState(StateError, detail.New("prepare_failed"))
				s.wait(5 * time.Second)
			}
			continue
		}
		t0 := time.Now()
		reason := s.stream(desc)
		if s.stopping() {
			break
		}
		if reason.Code == "" {
			continue // hidden meanwhile: not a failure, and no backoff before showing it as hidden
		}
		if time.Since(t0) > 30*time.Second {
			fails = 0
		} else {
			fails++
		}
		state := StateError
		if s.kind == "hdmi-csi" || s.kind == "network" {
			state = StateNoSignal
		}
		if n := s.noteFor(state); n.Code != "" {
			reason = n
		}
		s.setState(state, reason)
		d := 500 * time.Millisecond
		if fails > 0 {
			d = backoff[min(fails, len(backoff))-1]
		}
		s.wait(d)
	}
}

func (s *Source) prepare() (string, error) {
	w, h := s.e.width, s.e.height
	switch s.kind {
	case "hdmi-csi":
		return s.prepareCSI(w, h)
	case "usb":
		return s.prepareUSB(w, h)
	case "network":
		return s.prepareNet(w, h)
	}
	s.mu.Lock()
	s.format, s.mode = "barras", fmt.Sprintf("%dfps", s.e.fps)
	s.mu.Unlock()
	return s.e.plat.Test(w, h, s.e.fps), nil
}

func (s *Source) prepareCSI(w, h int) (string, error) {
	if s.stuckDone != nil && !waitDone(s.stuckDone, 0) {
		return "", &notReady{StateError, detail.New("hdmi_hung"), 10 * time.Second}
	}
	// The mode must be STABLE: at start, or when the camera is switched on, the TC358743 announces other
	// modes (640x480) until it locks (MIXER.md §3, trap 11). Three equal reads, 0.5 s apart.
	var reads []pipeline.Timings
	for i := range 3 {
		t, ok := hdmi.QueryDV(s.node, s.e.run)
		if !ok {
			return "", &notReady{StateNoSignal, detail.New("hdmi_no_signal"), time.Second}
		}
		reads = append(reads, t)
		if i < 2 && s.wait(500*time.Millisecond) {
			return "", &notReady{StateStarting, detail.D{}, 0}
		}
	}
	for _, r := range reads {
		if r.W != reads[0].W || r.H != reads[0].H || r.FPS != reads[0].FPS {
			return "", &notReady{StateStarting, detail.New("hdmi_locking"), 500 * time.Millisecond}
		}
	}
	t := reads[2]
	mode := t.Mode()
	if t.Interlaced {
		return "", &notReady{StateUnsupported, detail.New("hdmi_interlaced", "mode", mode), 5 * time.Second}
	}
	if pipeline.CSILoad(t) > 95 {
		return "", &notReady{StateUnsupported, detail.New("hdmi_too_fast", "mode", mode), 5 * time.Second}
	}
	if e := hdmi.SetMode(s.node, t, s.e.run); e != "" {
		return "", &notReady{StateError, detail.New("hdmi_set_mode_failed", "error", e), 2 * time.Second}
	}
	s.timings = t
	s.nvcam = s.e.nvcam && s.stuckDone == nil
	s.mu.Lock()
	s.format, s.mode, s.aspect = "UYVY", mode, scene.SourceAspect(t.W, t.H)
	s.mu.Unlock()
	return s.e.plat.CSI(s.node, t, w, h, s.nvcam), nil
}

func (s *Source) prepareUSB(w, h int) (string, error) {
	if _, err := os.Stat(s.id); err != nil {
		s.clearControls()
		s.noteUnplugged(s.physicallyGone())
		return "", &notReady{StateGone, detail.New("gone"), time.Second}
	}
	s.noteUnplugged(false)
	p, err := s.e.chooseUSB(s.id)
	if err != nil {
		return "", err
	}
	if !p.Complete {
		if p.HasWarning("no_bus") {
			return "", &notReady{StateUnsupported, detail.New("no_bus"), 10 * time.Second}
		}
		return "", &notReady{StateUnsupported, detail.New("no_format", "warnings", p.Warnings), 10 * time.Second}
	}
	s.node = p.Node
	dev := s.e.hubs.Sys.DeviceOf(s.node)
	ident := s.e.hubs.Sys.Ident(dev)
	s.mu.Lock()
	if dev != "" {
		s.usbDev = dev
	}
	if ident != "" {
		s.usbIdent = ident
	}
	s.choice = &p
	if p.Name != "" {
		s.name = p.Name
	}
	s.audioCard = p.ALSACard
	s.format, s.mode = p.Format, p.Mode()
	s.aspect = scene.SourceAspect(p.Width, p.Height)
	name := s.name
	s.mu.Unlock()
	if p.HasWarning("usb2") {
		elog.Event("mixer", "usb2", "source", name)
	}
	// Webcams: auto exposure must not lower the fps with little light (like the launcher), and the settings the
	// user gave this camera are put back
	s.openCamera(s.node, name)
	return s.e.plat.USB(p.Probe, w, h, s.jpegParse), nil
}

// physicallyGone: has the USB camera been unplugged? Its port is empty, or another device is on it. Not
// when it has only dropped off the bus and is still on its port: that is a hung one, which the recovery
// may cure by cutting the power, and which says "unplug it" when it gives up. Off the actor: asking the
// hub is a control transfer.
func (s *Source) physicallyGone() bool {
	s.mu.Lock()
	dev, ident := s.usbDev, s.usbIdent
	s.mu.Unlock()
	if dev == "" {
		return true // never seen on the bus: there is nothing to recover
	}
	sys := s.e.hubs.Sys
	if now := sys.Ident(dev); now != "" {
		// Enumerated on its port: another device, or itself without its video node yet (or hung)
		return ident != "" && now != ident
	}
	plugged, known := sys.Plugged(dev)
	return !known || !plugged
}

// noteUnplugged keeps since when the camera has been unplugged (gone) or forgets it.
func (s *Source) noteUnplugged(gone bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !gone:
		s.unplugged = time.Time{}
	case s.unplugged.IsZero():
		s.unplugged = time.Now()
	}
}

// unpluggedFor is how long the camera has been unplugged (0 if it is not).
func (s *Source) unpluggedFor(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unplugged.IsZero() {
		return 0
	}
	return now.Sub(s.unplugged)
}

func (s *Source) prepareNet(w, h int) (string, error) {
	st, err := s.e.net.State(netcam.PathOf(s.id))
	if err != nil {
		return "", &notReady{StateNoSignal, detail.New("net_receiver_down"), 2 * time.Second}
	}
	if !st.Ready {
		return "", &notReady{StateNoSignal, detail.New("net_waiting"), time.Second}
	}
	if st.Video == "" {
		return "", &notReady{StateUnsupported, detail.New("net_no_video", "tracks", detail.Join(st.Tracks)), 5 * time.Second}
	}
	s.net = st
	s.mu.Lock()
	s.format, s.mode = strings.ToUpper(st.Video), ""
	s.mu.Unlock()
	return s.e.plat.Network(s.node, st.Video, w, h), nil
}

// stillValid: with the source live, every 2 s: still the same HDMI mode / device / publisher?
func (s *Source) stillValid() detail.D {
	switch s.kind {
	case "hdmi-csi":
		t, ok := hdmi.QueryDV(s.node, s.e.run)
		if !ok {
			return detail.New("hdmi_signal_lost")
		}
		if t.W != s.timings.W || t.H != s.timings.H || t.FPS != s.timings.FPS {
			s.mu.Lock()
			old := s.mode
			s.mu.Unlock()
			return detail.New("hdmi_mode_changed", "from", old, "to", fmt.Sprintf("%dx%d@%d", t.W, t.H, t.FPS))
		}
	case "usb":
		if _, err := os.Stat(s.id); err != nil {
			return detail.New("gone")
		}
	case "network":
		st, err := s.e.net.State(netcam.PathOf(s.id))
		if err == nil && (!st.Ready || st.Publisher != s.net.Publisher) {
			return detail.New("net_disconnected")
		}
		// Behind: what arrives carries seconds of backlog (the CPU cannot keep up); reopening empties it
		// (NETWORK_SOURCES.md §3, trap 16)
		if lag := s.bridge.Stats(false, true).Lag; lag > netcam.MaxLag {
			return detail.New("net_behind", "seconds", fmt.Sprintf("%.1f", lag.Seconds()))
		}
	}
	return detail.D{}
}

func (s *Source) params() gst.BridgeParams {
	p := gst.BridgeParams{Sync: s.e.sync}
	switch {
	case s.kind == "hdmi-csi" && s.nvcam:
		p.Early = pipeline.CSILatency(s.timings.FPS) // nvv4l2camerasrc stamps the arrival
	case s.kind == "hdmi-csi":
		p.RawClock = true // the Tegra CSI stamps with CLOCK_MONOTONIC_RAW (trap 24)
	case s.kind == "network":
		p.Extra = s.e.netSync // the wifi's cushion, on video and audio alike
		p.JudgeLimit = netcam.JudgeLimit
	}
	if s.e.sync == 0 {
		p.Extra = 0
	}
	return p
}

// stream opens the source and watches it until it fails. Returns the reason (zero when asked to stop
// or hidden).
func (s *Source) stream(desc string) detail.D {
	m := s.e.media
	pipe, err := m.Parse(fmt.Sprintf("source%d", s.slot+1), desc)
	if err != nil {
		return detail.New("build_failed", "error", err.Error())
	}
	snk := pipe.ByName("snk")
	if snk == nil {
		pipe.Close()
		return detail.New("no_sink")
	}
	if s.stopping() { // stopped while preparing: never take the bridge
		pipe.Close()
		return detail.D{}
	}
	if s.kind == "network" {
		// Only the video track (the audio goes in its own session)
		if cap := pipe.ByName("cap"); cap != nil {
			cap.SelectStream("video")
		}
	}
	s.bridge.SetParams(s.params())
	start := s.bridge.Stats(false, true).Frames
	s.bridge.Attach(snk)
	if err := pipe.SetState(gst.StatePlaying); err != nil {
		reason := detail.New("start_failed")
		if msg := pipe.Pop(0, gst.MessageError); msg != nil && msg.Text != "" {
			reason = detail.New("stream_error", "error", msg.Text)
		}
		s.close(pipe, snk)
		return reason
	}
	defer s.closeSignal(snk) // after close: no black frame between releasing them and detaching
	defer s.close(pipe, snk)
	t0 := time.Now()
	nextCheck := t0.Add(checkEvery)
	sigTried := s.kind != "usb"
	for !s.stopping() {
		msg := pipe.Pop(200*time.Millisecond, gst.MessageError|gst.MessageEOS)
		now := time.Now()
		st := s.bridge.Stats(false, false)
		got := st.Frames > start
		if msg != nil {
			reason := detail.New("stream_ended")
			if msg.Type == gst.MessageError {
				reason = detail.StreamError(msg.Text)
			}
			if s.needsJPEGParse(desc, msg.Src, got) {
				elog.Event("mixer", "mjpeg_parse_enabled", "source", s.name, "why", "decoder_error")
				s.jpegParse = true
			}
			return reason
		}
		if got && !sigTried {
			// Its flag, once the card streams: no HID request while it starts (SOURCES.md §9.2)
			sigTried = true
			s.openSignal()
		}
		s.checkSignal(snk, now)
		if got {
			s.noImage = 0
			s.mu.Lock()
			s.lastFrame = now
			dev := s.usbDev
			s.mu.Unlock()
			if s.recovery != nil {
				s.recovery.Update(true, now, dev)
			}
			want, d := StateLive, detail.D{}
			if s.held {
				// Frames, but none from its HDMI input: the slate, as for the board's HDMI (SOURCES.md §9)
				want, d = StateNoSignal, detail.New("hdmi_no_signal")
			}
			if cur, _, _ := s.snapshot(); cur != want {
				s.setState(want, d)
			}
			if age := m.Monotonic() - st.LastFrame; age > watchdog {
				// It stops without an error (the Cam Link with -71): there is no other way to know
				return detail.New("frames_stopped", "seconds", fmt.Sprintf("%.0f", watchdog.Seconds()))
			}
		} else if now.Sub(t0) > firstFrame {
			s.noImage++
			if s.needsJPEGParse(desc, "", false) {
				elog.Event("mixer", "mjpeg_parse_enabled", "source", s.name, "why", "no_image")
				s.jpegParse = true
			}
			return detail.New("no_image")
		}
		if s.kind == "network" {
			s.readDecodedMode(pipe)
		}
		if s.e.isHidden(s.id) {
			return detail.D{}
		}
		if !now.Before(nextCheck) {
			nextCheck = now.Add(checkEvery)
			if reason := s.stillValid(); reason.Code != "" {
				return reason
			}
		}
	}
	return detail.D{}
}

// readDecodedMode: with the first decoded frame, a network camera's real size (a phone in portrait has
// bars: the engine places the scene again when the aspect changes).
func (s *Source) readDecodedMode(pipe gst.Pipeline) {
	dec := pipe.ByName("dec")
	if dec == nil {
		return
	}
	w, h, fps, ok := netcam.DecodedMode(dec.CapsString("src"))
	if !ok {
		return
	}
	mode, aspect := netcam.ModeText(w, h, fps), scene.SourceAspect(w, h)
	s.mu.Lock()
	changed := mode != s.mode || aspect != s.aspect
	s.mode, s.aspect = mode, aspect
	s.mu.Unlock()
	if changed {
		s.e.changed()
	}
}

// needsJPEGParse: reopen with jpegparse? An MJPEG source without it whose failure comes from the
// decoder, or TWO openings in a row without a frame: some webcams send JPEGs without an end marker. One
// is not enough: the MS2109 sometimes gives no image the first time after a restart, with it too.
func (s *Source) needsJPEGParse(desc, errSrc string, hadFrames bool) bool {
	if s.jpegParse || !s.e.plat.IsMJPEGWithoutParse(desc) {
		return false
	}
	return strings.HasPrefix(errSrc, "nvv4l2decoder") || (!hadFrames && s.noImage >= 2)
}

// close: nvv4l2camerasrc may wait forever for a frame that does not come; then STREAMOFF on its
// descriptor unsticks it. If not even that, the goroutine leaves it behind with the device open and the
// slot says so (it reopens with v4l2src).
func (s *Source) close(pipe gst.Pipeline, snk gst.Element) {
	s.bridge.DetachIf(snk)
	if !s.nvcam {
		pipe.Close()
		return
	}
	done := make(chan struct{})
	go func() { pipe.Close(); close(done) }()
	if waitDone(done, closeWait) {
		return
	}
	n := s.e.media.StreamOff(s.node)
	if waitDone(done, closeWait) {
		elog.Event("mixer", "capture_streamoff", "source", s.name, "fds", n)
		return
	}
	elog.Event("mixer", "capture_stuck", "source", s.name, "fds", n)
	s.stuckDone = done
}

// waitDone waits up to d for ch to close (d = 0: just looks).
func waitDone(ch <-chan struct{}, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	}
}
