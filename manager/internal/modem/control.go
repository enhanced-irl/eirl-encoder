package modem

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"time"
)

// Controller: what a backend can change on its modems (MODEMS.md §7).
type Controller interface {
	Apply(ctx context.Context, md Modem, c Config) error
	Unlock(ctx context.Context, md Modem, c Config, pin string, remove bool) error
	Reset(ctx context.Context, md Modem) error
}

// subnetMover: a backend whose modems have a LAN of their own (HiLink).
type subnetMover interface {
	MoveSubnet(ctx context.Context, md Modem, third int) error
}

// Change: what an order changes (nil = untouched).
type Change struct {
	Alias, APN, Mode *string
	Roaming          *bool
}

var (
	// ErrUnknown: no modem with that id now
	ErrUnknown = errors.New("modem: unknown")
	// ErrBusy: another change to the modems is running
	ErrBusy = errors.New("modem: busy")
	// ErrCannot: that modem does not let that be done from here
	ErrCannot = errors.New("modem: not supported")
	rePIN     = regexp.MustCompile(`^[0-9]{4,8}$`)
)

// ValidPIN: 4 to 8 digits.
func ValidPIN(s string) bool { return rePIN.MatchString(s) }

// opTimeout: one change, including a HiLink's encrypted PIN (four requests) or NetworkManager's profile.
const opTimeout = 20 * time.Second

func (m *Manager) find(id string) (Modem, Backend, error) {
	m.mu.Lock()
	var md Modem
	found := false
	for _, x := range m.snap {
		if x.ID == id {
			md, found = x, true
			break
		}
	}
	m.mu.Unlock()
	if !found {
		return Modem{}, nil, ErrUnknown
	}
	for _, b := range m.Backends {
		if b.Name() == md.Backend {
			return md, b, nil
		}
	}
	return Modem{}, nil, ErrUnknown
}

// lock: one change at a time (a second one waits for none: it is refused).
func (m *Manager) lock() bool { return m.opMu.TryLock() }

// Configure applies a change to one modem and keeps it: first on the modem (APN, roaming, mode), then in modems.json.
// The alias is only the app's. What the order names is applied even if modems.json already says it: the modem may not
// (a HiLink whose roaming was turned on in its own page, a profile changed by hand).
func (m *Manager) Configure(ctx context.Context, id string, ch Change) (Modem, error) {
	md, b, err := m.find(id)
	if err != nil {
		return Modem{}, err
	}
	if m.Store == nil {
		return Modem{}, ErrCannot
	}
	if !m.lock() {
		return Modem{}, ErrBusy
	}
	defer m.opMu.Unlock()
	cur := m.Store.Get(id)
	next := cur
	if ch.Alias != nil {
		next.Alias = *ch.Alias
	}
	if ch.APN != nil {
		if !md.can("apn") {
			return Modem{}, ErrCannot
		}
		next.APN = *ch.APN
	}
	if ch.Roaming != nil {
		if !md.can("roaming") {
			return Modem{}, ErrCannot
		}
		next.Roaming = *ch.Roaming
	}
	if ch.Mode != nil {
		if !md.can("mode") || *ch.Mode != "auto" && !contains(md.Modes, *ch.Mode) {
			return Modem{}, ErrCannot
		}
		next.Mode = orAuto(*ch.Mode) // "auto" is kept as such: "" is a modem nobody set, left as it is
	}
	if !next.Valid() {
		return Modem{}, ErrConfig
	}
	applied := false
	if ch.APN != nil || ch.Roaming != nil || ch.Mode != nil {
		c, ok := b.(Controller)
		if !ok {
			return Modem{}, ErrCannot
		}
		cctx, cancel := context.WithTimeout(ctx, opTimeout)
		err := c.Apply(cctx, md, next)
		cancel()
		if err != nil {
			return Modem{}, err
		}
		applied = true
		md.Roaming = next.Roaming // applied: shown at once, confirmed by the next reading
		if next.Mode != "" {
			md.Mode = next.Mode
		}
	}
	saved, err := m.Store.Update(id, func(c *Config) { *c = next })
	if err != nil {
		return Modem{}, err
	}
	if applied { // only then: an alias alone must not mark a profile that failed as written (reconcile retries it)
		m.setApplied(md, saved)
	}
	md = m.decorate(md, saved)
	m.replace(md)
	return md, nil
}

// Unlock enters the SIM's PIN (and keeps it where the backend can: NetworkManager's profile), or turns it off.
func (m *Manager) Unlock(ctx context.Context, id, pin string, remove bool) error {
	if !ValidPIN(pin) {
		return ErrConfig
	}
	md, b, err := m.find(id)
	if err != nil {
		return err
	}
	c, ok := b.(Controller)
	if !ok || !md.can("pin") {
		return ErrCannot
	}
	if !m.lock() {
		return ErrBusy
	}
	defer m.opMu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	cfg := Config{}
	if m.Store != nil {
		cfg = m.Store.Get(id)
	}
	return c.Unlock(cctx, md, cfg, pin, remove)
}

// Reset starts a modem again.
func (m *Manager) Reset(ctx context.Context, id string) error {
	md, b, err := m.find(id)
	if err != nil {
		return err
	}
	c, ok := b.(Controller)
	if !ok || !md.can("reset") {
		return ErrCannot
	}
	if !m.lock() {
		return ErrBusy
	}
	defer m.opMu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return c.Reset(cctx, md)
}

// SubnetTarget: the modem whose LAN MoveSubnet(id) really changes. Two identical HiLink sticks share an address, so
// an order for the one that cannot be reached goes to its reachable twin (the one carrying traffic): its link is the
// one a move cuts for a few seconds. "" if there is no such modem.
func (m *Manager) SubnetTarget(id string) string {
	md, _, err := m.find(id)
	if err != nil {
		return ""
	}
	if md.twinOf != "" {
		return md.twinOf
	}
	return md.ID
}

// MoveSubnet moves a HiLink's LAN to the first 192.168.X.0/24 (X from 9) no interface of the board uses. For a stick
// that cannot be reached (it has a twin), its twin's: either separates them (SubnetTarget).
func (m *Manager) MoveSubnet(ctx context.Context, id string) error {
	md, b, err := m.find(id)
	if err != nil {
		return err
	}
	mv, ok := b.(subnetMover)
	if !ok || !md.can("subnet") {
		return ErrCannot
	}
	third := freeSubnet(m.addrs())
	if third == 0 {
		return ErrCannot
	}
	if !m.lock() {
		return ErrBusy
	}
	defer m.opMu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return mv.MoveSubnet(cctx, md, third)
}

// addrs: the board's IPv4 networks now (tests: Addrs).
func (m *Manager) addrs() []net.IP {
	if m.Addrs != nil {
		return m.Addrs()
	}
	var out []net.IP
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		as, _ := i.Addrs()
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				out = append(out, n.IP.To4())
			}
		}
	}
	return out
}

// freeSubnet: the first X from 9 to 254 with no address of the board in 192.168.X.0/24 (0: none).
func freeSubnet(addrs []net.IP) int {
	used := map[int]bool{}
	for _, a := range addrs {
		if a4 := a.To4(); a4 != nil && a4[0] == 192 && a4[1] == 168 {
			used[int(a4[2])] = true
		}
	}
	for x := 9; x <= 254; x++ {
		if !used[x] {
			return x
		}
	}
	return 0
}

// --- configuration in the snapshot, and the profiles of new modems ------------------------------------------------

// decorate: the stored configuration in the modem's view.
func (m *Manager) decorate(md Modem, c Config) Modem {
	md.Alias, md.APN = c.Alias, c.APN
	if md.Backend == "mm" {
		md.Roaming = c.Roaming // what its NetworkManager profile says (the app wrote it)
		if c.APN == "" {
			md.APNAuto = AutoAPN(m.Providers, md.netCode, md.simName)
		}
	}
	return md
}

// replace: one modem in the snapshot (after a change, without waiting for the next round).
func (m *Manager) replace(md Modem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.snap {
		if m.snap[i].ID == md.ID {
			m.snap[i] = md
		}
	}
}

// fingerprint: what a ModemManager modem's profile was made from.
func fingerprint(md Modem, c Config) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%t|%s", md.devID, md.simID, md.netCode, md.simName, c.APN, c.Roaming, c.Mode)
}

func (m *Manager) setApplied(md Modem, c Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.applied == nil {
		m.applied = map[string]applied{}
	}
	m.applied[md.ID] = applied{fp: fingerprint(md, c), at: time.Now()}
}

type applied struct {
	fp     string
	at     time.Time
	failed bool
}

// retryEvery: a profile that could not be written is tried again after this long.
const retryEvery = time.Minute

// reconcile: every ModemManager modem gets its NetworkManager profile (without one it never connects) with the
// stored configuration, or the defaults: automatic APN, no roaming. Once per modem and configuration; in the
// background, never two at once, and never while a user's change runs. A profile rewrite reconnects the modem, so a
// modem carrying a stream (Hold) waits, and nothing is written when modems.json cannot be read (its defaults would
// overwrite the user's APN).
func (m *Manager) reconcile(ctx context.Context, list []Modem) {
	if m.Store == nil {
		return
	}
	stored, err := m.Store.All()
	if err != nil {
		return
	}
	held := map[string]bool{} // asked before taking m.mu: Hold reads the links service, which reads this manager
	for _, md := range list {
		if m.Hold != nil && md.Backend == "mm" {
			held[md.ID] = m.Hold(md.ID)
		}
	}
	var todo []Modem
	m.mu.Lock()
	for _, md := range list {
		if md.Backend != "mm" || md.devID == "" || md.SIM == "missing" || held[md.ID] {
			continue
		}
		fp := fingerprint(md, stored[md.ID])
		a, ok := m.applied[md.ID]
		if ok && a.fp == fp && (!a.failed || time.Since(a.at) < retryEvery) {
			continue
		}
		todo = append(todo, md)
	}
	busy := m.reconciling
	if len(todo) > 0 && !busy {
		m.reconciling = true
	}
	m.mu.Unlock()
	if len(todo) == 0 || busy {
		return
	}
	go func() {
		defer func() {
			m.mu.Lock()
			m.reconciling = false
			m.mu.Unlock()
		}()
		for _, md := range todo {
			if !m.lock() {
				return // a user's change is running: next round
			}
			_, b, err := m.find(md.ID)
			c, ok := b.(Controller)
			all, rerr := m.Store.All()
			if err == nil && ok && rerr == nil {
				cfg := all[md.ID]
				cctx, cancel := context.WithTimeout(ctx, opTimeout)
				err = c.Apply(cctx, md, cfg)
				cancel()
				m.mu.Lock()
				if m.applied == nil {
					m.applied = map[string]applied{}
				}
				m.applied[md.ID] = applied{fp: fingerprint(md, cfg), at: time.Now(), failed: err != nil}
				m.mu.Unlock()
				if err != nil && m.Log != nil {
					m.Log("modems: could not set up the modem's profile (will retry)", "modem", md.ID, "err", err)
				}
			}
			m.opMu.Unlock()
		}
	}()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func orAuto(s string) string {
	if s == "" {
		return "auto"
	}
	return s
}
