package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// Mixer (MIXER.md §5): the web sends the same commands as the board's touch screen and sees the same
// state. Commands go to the engine through its socket; the state comes from its copy in
// /run/eirl-engine/mixer.json and, live, through the SSE ("mixer" event, see hub.followMixer).

// mixerErrors: engine error code → catalog key of its message.
var mixerErrors = map[string]string{
	"unknown_source": "mixer.unknown_source",
	"hidden_source":  "mixer.hidden_source",
	"on_air":         "mixer.on_air",
	"no_secondary":   "mixer.no_secondary",
	"bad_layout":     "mixer.bad_layout",
	"bad_transition": "mixer.bad_transition",
	"bad_request":    "mixer.bad_command",
	"bad_json":       "mixer.bad_command",
	"timeout":        "mixer.timeout",
	"unsupported":    "mixer.unsupported",
	// an engine older than the command (e.g. pip, pause or lock)
	"unknown_command": "mixer.unsupported",
	// the cameras' image settings (SOURCES.md §7.3)
	"unknown_control": "mixer.unknown_control",
	"inactive":        "mixer.control_inactive",
	"busy":            "mixer.busy",
	"camera_error":    "mixer.camera_error",
}

// mixerCmdErrors: a code that means something else in one command. unsupported, for the camera commands, is the
// source that has no image settings now (an older engine answers unknown_command)
var mixerCmdErrors = map[string]map[string]string{
	"camera_control": {"unsupported": "mixer.camera_unsupported"},
	"camera_reset":   {"unsupported": "mixer.camera_unsupported"},
}

// mixerErrorKey: the catalog key of an engine error code in a command; mixer.rejected if unknown
func mixerErrorKey(cmd, code string) string {
	if key, ok := mixerCmdErrors[cmd][code]; ok {
		return key
	}
	if key, ok := mixerErrors[code]; ok {
		return key
	}
	return "mixer.rejected"
}

func (s *Server) getMixer(w http.ResponseWriter, r *http.Request) {
	if s.Mixer == nil {
		fail(w, r, 503, "mixer_disabled", "mixer.disabled")
		return
	}
	st, err := s.Mixer.Read()
	if errors.Is(err, mixer.ErrOff) {
		fail(w, r, 404, "mixer_off", "mixer.off")
		return
	}
	if err != nil {
		s.Log.Error("mixer", "err", err)
		fail(w, r, 500, "internal", "mixer.read_failed")
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) postMixer(w http.ResponseWriter, r *http.Request) {
	if s.Mixer == nil {
		fail(w, r, 503, "mixer_disabled", "mixer.disabled")
		return
	}
	var c mixer.Command
	if err := decode(r, &c); err != nil {
		fail(w, r, 400, "bad_request", "mixer.bad_command")
		return
	}
	if err := c.Validate(); err != nil {
		failErr(w, r, 400, "bad_request", err)
		return
	}
	switch c.Cmd {
	case "output":
		// Streaming from here would not save OUTPUT (lost at a restart, and the cloud would not see it): PUT /output
		fail(w, r, 400, "bad_request", "mixer.use_output")
		return
	case "slots":
		// The slots live in slots.json, which the app owns: PUT /sources/slots writes it and then sends it
		fail(w, r, 400, "bad_request", "mixer.use_slots")
		return
	}
	err := s.Mixer.Do(r.Context(), c)
	var ce *mixer.CommandError
	switch {
	case errors.Is(err, mixer.ErrOff):
		fail(w, r, 409, "mixer_off", "mixer.off")
		return
	case errors.As(err, &ce):
		fail(w, r, 422, ce.Code, mixerErrorKey(c.Cmd, ce.Code))
		return
	case err != nil:
		s.Log.Error("mixer", "cmd", c.Cmd, "err", err)
		fail(w, r, 502, "mixer_error", "mixer.unreachable")
		return
	}
	st, _ := s.Mixer.Read()
	writeJSON(w, 200, map[string]any{"ok": true, "state": st})
}

// mixerImage: thumbnail of a source (source-<slot>.jpg, 1 per second) or what the board's screen shows
// (screen.jpg). The engine writes them with rename: never half-written.
func (s *Server) mixerImage(w http.ResponseWriter, r *http.Request) {
	path := ""
	if s.Mixer != nil {
		path = s.Mixer.Image(r.PathValue("image"))
	}
	if path == "" {
		fail(w, r, 404, "not_found", "mixer.unknown_image")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		fail(w, r, 404, "no_image", "mixer.no_image")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/jpeg")
	io.Copy(w, io.LimitReader(f, 2<<20))
}

// FollowMixer follows the engine while the app lives, with or without open tabs (the SSE has its own
// subscription, hub.followMixer): it saves the output changed from the screen (screenOutput). Without a
// running mixer it retries every 2 s.
func (s *Server) FollowMixer(ctx context.Context) {
	if s.Mixer == nil {
		return
	}
	last := -1 // output.seq already handled (-1 = none): kept when reconnecting to the same engine
	for ctx.Err() == nil {
		// screen.power.seq goes back to 1 with each engine: from zero on each connection (otherwise, after a
		// failed request, the first one of the next engine would be ignored). The 15 s window avoids
		// repeating an old one
		lastPower := 0
		s.Mixer.Watch(ctx, func(st *mixer.State) {
			s.screenOutput(&last, st)
			s.screenPower(ctx, &lastPower, st, time.Now())
		})
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

// screenPower: Restart or Power off from the touch screen's Power menu (screen.power). Once per request
// (seq) and only if recent (≤ 15 s): when the app reconnects to an engine that already had it (e.g.
// after updating the app), it is not repeated. The engine has no permission; the app has the exact
// commands in sudoers.
func (s *Server) screenPower(ctx context.Context, last *int, st *mixer.State, now time.Time) {
	p := st.Screen.Power
	if p == nil || p.Seq == *last {
		return
	}
	*last = p.Seq
	age := now.Sub(time.Unix(0, int64(p.At*1e9)))
	if age < -5*time.Second || age > 15*time.Second {
		s.Log.Info("screen: old power request, ignored", "want", p.Want, "age", age.Round(time.Second).String())
		return
	}
	if s.Power == nil {
		s.Log.Warn("screen: restart/power off not available", "want", p.Want)
		return
	}
	s.Log.Info("screen: " + p.Want + " requested from the Power menu")
	var err error
	if p.Want == "reboot" {
		err = s.Power.Reboot(ctx)
	} else {
		err = s.Power.Poweroff(ctx)
	}
	if err != nil {
		s.Log.Error("screen: "+p.Want+" failed", "err", err)
	}
}

// errSameOutput: what is saved is already what the engine says; the file is not rewritten.
var errSameOutput = errors.New("OUTPUT already saved")

// screenOutput: Start or Stop streaming from the touch screen (output.by == "screen") is saved in OUTPUT
// like a PUT /output from the web (same validation and log; OUTPUT belongs to the device even if
// EnhancedIRL manages the destination), so that it survives a restart and the cloud sees it in its
// configuration. Once per change (output.seq) and only if it differs from what is saved. Not applied:
// the engine already is like that, and a touch does not restart the stream even with other unapplied
// changes.
//
// With by start or socket, what is saved is already the truth (syncOutput sent it, or it is the start
// one): following it could undo a PUT /output just saved with a state older than its command.
func (s *Server) screenOutput(last *int, st *mixer.State) {
	o := st.Output
	if o == nil {
		return
	}
	seen := *last
	*last = o.Seq // also with by start or socket: after the engine restarts, seq goes back to 0
	if o.By != "screen" || o.Seq == seen {
		return
	}
	cfg, err := s.Config.Update(func(cur config.Settings) (config.Settings, error) {
		if cur.Output == o.Mode {
			return cur, errSameOutput
		}
		cur.Output = o.Mode
		return cur, cur.Validate()
	})
	switch {
	case errors.Is(err, errSameOutput):
	case err != nil:
		s.Log.Warn("could not save the output chosen on the screen", "output", o.Mode, "err", err)
	default:
		s.logConfig("configuration saved (encoder screen)", cfg)
	}
}
