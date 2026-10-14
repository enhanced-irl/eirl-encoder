package modem

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// ModemManager over the system bus (1.10 on the board: no 5G modes, no NR signal; MODEMS.md §2). Reading
// needs no permission; Signal.Setup (turning on the extended signal readings) needs the polkit action
// org.freedesktop.ModemManager1.Device.Control, granted to the app by deploy/eirl-encoder-mm.pkla.
//
// It never starts ModemManager: if the name has no owner on the bus there are no modems (the service is
// off on a board without modems; D-Bus activation would start it otherwise).

const (
	mmDest    = "org.freedesktop.ModemManager1"
	mmPath    = dbus.ObjectPath("/org/freedesktop/ModemManager1")
	mmModem   = mmDest + ".Modem"
	mm3gpp    = mmDest + ".Modem.Modem3gpp"
	mmSignal  = mmDest + ".Modem.Signal"
	mmSim     = mmDest + ".Sim"
	mmBearer  = mmDest + ".Bearer"
	objMgr    = "org.freedesktop.DBus.ObjectManager"
	propsIntf = "org.freedesktop.DBus.Properties"
)

// signalRate: seconds between ModemManager's signal readings (Signal.Setup).
const signalRate = 5

// mmBus: what the backend needs from D-Bus (a fake in the tests).
type mmBus interface {
	hasOwner(ctx context.Context, name string) (bool, error)
	managedObjects(ctx context.Context) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error)
	props(ctx context.Context, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error)
	call(ctx context.Context, path dbus.ObjectPath, method string, args ...any) error
}

// MM: the ModemManager backend.
type MM struct {
	bus mmBus
	nm  nmBus // NetworkManager: the modems' gsm profiles
	// Providers: the APN database ("" = ProvidersFile)
	Providers string

	mu      sync.Mutex
	setupAt map[dbus.ObjectPath]time.Time // when Signal.Setup was last asked for that modem object
	home    map[string]simHome            // by modem id: the SIM's last reading (a failed one keeps it)
	now     func() time.Time
}

// simHome: what the SIM said last: its ICCID, home operator code and name.
type simHome struct{ id, code, name string }

// setupRetry: how often Signal.Setup is asked again while the readings stay off (Rate 0): a refusal (no polkit)
// or a timeout does not leave a modem without them for good, and a restarted ModemManager reuses /Modem/0.
const setupRetry = time.Minute

// NewMM: the backend on the real system bus.
func NewMM() *MM { return &MM{bus: &systemBus{}, nm: &nmSystem{}} }

func (b *MM) Name() string { return "mm" }

// List: every modem ModemManager knows, with its SIM and bearer.
func (b *MM) List(ctx context.Context, _ []Link) ([]Modem, error) {
	up, err := b.bus.hasOwner(ctx, mmDest)
	if err != nil {
		return nil, err
	}
	if !up {
		return nil, nil
	}
	objs, err := b.bus.managedObjects(ctx)
	if err != nil {
		return nil, err
	}
	var out []Modem
	present := map[dbus.ObjectPath]bool{}
	for path, ifaces := range objs {
		props, ok := ifaces[mmModem]
		if !ok {
			continue
		}
		present[path] = true
		md := b.modem(ctx, path, props, ifaces)
		out = append(out, md)
	}
	b.mu.Lock()
	for path := range b.setupAt {
		if !present[path] {
			delete(b.setupAt, path) // gone: a modem that comes back under that path is asked at once
		}
	}
	b.mu.Unlock()
	return out, nil
}

func (b *MM) modem(ctx context.Context, path dbus.ObjectPath, p map[string]dbus.Variant, ifaces map[string]map[string]dbus.Variant) Modem {
	md := Modem{Backend: "mm", Manufacturer: str(p, "Manufacturer"), Model: str(p, "Model"), path: string(path),
		devID: str(p, "DeviceIdentifier"), Can: []string{"apn", "roaming", "pin", "reset"}}
	state := i32(p, "State")
	md.State = mmState(state)
	imei := str(p, "EquipmentIdentifier")
	g3 := ifaces[mm3gpp]
	if v := str(g3, "Imei"); v != "" {
		imei = v
	}
	md.ID, md.IMEITail = stableID("m-", imei), tail(imei)
	if imei == "" {
		md.ID = stableID("m-", string(path)) // no IMEI (rare): stable while ModemManager runs
	}
	md.Iface = netPort(p["Ports"])
	md.Tech = mmTech(u32(p, "AccessTechnologies"))
	if q, ok := p["SignalQuality"].Value().([]any); ok && len(q) == 2 {
		if v, ok := q[0].(uint32); ok && (md.State == "registered" || md.State == "connected" || md.State == "connecting") {
			md.Signal.Quality = ptrI(int(v))
		}
	}
	md.Registration = mmRegistration(g3)
	md.Operator = str(g3, "OperatorName")
	md.netCode = str(g3, "OperatorCode")
	md.combos = mmCombos(p["SupportedModes"])
	md.Mode, md.Modes = mmModeNow(p["CurrentModes"], md.combos)
	if len(md.Modes) > 1 {
		md.Can = append(md.Can, "mode")
	}

	// SIM: the lock first (PIN/PUK), then whether there is a SIM at all
	simPath, _ := p["Sim"].Value().(dbus.ObjectPath)
	md.SIM = mmSIM(state, u32(p, "StateFailedReason"), u32(p, "UnlockRequired"), simPath)
	if simPath != "" && simPath != "/" {
		md.simPath = string(simPath)
		if sp, err := b.bus.props(ctx, simPath, mmSim); err == nil {
			h := simHome{id: str(sp, "SimIdentifier"), code: str(sp, "OperatorIdentifier"), name: str(sp, "OperatorName")}
			b.mu.Lock()
			if b.home == nil {
				b.home = map[string]simHome{}
			}
			b.home[md.ID] = h
			b.mu.Unlock()
		}
		// A failed reading keeps the last one: the automatic APN (and so the profile) does not flap with it
		b.mu.Lock()
		h, known := b.home[md.ID]
		b.mu.Unlock()
		if known {
			md.ICCIDTail, md.simID, md.simName = tail(h.id), h.id, h.name
			if md.Operator == "" {
				md.Operator = h.name
			}
			// The SIM's home operator (from its IMSI) gives the APN: roaming, the network's code is the visited one
			if h.code != "" {
				md.netCode = h.code
			}
		}
	} else {
		b.mu.Lock()
		delete(b.home, md.ID) // no SIM: a new one is read afresh
		b.mu.Unlock()
	}
	// The bearer gives the interface once connected (some modems have no NET port before)
	if bearers, ok := p["Bearers"].Value().([]dbus.ObjectPath); ok {
		for _, bp := range bearers {
			if bprops, err := b.bus.props(ctx, bp, mmBearer); err == nil {
				if v := str(bprops, "Interface"); v != "" && (md.Iface == "" || boolean(bprops, "Connected")) {
					md.Iface = v
				}
			}
		}
	}
	// Extended signal: the interface is there (MM ≥ 1.2) but off until someone sets a rate
	if sig, ok := ifaces[mmSignal]; ok {
		if u32(sig, "Rate") == 0 {
			b.setup(ctx, path)
		}
		md.Signal = mmSignalOf(sig, md.Tech, md.Signal)
	}
	return md
}

// setup turns on the extended signal readings (called while their Rate is 0), at most once per setupRetry
// for each modem object.
func (b *MM) setup(ctx context.Context, path dbus.ObjectPath) {
	now := time.Now()
	if b.now != nil {
		now = b.now()
	}
	b.mu.Lock()
	last, asked := b.setupAt[path]
	if asked && now.Sub(last) < setupRetry {
		b.mu.Unlock()
		return
	}
	if b.setupAt == nil {
		b.setupAt = map[dbus.ObjectPath]time.Time{}
	}
	b.setupAt[path] = now
	b.mu.Unlock()
	_ = b.bus.call(ctx, path, mmSignal+".Setup", uint32(signalRate))
}

// mmState: MMModemState → our names.
func mmState(s int32) string {
	switch s {
	case -1:
		return "failed"
	case 1, 5: // initializing, enabling
		return "enabling"
	case 2:
		return "locked"
	case 3, 4: // disabled, disabling
		return "disabled"
	case 6, 7: // enabled (not registered yet), searching
		return "searching"
	case 8, 9: // registered, disconnecting
		return "registered"
	case 10:
		return "connecting"
	case 11:
		return "connected"
	}
	return "unknown"
}

// mmSIM: from the state, its failed reason (MMModemStateFailedReason) and the lock (MMModemLock).
func mmSIM(state int32, failedReason, lock uint32, sim dbus.ObjectPath) string {
	switch failedReason {
	case 2:
		return "missing"
	case 3:
		return "error"
	}
	switch lock {
	case 2: // SIM PIN
		return "pin"
	case 4: // SIM PUK
		return "puk"
	case 0, 1, 3, 5: // unknown, none, PIN2, PUK2 (they do not block data)
	default: // network/corporate locks
		return "error"
	}
	if sim == "" || sim == "/" {
		if state == -1 || state >= 3 {
			return "missing"
		}
		return "unknown"
	}
	return "ready"
}

// mmRegistration: MMModem3gppRegistrationState → our names.
func mmRegistration(g3 map[string]dbus.Variant) string {
	if g3 == nil {
		return ""
	}
	switch u32(g3, "RegistrationState") {
	case 0:
		return "idle"
	case 1, 6, 9: // home, home SMS only, home CSFB not preferred
		return "home"
	case 2:
		return "searching"
	case 3, 8: // denied, emergency only
		return "denied"
	case 5, 7, 10: // roaming, roaming SMS only, roaming CSFB not preferred
		return "roaming"
	}
	return "unknown"
}

// mmTech: MMModemAccessTechnology (bit mask) → the best technology in use.
func mmTech(bits uint32) string {
	const (
		lte  = 1 << 14
		nr5g = 1 << 15 // ModemManager ≥ 1.14
	)
	switch {
	case bits&nr5g != 0 && bits&lte != 0:
		return "5g-nsa"
	case bits&nr5g != 0:
		return "5g"
	case bits&lte != 0:
		return "4g"
	case bits&(0x1f<<5) != 0: // UMTS, HSDPA, HSUPA, HSPA, HSPA+
		return "3g"
	case bits&(0xf<<10) != 0: // 1xRTT, EVDO 0/A/B
		return "3g"
	case bits&(0xf<<1) != 0: // GSM, GSM compact, GPRS, EDGE
		return "2g"
	}
	return ""
}

// mmSignalOf: the extended signal for the technology in use. 5G NSA is judged by its LTE anchor (that is
// what ModemManager 1.10 reports, and it carries the uplink); standalone 5G by the NR readings.
func mmSignalOf(sig map[string]dbus.Variant, tech string, base Signal) Signal {
	out := base
	pick := func(key string) map[string]dbus.Variant {
		m, _ := sig[key].Value().(map[string]dbus.Variant)
		return m
	}
	var m map[string]dbus.Variant
	switch tech {
	case "5g":
		m = pick("Nr5g")
		if len(m) == 0 {
			m = pick("Lte")
		}
	case "4g", "5g-nsa":
		m = pick("Lte")
	case "3g":
		m = pick("Umts")
	case "2g":
		m = pick("Gsm")
	}
	if len(m) == 0 {
		return out
	}
	set := func(dst **float64, key string) {
		if v, ok := m[key].Value().(float64); ok && v > -500 && v < 500 {
			*dst = ptrF(round1(v))
		}
	}
	set(&out.RSRP, "rsrp")
	set(&out.RSRQ, "rsrq")
	set(&out.SINR, "snr")
	set(&out.RSSI, "rssi")
	return out
}

// netPort: the NET port's name in Ports, an a(su) (type 2 = MM_MODEM_PORT_TYPE_NET).
func netPort(v dbus.Variant) string {
	list, ok := v.Value().([][]any)
	if !ok {
		if alt, ok := v.Value().([]any); ok {
			for _, e := range alt {
				if pair, ok := e.([]any); ok {
					list = append(list, pair)
				}
			}
		}
	}
	for _, pair := range list {
		if len(pair) != 2 {
			continue
		}
		name, _ := pair[0].(string)
		typ, _ := pair[1].(uint32)
		if typ == 2 && name != "" {
			return name
		}
	}
	return ""
}

func str(m map[string]dbus.Variant, k string) string {
	v, _ := m[k].Value().(string)
	return strings.TrimSpace(v)
}

func u32(m map[string]dbus.Variant, k string) uint32 {
	v, _ := m[k].Value().(uint32)
	return v
}

func i32(m map[string]dbus.Variant, k string) int32 {
	v, _ := m[k].Value().(int32)
	return v
}

func boolean(m map[string]dbus.Variant, k string) bool {
	v, _ := m[k].Value().(bool)
	return v
}

func round1(v float64) float64 {
	if v < 0 {
		return -float64(int(-v*10+0.5)) / 10
	}
	return float64(int(v*10+0.5)) / 10
}

// --- the real bus ---------------------------------------------------------------------------------------

type systemBus struct {
	mu   sync.Mutex
	conn *dbus.Conn
}

func (s *systemBus) bus() (*dbus.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil && s.conn.Connected() {
		return s.conn, nil
	}
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	s.conn = c
	return c, nil
}

func (s *systemBus) hasOwner(ctx context.Context, name string) (bool, error) {
	c, err := s.bus()
	if err != nil {
		return false, err
	}
	var ok bool
	err = c.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, name).Store(&ok)
	return ok, err
}

func (s *systemBus) managedObjects(ctx context.Context) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	c, err := s.bus()
	if err != nil {
		return nil, err
	}
	var out map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	err = c.Object(mmDest, mmPath).CallWithContext(ctx, objMgr+".GetManagedObjects", 0).Store(&out)
	return out, err
}

func (s *systemBus) props(ctx context.Context, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	c, err := s.bus()
	if err != nil {
		return nil, err
	}
	var out map[string]dbus.Variant
	err = c.Object(mmDest, path).CallWithContext(ctx, propsIntf+".GetAll", 0, iface).Store(&out)
	return out, err
}

func (s *systemBus) call(ctx context.Context, path dbus.ObjectPath, method string, args ...any) error {
	c, err := s.bus()
	if err != nil {
		return err
	}
	return c.Object(mmDest, path).CallWithContext(ctx, method, 0, args...).Err
}
