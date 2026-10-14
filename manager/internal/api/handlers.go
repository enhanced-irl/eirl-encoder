package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/audiolevel"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/auth"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/live"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

// --- session ----------------------------------------------------------------------

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if decode(r, &body) != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	tok, err := s.Auth.Login(limitKey(remoteIP(r)), body.Password, time.Now())
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		fail(w, r, 429, "rate_limited", "auth.rate_limited")
		return
	case err != nil:
		s.Log.Warn("login failed", "ip", remoteIP(r).String())
		fail(w, r, 401, "bad_password", "auth.bad_password")
		return
	}
	setSessionCookie(w, tok)
	w.WriteHeader(204)
}

// setSessionCookie: the session cookie (login and the first password of the wizard).
func setSessionCookie(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		MaxAge: int(auth.SessionTTL / time.Second),
	})
}

// passwordError: the user-visible message of a password rule (auth.ErrTooShort/ErrTooLong), as a ref.
func passwordError(err error) (string, bool) {
	switch {
	case errors.Is(err, auth.ErrTooShort):
		return i18n.Ref("auth.too_short", auth.MinPasswordLen), true
	case errors.Is(err, auth.ErrTooLong):
		return i18n.Ref("auth.too_long", auth.MaxPasswordLen), true
	}
	return "", false
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.Auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if decode(r, &body) != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	c, _ := r.Cookie(cookieName)
	err := s.Auth.ChangePassword(c.Value, body.Current, body.New)
	if ref, ok := passwordError(err); ok {
		apiError(w, 400, "validation", i18n.Text(reqLang(r), ref))
		return
	}
	switch {
	case errors.Is(err, auth.ErrBadPassword):
		fail(w, r, 403, "bad_password", "auth.bad_current")
	case err != nil:
		s.Log.Error("password change", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
	default:
		s.Log.Info("password changed")
		w.WriteHeader(204)
	}
}

// --- status ---------------------------------------------------------------------

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.collect(r.Context()))
}

// events: SSE. The status every 2 s, new journal lines and, with ?levels=1, the level meter (10 Hz).
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, r, 500, "internal", "common.no_streaming")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fl.Flush()

	// Long stream: without the server's read deadline (otherwise it cancels the request when it expires)
	http.NewResponseController(w).SetReadDeadline(time.Time{})
	ch, stop := s.hub.subscribe()
	defer stop()
	var levels <-chan audiolevel.Level // nil = no level meter: that case never fires
	if r.URL.Query().Get("levels") == "1" {
		lc, lstop := s.Meter.Subscribe()
		defer lstop()
		levels = lc
	}
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, e.data)
		case l := <-levels:
			fmt.Fprintf(w, "event: level\ndata: {\"peakDb\":[%.1f,%.1f],\"rmsDb\":[%.1f,%.1f]}\n\n", l.PeakDb[0], l.PeakDb[1], l.RmsDb[0], l.RmsDb[1])
		case <-keepalive.C:
			if !s.sessionValid(r) { // logout or password change: the stream is cut
				return
			}
			fmt.Fprint(w, ": ka\n\n")
		}
		fl.Flush()
	}
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	n := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("lines")); err == nil && v > 0 && v <= 500 {
		n = v
	}
	entries, err := s.Unit.Tail(r.Context(), n)
	if err != nil {
		s.Log.Error("journal", "err", err)
		fail(w, r, 500, "internal", "logs.read_failed")
		return
	}
	if entries == nil {
		entries = []service.Entry{}
	}
	writeJSON(w, 200, entries)
}

// --- destination ------------------------------------------------------------------

type destinationView struct {
	config.Public
	PendingRestart bool `json:"pendingRestart"`
	Restarted      bool `json:"restarted,omitempty"`
}

func (s *Server) destinationView(r *http.Request, cfg config.Settings) destinationView {
	v := destinationView{Public: cfg.Public()}
	if st, err := s.Unit.Status(r.Context()); err == nil {
		v.PendingRestart = s.pending(st.State)
	}
	return v
}

func (s *Server) getDestination(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Config.Load()
	if err != nil {
		fail(w, r, 500, "internal", "config.read_failed")
		return
	}
	writeJSON(w, 200, s.destinationView(r, cfg))
}

func (s *Server) putDestination(w http.ResponseWriter, r *http.Request) {
	var p config.Patch
	if err := decode(r, &p); err != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	s.saveAndApply(w, r, p.Apply, func(cur config.Settings) (config.Settings, error) {
		if cur.ManagedBy == "eirl" {
			return cur, errManaged
		}
		return p.ApplyTo(cur)
	})
}

// putOutput: start (on) or stop (off) streaming.
func (s *Server) putOutput(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Output string `json:"output"`
		Apply  bool   `json:"apply"`
	}
	if decode(r, &body) != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	s.saveAndApply(w, r, body.Apply, func(cur config.Settings) (config.Settings, error) {
		cur.Output = body.Output
		return cur, cur.Validate()
	})
}

var errManaged = i18n.E("destination.managed")

func (s *Server) saveAndApply(w http.ResponseWriter, r *http.Request, apply bool, fn func(config.Settings) (config.Settings, error)) {
	before := s.Config.Hashes()
	cfg, err := s.Config.Update(fn)
	var fe config.FieldErrors
	switch {
	case errors.As(err, &fe):
		fieldErrors(w, r, fe)
		return
	case errors.Is(err, errManaged):
		failErr(w, r, 409, "managed", err)
		return
	case err != nil:
		s.Log.Error("save configuration", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	s.logConfig("configuration saved", cfg)
	restarted := false
	if apply { // the same rule as the cloud (applySaved)
		if restarted, err = s.applySaved(r.Context(), before); err != nil {
			s.Log.Error("restart", "err", err)
			fail(w, r, 500, "restart_failed", "apply.restart_failed")
			return
		}
	}
	v := s.destinationView(r, cfg)
	v.Restarted = restarted
	writeJSON(w, 200, v)
}

// --- uplinks (SRTLA) ----------------------------------------------------------------

// putUplink: {"disabled": true|false} → takes ONE uplink out of the SRTLA bonding or back in. Applied
// live: the stream rereads the file every 2 s and reloads srtla_send without a cut. The full state
// arrives through the SSE; here it is only confirmed.
func (s *Server) putUplink(w http.ResponseWriter, r *http.Request) {
	if s.Uplinks == nil {
		fail(w, r, 503, "uplinks_disabled", "uplinks.disabled")
		return
	}
	name := r.PathValue("name")
	var body struct {
		Disabled *bool `json:"disabled"`
	}
	if err := decode(r, &body); err != nil || body.Disabled == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"disabled": true|false}`)
		return
	}
	names, err := s.Uplinks.SetDisabled(name, *body.Disabled, func(path string, data []byte) error {
		return config.WriteAtomic(path, data, 0o640)
	})
	switch {
	case errors.Is(err, uplinks.ErrInvalid):
		fieldError(w, r, "name", "uplinks.bad_name")
		return
	case err != nil:
		s.Log.Error("save disabled uplinks", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	s.Log.Info("disabled uplinks", "disabled", strings.Join(names, ","))
	writeJSON(w, 200, map[string]any{"name": name, "disabled": *body.Disabled})
}

// --- service ------------------------------------------------------------------------

func (s *Server) serviceAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	switch action {
	case "start", "stop", "restart":
	default:
		fail(w, r, 404, "not_found", "common.unknown_action")
		return
	}
	if err := s.Unit.Control(r.Context(), action); err != nil {
		s.Log.Error("service", "action", action, "err", err)
		fail(w, r, 500, "control_failed", "service."+action+"_failed")
		return
	}
	s.Log.Info("service", "action", action)
	st, _ := s.Unit.Status(r.Context())
	writeJSON(w, 202, map[string]string{"state": st.State})
}

// --- live preview ---------------------------------------------------------------------

// liveOffer: signalling of the WebRTC preview. One request: the browser's SDP offer → the app's answer
// with its candidates (ICE lite, no trickle).
func (s *Server) liveOffer(w http.ResponseWriter, r *http.Request) {
	if s.Live == nil {
		fail(w, r, 503, "live_disabled", "live.disabled")
		return
	}
	var req struct {
		SDP string `json:"sdp"`
	}
	if err := decode(r, &req); err != nil {
		fail(w, r, 400, "bad_request", "common.bad_request")
		return
	}
	c, _ := r.Cookie(cookieName) // requireAuth already checked it
	tok := c.Value
	// Key = hash of the token: the relay does not need (and must not keep) the token in clear
	sum := sha256.Sum256([]byte(tok))
	answer, err := s.Live.Answer(hex.EncodeToString(sum[:8]), req.SDP, func() bool { return s.Auth.Valid(tok, time.Now()) })
	switch {
	case errors.Is(err, live.ErrBusy):
		fail(w, r, 429, "busy", "live.busy")
	case errors.Is(err, live.ErrBadOffer):
		fail(w, r, 400, "bad_offer", "live.bad_offer")
	case err != nil:
		s.Log.Error("live preview", "err", err)
		fail(w, r, 500, "live_failed", "live.failed")
	default:
		writeJSON(w, 200, map[string]string{"sdp": answer})
	}
}
