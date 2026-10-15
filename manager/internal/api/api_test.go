package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/audiolevel"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/auth"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/hw"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/live"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

const key = "AbCdEfGh0123456789_-AbCdEfGh0123"

type fakeUnit struct {
	mu      sync.Mutex
	state   string
	actions []string
}

func (f *fakeUnit) Status(context.Context) (service.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return service.Status{State: f.state, Started: time.Now().Add(-time.Hour)}, nil
}
func (f *fakeUnit) Control(_ context.Context, a string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, a)
	return nil
}
func (f *fakeUnit) Tail(context.Context, int) ([]service.Entry, error) {
	e, _ := service.ParseJournal([]byte(`{"MESSAGE":"[engine] SRT → srt://h:1?streamid=publish:` + key + `"}`))
	return []service.Entry{e}, nil
}
func (f *fakeUnit) Follow(ctx context.Context, fn func(service.Entry)) error {
	<-ctx.Done()
	return nil
}

type fakeBoard struct{}

func (fakeBoard) Signal() hw.Signal {
	return hw.Signal{Present: true, Width: 1920, Height: 1080, FPS: 30, Supported: true}
}
func (fakeBoard) Audio() hw.Audio   { return hw.Audio{State: "playing", AuStatus0: "0x61"} }
func (fakeBoard) System() hw.System { return hw.System{} }

type env struct {
	srv  *Server
	unit *fakeUnit
	h    http.Handler
	dir  string
}

func setup(t *testing.T, withPassword bool) *env {
	dir := t.TempDir()
	def := filepath.Join(dir, "default")
	os.WriteFile(def, []byte("SRT_URL=srt://relay.example:5000\nSRT_MODE=caller\nSRT_STREAMID=publish:"+key+"\nSRT_PASSPHRASE=secretpassphrase\n"), 0o644)
	hash := filepath.Join(dir, "admin.hash")
	if withPassword {
		h, _ := auth.HashPassword("correcto-123")
		auth.WriteHash(hash, h)
	}
	u := &fakeUnit{state: "active"}
	s := &Server{
		Unit: u, Board: fakeBoard{}, Auth: auth.New(hash),
		Config:    &config.Store{DefaultPath: def, AppPath: filepath.Join(dir, "app.env")},
		StatsPath: filepath.Join(dir, "stats.json"),
		Meter:     &audiolevel.Meter{Addr: "127.0.0.1:0"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		RunStatePath: filepath.Join(dir, "state"), Hostnames: []string{"example.com"},
	}
	return &env{srv: s, unit: u, h: s.Handler(), dir: dir}
}

// setManaged: the active destination becomes the cloud's (id eirl, as a config.set creates it) or, with false, the
// cloud's becomes a local one again: managedBy follows the active destination.
func (e *env) setManaged(t *testing.T, cloudOwned bool) {
	t.Helper()
	_, _, err := e.srv.Config.UpdateDestinations(func(ds *config.Destinations, _ *config.Settings) error {
		from, to := ds.Active, config.CloudID
		if !cloudOwned {
			from, to = config.CloudID, "d-0000abcd"
		}
		d, ok := ds.Get(from)
		if !ok {
			return errors.New("no destination to convert")
		}
		ds.Remove(from)
		d.ID, d.Managed, d.Template, d.Name = to, cloudOwned, config.TemplateEIRL, "EnhancedIRL"
		if !cloudOwned {
			d.Template, d.Name = config.TemplateCustom, "SRT"
		}
		ds.Put(d)
		ds.Active = to
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) do(method, path, body, cookie string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.50:4000"
	if body != "" || method != "GET" {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "RemoteAddr" {
			r.RemoteAddr = hdr[i+1]
		} else if hdr[i] == "Host" {
			r.Host = hdr[i+1]
		} else {
			r.Header.Set(hdr[i], hdr[i+1])
		}
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *env) login(t *testing.T) string {
	w := e.do("POST", "/api/v1/login", `{"password":"correcto-123"}`, "")
	if w.Code != 204 {
		t.Fatalf("login %d %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Errorf("cookie without HttpOnly/SameSite=Strict: %+v", c)
			}
			return c.Value
		}
	}
	t.Fatal("no cookie")
	return ""
}

func TestAccessControl(t *testing.T) {
	if w := setup(t, false).do("GET", "/api/v1/status", "", ""); w.Code != 503 {
		t.Errorf("no password must give 503: %d", w.Code)
	}
	e := setup(t, true)
	if w := e.do("GET", "/api/v1/status", "", "", "RemoteAddr", "8.8.8.8:1234"); w.Code != 403 {
		t.Errorf("public IP must give 403: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/status", "", ""); w.Code != 401 {
		t.Errorf("no session must give 401: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/status", "", "inventado"); w.Code != 401 {
		t.Errorf("made-up token must give 401: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/login", `{"password":"mal"}`, ""); w.Code != 401 {
		t.Errorf("wrong password: %d", w.Code)
	}
	tok := e.login(t)
	if w := e.do("GET", "/api/v1/status", "", tok); w.Code != 200 || !strings.Contains(w.Body.String(), `"phase":"streaming"`) {
		t.Errorf("status %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/logout", "", tok); w.Code != 204 {
		t.Errorf("logout %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/status", "", tok); w.Code != 401 {
		t.Errorf("after logout it must give 401: %d", w.Code)
	}
}

func TestHostAllowlist(t *testing.T) {
	e := setup(t, true)
	for host, want := range map[string]int{"evil.example": 403, "evil.example:80": 403, "192.168.1.232": 401, "[fe80::1]:80": 401, "EXAMPLE.com.": 401, "localhost:80": 401} {
		r := httptest.NewRequest("GET", "/api/v1/status", nil)
		r.RemoteAddr, r.Host = "192.168.1.50:4000", host
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q → %d, want %d", host, w.Code, want)
		}
	}
	if k := limitKey(net.ParseIP("2001:db8:1:2:aaaa::1")); k != "2001:db8:1:2::/64" {
		t.Errorf("IPv6 by /64: %q", k)
	}
}

func TestPendingByHash(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	pending := func() bool {
		return strings.Contains(e.do("GET", "/api/v1/status", "", tok).Body.String(), `"pendingRestart":true`)
	}
	e.do("PUT", "/api/v1/destination", `{"latencyMs":300}`, tok)
	os.WriteFile(filepath.Join(e.dir, "state"), []byte("OUTPUT=on\nSOURCE=hdmi\nCONF_SHA="+e.srv.Config.Hash()+"\n"), 0o644)
	if pending() {
		t.Error("same fingerprint: no pending changes")
	}
	e.do("PUT", "/api/v1/destination", `{"latencyMs":400}`, tok)
	if !pending() {
		t.Error("different fingerprint: there are pending changes")
	}
	// test source: streaming even without HDMI
	os.WriteFile(filepath.Join(e.dir, "state"), []byte("OUTPUT=off\nSOURCE=test\nCONF_SHA=x\n"), 0o644)
	if b := e.do("GET", "/api/v1/status", "", tok).Body.String(); !strings.Contains(b, `"phase":"ready"`) || !strings.Contains(b, `"state":"test"`) {
		t.Errorf("SOURCE=test: %s", b)
	}
}

func TestCSRF(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	if w := e.do("POST", "/api/v1/service/restart", "", tok, "Sec-Fetch-Site", "cross-site"); w.Code != 403 {
		t.Errorf("cross-site: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/service/restart", "", tok, "Origin", "http://evil.example"); w.Code != 403 {
		t.Errorf("foreign origin: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/service/restart", "", tok, "Content-Type", "application/x-www-form-urlencoded"); w.Code != 415 {
		t.Errorf("form: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/service/restart", "", tok, "Origin", "http://example.com", "Sec-Fetch-Site", "same-origin"); w.Code != 202 {
		t.Errorf("same origin must pass: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/service/reboot", "", tok); w.Code != 404 {
		t.Errorf("unknown action: %d", w.Code)
	}
	if got := strings.Join(e.unit.actions, ","); got != "restart" {
		t.Errorf("actions run: %q", got)
	}
}

func TestDestinationNeverLeaksAndApplies(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	w := e.do("GET", "/api/v1/destination", "", tok)
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, key) || strings.Contains(body, "secretpassphrase") {
		t.Fatalf("GET destination leaks secrets: %s", body)
	}
	if !strings.Contains(body, `"masked":"publish:AbCd…0123"`) {
		t.Errorf("mask: %s", body)
	}
	// invalid → 400 with fields, without touching anything or restarting
	w = e.do("PUT", "/api/v1/destination", `{"streamId":"publish:x&mode=listener","apply":true}`, tok)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"streamId"`) || len(e.unit.actions) != 0 {
		t.Errorf("invalid: %d %s %v", w.Code, w.Body, e.unit.actions)
	}
	if w := e.do("PUT", "/api/v1/destination", `{"latencyMs":300,"extra":1}`, tok); w.Code != 400 {
		t.Errorf("unknown fields must be rejected: %d", w.Code)
	}
	// URL pasted from the dashboard + apply → saves and restarts
	// changing server requires giving the passphrase again (the streamid comes in the URL)
	w = e.do("PUT", "/api/v1/destination", `{"url":"srt://eu1.relay.enhancedirl.com:5000?streamid=publish:NEWKEY0123456789abcd","passphrase":"secretpassphrase","apply":true}`, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"restarted":true`) || strings.Contains(w.Body.String(), "NEWKEY0123456789abcd") {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	env, _ := config.ReadEnv(filepath.Join(e.dir, "app.env"))
	if env["SRT_STREAMID"] != "publish:NEWKEY0123456789abcd" || env["SRT_PASSPHRASE"] != "secretpassphrase" || env["SRT_URL"] != "srt://eu1.relay.enhancedirl.com:5000" {
		t.Errorf("file: %v", env)
	}
	if got := strings.Join(e.unit.actions, ","); got != "restart" {
		t.Errorf("actions: %q", got)
	}
	// rehearsal mode without apply: no restart
	if w := e.do("PUT", "/api/v1/output", `{"output":"off"}`, tok); w.Code != 200 || !strings.Contains(w.Body.String(), `"output":"off"`) {
		t.Errorf("output: %d %s", w.Code, w.Body)
	}
	if len(e.unit.actions) != 1 {
		t.Errorf("without apply there is no restart: %v", e.unit.actions)
	}
	// deliberately stopped: apply does not start it; crashed (failed): it does
	e.unit.state = "inactive"
	e.do("PUT", "/api/v1/output", `{"output":"on","apply":true}`, tok)
	e.unit.state = "failed"
	e.do("PUT", "/api/v1/output", `{"output":"on","apply":true}`, tok)
	if got := strings.Join(e.unit.actions, ","); got != "restart,restart" {
		t.Errorf("inactive must not restart and failed must: %q", got)
	}
	// the cloud's destination active (managed by EnhancedIRL) → 409; the file's EIRL_MANAGED_BY does not decide it
	e.setManaged(t, true)
	if w := e.do("PUT", "/api/v1/destination", `{"latencyMs":300}`, tok); w.Code != 409 {
		t.Errorf("managed: %d", w.Code)
	}
}

func TestInputEDID(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	for _, bad := range []string{`{"edidPreferred":"1080p60"}`, `{"edidPreferred":"../x"}`, `{}`} {
		if w := e.do("PUT", "/api/v1/input", bad, tok); w.Code != 400 || len(e.unit.actions) != 0 {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	w := e.do("PUT", "/api/v1/input", `{"edidPreferred":"1080p50","apply":true}`, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"edidPreferred":"1080p50"`) || !strings.Contains(w.Body.String(), `"restarted":true`) {
		t.Fatalf("PUT input: %d %s", w.Code, w.Body)
	}
	if env, _ := config.ReadEnv(filepath.Join(e.dir, "app.env")); env["EDID_PREFERRED"] != "1080p50" {
		t.Errorf("file: %v", env)
	}
	// the active one is what the script left in the state; the saved one, what is in the file
	os.WriteFile(filepath.Join(e.dir, "state"), []byte("OUTPUT=on\nSOURCE=hdmi\nCONF_SHA=x\nEDID=1080p30\n"), 0o644)
	if b := e.do("GET", "/api/v1/status", "", tok).Body.String(); !strings.Contains(b, `"input":{"edidPreferred":"1080p50","edidActive":"1080p30","source":"hdmi"`) {
		t.Errorf("status: %s", b)
	}
	// it belongs to the device: it can change even when EnhancedIRL manages the destination
	os.WriteFile(filepath.Join(e.dir, "app.env"), []byte("EIRL_MANAGED_BY=eirl\nOUTPUT=off\n"), 0o600)
	if w := e.do("PUT", "/api/v1/input", `{"edidPreferred":"720p60"}`, tok); w.Code != 200 {
		t.Errorf("managed: %d %s", w.Code, w.Body)
	}
}

func TestLogsRedacted(t *testing.T) {
	e := setup(t, true)
	w := e.do("GET", "/api/v1/logs", "", e.login(t))
	if w.Code != 200 || strings.Contains(w.Body.String(), key) {
		t.Errorf("logs: %d %s", w.Code, w.Body)
	}
}

func TestWebIsPublicButAPIIsNot(t *testing.T) {
	e := setup(t, true)
	e.srv.Web = fstest.MapFS{"index.html": {Data: []byte("<!doctype html>hola")}}
	e.h = e.srv.Handler()
	if w := e.do("GET", "/", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "hola") || w.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("web: %d %q %v", w.Code, w.Body, w.Header())
	}
	if w := e.do("GET", "/api/v1/destination", "", ""); w.Code != 401 {
		t.Errorf("the API still requires a session: %d", w.Code)
	}
}

type fakeLive struct {
	offer string
	alive func() bool
	err   error
}

func (f *fakeLive) Answer(_, offer string, alive func() bool) (string, error) {
	f.offer, f.alive = offer, alive
	return "v=0 respuesta", f.err
}

// WebRTC signaling: with a session, JSON and same origin; the session is checked again later
// (logout cuts the preview).
func TestLiveSignaling(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	if w := e.do("POST", "/api/v1/live", `{"sdp":"v=0"}`, tok); w.Code != 503 {
		t.Errorf("no live preview: %d", w.Code)
	}
	f := &fakeLive{}
	e.srv.Live = f
	if w := e.do("POST", "/api/v1/live", `{"sdp":"v=0"}`, ""); w.Code != 401 {
		t.Errorf("no session: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/live", `{"sdp":"v=0"}`, tok, "Origin", "http://evil.example"); w.Code != 403 {
		t.Errorf("other origin: %d", w.Code)
	}
	w := e.do("POST", "/api/v1/live", `{"sdp":"v=0 oferta"}`, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "v=0 respuesta") || f.offer != "v=0 oferta" {
		t.Fatalf("offer: %d %s", w.Code, w.Body)
	}
	if !f.alive() {
		t.Error("the session is valid")
	}
	e.do("POST", "/api/v1/logout", "", tok)
	if f.alive() {
		t.Error("after logout the preview must be cut")
	}
	f.err = live.ErrBusy
	tok = e.login(t)
	if w := e.do("POST", "/api/v1/live", `{"sdp":"v=0"}`, tok); w.Code != 429 {
		t.Errorf("busy: %d", w.Code)
	}
}

func TestUplinksAndBitrate(t *testing.T) {
	e := setup(t, true)
	e.srv.ABRPath = filepath.Join(e.dir, "abr")
	e.srv.Uplinks = &uplinks.Reader{
		Available: filepath.Join(e.dir, "available"), InUse: filepath.Join(e.dir, "inuse"),
		Disabled: filepath.Join(e.dir, "uplinks-disabled"), SysNet: filepath.Join(e.dir, "net"),
	}
	os.WriteFile(e.srv.Uplinks.Available, []byte("eth0 192.168.1.232\nenx1 192.168.8.100\n"), 0o644)
	os.WriteFile(e.srv.Uplinks.InUse, []byte("eth0 192.168.1.232\nenx1 192.168.8.100\n"), 0o644)
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=on\nSOURCE=hdmi\nCONF_SHA=x\nTRANSPORT=srtla\nABR=1\n"), 0o644)
	os.WriteFile(e.srv.ABRPath, []byte("KBPS=3200\nMIN=1000\nMAX=6000\n"), 0o644)
	tok := e.login(t)

	w := e.do("GET", "/api/v1/status", "", tok)
	for _, want := range []string{`"transport":"srtla"`, `"name":"enx1"`, `"inUse":true`, `"bitrate":{"kbps":3200,"minKbps":1000,"maxKbps":6000}`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("status without %s: %s", want, w.Body)
		}
	}
	origin := []string{"Origin", "http://example.com", "Host", "example.com"}
	if w := e.do("PUT", "/api/v1/uplinks/enx1", `{"disabled":true}`, tok, "Origin", "http://evil.example"); w.Code != 403 {
		t.Errorf("foreign origin (CSRF): %d", w.Code)
	}
	for path, body := range map[string]string{"a%20b": `{"disabled":true}`, "..": `{"disabled":true}`, "enx1": `{"disabled":"sí"}`, "enx2": `{}`} {
		// ServeMux normalizes ".." with a 307 before reaching the handler
		if w := e.do("PUT", "/api/v1/uplinks/"+path, body, tok, origin...); w.Code != 400 && w.Code != 404 && w.Code != 307 {
			t.Errorf("%s %s: %d", path, body, w.Code)
		}
	}
	// A disabled link that is gone (phone unplugged) stays disabled when another one is changed
	os.WriteFile(e.srv.Uplinks.Disabled, []byte("enxmovil\n"), 0o640)
	for i := 0; i < 2; i++ { // idempotent
		if w := e.do("PUT", "/api/v1/uplinks/enx1", `{"disabled":true}`, tok, origin...); w.Code != 200 {
			t.Fatalf("PUT: %d %s", w.Code, w.Body)
		}
	}
	if b, _ := os.ReadFile(e.srv.Uplinks.Disabled); !strings.HasSuffix(string(b), "\nenxmovil\nenx1\n") {
		t.Errorf("file: %q", b)
	}
	if w := e.do("PUT", "/api/v1/uplinks/enxmovil", `{"disabled":false}`, tok, origin...); w.Code != 200 {
		t.Fatalf("PUT: %d", w.Code)
	}
	w = e.do("GET", "/api/v1/status", "", tok)
	if !strings.Contains(w.Body.String(), `"name":"enx1","ip":"192.168.8.100","kind":"ethernet","inUse":true,"disabled":true`) {
		t.Errorf("status after disabling: %s", w.Body)
	}
	if b, _ := os.ReadFile(e.srv.Uplinks.Disabled); strings.Contains(string(b), "enxmovil") {
		t.Errorf("re-enable: %q", b)
	}
	// Without adaptive bitrate running there is no bitrate; with plain SRT the links are not "in use"
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=on\nSOURCE=hdmi\nCONF_SHA=x\nTRANSPORT=srt\nABR=0\n"), 0o644)
	w = e.do("GET", "/api/v1/status", "", tok)
	if strings.Contains(w.Body.String(), `"bitrate"`) || strings.Contains(w.Body.String(), `"inUse":true`) {
		t.Errorf("status with SRT: %s", w.Body)
	}
}

// With the mixer the output is switched on hot and the startup OUTPUT stays off: links in use and the bitrate
// still show (before, everything was “unused” and there was no bitrate while streaming)
func TestUplinksEnCaliente(t *testing.T) {
	e := setup(t, true)
	e.srv.ABRPath = filepath.Join(e.dir, "abr")
	e.srv.Uplinks = &uplinks.Reader{
		Available: filepath.Join(e.dir, "available"), InUse: filepath.Join(e.dir, "inuse"),
		Disabled: filepath.Join(e.dir, "uplinks-disabled"), SysNet: filepath.Join(e.dir, "net"),
	}
	os.WriteFile(e.srv.Uplinks.Available, []byte("eth0 192.168.1.232\nwlan0 172.20.10.2\n"), 0o644)
	os.WriteFile(e.srv.Uplinks.InUse, []byte("eth0 192.168.1.232\nwlan0 172.20.10.2\n"), 0o644)
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=off\nSOURCE=hdmi\nINPUT=ready\nMIXER=1\nTRANSPORT=srtla\nABR=1\n"), 0o644)
	os.WriteFile(e.srv.ABRPath, []byte("KBPS=6000\nMIN=1000\nMAX=6000\n"), 0o644)
	run := t.TempDir()
	cl, _ := fakeMixer(t, run)
	e.srv.Mixer = cl
	os.WriteFile(cl.StatePath, []byte(strings.Replace(mixerState, `"touch":true}}`, `"touch":true},"output":{"mode":"on","state":"on"}}`, 1)), 0o644)
	tok := e.login(t)
	body := e.do("GET", "/api/v1/status", "", tok).Body.String()
	for _, want := range []string{`"output":"on"`, `"name":"wlan0","ip":"172.20.10.2","kind":"`, `"bitrate":{"kbps":6000`} {
		if !strings.Contains(body, want) {
			t.Errorf("status without %s: %s", want, body)
		}
	}
	if strings.Count(body, `"inUse":true`) != 2 {
		t.Errorf("streaming hot, both links in use: %s", body)
	}
	// Hot stop: none in use
	os.WriteFile(cl.StatePath, []byte(strings.Replace(mixerState, `"touch":true}}`, `"touch":true},"output":{"mode":"off","state":"off"}}`, 1)), 0o644)
	if body := e.do("GET", "/api/v1/status", "", tok).Body.String(); strings.Contains(body, `"inUse":true`) || strings.Contains(body, `"bitrate":{`) {
		t.Errorf("not streaming, nothing in use and no bitrate: %s", body)
	}
}
