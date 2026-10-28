package api

import (
	"context"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// applySaved: the restart rule after saving, for the local web (PUT /destination(s), /output, /input with apply)
// and for the cloud (config.set with apply, start/rehearsal, unlink), when applyAfterSave did not apply it hot:
//   - deliberately stopped (inactive): respected, not started;
//   - crashed (failed): restarted with the saved config (MANAGER.md §3: “if it is down, start it
//     with what is saved”), even if the configuration did not change;
//   - running: restarted only if the configuration changed or there were saved, unapplied changes.
//     The cloud may resend the same configuration on reconnect and must not cut the broadcast.
//
// With the mixer running (F3), OUTPUT does not restart: start and stop streaming are applied hot
// (syncOutput). Only the other changes restart.
//
// before: the .env fingerprints (Config.Hashes) before this save.
func (s *Server) applySaved(ctx context.Context, before config.Hashes) (restarted bool, err error) {
	st, err := s.Unit.Status(ctx)
	if err != nil {
		return false, err
	}
	after := s.Config.Hashes()
	hot := st.State == "active" && s.mixing()
	changed := before.Full != after.Full
	if hot {
		changed = before.NoOutput != after.NoOutput
	}
	switch {
	case st.State == "inactive":
		return false, nil
	case st.State == "failed":
		// restart it
	case !changed && !s.pending(st.State):
		if hot {
			return false, s.syncOutput(ctx)
		}
		return false, nil
	}
	if err := s.Unit.Control(ctx, "restart"); err != nil {
		return false, err
	}
	return true, nil
}

// applyAfterSave: the apply rule after a save (OUTPUTS.md §9.4), for the local API and the cloud. With the engine
// running the mixer and only output keys changed (the destination, its bitrates and encoding settings: config.DiffEnv),
// the engine takes them hot ({"cmd":"output_config"}: no restart; the output restarts only if it was on air). If it
// cannot (unknown_command from an older engine, restart_required because something else changed, any error), the
// restart of today (applySaved).
func (s *Server) applyAfterSave(ctx context.Context, before config.Snapshot) (restarted bool, err error) {
	if s.outputConfig(ctx, before) {
		return false, nil
	}
	return s.applySaved(ctx, before.Hashes)
}

// outputConfig: true if the engine took the new output configuration hot.
func (s *Server) outputConfig(ctx context.Context, before config.Snapshot) bool {
	if s.Mixer == nil {
		return false
	}
	after := s.Config.Env()
	if _, onlyOutput := config.DiffEnv(before.Env, after); !onlyOutput {
		return false
	}
	if st, err := s.Unit.Status(ctx); err != nil || st.State != "active" || !s.mixing() {
		return false
	}
	if err := s.Mixer.Do(ctx, mixer.Command{Cmd: "output_config"}); err != nil {
		s.Log.Info("output configuration not taken hot: restarting", "err", err)
		return false
	}
	s.Log.Info("output configuration applied hot")
	// OUTPUT (on/off) has its own command: output_config does not start or stop the stream, and only a save
	// that changed OUTPUT switches it. A destination switch must never stop what is on air, even when a go-live
	// from the screen has not reached the saved file yet
	if before.Env["OUTPUT"] != after["OUTPUT"] {
		if err := s.syncOutput(ctx); err != nil {
			s.Log.Warn("output switch after output_config", "err", err)
		}
	}
	return true
}

// mixing: the running broadcast is the mixer's (MIXER=1 in /run/eirl-engine/state).
func (s *Server) mixing() bool {
	rs, ok := s.runState()
	return ok && rs.Mixer == "1"
}

// syncOutput: with the mixer, the output follows the saved OUTPUT, hot (without restarting).
func (s *Server) syncOutput(ctx context.Context) error {
	if s.Mixer == nil {
		return nil
	}
	cfg, err := s.Config.Load()
	if err != nil {
		return err
	}
	on := cfg.Output == "on"
	if m, err := s.Mixer.Read(); err == nil && m.Output != nil && (m.Output.Mode == "on") == on {
		return nil
	}
	err = s.Mixer.Do(ctx, mixer.Command{Cmd: "output", On: &on})
	if err == nil {
		s.Log.Info("output switched hot", "output", cfg.Output)
	}
	return err
}

// logConfig: the log line of every save (local web and cloud). Only what is not secret.
func (s *Server) logConfig(msg string, cfg config.Settings) {
	// Of the other protocols' URL only its host: a key pasted into an RTMP path by mistake stays out of the journal
	s.Log.Info(msg, "protocol", cfg.EffectiveProtocol(), "url", cfg.URL, "server", config.URLHost(cfg.OutputURL),
		"codec", cfg.Codec(), "size", cfg.OutputSize, "fps", cfg.OutputFPS, "key", cfg.Key != "",
		"mode", cfg.Mode, "latency", cfg.LatencyMs,
		"vbitrate", cfg.VideoKbps, "abitrate", cfg.AudioKbps, "abr", cfg.ABR, "vbitrateMin", cfg.VideoMinKbps,
		"output", cfg.Output, "edid", cfg.EDIDPreferred, "managedBy", cfg.ManagedBy,
		"streamid", cfg.StreamID != "", "passphrase", cfg.Passphrase != "" || cfg.OutputPassphrase != "",
		"ristUser", cfg.OutputUser != "", "recording", cfg.RecMode, "recQuality", cfg.RecQuality)
}
