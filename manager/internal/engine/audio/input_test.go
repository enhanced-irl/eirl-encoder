package audio

import (
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/netcam"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/probe"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst/fake"
)

// The input goroutine with the fake GStreamer: what the Python AudioSource.run/stream did on the board.

func stateOf(in *Input) (string, string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.state, in.detail.Text
}

func waitState(t *testing.T, in *Input, state, detail string) {
	t.Helper()
	eventually(t, state+" "+detail, func() bool { s, d := stateOf(in); return s == state && d == detail })
}

// opened waits for a pipeline with this label newer than prev (nil: any).
func (r *rig) opened(t *testing.T, label string, prev *fake.Pipeline) *fake.Pipeline {
	t.Helper()
	var p *fake.Pipeline
	eventually(t, "pipeline "+label, func() bool { p = r.media.Find(label); return p != nil && p != prev })
	return p
}

func (r *rig) attached(slot int) *Input {
	l := r.bus.links[slot]
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.owner
}

func TestInputI2SLifecycle(t *testing.T) {
	r := newRig(t, 2, Config{Sync: 150 * time.Millisecond})
	r.bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{{ID: "i2s"}}})
	in := r.bus.slots[0]
	p := r.opened(t, "audio1", nil)
	if !strings.HasPrefix(p.Desc, "alsasrc device=hw:tegrasndt210ref,1 provide-clock=false") {
		t.Errorf("desc: %s", p.Desc)
	}
	br := r.media.Bridges[0]
	if br.Params.Sync != 150*time.Millisecond {
		t.Errorf("bridge: %+v", br.Params)
	}
	// Without samples the I2S is "no signal" and is not reopened (closing it takes ~5 s)
	waitState(t, in, StateNoSignal, detailNoHDMI)
	br.Frames(1)
	waitState(t, in, StateLive, "")
	p.Post(&gst.Message{Type: gst.MessageElement, Src: "level0", Text: "level", Debug: level})
	eventually(t, "peak", func() bool { d := in.info(); return d.PeakDb != nil && *d.PeakDb == -9.5 })
	// A level message never keeps it from the checks (trap 26): the samples stop, no signal again
	r.media.Advance(2 * time.Second)
	p.Post(&gst.Message{Type: gst.MessageElement, Src: "level0", Text: "level", Debug: level})
	waitState(t, in, StateNoSignal, detailNoHDMI)
	if d := in.info(); d.PeakDb != nil {
		t.Error("peakDb while not live")
	}
	// An error closes it, detaches the bridge and reopens after the backoff (1 s)
	p.Post(&gst.Message{Type: gst.MessageError, Src: "alsasrc0", Text: "Could not read from resource."})
	waitState(t, in, StateError, "Could not read from resource.")
	if r.attached(0) != nil {
		t.Error("bridge attached after the error")
	}
	r.opened(t, "audio1", p)
	eventually(t, "attached again", func() bool { return r.attached(0) == in })
	if !strings.Contains(r.logs.String(), `[audio] event=audio_input_error slot=1 input="HDMI de la placa" state=error detail="Could not read from resource."`) {
		t.Errorf("log: %s", r.logs.String())
	}
	r.bus.Stop()
	if r.bus.Wait(time.Now().Add(3*time.Second)) != 0 || r.attached(0) != nil {
		t.Error("stop: still running or attached")
	}
}

func TestInputUSBWatchdogAndGone(t *testing.T) {
	r := newRig(t, 1, Config{})
	r.card("Mic", true)
	r.bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{{ID: "alsa:Mic", Name: "Mic"}}})
	in := r.bus.slots[0]
	p := r.opened(t, "audio1", nil)
	if !strings.Contains(p.Desc, "alsasrc device=hw:Mic,0 provide-clock=false latency-time=40000 ! audioconvert ! audioresample") {
		t.Errorf("desc: %s", p.Desc)
	}
	waitState(t, in, StateStarting, "") // no samples yet, within the watchdog
	r.media.Advance(7 * time.Second)
	waitState(t, in, StateError, "sin muestras 6 s")
	r.opened(t, "audio1", p)
	r.card("Mic", false)
	waitState(t, in, StateGone, detailGone)
}

func TestInputBuildFailure(t *testing.T) {
	r := newRig(t, 1, Config{})
	r.media.ParseErr["audio1"] = &gst.ParseError{Msg: "no element \"alsasrc\""}
	r.bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{{ID: "i2s"}}})
	waitState(t, r.bus.slots[0], StateError, `no se puede construir: no element "alsasrc"`)
}

func TestInputEndOfStream(t *testing.T) {
	r := newRig(t, 1, Config{})
	r.bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{{ID: "i2s"}}})
	p := r.opened(t, "audio1", nil)
	p.Post(&gst.Message{Type: gst.MessageEOS, Src: "audio1"})
	waitState(t, r.bus.slots[0], StateError, detailEnd)
}

func TestInputNetwork(t *testing.T) {
	r := newRig(t, 1, Config{Sync: 100 * time.Millisecond, NetSync: 150 * time.Millisecond})
	r.net.set(netcam.Path{}, errDown)
	r.bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{{ID: "net:cam1", Kind: "network", Name: "Móvil <1>"}}})
	in := r.bus.slots[0]
	if in.Name() != "Móvil 1" || in.device != "rtsp://127.0.0.1:8554/cam1" || r.media.Bridges[0].Params.Extra != 150*time.Millisecond {
		t.Errorf("input: %q %q %+v", in.Name(), in.device, r.media.Bridges[0].Params)
	}
	waitState(t, in, StateNoSignal, detailNoReceiver)
	r.net.set(netcam.Path{}, nil)
	waitState(t, in, StateNoSignal, "Esperando a la cámara")
	r.net.set(netcam.Path{Ready: true, Video: "h264"}, nil)
	waitState(t, in, StateNoSignal, detailNoAudio)
	r.net.set(netcam.Path{Ready: true, Video: "h264", Audio: "opus"}, nil)
	p := r.opened(t, "audio1", nil)
	// Not the I2S: without samples, the watchdog reopens it
	r.media.Advance(7 * time.Second)
	waitState(t, in, StateError, "sin muestras 6 s")
	if !strings.Contains(p.Desc, "rtpopusdepay ! opusdec") || p.El("cap").Select != "audio" { // read after the state: ordered
		t.Errorf("desc %s select %q", p.Desc, p.El("cap").Select)
	}
}

func TestStopLeavesASlowCloseBehind(t *testing.T) {
	r := newRig(t, 1, Config{})
	block := make(chan struct{})
	r.media.BlockClose["audio1"] = block // the I2S without clock: NULL takes ~5 s (trap 19)
	r.bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{{ID: "i2s"}}})
	r.opened(t, "audio1", nil)
	eventually(t, "attached", func() bool { return r.attached(0) != nil })
	r.bus.Stop()
	t0 := time.Now()
	if n := r.bus.Wait(time.Now().Add(300 * time.Millisecond)); n != 1 || time.Since(t0) > 2*time.Second {
		t.Errorf("pending %d after %v", n, time.Since(t0))
	}
	if r.attached(0) != nil {
		t.Error("bridge still attached while closing")
	}
	close(block)
	if n := r.bus.Wait(time.Now().Add(3 * time.Second)); n != 0 {
		t.Errorf("pending %d", n)
	}
}

func TestSlotReuseKeepsTheNewInputAttached(t *testing.T) {
	r := newRig(t, 1, Config{})
	b := r.bus
	old := newInput(b, 0, "alsa:Old", KindUSB, "Old", "")
	newer := newInput(b, 0, "alsa:New", KindUSB, "New", "")
	link := b.links[0]
	link.attach(old, &fake.Element{})
	link.attach(newer, r.pgm.El("amix"))
	link.detach(old) // the old one closing late
	if r.media.Bridges[0].Attached != "amix" {
		t.Errorf("attached %q", r.media.Bridges[0].Attached)
	}
	link.detach(newer)
	if r.media.Bridges[0].Attached != "" {
		t.Error("not detached")
	}
}

// The details' texts, byte for byte as the Python engine wrote them (the state contract)
const (
	detailGone       = "desconectada"
	detailNoHDMI     = "sin audio HDMI"
	detailNoReceiver = "el receptor de cámaras no responde"
	detailNoAudio    = "la cámara no manda audio"
	detailEnd        = "fin del audio"
)
