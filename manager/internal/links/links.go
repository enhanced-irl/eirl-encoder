// Package links joins, every 2 s, everything known about the encoder's internet connections (MODEMS.md §4):
// the dispatcher's links (internal/uplinks), the modems (internal/modem) and the Wi-Fi client's signal, and
// judges each one with internal/linkhealth (with its hysteresis). One round feeds:
//   - GET /status (uplinks[] with health, modems[]), from the cached snapshot: the SSE and the screen's
//     socket do not each reread the traffic counters;
//   - /run/eirl-encoder/links.json for the engine, which paints the dots of the touch screen's header
//     (no IPs, no identifiers: the engine runs as another user).
package links

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/linkhealth"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/modem"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

// Link: one connection as the screen draws it (links.json). Modems without a usable link (no SIM, PIN,
// searching) are there too, red, so that the screen says why.
type Link struct {
	Name  string `json:"name"` // interface; a modem without one: its id
	Kind  string `json:"kind"` // ethernet | usb | wifi | modem
	Label string `json:"label,omitempty"`
	Tech  string `json:"tech,omitempty"`
	// Bars: 0-4 for a signal icon (modems and Wi-Fi); absent for cables
	Bars      *int   `json:"bars,omitempty"`
	Health    string `json:"health"`
	Code      string `json:"code"`
	Available bool   `json:"available"`
	InUse     bool   `json:"inUse"`
	Disabled  bool   `json:"disabled"`
	TxKbps    int    `json:"txKbps"`
}

// File: links.json. V changes only with an incompatible format; At says how fresh it is (the engine shows
// the dots as unknown when it is older than StaleAfter: the app stopped).
type File struct {
	V         int       `json:"v"`
	At        time.Time `json:"at"`
	Streaming bool      `json:"streaming"`
	Links     []Link    `json:"links"`
}

const (
	FileVersion = 1
	// StaleAfter: a links.json older than this is not trusted (it is rewritten at least every rewriteEvery)
	StaleAfter   = 15 * time.Second
	rewriteEvery = 5 * time.Second
)

// Snapshot: one round.
type Snapshot struct {
	At        time.Time
	Streaming bool
	Uplinks   []uplinks.Uplink
	Modems    []modem.Modem
	Links     []Link
}

// Service: the 2-second round.
type Service struct {
	Uplinks *uplinks.Reader
	Modems  *modem.Manager // nil: no modems
	// WiFi: the Wi-Fi client's interface and its signal 0-100 (nil signal = not connected or unknown)
	WiFi func(ctx context.Context) (iface string, signal *int)
	// Streaming: the running stream goes over SRTLA (only then the in-use file counts)
	Streaming func() bool
	Path      string // /run/eirl-encoder/links.json ("" = not written)
	Write     func(path string, data []byte, mode os.FileMode) error
	Every     time.Duration // default 2 s
	Now       func() time.Time
	Health    linkhealth.Tracker

	mu        sync.Mutex
	cur       Snapshot
	written   []byte
	writtenAt time.Time
}

// Current: the latest round (zero before the first one).
func (s *Service) Current() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Run: a round every Every until ctx ends.
func (s *Service) Run(ctx context.Context) {
	every := s.Every
	if every <= 0 {
		every = 2 * time.Second
	}
	for {
		s.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

var kindOrder = map[string]int{"ethernet": 0, "wifi": 1, "modem": 2, "usb": 3}

// Refresh: one round (Run calls it; the tests too).
func (s *Service) Refresh(ctx context.Context) Snapshot {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	streaming := s.Streaming != nil && s.Streaming()
	var ups []uplinks.Uplink
	if s.Uplinks != nil {
		ups = s.Uplinks.Read(now, streaming)
	}
	var mods []modem.Modem
	if s.Modems != nil {
		mods = s.Modems.Snapshot()
	}
	wifiIface, wifiSig := "", (*int)(nil)
	if s.WiFi != nil {
		wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		wifiIface, wifiSig = s.WiFi(wctx)
		cancel()
	}
	byIface := modem.ByIface(mods)
	seen := map[string]bool{}
	keys := map[string]bool{}
	var out []Link
	for i := range ups {
		u := &ups[i]
		in := linkhealth.Input{Kind: u.Kind, Available: true, Disabled: u.Disabled, Conflict: u.Conflict,
			NoInternet: u.NoInternet, Streaming: streaming, InUse: u.InUse}
		l := Link{Name: u.Name, Available: true, InUse: u.InUse, Disabled: u.Disabled, TxKbps: u.TxKbps}
		if md, ok := byIface[u.Name]; ok {
			seen[md.ID] = true
			u.Kind, u.ModemID, u.Label = "modem", md.ID, md.Operator
			mh := md.Health()
			in.Kind, in.Modem = "modem", &mh
			l.Tech, l.Bars = md.Tech, bars(md)
		} else if u.Name == wifiIface && wifiIface != "" {
			in.WiFiPercent = wifiSig
			if wifiSig != nil {
				b := linkhealth.WiFiBars(*wifiSig)
				l.Bars = &b
			}
		}
		r := s.Health.Update(u.Name, linkhealth.Judge(in), now)
		keys[u.Name] = true
		u.Health, u.HealthCode = string(r.Level), r.Code
		l.Kind, l.Label, l.Health, l.Code = u.Kind, u.Label, u.Health, u.HealthCode
		out = append(out, l)
	}
	// Modems without a usable link: red (or grey if switched off), with the reason
	var off map[string]bool
	for _, md := range mods {
		if seen[md.ID] {
			continue
		}
		if off == nil {
			off = disabledSet(s.Uplinks)
		}
		name := md.Iface
		if name == "" {
			name = md.ID
		}
		mh := md.Health()
		in := linkhealth.Input{Kind: "modem", Disabled: off[name], Modem: &mh}
		r := s.Health.Update(name, linkhealth.Judge(in), now)
		keys[name] = true
		out = append(out, Link{Name: name, Kind: "modem", Label: md.Operator, Tech: md.Tech, Bars: bars(md),
			Health: string(r.Level), Code: r.Code, Disabled: off[name]})
	}
	s.Health.Keep(keys)
	sort.SliceStable(out, func(i, j int) bool {
		if kindOrder[out[i].Kind] != kindOrder[out[j].Kind] {
			return kindOrder[out[i].Kind] < kindOrder[out[j].Kind]
		}
		return out[i].Name < out[j].Name
	})
	snap := Snapshot{At: now, Streaming: streaming, Uplinks: ups, Modems: mods, Links: out}
	s.mu.Lock()
	s.cur = snap
	s.mu.Unlock()
	s.write(snap, now)
	return snap
}

// bars: the signal icon of a modem; nil when it gives nothing (locked, no SIM).
func bars(md modem.Modem) *int {
	r := md.Radio()
	if r.RSRP == nil && r.RSSI == nil && r.Bars == nil && r.Quality == nil {
		return nil
	}
	b := linkhealth.Bars(r)
	return &b
}

func disabledSet(r *uplinks.Reader) map[string]bool {
	out := map[string]bool{}
	if r == nil {
		return out
	}
	names, _ := uplinks.ReadDisabled(r.Disabled)
	for _, n := range names {
		out[n] = true
	}
	return out
}

// write: links.json when what it says changes, and every rewriteEvery anyway (its date says the app lives).
func (s *Service) write(snap Snapshot, now time.Time) {
	if s.Path == "" || s.Write == nil {
		return
	}
	links := snap.Links
	if links == nil {
		links = []Link{}
	}
	body, err := json.Marshal(File{V: FileVersion, Streaming: snap.Streaming, Links: links})
	if err != nil {
		return
	}
	s.mu.Lock()
	same := bytes.Equal(body, s.written) && now.Sub(s.writtenAt) < rewriteEvery
	s.mu.Unlock()
	if same {
		return
	}
	full, err := json.Marshal(File{V: FileVersion, At: now.UTC(), Streaming: snap.Streaming, Links: links})
	if err != nil {
		return
	}
	if err := s.Write(s.Path, append(full, '\n'), 0o640); err != nil {
		return // next round
	}
	s.mu.Lock()
	s.written, s.writtenAt = body, now
	s.mu.Unlock()
}
