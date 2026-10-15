//go:build linux && cgo

// eirl-engine is the encoder's engine (ENGINE_GO.md): the program, its sources and the command socket.
// It runs as eirl-engine.service with the same environment and files as the Python engine it replaces.
//
// Exit codes, like the launcher: 0 on SIGTERM/SIGINT, 64 for an invalid configuration (the unit does not
// restart: RestartPreventExitStatus=64), 1 for anything else (systemd restarts it).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/displayhold"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/hdmi"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

var version = "dev" // -ldflags "-X main.version=…"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	checkConfig := flag.Bool("check-config", false, "validate the configuration and exit (64 if invalid)")
	flag.Parse()
	if *showVersion {
		fmt.Println("eirl-engine", version)
		return
	}
	os.Exit(run(*checkConfig))
}

func run(checkOnly bool) int {
	cfg, err := config.Load(config.Options{})
	if err != nil {
		var inv *config.InvalidError
		if errors.As(err, &inv) {
			elog.Event("engine", "config_invalid", "key", inv.Key())
			return 64
		}
		elog.Event("engine", "config_invalid", "err", err)
		return 64
	}
	if checkOnly {
		return 0
	}
	elog.Event("engine", "starting", "version", version, "gstreamer", gst.Version())
	// The HDMI output stays lit when the screen closes (MIXER.md §3, trap 27); false = libc's ioctl won
	elog.Event("engine", "display_hold", "active", displayhold.Read().Active)
	if cfg.SingleSource {
		elog.Event("engine", "single_source_unsupported")
	}
	cfg.PrepareRunDir()
	cfg.PrepareThumb()
	_ = cfg.WriteState(cfg.StartingState())
	if cfg.RunDir != "" {
		for _, f := range []string{"abr", "uplinks", "video"} {
			os.Remove(filepath.Join(cfg.RunDir, f))
		}
	}
	media := gst.New()
	prep := engine.Prepare(cfg, sysx.Run, hdmi.Sys{Run: sysx.Run, Sleep: time.Sleep})
	e := engine.New(engine.Options{Config: cfg, Media: media, Platform: pipeline.Tegra, Run: sysx.Run,
		MixerMain: prep.MixerMain, FPS: prep.FPS, TC358743: prep.TC358743, InputALSA: prep.ALSA})
	if err := e.Start(cfg.MixerScreen); err != nil {
		elog.Event("engine", "start_failed", "err", err)
		e.Stop()
		return 1
	}
	ready := cfg.ReadyState(cfg.Program(prep.FPS), prep.Device, prep.DeviceName, cfg.AudioMain(prep.ALSA))
	_ = cfg.WriteState(ready)
	e.SetReadyState(ready)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	err = e.Run(ctx)
	e.Stop()
	if st := displayhold.Read(); st.Held+st.Released+st.Cancelled > 0 {
		elog.Event("engine", "display_hold", "held", st.Held, "released", st.Released, "cancelled", st.Cancelled)
	}
	if cfg.RunDir != "" {
		os.Remove(filepath.Join(cfg.RunDir, "video"))
	}
	if err != nil {
		elog.Event("engine", "engine_fatal", "err", err)
		return 1
	}
	return 0
}
