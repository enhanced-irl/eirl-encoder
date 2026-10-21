package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
)

// rustRig is srtlaRig with irlserver's sender (SRTLA.md §9.1).
func rustRig(t *testing.T) *rig {
	shortenUplinks(t)
	shorten(t, &statsRetry, 20*time.Millisecond)
	return newRig(t, func(c *config.Config) {
		c.SRTURL, c.Transport, c.SRTLASender = "srtla://relay.example:5000", "srtla", "irlserver"
	})
}

// The Rust sender: its arguments and environment, and its statistics per link into srtla-links.json, which
// goes with the session.
func TestSRTLARustSender(t *testing.T) {
	r := rustRig(t)
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\nwwan0 10.64.1.2\n")
	r.out.Set(true)
	r.waitState(StateOn, "")
	sock := filepath.Join(r.dir, "srtla.sock")
	want := strings.Join([]string{"--mode", "classic", "--no-rehome", "--control-socket", sock,
		"6000", testRelayIP, "5000", filepath.Join(r.dir, "srtla-ips")}, "\n")
	if got := readSoon(t, filepath.Join(r.dir, "srtla.args")); got != want {
		t.Errorf("args\n got %q\nwant %q", got, want)
	}
	env := readSoon(t, filepath.Join(r.dir, "srtla.env"))
	for _, kv := range []string{"RUST_LOG=warn", "NO_COLOR=1", "TOKIO_WORKER_THREADS=2"} {
		if !strings.Contains(env, kv+"\n") && !strings.HasSuffix(env, kv) {
			t.Errorf("no %s in its environment", kv)
		}
	}
	links := filepath.Join(r.dir, "srtla-links.json")
	var f LinksFile
	waitFor(t, "statistics with NAKs per second", func() bool {
		b, err := os.ReadFile(links)
		if err != nil || json.Unmarshal(b, &f) != nil || len(f.Links) != 2 {
			return false
		}
		return f.Links[0].NAKPerSec > 0
	})
	if f.V != 1 || f.Sender != "irlserver" || f.Mode != "classic" {
		t.Errorf("file %+v", f)
	}
	// 10.9.9.9 is not one of the session's uplinks: left out
	a, b := f.Links[0], f.Links[1]
	if a.Iface != "eth0" || a.IP != "192.168.1.232" || a.Bond != "active" || a.RTTMs != 57 || a.SendKbps != 5400 {
		t.Errorf("eth0 %+v", a)
	}
	if b.Iface != "wwan0" || b.Bond != "stalled" || b.RTTMs != 340 || b.SendKbps != 0 || b.NAKPerSec != 0 {
		t.Errorf("wwan0 %+v", b)
	}
	if !strings.Contains(r.logs.String(), "event=srtla_sender sender=irlserver mode=classic relay="+testRelayIP) {
		t.Errorf("logs:\n%s", r.logs)
	}
	r.out.Set(false)
	r.waitState(StateOff, "")
	waitFor(t, "the files go with the session", func() bool {
		_, e1 := os.Stat(links)
		_, e2 := os.Stat(sock)
		return os.IsNotExist(e1) && os.IsNotExist(e2)
	})
}

// It gives up with tracing's lines, coloured: the same reason as the C sender, and a clean detail.
func TestSRTLARustGivesUp(t *testing.T) {
	r := rustRig(t)
	t.Setenv(fakeSRTLA, "fail-rs")
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\n")
	r.out.Set(true)
	i := r.waitState(StateConnecting, ReasonRelayUnreachable)
	if i.Detail != "ERROR Failed to establish any initial connections" {
		t.Errorf("detail %q", i.Detail)
	}
	logs := r.logs.String()
	if !strings.Contains(logs, "[srtla] event=srtla_link_down") || strings.Contains(logs, "\x1b") {
		t.Errorf("logs:\n%s", logs)
	}
}

// The sender asked for is not installed: the other one, said in the journal (SRTLA.md §9.2).
func TestSRTLASenderFallback(t *testing.T) {
	shortenUplinks(t)
	r := newRig(t, func(c *config.Config) {
		c.SRTURL, c.Transport, c.SRTLASender, c.SRTLARSBin = "srtla://relay.example:5000", "srtla", "irlserver", "/nonexistent/srtla_send_rs"
	})
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\n")
	r.out.Set(true)
	r.waitState(StateOn, "")
	if got := readSoon(t, filepath.Join(r.dir, "srtla.args")); strings.Contains(got, "--mode") {
		t.Errorf("the Rust sender's arguments: %q", got)
	}
	if !strings.Contains(r.logs.String(), "event=srtla_sender_fallback wanted=irlserver using=belabox") {
		t.Errorf("logs:\n%s", r.logs)
	}
}

// srt to a relay by name: srt-live-transmit gets the address (SRTLA.md §9.7) and the 1,128-byte payload.
func TestSRTSendsToTheRelaysAddress(t *testing.T) {
	r := newRig(t, func(c *config.Config) {
		c.SRTURL, c.SRTPayload = "srt://relay.example:5000", 1128
	})
	r.out.Set(true)
	r.waitState(StateOn, "")
	a := read(t, filepath.Join(r.dir, "slt.args"))
	if !strings.HasPrefix(a, "-chunk:1128\n") || !strings.Contains(a, "srt://"+testRelayIP+":5000?mode=caller&latency=2000&payloadsize=1128&") {
		t.Errorf("slt args %q", a)
	}
}

// A relay whose name does not resolve and was never resolved: srt-live-transmit gets the name and tries it itself,
// as before the cache (which only ever adds a way to connect).
func TestRelayUnresolvable(t *testing.T) {
	r := newRig(t, func(c *config.Config) { c.SRTURL = "srt://unresolvable.example:5000" })
	r.out.Set(true)
	r.waitState(StateOn, "")
	if a := readSoon(t, filepath.Join(r.dir, "slt.args")); !strings.Contains(a, "srt://unresolvable.example:5000?") {
		t.Errorf("slt args %q", a)
	}
	if !strings.Contains(r.logs.String(), "event=relay_dns_failed host=unresolvable.example") {
		t.Errorf("logs:\n%s", r.logs)
	}
}

// The same with srtla: the Rust sender gets the name and keeps its re-home (it has a name to follow).
func TestRustGetsTheName(t *testing.T) {
	r := rustRig(t)
	r.cfg.SRTURL = "srtla://unresolvable.example:5000"
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\n")
	r.out.Set(true)
	r.waitState(StateOn, "")
	a := readSoon(t, filepath.Join(r.dir, "srtla.args"))
	if !strings.Contains(a, "\nunresolvable.example\n") || strings.Contains(a, "--no-rehome") {
		t.Errorf("args %q", a)
	}
}

func TestLinkStats(t *testing.T) {
	inUse := map[string]string{"192.168.1.232": "eth0", "10.64.1.2": "wwan0", "10.0.0.2": "usb0"}
	prev := map[string]nakMark{"192.168.1.232": {count: 10, t: 1}, "10.0.0.2": {count: 50, t: 1}, "10.1.1.1": {count: 1, t: 1}}
	got := linkStats([]rsLink{
		{IP: "192.168.1.232", Connected: true, RTTMs: 56.6, NAKCount: 13, BytesPS: 125000},
		{IP: "192.168.1.232", Connected: true, RTTMs: 1}, // repeated: the first one counts
		{IP: "10.64.1.2", Connected: true, TimedOut: true, Weak: true, RTTMs: 99999999},
		{IP: "10.0.0.2", Connected: true, Weak: true, NAKCount: 2, BytesPS: -5}, // reset after a reconnection
		{IP: "203.0.113.9", Connected: true},                                    // not in use
	}, inUse, prev, 3)
	want := []LinkStat{
		{Iface: "eth0", IP: "192.168.1.232", Bond: "active", RTTMs: 57, SendKbps: 1000, NAKPerSec: 1.5},
		{Iface: "wwan0", IP: "10.64.1.2", Bond: "down", RTTMs: 60000},
		{Iface: "usb0", IP: "10.0.0.2", Bond: "weak"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if _, ok := prev["10.1.1.1"]; ok {
		t.Error("a link that went is still remembered")
	}
	if prev["10.0.0.2"].count != 2 {
		t.Error("the count after a reset is not the new base")
	}
}

// readSoon reads a file a fake writes when it starts: the output may be on before the fake has written it.
func readSoon(t *testing.T, path string) string {
	t.Helper()
	waitFor(t, filepath.Base(path), func() bool { _, err := os.Stat(path); return err == nil })
	return read(t, path)
}

// srt-live-transmit starts once the SRTLA sender has registered, not before: the relays' gosrt would add the
// wait to the latency of the whole session (SRTLA.md §9.1).
func TestSLTWaitsForTheRegistration(t *testing.T) {
	r := srtlaRig(t)
	t.Setenv(fakeSRTLA, "late")
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\n")
	r.out.Set(true)
	r.waitState(StateOn, "")
	reg := readSoon(t, filepath.Join(r.dir, "srtla.registered"))
	slt := readSoon(t, filepath.Join(r.dir, "slt.started"))
	if slt < reg || len(slt) != len(reg) {
		t.Errorf("srt-live-transmit started at %s, before the registration at %s", slt, reg)
	}
	if !strings.Contains(r.logs.String(), "event=srtla_registered") {
		t.Errorf("logs:\n%s", r.logs)
	}
}

// A sender that never says it registered: srt-live-transmit starts anyway after srtlaRegisterWait.
func TestSLTStartsIfTheSenderIsSilent(t *testing.T) {
	r := srtlaRig(t)
	shorten(t, &srtlaRegisterWait, 300*time.Millisecond)
	t.Setenv(fakeSRTLA, "silent")
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\n")
	r.out.Set(true)
	r.waitState(StateOn, "")
	if !strings.Contains(r.logs.String(), "event=srtla_register_slow") {
		t.Errorf("logs:\n%s", r.logs)
	}
}

// irlserver's sender never gives up its first registration: when its statistics say it has not registered in
// srtlaRegisterWait, the session ends with relay_unreachable, so that the retry resolves the name again. (A short
// name: the test's directory holds the control socket, and macOS allows 104 bytes for its path.)
func TestRustNoRegister(t *testing.T) {
	r := rustRig(t)
	shorten(t, &srtlaRegisterWait, 400*time.Millisecond)
	t.Setenv(fakeSRTLA, "unregistered-rs")
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\n")
	r.out.Set(true)
	i := r.waitState(StateConnecting, ReasonRelayUnreachable)
	if !strings.Contains(i.Detail, "did not register") {
		t.Errorf("detail %q", i.Detail)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "slt.started")); err == nil {
		t.Error("srt-live-transmit started before the sender registered")
	}
	waitFor(t, "a retry", func() bool { return strings.Contains(r.logs.String(), "event=output_retry reason=relay_unreachable") })
}

// Lost every uplink: the journal gets the per-packet warning once in logEvery with its count, and after
// srtlaDeadWait the engine stops the sender (it never exits by itself), the session ends and is retried.
func TestRustDeadGivesUp(t *testing.T) {
	r := srtlaRig(t)
	shorten(t, &srtlaDeadWait, 300*time.Millisecond)
	t.Setenv(fakeSRTLA, "dead-rs")
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\n")
	r.out.Set(true)
	waitFor(t, "gave up", func() bool { return strings.Contains(r.logs.String(), "event=srtla_gave_up") })
	waitFor(t, "a retry", func() bool { return strings.Contains(r.logs.String(), "event=output_retry reason=relay_unreachable") })
	if n := strings.Count(r.logs.String(), "no available connection to forward packet"); n > 3 {
		t.Errorf("%d warnings in the journal", n)
	}
}
