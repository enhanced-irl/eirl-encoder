package pipeline

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// tokens splits a gst-launch description like the shell and gst-launch do: on spaces outside quotes,
// quotes removed. The launcher's descriptions reach Python already split (eval), so comparing tokens
// compares what GStreamer parses.
func tokens(s string) []string {
	var out []string
	var cur strings.Builder
	in, quote := false, rune(0)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// The Go engine attaches C callbacks to its appsinks instead of signals
func pythonSink(ts []string) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		if t == "emit-signals=true" {
			t = "emit-signals=false"
		}
		out[i] = t
	}
	return out
}

func sameTokens(t *testing.T, name string, got string, want []string) {
	t.Helper()
	g := tokens(got)
	for i := 0; i < len(g) || i < len(want); i++ {
		var a, b string
		if i < len(g) {
			a = g[i]
		}
		if i < len(want) {
			b = want[i]
		}
		if a != b {
			t.Errorf("%s: token %d = %q, want %q\n got: %s", name, i, a, b, strings.Join(g[max(0, i-6):min(len(g), i+6)], " "))
			return
		}
	}
}

func pythonGolden(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("testdata/python.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// testdata/python.json is generated from scripts/engine_mixer.py and engine_net.py (the frozen Python
// engine) with the inputs below
func TestSourcesMatchPython(t *testing.T) {
	g := pythonGolden(t)
	want := func(k string) []string { return pythonSink(tokens(g[k].(string))) }
	t30 := Timings{W: 1920, H: 1080, FPS: 30, HTotal: 2200, PClk: 74250000}
	t50 := Timings{W: 1920, H: 1080, FPS: 50, HTotal: 2640, PClk: 148500000}
	mj := Probe{Node: "/dev/video1", Format: "MJPG", Width: 1920, Height: 1080, FPSNum: 60, FPSDen: 1}
	nv := Probe{Node: "/dev/video2", Format: "NV12", Width: 1920, Height: 1080, FPSNum: 60000, FPSDen: 1001}
	p := Tegra
	cases := map[string]string{
		"sink_tail":          p.SinkTail(1920, 1080, 20),
		"sink_tail8":         p.SinkTail(1920, 1080, 8),
		"csi_nvcam_30":       p.CSI("/dev/video0", t30, 1920, 1080, true),
		"csi_v4l2_50":        p.CSI("/dev/video0", t50, 1920, 1080, false),
		"usb_mjpg":           p.USB(mj, 1920, 1080, false),
		"usb_mjpg_parse":     p.USB(mj, 1920, 1080, true),
		"usb_nv12":           p.USB(nv, 1920, 1080, false),
		"test60":             p.Test(1920, 1080, 60),
		"still_slate":        Still(StillSlate, 1920, 1080, true),
		"still_black":        Still(StillBlack, 1920, 1080, true),
		"still_pause":        Still(StillPause, 1920, 1080, true),
		"still_pause_notext": Still(StillPause, 1920, 1080, false),
	}
	for k, got := range cases {
		sameTokens(t, k, got, want(k))
	}
	// The network head in Python is followed by sink_tail
	for _, c := range []string{"h264", "h265"} {
		sameTokens(t, "net_"+c, p.Network("rtsp://127.0.0.1:8554/cam1", c, 1920, 1080),
			append(want("net_"+c), tokens(p.SinkTail(1920, 1080, 20))...))
	}
	if got, w := CSILatency(30), time.Duration(g["csi_latency_30"].(float64)); got != w {
		t.Errorf("CSILatency(30) = %v, want %v", got, w)
	}
	if got, w := CSILatency(50), time.Duration(g["csi_latency_50"].(float64)); got != w {
		t.Errorf("CSILatency(50) = %v, want %v", got, w)
	}
	if got := CSILoad(t50); got != int64(g["csi_load_50"].(float64)) {
		t.Errorf("CSILoad(1080p50) = %d", got)
	}
	if got := CSILoad(Timings{W: 1920, H: 1080, FPS: 60, HTotal: 2200, PClk: 148500000}); got <= 95 {
		t.Errorf("1080p60 must not fit in 2 lanes: %d %%", got)
	}
	if nv.Mode() != "1920x1080@59.94" || mj.Mode() != "1920x1080@60" {
		t.Errorf("probe modes: %s, %s", nv.Mode(), mj.Mode())
	}
	if !p.IsMJPEGWithoutParse(cases["usb_mjpg"]) || p.IsMJPEGWithoutParse(cases["usb_mjpg_parse"]) || p.IsMJPEGWithoutParse(cases["usb_nv12"]) {
		t.Error("IsMJPEGWithoutParse")
	}
}

// testdata/program-argv.txt is the program pipeline of the board's running engine (main 58c494d,
// 13-oct), as engine-run.py received it after `--`: 4 slots, 4 audio slots, 1920x1080@60, latency
// 60 ms, preview, VU meter, thumbnail and screen
func TestProgramMatchesTheBoard(t *testing.T) {
	b, err := os.ReadFile("testdata/program-argv.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := pythonSink(strings.Split(strings.TrimRight(string(b), "\n"), "\n"))
	got := Tegra.Program(Program{
		Width: 1920, Height: 1080, FPS: 60, Slots: 4, AudioSlots: 4, LatencyNs: 60_000_000,
		Live: true, LiveWidth: 640, LiveHeight: 360, LiveVKbps: 800, LiveAKbps: 96, LiveVPort: 5006, LiveAPort: 5008,
		Levels: true, LevelsPort: 5005,
		Thumb: true, ThumbWidth: 320, ThumbHeight: 180, ThumbEvery: 5, ThumbFile: "/run/eirl-engine/thumb.jpg",
		Screen: true,
	})
	sameTokens(t, "program", got, want)
}

func TestProgramWithoutOptionalBranches(t *testing.T) {
	got := Tegra.Program(Program{Width: 1280, Height: 720, FPS: 50, Slots: 2, LatencyNs: 1})
	for _, absent := range []string{"livevalve", "liveavalve", "pgmtap", "multifilesink", "udpsink", "aa0", "m2"} {
		if strings.Contains(got, absent) {
			t.Errorf("unexpected %s", absent)
		}
	}
	for _, present := range []string{"name=m1", "name=mp", "pgm.sink_2", "name=outv", "name=outa", "name=cadence", "name=phtap", "name=vsrc", "tee name=at"} {
		if !strings.Contains(got, present) {
			t.Errorf("missing %s", present)
		}
	}
}

// testdata/output.txt is the board's output pipeline (EIRL_ENGINE_OUTPUT_PIPELINE, same engine):
// 6000 kbps, GOP 120, High, preset 2, two passes, voaacenc 128 kbps
func TestOutputMatchesTheBoard(t *testing.T) {
	b, err := os.ReadFile("testdata/output.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := Tegra.Output(Output{Width: 1920, Height: 1080, FPS: 60, VKbps: 6000, AKbps: 128, GOP: 120,
		Profile: 4, Preset: 2, TwoPass: true, AEnc: "voaacenc"})
	sameTokens(t, "output", got, tokens(string(b)))
	rec := Tegra.Output(Output{Width: 1920, Height: 1080, FPS: 60, VKbps: 6000, AKbps: 128, GOP: 120, Profile: 4, Preset: 2,
		AEnc: "voaacenc", QPRange: "10,40:10,40:10,40", Record: "/tmp/x.ts"})
	if !strings.Contains(rec, `qp-range="10,40:10,40:10,40"`) || !strings.Contains(rec, "tee name=rt") || strings.Contains(rec, "EnableTwopassCBR") {
		t.Errorf("record/qp: %s", rec)
	}
}

func TestThumbs(t *testing.T) {
	got := Tegra.Thumbs(2, 1920, 1080)
	if strings.Count(got, "appsrc name=th") != 2 || !strings.Contains(got, "appsink name=thj1") ||
		!strings.Contains(got, "jpegenc quality=70") || !strings.Contains(got, "async=false") {
		t.Errorf("thumbs: %s", got)
	}
}
