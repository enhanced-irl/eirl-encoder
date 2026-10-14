package appapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Ported from the former Python engine's tests: a fake app (HTTP on a Unix socket, like the real one), no GStreamer, no
// board.

var qrRows = func() []string {
	rows := []string{strings.Repeat("1", 21)}
	for i := 0; i < 20; i++ {
		rows = append(rows, strings.Repeat("10", 10)+"1")
	}
	return rows
}()

type reply struct {
	code int
	body any
}

type seenReq struct {
	method, uri string // with the prefix
	body        any
}

type fakeApp struct {
	dir, sock string
	srv       *httptest.Server
	mu        sync.Mutex
	routes    map[string]reply // "METHOD /path?query" (without the prefix)
	seen      []seenReq
	langs     []string // the Accept-Language of each request in seen
	raws      [][]byte // the raw body of each request in seen
}

// shortDir: a temporary directory with a short path (a Unix socket path has ~104 bytes on macOS).
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "aa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func newFakeApp(t *testing.T) *fakeApp {
	t.Helper()
	a := &fakeApp{dir: shortDir(t)}
	a.sock = filepath.Join(a.dir, "api.sock")
	l, err := net.Listen("unix", a.sock)
	if err != nil {
		t.Fatal(err)
	}
	a.srv = httptest.NewUnstartedServer(http.HandlerFunc(a.answer))
	a.srv.Listener.Close()
	a.srv.Listener = l
	a.srv.Start()
	t.Cleanup(a.srv.Close)
	st := map[string]any{"enabled": true, "accessPoint": map[string]any{"enabled": true, "state": "on"},
		"lan":     map[string]any{"enabled": false},
		"cameras": map[string]any{"max": 1, "items": []any{map[string]any{"id": "net:cam1", "name": "Móvil"}}}}
	a.routes = map[string]reply{
		"GET /network": {200, st},
		"GET /network/credentials?qr=rows": {200, map[string]any{"wifi": map[string]any{"ssid": "EnhancedIRL-A1B2",
			"password": "abcdEFGH1234abcdEFGH", "qrRows": qrRows}}},
		"PUT /network": {409, map[string]any{"error": "on_air", "message": "hay una cámara de red al aire"}},
	}
	return a
}

func (a *fakeApp) answer(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body any
	if len(raw) > 0 {
		json.Unmarshal(raw, &body)
	}
	a.mu.Lock()
	a.seen = append(a.seen, seenReq{r.Method, r.RequestURI, body})
	a.langs = append(a.langs, r.Header.Get("Accept-Language"))
	a.raws = append(a.raws, raw)
	// Like the app: the routes under /api/v1 (without the prefix, 403 like its whitelist)
	rep, ok := reply{}, false
	if p, found := strings.CutPrefix(r.RequestURI, "/api/v1/"); found {
		rep, ok = a.routes[r.Method+" /"+p]
	}
	a.mu.Unlock()
	if !ok {
		rep = reply{403, map[string]any{"error": "local_forbidden", "message": "no"}}
	}
	out, _ := json.Marshal(rep.body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.code)
	w.Write(out)
}

func (a *fakeApp) route(key string, code int, body any) {
	a.mu.Lock()
	a.routes[key] = reply{code, body}
	a.mu.Unlock()
}

func (a *fakeApp) drop(key string) {
	a.mu.Lock()
	delete(a.routes, key)
	a.mu.Unlock()
}

func (a *fakeApp) last() seenReq {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.seen) == 0 {
		return seenReq{}
	}
	return a.seen[len(a.seen)-1]
}

// lastLang is the Accept-Language of the last request.
func (a *fakeApp) lastLang() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.langs) == 0 {
		return ""
	}
	return a.langs[len(a.langs)-1]
}

func (a *fakeApp) allSeen() []seenReq {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]seenReq(nil), a.seen...)
}

// js decodes JSON like the client does (json.Number).
func js(t *testing.T, s string) any {
	t.Helper()
	v, ok := parseJSON([]byte(s))
	if !ok {
		t.Fatalf("bad JSON in the test: %s", s)
	}
	return v
}

// syncBuf is a log sink safe across goroutines.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func intp(i int) *int    { return &i }
func boolp(b bool) *bool { return &b }
