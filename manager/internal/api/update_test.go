package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
)

type fakeUpdate struct {
	st        update.Status
	checks    int
	installs  int
	installEr error
	checkErr  error
}

func (f *fakeUpdate) Status() update.Status { return f.st }
func (f *fakeUpdate) Check() error          { f.checks++; return f.checkErr }
func (f *fakeUpdate) Install(context.Context) error {
	f.installs++
	return f.installEr
}

func TestUpdateAPI(t *testing.T) {
	e := setup(t, true)
	// Without updates (no helper or no update directory): 503
	ck := e.login(t)
	if w := e.do("GET", "/api/v1/update", "", ck); w.Code != 503 {
		t.Fatalf("no updater: %d", w.Code)
	}

	m := &update.Manifest{Version: "0.2.0", Notes: map[string]string{"en": "Fixes.", "es": "Arreglos."},
		Bundle: update.Bundle{Size: 1234}}
	f := &fakeUpdate{st: update.Status{Current: "0.1.0", State: update.StateReady, Available: m, Ready: true,
		Progress: 1, LastCheck: time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC), AutoCheck: true}}
	e.srv.Update = f
	e.srv.Info.Version = "0.1.0"
	os.WriteFile(filepath.Join(e.dir, "VERSION"), []byte("0.1.0\n"), 0o644)
	os.WriteFile(filepath.Join(e.dir, "BUILD"), []byte("commit=abc123 date=2026-10-14T00:00:00Z\n"), 0o644)
	os.WriteFile(filepath.Join(e.dir, "ENGINE"), []byte("0.1.0 abc123 release\n"), 0o644)
	e.srv.Versions = Versions{ReleaseFile: filepath.Join(e.dir, "VERSION"), EngineFile: filepath.Join(e.dir, "ENGINE")}
	e.h = e.srv.Handler()
	ck = e.login(t)

	// Session required
	if w := e.do("GET", "/api/v1/update", "", ""); w.Code != 401 {
		t.Errorf("without session: %d", w.Code)
	}
	w := e.do("GET", "/api/v1/update", "", ck, "Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	var v UpdateView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("GET %d %s", w.Code, w.Body)
	}
	if v.Current != "0.1.0" || v.State != "ready" || v.Available == nil || v.Available.Version != "0.2.0" ||
		v.Available.Notes != "Arreglos." || v.Available.Size != 1234 || !v.CanInstall || v.LastCheck == nil || v.App != "0.1.0" {
		t.Errorf("view %+v", v)
	}
	// Contract shape: the fields of CONTRACT §4 are always there (available null when nothing)
	var raw map[string]any
	json.Unmarshal(w.Body.Bytes(), &raw)
	for _, k := range []string{"current", "state", "available", "progress", "lastCheck"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing %q", k)
		}
	}
	if w := e.do("GET", "/api/v1/update", "", ck, "Accept-Language", "de"); !strings.Contains(w.Body.String(), `"notes":"Fixes."`) {
		t.Errorf("fallback to English: %s", w.Body)
	}

	// Mutations: JSON + same origin (CSRF), like the rest
	if w := e.do("POST", "/api/v1/update/check", "{}", ck, "Sec-Fetch-Site", "cross-site"); w.Code != 403 {
		t.Errorf("cross-site check: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/update/check", "{}", ck); w.Code != 202 || f.checks != 1 {
		t.Errorf("check: %d, %d checks", w.Code, f.checks)
	}
	if w := e.do("POST", "/api/v1/update/install", "{}", ck); w.Code != 202 || f.installs != 1 {
		t.Errorf("install: %d", w.Code)
	}
	for _, c := range []struct {
		err  error
		code int
		kind string
	}{{update.ErrStreaming, 409, "streaming"}, {update.ErrNotReady, 409, "not_ready"}, {update.ErrBusy, 409, "busy"},
		{context.DeadlineExceeded, 500, "helper_failed"}} {
		f.installEr = c.err
		w := e.do("POST", "/api/v1/update/install", "{}", ck)
		var body map[string]string
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != c.code || body["error"] != c.kind || body["message"] == "" {
			t.Errorf("%v: %d %s", c.err, w.Code, w.Body)
		}
	}
	f.checkErr = update.ErrBusy
	if w := e.do("POST", "/api/v1/update/check", "{}", ck); w.Code != 409 {
		t.Errorf("check while installing: %d", w.Code)
	}

	// Streaming: the button is disabled, with the reason
	f.st.Streaming = true
	w = e.do("GET", "/api/v1/update", "", ck)
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.CanInstall || v.Blocked != "streaming" || v.BlockedMsg == "" {
		t.Errorf("streaming view %+v", v)
	}
	// A rolled-back install shows its reason
	f.st.Streaming = false
	f.st.State, f.st.Error = update.StateRolledBack, "unhealthy"
	f.st.LastResult = &update.HelperStatus{State: update.HelperRolledBack, From: "0.1.0", To: "0.2.0", Error: "unhealthy"}
	w = e.do("GET", "/api/v1/update", "", ck)
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.State != "rolled_back" || v.Message == "" || v.LastResult == nil || v.LastResult.To != "0.2.0" || v.LastResult.Message == "" {
		t.Errorf("rolled back view %+v", v)
	}

	// /status carries the versions and the update in short
	w = e.do("GET", "/api/v1/status", "", ck)
	var st Status
	json.Unmarshal(w.Body.Bytes(), &st)
	if st.Version == nil || st.Version.Release != "0.1.0" || st.Version.Engine != "0.1.0" || st.Version.Commit != "abc123" ||
		st.Update == nil || st.Update.Available != "0.2.0" {
		t.Errorf("status version %+v update %+v", st.Version, st.Update)
	}
}

// The touch screen: the same routes over the local socket, without session or CSRF
func TestUpdateOverLocalSocket(t *testing.T) {
	e := setup(t, true)
	f := &fakeUpdate{st: update.Status{Current: "0.1.0", State: update.StateReady, Ready: true,
		Available: &update.Manifest{Version: "0.2.0", Notes: map[string]string{"en": "x", "fr": "y"}}}}
	e.srv.Update = f
	lh := e.srv.LocalHandler()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/update", nil)
	r.Header.Set("Accept-Language", "fr")
	lh.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"notes":"y"`) {
		t.Errorf("GET %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	lh.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/update/install", nil))
	if w.Code != 202 || f.installs != 1 {
		t.Errorf("install %d %s", w.Code, w.Body)
	}
	f.installEr = update.ErrStreaming
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/api/v1/update/install", nil)
	r.Header.Set("Accept-Language", "es")
	lh.ServeHTTP(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "deja de emitir") {
		t.Errorf("streaming %d %s", w.Code, w.Body)
	}
}

func TestUpdateRoutesOnAnyMux(t *testing.T) {
	s := &Server{Update: &fakeUpdate{st: update.Status{State: update.StateIdle}}}
	mux := http.NewServeMux()
	s.registerUpdate(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/update", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":null`) {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}
