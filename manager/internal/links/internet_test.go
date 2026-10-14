package links

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

func TestInternetHysteresisAndOneWriter(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	files := map[string]string{}
	writes := 0
	good := map[string]bool{"10.0.0.2": true, "10.0.1.2": true}
	n := &Internet{URL: func() string { return "https://eu1.example/ping" }, Path: "/run/nointernet",
		Write: func(p string, b []byte, _ os.FileMode) error { files[p] = string(b); writes++; return nil },
		Now:   func() time.Time { return now },
		check: func(_ context.Context, _, ip string) bool { return good[ip] }}
	targets := []uplinks.Addr{{Name: "wwan0", IP: "10.0.0.2"}, {Name: "enx1", IP: "10.0.1.2"}}
	round := func(adv time.Duration) {
		now = now.Add(adv)
		n.Round(context.Background(), targets)
	}
	round(0)
	if files["/run/nointernet"] != "" || writes != 1 {
		t.Fatalf("first round: %q (%d writes)", files["/run/nointernet"], writes)
	}
	// The SIM of wwan0 runs out of credit: two bad checks AND 60 s since the last change
	good["10.0.0.2"] = false
	round(30 * time.Second)
	if got := n.Off(); len(got) != 0 {
		t.Fatalf("one bad check is not enough: %v", got)
	}
	round(30 * time.Second)
	if got := files["/run/nointernet"]; got != "wwan0\n" {
		t.Fatalf("after two bad checks over 60 s: %q", got)
	}
	// The Wi-Fi client's monitor reports through Set: the same file, both lines
	if err := n.Set("wlan0", true); err != nil {
		t.Fatal(err)
	}
	if got := files["/run/nointernet"]; got != "wlan0\nwwan0\n" {
		t.Fatalf("with the Wi-Fi: %q", got)
	}
	n.Set("wlan0", false)
	// wwan0 back: two good checks and the dwell
	good["10.0.0.2"] = true
	w := writes
	round(30 * time.Second)
	round(30 * time.Second)
	if got := files["/run/nointernet"]; got != "" || writes != w+1 {
		t.Fatalf("back: %q (%d writes)", got, writes-w)
	}
	// Unplugged: forgotten
	good["10.0.1.2"] = false
	round(30 * time.Second)
	targets = targets[:1]
	round(30 * time.Second)
	if got := n.Off(); len(got) != 0 {
		t.Fatalf("an unplugged link stays: %v", got)
	}
}

func TestInternetNowhereToCheck(t *testing.T) {
	asked := 0
	n := &Internet{URL: func() string { return "" }, check: func(context.Context, string, string) bool { asked++; return false }}
	n.Round(context.Background(), []uplinks.Addr{{Name: "wwan0", IP: "10.0.0.2"}})
	if asked != 0 || len(n.Off()) != 0 {
		t.Fatalf("checked without a URL: %d %v", asked, n.Off())
	}
}

func TestTargets(t *testing.T) {
	snap := Snapshot{Uplinks: []uplinks.Uplink{{Name: "eth0", Kind: "ethernet"}, {Name: "wlan0", Kind: "wifi"},
		{Name: "wwan0", Kind: "modem", IP: "10.0.0.2"}, {Name: "usb0", Kind: "usb", IP: "192.168.42.2"},
		{Name: "enx2", Kind: "modem", Conflict: true}}}
	got := Targets(snap)
	if len(got) != 2 || got[0].Name != "wwan0" || got[1].Name != "usb0" {
		t.Fatalf("targets: %+v", got)
	}
}

func TestInternetForgetsWithoutURL(t *testing.T) {
	url := "https://eu1.example/ping"
	n := &Internet{URL: func() string { return url }, Streak: 1, check: func(context.Context, string, string) bool { return false }}
	n.Round(context.Background(), []uplinks.Addr{{Name: "wwan0", IP: "10.0.0.2"}})
	if len(n.Off()) != 1 {
		t.Fatalf("one bad check with Streak 1: %v", n.Off())
	}
	url = "" // a custom destination: nowhere to check against
	n.Round(context.Background(), []uplinks.Addr{{Name: "wwan0", IP: "10.0.0.2"}})
	if len(n.Off()) != 0 {
		t.Fatalf("judged without a way to check again: %v", n.Off())
	}
}

// Round and the Wi-Fi's Set run in two goroutines: the file always ends with what Off says
func TestInternetConcurrentWritersEndConsistent(t *testing.T) {
	var mu sync.Mutex
	file := ""
	n := &Internet{URL: func() string { return "https://x/ping" }, Path: "/f",
		Write: func(_ string, b []byte, _ os.FileMode) error { mu.Lock(); file = string(b); mu.Unlock(); return nil },
		check: func(context.Context, string, string) bool { return true }}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); n.Set("wlan0", i%2 == 0) }(i)
		go func() {
			defer wg.Done()
			n.Round(context.Background(), []uplinks.Addr{{Name: "wwan0", IP: "10.0.0.2"}})
		}()
	}
	wg.Wait()
	n.Round(context.Background(), []uplinks.Addr{{Name: "wwan0", IP: "10.0.0.2"}})
	want := strings.Join(n.Off(), "\n")
	if want != "" {
		want += "\n"
	}
	if file != want {
		t.Fatalf("file %q, Off %q", file, want)
	}
}
