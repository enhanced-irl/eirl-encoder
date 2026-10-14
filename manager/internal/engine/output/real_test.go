//go:build linux && cgo

package output

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// With real GStreamer (the build image, base plugins only: no NVENC, so `venc` is an identity and the
// "mux" is the raw video): the program's taps cross into the output through the C bridges, the FIFO
// carries it to srt-live-transmit (the fake), and stopping never hangs, even with a filesink blocked
// on a full FIFO whose reader does not read.

const realProgram = "videotestsrc is-live=true ! video/x-raw,format=NV12,width=64,height=36,framerate=30/1 " +
	"! appsink name=outv emit-signals=false sync=false async=false max-buffers=1 drop=true enable-last-sample=false " +
	"audiotestsrc is-live=true ! audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved " +
	"! appsink name=outa emit-signals=false sync=false async=false max-buffers=12 drop=true enable-last-sample=false"

const realOutput = `appsrc name=ov is-live=true format=time do-timestamp=false max-bytes=0 block=false ` +
	`caps="video/x-raw,format=NV12,width=64,height=36,framerate=30/1" ! queue max-size-buffers=4 leaky=downstream ` +
	`! identity name=venc ! filesink location=@FIFO@ buffer-mode=unbuffered sync=false async=false ` +
	`appsrc name=oa is-live=true format=time do-timestamp=false max-bytes=0 block=false ` +
	`caps="audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved" ! queue ! fakesink sync=false async=false`

func realOutputRig(t *testing.T, mode string) (*Output, gst.Pipeline, *logBuf) {
	t.Helper()
	dir := t.TempDir()
	logs := &logBuf{}
	elog.SetOutput(logs)
	t.Cleanup(func() { elog.SetOutput(nil) })
	t.Setenv(fakeEnv, dir)
	t.Setenv(fakeSLTMode, mode)
	oldBin := sltBin
	sltBin = os.Args[0]
	t.Cleanup(func() { sltBin = oldBin })
	g := gst.New()
	pgm, err := g.Parse("program", realProgram)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgm.SetState(gst.StatePlaying); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SRTURL: "srt://127.0.0.1:9", SRTMode: "caller", SRTLatencyMs: 200,
		SRTStreamID: config.NewSecret(streamID), Transport: "srt", VBitrate: 6000, VBitrateMin: 1000,
		ABitrate: 128, RunDir: dir, SRTStats: filepath.Join(dir, "srt-stats.json")}
	out, err := New(Options{Config: cfg, Media: g, Program: pgm, Desc: realOutput, RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		out.Close(5 * time.Second)
		pgm.Close() // the taps stop calling into the bridges: now they can go
		out.Release()
	})
	return out, pgm, logs
}

func TestRealOutputCrossesAndStops(t *testing.T) {
	shorten(t, &measureEvery, 600*time.Millisecond)
	out, _, logs := realOutputRig(t, "connect")
	time.Sleep(500 * time.Millisecond) // the taps fill while nobody emits: the idle bridges drop it
	out.Set(true)
	var i Info
	waitFor(t, "frames through the output", func() bool {
		i = out.Info()
		return i.State == StateOn && i.EncFPS != nil && *i.EncFPS > 10 && *i.InFPS > 10
	})
	t0 := time.Now()
	out.Set(false)
	waitFor(t, "off", func() bool { return out.Info().State == StateOff })
	if d := time.Since(t0); d > 3*time.Second {
		t.Errorf("stopping took %v", d)
	}
	if strings.Contains(logs.String(), "output_close_stuck") {
		t.Errorf("logs:\n%s", logs)
	}
	// Again, on the same taps: the idle bridges hand over and back
	out.Set(true)
	waitFor(t, "on again", func() bool { i = out.Info(); return i.State == StateOn })
	out.Set(false)
	waitFor(t, "off again", func() bool { return out.Info().State == StateOff })
}

// srt-live-transmit stops reading (it does not while connecting): the FIFO fills and the filesink
// blocks in write(). Stopping kills it first, so the write fails with EPIPE (not a SIGPIPE that would
// kill the engine) and the pipeline closes at once.
func TestRealStopWithAFullFIFO(t *testing.T) {
	out, _, logs := realOutputRig(t, "noread")
	out.Set(true)
	waitFor(t, "on", func() bool { return out.Info().State == StateOn })
	time.Sleep(1500 * time.Millisecond) // 64 KB of FIFO: ~20 frames of 3.4 KB
	t0 := time.Now()
	if !out.Close(4 * time.Second) {
		t.Fatal("not closed")
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Errorf("close took %v", d)
	}
	if strings.Contains(logs.String(), "output_close_stuck") {
		t.Errorf("logs:\n%s", logs)
	}
}
