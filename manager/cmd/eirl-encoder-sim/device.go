package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/auth"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
)

// simDevice: a fake but consistent encoder. The “service” goes through the same states as
// eirl-engine (activating → active, inactive, failed), the phase is computed as on the device, SRT
// only “connects” while streaming with a destination, and every change leaves a “journal” line so
// the client sends the snapshot right away.
//
// By default it mimics mixer F3 (ENCODER_CLOUD.md §13.3): Stream and Stop streaming switch the
// output hot without restarting, the phase is `ready` when not streaming and outputState goes
// connecting → on. With legacy, an older device: phase `rehearsal`, Stream/Rehearsal restart and
// stop stops the service.
type simDevice struct {
	log    *slog.Logger
	info   cloud.DeviceInfo
	client *cloud.Client
	flap   time.Duration
	links  int
	legacy bool
	start  time.Time
	// startDelay: how long it takes to go from activating to active (2 s; tests shorten it)
	startDelay time.Duration
	// connectDelay: how long the output takes to connect after Stream (F3: ~2 s on the board)
	connectDelay time.Duration

	mu        sync.Mutex
	cfg       config.Settings
	disabled  []string
	svc       string // active | activating | inactive | failed
	since     time.Time
	restarts  int
	signal    bool
	running   config.Settings // what the running “broadcast” started with
	outSince  time.Time       // when the output was switched on (F3: connecting until connectDelay)
	edid      string
	followers map[chan service.Entry]struct{}
}

func newSimDevice(log *slog.Logger, flap time.Duration, links int, legacy bool) *simDevice {
	cfg := config.Defaults()
	cfg.Output = "off" // starts without streaming: there is a picture but it goes nowhere
	now := time.Now()
	return &simDevice{log: log, flap: flap, links: links, legacy: legacy, start: now, cfg: cfg, running: cfg,
		startDelay: 2 * time.Second, connectDelay: 2 * time.Second,
		svc: "active", since: now, signal: true, edid: cfg.EDIDPreferred, followers: map[chan service.Entry]struct{}{}}
}

// run: the signal is lost for 10 s every -signal-flap (like a camera switching off).
func (d *simDevice) run(ctx context.Context) {
	if d.flap <= 0 {
		return
	}
	t := time.NewTicker(d.flap)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.mu.Lock()
		d.signal = false
		d.mu.Unlock()
		d.journal("signal_lost", "[engine] señal HDMI perdida")
		d.journal("waiting_signal", "[engine] esperando señal HDMI")
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
		d.mu.Lock()
		d.signal = true
		d.mu.Unlock()
		d.journal("signal", "[engine] señal: 1920x1080@30 (Frame format: progressive)")
	}
}

func (d *simDevice) Info() cloud.DeviceInfo { return d.info }

func (d *simDevice) LanURLs() []string { return []string{"http://eirl-sim.local"} }

func (d *simDevice) Status(context.Context) cloud.EncoderStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	run := d.cfg
	if d.svc == "active" || d.svc == "activating" {
		run = d.running
	}
	since := d.since.UTC()
	st := cloud.EncoderStatus{
		Phase:          simPhase(d.svc, d.signal, run.Output, d.legacy),
		Service:        cloud.StatusService{State: d.svc, Since: &since, Restarts: d.restarts},
		Output:         run.Output,
		PendingRestart: d.svc == "active" && !d.sameRun(d.cfg, d.running),
		Input:          cloud.StatusInput{EDIDPreferred: d.cfg.EDIDPreferred},
		Transport:      config.Transport(run.URL),
		Audio:          cloud.StatusAudio{State: "no_samples"},
		System: cloud.StatusSystem{
			CPUPct: []int{28 + rand.IntN(6), 18 + rand.IntN(4), 6 + rand.IntN(3), 4 + rand.IntN(3)},
			TempC:  cloud.StatusTemp{CPU: 44 + float64(rand.IntN(20))/10, GPU: 41 + float64(rand.IntN(20))/10},
			Power:  &cloud.StatusPower{InMw: 4300 + int64(rand.IntN(400)), InMv: 4950},
		},
		At: now.UTC(),
	}
	if d.svc == "active" {
		e := d.edid
		st.Input.EDIDActive = &e
	}
	if d.signal {
		st.Signal = cloud.StatusSignal{Present: true, Width: 1920, Height: 1080, FPS: 30, Supported: true}
		st.Audio.State = "playing"
	}
	names := []string{"eth0", "usb0", "usb1", "wlan0"}
	kinds := []string{"ethernet", "usb", "usb", "wifi"}
	off := map[string]bool{}
	for _, n := range d.disabled {
		off[n] = true
	}
	enabled := 0
	for i := 0; i < d.links; i++ {
		if !off[names[i]] {
			enabled++
		}
	}
	streaming := st.Phase == cloud.PhaseStreaming && run.URL != ""
	if !d.legacy && st.Phase == cloud.PhaseStreaming {
		// F3: streaming requested is not the same as streaming; the output connects in a few seconds.
		// Like the engine (ENCODER_CLOUD.md §13.3): a transient reason, retried on its own, waits in
		// connecting; error only for what needs a user action
		switch {
		case run.URL == "":
			st.OutputState = cloud.StatusOutput{State: "error", Reason: "no_destination"}
		case st.Transport == "srtla" && enabled == 0:
			st.OutputState = cloud.StatusOutput{State: "connecting", Reason: "no_uplinks", Detail: "waiting for an internet link"}
		case now.Sub(d.outSince) < d.connectDelay:
			st.OutputState = cloud.StatusOutput{State: "connecting"}
		default:
			st.OutputState = cloud.StatusOutput{State: "on"}
		}
		streaming = st.OutputState.State == "on"
	}
	if streaming {
		kbps := run.VideoKbps
		if run.ABR { // adaptive bitrate “breathes” between the minimum and the maximum
			span := float64(run.VideoKbps - run.VideoMinKbps)
			kbps = run.VideoMinKbps + int(span*(0.75+0.25*math.Sin(float64(now.Unix())/20)))
			st.Bitrate = &cloud.StatusBitrate{Kbps: kbps, MinKbps: run.VideoMinKbps, MaxKbps: run.VideoKbps}
		}
		st.SRT = &cloud.StatusSRT{Connected: true, RTTMs: 28 + float64(rand.IntN(120))/10,
			SendMbps: float64(kbps+run.AudioKbps) / 1000 * (0.97 + 0.06*rand.Float64()),
			LossPct:  float64(rand.IntN(5)) / 10, PktRetrans: rand.IntN(4)}
	}
	for i := 0; i < d.links; i++ {
		u := cloud.StatusUplink{Name: names[i], Kind: kinds[i], Disabled: off[names[i]]}
		if streaming && st.Transport == "srtla" && !u.Disabled {
			u.InUse = true
			u.TxKbps = int(st.SRT.SendMbps * 1000 / float64(enabled))
		}
		st.Uplinks = append(st.Uplinks, u)
	}
	return st
}

func (d *simDevice) Reported() (cloud.ReportedConfig, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return cloud.Reported(d.cfg, d.disabled), nil
}

func (d *simDevice) ApplyConfig(ctx context.Context, w cloud.WireConfig, apply bool) (cloud.ApplyResult, error) {
	d.mu.Lock()
	if ctx.Err() != nil {
		d.mu.Unlock()
		return cloud.ApplyResult{}, ctx.Err()
	}
	next, disabled, fe := cloud.ApplyWire(d.cfg, w)
	if fe != nil {
		d.mu.Unlock()
		return cloud.ApplyResult{}, fe
	}
	changed := !d.sameRun(next, d.running)
	d.cfg, d.disabled = next, disabled
	svc := d.svc
	// F3: the output is switched hot while the broadcast runs; everything else, as always
	hot := !d.legacy && apply && svc == "active" && !changed && next.Output != d.running.Output
	if hot {
		d.setOutputLocked(next.Output)
	}
	d.mu.Unlock()
	if hot {
		d.outputJournal(next.Output)
	}
	d.log.Info("sim: configuration", "url", next.URL, "streamid", config.MaskStreamID(next.StreamID), "latency", next.LatencyMs,
		"vbitrate", next.VideoKbps, "abr", next.ABR, "output", next.Output, "edid", next.EDIDPreferred, "apply", apply)
	// Same rule as the device (api.applySaved): a deliberate stop is respected; otherwise only if it changed
	if !apply || svc == "inactive" || !changed {
		return cloud.ApplyResult{Saved: true}, nil
	}
	d.restart("restart")
	return cloud.ApplyResult{Saved: true, Restarted: true}, nil
}

func (d *simDevice) CanReboot(context.Context) error { return nil }

func (d *simDevice) Action(ctx context.Context, name, password string) error {
	d.log.Info("sim: action", "action", name)
	switch name {
	case "start", "rehearsal", "stop":
		if d.legacy && name == "stop" {
			d.mu.Lock()
			d.svc, d.since = "inactive", time.Now()
			d.mu.Unlock()
			d.journal("stop", "Stopped Encoder HDMI (TC358743) -> H.264 + AAC -> SRT.")
			return nil
		}
		// F3: stop and rehearsal = stop streaming without stopping the service (the picture stays)
		out := map[string]string{"start": "on", "rehearsal": "off", "stop": "off"}[name]
		d.mu.Lock()
		next := d.cfg
		next.Output = out
		if err := next.Validate(); err != nil {
			d.mu.Unlock()
			return &cloud.ActionError{Code: "no_destination", Message: i18n.Msg(i18n.EN, "cloud.no_destination")}
		}
		d.cfg = next
		svc, same := d.svc, d.sameRun(next, d.running)
		hot := !d.legacy && svc == "active" && same && next.Output != d.running.Output
		if hot {
			d.setOutputLocked(out)
		}
		d.mu.Unlock()
		if hot {
			d.outputJournal(out)
			return nil
		}
		if svc == "active" && same && (d.legacy || next.Output == d.running.Output) {
			return nil
		}
		if !d.legacy && out == "off" && svc != "active" {
			return nil // stop streaming with the service stopped: only saved
		}
		d.restart("start")
	case "restart":
		d.restart("restart")
	case "reboot":
		// Like the board: the channel drops and reconnects on “boot”
		d.client.Disconnect(15 * time.Second)
		go func() {
			time.Sleep(3 * time.Second)
			d.restart("start")
		}()
	case "set_local_password":
		if len(password) < auth.MinPasswordLen || len(password) > auth.MaxPasswordLen {
			return &cloud.ActionError{Code: "validation", Message: fmt.Sprintf("between %d and %d characters", auth.MinPasswordLen, auth.MaxPasswordLen)}
		}
		d.log.Info("sim: local password changed (simulated)")
	default:
		return &cloud.ActionError{Code: "unknown_action", Message: i18n.Msg(i18n.EN, "common.unknown_action")}
	}
	return nil
}

// setOutputLocked: the hot output switch (F3); d.mu must be held.
func (d *simDevice) setOutputLocked(out string) {
	d.running.Output = out
	if out == "on" {
		d.outSince = time.Now()
	}
}

// outputJournal: leaves the line that triggers the snapshot and, when switching on, another one
// when it “connects”.
func (d *simDevice) outputJournal(out string) {
	if out == "off" {
		d.journal("output", "[mixer] salida apagada: sin emitir, con preview")
		return
	}
	d.journal("output", "[mixer] salida encendida: conectando")
	go func() {
		time.Sleep(d.connectDelay)
		d.journal("output", "[mixer] salida conectada")
	}()
}

// simPhase: the phase as the device's local API (api.phase) computes it for these states.
func simPhase(state string, signal bool, output string, legacy bool) string {
	switch {
	case state == "failed":
		return cloud.PhaseFailed
	case state == "activating":
		return cloud.PhaseRestarting
	case state != "active":
		return cloud.PhaseStopped
	case !signal:
		return cloud.PhaseWaitingSignal
	case output == "off" && legacy:
		return cloud.PhaseRehearsal
	case output == "off":
		return cloud.PhaseReady
	}
	return cloud.PhaseStreaming
}

// sameRun: same configuration for the broadcast (who manages it does not count). With F3 the
// output does not count either: it is switched hot and needs no restart.
func (d *simDevice) sameRun(a, b config.Settings) bool {
	a.ManagedBy, b.ManagedBy = "", ""
	if !d.legacy {
		a.Output, b.Output = "", ""
	}
	return a == b
}

// restart: activating for 2 s, then active with the saved config (or failed if it is invalid:
// streaming without a destination).
func (d *simDevice) restart(kind string) {
	d.mu.Lock()
	d.svc, d.since = "activating", time.Now()
	if kind == "restart" {
		d.restarts++
	}
	d.mu.Unlock()
	d.journal("start", "Started Encoder HDMI (TC358743) -> H.264 + AAC -> SRT.")
	go func() {
		time.Sleep(d.startDelay)
		d.mu.Lock()
		if d.svc != "activating" {
			d.mu.Unlock()
			return
		}
		ok := d.cfg.Validate() == nil
		if ok {
			d.svc, d.running, d.edid = "active", d.cfg, d.cfg.EDIDPreferred
			d.outSince = time.Now()
		} else {
			d.svc = "failed"
		}
		d.since = time.Now()
		out := d.running.Output
		d.mu.Unlock()
		switch {
		case !ok:
			d.journal("config_invalid", "[engine] configuración no válida: SRT_URL")
		case out == "off":
			d.journal("rehearsal", "[engine] sin emitir (OUTPUT=off): con preview")
		default:
			d.journal("srt_target", "[engine] SRT → (simulado)")
		}
	}()
}

// Thumbnail: a 320×180 test card with the phase color and a moving bar.
func (d *simDevice) Thumbnail() (cloud.Thumbnail, bool) {
	st := d.Status(context.Background())
	if !st.Signal.Present || st.Service.State != "active" {
		return cloud.Thumbnail{}, false
	}
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	bars := []color.RGBA{{192, 192, 192, 255}, {192, 192, 0, 255}, {0, 192, 192, 255}, {0, 192, 0, 255}, {192, 0, 192, 255}, {192, 0, 0, 255}, {0, 0, 192, 255}}
	phaseColor := map[string]color.RGBA{cloud.PhaseStreaming: {240, 68, 56, 255}, cloud.PhaseReady: {70, 95, 255, 255},
		cloud.PhaseRehearsal: {70, 95, 255, 255}}[st.Phase]
	x := int(time.Now().Unix()*8) % 320
	for py := 0; py < 180; py++ {
		for px := 0; px < 320; px++ {
			c := bars[px*len(bars)/320]
			switch {
			case py >= 150:
				c = phaseColor
			case px >= x && px < x+6:
				c = color.RGBA{255, 255, 255, 255}
			}
			img.Set(px, py, c)
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 70}); err != nil {
		return cloud.Thumbnail{}, false
	}
	return cloud.Thumbnail{JPEG: b.Bytes(), Width: 320, Height: 180, At: time.Now().UTC()}, true
}

func (d *simDevice) Follow(ctx context.Context, fn func(service.Entry)) error {
	ch := make(chan service.Entry, 16)
	d.mu.Lock()
	d.followers[ch] = struct{}{}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.followers, ch)
		d.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-ch:
			fn(e)
		}
	}
}

func (d *simDevice) journal(kind, msg string) {
	level := "info"
	if kind == "signal_lost" || kind == "config_invalid" {
		level = "warn"
	}
	e := service.Entry{TS: time.Now().UTC(), Level: level, Kind: kind, Msg: msg}
	d.mu.Lock()
	defer d.mu.Unlock()
	for ch := range d.followers {
		select {
		case ch <- e:
		default:
		}
	}
}

func (d *simDevice) Uplinks() []string {
	return []string{"eth0", "usb0", "usb1", "wlan0"}[:d.links]
}

func (d *simDevice) Unlinked(ctx context.Context, clearDestination bool) error {
	d.log.Info("sim: unlinked", "clearDestination", clearDestination)
	if !clearDestination {
		return nil
	}
	d.mu.Lock()
	d.cfg.URL, d.cfg.Mode, d.cfg.StreamID, d.cfg.Passphrase, d.cfg.Output = "", "caller", "", "", "off"
	svc := d.svc
	d.mu.Unlock()
	if svc == "active" || svc == "activating" {
		d.restart("restart")
	}
	return nil
}
