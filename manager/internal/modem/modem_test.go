package modem

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/linkhealth"
)

// --- ModemManager ----------------------------------------------------------------------------------------

type fakeBus struct {
	owner bool
	objs  map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	other map[dbus.ObjectPath]map[string]dbus.Variant // SIM and bearer objects
	calls []string
}

func (f *fakeBus) hasOwner(context.Context, string) (bool, error) { return f.owner, nil }
func (f *fakeBus) managedObjects(context.Context) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	return f.objs, nil
}
func (f *fakeBus) props(_ context.Context, p dbus.ObjectPath, _ string) (map[string]dbus.Variant, error) {
	m, ok := f.other[p]
	if !ok {
		return nil, errors.New("no such object")
	}
	return m, nil
}
func (f *fakeBus) call(_ context.Context, p dbus.ObjectPath, method string, _ ...any) error {
	f.calls = append(f.calls, string(p)+" "+method)
	return nil
}

func v(x any) dbus.Variant { return dbus.MakeVariant(x) }

func eg25(state int32, lock uint32, rate uint32) map[string]map[string]dbus.Variant {
	return map[string]map[string]dbus.Variant{
		mmModem: {
			"Manufacturer": v("Quectel"), "Model": v("EG25"), "EquipmentIdentifier": v("867698041235531"),
			"State": v(state), "StateFailedReason": v(uint32(0)), "UnlockRequired": v(lock),
			"AccessTechnologies": v(uint32(1 << 14)), "SignalQuality": v([]any{uint32(54), true}),
			"Sim":     v(dbus.ObjectPath("/org/freedesktop/ModemManager1/SIM/0")),
			"Ports":   v([][]any{{"cdc-wdm0", uint32(6)}, {"ttyUSB2", uint32(3)}, {"wwan0", uint32(2)}}),
			"Bearers": v([]dbus.ObjectPath{"/org/freedesktop/ModemManager1/Bearer/0"}),
		},
		mm3gpp: {"Imei": v("867698041235531"), "RegistrationState": v(uint32(1)), "OperatorName": v("Vodafone ES")},
		mmSignal: {"Rate": v(rate), "Lte": v(map[string]dbus.Variant{
			"rsrp": v(-106.04), "rsrq": v(-13.0), "snr": v(3.2), "rssi": v(-77.0)})},
	}
}

func TestMMList(t *testing.T) {
	bus := &fakeBus{owner: true,
		objs: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{"/org/freedesktop/ModemManager1/Modem/0": eg25(11, 1, 0)},
		other: map[dbus.ObjectPath]map[string]dbus.Variant{
			"/org/freedesktop/ModemManager1/SIM/0":    {"SimIdentifier": v("8934071100294821"), "OperatorName": v("Vodafone")},
			"/org/freedesktop/ModemManager1/Bearer/0": {"Interface": v("wwan0"), "Connected": v(true)},
		}}
	b := &MM{bus: bus}
	list, err := b.List(context.Background(), nil)
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v %v", list, err)
	}
	m := list[0]
	if m.Iface != "wwan0" || m.State != "connected" || m.SIM != "ready" || m.Registration != "home" || m.Tech != "4g" ||
		m.Operator != "Vodafone ES" || m.IMEITail != "5531" || m.ICCIDTail != "4821" || !strings.HasPrefix(m.ID, "m-") {
		t.Fatalf("modem: %+v", m)
	}
	if m.Signal.RSRP == nil || *m.Signal.RSRP != -106 || *m.Signal.SINR != 3.2 || *m.Signal.Quality != 54 {
		t.Fatalf("signal: %+v", m.Signal)
	}
	if strings.Contains(m.ID, "867698") {
		t.Fatalf("the id shows the IMEI: %s", m.ID)
	}
	// Signal.Setup asked once (Rate 0), not twice
	b.List(context.Background(), nil)
	if len(bus.calls) != 1 || !strings.HasSuffix(bus.calls[0], ".Modem.Signal.Setup") {
		t.Fatalf("Setup calls: %v", bus.calls)
	}
	if got := linkhealth.Judge(linkhealth.Input{Kind: "modem", Available: true, Modem: ptrModem(m.Health())}); got.Level != linkhealth.Weak {
		t.Fatalf("health of a −106 dBm / 3 dB modem: %+v", got)
	}
}

func ptrModem(m linkhealth.Modem) *linkhealth.Modem { return &m }

func TestMMLockedAndAbsent(t *testing.T) {
	objs := eg25(2, 2, 5) // locked, SIM PIN
	bus := &fakeBus{owner: true, objs: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{"/m/1": objs}}
	list, _ := (&MM{bus: bus}).List(context.Background(), nil)
	if len(list) != 1 || list[0].SIM != "pin" || list[0].State != "locked" || list[0].Signal.Quality != nil {
		t.Fatalf("locked: %+v", list)
	}
	if len(bus.calls) != 0 {
		t.Fatalf("Setup with a rate already set: %v", bus.calls)
	}
	// ModemManager not running: no modems, no error, and it is not started
	none, err := (&MM{bus: &fakeBus{owner: false}}).List(context.Background(), nil)
	if none != nil || err != nil {
		t.Fatalf("no ModemManager: %v %v", none, err)
	}
}

func TestMMMappings(t *testing.T) {
	for bits, want := range map[uint32]string{1 << 14: "4g", 1<<14 | 1<<15: "5g-nsa", 1 << 15: "5g", 1 << 9: "3g",
		1 << 4: "2g", 1<<4 | 1<<9: "3g", 0: ""} {
		if got := mmTech(bits); got != want {
			t.Errorf("mmTech(%#x) = %q, want %q", bits, got, want)
		}
	}
	for _, c := range []struct {
		state        int32
		reason, lock uint32
		sim          dbus.ObjectPath
		want         string
	}{
		{11, 0, 1, "/s/0", "ready"}, {2, 0, 2, "/s/0", "pin"}, {2, 0, 4, "/s/0", "puk"}, {-1, 2, 0, "/", "missing"},
		{-1, 3, 0, "/s/0", "error"}, {8, 0, 6, "/s/0", "error"}, {3, 0, 1, "/", "missing"}, {1, 0, 0, "/", "unknown"},
	} {
		if got := mmSIM(c.state, c.reason, c.lock, c.sim); got != c.want {
			t.Errorf("mmSIM(%d,%d,%d,%s) = %q, want %q", c.state, c.reason, c.lock, c.sim, got, c.want)
		}
	}
}

// --- HiLink ----------------------------------------------------------------------------------------------

func hilinkServer(t *testing.T, pages map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/api/webserver/SesTokInfo" && r.Header.Get("__RequestVerificationToken") != "tok123" {
			body = `<?xml version="1.0" encoding="UTF-8"?><error><code>125002</code><message></message></error>`
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sysnet: a fake /sys/class/net with one interface behind the given driver.
func sysnet(t *testing.T, iface, driver, mac string) string {
	t.Helper()
	dir := t.TempDir()
	drv := filepath.Join(dir, "drivers", driver)
	os.MkdirAll(drv, 0o755)
	dev := filepath.Join(dir, iface, "device")
	os.MkdirAll(dev, 0o755)
	if err := os.Symlink(drv, filepath.Join(dev, "driver")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, iface, "address"), []byte(mac+"\n"), 0o644)
	return dir
}

func routes(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "route")
	os.WriteFile(p, []byte("Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"+strings.Join(lines, "\n")+"\n"), 0o644)
	return p
}

const xmlHead = `<?xml version="1.0" encoding="UTF-8"?>`

func TestHiLink(t *testing.T) {
	srv := hilinkServer(t, map[string]string{
		"/api/webserver/SesTokInfo": xmlHead + `<response><SesInfo>SessionID=abc</SesInfo><TokInfo>tok123</TokInfo></response>`,
		"/api/monitoring/status": xmlHead + `<response><ConnectionStatus>901</ConnectionStatus><SignalIcon>3</SignalIcon>
			<CurrentNetworkType>19</CurrentNetworkType><CurrentNetworkTypeEx>1011</CurrentNetworkTypeEx><ServiceStatus>2</ServiceStatus>
			<RoamingStatus>0</RoamingStatus><SimStatus>1</SimStatus></response>`,
		"/api/device/signal":    xmlHead + `<response><rsrq>-11.0dB</rsrq><rsrp>-95dBm</rsrp><rssi>&gt;=-51dBm</rssi><sinr>8dB</sinr><mode>7</mode></response>`,
		"/api/net/current-plmn": xmlHead + `<response><State>0</State><FullName>Movistar</FullName><ShortName>Movistar</ShortName><Numeric>21407</Numeric></response>`,
		"/api/pin/status":       xmlHead + `<response><SimState>257</SimState><PinOptState>258</PinOptState></response>`,
	})
	h := &HiLink{SysNet: sysnet(t, "enx0c5b8f279a64", "cdc_ether", "0c:5b:8f:27:9a:64"),
		RouteFile: routes(t, "eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0",
			"enx0c5b8f279a64\t00000000\t0108A8C0\t0003\t0\t0\t101\t00000000\t0\t0\t0"),
		Client: func(string) *http.Client { return srv.Client() },
	}
	var gws []string
	h.baseURL = func(gw string) string { gws = append(gws, gw); return srv.URL }
	list, err := h.List(context.Background(), []Link{{Name: "enx0c5b8f279a64", IP: "192.168.8.100"}, {Name: "eth0", IP: "192.168.1.232"}})
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %+v %v", list, err)
	}
	if len(gws) != 1 || gws[0] != "192.168.8.1" {
		t.Fatalf("gateways asked: %v", gws)
	}
	m := list[0]
	if m.Backend != "hilink" || m.Iface != "enx0c5b8f279a64" || m.State != "connected" || m.SIM != "ready" || m.Registration != "home" ||
		m.Tech != "4g" || m.Operator != "Movistar" || !strings.HasPrefix(m.ID, "h-") {
		t.Fatalf("modem: %+v", m)
	}
	if *m.Signal.RSRP != -95 || *m.Signal.RSRQ != -11 || *m.Signal.SINR != 8 || *m.Signal.RSSI != -51 || *m.Signal.Bars != 3 {
		t.Fatalf("signal: %+v", m.Signal)
	}
}

func TestHiLinkNotAHiLinkIsSkippedForAMinute(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked++; http.NotFound(w, r) }))
	defer srv.Close()
	now := time.Unix(1_000_000, 0)
	h := &HiLink{SysNet: sysnet(t, "usb0", "rndis_host", "aa:bb:cc:dd:ee:ff"),
		RouteFile: routes(t, "usb0\t00000000\t012BA8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0"),
		Client:    func(string) *http.Client { return srv.Client() }, baseURL: func(string) string { return srv.URL },
		Now: func() time.Time { return now }}
	links := []Link{{Name: "usb0", IP: "192.168.43.2"}} // an Android phone
	for i := 0; i < 3; i++ {
		if l, _ := h.List(context.Background(), links); len(l) != 0 {
			t.Fatalf("an Android phone is not a HiLink: %+v", l)
		}
	}
	if asked != 1 {
		t.Fatalf("asked %d times within the minute", asked)
	}
	now = now.Add(61 * time.Second)
	h.List(context.Background(), links)
	if asked != 2 {
		t.Fatalf("not asked again after a minute: %d", asked)
	}
}

func TestHiLinkErrorAnswer(t *testing.T) {
	if _, err := flatXML(strings.NewReader(xmlHead + `<error><code>100003</code></error>`)); err == nil || err.Error() != "hilink error 100003" {
		t.Fatalf("error answer: %v", err)
	}
	if _, err := flatXML(strings.NewReader(`<html><body>router</body></html>`)); err == nil {
		t.Fatal("an HTML page is not a HiLink answer")
	}
}

func TestGateway(t *testing.T) {
	p := routes(t,
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0",
		"eth0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0",
		"wwan0\t00000000\t0108A8C0\t0003\t0\t0\t700\t00000000\t0\t0\t0",
		"wwan0\t00000000\t0109A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0")
	if g := gateway(p, "eth0"); g != "192.168.1.1" {
		t.Fatalf("eth0: %s", g)
	}
	if g := gateway(p, "wwan0"); g != "192.168.9.1" {
		t.Fatalf("lowest metric: %s", g)
	}
	if g := gateway(p, "wlan0"); g != "" {
		t.Fatalf("no route: %s", g)
	}
}

// --- Manager ---------------------------------------------------------------------------------------------

type stub struct {
	name string
	list []Modem
	err  error
}

func (s *stub) Name() string { return s.name }
func (s *stub) List(context.Context, []Link) ([]Modem, error) {
	return s.list, s.err
}

func TestManagerKeepsTheLastListOfAFailingBackend(t *testing.T) {
	mm := &stub{name: "mm", list: []Modem{{ID: "m-1", Backend: "mm", Iface: "wwan0"}}}
	hl := &stub{name: "hilink", list: []Modem{{ID: "h-1", Backend: "hilink", Iface: "enx1"}}}
	var logged []string
	m := &Manager{Backends: []Backend{mm, hl}, Log: func(msg string, _ ...any) { logged = append(logged, msg) }}
	m.Refresh(context.Background())
	if got := m.Snapshot(); len(got) != 2 || got[0].Iface != "enx1" || got[1].Iface != "wwan0" {
		t.Fatalf("first: %+v", got)
	}
	mm.err, mm.list = errors.New("timeout"), nil
	m.Refresh(context.Background())
	m.Refresh(context.Background())
	if got := m.Snapshot(); len(got) != 2 {
		t.Fatalf("a failing backend dropped its modems: %+v", got)
	}
	if len(logged) != 1 {
		t.Fatalf("the failure logged %d times", len(logged))
	}
	mm.err = nil
	m.Refresh(context.Background())
	if got := m.Snapshot(); len(got) != 1 || len(logged) != 2 {
		t.Fatalf("after recovery: %+v %v", got, logged)
	}
	if by := ByIface(m.Snapshot()); by["enx1"].ID != "h-1" {
		t.Fatalf("ByIface: %+v", by)
	}
}

func TestHiLinkKeepsItsLastReadingThenLetsGo(t *testing.T) {
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			http.Error(w, "busy", 503)
			return
		}
		switch r.URL.Path {
		case "/api/webserver/SesTokInfo":
			w.Write([]byte(xmlHead + `<response><SesInfo>SessionID=a</SesInfo><TokInfo>t</TokInfo></response>`))
		case "/api/monitoring/status":
			w.Write([]byte(xmlHead + `<response><ConnectionStatus>901</ConnectionStatus><CurrentNetworkType>19</CurrentNetworkType></response>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	now := time.Unix(1_000_000, 0)
	h := &HiLink{SysNet: sysnet(t, "enx1", "cdc_ether", "0c:5b:8f:00:00:01"),
		RouteFile: routes(t, "enx1\t00000000\t0108A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0"),
		Client:    func(string) *http.Client { return srv.Client() }, baseURL: func(string) string { return srv.URL },
		Now: func() time.Time { return now }}
	links := []Link{{Name: "enx1", IP: "192.168.8.100"}}
	if l, _ := h.List(context.Background(), links); len(l) != 1 || l[0].State != "connected" {
		t.Fatalf("answering: %+v", l)
	}
	up = false
	now = now.Add(20 * time.Second)
	if l, _ := h.List(context.Background(), links); len(l) != 1 || l[0].State != "connected" {
		t.Fatalf("quiet for 20 s: the last reading stays: %+v", l)
	}
	now = now.Add(15 * time.Second)
	if l, _ := h.List(context.Background(), links); len(l) != 0 {
		t.Fatalf("quiet for 35 s: let go: %+v", l)
	}
}

func TestHiLinkDeadlineIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte(xmlHead + `<response><SesInfo>SessionID=a</SesInfo></response>`))
	}))
	defer srv.Close()
	h := &HiLink{SysNet: sysnet(t, "enx1", "cdc_ether", "0c:5b:8f:00:00:01"),
		RouteFile: routes(t, "enx1\t00000000\t0108A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0"),
		Client:    func(string) *http.Client { return srv.Client() }, baseURL: func(string) string { return srv.URL }}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if l, err := h.List(ctx, []Link{{Name: "enx1", IP: "192.168.8.100"}}); err == nil {
		t.Fatalf("a deadline must be an error (the Manager keeps the previous list), got %+v", l)
	}
}

func TestIDKey(t *testing.T) {
	defer SetIDKey(nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "modem-id.key")
	read := func(p string) ([]byte, error) { return os.ReadFile(p) }
	write := func(p string, b []byte, m os.FileMode) error { return os.WriteFile(p, b, m) }
	k1, err := LoadIDKey(path, read, write)
	if err != nil || len(k1) != 32 {
		t.Fatalf("create: %v %d", err, len(k1))
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v", st.Mode())
	}
	k2, _ := LoadIDKey(path, read, write)
	if string(k1) != string(k2) {
		t.Fatal("the key changed on the second load")
	}
	SetIDKey(k1)
	a := stableID("m-", "867698041235531")
	if a != stableID("m-", "867698041235531") || len(a) != 14 {
		t.Fatalf("not stable: %s", a)
	}
	other := make([]byte, 32)
	SetIDKey(other)
	if stableID("m-", "867698041235531") == a {
		t.Fatal("another board's key gives the same id")
	}
}

func TestMMSetupRetriedEveryMinute(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	bus := &fakeBus{owner: true, objs: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{"/m/0": eg25(11, 1, 0)},
		other: map[dbus.ObjectPath]map[string]dbus.Variant{}}
	b := &MM{bus: bus, now: func() time.Time { return now }}
	for _, step := range []time.Duration{0, 30 * time.Second, 31 * time.Second} {
		now = now.Add(step)
		b.List(context.Background(), nil)
	}
	if len(bus.calls) != 2 {
		t.Fatalf("Setup calls at 0, 30 and 61 s: %v", bus.calls)
	}
	// ModemManager restarted: the modem is gone for a round, then back under the same path → asked at once
	bus.objs = nil
	b.List(context.Background(), nil)
	bus.objs = map[dbus.ObjectPath]map[string]map[string]dbus.Variant{"/m/0": eg25(11, 1, 0)}
	now = now.Add(time.Second)
	b.List(context.Background(), nil)
	if len(bus.calls) != 3 {
		t.Fatalf("after a restart: %v", bus.calls)
	}
}
