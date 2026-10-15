package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

func startTestEngine(t *testing.T) *simEngine {
	t.Helper()
	dir, err := os.MkdirTemp("", "sim") // short path (Unix socket limit on macOS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e, err := startSimEngine(ctx, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// controlsOf: the controls of a source as the web gets them (through the app's bounds)
func controlsOf(t *testing.T, e *simEngine, id string) map[string]mixer.Control {
	t.Helper()
	st, err := e.client().Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range st.Sources {
		if s.ID != id {
			continue
		}
		out := map[string]mixer.Control{}
		for _, c := range s.Controls {
			out[c.ID] = c
		}
		return out
	}
	return nil
}

// The simulated cameras follow the engine's contract (SOURCES.md §7.3): only the USB camera with a slot carries
// controls, values brought into range and step, automatics that make their manual control inactive, a reset to the
// defaults, and none on a hidden source
func TestSimCameraControls(t *testing.T) {
	e := startTestEngine(t)
	ctl := func(src, id string, v int) string {
		return e.command(mixer.Command{Cmd: "camera_control", Source: src, Control: id, Value: &v})
	}
	value := func(id string) mixer.Control { t.Helper(); return controlsOf(t, e, simLogi)[id] }

	st, _ := e.client().Read()
	for _, s := range st.Sources {
		if (s.ID == simLogi) != (s.Controls != nil) {
			t.Errorf("%s: %d controls", s.ID, len(s.Controls))
		}
	}
	// All of them pass the app's bounds, in the order of the table
	if got := controlsOf(t, e, simLogi); len(got) != 16 {
		t.Fatalf("Logi: %d controls", len(got))
	}
	if c := value("power_line"); c.Type != "menu" || c.Value != 1 || c.Default != 2 || len(c.Options) != 3 {
		t.Errorf("power_line: %+v", c)
	}
	for _, id := range []string{"exposure", "wb_temperature", "focus"} {
		if !value(id).Inactive {
			t.Errorf("%s: active while its automatic is on", id)
		}
	}

	for _, c := range []struct {
		id    string
		v     int
		code  string
		after int // the value the control has afterwards
	}{
		{"brightness", 300, "", 255},
		{"brightness", -4, "", 0},
		{"wb_temperature", 3000, "inactive", 4000},
		{"wb_auto", 0, "", 0},
		{"wb_temperature", 3333, "", 3330},
		{"wb_temperature", 9999, "", 7500},
		{"pan", 1000, "", 0},
		{"pan", 5000, "", 3600},
		{"pan", 40000, "", 36000},
		{"focus_auto", 0, "", 0},
		{"focus", 253, "", 255},
		{"focus", 251, "", 250},
		{"exposure_auto", 2, "bad_request", 1},
		{"power_line", 3, "bad_request", 1},
		{"power_line", 0, "", 0},
		{"hue", 0, "unknown_control", 0},
	} {
		if code := ctl(simLogi, c.id, c.v); code != c.code {
			t.Errorf("%s=%d: %q, want %q", c.id, c.v, code, c.code)
		}
		if c.code == "unknown_control" {
			continue
		}
		if got := value(c.id).Value; got != c.after {
			t.Errorf("%s=%d: value %d, want %d", c.id, c.v, got, c.after)
		}
	}
	if value("wb_temperature").Inactive || value("focus").Inactive || !value("exposure").Inactive {
		t.Errorf("inactive flags: %+v %+v %+v", value("wb_temperature"), value("focus"), value("exposure"))
	}
	// Back to the camera's defaults, automatics included
	if code := e.command(mixer.Command{Cmd: "camera_reset", Source: simLogi}); code != "" {
		t.Fatal(code)
	}
	for _, c := range controlsOf(t, e, simLogi) {
		if c.Value != c.Default {
			t.Errorf("after the reset %s = %d, default %d", c.ID, c.Value, c.Default)
		}
	}
	if !value("wb_temperature").Inactive {
		t.Error("wb_temperature active after the reset")
	}
	// Sources without settings, without a slot, hidden
	if code := ctl("hdmi", "brightness", 1); code != "unsupported" {
		t.Errorf("hdmi: %q", code)
	}
	if code := e.command(mixer.Command{Cmd: "camera_reset", Source: simMS2109}); code != "unknown_source" {
		t.Errorf("without a slot: %q", code)
	}
	yes, no := true, false
	ctl(simLogi, "zoom", 300)
	if code := e.command(mixer.Command{Cmd: "hide", Source: simLogi, Hidden: &yes}); code != "" {
		t.Fatal(code)
	}
	if got := controlsOf(t, e, simLogi); len(got) != 0 {
		t.Errorf("hidden with controls: %v", got)
	}
	if code := ctl(simLogi, "zoom", 200); code != "unsupported" {
		t.Errorf("hidden: %q", code)
	}
	if code := e.command(mixer.Command{Cmd: "take", Source: simLogi}); code != "hidden_source" {
		t.Errorf("take hidden: %q", code)
	}
	e.command(mixer.Command{Cmd: "hide", Source: simLogi, Hidden: &no})
	if got := value("zoom").Value; got != 300 {
		t.Errorf("shown again: zoom %d", got)
	}
	// The source on air is not hidden
	if code := e.command(mixer.Command{Cmd: "hide", Source: "hdmi", Hidden: &yes}); code != "on_air" {
		t.Errorf("hide on air: %q", code)
	}
	// The capture card in a slot: two sources with settings
	e.command(mixer.Command{Cmd: "slots", Slots: []mixer.SlotAssign{{Slot: 4, Assign: simMS2109}}})
	if c := controlsOf(t, e, simMS2109); len(c) != 4 || c["brightness"].Value != -11 || c["hue"].Min != -128 {
		t.Errorf("MS2109: %+v", c)
	}
}
