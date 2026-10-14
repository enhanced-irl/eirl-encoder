package links

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcheck"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

// Internet: the internet check of the links behind a modem or a phone (MODEMS.md §5): a SIM without credit or
// with a wrong APN gives an IP and a route but no internet, and must leave the SRTLA share like a Wi-Fi behind
// a captive portal (WIFI.md §13). The same check (HTTPS to the stream's relay, from the link's own IP, the
// certificate verified) and the same hysteresis as the Wi-Fi client.
//
// It is the ONE writer of the no-internet file the engine reads: its own results plus what others report with
// Set (the Wi-Fi client's monitor, network.MonitorWiFi). Two writers would erase each other's lines.
//
// Cost: one HTTPS HEAD per link every 30 s (~5 KB: ~15 MB a day per modem).
type Internet struct {
	URL   func() string // where to check ("" = nowhere: nothing changes)
	Path  string        // /run/eirl-encoder/uplinks-nointernet
	Write func(path string, data []byte, mode os.FileMode) error
	Every time.Duration // default 30 s
	// Streak results in a row saying the opposite, and (except the first time) Dwell since the last change,
	// before a link changes: each change reloads srtla_send. Defaults 2 and 60 s, like the Wi-Fi's.
	Streak int
	Dwell  time.Duration
	Now    func() time.Time
	Log    func(msg string, args ...any)

	check func(ctx context.Context, url, ip string) bool // tests

	mu       sync.Mutex
	links    map[string]*netcheck.State
	external map[string]bool
	wmu      sync.Mutex // one flush at a time: compute, compare and write (Set and Round run in two goroutines)
	written  *string
}

// Set: another monitor (the Wi-Fi client's) says whether its link is without internet. Written at once.
func (n *Internet) Set(iface string, off bool) error {
	n.mu.Lock()
	if n.external == nil {
		n.external = map[string]bool{}
	}
	if off {
		n.external[iface] = true
	} else {
		delete(n.external, iface)
	}
	n.mu.Unlock()
	return n.flush()
}

// Run: a round every Every until ctx ends. targets: the links to check now (modems and phones).
func (n *Internet) Run(ctx context.Context, targets func() []uplinks.Addr) {
	every := n.Every
	if every <= 0 {
		every = 30 * time.Second
	}
	for {
		n.Round(ctx, targets())
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Round: one check of every target (one after another: a modem without data takes its timeout, the others
// wait; 4 modems × 8 s at worst, well within 30 s). Links that are gone are forgotten.
func (n *Internet) Round(ctx context.Context, targets []uplinks.Addr) {
	url := ""
	if n.URL != nil {
		url = n.URL()
	}
	if url == "" {
		// Nowhere to check against (a custom destination without the cloud): what was known is forgotten,
		// or a link judged without internet would stay out of the share for good
		n.mu.Lock()
		n.links = nil
		n.mu.Unlock()
		n.flushLogged()
		return
	}
	keep := map[string]bool{}
	for _, t := range targets {
		keep[t.Name] = true
		ok := n.checker()(ctx, url, t.IP)
		if ctx.Err() != nil {
			return // the app is stopping: it does not count
		}
		n.record(t.Name, ok)
	}
	n.mu.Lock()
	for name := range n.links {
		if !keep[name] {
			delete(n.links, name)
		}
	}
	n.mu.Unlock()
	n.flushLogged()
}

func (n *Internet) flushLogged() {
	if err := n.flush(); err != nil && n.Log != nil {
		n.Log("links: could not write the no-internet list (will retry)", "err", err)
	}
}

// record: one result for a link, with the shared hysteresis (internal/netcheck, the Wi-Fi client's too).
func (n *Internet) record(name string, ok bool) {
	streak, dwell := n.Streak, n.Dwell
	if streak <= 0 {
		streak = 2
	}
	if dwell <= 0 {
		dwell = 60 * time.Second
	}
	now := time.Now()
	if n.Now != nil {
		now = n.Now()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.links == nil {
		n.links = map[string]*netcheck.State{}
	}
	s := n.links[name]
	if s == nil {
		s = &netcheck.State{}
		n.links[name] = s
	}
	was := s.Off()
	s.Record(ok, now, streak, dwell)
	if s.Off() != was && n.Log != nil {
		n.Log("link internet", "link", name, "noInternet", s.Off())
	}
}

// Off: the links without internet now (sorted).
func (n *Internet) Off() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for name, s := range n.links {
		if s.Off() {
			out = append(out, name)
		}
	}
	for name := range n.external {
		if _, own := n.links[name]; !own {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// flush writes the file when its content changes (and the first time). Under wmu from start to end: two flushes
// in a row (Round and the Wi-Fi's Set) must not write their contents in the wrong order.
func (n *Internet) flush() error {
	if n.Path == "" || n.Write == nil {
		return nil
	}
	n.wmu.Lock()
	defer n.wmu.Unlock()
	off := n.Off()
	body := ""
	if len(off) > 0 {
		body = strings.Join(off, "\n") + "\n"
	}
	n.mu.Lock()
	same := n.written != nil && *n.written == body
	n.mu.Unlock()
	if same {
		return nil
	}
	if err := n.Write(n.Path, []byte(body), 0o640); err != nil {
		return err
	}
	n.mu.Lock()
	n.written = &body
	n.mu.Unlock()
	return nil
}

func (n *Internet) checker() func(ctx context.Context, url, ip string) bool {
	if n.check != nil {
		return n.check
	}
	return netcheck.HTTPS
}

// Targets: the links the internet check covers: modems and USB links (phones), not the Wi-Fi client (it has its
// own monitor) nor the cable (unchanged behaviour: a LAN for cameras is not judged by the relay).
func Targets(snap Snapshot) []uplinks.Addr {
	var out []uplinks.Addr
	for _, u := range snap.Uplinks {
		if (u.Kind == "modem" || u.Kind == "usb") && !u.Conflict {
			out = append(out, uplinks.Addr{Name: u.Name, IP: u.IP})
		}
	}
	return out
}
