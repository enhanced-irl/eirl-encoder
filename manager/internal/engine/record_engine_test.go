package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst/fake"
)

// startRecordingEngine is startEngine on a board that can record (mp4mux and NVENC present), with the
// storage helper's state file in a temporary folder: ready with a disk when ready is true.
func startRecordingEngine(t *testing.T, mode string, ready bool) (*Engine, *fake.Media, string) {
	t.Helper()
	elog.SetOutput(&strings.Builder{})
	t.Cleanup(func() { elog.SetOutput(nil) })
	root := t.TempDir()
	dir := filepath.Join(root, "EnhancedIRL")
	os.MkdirAll(dir, 0o755)
	state := filepath.Join(t.TempDir(), "state.json")
	if ready {
		os.WriteFile(state, []byte(`{"state":"ready","seq":1,"fs":"exfat","uuid":"U1","dir":"`+dir+`"}`), 0o644)
	}
	c := testConfig(t, map[string]string{"REC_MODE": mode, "REC_DIR": root, "REC_STORAGE_STATE": state})
	m := fake.New()
	m.Elements["mp4mux"], m.Elements["nvv4l2h264enc"] = true, true
	e := New(Options{Config: c, Media: m, Platform: pipeline.Tegra, Run: (&runner{list: catalogue}).run, MixerMain: "hdmi", FPS: 60})
	if err := e.Start(false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	return e, m, dir
}

// recordOf is the state's record object as mixer.json has it.
func recordOf(t *testing.T, e *Engine) map[string]any {
	t.Helper()
	e.publish()
	b, err := os.ReadFile(filepath.Join(e.runDir, "mixer.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	r, _ := st["record"].(map[string]any)
	return r
}

func waitRecord(t *testing.T, e *Engine, what string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; {
		r := recordOf(t, e)
		if r != nil && ok(r) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s: %v", what, r)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The recording's taps, the command and the state (RECORDING.md §5.2); while recording the engine is busy.
func TestRecordCommand(t *testing.T) {
	e, m, _ := startRecordingEngine(t, "manual", true)
	pgm := m.Find("program")
	if pgm.El("recv") == nil || pgm.El("reca") == nil || !strings.Contains(pgm.Desc, "output-buffers=16") {
		t.Fatal("no recording taps in the program")
	}
	r := recordOf(t, e)
	if r["mode"] != "off" || r["by"] != "start" || r["disk"].(map[string]any)["state"] != "ready" ||
		r["setting"] != "manual" || r["quality"] != "high" {
		t.Fatalf("idle: %v", r)
	}
	if ok, code := e.command("record", map[string]any{"cmd": "record", "on": "yes"}); ok || code != "bad_request" {
		t.Fatalf("bad request: %v %q", ok, code)
	}
	if ok, code := e.command("record", map[string]any{"cmd": "record", "on": true}); !ok || code != "" {
		t.Fatalf("record on: %v %q", ok, code)
	}
	r = waitRecord(t, e, "on", func(r map[string]any) bool { return r["state"] == "on" })
	if r["mode"] != "on" || r["by"] != "socket" || r["file"] == "" || !e.busy() || e.replugAllowed() {
		t.Fatalf("recording: %v busy=%v", r, e.busy())
	}
	rec := m.Find("record")
	if rec == nil || !strings.Contains(rec.Desc, "nvv4l2h264enc name=renc control-rate=0 bitrate=25000000") ||
		!strings.Contains(rec.Desc, "framerate=60/1") || rec.Base != pgm.Base {
		t.Fatalf("record pipeline: %+v", rec)
	}
	if _, err := e.onAirFor(&Source{}); err == nil {
		t.Error("the USB recovery may cut the hub while recording")
	}
	e.command("record", map[string]any{"cmd": "record", "on": false})
	waitRecord(t, e, "off", func(r map[string]any) bool { return r["state"] == "off" && r["mode"] == "off" })
	if e.busy() {
		t.Error("still busy after stopping")
	}
}

func TestRecordDisabledOrWithoutDisk(t *testing.T) {
	e, _, _ := startRecordingEngine(t, "off", true)
	if ok, code := e.command("record", map[string]any{"cmd": "record", "on": true}); ok || code != "disabled" {
		t.Fatalf("REC_MODE=off: %v %q", ok, code)
	}
	e2, _, _ := startRecordingEngine(t, "manual", false)
	if ok, code := e2.command("record", map[string]any{"cmd": "record", "on": true}); ok || code != "no_disk" {
		t.Fatalf("no disk: %v %q", ok, code)
	}
	// Without mp4mux there are no taps and no record object
	e3, _, _ := startEngine(t, nil, catalogue)
	if ok, code := e3.command("record", map[string]any{"cmd": "record", "on": true}); ok || code != "unsupported" {
		t.Fatalf("no recorder: %v %q", ok, code)
	}
	if r := recordOf(t, e3); r != nil {
		t.Fatalf("a record object without a recorder: %v", r)
	}
}

// with_output: Go live also records, Stop stops what Go live started; a recording started with REC is not
// stopped by Stop (RECORDING.md §5.2).
func TestRecordWithOutput(t *testing.T) {
	e, _, _ := startRecordingEngine(t, "with_output", true)
	e.setOutput(true, "screen")
	r := waitRecord(t, e, "on with the stream", func(r map[string]any) bool { return r["state"] == "on" })
	if r["by"] != "output" {
		t.Fatalf("by: %v", r["by"])
	}
	e.setOutput(false, "screen")
	waitRecord(t, e, "off with the stream", func(r map[string]any) bool { return r["mode"] == "off" })
	// REC first, then Go live and Stop: the recording stays
	e.command("record", map[string]any{"cmd": "record", "on": true})
	waitRecord(t, e, "on by REC", func(r map[string]any) bool { return r["state"] == "on" })
	e.setOutput(true, "screen")
	e.setOutput(false, "screen")
	if r := recordOf(t, e); r["mode"] != "on" || r["by"] != "socket" {
		t.Fatalf("Stop stopped REC's recording: %v", r)
	}
	// A repeated Go live (no change) does not restart a recording stopped by hand
	e.command("record", map[string]any{"cmd": "record", "on": false})
	e.setOutput(true, "socket")
	e.command("record", map[string]any{"cmd": "record", "on": false})
	e.setOutput(true, "socket")
	if r := recordOf(t, e); r["mode"] != "off" {
		t.Fatalf("a repeated Go live restarted the recording: %v", r)
	}
}

// output_config with only the recording's keys changed: the recorder takes them, the output keeps its
// configuration (a new one would restart a stream on air).
func TestRecordKeysLeaveTheOutputAlone(t *testing.T) {
	e, _, _ := startRecordingEngine(t, "manual", true)
	cur := e.out.Config()
	e.loadConfig = func() (*config.Config, error) {
		return testConfig(t, map[string]string{"REC_MODE": "with_output", "REC_QUALITY": "max"}), nil
	}
	if ok, code := e.reloadOutput(); !ok {
		t.Fatalf("output_config: %q", code)
	}
	if e.out.Config() != cur || e.rec.Config().RecMode != config.RecWithOutput || e.rec.Config().RecQuality != "max" {
		t.Fatalf("output replaced %v, recorder %q %q", e.out.Config() != cur, e.rec.Config().RecMode, e.rec.Config().RecQuality)
	}
	if r := recordOf(t, e); r["setting"] != "with_output" || r["quality"] != "max" {
		t.Fatalf("the state does not follow output_config: %v", r)
	}
	e.loadConfig = func() (*config.Config, error) {
		return testConfig(t, map[string]string{"REC_MODE": "with_output", "VBITRATE": "4000"}), nil
	}
	if ok, _ := e.reloadOutput(); !ok || e.out.Config() == cur || e.out.Config().VBitrate != 4000 {
		t.Fatal("a new bitrate did not reach the output")
	}
}
