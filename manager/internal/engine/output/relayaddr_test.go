package output

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// resolver is a fake DNS: answers by name, or fails.
func resolver(t *testing.T, answers map[string][]string) {
	t.Helper()
	old := lookupIPv4
	t.Cleanup(func() { lookupIPv4 = old })
	lookupIPv4 = func(_ context.Context, host string) ([]net.IP, error) {
		a, ok := answers[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var ips []net.IP
		for _, s := range a {
			ips = append(ips, net.ParseIP(s))
		}
		return ips, nil
	}
}

func TestRelayAddr(t *testing.T) {
	dir := t.TempDir()
	logs := captureLogs(t)
	resolver(t, map[string][]string{"eu1.example": {"2001:db8::1", "198.51.100.7"}})

	// An address is sent as it is, and nothing is saved
	if ip, ok := relayAddr("192.0.2.1", dir); !ok || ip != "192.0.2.1" {
		t.Fatalf("literal: %q %v", ip, ok)
	}
	// Resolved: the IPv4 answer, saved
	if ip, ok := relayAddr("eu1.example", dir); !ok || ip != "198.51.100.7" {
		t.Fatalf("resolved: %q %v", ip, ok)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, fileRelayAddr)); !strings.Contains(string(b), `"eu1.example":{"ip":"198.51.100.7"`) {
		t.Errorf("file %s", b)
	}
	// Round-robin that still lists the address in use: it stays
	resolver(t, map[string][]string{"eu1.example": {"198.51.100.9", "198.51.100.7"}})
	if ip, _ := relayAddr("eu1.example", dir); ip != "198.51.100.7" {
		t.Errorf("round-robin moved it to %s", ip)
	}
	// DNS down: the saved address, said with its age
	resolver(t, nil)
	if ip, ok := relayAddr("eu1.example", dir); !ok || ip != "198.51.100.7" {
		t.Errorf("cached: %q %v", ip, ok)
	}
	if !strings.Contains(logs.String(), "event=relay_dns_cached host=eu1.example ip=198.51.100.7") {
		t.Errorf("logs:\n%s", logs)
	}
	// Never resolved: the name itself, for the sender to try (the cache only adds)
	if ip, ok := relayAddr("other.example", dir); ok || ip != "other.example" {
		t.Errorf("unresolved: %q %v", ip, ok)
	}
	// Without STATE_DIR: resolved but kept nowhere
	resolver(t, map[string][]string{"eu1.example": {"198.51.100.8"}})
	if ip, ok := relayAddr("eu1.example", ""); !ok || ip != "198.51.100.8" {
		t.Errorf("no state dir: %q %v", ip, ok)
	}
}

func TestRelayAddrFileIsData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileRelayAddr)
	captureLogs(t)
	resolver(t, nil)
	for _, bad := range []string{`not json`, `{"v":2,"hosts":{"eu1.example":{"ip":"198.51.100.7"}}}`,
		`{"v":1,"hosts":{"eu1.example":{"ip":"2001:db8::1"}}}`, `{"v":1,"hosts":{"a b":{"ip":"198.51.100.7"}}}`} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if ip, ok := relayAddr("eu1.example", dir); ok {
			t.Errorf("%s: used %s", bad, ip)
		}
	}
	// Only the newest relayAddrMax names stay
	answers := map[string][]string{}
	for i := 0; i < relayAddrMax+3; i++ {
		answers[fmt.Sprintf("r%d.example", i)] = []string{fmt.Sprintf("198.51.100.%d", i+1)}
	}
	resolver(t, answers)
	now := int64(1000)
	old := nowUnix
	t.Cleanup(func() { nowUnix = old })
	nowUnix = func() int64 { now++; return now }
	for i := 0; i < relayAddrMax+3; i++ {
		if _, ok := relayAddr(fmt.Sprintf("r%d.example", i), dir); !ok {
			t.Fatal(i)
		}
	}
	if f := readRelayFile(path); len(f.Hosts) != relayAddrMax {
		t.Errorf("%d names kept", len(f.Hosts))
	} else if _, ok := f.Hosts["r0.example"]; ok {
		t.Error("the oldest name stayed")
	}
}

// captureLogs sends the engine's events to a buffer for the test.
func captureLogs(t *testing.T) *logBuf {
	t.Helper()
	b := &logBuf{}
	elog.SetOutput(b)
	t.Cleanup(func() { elog.SetOutput(nil) })
	return b
}
