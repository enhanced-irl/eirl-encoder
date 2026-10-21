package uplinks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SRTLA.md §9.3-§9.4: the Rust sender's figures reach a link in use, and only a fresh, valid file counts.
func TestReadLinkStats(t *testing.T) {
	dir := t.TempDir()
	sys := filepath.Join(dir, "sys", "class", "net")
	r := &Reader{Available: filepath.Join(dir, "available"), InUse: filepath.Join(dir, "run", "uplinks"),
		Disabled: filepath.Join(dir, "disabled"), SysNet: sys}
	write(t, r.Available, "eth0 192.168.1.50\nwwan0 10.64.1.2\nwlan0 192.168.43.20\n")
	write(t, r.InUse, "eth0 192.168.1.50\nwwan0 10.64.1.2\n")
	stats := filepath.Join(dir, "run", "srtla-links.json") // next to InUse by default
	write(t, stats, `{"v":1,"sender":"irlserver","mode":"classic","links":[
		{"iface":"eth0","ip":"192.168.1.50","bond":"active","rttMs":57,"sendKbps":5400,"nakPerSec":0.24,"future":1},
		{"iface":"wwan0","ip":"10.64.1.99","bond":"active","rttMs":80,"sendKbps":100,"nakPerSec":0},
		{"iface":"wlan0","ip":"192.168.43.20","bond":"stalled","rttMs":300,"sendKbps":0,"nakPerSec":0}]}`)
	now := time.Now()
	got := r.Read(now, true)
	byName := map[string]Uplink{}
	for _, u := range got {
		byName[u.Name] = u
	}
	e := byName["eth0"]
	if e.RTTMs == nil || *e.RTTMs != 57 || *e.SendKbps != 5400 || *e.NakPerSec != 0.2 || e.Bond != "active" {
		t.Errorf("eth0 %+v", e)
	}
	if w := byName["wwan0"]; w.RTTMs != nil || w.Bond != "" {
		t.Errorf("wwan0's address does not match, its figures are not its: %+v", w)
	}
	if w := byName["wlan0"]; w.RTTMs != nil || w.Bond != "" {
		t.Errorf("wlan0 is not in use: %+v", w)
	}
	// In the JSON of /status: the fields only when there are figures
	b, _ := json.Marshal(byName["wlan0"])
	if strings.Contains(string(b), "rttMs") || strings.Contains(string(b), "bond") {
		t.Errorf("wlan0 json %s", b)
	}
	// Not over SRTLA, stale, or not version 1: none
	if u := r.Read(now, false); u[0].RTTMs != nil {
		t.Error("figures without streaming over SRTLA")
	}
	old := now.Add(-10 * time.Second)
	if err := os.Chtimes(stats, old, old); err != nil {
		t.Fatal(err)
	}
	if u := r.Read(now, true); u[0].RTTMs != nil {
		t.Error("figures from a stale file")
	}
	write(t, stats, `{"v":2,"links":[{"iface":"eth0","ip":"192.168.1.50","bond":"active","rttMs":1,"sendKbps":1,"nakPerSec":0}]}`)
	if u := r.Read(time.Now(), true); u[0].RTTMs != nil {
		t.Error("figures from version 2")
	}
}

func TestReadLinkStatsIsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "srtla-links.json")
	link := func(s string) string { return `{"v":1,"links":[` + s + `]}` }
	for _, bad := range []string{
		`{"iface":"eth0;rm","ip":"192.168.1.50","bond":"active","rttMs":1,"sendKbps":1,"nakPerSec":0}`,
		`{"iface":"eth0","ip":"2001:db8::1","bond":"active","rttMs":1,"sendKbps":1,"nakPerSec":0}`,
		`{"iface":"eth0","ip":"192.168.1.50","bond":"dead","rttMs":1,"sendKbps":1,"nakPerSec":0}`,
		`{"iface":"eth0","ip":"192.168.1.50","bond":"active","rttMs":-1,"sendKbps":1,"nakPerSec":0}`,
		`{"iface":"eth0","ip":"192.168.1.50","bond":"active","rttMs":60001,"sendKbps":1,"nakPerSec":0}`,
		`{"iface":"eth0","ip":"192.168.1.50","bond":"active","rttMs":1,"sendKbps":1000001,"nakPerSec":0}`,
		`{"iface":"eth0","ip":"192.168.1.50","bond":"active","rttMs":1,"sendKbps":1}`,
	} {
		write(t, path, link(bad))
		if m := ReadLinkStats(path, time.Now()); len(m) != 0 {
			t.Errorf("%s: %+v", bad, m)
		}
	}
	write(t, path, `not json`)
	if ReadLinkStats(path, time.Now()) != nil {
		t.Error("not json")
	}
	write(t, path, strings.Repeat(" ", linkStatsSize+1))
	if ReadLinkStats(path, time.Now()) != nil {
		t.Error("too big")
	}
	// At most linkStatsMax links
	var ls []string
	for i := 0; i < linkStatsMax+4; i++ {
		ls = append(ls, `{"iface":"eth`+string(rune('a'+i))+`","ip":"192.168.1.50","bond":"active","rttMs":1,"sendKbps":1,"nakPerSec":0}`)
	}
	write(t, path, link(strings.Join(ls, ",")))
	if m := ReadLinkStats(path, time.Now()); len(m) != linkStatsMax {
		t.Errorf("%d links", len(m))
	}
}
