package bluetooth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/btaudio"
)

// --- what is needed from BlueZ and BlueALSA (D-Bus on the board, a fake one in the tests) ---------------------

type adapter struct {
	Path, Address, Alias                         string
	Powered, Discoverable, Pairable, Discovering bool
}

type rawDevice struct {
	Path, Address string
	Name          string
	HasName       bool // it announced a name (a device found without one is not listed)
	Icon          string
	Class         uint32
	UUIDs         []string
	Paired        bool
	Trusted       bool
	Connected     bool
	Blocked       bool
	RSSI          *int // only while a search sees it
}

// pcm is one of BlueALSA's PCMs: a device's audio in one direction. Out: the board writes it (the device plays).
type pcm struct {
	Address string
	Profile string // a2dp | sco
	Out     bool
	Running bool
	DelayMs int
}

type snapshot struct {
	Owner   string   // bluetoothd's unique name on the bus: a new one means it restarted
	Adapter *adapter // nil: no adapter
	Devices []rawDevice
}

var (
	errNoStack = errors.New("bluetooth: bluetoothd is not running")
	errNoAudio = errors.New("bluetooth: bluealsa is not running")
)

type backend interface {
	watch(ctx context.Context, ag *agent) (<-chan struct{}, error)
	snapshot(ctx context.Context) (snapshot, error)
	pcms(ctx context.Context) ([]pcm, error)
	registerAgent(ctx context.Context) error
	setAdapter(ctx context.Context, path, prop string, v any) error
	discovery(ctx context.Context, path string, on bool) error
	pair(ctx context.Context, dev string) error
	connect(ctx context.Context, dev string) error
	disconnect(ctx context.Context, dev string) error
	setTrusted(ctx context.Context, dev string, on bool) error
	remove(ctx context.Context, adapterPath, dev string) error
}

// Timings
const (
	VisibleFor   = 2 * time.Minute  // the visible window (D4)
	ScanFor      = 30 * time.Second // a search
	pairWait     = 45 * time.Second // BlueZ gives up a pairing before this
	connectWait  = 25 * time.Second
	callWait     = 5 * time.Second
	refreshEvery = 5 * time.Second
	settle       = 200 * time.Millisecond // a burst of signals is read once
	redial       = 5 * time.Second
	// outgoingFor: how long after the app starts a pairing the agent accepts it (BlueZ asks while it runs)
	outgoingFor = pairWait + 15*time.Second
	// seenFor: a device found by a search stays listed this long after the search sees it (BlueZ 5.48 keeps found
	// devices 3 min): pairing one stops the search, and the others must not vanish from the list
	seenFor = 2 * time.Minute
	// powerRetry: after the adapter refused to switch on or off (rfkill, firmware), how long before trying again
	powerRetry = time.Minute
	// reconnectWait: a reconnection at power on (the device may be off: no need to wait as long as an order)
	reconnectWait = 12 * time.Second
	// pruneAfter: a device with a use whose pairing BlueZ no longer has (forgotten outside the app) loses it after this
	pruneAfter = time.Minute
	maxDevices = 32
	maxSaved   = 64
)

var reAddr = regexp.MustCompile(`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`)

func validAddr(a string) bool { return reAddr.MatchString(a) }

// saved is /etc/eirl-encoder/bluetooth.json: whether Bluetooth is on and each device's use (the pairings are BlueZ's).
type saved struct {
	On   *bool             `json:"on,omitempty"` // nil: the default, off
	Uses map[string]string `json:"uses"`
}

// on: Bluetooth is OFF until the operator switches it on (security review): a board with an adapter does not show
// its radio and its stack to everyone around without being asked.
func (s saved) on() bool { return s.On != nil && *s.On }

// Options of the manager.
type Options struct {
	ConfigPath string // /etc/eirl-encoder/bluetooth.json
	EnginePath string // btaudio.Path
	// Name: the encoder's name, which the adapter takes as its alias (what phones show). nil or "": BlueZ's
	Name func() string
	Log  *slog.Logger
	Now  func() time.Time
	// Read and Write the files (config.WriteAtomic and safefile.ReadOwned in main)
	Read  func(path string) ([]byte, error)
	Write func(path string, data []byte, mode os.FileMode) error
}

// Manager is the board's Bluetooth: one goroutine (Run) follows BlueZ and BlueALSA and applies the rules; the API
// asks it for its State and gives it orders.
type Manager struct {
	o  Options
	be backend
	ag *agent

	mu          sync.Mutex
	cfg         saved
	snap        snapshot
	snapErr     error
	pcms        []pcm
	pcmErr      error
	read        bool   // a first refresh happened
	agentOwner  string // the bluetoothd our agent is registered with
	visibleTill time.Time
	scanTill    time.Time
	wasPowered  bool
	powerFailed bool                 // the adapter refused the last power change
	powerRetry  time.Time            // not before then
	pending     map[string]string    // address → the order running on it
	errs        map[string]string    // address → why its last order failed
	connectedAt map[string]time.Time // since when each connected device is
	outgoing    map[string]time.Time // address → until when a pairing the app started is accepted
	seenAt      map[string]time.Time // address → when a search last saw it
	unpairedAt  map[string]time.Time // address with a use → since when BlueZ has no pairing for it
	lastFile    []byte

	kick chan struct{}
	op   sync.Mutex // one order at a time (a search, pairing and connecting at once trip each other up in BlueZ)
}

// New makes the manager on the board's D-Bus.
func New(o Options) *Manager { return newManager(o, &dbusBT{}) }

func newManager(o Options, be backend) *Manager {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Read == nil {
		o.Read = os.ReadFile
	}
	if o.EnginePath == "" {
		o.EnginePath = btaudio.Path
	}
	m := &Manager{o: o, be: be, pending: map[string]string{}, errs: map[string]string{},
		connectedAt: map[string]time.Time{}, outgoing: map[string]time.Time{}, seenAt: map[string]time.Time{},
		unpairedAt: map[string]time.Time{},
		kick:       make(chan struct{}, 1)}
	m.ag = &agent{allowed: m.allowed, log: o.Log}
	m.cfg = m.load()
	return m
}

func (m *Manager) load() saved {
	s := saved{Uses: map[string]string{}}
	if m.o.ConfigPath == "" {
		return s
	}
	b, err := m.o.Read(m.o.ConfigPath)
	if err != nil {
		return s
	}
	var f saved
	if json.Unmarshal(b, &f) != nil {
		m.o.Log.Warn("bluetooth config unreadable: defaults", "event", "bt_config_bad")
		return s
	}
	s.On = f.On
	for a, u := range f.Uses {
		if validAddr(a) && validUse(u) && len(s.Uses) < maxSaved {
			s.Uses[a] = u
		}
	}
	return s
}

// save writes the configuration (with m.mu).
func (m *Manager) save() error {
	if m.o.ConfigPath == "" || m.o.Write == nil {
		return nil
	}
	b, _ := json.MarshalIndent(m.cfg, "", "  ")
	return m.o.Write(m.o.ConfigPath, append(b, '\n'), 0o640)
}

// Run follows BlueZ and BlueALSA until ctx ends: it registers the agent (again when bluetoothd restarts), keeps the
// adapter as configured, closes the visible window and the search when they run out, trusts what was paired, and
// rewrites the engine's file on every change.
func (m *Manager) Run(ctx context.Context) {
	for ctx.Err() == nil {
		ticks, err := m.be.watch(ctx, m.ag)
		if err != nil {
			m.mu.Lock()
			m.snapErr, m.read = err, true
			m.mu.Unlock()
			m.writeEngine()
			m.sleep(ctx, redial)
			continue
		}
		m.mu.Lock()
		m.agentOwner = "" // a new connection: the agent is exported on it, register it again
		m.mu.Unlock()
		m.loop(ctx, ticks)
	}
	// The engine's file is left as it is when the app stops: the engine keeps what is on air through the app's
	// restart (btaudio.Reader), and a device that really goes, BlueALSA closes
}

func (m *Manager) sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (m *Manager) loop(ctx context.Context, ticks <-chan struct{}) {
	m.refresh(ctx)
	t := time.NewTimer(refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return // the connection went: dial again
			}
			m.sleep(ctx, settle)
			m.refresh(ctx)
		case <-m.kick:
			m.refresh(ctx)
		case <-t.C:
			m.refresh(ctx)
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(refreshEvery)
	}
}

// Kick asks for a refresh now (after an order).
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) refresh(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, callWait)
	snap, err := m.be.snapshot(c)
	var pcms []pcm
	var perr error
	if err == nil {
		pcms, perr = m.be.pcms(c)
	}
	cancel()
	now := m.o.Now()
	m.mu.Lock()
	m.snap, m.snapErr, m.pcms, m.pcmErr, m.read = snap, err, pcms, perr, true
	conn := map[string]bool{}
	for _, d := range snap.Devices {
		if d.Connected {
			conn[d.Address] = true
			if _, ok := m.connectedAt[d.Address]; !ok {
				m.connectedAt[d.Address] = now
			}
		}
	}
	for a := range m.connectedAt {
		if !conn[a] {
			delete(m.connectedAt, a)
		}
	}
	for a, until := range m.outgoing {
		if now.After(until) {
			delete(m.outgoing, a)
		}
	}
	for _, d := range snap.Devices {
		if d.RSSI != nil {
			m.seenAt[d.Address] = now
		}
	}
	for a, at := range m.seenAt {
		if now.Sub(at) > seenFor {
			delete(m.seenAt, a)
		}
	}
	needAgent := err == nil && snap.Owner != "" && snap.Owner != m.agentOwner
	m.mu.Unlock()
	if needAgent {
		c, cancel := context.WithTimeout(ctx, callWait)
		if err := m.be.registerAgent(c); err != nil {
			m.o.Log.Warn("bluetooth agent not registered", "event", "bt_agent_failed", "err", err)
		} else {
			m.mu.Lock()
			m.agentOwner = snap.Owner
			m.mu.Unlock()
			m.o.Log.Info("bluetooth agent registered", "event", "bt_agent_registered")
		}
		cancel()
	}
	if err == nil && snap.Adapter != nil {
		m.housekeep(ctx, snap)
	}
	m.writeEngine()
}

// housekeep applies the rules to what BlueZ says now.
func (m *Manager) housekeep(ctx context.Context, snap snapshot) {
	a := snap.Adapter
	now := m.o.Now()
	m.mu.Lock()
	want := m.cfg.on()
	visible := now.Before(m.visibleTill)
	scanning := now.Before(m.scanTill)
	pairingOut := len(m.outgoing) > 0 // a pairing the app runs keeps the adapter bondable (pairAndConnect)
	justOn := a.Powered && !m.wasPowered
	m.wasPowered = a.Powered
	// Paired devices (security review). Ours (with a use) are trusted if they are not yet. One that paired from its
	// side inside the window (not a pairing the app started: pairAndConnect deals with those) is trusted, gets a use
	// only if it SENDS audio (a phone: what the window is for; never the monitor or a microphone, which the operator
	// chooses), and the window closes: one device per window. A device whose pairing BlueZ no longer has loses its use.
	var trust, incoming []rawDevice
	paired := map[string]bool{}
	for _, d := range snap.Devices {
		if !d.Paired || d.Blocked {
			continue
		}
		paired[d.Address] = true
		_, ours := m.cfg.Uses[d.Address]
		outgoing := now.Before(m.outgoing[d.Address])
		switch {
		case ours && !d.Trusted:
			trust = append(trust, d)
		case !ours && !d.Trusted && visible && !outgoing:
			incoming = append(incoming, d)
		}
	}
	// Pruned only after pruneAfter without its pairing: bluetoothd lists its devices a moment after it starts
	pruned := false
	for addr := range m.cfg.Uses {
		switch since, missing := m.unpairedAt[addr]; {
		case paired[addr] || m.pending[addr] != "":
			delete(m.unpairedAt, addr)
		case !missing:
			m.unpairedAt[addr] = now
		case now.Sub(since) >= pruneAfter:
			delete(m.cfg.Uses, addr)
			delete(m.unpairedAt, addr)
			pruned = true
			m.o.Log.Info("bluetooth use dropped: no pairing", "event", "bt_use_pruned", "device", addr)
		}
	}
	if len(incoming) > 0 {
		visible, m.visibleTill = false, time.Time{} // the agent refuses from now on
	}
	for _, d := range incoming {
		if slices.Contains(uses(d.UUIDs), UseInput) && len(m.cfg.Uses) < maxSaved {
			m.cfg.Uses[d.Address] = UseInput
		}
		m.o.Log.Info("bluetooth device paired from its side", "event", "bt_incoming", "device", d.Address,
			"use", m.cfg.Uses[d.Address])
	}
	if pruned || len(incoming) > 0 {
		if err := m.save(); err != nil {
			m.o.Log.Warn("bluetooth config not saved", "event", "bt_config_save_failed", "err", err)
		}
	}
	trust = append(trust, incoming...)
	m.mu.Unlock()

	c, cancel := context.WithTimeout(ctx, callWait)
	defer cancel()
	set := func(prop string, v any) {
		if err := m.be.setAdapter(c, a.Path, prop, v); err != nil {
			m.o.Log.Warn("bluetooth adapter not set", "event", "bt_adapter_failed", "prop", prop, "err", err)
		}
	}
	// D4: not pairable outside the window, also before switching on (BlueZ starts pairable; bondable can be set with
	// the adapter off), so the board is never pairable for a moment at start
	if !visible && a.Pairable && !pairingOut {
		set("Pairable", false)
	}
	if a.Powered != want {
		m.mu.Lock()
		due := !now.Before(m.powerRetry)
		m.mu.Unlock()
		if due {
			err := m.be.setAdapter(c, a.Path, "Powered", want)
			m.mu.Lock()
			if err != nil {
				// rfkill or the firmware: say it once and try again later, not every refresh
				if !m.powerFailed {
					m.o.Log.Warn("bluetooth adapter refused the power change", "event", "bt_power_failed", "on", want, "err", err)
				}
				m.powerFailed, m.powerRetry = true, now.Add(powerRetry)
			} else {
				m.powerFailed, m.powerRetry = false, time.Time{}
				m.o.Log.Info("bluetooth power", "event", "bt_power", "on", want)
			}
			m.mu.Unlock()
		}
	} else {
		m.mu.Lock()
		m.powerFailed, m.powerRetry = false, time.Time{}
		m.mu.Unlock()
	}
	if !a.Powered {
		return
	}
	if name := m.alias(); name != "" && a.Alias != name {
		set("Alias", name)
	}
	// D4: visible only inside the window (also if something else switched it on)
	if !visible && a.Discoverable {
		set("Discoverable", false)
	}
	if !scanning && a.Discovering {
		if err := m.be.discovery(c, a.Path, false); err != nil {
			m.o.Log.Warn("bluetooth search not stopped", "event", "bt_scan_stop_failed", "err", err)
		}
	}
	for _, d := range trust {
		if err := m.be.setTrusted(c, d.Path, true); err == nil {
			m.o.Log.Info("bluetooth device trusted", "event", "bt_trusted", "device", d.Address)
		}
	}
	if justOn {
		m.reconnect(snap)
	}
}

// reconnect: once per power-on, one connection attempt to each paired device with a use that is not connected
// (headphones usually come back by themselves; a phone used as an input, not always).
func (m *Manager) reconnect(snap snapshot) {
	m.mu.Lock()
	var todo []rawDevice
	for _, d := range snap.Devices {
		if _, ok := m.cfg.Uses[d.Address]; ok && d.Paired && !d.Connected && !d.Blocked {
			todo = append(todo, d)
		}
	}
	m.mu.Unlock()
	if len(todo) == 0 {
		return
	}
	go func() {
		for _, d := range todo {
			if !m.op.TryLock() {
				return // an order from the user goes first
			}
			// Shown as an order running (the other orders wait for it: the web and the screen grey them out)
			m.mu.Lock()
			m.pending[d.Address] = ActConnect
			m.mu.Unlock()
			m.Kick()
			c, cancel := context.WithTimeout(context.Background(), reconnectWait)
			err := m.be.connect(c, d.Path)
			cancel()
			m.mu.Lock()
			delete(m.pending, d.Address)
			m.mu.Unlock()
			m.op.Unlock()
			m.o.Log.Info("bluetooth reconnect", "event", "bt_reconnect", "device", d.Address, "ok", err == nil)
		}
		m.Kick()
	}()
}

func (m *Manager) alias() string {
	if m.o.Name == nil {
		return ""
	}
	return cleanName(m.o.Name())
}

// allowed (the agent's rule, D4): a pairing or service of that device may go ahead now.
func (m *Manager) allowed(addr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.o.Now()
	return now.Before(m.visibleTill) || now.Before(m.outgoing[addr])
}

// --- the engine's file -----------------------------------------------------------------------------------------

// engineFile: what is ready for audio (with m.mu). Out: of the connected devices used to listen (or as a headset),
// the last one connected; In: the ones used as an input or a headset, in the order they connected.
func (m *Manager) engineFile() btaudio.File {
	f := btaudio.File{V: btaudio.Version}
	if m.snapErr != nil || m.pcmErr != nil || m.snap.Adapter == nil || !m.snap.Adapter.Powered {
		return f
	}
	type key struct {
		addr, profile string
		out           bool
	}
	have := map[key]pcm{}
	for _, p := range m.pcms {
		have[key{p.Address, p.Profile, p.Out}] = p
	}
	var outAt time.Time
	type in struct {
		d  btaudio.Device
		at time.Time
	}
	var ins []in
	for _, d := range m.snap.Devices {
		use := m.cfg.Uses[d.Address]
		if !d.Paired || !d.Connected || use == "" {
			continue
		}
		at := m.connectedAt[d.Address]
		name := m.deviceName(d)
		switch use {
		case UseListen, UseMic:
			prof := btaudio.ProfileA2DP
			if use == UseMic {
				prof = btaudio.ProfileSCO
			}
			if p, ok := have[key{d.Address, prof, true}]; ok && (f.Out == nil || at.After(outAt)) {
				f.Out = &btaudio.Device{Addr: d.Address, Name: name, Profile: prof, DelayMs: min(p.DelayMs, btaudio.MaxDelayMs)}
				outAt = at
			}
		}
		switch use {
		case UseInput, UseMic:
			prof := btaudio.ProfileA2DP
			if use == UseMic {
				prof = btaudio.ProfileSCO
			}
			if _, ok := have[key{d.Address, prof, false}]; ok {
				ins = append(ins, in{btaudio.Device{Addr: d.Address, Name: name, Profile: prof}, at})
			}
		}
	}
	sort.SliceStable(ins, func(i, j int) bool { return ins[i].at.Before(ins[j].at) })
	for _, x := range ins {
		if len(f.In) < btaudio.MaxInputs {
			f.In = append(f.In, x.d)
		}
	}
	return f
}

func (m *Manager) deviceName(d rawDevice) string {
	if n := cleanName(d.Name); n != "" {
		return n
	}
	return d.Address
}

// writeEngine rewrites the engine's file when what is ready changed.
func (m *Manager) writeEngine() {
	if m.o.Write == nil {
		return
	}
	m.mu.Lock()
	b := btaudio.Marshal(m.engineFile())
	same := bytes.Equal(b, m.lastFile)
	m.mu.Unlock()
	if same {
		return
	}
	if err := m.o.Write(m.o.EnginePath, b, 0o640); err != nil {
		m.o.Log.Warn("bluetooth engine file not written", "event", "bt_engine_file_failed", "err", err)
		return
	}
	m.mu.Lock()
	m.lastFile = b
	m.mu.Unlock()
	m.o.Log.Info("bluetooth audio devices", "event", "bt_engine_file", "file", string(bytes.TrimSpace(b)))
}

// --- the state --------------------------------------------------------------------------------------------------

// State is what the web and the screen show.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.o.Now()
	s := State{On: m.cfg.on(), Devices: []Device{}}
	switch {
	case !m.read:
		s.Reason = ReasonNoStack // not read yet: the first refresh comes within a second
		return s
	case m.snapErr != nil:
		s.Reason = ReasonNoStack
		return s
	case m.snap.Adapter == nil:
		s.Reason = ReasonNoAdapter
		return s
	case m.pcmErr != nil:
		// BlueZ is there: devices can be managed, not heard (nothing goes to the engine's file)
		s.Reason = ReasonNoAudio
	}
	a := m.snap.Adapter
	s.Available, s.Powered, s.Name = true, a.Powered, cleanName(a.Alias)
	s.PowerFailed = m.powerFailed && a.Powered != s.On
	if a.Powered && now.Before(m.visibleTill) {
		s.Visible = true
		s.VisibleLeft = int((m.visibleTill.Sub(now) + time.Second - 1) / time.Second)
	}
	s.Scanning = a.Powered && a.Discovering && now.Before(m.scanTill)
	f := m.engineFile()
	ready := map[string]bool{}
	if f.Out != nil {
		ready[f.Out.Addr] = true
	}
	for _, d := range f.In {
		ready[d.Addr] = true
	}
	for _, d := range m.snap.Devices {
		if d.Blocked || !isAudio(d.Icon, d.Class, d.UUIDs) {
			continue
		}
		pending := m.pending[d.Address]
		// A device found and not paired: only with a name, and only if a search saw it lately (or being paired)
		_, seen := m.seenAt[d.Address]
		if !d.Paired && (!d.HasName || d.RSSI == nil && !seen) && pending == "" {
			continue
		}
		u := uses(d.UUIDs)
		use := ""
		if d.Paired {
			use = m.cfg.Uses[d.Address]
			if use != "" && !slices.Contains(u, use) {
				u = append(u, use) // its profiles are not read yet: keep what it is used for
			}
		}
		dv := Device{Address: d.Address, Name: m.deviceName(d), Kind: kindOf(d.Icon, d.Class, d.UUIDs), Paired: d.Paired,
			Connected: d.Connected, Ready: ready[d.Address], Use: use, Uses: u, RSSI: d.RSSI, Pending: pending,
			Error: m.errs[d.Address], Output: f.Out != nil && f.Out.Addr == d.Address}
		s.Devices = append(s.Devices, dv)
	}
	sort.SliceStable(s.Devices, func(i, j int) bool {
		x, y := s.Devices[i], s.Devices[j]
		switch {
		case x.Paired != y.Paired:
			return x.Paired
		case x.Paired && x.Connected != y.Connected:
			return x.Connected
		case !x.Paired && rssi(x) != rssi(y):
			return rssi(x) > rssi(y)
		}
		return x.Name < y.Name
	})
	if len(s.Devices) > maxDevices {
		s.Devices = s.Devices[:maxDevices]
	}
	return s
}

func rssi(d Device) int {
	if d.RSSI == nil {
		return -1000
	}
	return *d.RSSI
}

// --- orders -----------------------------------------------------------------------------------------------------

// ready: Bluetooth is there (its adapter, with m.mu); powered too when power is true.
func (m *Manager) ready(power bool) (*adapter, error) {
	if !m.read || m.snapErr != nil || m.snap.Adapter == nil {
		return nil, ErrUnavailable
	}
	if power && !m.snap.Adapter.Powered {
		return nil, ErrOff
	}
	a := *m.snap.Adapter
	return &a, nil
}

// SetOn switches Bluetooth on or off (saved). Off disconnects everything; the pairings stay.
func (m *Manager) SetOn(ctx context.Context, on bool) error {
	m.mu.Lock()
	a, err := m.ready(false)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	// Applied first and saved only if the adapter took it: a refused change must not come back by itself later
	c, cancel := context.WithTimeout(ctx, callWait)
	defer cancel()
	err = m.be.setAdapter(c, a.Path, "Powered", on)
	m.o.Log.Info("bluetooth switched", "event", "bt_power", "on", on, "ok", err == nil)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg.On = &on
	m.powerFailed, m.powerRetry = false, time.Time{}
	if !on {
		m.visibleTill, m.scanTill = time.Time{}, time.Time{}
	}
	err = m.save()
	m.mu.Unlock()
	m.Kick()
	return err
}

// Scan searches for ScanFor (again from now if one is running).
func (m *Manager) Scan(ctx context.Context) error {
	if !m.op.TryLock() {
		return ErrBusy
	}
	defer m.op.Unlock()
	m.mu.Lock()
	a, err := m.ready(true)
	if err == nil {
		m.scanTill = m.o.Now().Add(ScanFor)
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, callWait)
	defer cancel()
	if !a.Discovering {
		if err := m.be.discovery(c, a.Path, true); err != nil && dbusErrorName(err) != "org.bluez.Error.InProgress" {
			m.mu.Lock()
			m.scanTill = time.Time{}
			m.mu.Unlock()
			return err
		}
	}
	m.o.Log.Info("bluetooth search", "event", "bt_scan")
	m.Kick()
	return nil
}

// SetVisible opens (VisibleFor) or closes the window in which a device may find the encoder and pair with it (D4).
func (m *Manager) SetVisible(ctx context.Context, on bool) error {
	m.mu.Lock()
	a, err := m.ready(true)
	if err == nil && !on {
		m.visibleTill = time.Time{} // closing: the agent refuses from now on, whatever BlueZ answers
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, callWait)
	defer cancel()
	if on {
		// Our agent default again: an incoming pairing goes to the default agent, and a tool such as bluetoothctl
		// takes that place (and leaves it empty when it quits)
		if err := m.be.registerAgent(c); err != nil {
			m.o.Log.Warn("bluetooth agent not registered", "event", "bt_agent_failed", "err", err)
			return err
		}
	}
	secs := uint32(VisibleFor / time.Second)
	for _, p := range []struct {
		prop string
		v    any
	}{{"PairableTimeout", secs}, {"DiscoverableTimeout", secs}, {"Pairable", on}, {"Discoverable", on}} {
		if !on && (p.prop == "PairableTimeout" || p.prop == "DiscoverableTimeout") {
			continue
		}
		if err := m.be.setAdapter(c, a.Path, p.prop, p.v); err != nil {
			m.o.Log.Warn("bluetooth visibility not set", "event", "bt_visible_failed", "prop", p.prop, "err", err)
			if on { // half open: back to hidden (best effort), and the window stays closed (D4)
				m.be.setAdapter(c, a.Path, "Discoverable", false)
				m.be.setAdapter(c, a.Path, "Pairable", false)
			}
			return err
		}
	}
	if on {
		// The window opens for the agent only once the adapter took all of it (D4)
		m.mu.Lock()
		m.visibleTill = m.o.Now().Add(VisibleFor)
		m.mu.Unlock()
	}
	m.o.Log.Info("bluetooth visible", "event", "bt_visible", "on", on)
	m.Kick()
	return nil
}

// device: a device BlueZ knows, by address (with m.mu).
func (m *Manager) device(addr string) (rawDevice, bool) {
	for _, d := range m.snap.Devices {
		if d.Address == addr {
			return d, true
		}
	}
	return rawDevice{}, false
}

// Act starts an order on a device and returns at once (pairing takes up to ~45 s): State shows it running
// (Pending), and if it fails, why (Error).
func (m *Manager) Act(addr, action string) error {
	if !validAddr(addr) {
		return ErrInvalid
	}
	switch action {
	case ActPair, ActConnect, ActDisconnect, ActForget:
	default:
		return ErrInvalid
	}
	m.mu.Lock()
	a, err := m.ready(action != ActForget)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	d, ok := m.device(addr)
	switch {
	case !ok:
		m.mu.Unlock()
		return ErrUnknown
	case (action == ActConnect || action == ActDisconnect) && !d.Paired:
		m.mu.Unlock()
		return ErrNotPaired
	}
	if !m.op.TryLock() {
		m.mu.Unlock()
		return ErrBusy
	}
	m.pending[addr] = action
	delete(m.errs, addr)
	if action == ActPair && !d.Paired {
		m.outgoing[addr] = m.o.Now().Add(outgoingFor)
	}
	m.mu.Unlock()
	m.o.Log.Info("bluetooth order", "event", "bt_order", "device", addr, "action", action)
	go m.run(*a, d, action)
	m.Kick()
	return nil
}

func (m *Manager) run(a adapter, d rawDevice, action string) {
	defer m.op.Unlock()
	var err error
	switch action {
	case ActPair:
		err = m.pairAndConnect(a, d)
	case ActConnect:
		err = m.timed(connectWait, func(c context.Context) error { return m.be.connect(c, d.Path) })
	case ActDisconnect:
		err = m.timed(connectWait, func(c context.Context) error { return m.be.disconnect(c, d.Path) })
	case ActForget:
		err = m.timed(callWait, func(c context.Context) error { return m.be.remove(c, a.Path, d.Path) })
		if err == nil || failOf(err) == FailGone {
			err = nil
			m.mu.Lock()
			delete(m.cfg.Uses, d.Address)
			if serr := m.save(); serr != nil {
				m.o.Log.Warn("bluetooth config not saved", "event", "bt_config_save_failed", "err", serr)
			}
			m.mu.Unlock()
		}
	}
	m.mu.Lock()
	delete(m.pending, d.Address)
	delete(m.outgoing, d.Address)
	if err != nil {
		m.errs[d.Address] = failOf(err)
	}
	m.mu.Unlock()
	if err != nil {
		m.o.Log.Warn("bluetooth order failed", "event", "bt_order_failed", "device", d.Address, "action", action,
			"reason", failOf(err), "err", err)
	} else {
		m.o.Log.Info("bluetooth order done", "event", "bt_order_done", "device", d.Address, "action", action)
	}
	m.Kick()
}

func (m *Manager) timed(d time.Duration, f func(context.Context) error) error {
	c, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return f(c)
}

// pairAndConnect: stop the search (it slows the pairing down), pair, trust, give it its default use and connect.
func (m *Manager) pairAndConnect(a adapter, d rawDevice) error {
	if a.Discovering {
		_ = m.timed(callWait, func(c context.Context) error { return m.be.discovery(c, a.Path, false) })
		m.mu.Lock()
		m.scanTill = time.Time{}
		m.mu.Unlock()
	}
	if !d.Paired {
		// 🪤 Bondable while it pairs (BLUETOOTH.md §1): with the adapter not pairable the kernel turns even a pairing the
		// encoder starts into a non-bonding one, and BlueZ keeps no key (found on the board: the headphones were
		// unpaired after the adapter came back). Others still get nothing: the agent only accepts this device, and the
		// adapter is not discoverable. Back to not pairable right after, unless the window is open
		_ = m.timed(callWait, func(c context.Context) error { return m.be.setAdapter(c, a.Path, "Pairable", true) })
		err := m.timed(pairWait, func(c context.Context) error { return m.be.pair(c, d.Path) })
		m.mu.Lock()
		visible := m.o.Now().Before(m.visibleTill)
		m.mu.Unlock()
		if !visible {
			_ = m.timed(callWait, func(c context.Context) error { return m.be.setAdapter(c, a.Path, "Pairable", false) })
		}
		if err != nil && dbusErrorName(err) != "org.bluez.Error.AlreadyExists" {
			return err
		}
	}
	if err := m.timed(callWait, func(c context.Context) error { return m.be.setTrusted(c, d.Path, true) }); err != nil {
		return err
	}
	// Its use: by its profiles, read again now that it is paired (a search only gives some of them)
	c, cancel := context.WithTimeout(context.Background(), callWait)
	snap, err := m.be.snapshot(c)
	cancel()
	uuids := d.UUIDs
	if err == nil {
		for _, x := range snap.Devices {
			if x.Address == d.Address {
				uuids = x.UUIDs
			}
		}
	}
	m.mu.Lock()
	if _, ok := m.cfg.Uses[d.Address]; !ok {
		if u := defaultUse(uses(uuids)); u != "" && len(m.cfg.Uses) < maxSaved {
			m.cfg.Uses[d.Address] = u
			if err := m.save(); err != nil {
				m.o.Log.Warn("bluetooth config not saved", "event", "bt_config_save_failed", "err", err)
			}
		}
	}
	m.mu.Unlock()
	return m.timed(connectWait, func(c context.Context) error { return m.be.connect(c, d.Path) })
}

// SetUse changes what a paired device is used for (one of its uses). The engine follows the file.
func (m *Manager) SetUse(addr, use string) error {
	if !validAddr(addr) || !validUse(use) {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.ready(false); err != nil {
		return err
	}
	d, ok := m.device(addr)
	if !ok {
		return ErrUnknown
	}
	if !d.Paired {
		return ErrNotPaired
	}
	if u := uses(d.UUIDs); len(u) > 0 && !slices.Contains(u, use) {
		return ErrInvalid
	}
	if m.cfg.Uses[addr] == use {
		return nil
	}
	m.cfg.Uses[addr] = use
	if err := m.save(); err != nil {
		return err
	}
	m.o.Log.Info("bluetooth use", "event", "bt_use", "device", addr, "use", use)
	m.Kick()
	return nil
}
