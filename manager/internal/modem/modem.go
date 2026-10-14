// Package modem watches the cellular modems plugged into the encoder (MODEMS.md): their signal, SIM,
// network and technology. Two backends:
//   - ModemManager over D-Bus, for real modems (QMI/MBIM: Quectel, Sierra, Telit…): mm.go;
//   - the web API of HiLink modems (a router inside the stick, seen as an Ethernet card): hilink.go.
//
// Read-only for now (phase M1): configuration (PIN, APN, roaming, network mode, reset) comes in M4.
// Never exposes the IMEI, ICCID or IMSI whole: they are personal data (MODEMS.md §7, D-M10).
package modem

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/linkhealth"
)

// Modem: what is known of one modem.
type Modem struct {
	// ID: stable across replugs and reboots (a hash of the IMEI, or of the MAC for HiLink); never the IMEI
	ID      string `json:"id"`
	Backend string `json:"backend"` // mm | hilink
	// Iface: its network interface now (wwan0, enx…); empty while it has none
	Iface        string `json:"iface,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	// State: failed | locked | disabled | enabling | searching | registered | connecting | connected | disconnected | unknown
	State string `json:"state"`
	// SIM: ready | missing | pin | puk | error | unknown
	SIM string `json:"sim"`
	// Registration: home | roaming | searching | denied | idle | unknown
	Registration string `json:"registration,omitempty"`
	Operator     string `json:"operator,omitempty"`
	Tech         string `json:"tech,omitempty"` // 2g | 3g | 4g | 5g-nsa | 5g
	Signal       Signal `json:"signal"`
	// The last 4 digits only, to tell modems and SIMs apart
	IMEITail  string `json:"imeiTail,omitempty"`
	ICCIDTail string `json:"iccidTail,omitempty"`
}

// Signal: every field optional (nil = not reported). dBm for RSRP and RSSI, dB for RSRQ and SINR.
type Signal struct {
	RSRP    *float64 `json:"rsrp,omitempty"`
	RSRQ    *float64 `json:"rsrq,omitempty"`
	SINR    *float64 `json:"sinr,omitempty"`
	RSSI    *float64 `json:"rssi,omitempty"`
	Bars    *int     `json:"bars,omitempty"`    // 0-5, HiLink
	Quality *int     `json:"quality,omitempty"` // 0-100, ModemManager
}

// Health: the modem as linkhealth needs it.
func (m Modem) Health() linkhealth.Modem {
	return linkhealth.Modem{State: m.State, SIM: m.SIM, Registration: m.Registration, Radio: m.Radio()}
}

// Radio: the signal as linkhealth needs it.
func (m Modem) Radio() linkhealth.Radio {
	return linkhealth.Radio{Tech: m.Tech, RSRP: m.Signal.RSRP, RSRQ: m.Signal.RSRQ, SINR: m.Signal.SINR,
		RSSI: m.Signal.RSSI, Bars: m.Signal.Bars, Quality: m.Signal.Quality}
}

// Link: an available internet link (from the dispatcher's list): where to look for HiLink modems.
type Link struct {
	Name string
	IP   string
}

// Backend: one way of finding modems. List must return quickly (it runs every few seconds) and treat
// "this backend is not there" (ModemManager not running) as no modems, not as an error.
type Backend interface {
	Name() string
	List(ctx context.Context, links []Link) ([]Modem, error)
}

// backendDeadline: what one backend's List may take (a HiLink: the session request and the readings, a timeout
// each at worst; ModemManager: a few D-Bus calls).
const backendDeadline = 5 * time.Second

// Manager polls the backends and keeps the latest list.
type Manager struct {
	Backends []Backend
	// Links: the available links now (HiLink modems are looked for among them)
	Links func() []Link
	Every time.Duration // default 5 s
	// Log: errors, once per backend until it works again (nil = silent)
	Log func(msg string, args ...any)

	mu     sync.Mutex
	snap   []Modem
	at     time.Time
	failed map[string]string
}

// Snapshot: the latest list (a copy), sorted by interface and id.
func (m *Manager) Snapshot() []Modem {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Modem(nil), m.snap...)
}

// Run polls until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	every := m.Every
	if every <= 0 {
		every = 5 * time.Second
	}
	for {
		m.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Refresh: one round over every backend. A backend that fails keeps what it gave last time (a modem
// does not vanish from the screen because ModemManager took long once).
func (m *Manager) Refresh(ctx context.Context) {
	var links []Link
	if m.Links != nil {
		links = m.Links()
	}
	m.mu.Lock()
	prev := map[string][]Modem{}
	for _, md := range m.snap {
		prev[md.Backend] = append(prev[md.Backend], md)
	}
	m.mu.Unlock()
	var out []Modem
	for _, b := range m.Backends {
		cctx, cancel := context.WithTimeout(ctx, backendDeadline)
		list, err := b.List(cctx, links)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			m.report(b.Name(), err.Error())
			out = append(out, prev[b.Name()]...)
			continue
		}
		m.report(b.Name(), "")
		out = append(out, list...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Iface != out[j].Iface {
			return out[i].Iface < out[j].Iface
		}
		return out[i].ID < out[j].ID
	})
	m.mu.Lock()
	m.snap, m.at = out, time.Now()
	m.mu.Unlock()
}

func (m *Manager) report(backend, err string) {
	m.mu.Lock()
	if m.failed == nil {
		m.failed = map[string]string{}
	}
	was := m.failed[backend]
	m.failed[backend] = err
	m.mu.Unlock()
	if m.Log == nil || was == err {
		return
	}
	if err == "" {
		m.Log("modems: backend back", "backend", backend)
	} else {
		m.Log("modems: backend failed", "backend", backend, "err", err)
	}
}

// ByIface: the modems by their network interface (those that have one).
func ByIface(list []Modem) map[string]Modem {
	out := map[string]Modem{}
	for _, md := range list {
		if md.Iface != "" {
			out[md.Iface] = md
		}
	}
	return out
}

// idKey: this device's secret for modem ids (SetIDKey). An IMEI is 8 digits that say the model, 6 of serial and
// a check digit, and imeiTail shows 4 of them: a hash with a public salt would be undone by trying ~1,000
// candidates. With a key that never leaves the board, the id says nothing.
var (
	idMu  sync.RWMutex
	idKey []byte
)

// SetIDKey: the key for modem ids (LoadIDKey reads or creates it). Without one (tests), a fixed salt.
func SetIDKey(k []byte) {
	idMu.Lock()
	idKey = append([]byte(nil), k...)
	idMu.Unlock()
}

// LoadIDKey: the key in path (32 bytes, hex), created at random the first time. Losing it only changes the ids
// (the modems' names and switches are keyed by them: they would be set again).
func LoadIDKey(path string, read func(path string) ([]byte, error), write func(path string, data []byte, mode os.FileMode) error) ([]byte, error) {
	if b, err := read(path); err == nil {
		if k, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(k) == 32 {
			return k, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := write(path, []byte(hex.EncodeToString(k)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// stableID: "m-" or "h-" plus 12 hex of an HMAC of the IMEI (or MAC) with this device's key: stable across
// replugs and reboots, short, and it cannot be undone into the IMEI.
func stableID(prefix, secret string) string {
	idMu.RLock()
	key := idKey
	idMu.RUnlock()
	if len(key) == 0 {
		key = []byte("eirl-modem")
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.ToLower(strings.TrimSpace(secret))))
	return prefix + hex.EncodeToString(m.Sum(nil)[:6])
}

// tail: the last 4 characters of an identifier (empty if it is too short to mean anything).
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return ""
	}
	return s[len(s)-4:]
}

func ptrF(v float64) *float64 { return &v }
func ptrI(v int) *int         { return &v }
