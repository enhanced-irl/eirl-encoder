//go:build linux && cgo

package audio

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/probe"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// With real GStreamer (the build image: base plugins, no sound cards): the audio part of the program as
// pipeline.Program builds it, and two inputs whose alsasrc is replaced by audiotestsrc. It checks the
// whole chain: input pipeline → bridge in C → aaN → the audiomixer pad PadName(N) (sink_0 is the
// silence) → the program, and the volumes and fades, measuring the program's samples in an appsink.
// With the `level` element (gst-plugins-good, on the board but not in the build image) it also checks the
// level messages' text as ParsePeaks reads it, per input and for the program.
func TestRealGStreamerBus(t *testing.T) {
	g := gst.New()
	hasLevel := g.HasElement("level")
	pgmLevel := "identity name=pgmlevel"
	if hasLevel {
		pgmLevel = "level name=pgmlevel interval=50000000 post-messages=true"
	}
	var b strings.Builder
	b.WriteString("audiotestsrc is-live=true wave=silence ! " + Caps + " ! amix. ")
	for i := range 2 {
		n := string(rune('0' + i))
		b.WriteString("appsrc name=aa" + n + " is-live=true format=time do-timestamp=true min-latency=0 max-latency=500000000 " +
			"max-bytes=0 block=false caps=" + Caps + ",layout=interleaved ! queue name=aq" + n +
			" max-size-time=1000000000 leaky=downstream ! amix. ")
	}
	b.WriteString("audiomixer name=amix latency=100000000 output-buffer-duration=40000000 ! audioconvert ! audioresample ! " + Caps +
		" ! " + pgmLevel + " ! appsink name=out sync=true max-buffers=4 drop=true enable-last-sample=false")
	pgm, err := g.Parse("program", b.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pgm.Close()
	pgm.FixBase(g.Now())

	asound := t.TempDir()
	if err := os.Mkdir(filepath.Join(asound, "T2"), 0o755); err != nil {
		t.Fatal(err)
	}
	bus, err := New(Host{Media: g, Program: pgm}, Config{Slots: 2, ConfigMain: "i2s", Sync: 150 * time.Millisecond, ASound: asound})
	if err != nil {
		t.Fatal(err)
	}
	// The I2S: a sine at half scale (−6 dB); the USB card: a sine at −20 dB. Different frequencies: two
	// 440 Hz sines with a random phase between them mixed anywhere from 0.15 to 0.35 of full scale, and the
	// mix's check failed now and then; with 440 and 1000 Hz their peaks meet many times a second
	bus.descFor = func(kind, device, codec string) string {
		vol, freq := "0.5", "440"
		if kind == KindUSB {
			vol, freq = "0.1", "1000"
		}
		d := Desc(kind, device, codec)
		tail := d[strings.Index(d, " ! level "):]
		if !hasLevel {
			tail = tail[strings.Index(tail, " ! appsink "):]
		}
		return "audiotestsrc is-live=true wave=sine freq=" + freq + " volume=" + vol + " ! " + Caps + tail
	}
	bus.Start(false)
	bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{{ID: "alsa:T2", Name: "T2"}, {ID: "i2s"}}})
	if err := pgm.SetState(gst.StatePlaying); err != nil {
		t.Fatal(err)
	}
	defer func() {
		bus.Stop()
		if n := bus.Wait(time.Now().Add(3 * time.Second)); n != 0 {
			t.Errorf("%d inputs still closing", n)
		}
	}()

	var mu sync.Mutex
	var pgmErr string
	stop := make(chan struct{})
	defer close(stop)
	go func() { // the engine's program watcher, also handing level messages to the bus
		for {
			select {
			case <-stop:
				return
			default:
			}
			if m := pgm.Pop(100*time.Millisecond, gst.MessageError|gst.MessageEOS|gst.MessageElement); m != nil && !bus.OnProgramMessage(m) {
				mu.Lock()
				pgmErr = m.Src + ": " + m.Text
				mu.Unlock()
			}
		}
	}()
	out := pgm.ByName("out")
	// peak is the program's peak in dBFS over ~200 ms of samples (S16LE), and the level's when there is one
	peak := func() (float64, float64) {
		m := 0
		for range 5 {
			for i, buf := 0, out.PullBytes(200*time.Millisecond); i+1 < len(buf); i += 2 {
				v := int(int16(binary.LittleEndian.Uint16(buf[i:])))
				m = max(m, v, -v)
			}
		}
		db := max(-90, 20*math.Log10(float64(m)/32768))
		lv := db
		if hasLevel {
			if p := bus.ProgramPeaks(); len(p) == 2 {
				lv = max(p[0], p[1])
			} else {
				lv = 1000
			}
		}
		return db, lv
	}
	waitPeak := func(what string, lo, hi float64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			bus.Tick(time.Now())
			p, lv := peak()
			if p >= lo && p <= hi && lv >= lo-1 && lv <= hi+1 {
				return
			}
			mu.Lock()
			e := pgmErr
			mu.Unlock()
			if e != "" || time.Now().After(deadline) {
				t.Fatalf("%s: program peak %.1f, level %.1f (want %v…%v); program message %q; inputs %+v", what, p, lv, lo, hi, e,
					bus.State(View{}).Sources)
			}
		}
	}

	waitPeak("the I2S on air", -7.5, -4.5)
	st := bus.State(View{})
	for _, s := range st.Sources {
		if s.State != StateLive || (hasLevel && s.PeakDb == nil) {
			t.Errorf("input not live (or without a level): %+v", s)
		}
	}
	if p := bus.InputPeaks(); hasLevel && (p["i2s"] < -7.5 || p["i2s"] > -4.5 || p["alsa:T2"] < -21.5 || p["alsa:T2"] > -18.5) {
		t.Errorf("input peaks: %v", p)
	}
	if !hasLevel {
		t.Log("no level element here: level messages not checked")
	}
	bus.Command("audio", map[string]any{"source": "alsa:T2"}, View{})
	waitPeak("the USB on air", -21.5, -18.5)
	bus.Command("audio_mode", map[string]any{"mode": "mix"}, View{})
	bus.Command("audio_mix", map[string]any{"source": "i2s", "on": true, "gain": -6.0}, View{})
	waitPeak("the mix", -11, -7) // 0.25 + 0.1 of full scale
	bus.SetSilenced(true)
	waitPeak("the Pause with silence", -90, -80)
}
