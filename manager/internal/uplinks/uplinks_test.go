package uplinks

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setIface(t *testing.T, sys, name string, tx, rx int, usb bool) {
	t.Helper()
	write(t, filepath.Join(sys, name, "statistics", "tx_bytes"), strconv.Itoa(tx))
	write(t, filepath.Join(sys, name, "statistics", "rx_bytes"), strconv.Itoa(rx))
	if usb {
		dev := filepath.Join(sys, "..", "devices", "usb1", "1-2", name)
		os.MkdirAll(dev, 0o755)
		os.Symlink(dev, filepath.Join(sys, name, "device"))
	}
}

func TestRead(t *testing.T) {
	dir := t.TempDir()
	sys := filepath.Join(dir, "sys", "class", "net")
	r := &Reader{
		Available: filepath.Join(dir, "available"),
		InUse:     filepath.Join(dir, "inuse"),
		Disabled:  filepath.Join(dir, "disabled"),
		SysNet:    sys,
	}
	if r.Read(time.Now(), true) != nil {
		t.Fatal("no dispatcher (missing file) must give nil")
	}
	write(t, r.Available, "eth0 192.168.1.50\nenx001122 192.168.8.100\nbad;name 1.2.3.4\nwlan0 2001:db8::1\nenx334455 192.168.8.100 conflict\n")
	write(t, r.InUse, "eth0 192.168.1.50\n")
	write(t, r.Disabled, "# comment\nenx001122\n")
	setIface(t, sys, "eth0", 1000, 500, false)
	setIface(t, sys, "enx001122", 0, 0, true)

	t0 := time.Unix(1000, 0)
	got := r.Read(t0, true)
	if len(got) != 3 {
		t.Fatalf("expected 3 valid links, %+v", got)
	}
	if !got[2].Conflict || got[0].Conflict {
		t.Errorf("conflict: %+v", got)
	}
	if got[0].Name != "eth0" || !got[0].InUse || got[0].Disabled || got[0].Kind != "ethernet" || got[0].TxKbps != 0 {
		t.Errorf("eth0: %+v", got[0])
	}
	if got[1].Kind != "usb" || !got[1].Disabled || got[1].InUse {
		t.Errorf("modem: %+v", got[1])
	}
	setIface(t, sys, "eth0", 1000+250000, 500, false) // 250 kB in 2 s = 1000 kbps
	got = r.Read(t0.Add(2*time.Second), true)
	if got[0].TxKbps != 1000 || got[0].RxKbps != 0 {
		t.Errorf("rate: %+v", got[0])
	}
	// Without SRTLA running, the in-use file is from another run
	if got = r.Read(t0.Add(4*time.Second), false); got[0].InUse {
		t.Error("InUse without SRTLA running")
	}
}

// Several callers at once (the web's SSE and the screen's GET /status, every 2 s each): a call less than MinSample
// after the baseline gets the rates computed last time, without moving the baseline
func TestReadSharedBaseline(t *testing.T) {
	dir := t.TempDir()
	sys := filepath.Join(dir, "sys", "class", "net")
	r := &Reader{Available: filepath.Join(dir, "available"), Disabled: filepath.Join(dir, "disabled"), SysNet: sys}
	write(t, r.Available, "eth0 192.168.1.50\n")
	rates := func(at time.Time) [2]int {
		t.Helper()
		got := r.Read(at, false)
		if len(got) != 1 {
			t.Fatalf("links: %+v", got)
		}
		return [2]int{got[0].TxKbps, got[0].RxKbps}
	}
	t0 := time.Unix(1000, 0)
	setIface(t, sys, "eth0", 1000, 500, false)
	if got := rates(t0); got != [2]int{0, 0} {
		t.Errorf("first read: %v", got)
	}
	setIface(t, sys, "eth0", 1000+250000, 500+25000, false) // in 2 s: 1000 and 100 kbps
	if got := rates(t0.Add(2 * time.Second)); got != [2]int{1000, 100} {
		t.Errorf("after 2 s: %v", got)
	}
	// 100 ms later, the other caller: the same rates (with the delta since the last call, 1000 and 0)
	setIface(t, sys, "eth0", 1000+250000+12500, 500+25000, false)
	if got := rates(t0.Add(2100 * time.Millisecond)); got != [2]int{1000, 100} {
		t.Errorf("100 ms later: %v", got)
	}
	// After the interval: since the baseline at 2 s (not the call at 2.1 s), 500 kB and 50 kB in 2 s
	setIface(t, sys, "eth0", 1000+250000+500000, 500+25000+50000, false)
	if got := rates(t0.Add(4 * time.Second)); got != [2]int{2000, 200} {
		t.Errorf("after the interval: %v", got)
	}
	// Counters reset (the interface came back): a new baseline, without rates; then rates again
	setIface(t, sys, "eth0", 100, 100, false)
	if got := rates(t0.Add(6 * time.Second)); got != [2]int{0, 0} {
		t.Errorf("after a counter reset: %v", got)
	}
	setIface(t, sys, "eth0", 100+125000, 100, false)
	if got := rates(t0.Add(8 * time.Second)); got != [2]int{500, 0} {
		t.Errorf("after the reset and an interval: %v", got)
	}
	// A clock that went back: a new baseline, without rates
	if got := rates(t0.Add(time.Second)); got != [2]int{0, 0} {
		t.Errorf("clock back: %v", got)
	}
}

func TestNormalize(t *testing.T) {
	n, err := Normalize([]string{"enx1", "eth0", "enx1"})
	if err != nil || len(n) != 2 || n[0] != "enx1" {
		t.Fatalf("%v %v", n, err)
	}
	for _, bad := range [][]string{{"a b"}, {"x/y"}, {""}, {"abcdefghijklmnop"}} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	many := make([]string, MaxDisabled+1)
	for i := range many {
		many[i] = "e" + strconv.Itoa(i)
	}
	if _, err := Normalize(many); err == nil {
		t.Error("too many")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "d")
	write(t, p, string(Render([]string{"eth0", "enx1"})))
	if got, _ := ReadDisabled(p); len(got) != 2 || got[1] != "enx1" {
		t.Errorf("round trip: %v", got)
	}
}
