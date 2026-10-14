package modem

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
)

// --- the store and the automatic APN -----------------------------------------------------------------------------

func memStore(t *testing.T) *Store {
	p := filepath.Join(t.TempDir(), "modems.json")
	return &Store{Path: p, Write: func(path string, b []byte, mode os.FileMode) error { return os.WriteFile(path, b, mode) }}
}

func TestStore(t *testing.T) {
	s := memStore(t)
	if c := s.Get("m-000000000001"); c != (Config{}) {
		t.Fatalf("empty store: %+v", c)
	}
	if _, err := s.Update("m-000000000001", func(c *Config) { c.Alias, c.APN, c.Roaming = "Movistar 1", "telefonica.es", true }); err != nil {
		t.Fatal(err)
	}
	if c := s.Get("m-000000000001"); c.Alias != "Movistar 1" || c.APN != "telefonica.es" || !c.Roaming {
		t.Fatalf("stored: %+v", c)
	}
	for name, fn := range map[string]func(*Config){
		"alias too long": func(c *Config) { c.Alias = "Una red muy larga" },
		"alias control":  func(c *Config) { c.Alias = "a\nb" },
		"apn spaces":     func(c *Config) { c.APN = "bad apn" },
		"apn quote":      func(c *Config) { c.APN = `a"b` },
		"mode":           func(c *Config) { c.Mode = "6g" },
	} {
		if _, err := s.Update("m-000000000001", fn); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.Update("not-an-id", func(c *Config) {}); !errors.Is(err, ErrConfig) {
		t.Errorf("bad id: %v", err)
	}
	// Back to defaults: the entry goes
	s.Update("m-000000000001", func(c *Config) { *c = Config{} })
	if all, _ := s.All(); len(all) != 0 {
		t.Fatalf("defaults kept an entry: %+v", all)
	}
	if st, _ := os.Stat(s.Path); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode())
	}
}

const providersXML = `<?xml version="1.0"?>
<serviceproviders format="2.0">
 <country code="es">
  <provider><name>Movistar</name><gsm><network-id mcc="214" mnc="07"/>
   <apn value="telefonica.es"><usage type="internet"/></apn><apn value="mms.movistar.es"><usage type="mms"/></apn></gsm></provider>
  <provider><name>Vodafone</name><gsm><network-id mcc="214" mnc="01"/><apn value="mms.vodafone.net"><usage type="mms"/></apn>
   <apn value="airtelwap.es"><usage type="internet"/></apn></gsm></provider>
  <provider><name>Lowi</name><gsm><network-id mcc="214" mnc="01"/><apn value="lowi.private.omv.es"><usage type="internet"/></apn></gsm></provider>
  <provider><name>Movistar Empresas</name><gsm><network-id mcc="214" mnc="07"/><apn value="telefonica.es"/></gsm></provider>
 </country>
 <country code="us"><provider><name>T-Mobile</name><gsm><network-id mcc="310" mnc="260"/><apn value="fast.t-mobile.com"/></gsm></provider></country>
</serviceproviders>`

func TestAutoAPN(t *testing.T) {
	p := filepath.Join(t.TempDir(), "serviceproviders.xml")
	os.WriteFile(p, []byte(providersXML), 0o644)
	for _, c := range []struct{ code, sim, want string }{{"21407", "", "telefonica.es"}, {"214007", "", "telefonica.es"},
		{"310260", "", "fast.t-mobile.com"}, {"99999", "", ""}, {"", "", ""}, {"21", "", ""},
		// Two operators on 214 01 with different APNs: the SIM's name decides, else none
		{"21401", "", ""}, {"21401", "Vodafone ES", ""}, {"21401", "vodafone", "airtelwap.es"}, {"21401", " LOWI ", "lowi.private.omv.es"},
		// Two on 214 07 with the same APN: no doubt
		{"21407", "Tuenti", "telefonica.es"}} {
		if got := AutoAPN(p, c.code, c.sim); got != c.want {
			t.Errorf("AutoAPN(%q, %q) = %q, want %q", c.code, c.sim, got, c.want)
		}
	}
	if got := AutoAPN(filepath.Join(t.TempDir(), "none.xml"), "21407", ""); got != "" {
		t.Errorf("without the database: %q", got)
	}
}

// --- NetworkManager's gsm profiles ----------------------------------------------------------------------------------

type fakeNM struct {
	mu          sync.Mutex
	conns       map[dbus.ObjectPath]nmMap
	pins        map[dbus.ObjectPath]string
	adds, upds  int
	reactivated []string
	secretsErr  error
}

func newFakeNM() *fakeNM {
	return &fakeNM{conns: map[dbus.ObjectPath]nmMap{}, pins: map[dbus.ObjectPath]string{}}
}

func (f *fakeNM) connectionByUUID(_ context.Context, uuid string) (dbus.ObjectPath, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for p, s := range f.conns {
		if u, _ := s["connection"]["uuid"].Value().(string); u == uuid {
			return p, nil
		}
	}
	return "", nil
}

func (f *fakeNM) settings(_ context.Context, p dbus.ObjectPath) (nmMap, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := nmMap{}
	for k, v := range f.conns[p] {
		m := map[string]dbus.Variant{}
		for kk, vv := range v {
			if kk != "pin" { // GetSettings never returns secrets
				m[kk] = vv
			}
		}
		out[k] = m
	}
	return out, nil
}

func (f *fakeNM) secrets(_ context.Context, p dbus.ObjectPath, _ string) (nmMap, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.secretsErr != nil {
		return nil, f.secretsErr
	}
	return nmMap{"gsm": {"pin": dbus.MakeVariant(f.pins[p])}}, nil
}

func (f *fakeNM) store(p dbus.ObjectPath, s nmMap) {
	f.conns[p] = s
	pin, _ := s["gsm"]["pin"].Value().(string)
	f.pins[p] = pin
}

func (f *fakeNM) add(_ context.Context, s nmMap) (dbus.ObjectPath, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds++
	p := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/NetworkManager/Settings/%d", len(f.conns)+1))
	f.store(p, s)
	return p, nil
}

func (f *fakeNM) update(_ context.Context, p dbus.ObjectPath, s nmMap) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upds++
	f.store(p, s)
	return nil
}

func (f *fakeNM) reactivate(_ context.Context, p dbus.ObjectPath, mm string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactivated = append(f.reactivated, mm)
	return nil
}

func (f *fakeNM) only(t *testing.T) (nmMap, string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.conns) != 1 {
		t.Fatalf("%d profiles", len(f.conns))
	}
	for p, s := range f.conns {
		return s, f.pins[p]
	}
	return nil, ""
}

func TestEnsureProfile(t *testing.T) {
	nm := newFakeNM()
	ctx := context.Background()
	const sim1, sim2 = "8934071100294821", "8934011234567890"
	p := gsmProfile{ID: "m-0c7e44a19f02", DeviceID: "a1b2c3", SimID: sim1, APN: "airtelwap.es"}
	if ch, err := ensureProfile(ctx, nm, p, "/org/freedesktop/ModemManager1/Modem/0", false); err != nil || !ch {
		t.Fatalf("create: %v %v", ch, err)
	}
	s, pin := nm.only(t)
	if id, _ := s["connection"]["id"].Value().(string); id != "eirl-modem-m-0c7e44a19f02" {
		t.Errorf("id %q", id)
	}
	if typ, _ := s["connection"]["type"].Value().(string); typ != "gsm" {
		t.Errorf("type %q", typ)
	}
	if ho, _ := s["gsm"]["home-only"].Value().(bool); !ho {
		t.Error("roaming not off by default")
	}
	if dev, _ := s["gsm"]["device-id"].Value().(string); dev != "a1b2c3" {
		t.Errorf("device-id %q", dev)
	}
	if sim, _ := s["gsm"]["sim-id"].Value().(string); sim != sim1 {
		t.Errorf("sim-id %q", sim)
	}
	if v6, _ := s["ipv6"]["method"].Value().(string); v6 != "ignore" || pin != "" {
		t.Errorf("ipv6 %q pin %q", v6, pin)
	}
	// The same again: nothing written, nothing activated (unless asked)
	if ch, _ := ensureProfile(ctx, nm, p, "/m/0", false); ch || nm.upds != 0 || len(nm.reactivated) != 1 {
		t.Fatalf("unchanged profile rewritten: %v %d %v", ch, nm.upds, nm.reactivated)
	}
	if ensureProfile(ctx, nm, p, "/m/0", true); len(nm.reactivated) != 2 {
		t.Fatalf("not activated again: %v", nm.reactivated)
	}
	// A PIN, then another APN: the PIN stays (it is read from the secrets, never from the app)
	pp := p
	pp.PIN = "1234"
	ensureProfile(ctx, nm, pp, "", false)
	p.APN = "ac.vodafone.es"
	if ch, _ := ensureProfile(ctx, nm, p, "", false); !ch {
		t.Fatal("APN change not written")
	}
	s, pin = nm.only(t)
	if apn, _ := s["gsm"]["apn"].Value().(string); apn != "ac.vodafone.es" || pin != "1234" {
		t.Fatalf("apn %q pin %q", apn, pin)
	}
	// The secrets cannot be read: nothing is written (rewriting would lose the PIN)
	nm.secretsErr = dbus.Error{Name: "org.freedesktop.NetworkManager.Settings.Failed"}
	p.Roaming = true
	if _, err := ensureProfile(ctx, nm, p, "", false); err == nil {
		t.Fatal("a profile rewritten without its secrets")
	}
	if _, pin = nm.only(t); pin != "1234" {
		t.Fatalf("PIN lost: %q", pin)
	}
	nm.secretsErr = dbus.Error{Name: "org.freedesktop.NetworkManager.Settings.NoSecrets"} // none stored: fine
	if _, err := ensureProfile(ctx, nm, p, "", false); err != nil {
		t.Fatal(err)
	}
	nm.secretsErr = nil
	pp.Roaming = true
	ensureProfile(ctx, nm, pp, "", false)
	// Another SIM in the same modem: the old SIM's PIN is never offered to it
	p.SimID = sim2
	ensureProfile(ctx, nm, p, "", false)
	s, pin = nm.only(t)
	if sim, _ := s["gsm"]["sim-id"].Value().(string); sim != sim2 || pin != "" {
		t.Fatalf("sim-id %q pin %q", sim, pin)
	}
	// Without the SIM's identity no PIN is kept; DropPIN forgets it
	q := gsmProfile{ID: "m-0c7e44a19f02", DeviceID: "a1b2c3", PIN: "1234"}
	ensureProfile(ctx, nm, q, "", false)
	if _, pin = nm.only(t); pin != "" {
		t.Fatalf("a PIN kept for an unknown SIM: %q", pin)
	}
	p.PIN = "5678"
	ensureProfile(ctx, nm, p, "", false)
	p.PIN, p.DropPIN = "", true
	ensureProfile(ctx, nm, p, "", false)
	if _, pin = nm.only(t); pin != "" {
		t.Fatalf("PIN not forgotten: %q", pin)
	}
	// Automatic APN: no apn key at all
	p.APN, p.DropPIN = "", false
	ensureProfile(ctx, nm, p, "", false)
	s, _ = nm.only(t)
	if _, has := s["gsm"]["apn"]; has {
		t.Fatal("an automatic APN wrote one")
	}
	if profileUUID("m-0c7e44a19f02") != profileUUID("m-0c7e44a19f02") || profileUUID("m-0c7e44a19f02") == profileUUID("m-5a1d0c3e9b21") {
		t.Fatal("uuid not stable or not distinct")
	}
}

// --- ModemManager's orders ------------------------------------------------------------------------------------------

type callBus struct {
	*fakeBus
	errs map[string]error
	args map[string][]any
}

func (c *callBus) call(_ context.Context, p dbus.ObjectPath, method string, args ...any) error {
	c.calls = append(c.calls, string(p)+" "+method)
	if c.args == nil {
		c.args = map[string][]any{}
	}
	c.args[method] = args
	return c.errs[method]
}

func TestMMModes(t *testing.T) {
	combos := []combo{{mode2G | mode3G | mode4G, 0}, {mode2G | mode3G | mode4G, mode4G}, {mode4G, 0}, {mode3G | mode4G, mode4G}}
	now, modes := mmModeNow(dbus.MakeVariant([]any{uint32(mode4G), uint32(0)}), combos)
	if now != "4g" || strings.Join(modes, ",") != "auto,4g" {
		t.Fatalf("4G only: %s %v", now, modes)
	}
	if c := pickCombo(combos, "auto"); c == nil || *c != (combo{mode2G | mode3G | mode4G, 0}) {
		t.Fatalf("auto: %+v", c)
	}
	five := append(combos, combo{mode4G | mode5G, mode5G}, combo{mode3G | mode4G | mode5G, 0})
	_, modes = mmModeNow(dbus.MakeVariant([]any{uint32(mode4G | mode5G), uint32(mode5G)}), five)
	if strings.Join(modes, ",") != "auto,4g,5g" {
		t.Fatalf("with 5G: %v", modes)
	}
	if c := pickCombo(five, "5g"); c == nil || *c != (combo{mode4G | mode5G, mode5G}) {
		t.Fatalf("5g: %+v", c)
	}
	if c := pickCombo(five, "auto"); c == nil || c.Allowed != mode3G|mode4G|mode5G {
		t.Fatalf("auto with 5G: %+v", c)
	}
}

func TestMMOrders(t *testing.T) {
	ctx := context.Background()
	// A locked SIM may not say its ICCID: read once unlocked
	bus := &callBus{fakeBus: &fakeBus{owner: true, other: map[dbus.ObjectPath]map[string]dbus.Variant{
		"/org/freedesktop/ModemManager1/SIM/0": {"SimIdentifier": v("8934071100294821")}}}, errs: map[string]error{}}
	nm := newFakeNM()
	prov := filepath.Join(t.TempDir(), "p.xml")
	os.WriteFile(prov, []byte(providersXML), 0o644)
	b := &MM{bus: bus, nm: nm, Providers: prov}
	md := Modem{ID: "m-0c7e44a19f02", Backend: "mm", path: "/org/freedesktop/ModemManager1/Modem/0", simPath: "/org/freedesktop/ModemManager1/SIM/0",
		devID: "a1b2c3", netCode: "21401", simName: "Vodafone", SIM: "ready", Mode: "auto",
		combos: []combo{{mode2G | mode3G | mode4G, 0}, {mode4G, 0}}}
	// Automatic APN from the database (the SIM's operator among the two on 214 01); 4G only
	if err := b.Apply(ctx, md, Config{Mode: "4g"}); err != nil {
		t.Fatal(err)
	}
	s, _ := nm.only(t)
	if apn, _ := s["gsm"]["apn"].Value().(string); apn != "airtelwap.es" {
		t.Errorf("auto APN %q", apn)
	}
	if a := bus.args[mmModem+".SetCurrentModes"]; len(a) != 1 || a[0] != (combo{mode4G, 0}) {
		t.Errorf("SetCurrentModes %v", a)
	}
	// Back to automatic: the widest; a mode nobody chose ("") leaves the modem's
	md.Mode = "4g"
	delete(bus.args, mmModem+".SetCurrentModes")
	b.Apply(ctx, md, Config{})
	if _, set := bus.args[mmModem+".SetCurrentModes"]; set {
		t.Error("an unset mode changed the modem's")
	}
	b.Apply(ctx, md, Config{Mode: "auto"})
	if a := bus.args[mmModem+".SetCurrentModes"]; len(a) != 1 || a[0] != (combo{mode2G | mode3G | mode4G, 0}) {
		t.Errorf("auto: SetCurrentModes %v", a)
	}
	// A PIN for a SIM that does not ask for one: refused (it could not be checked)
	if err := b.Unlock(ctx, md, Config{}, "1234", false); !errors.Is(err, ErrNotLocked) {
		t.Errorf("ready SIM: %v", err)
	}
	// PIN: sent and kept in the profile, for that SIM
	md.SIM = "pin"
	if err := b.Unlock(ctx, md, Config{}, "1234", false); err != nil {
		t.Fatal(err)
	}
	if a := bus.args[mmSim+".SendPin"]; len(a) != 1 || a[0] != "1234" {
		t.Errorf("SendPin %v", a)
	}
	if _, pin := nm.only(t); pin != "1234" {
		t.Errorf("PIN not kept: %q", pin)
	}
	// A wrong PIN, and a blocked SIM
	bus.errs[mmSim+".SendPin"] = dbus.Error{Name: "org.freedesktop.ModemManager1.Error.MobileEquipment.IncorrectPassword"}
	if err := b.Unlock(ctx, md, Config{}, "0000", false); !errors.Is(err, ErrWrongPIN) {
		t.Errorf("wrong PIN: %v", err)
	}
	bus.errs[mmSim+".SendPin"] = dbus.Error{Name: "org.freedesktop.ModemManager1.Error.MobileEquipment.SimPuk"}
	if err := b.Unlock(ctx, md, Config{}, "0000", false); !errors.Is(err, ErrPUK) {
		t.Errorf("PUK: %v", err)
	}
	// Turning the PIN off: a SIM asking for it is unlocked first; a ready one only turned off
	delete(bus.errs, mmSim+".SendPin")
	bus.calls, bus.args = nil, nil
	if err := b.Unlock(ctx, md, Config{}, "1234", true); err != nil {
		t.Fatal(err)
	}
	if a := bus.args[mmSim+".EnablePin"]; len(a) != 2 || a[0] != "1234" || a[1] != false {
		t.Errorf("EnablePin %v", a)
	}
	if len(bus.calls) != 2 || !strings.HasSuffix(bus.calls[0], ".SendPin") || !strings.HasSuffix(bus.calls[1], ".EnablePin") {
		t.Errorf("order %v", bus.calls)
	}
	if _, pin := nm.only(t); pin != "" {
		t.Errorf("the turned-off PIN is still kept: %q", pin)
	}
	if n := len(nm.reactivated); n == 0 {
		t.Error("not activated again after unlocking")
	}
	md.SIM = "ready"
	bus.calls = nil
	if err := b.Unlock(ctx, md, Config{}, "1234", true); err != nil || len(bus.calls) != 1 || !strings.HasSuffix(bus.calls[0], ".EnablePin") {
		t.Errorf("ready SIM: %v %v", err, bus.calls)
	}
	if err := b.Reset(ctx, md); err != nil || bus.calls[len(bus.calls)-1] != "/org/freedesktop/ModemManager1/Modem/0 "+mmModem+".Reset" {
		t.Errorf("reset: %v %v", err, bus.calls)
	}
}

// --- HiLink's orders -----------------------------------------------------------------------------------------------

// hilinkFake: a HiLink web API that keeps what it is told, checks the token and decrypts the PIN with its key.
type hilinkFake struct {
	mu      sync.Mutex
	key     *rsa.PrivateKey
	oaep    bool
	posts   map[string]string // path → last body (decrypted)
	pinOps  []string          // every pin/operate's OperateType, in order
	current string            // the default profile's index ("" = 1)
	badProf bool              // dialup/profiles answers <error>
	pinCode string            // pin/operate's error for a wrong PIN ("" = 103002)
	pinOK   string
	dhcp    string
	tokens  int
	noToken bool
}

func (f *hilinkFake) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ok := func(body string) { w.Write([]byte(xmlHead + body)) }
		switch r.URL.Path {
		case "/api/webserver/SesTokInfo":
			f.tokens++
			ok(fmt.Sprintf(`<response><SesInfo>SessionID=s%d</SesInfo><TokInfo>t%d</TokInfo></response>`, f.tokens, f.tokens))
			return
		case "/api/webserver/publickey":
			ok(fmt.Sprintf(`<response><encpubkeyn>%x</encpubkeyn><encpubkeye>%x</encpubkeye></response>`, f.key.N, f.key.E))
			return
		case "/api/user/state-login":
			pad := "0"
			if f.oaep {
				pad = "1"
			}
			ok(`<response><State>-1</State><rsapadingtype>` + pad + `</rsapadingtype></response>`)
			return
		}
		if !strings.HasPrefix(r.Header.Get("__RequestVerificationToken"), "t") {
			ok(`<error><code>125002</code></error>`)
			return
		}
		if r.Method == http.MethodGet {
			switch r.URL.Path {
			case "/api/dialup/profiles":
				if f.badProf {
					ok(`<error><code>125002</code><message></message></error>`)
					return
				}
				cur := f.current
				if cur == "" {
					cur = "1"
				}
				ok(`<response><CurrentProfile>` + cur + `</CurrentProfile><Profiles><Profile><Index>1</Index><Name>Movistar</Name><ApnName>telefonica.es</ApnName></Profile>` +
					`<Profile><Index>2</Index><Name>EnhancedIRL</Name><ApnName>old.apn</ApnName></Profile></Profiles></response>`)
			case "/api/dialup/connection":
				ok(`<response><RoamAutoConnectEnable>0</RoamAutoConnectEnable><MaxIdelTime>600</MaxIdelTime><ConnectMode>0</ConnectMode><MTU>1450</MTU><auto_dial_switch>1</auto_dial_switch><pdp_always_on>0</pdp_always_on></response>`)
			case "/api/net/net-mode":
				ok(`<response><NetworkMode>00</NetworkMode><NetworkBand>3FFFFFFF</NetworkBand><LTEBand>80005</LTEBand></response>`)
			case "/api/dhcp/settings":
				ok(f.dhcp)
			case "/api/pin/status":
				ok(`<response><SimState>260</SimState></response>`)
			default:
				http.NotFound(w, r)
			}
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("encrypt_transmit") != "" {
			body = f.decrypt(t, string(body))
		}
		f.posts[r.URL.Path] = string(body)
		if r.URL.Path == "/api/pin/operate" {
			if _, rest, ok := strings.Cut(string(body), "<OperateType>"); ok {
				op, _, _ := strings.Cut(rest, "<")
				f.pinOps = append(f.pinOps, op)
			}
		}
		if r.URL.Path == "/api/pin/operate" && !strings.Contains(string(body), "<CurrentPin>"+f.pinOK+"</CurrentPin>") {
			code := f.pinCode
			if code == "" {
				code = "103002"
			}
			ok(`<error><code>` + code + `</code></error>`)
			return
		}
		ok(`<response>OK</response>`)
	})
}

func (f *hilinkFake) decrypt(t *testing.T, hexBody string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(strings.TrimPrefix(hexBody, "0"))
	if err != nil {
		raw, _ = hex.DecodeString(hexBody)
	}
	var b64 []byte
	for len(raw) > 0 {
		chunk := raw[:f.key.Size()]
		raw = raw[f.key.Size():]
		var plain []byte
		if f.oaep {
			plain, err = rsa.DecryptOAEP(sha1New(), nil, f.key, chunk, nil)
		} else {
			plain, err = rsa.DecryptPKCS1v15(nil, f.key, chunk)
		}
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		b64 = append(b64, plain...)
	}
	out, err := base64.StdEncoding.DecodeString(string(b64))
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	return out
}

// The reviewed edges of a HiLink's orders: a ready SIM, a session error is not a wrong PIN, an <error> profile list,
// and our profile being the default when it goes
func TestHiLinkOrderEdges(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &hilinkFake{key: key, posts: map[string]string{}, pinOK: "4321"}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	h, md := hilinkModem(srv)
	ctx := context.Background()
	ready := md
	ready.SIM = "ready"
	if err := h.Unlock(ctx, ready, Config{}, "4321", false); !errors.Is(err, ErrNotLocked) {
		t.Errorf("ready SIM: %v", err)
	}
	f.mu.Lock()
	f.pinCode = "125002"
	f.mu.Unlock()
	if err := h.Unlock(ctx, md, Config{}, "1111", false); err == nil || errors.Is(err, ErrWrongPIN) {
		t.Errorf("a session error taken for a wrong PIN: %v", err)
	}
	f.mu.Lock()
	f.badProf = true
	delete(f.posts, "/api/dialup/profiles")
	f.mu.Unlock()
	if err := h.Apply(ctx, md, Config{APN: "web.es"}); err == nil {
		t.Error("an <error> list taken for no profiles")
	}
	f.mu.Lock()
	_, posted := f.posts["/api/dialup/profiles"]
	f.badProf, f.current = false, "2"
	f.mu.Unlock()
	if posted {
		t.Error("a profile written after an <error> list")
	}
	if err := h.Apply(ctx, md, Config{}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	p := f.posts["/api/dialup/profiles"]
	f.mu.Unlock()
	if !strings.Contains(p, "<SetDefault>1</SetDefault>") || !strings.Contains(p, "<Delete>0</Delete>") {
		t.Errorf("the stick's own profile not made the default again: %s", p)
	}
}

func hilinkModem(srv *httptest.Server) (*HiLink, Modem) {
	h := &HiLink{Client: func(string) *http.Client { return srv.Client() }, baseURL: func(string) string { return srv.URL }}
	return h, Modem{ID: "h-9b2c41d07e5a", Backend: "hilink", Iface: "enx1", gateway: "192.168.8.1", localIP: "192.168.8.100",
		Mode: "auto", Can: []string{"apn", "roaming", "mode", "pin", "reset", "subnet"}}
}

func TestHiLinkOrders(t *testing.T) {
	for _, oaep := range []bool{false, true} {
		key, _ := rsa.GenerateKey(rand.Reader, 2048)
		f := &hilinkFake{key: key, oaep: oaep, posts: map[string]string{}, pinOK: "4321",
			dhcp: `<response><DhcpIPAddress>192.168.8.1</DhcpIPAddress><DhcpLanNetmask>255.255.255.0</DhcpLanNetmask><DhcpStatus>1</DhcpStatus>` +
				`<DhcpStartIPAddress>192.168.8.100</DhcpStartIPAddress><DhcpEndIPAddress>192.168.8.200</DhcpEndIPAddress><DhcpLeaseTime>86400</DhcpLeaseTime>` +
				`<DnsStatus>1</DnsStatus><PrimaryDns>192.168.8.1</PrimaryDns><SecondaryDns>8.8.8.8</SecondaryDns></response>`}
		srv := httptest.NewServer(f.handler(t))
		h, md := hilinkModem(srv)
		ctx := context.Background()
		if err := h.Apply(ctx, md, Config{APN: "ac.vodafone.es", Roaming: true, Mode: "4g"}); err != nil {
			t.Fatal(err)
		}
		if p := f.posts["/api/dialup/profiles"]; !strings.Contains(p, "<Modify>2</Modify>") || !strings.Contains(p, "<Index>2</Index>") ||
			!strings.Contains(p, "<ApnName>ac.vodafone.es</ApnName>") || !strings.Contains(p, "<SetDefault>2</SetDefault>") {
			t.Errorf("profile: %s", p)
		}
		if p := f.posts["/api/dialup/connection"]; !strings.Contains(p, "<RoamAutoConnectEnable>1</RoamAutoConnectEnable>") || !strings.Contains(p, "<MTU>1450</MTU>") {
			t.Errorf("roaming: %s", p)
		}
		if p := f.posts["/api/net/net-mode"]; !strings.Contains(p, "<NetworkMode>03</NetworkMode>") || !strings.Contains(p, "<LTEBand>80005</LTEBand>") {
			t.Errorf("mode: %s", p)
		}
		// Automatic again: our profile goes
		if err := h.Apply(ctx, Modem{ID: md.ID, gateway: md.gateway, Mode: "4g", Roaming: true}, Config{}); err != nil {
			t.Fatal(err)
		}
		if p := f.posts["/api/dialup/profiles"]; !strings.Contains(p, "<Delete>2</Delete>") {
			t.Errorf("auto APN: %s", p)
		}
		// The PIN, encrypted with the modem's key
		if err := h.Unlock(ctx, md, Config{}, "4321", false); err != nil {
			t.Fatal(err)
		}
		if p := f.posts["/api/pin/operate"]; !strings.Contains(p, "<OperateType>0</OperateType>") || !strings.Contains(p, "<CurrentPin>4321</CurrentPin>") {
			t.Errorf("pin: %s", p)
		}
		if err := h.Unlock(ctx, md, Config{}, "1111", true); !errors.Is(err, ErrWrongPIN) {
			t.Errorf("wrong pin: %v", err)
		}
		if p := f.posts["/api/pin/operate"]; !strings.Contains(p, "<OperateType>2</OperateType>") {
			t.Errorf("remove pin: %s", p)
		}
		// A SIM asking for its PIN: entered first, then turned off
		f.mu.Lock()
		f.pinOps = nil
		f.mu.Unlock()
		locked := md
		locked.SIM = "pin"
		if err := h.Unlock(ctx, locked, Config{}, "4321", true); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		if strings.Join(f.pinOps, ",") != "0,2" {
			t.Errorf("ops %v", f.pinOps)
		}
		f.mu.Unlock()
		if err := h.Reset(ctx, md); err != nil || !strings.Contains(f.posts["/api/device/control"], "<Control>1</Control>") {
			t.Errorf("reboot: %v %s", err, f.posts["/api/device/control"])
		}
		if err := h.MoveSubnet(ctx, md, 9); err != nil {
			t.Fatal(err)
		}
		p := f.posts["/api/dhcp/settings"]
		for _, want := range []string{"<DhcpIPAddress>192.168.9.1</DhcpIPAddress>", "<DhcpStartIPAddress>192.168.9.100</DhcpStartIPAddress>",
			"<DhcpEndIPAddress>192.168.9.200</DhcpEndIPAddress>", "<PrimaryDns>192.168.9.1</PrimaryDns>", "<SecondaryDns>8.8.8.8</SecondaryDns>"} {
			if !strings.Contains(p, want) {
				t.Errorf("subnet: %s missing in %s", want, p)
			}
		}
		srv.Close()
	}
}

func TestRequestXMLEscapes(t *testing.T) {
	b := string(requestXML([]field{{"Name", `a<b&"c"`, nil}}))
	if !strings.Contains(b, "<Name>a&lt;b&amp;&#34;c&#34;</Name>") {
		t.Fatal(b)
	}
}

// --- the Manager ------------------------------------------------------------------------------------------------

type ctlBackend struct {
	stub
	applied []Config
	err     error
	pins    []string
}

func (c *ctlBackend) Apply(_ context.Context, _ Modem, cfg Config) error {
	c.applied = append(c.applied, cfg)
	return c.err
}
func (c *ctlBackend) Unlock(_ context.Context, _ Modem, _ Config, pin string, _ bool) error {
	c.pins = append(c.pins, pin)
	return nil
}
func (c *ctlBackend) Reset(context.Context, Modem) error { return nil }

func TestManagerConfigure(t *testing.T) {
	b := &ctlBackend{stub: stub{name: "hilink", list: []Modem{{ID: "h-9b2c41d07e5a", Backend: "hilink", Iface: "enx1", Operator: "Orange",
		Mode: "auto", Modes: []string{"auto", "4g"}, Can: []string{"apn", "roaming", "mode", "pin", "reset"}}}}}
	m := &Manager{Backends: []Backend{b}, Store: memStore(t)}
	ctx := context.Background()
	m.Refresh(ctx)
	alias, apn, mode := "Naranja", "orangeworld", "4g"
	md, err := m.Configure(ctx, "h-9b2c41d07e5a", Change{Alias: &alias, APN: &apn, Mode: &mode})
	if err != nil || md.Alias != "Naranja" || md.Label() != "Naranja" || md.APN != "orangeworld" {
		t.Fatalf("configure: %+v %v", md, err)
	}
	if len(b.applied) != 1 || b.applied[0].APN != "orangeworld" || b.applied[0].Mode != "4g" {
		t.Fatalf("applied: %+v", b.applied)
	}
	// An alias alone does not touch the modem
	alias = "Orange"
	m.Configure(ctx, "h-9b2c41d07e5a", Change{Alias: &alias})
	if len(b.applied) != 1 {
		t.Fatalf("alias applied to the modem: %+v", b.applied)
	}
	// A refused change is not kept
	b.err = errors.New("refused")
	apn = "other"
	if _, err := m.Configure(ctx, "h-9b2c41d07e5a", Change{APN: &apn}); err == nil {
		t.Fatal("refused change accepted")
	}
	if c := m.Store.Get("h-9b2c41d07e5a"); c.APN != "orangeworld" {
		t.Fatalf("refused change kept: %+v", c)
	}
	// 5G on a modem without it; an unknown modem; a bad PIN
	five := "5g"
	if _, err := m.Configure(ctx, "h-9b2c41d07e5a", Change{Mode: &five}); !errors.Is(err, ErrCannot) {
		t.Errorf("5g: %v", err)
	}
	if _, err := m.Configure(ctx, "h-000000000000", Change{Alias: &alias}); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown: %v", err)
	}
	if err := m.Unlock(ctx, "h-9b2c41d07e5a", "12a4", false); !errors.Is(err, ErrConfig) {
		t.Errorf("bad pin: %v", err)
	}
	// The stored alias shows in the next round
	b.err = nil
	m.Refresh(ctx)
	if s := m.Snapshot(); s[0].Alias != "Orange" || s[0].APN != "orangeworld" {
		t.Fatalf("decorated: %+v", s[0])
	}
	// What the order names is applied even if modems.json already says it (the stick's own page may have changed it)
	n, off := len(b.applied), false
	if _, err := m.Configure(ctx, "h-9b2c41d07e5a", Change{Roaming: &off}); err != nil || len(b.applied) != n+1 {
		t.Fatalf("roaming off not applied: %v %d", err, len(b.applied)-n)
	}
	// «Automatic» is kept as such: "" stays for a modem nobody set
	auto := "auto"
	m.Configure(ctx, "h-9b2c41d07e5a", Change{Mode: &auto})
	if c := m.Store.Get("h-9b2c41d07e5a"); c.Mode != "auto" || b.applied[len(b.applied)-1].Mode != "auto" {
		t.Fatalf("auto: %+v %+v", c, b.applied[len(b.applied)-1])
	}
}

func TestManagerReconcileMakesProfilesOnce(t *testing.T) {
	b := &ctlBackend{stub: stub{name: "mm", list: []Modem{{ID: "m-0c7e44a19f02", Backend: "mm", SIM: "ready", devID: "a1b2", netCode: "21401"},
		{ID: "m-5a1d0c3e9b21", Backend: "mm", SIM: "missing", devID: "c3d4"}}}}
	m := &Manager{Backends: []Backend{b}, Store: memStore(t)}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		m.Refresh(ctx)
		waitReconcile(t, m)
	}
	if len(b.applied) != 1 || b.applied[0] != (Config{}) {
		t.Fatalf("profiles: %+v", b.applied)
	}
	// A new configuration: the profile is made again
	m.Store.Update("m-0c7e44a19f02", func(c *Config) { c.Roaming = true })
	m.Refresh(ctx)
	waitReconcile(t, m)
	if len(b.applied) != 2 || !b.applied[1].Roaming {
		t.Fatalf("after a change: %+v", b.applied)
	}
	// A modem carrying the stream waits; so does everyone while modems.json cannot be read
	held := true
	m.Hold = func(id string) bool { return held && id == "m-0c7e44a19f02" }
	m.Store.Update("m-0c7e44a19f02", func(c *Config) { c.APN = "ac.vodafone.es" })
	m.Refresh(ctx)
	waitReconcile(t, m)
	if len(b.applied) != 2 {
		t.Fatalf("a held modem's profile rewritten: %+v", b.applied)
	}
	held = false
	m.Store.Read = func(string) ([]byte, error) { return nil, errors.New("unreadable") }
	m.Refresh(ctx)
	waitReconcile(t, m)
	if len(b.applied) != 2 {
		t.Fatalf("defaults written over an unreadable modems.json: %+v", b.applied)
	}
	m.Store.Read = nil
	m.Refresh(ctx)
	waitReconcile(t, m)
	if len(b.applied) != 3 || b.applied[2].APN != "ac.vodafone.es" {
		t.Fatalf("after the hold: %+v", b.applied)
	}
}

// A profile that failed is retried, and a change of alias alone does not mark it as written
func TestReconcileFailureSurvivesAnAlias(t *testing.T) {
	b := &ctlBackend{stub: stub{name: "mm", list: []Modem{{ID: "m-0c7e44a19f02", Backend: "mm", SIM: "ready", devID: "a1b2"}}},
		err: errors.New("NetworkManager not ready")}
	m := &Manager{Backends: []Backend{b}, Store: memStore(t)}
	ctx := context.Background()
	m.Refresh(ctx)
	waitReconcile(t, m)
	alias := "Mov"
	if _, err := m.Configure(ctx, "m-0c7e44a19f02", Change{Alias: &alias}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	a := m.applied["m-0c7e44a19f02"]
	m.mu.Unlock()
	if !a.failed {
		t.Fatalf("the failure was forgotten: %+v", a)
	}
}

func waitReconcile(t *testing.T, m *Manager) {
	t.Helper()
	for i := 0; i < 200; i++ {
		m.mu.Lock()
		busy := m.reconciling
		m.mu.Unlock()
		if !busy {
			return
		}
		testSleep()
	}
	t.Fatal("reconcile never finished")
}

func TestFreeSubnet(t *testing.T) {
	ips := []net.IP{net.ParseIP("192.168.8.100"), net.ParseIP("192.168.9.100"), net.ParseIP("10.64.12.7"), net.ParseIP("192.168.1.232")}
	if x := freeSubnet(ips); x != 10 {
		t.Fatalf("free subnet: %d", x)
	}
}
