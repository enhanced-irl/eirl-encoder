package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/update/syslayer"
)

// --- updates (OTA, UPDATES.md §8; CONTRACT §4) -------------------------------------------------------------
//
// GET /update, POST /update/check (202), POST /update/install (202; 409 streaming | not_ready | busy), POST
// /update/reboot (202; 409 streaming | nothing_pending; 503 power_unavailable: "restart to finish", UPDATES.md §12.5). Web
// (session + CSRF, like every mutation) and local socket (the touch screen has the button; local.go whitelists the
// three routes). Messages come from the i18n catalog (update.*) in the request language. The app only
// checks, downloads and verifies; installing is the root helper's job (eirl-update.service).

// UpdateControl is update.Updater (an interface for tests).
type UpdateControl interface {
	Status() update.Status
	Check() error
	Install(ctx context.Context) error
}

// registerUpdate adds the /update routes to a mux (the web's and the local socket's).
func (s *Server) registerUpdate(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/update", s.getUpdate)
	mux.HandleFunc("POST /api/v1/update/check", s.postUpdateCheck)
	mux.HandleFunc("POST /api/v1/update/install", s.postUpdateInstall)
	mux.HandleFunc("POST /api/v1/update/reboot", s.postUpdateReboot)
}

// UpdateView is GET /update.
type UpdateView struct {
	Current   string               `json:"current"` // installed release
	State     string               `json:"state"`   // idle | checking | available | downloading | ready | installing | rolled_back | error
	Available *UpdateAvailableView `json:"available"`
	Progress  float64              `json:"progress"`
	LastCheck *time.Time           `json:"lastCheck"`
	Error     string               `json:"error,omitempty"`
	Message   string               `json:"message,omitempty"`
	// Additive
	App        string          `json:"app"`               // the running app binary's version (the helper's health check compares it)
	Streaming  bool            `json:"streaming"`         // installing is refused while streaming
	CanInstall bool            `json:"canInstall"`        // the "Update" button is enabled
	Blocked    string          `json:"blocked,omitempty"` // why it is not: streaming | not_ready | busy
	BlockedMsg string          `json:"blockedMessage,omitempty"`
	LastResult *UpdateRunView  `json:"lastResult,omitempty"` // the helper's last run
	AutoCheck  bool            `json:"autoCheck"`
	TestBuild  bool            `json:"testBuild,omitempty"` // this build trusts a test key (never on a customer device)
	Versions   update.Versions `json:"versions"`
	// DownloadWaits: a newer release waits for Wi-Fi or a cable before it downloads by itself ("metered"); a check
	// (POST /update/check) downloads it at once. DownloadWaitsMsg says so in the request's language
	DownloadWaits    string `json:"downloadWaits,omitempty"`
	DownloadWaitsMsg string `json:"downloadWaitsMessage,omitempty"`
	// System: the system layer (UPDATES.md §12): what the last update installed below the app, and what waits for
	// a restart. Absent where the board has no system layer state
	System *UpdateSystemView `json:"system,omitempty"`
}

// UpdateSystemView is GET /update's "system".
type UpdateSystemView struct {
	Release   string     `json:"release,omitempty"` // the release whose system layer is installed
	AppliedAt *time.Time `json:"appliedAt,omitempty"`
	// State: ok | failed (the last one did not install: the previous one stays) | skipped (not the encoder's own
	// system: nothing below the app is updated)
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`   // the stable code when failed
	Message string `json:"message,omitempty"` // in the request's language
	// RestartRequired: something installed acts only after a restart (Pending: hdmi | bluetooth | uplinks |
	// system, what it touches); RestartMessage says so. POST /update/reboot restarts (never while streaming)
	RestartRequired bool     `json:"restartRequired"`
	Pending         []string `json:"pending,omitempty"`
	RestartMessage  string   `json:"restartMessage,omitempty"`
}

type UpdateAvailableView struct {
	Version  string    `json:"version"`
	Notes    string    `json:"notes"` // in the request's language (fallback English)
	Size     int64     `json:"size"`
	Released time.Time `json:"released,omitzero"`
}

type UpdateRunView struct {
	State      string    `json:"state"` // installed | rolled_back | failed | refused | installing
	From       string    `json:"from,omitempty"`
	To         string    `json:"to,omitempty"`
	At         time.Time `json:"at,omitzero"`
	Error      string    `json:"error,omitempty"`
	Message    string    `json:"message,omitempty"`
	Phase      string    `json:"phase,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
}

// updateMessage: what the user reads for a stable code of the updater or the helper, in lang (i18n catalog
// keys update.err.<code>; an unknown code gets the generic one).
func updateMessage(lang, code string) string {
	if code == "" {
		return ""
	}
	key := "update.err." + code
	if !i18n.Has(key) {
		key = "update.err.internal"
	}
	return i18n.Msg(lang, key)
}

// installedVersions: the release, app and engine versions (and the commit from BUILD next to VERSION).
func (s *Server) installedVersions() *update.Versions {
	if s.Versions.ReleaseFile == "" && s.Versions.EngineFile == "" {
		return nil
	}
	v := update.Versions{Release: readVersion(s.Versions.ReleaseFile), App: s.Info.Version, Engine: readVersion(s.Versions.EngineFile)}
	if s.Versions.ReleaseFile != "" {
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(s.Versions.ReleaseFile), "BUILD")); err == nil && len(b) < 1024 {
			for _, f := range strings.Fields(string(b)) {
				if c, ok := strings.CutPrefix(f, "commit="); ok {
					v.Commit = c
				}
			}
		}
	}
	return &v
}

func (s *Server) updateView(r *http.Request) UpdateView {
	st := s.Update.Status()
	lang := reqLang(r)
	v := UpdateView{Current: st.Current, State: st.State, Progress: st.Progress, Error: st.Error,
		Message: updateMessage(lang, st.Error), App: s.Info.Version, Streaming: st.Streaming, CanInstall: st.CanInstallNow(),
		AutoCheck: st.AutoCheck, TestBuild: update.TestBuild()}
	if v.Current == "" {
		v.Current = s.Info.Version // an install from before the OTA (no VERSION file): the app's own
	}
	if iv := s.installedVersions(); iv != nil {
		v.Versions = *iv
	}
	if !st.LastCheck.IsZero() {
		t := st.LastCheck.UTC()
		v.LastCheck = &t
	}
	if m := st.Available; m != nil {
		v.Available = &UpdateAvailableView{Version: m.Version, Notes: m.Note(lang), Size: m.Bundle.Size, Released: m.Released}
	}
	switch {
	case v.CanInstall:
	case st.State == update.StateInstalling:
		v.Blocked = "busy"
	case st.Available != nil && st.Ready && st.Streaming:
		v.Blocked = "streaming"
	case st.Available != nil:
		v.Blocked = "not_ready"
	}
	if v.Blocked != "" {
		v.BlockedMsg = i18n.Msg(lang, "update."+v.Blocked)
	}
	if st.DownloadWaits != "" {
		v.DownloadWaits, v.DownloadWaitsMsg = st.DownloadWaits, i18n.Msg(lang, "update.waits."+st.DownloadWaits)
	}
	v.System = s.systemView(lang)
	if lr := st.LastResult; lr != nil {
		v.LastResult = &UpdateRunView{State: lr.State, From: lr.From, To: lr.To, At: lr.FinishedAt, Error: lr.Error,
			Message: updateMessage(lang, lr.Error), Phase: lr.Phase, StartedAt: lr.StartedAt, FinishedAt: lr.FinishedAt}
		if lr.State == update.HelperInstalled {
			v.LastResult.Message = "" // the UI shows "Updated to <to>"
		}
	}
	return v
}

func (s *Server) getUpdate(w http.ResponseWriter, r *http.Request) {
	if s.Update == nil {
		fail(w, r, 503, "update_unavailable", "update.unavailable")
		return
	}
	writeJSON(w, 200, s.updateView(r))
}

func (s *Server) postUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.Update == nil {
		fail(w, r, 503, "update_unavailable", "update.unavailable")
		return
	}
	if err := s.Update.Check(); errors.Is(err, update.ErrBusy) {
		fail(w, r, 409, "busy", "update.busy")
		return
	}
	writeJSON(w, 202, s.updateView(r))
}

func (s *Server) postUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if s.Update == nil {
		fail(w, r, 503, "update_unavailable", "update.unavailable")
		return
	}
	err := s.Update.Install(r.Context())
	switch {
	case err == nil:
		writeJSON(w, 202, s.updateView(r))
	case errors.Is(err, update.ErrStreaming):
		fail(w, r, 409, "streaming", "update.streaming")
	case errors.Is(err, update.ErrNotReady):
		fail(w, r, 409, "not_ready", "update.not_ready")
	case errors.Is(err, update.ErrBusy):
		fail(w, r, 409, "busy", "update.busy")
	default:
		s.Log.Error("update: cannot start the installer", "err", err)
		fail(w, r, 500, "helper_failed", "update.err.helper_failed")
	}
}

// systemView reads the system layer's state.json (syslayer.State): nil if there is none.
func (s *Server) systemView(lang string) *UpdateSystemView {
	if s.SystemStateDir == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(s.SystemStateDir, syslayer.StateFile)); err != nil {
		return nil
	}
	st := syslayer.ReadState(s.SystemStateDir)
	v := &UpdateSystemView{Release: st.Release, State: "ok"}
	if !st.AppliedAt.IsZero() {
		t := st.AppliedAt.UTC()
		v.AppliedAt = &t
	}
	switch {
	case st.Skipped != "":
		v.State, v.Message = "skipped", i18n.Msg(lang, "update.system.skipped")
	case st.Error != "":
		code, _, _ := strings.Cut(st.Error, ":")
		v.State, v.Error, v.Message = "failed", code, i18n.Msg(lang, "update.system.failed")
	}
	boot := ""
	if s.BootID != nil {
		boot = s.BootID()
	}
	seen := map[string]bool{}
	for _, p := range st.PendingNow(boot) {
		if !seen[p.Area] {
			seen[p.Area] = true
			v.Pending = append(v.Pending, p.Area)
		}
	}
	if len(v.Pending) > 0 {
		sort.Slice(v.Pending, func(i, j int) bool { return areaOrder(v.Pending[i]) < areaOrder(v.Pending[j]) })
		names := make([]string, len(v.Pending))
		for i, a := range v.Pending {
			names[i] = i18n.Msg(lang, "update.area."+a)
		}
		v.RestartRequired = true
		v.RestartMessage = i18n.Msg(lang, "update.restart_required", strings.Join(names, ", "))
	}
	return v
}

func areaOrder(a string) int {
	switch a {
	case "hdmi":
		return 0
	case "bluetooth":
		return 1
	case "uplinks":
		return 2
	}
	return 3
}

// postUpdateReboot: "restart to finish the update" (UPDATES.md §12.5), only when something waits for it and never
// while streaming.
func (s *Server) postUpdateReboot(w http.ResponseWriter, r *http.Request) {
	if s.Power == nil {
		fail(w, r, 503, "power_unavailable", "update.power_unavailable")
		return
	}
	if v := s.systemView(reqLang(r)); v == nil || !v.RestartRequired {
		fail(w, r, 409, "nothing_pending", "update.nothing_pending")
		return
	}
	if s.Update != nil {
		st := s.Update.Status()
		if st.Streaming {
			fail(w, r, 409, "streaming", "update.reboot_streaming")
			return
		}
		if st.State == update.StateInstalling {
			fail(w, r, 409, "busy", "update.busy")
			return
		}
	}
	s.Log.Warn("update: restarting the board to finish the update")
	if err := s.Power.Reboot(r.Context()); err != nil {
		s.Log.Error("update: restart failed", "err", err)
		fail(w, r, 500, "power_failed", "update.power_failed")
		return
	}
	writeJSON(w, 202, map[string]bool{"rebooting": true})
}

// UpdateSummary is what GET /status (and its SSE) says about updates, so the UIs can show "update
// available" without polling /update.
type UpdateSummary struct {
	State      string `json:"state"`
	Available  string `json:"available,omitempty"` // version
	CanInstall bool   `json:"canInstall"`
}

// addVersionAndUpdate fills the additive version/update fields of the status.
func (s *Server) addVersionAndUpdate(st *Status) {
	st.Version = s.installedVersions()
	if s.Update != nil {
		u := s.Update.Status()
		sum := &UpdateSummary{State: u.State, CanInstall: u.CanInstallNow()}
		if u.Available != nil {
			sum.Available = u.Available.Version
		}
		st.Update = sum
	}
}
