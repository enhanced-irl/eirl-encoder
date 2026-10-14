package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/links"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/modem"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

type stubModems struct{ list []modem.Modem }

func (s *stubModems) Name() string { return "mm" }
func (s *stubModems) List(context.Context, []modem.Link) ([]modem.Modem, error) {
	return s.list, nil
}

func fl(v float64) *float64 { return &v }

// With the links service, /status serves its snapshot: each uplink with its dot, the modem's uplink as kind
// modem, and the modems (also the one without a link); GET /modems the same list, without whole identifiers.
func TestStatusWithLinksAndModems(t *testing.T) {
	e := setup(t, true)
	e.srv.Uplinks = &uplinks.Reader{
		Available: filepath.Join(e.dir, "available"), InUse: filepath.Join(e.dir, "inuse"),
		Disabled: filepath.Join(e.dir, "uplinks-disabled"), SysNet: filepath.Join(e.dir, "net"),
	}
	os.WriteFile(e.srv.Uplinks.Available, []byte("eth0 192.168.1.232\nwwan0 10.64.12.7\n"), 0o644)
	os.WriteFile(e.srv.Uplinks.InUse, []byte("eth0 192.168.1.232\nwwan0 10.64.12.7\n"), 0o644)
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=on\nSOURCE=hdmi\nCONF_SHA=x\nTRANSPORT=srtla\n"), 0o644)
	mgr := &modem.Manager{Backends: []modem.Backend{&stubModems{list: []modem.Modem{
		{ID: "m-aaa", Backend: "mm", Iface: "wwan0", State: "connected", SIM: "ready", Registration: "home",
			Operator: "Vodafone", Tech: "4g", IMEITail: "5531", Signal: modem.Signal{RSRP: fl(-106), SINR: fl(3)}},
		{ID: "m-bbb", Backend: "mm", State: "locked", SIM: "pin"},
	}}}}
	mgr.Refresh(context.Background())
	e.srv.Links = &links.Service{Uplinks: e.srv.Uplinks, Modems: mgr, Streaming: e.srv.StreamingSRTLA}
	if !e.srv.StreamingSRTLA() {
		t.Fatal("OUTPUT=on over SRTLA is streaming")
	}
	e.srv.Links.Refresh(context.Background())
	tok := e.login(t)

	var st Status
	w := e.do("GET", "/api/v1/status", "", tok)
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	by := map[string]uplinks.Uplink{}
	for _, u := range st.Uplinks {
		by[u.Name] = u
	}
	if u := by["wwan0"]; u.Kind != "modem" || u.ModemID != "m-aaa" || u.Label != "Vodafone" || u.Health != "weak" || u.HealthCode != "signal_weak" || !u.InUse {
		t.Errorf("wwan0: %+v", u)
	}
	if u := by["eth0"]; u.Health != "good" || u.HealthCode != "ok" {
		t.Errorf("eth0: %+v", u)
	}
	if len(st.Modems) != 2 {
		t.Errorf("modems: %+v", st.Modems)
	}
	w = e.do("GET", "/api/v1/modems", "", tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"m-bbb"`) || !strings.Contains(w.Body.String(), `"sim":"pin"`) ||
		!strings.Contains(w.Body.String(), `"imeiTail":"5531"`) {
		t.Errorf("GET /modems: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/v1/modems", "", ""); w.Code != 401 {
		t.Errorf("GET /modems without a session: %d", w.Code)
	}
	// To the cloud, a modem's uplink is still "usb" until its contract has modems (M6)
	cs := CloudStatus(st, time.Now())
	for _, u := range cs.Uplinks {
		if u.Name == "wwan0" && u.Kind != "usb" {
			t.Errorf("cloud kind: %+v", u)
		}
	}
}

func TestModemsWithoutTheService(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	if w := e.do("GET", "/api/v1/modems", "", tok); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"modems":[]}` {
		t.Errorf("GET /modems with no service: %d %s", w.Code, w.Body)
	}
	if e.srv.StreamingSRTLA() {
		t.Error("nothing running is not streaming")
	}
}

type ctlModems struct {
	stubModems
	applied []modem.Config
	pins    []string
	resets  int
	pinErr  error
}

func (c *ctlModems) Apply(_ context.Context, _ modem.Modem, cfg modem.Config) error {
	c.applied = append(c.applied, cfg)
	return nil
}
func (c *ctlModems) Unlock(_ context.Context, _ modem.Modem, _ modem.Config, pin string, _ bool) error {
	c.pins = append(c.pins, pin)
	return c.pinErr
}
func (c *ctlModems) Reset(context.Context, modem.Modem) error { c.resets++; return nil }

func TestModemOrders(t *testing.T) {
	e := setup(t, true)
	var logBuf strings.Builder
	e.srv.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	e.srv.Uplinks = &uplinks.Reader{Available: filepath.Join(e.dir, "available"), InUse: filepath.Join(e.dir, "inuse"),
		Disabled: filepath.Join(e.dir, "uplinks-disabled"), SysNet: filepath.Join(e.dir, "net")}
	os.WriteFile(e.srv.Uplinks.Available, []byte("wwan0 10.64.12.7\n"), 0o644)
	os.WriteFile(e.srv.Uplinks.InUse, []byte("wwan0 10.64.12.7\n"), 0o644)
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=on\nSOURCE=hdmi\nCONF_SHA=x\nTRANSPORT=srtla\n"), 0o644)
	b := &ctlModems{stubModems: stubModems{list: []modem.Modem{{ID: "m-0c7e44a19f02", Backend: "mm", Iface: "wwan0", State: "connected",
		SIM: "ready", Operator: "Vodafone", Mode: "auto", Modes: []string{"auto", "4g"}, Can: []string{"apn", "roaming", "mode", "pin", "reset"}}}}}
	mgr := &modem.Manager{Backends: []modem.Backend{b}, Store: &modem.Store{Path: filepath.Join(e.dir, "modems.json"),
		Write: func(p string, d []byte, m os.FileMode) error { return os.WriteFile(p, d, m) }}}
	mgr.Refresh(context.Background())
	e.srv.Links = &links.Service{Uplinks: e.srv.Uplinks, Modems: mgr, Streaming: e.srv.StreamingSRTLA}
	e.srv.Links.Refresh(context.Background())
	tok := e.login(t)
	origin := []string{"Origin", "http://example.com", "Host", "example.com"}
	put := func(body string) *httptest.ResponseRecorder {
		return e.do("PUT", "/api/v1/modems/m-0c7e44a19f02", body, tok, origin...)
	}
	// An alias does not reconnect: allowed while it is the only link on air
	if w := put(`{"alias":"Voda"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"alias":"Voda"`) {
		t.Fatalf("alias: %d %s", w.Code, w.Body)
	}
	// An APN would reconnect the only link carrying the stream: 409, then with force
	if w := put(`{"apn":"ac.vodafone.es"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "last_link") {
		t.Fatalf("last link: %d %s", w.Code, w.Body)
	}
	if w := put(`{"apn":"ac.vodafone.es","mode":"4g","force":true}`); w.Code != 200 || len(b.applied) != 1 || b.applied[0].APN != "ac.vodafone.es" {
		t.Fatalf("force: %d %s %+v", w.Code, w.Body, b.applied)
	}
	for body, field := range map[string]string{`{"alias":"Un nombre demasiado largo"}`: "alias", `{"apn":"bad apn"}`: "apn",
		`{"mode":"6g"}`: "mode"} {
		if w := put(body); w.Code != 400 || !strings.Contains(w.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := put(`{}`); w.Code != 400 {
		t.Errorf("empty: %d", w.Code)
	}
	if w := e.do("PUT", "/api/v1/modems/m-000000000000", `{"alias":"x"}`, tok, origin...); w.Code != 404 {
		t.Errorf("unknown: %d", w.Code)
	}
	// The PIN: never in the log; a wrong one is 400 pin_wrong
	if w := e.do("POST", "/api/v1/modems/m-0c7e44a19f02/pin", `{"pin":"4711"}`, tok, origin...); w.Code != 204 || len(b.pins) != 1 {
		t.Fatalf("pin: %d %s", w.Code, w.Body)
	}
	b.pinErr = modem.ErrWrongPIN
	if w := e.do("POST", "/api/v1/modems/m-0c7e44a19f02/pin", `{"pin":"0815"}`, tok, origin...); w.Code != 400 || !strings.Contains(w.Body.String(), "pin_wrong") {
		t.Fatalf("wrong pin: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/modems/m-0c7e44a19f02/pin", `{"pin":"12"}`, tok, origin...); w.Code != 400 {
		t.Errorf("short pin: %d", w.Code)
	}
	b.pinErr = modem.ErrNotLocked
	if w := e.do("POST", "/api/v1/modems/m-0c7e44a19f02/pin", `{"pin":"0815"}`, tok, origin...); w.Code != 409 || !strings.Contains(w.Body.String(), "pin_not_needed") {
		t.Fatalf("not locked: %d %s", w.Code, w.Body)
	}
	b.pinErr = nil
	if strings.Contains(logBuf.String(), "4711") || strings.Contains(logBuf.String(), "0815") {
		t.Fatalf("a PIN in the log: %s", logBuf.String())
	}
	// Reset: the only link on air needs force; subnet: not on a ModemManager modem
	if w := e.do("POST", "/api/v1/modems/m-0c7e44a19f02/reset", ``, tok, origin...); w.Code != 409 {
		t.Errorf("reset last link: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/modems/m-0c7e44a19f02/reset", `{"force":true}`, tok, origin...); w.Code != 202 || b.resets != 1 {
		t.Errorf("reset: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/modems/m-0c7e44a19f02/subnet", ``, tok, origin...); w.Code != 409 || !strings.Contains(w.Body.String(), "not_supported") {
		t.Errorf("subnet: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/modems/m-0c7e44a19f02/explode", ``, tok, origin...); w.Code != 404 {
		t.Errorf("unknown action: %d", w.Code)
	}
	// The screen's socket reaches them
	for _, route := range []string{"GET /api/v1/modems", "PUT /api/v1/modems/{id}", "POST /api/v1/modems/{id}/{action}"} {
		found := false
		for _, r := range localRoutes {
			found = found || r == route
		}
		if !found {
			t.Errorf("not on the socket: %s", route)
		}
	}
}
