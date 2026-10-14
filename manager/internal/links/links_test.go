package links

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/modem"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

type backend struct{ list []modem.Modem }

func (b *backend) Name() string { return "mm" }
func (b *backend) List(context.Context, []modem.Link) ([]modem.Modem, error) {
	return b.list, nil
}

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

type env struct {
	dir     string
	reader  *uplinks.Reader
	written map[string][]byte
	writes  int
}

func newEnv(t *testing.T, available string, ifaces ...string) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{dir: dir, written: map[string][]byte{}}
	os.WriteFile(filepath.Join(dir, "available"), []byte(available), 0o644)
	for _, n := range ifaces {
		st := filepath.Join(dir, "sys", n, "statistics")
		os.MkdirAll(st, 0o755)
		switch {
		case strings.HasPrefix(n, "wlan"): // what sysfs has for a wireless interface
			os.MkdirAll(filepath.Join(dir, "sys", n, "wireless"), 0o755)
		case strings.HasPrefix(n, "enx"), strings.HasPrefix(n, "wwan"): // behind USB
			dev := filepath.Join(dir, "devices", "usb1", "1-2", n)
			os.MkdirAll(dev, 0o755)
			os.Symlink(dev, filepath.Join(dir, "sys", n, "device"))
		}
		os.WriteFile(filepath.Join(st, "tx_bytes"), []byte("0\n"), 0o644)
		os.WriteFile(filepath.Join(st, "rx_bytes"), []byte("0\n"), 0o644)
	}
	e.reader = &uplinks.Reader{Available: filepath.Join(dir, "available"), InUse: filepath.Join(dir, "inuse"),
		Disabled: filepath.Join(dir, "disabled"), SysNet: filepath.Join(dir, "sys")}
	return e
}

func (e *env) write(p string, b []byte, _ os.FileMode) error {
	e.written[p] = append([]byte(nil), b...)
	e.writes++
	return nil
}

func TestRefresh(t *testing.T) {
	e := newEnv(t, "eth0 192.168.1.232\nwwan0 10.64.12.7\nwlan0 172.20.10.4\nenx1 192.168.8.100\nenx2 192.168.8.100 conflict\n",
		"eth0", "wwan0", "wlan0", "enx1", "enx2")
	os.WriteFile(e.reader.InUse, []byte("eth0 192.168.1.232\nwwan0 10.64.12.7\n"), 0o644)
	mm := &backend{list: []modem.Modem{
		{ID: "m-aaa", Backend: "mm", Iface: "wwan0", State: "connected", SIM: "ready", Registration: "home", Operator: "Vodafone",
			Tech: "4g", Signal: modem.Signal{RSRP: f(-106), SINR: f(3)}},
		{ID: "m-bbb", Backend: "mm", State: "locked", SIM: "pin"}, // no interface: a SIM asking for its PIN
	}}
	mgr := &modem.Manager{Backends: []modem.Backend{mm}}
	mgr.Refresh(context.Background())
	now := time.Unix(1_000_000, 0)
	s := &Service{Uplinks: e.reader, Modems: mgr, Path: filepath.Join(e.dir, "links.json"), Write: e.write,
		Streaming: func() bool { return true }, Now: func() time.Time { return now },
		WiFi: func(context.Context) (string, *int) { return "wlan0", i(48) }}
	snap := s.Refresh(context.Background())

	got := map[string]Link{}
	var order []string
	for _, l := range snap.Links {
		got[l.Name] = l
		order = append(order, l.Name)
	}
	if strings.Join(order, ",") != "eth0,wlan0,m-bbb,wwan0,enx1,enx2" {
		t.Fatalf("order: %v", order)
	}
	check := func(name, health, code string) {
		t.Helper()
		if l := got[name]; l.Health != health || l.Code != code {
			t.Errorf("%s: %s/%s, want %s/%s", name, l.Health, l.Code, health, code)
		}
	}
	check("eth0", "good", "ok")
	check("wlan0", "weak", "signal_weak")
	check("wwan0", "weak", "signal_weak")
	check("enx1", "weak", "connecting") // streaming, enabled, not in use yet
	check("enx2", "bad", "ip_conflict")
	check("m-bbb", "bad", "sim_pin")
	if l := got["wwan0"]; l.Kind != "modem" || l.Label != "Vodafone" || l.Tech != "4g" || l.Bars == nil || *l.Bars != 2 || !l.InUse {
		t.Errorf("wwan0: %+v", l)
	}
	if l := got["eth0"]; l.Bars != nil {
		t.Errorf("a cable has no bars: %+v", l)
	}
	// /status: the uplink of the modem carries its kind, label and id
	for _, u := range snap.Uplinks {
		if u.Name == "wwan0" && (u.Kind != "modem" || u.ModemID != "m-aaa" || u.Health != "weak") {
			t.Errorf("uplink wwan0: %+v", u)
		}
	}
	if len(snap.Modems) != 2 {
		t.Errorf("modems: %+v", snap.Modems)
	}

	// links.json: no IPs, no identifiers beyond the id of a modem without interface
	var file File
	raw := e.written[s.Path]
	if err := json.Unmarshal(raw, &file); err != nil || file.V != 1 || !file.Streaming || len(file.Links) != 6 || file.At.IsZero() {
		t.Fatalf("links.json: %s (%v)", raw, err)
	}
	if strings.Contains(string(raw), "192.168.") || strings.Contains(string(raw), "10.64.") {
		t.Fatalf("links.json shows IPs: %s", raw)
	}

	// Not rewritten while nothing changes, rewritten after rewriteEvery
	n := e.writes
	now = now.Add(2 * time.Second)
	s.Refresh(context.Background())
	if e.writes != n {
		t.Fatalf("rewritten without changes")
	}
	now = now.Add(4 * time.Second)
	s.Refresh(context.Background())
	if e.writes != n+1 {
		t.Fatalf("not rewritten after %v", rewriteEvery)
	}
}

func TestDisabledModemWithoutLinkIsGrey(t *testing.T) {
	e := newEnv(t, "eth0 192.168.1.232\n", "eth0")
	os.WriteFile(e.reader.Disabled, []byte("wwan1\n"), 0o640)
	mgr := &modem.Manager{Backends: []modem.Backend{&backend{list: []modem.Modem{
		{ID: "m-ccc", Iface: "wwan1", State: "registered", SIM: "ready", Registration: "home"}}}}}
	mgr.Refresh(context.Background())
	s := &Service{Uplinks: e.reader, Modems: mgr}
	snap := s.Refresh(context.Background())
	for _, l := range snap.Links {
		if l.Name == "wwan1" && (l.Health != "off" || !l.Disabled) {
			t.Fatalf("switched-off modem: %+v", l)
		}
	}
}

func TestNoUplinksNoModems(t *testing.T) {
	s := &Service{}
	snap := s.Refresh(context.Background())
	if len(snap.Links) != 0 || snap.At.IsZero() {
		t.Fatalf("empty round: %+v", snap)
	}
}
