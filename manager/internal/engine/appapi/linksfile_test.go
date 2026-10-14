package appapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLinksFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "links.json")
	now := time.Date(2026, 10, 14, 18, 41, 30, 0, time.UTC)
	write := func(body string, mtime time.Time) {
		os.WriteFile(p, []byte(body), 0o640)
		os.Chtimes(p, mtime, mtime)
	}
	write(`{"v":1,"at":"2026-10-14T18:41:28.012Z","streaming":true,"links":[
		{"name":"eth0","kind":"ethernet","health":"good","code":"ok","available":true,"inUse":true,"disabled":false,"txKbps":2100},
		{"name":"wwan0","kind":"modem","label":"Vodafone","tech":"4g","bars":2,"health":"weak","code":"signal_weak","available":true,"inUse":true},
		{"name":"m-0c1d2e3f4a5b","kind":"modem","health":"bad","code":"sim_pin"},
		{"name":"bad name;rm","kind":"modem","health":"good"},
		{"name":"x1","kind":"satellite","health":"good"},
		{"name":"x2","kind":"modem","health":"purple"},
		{"name":"x3","kind":"modem","health":"bad","code":"made_up","label":"a\u0000b"}]}`, now)
	links, ok := Links(p, now)
	if !ok || len(links) != 4 {
		t.Fatalf("links: %v %+v", ok, links)
	}
	if l := links[1]; l.Name != "wwan0" || l.Label != "Vodafone" || l.Tech != "4g" || l.Health != "weak" || l.Code != "signal_weak" || !l.InUse {
		t.Errorf("wwan0: %+v", l)
	}
	if l := links[3]; l.Name != "x3" || l.Code != "" || l.Label != "" {
		t.Errorf("an unknown code and a label with a NUL are dropped: %+v", l)
	}
	// Stale: the app is not running
	if _, ok := Links(p, now.Add(20*time.Second)); ok {
		t.Error("a 20 s old file is trusted")
	}
	// Another format, or no date: not understood
	write(`{"v":2,"at":"2026-10-14T18:41:28Z","links":[{"name":"eth0","kind":"ethernet","health":"good"}]}`, now.Add(time.Second))
	if _, ok := Links(p, now); ok {
		t.Error("v2 understood")
	}
	write(`{"v":1,"links":[{"name":"eth0","kind":"ethernet","health":"good"}]}`, now.Add(2*time.Second))
	if _, ok := Links(p, now); ok {
		t.Error("no date: trusted")
	}
	os.Remove(p)
	if _, ok := Links(p, now); ok {
		t.Error("no file: trusted")
	}
}

func TestLinksFileCap(t *testing.T) {
	p := filepath.Join(t.TempDir(), "links.json")
	body := `{"v":1,"at":"2026-10-14T18:41:28Z","links":[`
	for i := 0; i < 20; i++ {
		if i > 0 {
			body += ","
		}
		body += `{"name":"eth` + string(rune('a'+i)) + `","kind":"ethernet","health":"good"}`
	}
	os.WriteFile(p, []byte(body+"]}"), 0o640)
	links, ok := Links(p, time.Date(2026, 10, 14, 18, 41, 30, 0, time.UTC))
	if !ok || len(links) != MaxLinks {
		t.Fatalf("cap: %v %d", ok, len(links))
	}
}
