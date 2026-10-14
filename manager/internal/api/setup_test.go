package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/device"
)

// withDevice: the env with a device store (fresh or migrated), an in-memory system, the zone list and a setup code.
func withDevice(t *testing.T, e *env) *device.MemSystem {
	t.Helper()
	sys := device.NewMemSystem("jetson")
	_, err := os.Stat(filepath.Join(e.dir, "admin.hash"))
	st, _, err := device.Open(filepath.Join(e.dir, "device.json"), err == nil, sys.Hostname(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Device, e.srv.System, e.srv.Zones = st, sys, &device.Zones{Sys: sys}
	e.srv.SetupCodePath = filepath.Join(e.dir, "setup-code")
	if err := os.WriteFile(e.srv.SetupCodePath, []byte("ABCD2345\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	e.srv.SlotsPath = filepath.Join(e.dir, "slots.json")
	vf := filepath.Join(e.dir, "VERSION")
	os.WriteFile(vf, []byte("0.1.0\n"), 0o644)
	ef := filepath.Join(e.dir, "ENGINE")
	os.WriteFile(ef, []byte("go-1c14b9b 2026-10-14 extra\n"), 0o644)
	e.srv.Versions = Versions{ReleaseFile: vf, EngineFile: ef}
	e.srv.Info.Version = "0.1.0"
	e.h = e.srv.Handler()
	return sys
}

// localClient: a client over a real local socket (LocalHandler).
func localClient(t *testing.T, srv *Server) func(method, path, body string, hdr ...string) (int, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "eirl") // short path: sun_path limit on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: srv.LocalHandler(), ConnContext: LocalConnContext}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })
	cl := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	return func(method, path, body string, hdr ...string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
}

func cookieOf(w *httptest.ResponseRecorder) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			return c.Value
		}
	}
	return ""
}

// A fresh device over the web: only the wizard and the language until the password (with the setup code) exists
func TestSetupWebFresh(t *testing.T) {
	e := setup(t, false)
	withDevice(t, e)
	// Nothing else is open
	for _, r := range [][2]string{{"GET", "/api/v1/status"}, {"GET", "/api/v1/destination"}, {"POST", "/api/v1/login"},
		{"GET", "/api/v1/sources/slots"}, {"POST", "/api/v1/setup/restart"}} {
		if w := e.do(r[0], r[1], "{}", ""); w.Code != 503 || !strings.Contains(w.Body.String(), `"setup_required"`) {
			t.Errorf("%s %s without a password: %d %s", r[0], r[1], w.Code, w.Body)
		}
	}
	if w := e.do("GET", "/api/v1/session", "", ""); w.Code != 401 || !strings.Contains(w.Body.String(), "setup_required") {
		t.Errorf("session: %d %s", w.Code, w.Body)
	}
	// The wizard state: no session needed; the code is required and never shown on the web
	w := e.do("GET", "/api/v1/setup", "", "")
	var sv setupView
	json.Unmarshal(w.Body.Bytes(), &sv)
	if w.Code != 200 || sv.Done || !sv.NeedsPassword || !sv.CodeRequired || sv.Language != "en" || len(sv.Steps) != 6 ||
		strings.Contains(w.Body.String(), "ABCD2345") {
		t.Fatalf("GET /setup: %d %s", w.Code, w.Body)
	}
	// The language yes; anything else no
	if w := e.do("PUT", "/api/v1/device", `{"language":"fr"}`, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"language":"fr"`) {
		t.Errorf("language before the password: %d %s", w.Code, w.Body)
	}
	if w := e.do("PUT", "/api/v1/device", `{"language":"es","timezone":"Europe/Madrid"}`, ""); w.Code != 503 || e.srv.Device.Language() != "fr" {
		t.Errorf("time zone before the password: %d %s", w.Code, w.Body)
	}
	// Without Accept-Language the device language (now fr); with it, the request's
	if w := e.do("GET", "/api/v1/status", "", ""); !strings.Contains(w.Body.String(), "pas encore configuré") {
		t.Errorf("device language: %s", w.Body)
	}
	if w := e.do("GET", "/api/v1/status", "", "", "Accept-Language", "es-ES,es;q=0.9"); !strings.Contains(w.Body.String(), "aún no está configurado") {
		t.Errorf("request language: %s", w.Body)
	}
	if w := e.do("GET", "/api/v1/device/timezones", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `{"id":"Europe/Madrid","offset":"+0`) {
		t.Errorf("timezones: %d %s", w.Code, w.Body)
	}
	// CSRF on the new mutations
	if w := e.do("POST", "/api/v1/setup/password", `{"password":"nueva-clave-1","code":"ABCD2345"}`, "", "Origin", "http://evil.example"); w.Code != 403 {
		t.Errorf("cross-origin first password: %d", w.Code)
	}
	if w := e.do("PUT", "/api/v1/device", `{"language":"en"}`, "", "Sec-Fetch-Site", "cross-site"); w.Code != 403 {
		t.Errorf("cross-site PUT /device: %d", w.Code)
	}
	// The first password: rules first (no attempt spent), then the code
	if w := e.do("POST", "/api/v1/setup/password", `{"password":"corta","code":"ABCD2345"}`, ""); w.Code != 400 || !strings.Contains(w.Body.String(), `"password"`) {
		t.Errorf("short password: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/setup/password", `{"password":"nueva-clave-1"}`, ""); w.Code != 400 || !strings.Contains(w.Body.String(), `"code"`) {
		t.Errorf("no code: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/setup/password", `{"password":"nueva-clave-1","code":"ABCD2346"}`, ""); w.Code != 403 || !strings.Contains(w.Body.String(), "bad_code") {
		t.Errorf("wrong code: %d %s", w.Code, w.Body)
	}
	if e.srv.Auth.Configured() {
		t.Fatal("a wrong code set the password")
	}
	// The right code as typed from a label (lowercase, a dash): a session (the login's cookie)
	w = e.do("POST", "/api/v1/setup/password", `{"password":"nueva-clave-1","code":"abcd-2345"}`, "")
	tok := cookieOf(w)
	if w.Code != 204 || tok == "" || !e.srv.Auth.Configured() {
		t.Fatalf("first password: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/v1/status", "", tok); w.Code != 200 {
		t.Errorf("session after the first password: %d", w.Code)
	}
	// Only once
	if w := e.do("POST", "/api/v1/setup/password", `{"password":"otra-clave-22","code":"ABCD2345"}`, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "password_exists") {
		t.Errorf("second first password: %d %s", w.Code, w.Body)
	}
	// Steps and completion need the session now
	if w := e.do("POST", "/api/v1/setup/step", `{"id":"time","state":"skipped"}`, ""); w.Code != 401 {
		t.Errorf("step without session: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/setup/step", `{"id":"password","state":"skipped"}`, tok); w.Code != 400 {
		t.Errorf("skipping the password: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/setup/step", `{"id":"bogus","state":"done"}`, tok); w.Code != 400 {
		t.Errorf("unknown step: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/setup/step", `{"id":"time","state":"skipped"}`, tok); w.Code != 200 || !strings.Contains(w.Body.String(), `{"id":"time","state":"skipped"}`) ||
		!strings.Contains(w.Body.String(), `{"id":"password","state":"done"}`) {
		t.Errorf("step: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/setup/complete", `{}`, tok); w.Code != 200 || !strings.Contains(w.Body.String(), `"done":true`) {
		t.Errorf("complete: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/setup/restart", `{}`, tok); w.Code != 200 || !strings.Contains(w.Body.String(), `"done":false`) || !e.srv.Auth.Configured() {
		t.Errorf("restart: %d %s", w.Code, w.Body)
	}
}

// Wrong codes are limited like the login: 5 per minute per IP
func TestSetupCodeRateLimit(t *testing.T) {
	e := setup(t, false)
	withDevice(t, e)
	for i := 0; i < 5; i++ {
		if w := e.do("POST", "/api/v1/setup/password", `{"password":"nueva-clave-1","code":"ZZZZZZZZ"}`, ""); w.Code != 403 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := e.do("POST", "/api/v1/setup/password", `{"password":"nueva-clave-1","code":"ABCD2345"}`, ""); w.Code != 429 {
		t.Errorf("6th attempt, even with the right code: %d", w.Code)
	}
	// Another IP still can
	if w := e.do("POST", "/api/v1/setup/password", `{"password":"nueva-clave-1","code":"ABCD2345"}`, "", "RemoteAddr", "192.168.1.60:1"); w.Code != 204 {
		t.Errorf("other IP: %d %s", w.Code, w.Body)
	}
}

// The screen (local socket): no code, no session; it sees the code while there is no password; everything of the
// device settings works before the password
func TestSetupLocalSocket(t *testing.T) {
	e := setup(t, false)
	sys := withDevice(t, e)
	do := localClient(t, e.srv)
	code, body := do("GET", "/api/v1/setup", "")
	if code != 200 || !strings.Contains(body, `"code":"ABCD2345"`) || !strings.Contains(body, `"codeRequired":false`) {
		t.Fatalf("GET /setup over the socket: %d %s", code, body)
	}
	code, body = do("PUT", "/api/v1/device", `{"timezone":"Europe/Madrid","ntp":false,"time":"2026-10-14T10:00:00+02:00","name":"Estudio 2"}`)
	if code != 200 || !strings.Contains(body, `"hostname":"estudio-2"`) || !strings.Contains(body, `"timezone":"Europe/Madrid"`) ||
		!strings.Contains(body, `"ntp":false`) || !strings.Contains(body, `"name":"Estudio 2"`) {
		t.Fatalf("PUT /device over the socket: %d %s", code, body)
	}
	if got := strings.Join(sys.Changes(), "|"); got != "hostname estudio-2|timezone Europe/Madrid|ntp false|time 2026-10-14T08:00:00Z" {
		t.Errorf("system changes: %s", got)
	}
	// A phone's typographic apostrophe in the name: accepted and stored as a plain '
	if code, body := do("PUT", "/api/v1/device", `{"name":"Plató d\u2019Ana"}`); code != 200 || !strings.Contains(body, `"name":"Plató d'Ana"`) ||
		e.srv.Device.Get().Name != "Plató d'Ana" || !strings.Contains(body, `"hostname":"plato-d-ana"`) {
		t.Errorf("typographic apostrophe in the name: %d %s", code, body)
	}
	// The web accepts the new hostname right away
	if w := e.do("GET", "/api/v1/setup", "", "", "Host", "plato-d-ana.local"); w.Code != 200 {
		t.Errorf("new hostname: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/setup", "", "", "Host", "jetson.local"); w.Code != 200 {
		t.Errorf("the hostname at start stays allowed (mDNS until a restart): %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/setup", "", "", "Host", "evil.example"); w.Code != 403 {
		t.Errorf("foreign host: %d", w.Code)
	}
	// Validation: manual time with NTP on, bad zone, bad name, bad language
	code, body = do("PUT", "/api/v1/device", `{"ntp":true,"time":"2026-10-14T10:00:00Z","timezone":"Mars/Base","name":"","language":"de"}`, "Accept-Language", "es")
	for _, f := range []string{`"time":"para poner la hora a mano`, `"timezone":"zona horaria desconocida"`, `"name":"de 1 a 40`, `"language":"uno de es, en, fr"`} {
		if code != 400 || !strings.Contains(body, f) {
			t.Errorf("validation: %d %s, want %s", code, body, f)
		}
	}
	// The first password with no code
	if code, body := do("POST", "/api/v1/setup/password", `{"password":"pantalla-123"}`); code != 204 || !e.srv.Auth.Configured() {
		t.Fatalf("first password over the socket: %d %s", code, body)
	}
	// The code is no longer shown
	if _, body := do("GET", "/api/v1/setup", ""); strings.Contains(body, "ABCD2345") {
		t.Errorf("code shown after the password: %s", body)
	}
	if code, _ := do("POST", "/api/v1/setup/complete", `{}`); code != 200 {
		t.Errorf("complete over the socket: %d", code)
	}
	// Not on the whitelist
	for _, r := range [][2]string{{"POST", "/api/v1/service/stop"}, {"GET", "/api/v1/logs"}} {
		if code, body := do(r[0], r[1], "{}"); code != 403 || !strings.Contains(body, "local_forbidden") {
			t.Errorf("%s %s over the socket: %d %s", r[0], r[1], code, body)
		}
	}
	// The wizard again from the screen: allowed, and the password stays
	if code, body := do("POST", "/api/v1/setup/restart", "{}"); code != 200 || !strings.Contains(body, `"done":false`) {
		t.Errorf("restart over the socket: %d %s", code, body)
	}
	// /update is on the whitelist: until the updater's routes exist, the route table answers 404 (not 403)
	if code, _ := do("GET", "/api/v1/update", ""); code != 404 && code != 503 && code != 200 {
		t.Errorf("GET /update over the socket: %d", code)
	}
}

// GET /device: versions, time, migrated board
func TestDeviceView(t *testing.T) {
	e := setup(t, true)
	withDevice(t, e)
	tok := e.login(t)
	w := e.do("GET", "/api/v1/device", "", tok)
	var v deviceView
	json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != 200 || v.Language != "es" || v.Name != "jetson" || v.Version.Release != "0.1.0" || v.Version.Engine != "go-1c14b9b" ||
		v.Version.App != "0.1.0" || v.Timezone != "UTC" || !v.Time.NTP || len(v.Languages) != 3 {
		t.Errorf("GET /device: %d %s", w.Code, w.Body)
	}
	if _, err := time.Parse(time.RFC3339, v.Time.Now); err != nil {
		t.Errorf("time.now: %v", err)
	}
	// A migrated board: setup done
	if w := e.do("GET", "/api/v1/setup", "", ""); !strings.Contains(w.Body.String(), `"done":true`) || !strings.Contains(w.Body.String(), `"needsPassword":false`) {
		t.Errorf("migrated: %s", w.Body)
	}
	// A failing system: 502 system_failed, device.json untouched
	sys := e.srv.System.(*device.MemSystem)
	sys.Fail = io.ErrUnexpectedEOF
	if w := e.do("PUT", "/api/v1/device", `{"name":"Otro nombre"}`, tok); w.Code != 502 || e.srv.Device.Get().Name != "jetson" {
		t.Errorf("hostname failure: %d %s", w.Code, w.Body)
	}
}

// Cloud command results are in the device language
func TestCloudActionLanguage(t *testing.T) {
	e := setup(t, true)
	withDevice(t, e) // migrated: Spanish
	d := &CloudDevice{Srv: e.srv}
	if err := d.Action(context.Background(), "bogus", ""); err == nil || !strings.Contains(err.Error(), "acción desconocida") {
		t.Errorf("es: %v", err)
	}
	e.srv.Device.Update(func(s *device.Settings) error { s.Language = "fr"; return nil })
	if err := d.Action(context.Background(), "set_local_password", "x"); err == nil || !strings.Contains(err.Error(), "au moins 10") {
		t.Errorf("fr: %v", err)
	}
}

// The local socket can start a link and see its code and QR, never more
func TestLocalCloudPairing(t *testing.T) {
	e := setup(t, true)
	fc := &fakeCloud{}
	e.srv.Cloud = fc
	do := localClient(t, e.srv)
	code, body := do("POST", "/api/v1/cloud/pair?qr=rows", `{}`)
	if code != 200 || !strings.Contains(body, `"code":"ABCD-2346"`) || !strings.Contains(body, `"linkQrRows":["`) {
		t.Errorf("pair over the socket: %d %s", code, body)
	}
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		if strings.Contains(sc.Text(), "token") || strings.Contains(sc.Text(), "pollSecret") {
			t.Errorf("secret in the view: %s", sc.Text())
		}
	}
}
