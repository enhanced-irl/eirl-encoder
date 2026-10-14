package api

import (
	"context"
	"net/http"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// --- languages of the API messages (SETUP.md §3) ----------------------------------------------------
//
// Every error keeps its stable `error` code; `message` is rendered here in the request language: the
// first supported primary tag of Accept-Language (the web sends its language on every fetch, the screen
// its device language), else the device language, else English.

// deviceLanguage: the language of device.json (English without a device store).
func (s *Server) deviceLanguage() string {
	if s.Device != nil {
		return s.Device.Language()
	}
	return i18n.Fallback
}

// DeviceLanguage: the device language, for the cloud client's own messages.
func (s *Server) DeviceLanguage() string { return s.deviceLanguage() }

// withLang negotiates the language of the request and leaves it in its context.
func (s *Server) withLang(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := i18n.Negotiate(r.Header.Get("Accept-Language"), s.deviceLanguage())
		next.ServeHTTP(w, r.WithContext(i18n.WithLang(r.Context(), lang)))
	})
}

// reqLang: the language negotiated for r (English if withLang did not run).
func reqLang(r *http.Request) string {
	if l, ok := i18n.LangFrom(r.Context()); ok {
		return l
	}
	return i18n.Fallback
}

// ctxLang: the language of a request context, else the device language (the SSE status, the cloud).
func (s *Server) ctxLang(ctx context.Context) string {
	if l, ok := i18n.LangFrom(ctx); ok {
		return l
	}
	return s.deviceLanguage()
}

// fail answers {"error": kind, "message": <key in the request language>}.
func fail(w http.ResponseWriter, r *http.Request, code int, kind, key string, args ...any) {
	apiError(w, code, kind, i18n.Msg(reqLang(r), key, args...))
}

// failErr: like fail, with the message of err (an *i18n.Error, or its text as is).
func failErr(w http.ResponseWriter, r *http.Request, code int, kind string, err error) {
	apiError(w, code, kind, i18n.Render(reqLang(r), err))
}

// fieldErrors answers 400 {"error": "validation", "fields": {…}} with every message in the request
// language.
func fieldErrors(w http.ResponseWriter, r *http.Request, fe config.FieldErrors) {
	writeJSON(w, 400, map[string]any{"error": "validation", "fields": fe.Render(reqLang(r))})
}

// fieldError: one field error (key with args).
func fieldError(w http.ResponseWriter, r *http.Request, field, key string, args ...any) {
	fieldErrors(w, r, config.FieldErrors{field: i18n.Ref(key, args...)})
}
