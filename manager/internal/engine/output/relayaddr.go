package output

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

// The relay's address (SRTLA.md §9.7). Each srt or srtla session resolves the relay's name itself and gives
// the senders the address: when name resolution fails (the night test of ENGINE_GO.md §14 waited two minutes
// for it after the line came back), the last good address of that name is used instead.

const (
	fileRelayAddr = "relay-addr.json" // in STATE_DIR: kept across reboots
	relayAddrMax  = 8                 // names kept
)

// Variables so that the tests can replace them.
var (
	resolveBudget = 5 * time.Second // the senders' own resolver (getaddrinfo) waited about as long
	lookupIPv4    = func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip4", host)
	}
	nowUnix = func() int64 { return time.Now().Unix() }
)

type relayEntry struct {
	IP string `json:"ip"`
	At int64  `json:"at"` // unix seconds of the resolution
}

type relayFile struct {
	V     int                   `json:"v"`
	Hosts map[string]relayEntry `json:"hosts"`
}

// relayMu serialises the read-modify-write of the file (one session at a time, but tests run in parallel).
var relayMu sync.Mutex

// relayAddr is the IPv4 address to send to for host: host itself when it is an address, else a fresh
// resolution (saved in stateDir), else the address saved last time. stateDir "" keeps nothing. Without any of
// them it returns host itself and false: the sender resolves the name as it always did (an IPv6-only name too),
// so the cache only ever adds a way to connect.
func relayAddr(host, stateDir string) (string, bool) {
	if ip := net.ParseIP(host); ip != nil {
		return host, true
	}
	relayMu.Lock()
	defer relayMu.Unlock()
	path := ""
	if stateDir != "" {
		path = filepath.Join(stateDir, fileRelayAddr)
	}
	f := readRelayFile(path)
	cached, hasCached := f.Hosts[host]

	ctx, cancel := context.WithTimeout(context.Background(), resolveBudget)
	t0 := time.Now()
	ips, err := lookupIPv4(ctx, host)
	cancel()
	var found []string
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			found = append(found, v4.String())
		}
	}
	if len(found) == 0 {
		if err == nil {
			err = errors.New("no IPv4 address")
		}
		if !hasCached {
			elog.Event("engine", "relay_dns_failed", "host", host, "err", err, "action", "the sender resolves it")
			return host, false
		}
		elog.Event("engine", "relay_dns_cached", "host", host, "ip", cached.IP,
			"age_s", max(nowUnix()-cached.At, 0), "err", err)
		return cached.IP, true
	}
	// The address in use, while the name still gives it: a round-robin answer does not move the stream
	ip := found[0]
	if hasCached && slices.Contains(found, cached.IP) {
		ip = cached.IP
	}
	if !hasCached || cached.IP != ip {
		elog.Event("engine", "relay_resolved", "host", host, "ip", ip, "ms", time.Since(t0).Milliseconds())
	}
	f.Hosts[host] = relayEntry{IP: ip, At: nowUnix()}
	writeRelayFile(path, f)
	return ip, true
}

func readRelayFile(path string) relayFile {
	f := relayFile{V: 1, Hosts: map[string]relayEntry{}}
	if path == "" {
		return f
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 64<<10 {
		return f
	}
	var r relayFile
	if json.Unmarshal(b, &r) != nil || r.V != 1 {
		return f
	}
	// Read as data: only names and IPv4 addresses that validate
	for h, e := range r.Hosts {
		if ip := net.ParseIP(e.IP); validRelayHost(h) && ip != nil && ip.To4() != nil {
			f.Hosts[h] = e
		}
	}
	return f
}

func writeRelayFile(path string, f relayFile) {
	if path == "" {
		return
	}
	// The newest relayAddrMax names
	if len(f.Hosts) > relayAddrMax {
		names := make([]string, 0, len(f.Hosts))
		for h := range f.Hosts {
			names = append(names, h)
		}
		sort.Slice(names, func(i, j int) bool { return f.Hosts[names[i]].At > f.Hosts[names[j]].At })
		for _, h := range names[relayAddrMax:] {
			delete(f.Hosts, h)
		}
	}
	b, err := json.Marshal(f)
	if err == nil {
		err = sysx.WriteAtomic(path, b, 0o644)
	}
	if err != nil {
		elog.Event("engine", "relay_addr_write_failed", "err", err)
	}
}

// validRelayHost is a DNS name as the configuration takes it (reSRTLAURL, reSRTURL).
func validRelayHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, r := range h {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return false
		}
	}
	return !strings.HasPrefix(h, ".")
}
