package api

import (
	"context"
	"encoding/json"
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
