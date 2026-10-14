package output

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// UplinksTest.test_select
func TestSelect(t *testing.T) {
	d := t.TempDir()
	a, b := filepath.Join(d, "available"), filepath.Join(d, "disabled")
	write(t, a, "eth0 192.168.1.232\nwwan0 10.64.1.2\nusb0 10.0.0.7 conflict\nmal; rm 1.2.3.4\nwlan0 999.1\n")
	write(t, b, "wwan0\n")
	eth, wwan := uplink{"eth0", "192.168.1.232"}, uplink{"wwan0", "10.64.1.2"}
	if got := selectUplinks(a, b); !slices.Equal(got, []uplink{eth}) {
		t.Errorf("got %v", got)
	}
	if got := selectUplinks(a, "/no/existe"); !slices.Equal(got, []uplink{eth, wwan}) {
		t.Errorf("got %v", got)
	}
	if got := selectUplinks("/no/existe", b); len(got) != 0 {
		t.Errorf("got %v", got)
	}
	// Log text: English in Go (Python: 'ninguno')
	if got := uplinksDesc([]uplink{{"eth0", "1.2.3.4"}}); got != "eth0 (1.2.3.4)" {
		t.Errorf("desc %q", got)
	}
	if got := uplinksDesc(nil); got != "none" {
		t.Errorf("desc %q", got)
	}
}

// UplinksTest.test_select_without_internet: the client wifi without internet (WIFI.md §13), another
// file of excluded uplinks, besides the disabled ones
func TestSelectWithoutInternet(t *testing.T) {
	d := t.TempDir()
	a, b, nonet := filepath.Join(d, "available"), filepath.Join(d, "disabled"), filepath.Join(d, "nointernet")
	write(t, a, "eth0 192.168.1.232\nwwan0 10.64.1.2\nwlan0 172.20.10.2\n")
	write(t, b, "wwan0\n")
	write(t, nonet, "wlan0\nmal; rm\n")
	eth, wwan, wlan := uplink{"eth0", "192.168.1.232"}, uplink{"wwan0", "10.64.1.2"}, uplink{"wlan0", "172.20.10.2"}
	for _, c := range []struct {
		excl []string
		want []uplink
	}{
		{[]string{b, nonet}, []uplink{eth}},
		{[]string{b, "/no/existe"}, []uplink{eth, wlan}},
		{[]string{b, ""}, []uplink{eth, wlan}},
		{nil, []uplink{eth, wwan, wlan}},
	} {
		if got := selectUplinks(a, c.excl...); !slices.Equal(got, c.want) {
			t.Errorf("%v: got %v", c.excl, got)
		}
	}
}

// fakeProc is a srtla_send for watchUplinks: alive until stopped.
type fakeProc struct {
	mu      sync.Mutex
	signals int
	done    chan struct{}
	once    sync.Once
}

func newFakeProc() *fakeProc { return &fakeProc{done: make(chan struct{})} }

func (p *fakeProc) Done() <-chan struct{} { return p.done }

func (p *fakeProc) Hangup() error { p.mu.Lock(); p.signals++; p.mu.Unlock(); return nil }

func (p *fakeProc) Terminate() { p.once.Do(func() { close(p.done) }) }

func (p *fakeProc) terminated() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func fastUplinks(t *testing.T) {
	elog.SetOutput(io.Discard)
	t.Cleanup(func() { elog.SetOutput(nil) })
	old := uplinksEvery
	uplinksEvery = 10 * time.Millisecond
	t.Cleanup(func() { uplinksEvery = old })
}

// WatchUplinksTest.test_last_uplink_goes_away: the only wifi loses internet: srtla_send is stopped and
// the session ends in no_uplinks (transient), not in connection_lost by SRT's timeout
func TestLastUplinkGoesAway(t *testing.T) {
	fastUplinks(t)
	d := t.TempDir()
	a, nonet := filepath.Join(d, "available"), filepath.Join(d, "nointernet")
	write(t, a, "wlan0 172.20.10.2\n")
	write(t, nonet, "")
	f := uplinkFiles{runDir: d, available: a, nonet: nonet}
	p, res := newFakeProc(), &result{}
	sel := f.current()
	if !slices.Equal(sel, []uplink{{"wlan0", "172.20.10.2"}}) {
		t.Fatalf("sel %v", sel)
	}
	write(t, nonet, "wlan0\n")
	watchUplinks(f, p, sel, res)
	if !p.terminated() {
		t.Error("srtla_send not stopped")
	}
	if res.noUplinks == nil || *res.noUplinks != (failure{ReasonNoUplinks, "sin enlaces a internet"}) {
		t.Errorf("result %+v", res.noUplinks)
	}
	if got := read(t, filepath.Join(d, "srtla-ips")); got != "" {
		t.Errorf("srtla-ips %q", got)
	}
	if !transient[ReasonNoUplinks] {
		t.Error("no_uplinks is not transient")
	}
}

// WatchUplinksTest.test_uplink_change_without_cut: with another uplink, the wifi without internet
// leaves the split with SIGHUP, without stopping srtla_send
func TestUplinkChangeWithoutCut(t *testing.T) {
	fastUplinks(t)
	d := t.TempDir()
	a, nonet := filepath.Join(d, "available"), filepath.Join(d, "nointernet")
	write(t, a, "eth0 192.168.1.232\nwlan0 172.20.10.2\n")
	write(t, nonet, "wlan0\n")
	f := uplinkFiles{runDir: d, available: a, nonet: nonet}
	p, res := newFakeProc(), &result{}
	cur := []uplink{{"eth0", "192.168.1.232"}, {"wlan0", "172.20.10.2"}}
	time.AfterFunc(100*time.Millisecond, p.Terminate)
	watchUplinks(f, p, cur, res)
	if p.signals != 1 {
		t.Errorf("%d signals", p.signals)
	}
	if res.noUplinks != nil {
		t.Error("no_uplinks with an uplink left")
	}
	if got := read(t, filepath.Join(d, "srtla-ips")); got != "192.168.1.232\n" {
		t.Errorf("srtla-ips %q", got)
	}
	if got := read(t, filepath.Join(d, "uplinks")); got != "eth0 192.168.1.232\n" {
		t.Errorf("uplinks %q", got)
	}
}
