// Package appapi is the screen's client of the app's local API (eirl-encoderd): HTTP/1.1 over its Unix socket
// (MANAGER.md, «socket local»), with the same routes, JSON and errors as the web, and a whitelist in the app (the
// rest is 403 local_forbidden). The socket's permissions are the authentication (0660 eirl-encoder; the engine runs
// with that group). It is the Go port of the former Python engine's NetState and wifi_info (the
// wifi.json fallback of an older app without the socket).
//
// Rules (MIXER.md §18-§19, WIFI.md §6): nothing here runs on the draw goroutine (NetState has its own); a request
// or its body is never logged (the wifi keyboard sends passwords through here); errors carry only the HTTP code and
// the app's `error` and `message`.
package appapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// Sock is the app's local API socket.
	Sock = "/run/eirl-encoder/api.sock"
	// Prefix: the routes are the web's; Call receives the part after it (/network…).
	Prefix = "/api/v1"
	// GetTimeout: GETs answer in ms; if the app hangs, «no responde» and move on.
	GetTimeout = 3 * time.Second
	// PutTimeout: a network PUT waits until it is applied (up to ~10 s if it creates ap0; 2 min at most).
	PutTimeout = 150 * time.Second
	// PostTimeout: connect answers 202 at once (the result comes in the state); forget, immediately.
	PostTimeout = 20 * time.Second
	// MaxBody caps what is read of a response; a longer one does not parse and counts as {}.
	MaxBody = 1 << 20
)

// APIError is a request that did not work: Status 0 if there is no app (no socket, or it does not answer), or the
// HTTP code with the app's `error` (Kind) and `message` (in the language the request asked for with
// Accept-Language, shown as is). It never carries anything of the request.
//
// Deviation from the former Python engine: with Status 0 the Message is empty (it put the exception's class name there, which the
// screen then showed instead of «La app no responde»).
type APIError struct {
	Status  int
	Kind    string
	Message string
}

func (e *APIError) Error() string { return strconv.Itoa(e.Status) + " " + e.Kind }

// userText is what the screen says for a failed change: the app's message, or a fallback in lang.
func (e *APIError) userText(lang string) string {
	switch {
	case e.Message != "":
		return e.Message
	case e.Status == 0:
		return text(lang, "not_responding")
	default:
		return text(lang, "failed", e.Kind)
	}
}

// Call makes a request to the app and returns its code and decoded JSON ({} for an empty or unreadable body;
// numbers as json.Number). body is the JSON to send (nil: none); Call consumes it: it is zeroed when the exchange
// ends, so a secret in it does not stay in memory. timeout 0 means GetTimeout. The error is always an *APIError.
func Call(ctx context.Context, sock, method, path string, body []byte, timeout time.Duration) (int, any, error) {
	code, d, e := call(ctx, sock, "", method, path, body, timeout)
	if e != nil {
		return code, d, e
	}
	return code, d, nil
}

// call is Call with the language of the answer's messages (Accept-Language; "" = none: the device's).
func call(ctx context.Context, sock, lang, method, path string, body []byte, timeout time.Duration) (int, any, *APIError) {
	if sock == "" {
		sock = Sock
	}
	if timeout <= 0 {
		timeout = GetTimeout
	}
	var wb *wipeBody
	if body != nil {
		wb = &wipeBody{b: body}
		defer wb.wipe()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	unavailable := &APIError{Status: 0, Kind: "unavailable"}
	var rd io.Reader
	if wb != nil {
		rd = wb
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+Prefix+path, rd)
	if err != nil {
		return 0, nil, unavailable
	}
	req.Header.Set("Accept", "application/json")
	if knownLanguages[lang] {
		req.Header.Set("Accept-Language", lang)
	}
	if wb != nil {
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := clientFor(sock).Do(req)
	if err != nil {
		return 0, nil, unavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	resp.Body.Close()
	if err != nil {
		return 0, nil, unavailable
	}
	d, ok := parseJSON(raw)
	if !ok {
		d = map[string]any{}
	}
	if resp.StatusCode >= 400 {
		m := obj(d)
		kind, ok := m["error"].(string)
		if !ok {
			kind = "http_" + strconv.Itoa(resp.StatusCode)
		}
		msg, _ := m["message"].(string)
		if r := []rune(msg); len(r) > 200 {
			msg = string(r[:200])
		}
		return resp.StatusCode, d, &APIError{Status: resp.StatusCode, Kind: kind, Message: msg}
	}
	return resp.StatusCode, d, nil
}

// clientFor: an HTTP client over the Unix socket, one connection per request (like Python's), no redirects, no
// proxy, no compression.
func clientFor(sock string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
			DisableKeepAlives:      true,
			DisableCompression:     true,
			MaxResponseHeaderBytes: 64 << 10,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// wipeBody is a request body that can be zeroed when the exchange is over: the transport may still read it after
// Do returns (on an error), so reads and the wipe share a lock and a wiped body reads as EOF. Best effort: the
// transport's own write buffer keeps a copy until it is collected.
type wipeBody struct {
	mu   sync.Mutex
	b    []byte
	off  int
	done bool
}

func (w *wipeBody) Read(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done || w.off >= len(w.b) {
		return 0, io.EOF
	}
	n := copy(p, w.b[w.off:])
	w.off += n
	return n, nil
}

func (w *wipeBody) wipe() {
	w.mu.Lock()
	clear(w.b)
	w.done = true
	w.mu.Unlock()
}

// parseJSON decodes like Python's json.loads on the UTF-8 text: false for invalid UTF-8, invalid JSON or trailing
// data. Numbers stay json.Number so ints and floats can be told apart (Python: isinstance(x, int)).
func parseJSON(raw []byte) (any, bool) {
	if len(raw) == 0 {
		return map[string]any{}, true
	}
	if !utf8.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return v, true
}

// obj is v as a JSON object, or nil.
func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

var reInt = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// intIn is v as an integer in [lo, hi] (a JSON integer literal: not a bool, not 1.0).
func intIn(v any, lo, hi int) (int, bool) {
	n, ok := v.(json.Number)
	if !ok || !reInt.MatchString(string(n)) {
		return 0, false
	}
	i, err := strconv.Atoi(string(n))
	if err != nil || i < lo || i > hi {
		return 0, false
	}
	return i, true
}

// jsonBody marshals a small request body (no secrets: those are built by connectBody).
func jsonBody(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
