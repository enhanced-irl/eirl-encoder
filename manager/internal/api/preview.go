package api

import (
	"context"
	"errors"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// PreviewWatch: who is watching the WebRTC preview (live.Relay).
type PreviewWatch interface {
	Viewers() int
	Changed() <-chan struct{}
}

const (
	// After the last viewer leaves, the preview stays on for a while: hiding and showing the tab
	// again, or reconnecting after a Wi-Fi drop, does not switch it off and on again
	previewLinger = 15 * time.Second
	// A freshly started engine has the preview on: the wanted state is repeated to it
	previewReassert = 30 * time.Second
	previewCheck    = 5 * time.Second
)

// FollowPreview stops the engine's preview (`preview` command) while nobody watches it: with no
// viewers, the engine does not scale, encode or convert to Opus (~6-8 points of a core, MIXER.md
// §17). With someone watching, it turns on as soon as they arrive (the first IDR takes ≤ 1 s). An
// engine without the command keeps it always on, as before.
func (s *Server) FollowPreview(ctx context.Context, w PreviewWatch) {
	if s.Mixer == nil || w == nil {
		return
	}
	var seen, sentAt time.Time // the last time someone was watching; the last send
	sent := -1                 // the last state the engine accepted: -1 none, 0 stopped, 1 on
	unsupported := false
	tick := time.NewTicker(previewCheck)
	defer tick.Stop()
	for {
		now := time.Now()
		if w.Viewers() > 0 {
			seen = now
		}
		on := previewOn(seen, now)
		want := 0
		if on {
			want = 1
		}
		if want != sent || now.Sub(sentAt) >= previewReassert {
			sentAt = now
			err := s.Mixer.Do(ctx, mixer.Command{Cmd: "preview", On: &on})
			var ce *mixer.CommandError
			switch {
			case err == nil:
				if want != sent {
					s.Log.Info(map[bool]string{true: "live preview on: someone is watching",
						false: "live preview stopped: nobody is watching"}[on])
				}
				sent, unsupported = want, false
			case errors.As(err, &ce) && (ce.Code == "unsupported" || ce.Code == "unknown_command"):
				// No preview in the engine (LIVE=0) or an engine older than the command: nothing to stop
				if !unsupported {
					s.Log.Info("the engine cannot stop the preview: it stays as it is")
				}
				unsupported, sent = true, want
			}
			// Any other error (the engine is restarting): retried on the next round
		}
		select {
		case <-ctx.Done():
			return
		case <-w.Changed():
		case <-tick.C:
		}
	}
}

// previewOn: someone is watching now or did less than previewLinger ago.
func previewOn(seen, now time.Time) bool {
	return !seen.IsZero() && now.Sub(seen) < previewLinger
}
