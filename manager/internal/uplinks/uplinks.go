// Package uplinks: internet links for SRTLA (see SRTLA.md).
//
// Three files, all "one interface per line" and read as allow-listed data:
//   - /run/eirl-uplinks/available: "<interface> <ipv4>" of every link with internet access. Written
//     by the NetworkManager dispatcher (root), which sets up source-based routing for each one.
//   - /run/eirl-engine/uplinks: "<interface> <ipv4>" of the links the running stream uses (internal/engine/output).
//   - /etc/eirl-encoder/uplinks-disabled: interfaces not to use. Written by the app; the engine
//     re-reads it every 2 s and reloads srtla_send without cutting the stream.
//
// Each link's traffic comes from /sys/class/net/<if>/statistics (srtla_send is not touched).
package uplinks

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

// Uplink: what the API shows for each link.
type Uplink struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Kind     string `json:"kind"`     // ethernet | usb | wifi
	InUse    bool   `json:"inUse"`    // the running stream uses it
	Disabled bool   `json:"disabled"` // disabled from the app
	// Conflict: same IP as another link (two factory-default HiLink modems both hand out 192.168.8.100).
	// Source routing cannot tell them apart: the dispatcher only routes the first one and the stream
	// does not use it.
	Conflict bool `json:"conflict,omitempty"`
	// NoInternet: the Wi-Fi client is connected to a network without internet access (a captive portal):
	// left out of the SRTLA share until it comes back (WIFI.md §13). The app decides it, not the user
	NoInternet bool `json:"noInternet,omitempty"`
	TxKbps     int  `json:"txKbps"` // upload since the previous read (0 on the first)
	RxKbps     int  `json:"rxKbps"`
}

var reName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// MaxDisabled: plenty for the board's 4 USB ports plus Ethernet and Wi-Fi.
const MaxDisabled = 16

// ValidName: allow-listed Linux interface name (IFNAMSIZ-1).
func ValidName(n string) bool { return reName.MatchString(n) }

type Reader struct {
	Available string // /run/eirl-uplinks/available
	InUse     string // /run/eirl-engine/uplinks
	Disabled  string // /etc/eirl-encoder/uplinks-disabled
	// NoInternet: /run/eirl-encoder/uplinks-nointernet (written by the app; the engine leaves them out of the share)
	NoInternet string
	SysNet     string // /sys/class/net

	mu   sync.Mutex
	prev map[string]counters

	wmu sync.Mutex // serializes SetDisabled (read, change and write the file)
}

type counters struct {
	tx, rx uint64
	at     time.Time
}

// Read returns the available links (nil if no dispatcher is installed). inUse: whether the running
// stream goes over SRTLA (otherwise /run/eirl-engine/uplinks is from another run and does not count).
func (r *Reader) Read(now time.Time, inUse bool) []Uplink {
	avail, err := readPairs(r.Available)
	if err != nil {
		return nil
	}
	used := map[string]bool{}
	if inUse {
		pairs, _ := readPairs(r.InUse)
		for _, p := range pairs {
			used[p.name] = true
		}
	}
	off := map[string]bool{}
	names, _ := ReadDisabled(r.Disabled)
	for _, n := range names {
		off[n] = true
	}
	nonet := map[string]bool{}
	if r.NoInternet != "" {
		names, _ := ReadDisabled(r.NoInternet) // same format: one allow-listed name per line
		for _, n := range names {
			nonet[n] = true
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	next := map[string]counters{}
	out := make([]Uplink, 0, len(avail))
	for _, p := range avail {
		u := Uplink{Name: p.name, IP: p.ip, Kind: r.kind(p.name), InUse: used[p.name], Disabled: off[p.name], Conflict: p.conflict,
			NoInternet: nonet[p.name]}
		c, ok := r.counters(p.name, now)
		if ok {
			next[p.name] = c
			if pc, had := r.prev[p.name]; had && c.at.After(pc.at) && c.tx >= pc.tx && c.rx >= pc.rx {
				secs := c.at.Sub(pc.at).Seconds()
				u.TxKbps = int(float64(c.tx-pc.tx) * 8 / 1000 / secs)
				u.RxKbps = int(float64(c.rx-pc.rx) * 8 / 1000 / secs)
			}
		}
		out = append(out, u)
	}
	r.prev = next
	return out
}

// AvailableNames: names of the available links (only reads the dispatcher's file, in tmpfs).
func (r *Reader) AvailableNames() []string {
	pairs, _ := readPairs(r.Available)
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p.name)
	}
	return out
}

func (r *Reader) counters(name string, now time.Time) (counters, bool) {
	tx, e1 := readUint(filepath.Join(r.SysNet, name, "statistics", "tx_bytes"))
	rx, e2 := readUint(filepath.Join(r.SysNet, name, "statistics", "rx_bytes"))
	return counters{tx: tx, rx: rx, at: now}, e1 == nil && e2 == nil
}

func (r *Reader) kind(name string) string {
	d := filepath.Join(r.SysNet, name)
	if _, err := os.Stat(filepath.Join(d, "wireless")); err == nil {
		return "wifi"
	}
	if dev, err := filepath.EvalSymlinks(filepath.Join(d, "device")); err == nil && strings.Contains(dev, "/usb") {
		return "usb"
	}
	return "ethernet"
}

type entry struct {
	name, ip string
	conflict bool
}

// readPairs reads "<interface> <ipv4> [conflict]" per line; whatever does not fit is skipped.
func readPairs(path string) ([]entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []entry
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || !ValidName(f[0]) {
			continue
		}
		if ip := net.ParseIP(f[1]); ip == nil || ip.To4() == nil {
			continue
		}
		out = append(out, entry{name: f[0], ip: f[1], conflict: len(f) > 2 && f[2] == "conflict"})
	}
	return out, sc.Err()
}

// ReadDisabled: disabled interfaces. A missing file is an empty list.
func ReadDisabled(path string) ([]string, error) {
	b, err := safefile.ReadOwned(path, 64<<10) // the app's own files (config dir and its run dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); ValidName(l) {
			out = append(out, l)
		}
	}
	return out, nil
}

// ErrInvalid: some name is not an interface name, or there are too many.
var ErrInvalid = errors.New("invalid interface names")

// Normalize validates, drops duplicates and keeps the order of appearance.
func Normalize(names []string) ([]string, error) {
	if len(names) > MaxDisabled {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	out := []string{}
	for _, n := range names {
		if !ValidName(n) {
			return nil, ErrInvalid
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// SetDisabled enables or disables ONE link on top of what the file already holds (not of what the
// caller sees): a disabled link that is now unplugged stays disabled, and two changes in a row do not
// overwrite each other. Atomic write (write), 0640.
func (r *Reader) SetDisabled(name string, off bool, write func(path string, data []byte) error) ([]string, error) {
	if !ValidName(name) {
		return nil, ErrInvalid
	}
	r.wmu.Lock()
	defer r.wmu.Unlock()
	cur, err := ReadDisabled(r.Disabled)
	if err != nil {
		return nil, err
	}
	next := []string{}
	for _, n := range cur {
		if n != name {
			next = append(next, n)
		}
	}
	if off {
		next = append(next, name)
	}
	if next, err = Normalize(next); err != nil {
		return nil, err
	}
	return next, write(r.Disabled, Render(next))
}

// Render: the content of the disabled-links file.
func Render(names []string) []byte {
	var b strings.Builder
	b.WriteString("# Written by eirl-encoderd: links SRTLA does not use (one interface per line).\n")
	for _, n := range names {
		b.WriteString(n + "\n")
	}
	return []byte(b.String())
}

func readUint(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}
