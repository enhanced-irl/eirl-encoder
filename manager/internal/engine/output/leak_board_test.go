//go:build linux && cgo && board

package output

// On the board only (NVENC; the M5 night grew ~0.9 MB per output reconnection, ENGINE_GO.md §14): does an
// output session leave memory behind, and is it the session or the encoder? Without the engine: it can run
// while the service runs, not broadcasting (a second NVENC session).
//
//	go test -c -tags board -o leak.test ./internal/engine/output/
//	LEAK_N=60 ./leak.test -test.run 'TestBoardLeak' -test.v
//
// Every 10 rounds it prints the process' RSS and the Go heap: what grows outside the heap is C memory
// (GStreamer, the NVIDIA libraries, glibc's arenas).

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// boardProgram is the program's two taps as the engine has them: 1080p60 NV12 in GPU memory and 48 kHz
// stereo.
const boardProgram = "videotestsrc is-live=true pattern=black ! video/x-raw,format=NV12,width=1920,height=1080,framerate=60/1 " +
	"! nvvidconv ! video/x-raw(memory:NVMM),format=NV12 ! queue leaky=downstream max-size-buffers=2 " +
	"! appsink name=outv emit-signals=false sync=false async=false max-buffers=1 drop=true enable-last-sample=false " +
	"audiotestsrc is-live=true wave=silence ! audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved " +
	"! queue leaky=downstream max-size-buffers=12 " +
	"! appsink name=outa emit-signals=false sync=false async=false max-buffers=12 drop=true enable-last-sample=false"

func boardOutputDesc() string {
	return pipeline.Tegra.Output(pipeline.Output{Width: 1920, Height: 1080, FPS: 60, VKbps: 6000, AKbps: 128, GOP: 120,
		Profile: 4, Preset: 2, TwoPass: true, AEnc: "voaacenc"})
}

func leakRounds() int {
	if n, err := strconv.Atoi(os.Getenv("LEAK_N")); err == nil && n > 0 {
		return n
	}
	return 60
}

func rssMB() float64 {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "VmRSS:" {
			kb, _ := strconv.Atoi(f[1])
			return float64(kb) / 1024
		}
	}
	return 0
}

func memLine(round int) string {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	before := rssMB()
	gst.TrimMalloc()
	return fmt.Sprintf("round %3d  rss %6.1f MB  after malloc_trim %6.1f MB  go-heap %5.1f MB  go-sys %5.1f MB", round, before,
		rssMB(), float64(m.HeapInuse)/(1<<20), float64(m.Sys)/(1<<20))
}

// The output's sessions, on and off, as each reconnection does (srt-live-transmit is the test's fake).
func TestBoardLeakSessions(t *testing.T) {
	shorten(t, &measureEvery, time.Second) // the encoder's rates, to know it encodes
	dir := t.TempDir()
	elog.SetOutput(&logBuf{})
	t.Cleanup(func() { elog.SetOutput(nil) })
	t.Setenv(fakeEnv, dir)
	t.Setenv(fakeSLTMode, "connect")
	oldBin := sltBin
	sltBin = os.Args[0]
	t.Cleanup(func() { sltBin = oldBin })
	g := gst.New()
	pgm, err := g.Parse("program", boardProgram)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgm.SetState(gst.StatePlaying); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SRTURL: "srt://127.0.0.1:9", SRTMode: "caller", SRTLatencyMs: 200,
		SRTStreamID: config.NewSecret(streamID), Transport: "srt", VBitrate: 6000, VBitrateMin: 1000, ABR: true,
		ABitrate: 128, RunDir: dir, SRTStats: dir + "/srt-stats.json"}
	out, err := New(Options{Config: cfg, Media: g, Program: pgm, Desc: boardOutputDesc(), RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		out.Close(5 * time.Second)
		pgm.Close()
		out.Release()
	}()
	n := leakRounds()
	for i := 0; i <= n; i++ {
		if i%10 == 0 {
			t.Log(memLine(i))
		}
		if i == n {
			break
		}
		out.Set(true)
		waitFor(t, "encoding", func() bool { i := out.Info(); return i.State == StateOn && i.EncFPS != nil && *i.EncFPS > 30 })
		time.Sleep(time.Second)
		out.Set(false)
		waitFor(t, "off", func() bool { return out.Info().State == StateOff })
	}
}

// Only the encoder: the same description fed by a test source, to EOS and closed, without the output
// package (if this grows like the sessions, it is the NVIDIA encoder or GStreamer, not the engine).
func TestBoardLeakEncoder(t *testing.T) {
	desc := "videotestsrc num-buffers=90 pattern=black ! video/x-raw,format=NV12,width=1920,height=1080,framerate=60/1 " +
		"! nvvidconv ! video/x-raw(memory:NVMM),format=NV12 ! nvvidconv output-buffers=6 ! video/x-raw(memory:NVMM),format=I420 " +
		"! nvv4l2h264enc control-rate=1 bitrate=6000000 iframeinterval=120 idrinterval=120 profile=4 preset-level=2 " +
		"maxperf-enable=true insert-sps-pps=true insert-vui=true poc-type=2 EnableTwopassCBR=true " +
		"! h264parse config-interval=-1 ! mpegtsmux alignment=7 ! fakesink sync=false async=false"
	g := gst.New()
	n := leakRounds()
	for i := 0; i <= n; i++ {
		if i%10 == 0 {
			t.Log(memLine(i))
		}
		if i == n {
			break
		}
		p, err := g.Parse("enc", desc)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.SetState(gst.StatePlaying); err != nil {
			t.Fatal(err)
		}
		if m := p.Pop(10*time.Second, gst.MessageEOS|gst.MessageError); m == nil || m.Type != gst.MessageEOS {
			t.Fatalf("round %d: %+v", i, m)
		}
		p.Close()
	}
}
