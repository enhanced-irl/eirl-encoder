package api

import (
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/auth"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/device"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// --- first-boot wizard (SETUP.md §4): /setup* -------------------------------------------------------
//
// The wizard drives the order of the steps; the API stores what each one ended as. The touch screen
// runs it over the local socket (no session, no setup code: the socket is the physical screen). The web
// runs it only when there is no screen: before an admin password exists it may change nothing but the
// language, and it needs the setup code to set that password (SETUP.md §6, threat model).

type stepView struct {
	ID    string `json:"id"`
	State string `json:"state"` // todo | done | skipped
}

type setupView struct {
	Done          bool       `json:"done"`
	Steps         []stepView `json:"steps"`
	NeedsPassword bool       `json:"needsPassword"`
	// CodeRequired: POST /setup/password needs the setup code (from the web, while there is no password)
	CodeRequired bool `json:"codeRequired"`
	// Language: the device language, so the web can draw the wizard or the sign-in form before a session
	// (additive to the contract)
	Language string `json:"language"`
	// Code: the setup code, ONLY over the local socket and only while there is no password: the screen
	// shows it (additive to the contract)
	Code string `json:"code,omitempty"`
}

func (s *Server) setupView(r *http.Request) setupView {
	d := s.Device.Get()
	pw := s.Auth.Configured()
	v := setupView{Done: d.Setup.Done, NeedsPassword: !pw, CodeRequired: !pw && !isLocal(r), Language: d.Language}
	for _, id := range device.Steps {
		v.Steps = append(v.Steps, stepView{ID: id, State: d.StepState(id, pw)})
	}
	if !pw && isLocal(r) && s.SetupCodePath != "" {
		if code, err := device.ReadCode(s.SetupCodePath); err == nil {
			v.Code = code
		}
	}
	return v
}

// getSetup: works without a session (it only says where the wizard is; nothing secret on the web).
func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	if s.deviceOff(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.setupView(r))
}

// postSetupPassword: {"password", "code"?}: the FIRST admin password. Over the local socket without a
// code; from the web with the setup code (constant-time, attempts limited like the login), and it opens
// a session (the same cookie as the login). 409 password_exists once there is one: changing it is
// PUT /password, with the current one.
func (s *Server) postSetupPassword(w http.ResponseWriter, r *http.Request) {
	if s.deviceOff(w, r) {
		return
	}
	var body struct {
		Password string  `json:"password"`
		Code     *string `json:"code"`
	}
	if decode(r, &body) != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	local := isLocal(r)
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.Auth.Configured() || s.Auth.LoadErr != nil {
		fail(w, r, 409, "password_exists", "setup.password_exists")
		return
	}
	// The password rules first: a typo there should not cost an attempt at the code
	if n := len(body.Password); n < auth.MinPasswordLen || n > auth.MaxPasswordLen {
		ref, _ := passwordError(map[bool]error{true: auth.ErrTooShort, false: auth.ErrTooLong}[n < auth.MinPasswordLen])
		fieldErrors(w, r, config.FieldErrors{"password": ref})
		return
	}
	ip := limitKey(remoteIP(r))
	if !local {
		if s.SetupCodePath == "" {
			fail(w, r, 503, "setup_code_unavailable", "setup.code_unavailable")
			return
		}
		if body.Code == nil || *body.Code == "" {
			fieldError(w, r, "code", "setup.code_required")
			return
		}
		if !s.codeLimit.take(ip, time.Now()) {
			fail(w, r, 429, "rate_limited", "auth.rate_limited")
			return
		}
		ok, err := device.CheckCode(s.SetupCodePath, *body.Code)
		if err != nil {
			s.Log.Error("setup code", "err", err)
			fail(w, r, 503, "setup_code_unavailable", "setup.code_unavailable")
			return
		}
		if !ok {
			s.Log.Warn("setup: wrong setup code", "ip", remoteIP(r).String())
			fail(w, r, 403, "bad_code", "setup.bad_code")
			return
		}
		s.codeLimit.clear(ip)
	}
	if err := s.Auth.SetPassword(body.Password); err != nil {
		if ref, ok := passwordError(err); ok {
			fieldErrors(w, r, config.FieldErrors{"password": ref})
			return
		}
		s.Log.Error("setup: save the admin password", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	s.Log.Info("setup: admin password set", "via", via(r))
	if !local {
		// The same session as a login (one more bcrypt, ~0.1 s, once in the life of the device)
		if tok, err := s.Auth.Login(ip, body.Password, time.Now()); err == nil {
			setSessionCookie(w, tok)
		} else {
			s.Log.Warn("setup: password set but no session opened", "err", err)
		}
	}
	w.WriteHeader(204)
}

// postSetupStep: {"id", "state": "done"|"skipped"}. The password step cannot be skipped, and is done
// only when a password exists.
func (s *Server) postSetupStep(w http.ResponseWriter, r *http.Request) {
	if s.deviceOff(w, r) {
		return
	}
	var body struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if decode(r, &body) != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	errs := config.FieldErrors{}
	if !slices.Contains(device.Steps, body.ID) {
		errs["id"] = i18n.Ref("setup.bad_step")
	}
	switch {
	case body.State != "done" && body.State != "skipped":
		errs["state"] = i18n.Ref("setup.bad_step_state")
	case body.ID == device.StepPassword && body.State == "skipped":
		errs["state"] = i18n.Ref("setup.password_not_skippable")
	}
	if len(errs) > 0 {
		fieldErrors(w, r, errs)
		return
	}
	if body.ID == device.StepPassword && !s.Auth.Configured() {
		fail(w, r, 409, "password_required", "setup.password_required")
		return
	}
	if _, err := s.Device.Update(func(d *device.Settings) error { d.MarkStep(body.ID, body.State); return nil }); err != nil {
		s.Log.Error("save device.json", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	s.Log.Info("setup: step", "id", body.ID, "state", body.State, "via", via(r))
	writeJSON(w, 200, s.setupView(r))
}

// postSetupComplete: the wizard is over (needs an admin password).
func (s *Server) postSetupComplete(w http.ResponseWriter, r *http.Request) {
	if s.deviceOff(w, r) {
		return
	}
	if !s.Auth.Configured() {
		fail(w, r, 409, "password_required", "setup.password_required")
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := s.Device.Update(func(d *device.Settings) error {
		d.Setup.Done, d.Setup.CompletedAt = true, &now
		return nil
	}); err != nil {
		s.Log.Error("save device.json", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	s.Log.Info("setup: complete", "via", via(r))
	writeJSON(w, 200, s.setupView(r))
}

// postSetupRestart: run the wizard again from the settings (session required; the password is kept).
func (s *Server) postSetupRestart(w http.ResponseWriter, r *http.Request) {
	if s.deviceOff(w, r) {
		return
	}
	if _, err := s.Device.Update(func(d *device.Settings) error {
		d.Setup = device.Setup{Skipped: []string{}, Completed: []string{}}
		return nil
	}); err != nil {
		s.Log.Error("save device.json", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	s.Log.Info("setup: wizard restarted", "via", via(r))
	writeJSON(w, 200, s.setupView(r))
}

// attemptLimiter: attempts at the setup code, with the login's rule (auth): 5 per minute per IP (IPv6:
// per /64) and 20 per minute in total, counted BEFORE comparing.
type attemptLimiter struct {
	mu sync.Mutex
	at map[string][]time.Time
}

const (
	codeAttempts       = 5
	codeAttemptsGlobal = 20
	codeWindow         = time.Minute
)

// take records an attempt of key; false if it is over the limit (then it is not recorded).
func (l *attemptLimiter) take(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.at == nil {
		l.at = map[string][]time.Time{}
	}
	recent := func(k string) int {
		kept := l.at[k][:0]
		for _, t := range l.at[k] {
			if now.Sub(t) < codeWindow {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(l.at, k)
		} else {
			l.at[k] = kept
		}
		return len(kept)
	}
	if recent(key) >= codeAttempts || recent("*") >= codeAttemptsGlobal {
		return false
	}
	l.at[key] = append(l.at[key], now)
	l.at["*"] = append(l.at["*"], now)
	return true
}

// clear forgets the attempts of key (a right code), not the global count.
func (l *attemptLimiter) clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.at, key)
}
