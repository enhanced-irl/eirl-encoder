package api

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
)

func sessionCookie(w interface{ Result() *http.Response }) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName && c.MaxAge > 0 {
			return c.Value
		}
	}
	return ""
}

// The rescue credential (MANAGER.md §6): the setup code sets a new web password without the current one, closes
// every session and opens one for whoever used it; the screen does it without the code.
func TestPasswordResetWithSetupCode(t *testing.T) {
	e := setup(t, true)
	withDevice(t, e)
	old := e.login(t)
	reset := func(body string, hdr ...string) (int, string, string) {
		w := e.do("POST", "/api/v1/password/reset", body, "", hdr...)
		return w.Code, w.Body.String(), sessionCookie(w)
	}
	// No session needed; CSRF and JSON still are
	if c, _, _ := reset(`{"code":"ABCD2345","password":"rescued-pass-1"}`, "Origin", "http://evil.example"); c != 403 {
		t.Errorf("CSRF: %d", c)
	}
	if c, b, _ := reset(`{"code":"ABCD2345","password":"short"}`); c != 400 || !strings.Contains(b, `"password"`) {
		t.Errorf("short password: %d %s", c, b)
	}
	if c, b, _ := reset(`{"password":"rescued-pass-1"}`); c != 400 || !strings.Contains(b, `"code"`) {
		t.Errorf("no code: %d %s", c, b)
	}
	if c, b, _ := reset(`{"code":"ABCD2346","password":"rescued-pass-1"}`); c != 403 || !strings.Contains(b, `"bad_code"`) {
		t.Errorf("wrong code: %d %s", c, b)
	}
	if c, _, _ := reset(`{"code":"ABCD2345","password":"rescued-pass-1","x":1}`); c != 400 {
		t.Errorf("unknown field: %d", c)
	}
	if !e.srv.Auth.Valid(old, time.Now()) {
		t.Fatal("a failed reset closed the session")
	}
	// As a person types it from the screen: lowercase, a dash, spaces
	c, _, tok := reset(`{"code":" abcd-2345 ","password":"rescued-pass-1"}`)
	if c != 204 || tok == "" {
		t.Fatalf("reset: %d, cookie %q", c, tok)
	}
	if e.srv.Auth.Valid(old, time.Now()) || !e.srv.Auth.Valid(tok, time.Now()) {
		t.Error("the old session must be closed and the new one open")
	}
	if w := e.do("POST", "/api/v1/login", `{"password":"correcto-123"}`, ""); w.Code != 401 {
		t.Errorf("the old password still works: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/login", `{"password":"rescued-pass-1"}`, ""); w.Code != 204 {
		t.Errorf("the new password: %d", w.Code)
	}
	// Attempts: 5 a minute per IP, shared with the wizard's first password (the same secret)
	for i := 0; i < 5; i++ {
		reset(`{"code":"ZZZZ2345","password":"rescued-pass-2"}`, "RemoteAddr", "192.168.1.70:1")
	}
	if c, _, _ := reset(`{"code":"ABCD2345","password":"rescued-pass-2"}`, "RemoteAddr", "192.168.1.70:1"); c != 429 {
		t.Errorf("after 5 wrong codes: %d", c)
	}
	if w := e.do("POST", "/api/v1/setup/password", `{"password":"rescued-pass-2","code":"ABCD2345"}`, "", "RemoteAddr", "192.168.1.70:1"); w.Code != 409 {
		t.Errorf("setup password: %d", w.Code) // 409 before the code: there is a password
	}
}

func TestPasswordResetNeedsAPassword(t *testing.T) {
	e := setup(t, false)
	withDevice(t, e)
	if w := e.do("POST", "/api/v1/password/reset", `{"code":"ABCD2345","password":"rescued-pass-1"}`, ""); w.Code != 503 {
		t.Errorf("without a password: %d %s", w.Code, w.Body)
	}
	e = setup(t, true)
	e.h = e.srv.Handler() // no setup code
	if w := e.do("POST", "/api/v1/password/reset", `{"code":"ABCD2345","password":"rescued-pass-1"}`, ""); w.Code != 503 ||
		!strings.Contains(w.Body.String(), "setup_code_unavailable") {
		t.Errorf("without a setup code: %d %s", w.Code, w.Body)
	}
}

// The screen: the code on demand (only over the socket) and a reset without it.
func TestScreenSetupCodeAndReset(t *testing.T) {
	e := setup(t, true)
	withDevice(t, e)
	tok := e.login(t)
	call := localClient(t, e.srv)
	if c, b := call("GET", "/api/v1/setup/code", ""); c != 200 || b != "{\"code\":\"ABCD2345\"}\n" {
		t.Errorf("socket: %d %q", c, b)
	}
	if w := e.do("GET", "/api/v1/setup/code", "", tok); w.Code != 404 || strings.Contains(w.Body.String(), "ABCD2345") {
		t.Errorf("web: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/v1/setup", "", tok); strings.Contains(w.Body.String(), "ABCD2345") {
		t.Errorf("GET /setup on the web carries the code: %s", w.Body)
	}
	if c, b := call("POST", "/api/v1/password/reset", `{"password":"short"}`); c != 400 {
		t.Errorf("socket, short: %d %s", c, b)
	}
	if c, b := call("POST", "/api/v1/password/reset", `{"password":"screen-pass-1"}`); c != 204 {
		t.Errorf("socket reset: %d %s", c, b)
	}
	if e.srv.Auth.Valid(tok, time.Now()) {
		t.Error("the web session survived the screen's reset")
	}
	if w := e.do("POST", "/api/v1/login", `{"password":"screen-pass-1"}`, ""); w.Code != 204 {
		t.Errorf("login with the screen's password: %d", w.Code)
	}
	os.Remove(e.srv.SetupCodePath)
	if c, _ := call("GET", "/api/v1/setup/code", ""); c != 503 {
		t.Errorf("no code: %d", c)
	}
	// Unlinking from the screen needs no password
	fc := &fakeCloud{view: cloud.View{State: "linked"}}
	e.srv.Cloud = fc
	if c, b := call("DELETE", "/api/v1/cloud", ""); c != 200 || fc.unlinks != 1 {
		t.Errorf("screen unlink: %d %s", c, b)
	}
}

// The login's global limit (20 a minute) cannot lock the owner out: a client that signed in recently is not held
// back by it, and the setup code has its own budget (MANAGER.md §6).
func TestGlobalLimitsSpareTheOwner(t *testing.T) {
	e := setup(t, true)
	withDevice(t, e)
	owner := "RemoteAddr"
	e.login(t)                // 192.168.1.50 is now known
	for i := 0; i < 20; i++ { // 4 addresses × 5 wrong passwords
		e.do("POST", "/api/v1/login", `{"password":"wrong-pass"}`, "", owner, fmt.Sprintf("192.168.1.%d:1", 100+i/5))
	}
	if w := e.do("POST", "/api/v1/login", `{"password":"correcto-123"}`, "", owner, "192.168.1.120:1"); w.Code != 429 {
		t.Errorf("a new address past the global limit: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/login", `{"password":"correcto-123"}`, ""); w.Code != 204 {
		t.Errorf("the owner's known address: %d", w.Code)
	}
	// The rescue from a new address is not blocked by the login's budget
	if w := e.do("POST", "/api/v1/password/reset", `{"code":"ABCD2345","password":"rescued-pass-1"}`, "", owner, "192.168.1.121:1"); w.Code != 204 {
		t.Errorf("rescue after the login's global limit: %d %s", w.Code, w.Body)
	}
	// Burning the code's own global budget: a new address waits, a known one does not
	for i := 0; i < 20; i++ {
		e.do("POST", "/api/v1/password/reset", `{"code":"ZZZZ2345","password":"rescued-pass-2"}`, "", owner, fmt.Sprintf("192.168.1.%d:1", 130+i/5))
	}
	if w := e.do("POST", "/api/v1/password/reset", `{"code":"ABCD2345","password":"rescued-pass-2"}`, "", owner, "192.168.1.150:1"); w.Code != 429 {
		t.Errorf("a new address past the code's global limit: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/password/reset", `{"code":"ABCD2345","password":"rescued-pass-2"}`, "", owner, "192.168.1.121:1"); w.Code != 204 {
		t.Errorf("the address that used the code: %d %s", w.Code, w.Body)
	}
}
