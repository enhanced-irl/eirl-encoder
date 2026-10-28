package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/storage"
)

// --- local recording to a USB disk (RECORDING.md §6; MANAGER.md §3, "Local recording") ------------------------------
//
// The settings are REC_MODE and REC_QUALITY in engine.env: saved like the other global keys and taken by the engine
// live (output_config), never with a restart. Recording or not is the engine's record command, never saved (D6). The
// disk is the storage helper's (eirl-storage, root): the app reads its state file and runs its two actions, eject and
// format, through sudo with the exact commands of the sudoers rule.

// StorageHelper: the storage helper, as the sudoers rule names it.
const StorageHelper = "/usr/local/sbin/eirl-storage"

// How long eject and format are waited for. The helper's units have longer limits of their own: a format that runs
// past it goes on, and the state file says how it ends.
const (
	ejectTimeout  = 90 * time.Second
	formatTimeout = 120 * time.Second
)

// errRecordingNow: PUT /recording changes the quality while recording.
var errRecordingNow = i18n.E("record.quality_locked")

// recordingView: GET /recording and the answer of PUT /recording and PUT /record.
type recordingView struct {
	Mode      string              `json:"mode"`
	Quality   string              `json:"quality"`
	Qualities []config.RecQuality `json:"qualities"`
	Record    *mixer.Record       `json:"record"` // the engine's (§6.3); null with an older engine or the engine stopped
	Storage   storageView         `json:"storage"`
}

// storageView: the storage object, the helper's state file (RECORDING.md §4.3) with a closed set of fields and no
// paths. The model and the label are third-party data: the rule of a device's name.
type storageView struct {
	State       string  `json:"state"`
	Reason      string  `json:"reason,omitempty"`
	Seq         int64   `json:"seq"`
	Device      string  `json:"device,omitempty"`
	Model       string  `json:"model,omitempty"`
	FS          string  `json:"fs,omitempty"`
	Label       string  `json:"label,omitempty"`
	SizeBytes   int64   `json:"sizeBytes,omitempty"`
	ReadOnly    *bool   `json:"readOnly,omitempty"`
	Dirty       *bool   `json:"dirty,omitempty"`
	WriteMBps   float64 `json:"writeMBps,omitempty"`
	AnotherDisk string  `json:"anotherDisk,omitempty"`
}

// storageState: the storage object now. Without the file (or without a helper), none; a state outside the list, none.
func (s *Server) storageState() storageView {
	st := storage.State{State: storage.StateNone}
	if s.StoragePath != "" {
		var err error
		if st, err = storage.ReadState(s.StoragePath); err != nil {
			s.Log.Warn("storage state unreadable", "err", err)
		}
	}
	v := storageView{State: st.State, Seq: max(st.Seq, 0)}
	if !storage.States[st.State] {
		v.State = storage.StateNone
		return v
	}
	if storage.Reasons[st.Reason] {
		v.Reason = st.Reason
	}
	if storage.ValidDisk(st.Device) {
		v.Device = st.Device
	}
	if storage.ValidDisk(st.AnotherDisk) {
		v.AnotherDisk = st.AnotherDisk
	}
	if storage.ValidFS(st.FS) {
		v.FS = st.FS
	}
	v.Model, v.Label = mixer.CleanDeviceName(st.Model), mixer.CleanDeviceName(st.Label)
	v.SizeBytes, v.WriteMBps = max(st.SizeBytes, 0), max(st.WriteMBps, 0)
	v.ReadOnly, v.Dirty = st.ReadOnly, st.Dirty
	return v
}

// engineRecord: the engine's recording state; nil with an older engine, one without a recorder, or none running.
func (s *Server) engineRecord() *mixer.Record {
	if s.Mixer == nil {
		return nil
	}
	m, err := s.Mixer.Read()
	if err != nil {
		return nil
	}
	return m.Record
}

// recordingNow: the engine records or is starting to (the storage helper's rule for refusing eject and format).
func recordingNow(rec *mixer.Record) bool {
	return rec != nil && (rec.Mode == "on" || rec.State == "starting" || rec.State == "on")
}

// streamingNow: the output is on air or trying to be (update.Streaming's rule).
func streamingNow(o *mixer.Output) bool {
	return o != nil && (o.Mode == "on" || o.State == "on" || o.State == "connecting")
}

// busyWith: why the engine is busy for the OTA (update.Streaming counts both): recording when only the recording is
// on, streaming otherwise (with REC_MODE=with_output, stopping the stream also stops its recording).
func (s *Server) busyWith() string {
	if s.Mixer != nil {
		if m, err := s.Mixer.Read(); err == nil && recordingNow(m.Record) && !streamingNow(m.Output) {
			return "recording"
		}
	}
	return "streaming"
}

func (s *Server) recordingView(cfg config.Settings) recordingView {
	return recordingView{Mode: cfg.RecMode, Quality: cfg.RecQuality, Qualities: config.RecQualities,
		Record: s.engineRecord(), Storage: s.storageState()}
}

func (s *Server) getRecording(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Config.Load()
	if err != nil {
		s.Log.Error("configuration", "err", err)
		fail(w, r, 500, "internal", "config.read_failed")
		return
	}
	writeJSON(w, 200, s.recordingView(cfg))
}

// putRecording: the mode and the quality, saved and taken by the engine live. The quality cannot change while
// recording (409 recording); the mode can, and off stops the recording (the engine does it).
func (s *Server) putRecording(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode    *string `json:"mode"`
		Quality *string `json:"quality"`
	}
	if err := decode(r, &body); err != nil || body.Mode == nil && body.Quality == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"mode"?, "quality"?}`)
		return
	}
	fe := config.FieldErrors{}
	if body.Mode != nil && !config.ValidRecMode(*body.Mode) {
		fe["mode"] = i18n.Ref("config.one_of", strings.Join(config.RecModes, ", "))
	}
	if body.Quality != nil && !config.ValidRecQuality(*body.Quality) {
		fe["quality"] = i18n.Ref("config.one_of", config.RecQualityIDs())
	}
	if len(fe) > 0 {
		fieldErrors(w, r, fe)
		return
	}
	before := s.Config.Snapshot()
	cfg, _, ok := s.commit(w, r, false, func() (config.Settings, error) {
		return s.Config.Update(func(cur config.Settings) (config.Settings, error) {
			if body.Quality != nil && *body.Quality != cur.RecQuality && recordingNow(s.engineRecord()) {
				return cur, errRecordingNow
			}
			if body.Mode != nil {
				cur.RecMode = *body.Mode
			}
			if body.Quality != nil {
				cur.RecQuality = *body.Quality
			}
			return cur, cur.Validate()
		})
	})
	if !ok {
		return
	}
	// Not cut if the caller hangs up: the file is saved already, and the engine should have it
	ctx, cancel := s.applyCtx(r)
	defer cancel()
	s.applyRecording(ctx, before)
	writeJSON(w, 200, s.recordingView(cfg))
}

// applyRecording: the engine takes the recording's settings live (output_config). Never a restart (RECORDING.md §6.1):
// an engine that cannot (stopped, older, or with other changes waiting for a restart) takes them at its next start.
func (s *Server) applyRecording(ctx context.Context, before config.Snapshot) {
	if s.Mixer == nil {
		return
	}
	if changed, _ := config.DiffEnv(before.Env, s.Config.Env()); !changed {
		return
	}
	if st, err := s.Unit.Status(ctx); err != nil || st.State != "active" || !s.mixing() {
		return
	}
	if err := s.Mixer.Do(ctx, mixer.Command{Cmd: "output_config"}); err != nil {
		s.Log.Warn("recording settings not taken live: the engine takes them at its next start", "err", err)
		return
	}
	s.Log.Info("recording settings applied hot")
}

// putRecord: record or stop, the engine's record command (the screen's REC button does the same).
func (s *Server) putRecord(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On *bool `json:"on"`
	}
	if err := decode(r, &body); err != nil || body.On == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"on": true|false}`)
		return
	}
	if s.Mixer == nil {
		fail(w, r, 503, "mixer_disabled", "mixer.disabled")
		return
	}
	err := s.Mixer.Do(r.Context(), mixer.Command{Cmd: "record", On: body.On})
	var ce *mixer.CommandError
	switch {
	case errors.Is(err, mixer.ErrOff):
		fail(w, r, 503, "mixer_off", "mixer.off")
		return
	case errors.As(err, &ce):
		switch ce.Code {
		case "disabled", "no_disk", "disk_full", "unsupported":
			fail(w, r, 409, ce.Code, mixerErrorKey("record", ce.Code))
		case "unknown_command": // an engine older than the recording
			fail(w, r, 409, "unsupported", mixerErrorKey("record", ce.Code))
		case "timeout":
			fail(w, r, 503, "timeout", "mixer.timeout")
		default:
			fail(w, r, 422, ce.Code, mixerErrorKey("record", ce.Code))
		}
		return
	case err != nil:
		s.Log.Error("mixer", "cmd", "record", "err", err)
		fail(w, r, 503, "mixer_error", "mixer.unreachable")
		return
	}
	s.Log.Info("recording", "on", *body.On, "via", via(r))
	s.getRecording(w, r)
}

func (s *Server) getStorage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.storageState())
}

func (s *Server) postStorageEject(w http.ResponseWriter, r *http.Request) {
	s.storageAction(w, r, "eject", "", ejectTimeout)
}

// postStorageFormat: format the disk the web showed. The seq the confirmation named must still be the state's, with a
// disk there: a disk swapped after the confirmation is never the one formatted.
func (s *Server) postStorageFormat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Seq *int64 `json:"seq"`
	}
	if err := decode(r, &body); err != nil || body.Seq == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"seq": n}`)
		return
	}
	if st := s.storageState(); st.Seq != *body.Seq || st.State == storage.StateNone || st.State == storage.StateChecking {
		fail(w, r, 409, "disk_changed", "storage.disk_changed")
		return
	}
	// The helper checks the seq again under its lock (exit 5): the disk may still change between here and there
	s.storageAction(w, r, "format", fmt.Sprintf("%d\n", *body.Seq), formatTimeout)
}

// storageAction runs `sudo -n eirl-storage <action>` (with stdin: format's confirmed seq) and answers its exit status
// (RECORDING.md §4.2): 0 the storage object; 2 no_disk, 3 recording, 4 busy and 5 disk_changed (409); anything else,
// or a helper that did not finish in time, <action>_failed (500).
func (s *Server) storageAction(w http.ResponseWriter, r *http.Request, action, stdin string, timeout time.Duration) {
	// Not cut if the caller hangs up: the helper's result is logged anyway
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), timeout)
	defer cancel()
	run := s.StorageRunner
	if run == nil {
		run = storage.ExecRunner{}
	}
	start := time.Now()
	out, code, err := run.Run(ctx, stdin, "/usr/bin/sudo", "-n", StorageHelper, action)
	s.Log.Info("storage", "action", action, "code", code, "ms", time.Since(start).Milliseconds(), "via", via(r))
	switch {
	case err == nil && code == storage.ExitOK:
		writeJSON(w, 200, s.storageState())
	case err == nil && code == storage.ExitNoDisk:
		fail(w, r, 409, "no_disk", "storage.no_disk")
	case err == nil && code == storage.ExitRecording:
		fail(w, r, 409, "recording", "storage.recording")
	case err == nil && code == storage.ExitBusy:
		fail(w, r, 409, "busy", "storage.busy")
	case err == nil && code == storage.ExitChanged:
		fail(w, r, 409, "disk_changed", "storage.disk_changed")
	default:
		s.Log.Error("storage action failed", "action", action, "code", code, "err", err, "out", lastOutputLine(out))
		fail(w, r, 500, action+"_failed", "storage."+action+"_failed")
	}
}

// lastOutputLine: the last line of a command's output, short, for the log.
func lastOutputLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if r := []rune(l); len(r) > 200 {
		l = string(r[:200])
	}
	return l
}
