package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// runEngine runs the actor (Run) until the test ends: the screen's commands are posted to it.
func runEngine(t *testing.T, e *Engine) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// publishedRecord is the record object of mixer.json as the actor last wrote it (read without publishing: the actor
// is running).
func publishedRecord(t *testing.T, e *Engine, what string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; {
		var st map[string]any
		if b, err := os.ReadFile(filepath.Join(e.runDir, "mixer.json")); err == nil && json.Unmarshal(b, &st) == nil {
			if r, _ := st["record"].(map[string]any); r != nil && ok(r) {
				return r
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// REC on the screen: the engine's record command through the screen's host, which says by=screen (like Go live), and
// its refusals as codes (the screen words them).
func TestScreenRecord(t *testing.T) {
	e, _, _ := startRecordingEngine(t, "manual", true)
	runEngine(t, e)
	h := screenHost{e}
	if ok, code := h.Command("record", map[string]any{"cmd": "record", "on": "yes", "by": "screen"}); ok || code != "bad_request" {
		t.Fatalf("bad request: %v %q", ok, code)
	}
	if ok, code := h.Command("record", map[string]any{"cmd": "record", "on": true, "by": "screen"}); !ok || code != "" {
		t.Fatalf("REC: %v %q", ok, code)
	}
	r := publishedRecord(t, e, "recording", func(r map[string]any) bool { return r["state"] == "on" })
	if r["mode"] != "on" || r["by"] != "screen" || r["seq"] != 1.0 {
		t.Fatalf("recording: %v", r)
	}
	if ok, code := h.Command("record", map[string]any{"cmd": "record", "on": false, "by": "screen"}); !ok || code != "" {
		t.Fatalf("stop: %v %q", ok, code)
	}
	r = publishedRecord(t, e, "stopped", func(r map[string]any) bool { return r["state"] == "off" && r["mode"] == "off" })
	if r["by"] != "screen" || r["seq"] != 2.0 {
		t.Fatalf("stopped: %v", r)
	}
	e2, _, _ := startRecordingEngine(t, "off", true)
	runEngine(t, e2)
	if ok, code := (screenHost{e2}).Command("record", map[string]any{"cmd": "record", "on": true}); ok || code != "disabled" {
		t.Fatalf("REC_MODE=off: %v %q", ok, code)
	}
	e3, _, _ := startRecordingEngine(t, "with_output", false)
	runEngine(t, e3)
	if ok, code := (screenHost{e3}).Command("record", map[string]any{"cmd": "record", "on": true}); ok || code != "no_disk" {
		t.Fatalf("no disk: %v %q", ok, code)
	}
}
