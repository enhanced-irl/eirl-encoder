package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
)

func wire(t *testing.T, output string, url string) cloud.WireConfig {
	t.Helper()
	var dest any
	if url != "" {
		dest = map[string]any{"url": url, "streamId": "publish:AbCdEfGh0123456789_-AbCdEfGh0123", "latencyMs": 2000}
	}
	raw, _ := json.Marshal(map[string]any{
		"destination": dest,
		"video":       map[string]any{"bitrateKbps": 6000, "adaptive": true, "minBitrateKbps": 1000},
		"audio":       map[string]any{"bitrateKbps": 128},
		"input":       map[string]any{"edidPreferred": "1080p30"},
		"output":      output,
		"uplinks":     map[string]any{"disabled": []string{}},
	})
	w, fe := cloud.ParseWireConfig(raw)
	if fe != nil {
		t.Fatal(fe)
	}
	return w
}

func waitPhase(t *testing.T, d *simDevice, phase string) cloud.EncoderStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := d.Status(context.Background())
		if st.Phase == phase {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("phase %q, want %q", st.Phase, phase)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Device older than F3 (-legacy): rehearsal, and Stream/Rehearsal/stop restart or stop the service.
func TestSimDeviceLegacyIsCoherent(t *testing.T) {
	d := newSimDevice(slog.New(slog.NewTextHandler(io.Discard, nil)), 0, 2, true)
	d.startDelay = 20 * time.Millisecond
	ctx := context.Background()
	lines := make(chan service.Entry, 32)
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go d.Follow(fctx, func(e service.Entry) { lines <- e })
	time.Sleep(10 * time.Millisecond)

	if st := d.Status(ctx); st.Phase != cloud.PhaseRehearsal || st.SRT != nil || !st.Signal.Present {
		t.Fatalf("starts in rehearsal with signal and no SRT: %+v", st)
	}
	if _, ok := d.Thumbnail(); !ok {
		t.Error("in rehearsal there is a thumbnail")
	}
	var ae *cloud.ActionError
	if err := d.Action(ctx, "start", ""); err == nil || !asAction(err, &ae) || ae.Code != "no_destination" {
		t.Errorf("start without destination: %v", err)
	}
	// D9 in the simulator too (it uses the same ApplyWire)
	if _, err := d.ApplyConfig(ctx, wire(t, "on", "srtla://evil.example.com:5002"), true); err == nil {
		t.Error("D9: foreign destination accepted")
	}
	res, err := d.ApplyConfig(ctx, wire(t, "on", "srtla://eu1.relay.enhancedirl.com:5002"), true)
	if err != nil || !res.Saved || !res.Restarted {
		t.Fatalf("valid config: %+v %v", res, err)
	}
	st := waitPhase(t, d, cloud.PhaseStreaming)
	if st.SRT == nil || !st.SRT.Connected || st.Transport != "srtla" || st.Bitrate == nil || len(st.Uplinks) != 2 || !st.Uplinks[0].InUse {
		t.Errorf("streaming over SRTLA with ABR: %+v", st)
	}
	if err := d.Action(ctx, "rehearsal", ""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, d, cloud.PhaseRehearsal)
	d.Action(ctx, "stop", "")
	if st := d.Status(ctx); st.Phase != cloud.PhaseStopped {
		t.Errorf("stop: %s", st.Phase)
	}
	if _, ok := d.Thumbnail(); ok {
		t.Error("stopped: no thumbnail")
	}
	if err := d.Action(ctx, "set_local_password", "corta"); !asAction(err, &ae) || ae.Code != "validation" {
		t.Errorf("short password: %v", err)
	}
	d.Action(ctx, "start", "")
	waitPhase(t, d, cloud.PhaseStreaming)
	d.Unlinked(ctx, true)
	waitPhase(t, d, cloud.PhaseRehearsal)
	if rep, _ := d.Reported(); rep.Destination != nil {
		t.Errorf("unlinked: destination cleared: %+v", rep.Destination)
	}
	kinds := map[string]bool{}
	for len(lines) > 0 {
		kinds[(<-lines).Kind] = true
	}
	if !kinds["start"] || !kinds["stop"] {
		t.Errorf("simulated journal lines: %v", kinds)
	}
}

// Mixer F3 (default): the output switches hot, the phase is ready when not streaming and
// outputState goes connecting → on; stop keeps the picture.
func TestSimDeviceF3HotOutput(t *testing.T) {
	d := newSimDevice(slog.New(slog.NewTextHandler(io.Discard, nil)), 0, 2, false)
	d.startDelay, d.connectDelay = 20*time.Millisecond, 80*time.Millisecond
	ctx := context.Background()

	st := d.Status(ctx)
	st.Normalize()
	if st.Phase != cloud.PhaseReady || st.OutputState.State != "off" || st.SRT != nil {
		t.Fatalf("starts ready, not streaming: %+v", st)
	}
	if _, ok := d.Thumbnail(); !ok {
		t.Error("not streaming: there is a thumbnail")
	}
	// Destination saved without streaming: restarts (the destination changes), stays ready
	if _, err := d.ApplyConfig(ctx, wire(t, "off", "srtla://eu1.relay.enhancedirl.com:5002"), true); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, d, cloud.PhaseReady)
	restarts := d.Status(ctx).Service.Restarts

	if err := d.Action(ctx, "start", ""); err != nil {
		t.Fatal(err)
	}
	st = d.Status(ctx)
	if st.Phase != cloud.PhaseStreaming || st.OutputState.State != "connecting" || st.SRT != nil || st.Service.State != "active" {
		t.Fatalf("hot Stream: requested and connecting, no restart: %+v", st)
	}
	time.Sleep(100 * time.Millisecond)
	st = d.Status(ctx)
	if st.OutputState.State != "on" || st.SRT == nil || !st.Uplinks[0].InUse || st.Service.Restarts != restarts || st.PendingRestart {
		t.Fatalf("connected, no restarts or pending changes: %+v", st)
	}
	// No links with SRTLA: transient (retried on its own), so connecting with no_uplinks and no SRT
	d.mu.Lock()
	d.disabled = []string{"eth0", "usb0"}
	d.mu.Unlock()
	if st := d.Status(ctx); st.OutputState.State != "connecting" || st.OutputState.Reason != "no_uplinks" || st.SRT != nil {
		t.Errorf("no links: %+v", st.OutputState)
	}
	d.mu.Lock()
	d.disabled = nil
	d.mu.Unlock()

	// Stop streaming: the picture stays and the service does not stop (stop and rehearsal alike)
	for _, name := range []string{"stop", "rehearsal"} {
		d.Action(ctx, "start", "")
		if err := d.Action(ctx, name, ""); err != nil {
			t.Fatal(err)
		}
		st = d.Status(ctx)
		st.Normalize()
		if st.Phase != cloud.PhaseReady || st.OutputState.State != "off" || st.Service.State != "active" || st.Service.Restarts != restarts {
			t.Errorf("%s: stops streaming without stopping: %+v", name, st)
		}
		if _, ok := d.Thumbnail(); !ok {
			t.Errorf("%s: the picture stays", name)
		}
	}
	// Stream from the configuration (output on applied): hot as well
	res, err := d.ApplyConfig(ctx, wire(t, "on", "srtla://eu1.relay.enhancedirl.com:5002"), true)
	if err != nil || res.Restarted {
		t.Fatalf("output on applied hot: %+v %v", res, err)
	}
	if st := d.Status(ctx); st.Phase != cloud.PhaseStreaming || st.Service.Restarts != restarts {
		t.Errorf("streaming without restarting: %+v", st)
	}
}

func asAction(err error, ae **cloud.ActionError) bool {
	a, ok := err.(*cloud.ActionError)
	if ok {
		*ae = a
	}
	return ok
}
