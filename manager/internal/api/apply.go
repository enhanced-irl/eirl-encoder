package api

import (
	"context"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// applySaved: the SINGLE “apply” rule after saving, for the local web (PUT /destination, /output,
// /input with apply) and for the cloud (config.set with apply, start/rehearsal, unlink):
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
	s.Log.Info(msg, "url", cfg.URL, "mode", cfg.Mode, "latency", cfg.LatencyMs,
		"vbitrate", cfg.VideoKbps, "abitrate", cfg.AudioKbps, "abr", cfg.ABR, "vbitrateMin", cfg.VideoMinKbps,
		"output", cfg.Output, "edid", cfg.EDIDPreferred, "managedBy", cfg.ManagedBy,
		"streamid", cfg.StreamID != "", "passphrase", cfg.Passphrase != "")
}
