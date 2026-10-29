package api

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/auth"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/device"
)

// --- the rescue credential and the re-authentication (MANAGER.md §6, SETUP.md §6) ---------------------------------
//
// Linked to EnhancedIRL, the cloud can set the local password without knowing it (set_local_password). Its
// counterweight: the setup code, which proves physical access (the touch screen shows it on demand, the label
// carries it), ALWAYS sets a new web password, and whoever holds the screen can set one without it. And the other
// way round, what hands the device to an account or takes it from one (linking, unlinking) needs the current web
// password from the web, not only a session: a session left open on a shared computer does not give the device away.

// resetPassword: POST /password/reset {"code", "password"} sets a new admin password without the current one.
// From the web with the setup code (normalised as a person types it, constant-time, its own attempt limit shared
// with the wizard's first password: the same secret), and it opens a session; over the local socket (the screen)
// without a code. Either way every existing session is closed and the event is logged (never the code or the
// password).
func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if decode(r, &body) != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	// The web never gets here without a password (requireAuth); the socket has no requireAuth: the same answers
	switch {
	case s.Auth.LoadErr != nil:
		fail(w, r, 503, "password_unreadable", "access.password_unreadable")
		return
	case !s.Auth.Configured():
		fail(w, r, 503, "setup_required", "access.setup_required")
		return
	}
	// The password rules first: a typo there should not cost an attempt at the code
	if n := len(body.Password); n < auth.MinPasswordLen || n > auth.MaxPasswordLen {
		ref, _ := passwordError(map[bool]error{true: auth.ErrTooShort, false: auth.ErrTooLong}[n < auth.MinPasswordLen])
		fieldErrors(w, r, config.FieldErrors{"password": ref})
		return
	}
	local := isLocal(r)
	ip := limitKey(remoteIP(r))
	if !local {
		if s.SetupCodePath == "" {
			fail(w, r, 503, "setup_code_unavailable", "setup.code_unavailable")
			return
		}
		if body.Code == "" {
			fieldError(w, r, "code", "setup.code_required")
			return
		}
		now := time.Now()
		if !s.codeLimit.take(ip, now, s.Auth.Known(ip, now)) {
			fail(w, r, 429, "rate_limited", "auth.rate_limited")
			return
		}
		ok, err := device.CheckCode(s.SetupCodePath, body.Code)
		if err != nil {
			s.Log.Error("password reset: setup code", "err", err)
			fail(w, r, 503, "setup_code_unavailable", "setup.code_unavailable")
			return
		}
		if !ok {
			s.Log.Warn("password reset: wrong setup code", "ip", remoteIP(r).String())
			fail(w, r, 403, "bad_code", "setup.bad_code")
			return
		}
		s.codeLimit.clear(ip)
	}
	s.setupMu.Lock() // one password write at a time with the wizard's
	err := s.Auth.SetPassword(body.Password)
	s.setupMu.Unlock()
	if err != nil {
		if ref, ok := passwordError(err); ok {
			fieldErrors(w, r, config.FieldErrors{"password": ref})
			return
		}
		s.Log.Error("password reset: save the admin password", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	if local {
		s.Log.Warn("admin password reset from the screen: every web session closed", "via", via(r))
	} else {
		s.Log.Warn("admin password reset with the setup code: every web session closed", "via", via(r), "ip", remoteIP(r).String())
		now := time.Now()
		s.Auth.MarkKnown(ip, now)
		setSessionCookie(w, s.Auth.OpenSession(now))
	}
	w.WriteHeader(204)
}

// getSetupCode: GET /setup/code, only over the local socket (never registered on the web): the setup code for the
// screen's Settings › Device › Name & password, revealed on demand with its eye. 503 without a code.
func (s *Server) getSetupCode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.SetupCodePath == "" {
		fail(w, r, 503, "setup_code_unavailable", "setup.code_unavailable")
		return
	}
	code, err := device.ReadCode(s.SetupCodePath)
	if err != nil {
		s.Log.Error("setup code", "err", err)
		fail(w, r, 503, "setup_code_unavailable", "setup.code_unavailable")
		return
	}
	writeJSON(w, 200, map[string]string{"code": code})
}

// reauth: the current web password, from the web, for what hands the device to an account or takes it from one
// (MANAGER.md §6). The screen (local socket) is physical access and is never asked. The attempts share PUT /password's
// limit (5 a minute per IP, the total not for a known client). false: it has answered.
func (s *Server) reauth(w http.ResponseWriter, r *http.Request, pw string) bool {
	if isLocal(r) {
		return true
	}
	if pw == "" {
		fieldError(w, r, "password", "auth.password_required")
		return false
	}
	key, now := limitKey(remoteIP(r)), time.Now()
	if !s.passwordLimit.take(key, now, s.Auth.Known(key, now)) {
		fail(w, r, 429, "rate_limited", "auth.rate_limited")
		return false
	}
	switch err := s.Auth.CheckPassword(pw); {
	case errors.Is(err, auth.ErrBadPassword):
		s.Log.Warn("re-authentication: wrong current password", "path", r.URL.Path, "ip", remoteIP(r).String())
		fail(w, r, 403, "bad_password", "auth.bad_current")
		return false
	case err != nil:
		fail(w, r, 503, "setup_required", "access.setup_required")
		return false
	}
	s.passwordLimit.clear(key)
	return true
}

// passwordBody reads an optional {"password"} body (a DELETE from the screen has none). false: it has answered.
func passwordBody(w http.ResponseWriter, r *http.Request, pw *string) bool {
	var body struct {
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil && !errors.Is(err, io.EOF) {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return false
	}
	*pw = body.Password
	return true
}
