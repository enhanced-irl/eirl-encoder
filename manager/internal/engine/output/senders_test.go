package output

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
)

const sendKey = "live_SECRETKEY123"

// rtmpRig is an output configured for RTMP with the test binary as eirl-send.
func rtmpRig(t *testing.T, mode string) *rig {
	t.Helper()
	t.Setenv(fakeSend, mode)
	return newRig(t, func(c *config.Config) {
		c.Transport, c.OutputURL, c.OutputKey, c.SendBin = config.ProtoRTMPS, "rtmps://live.example.com:443/app",
			config.NewSecret(sendKey), os.Args[0]
		c.VCodec = "h265"
		c.ABR = true
	})
}

// RTMP: eirl-send gets the URL in argv and the key only in its environment; on air once it publishes, with
// the ABR on its statistics; the key never reaches the journal.
func TestRTMPSession(t *testing.T) {
	r := rtmpRig(t, "publish")
	r.out.Set(true)
	i := r.waitState(StateOn, "")
	if i.Protocol != config.ProtoRTMPS || i.Codec != "h265" {
		t.Errorf("info: %+v", i)
	}
	b, err := os.ReadFile(filepath.Join(r.dir, "send.args"))
	if err != nil {
		t.Fatal(err)
	}
	args := string(b)
	if !strings.Contains(args, "rtmps://live.example.com:443/app") || !strings.Contains(args, "-stats-fd\n3") ||
		strings.Contains(args, sendKey) {
		t.Errorf("args: %q", args)
	}
	env, _ := os.ReadFile(filepath.Join(r.dir, "send.env"))
	if !strings.Contains(string(env), "EIRL_SEND_KEY="+sendKey) {
		t.Error("the key is not in eirl-send's environment")
	}
	waitFor(t, "sentKbps and RTT", func() bool {
		i := r.out.Info()
		return i.SentKbps != nil && i.RTTMs != nil && *i.RTTMs == 33
	})
	if strings.Contains(r.logs.String(), sendKey) {
		t.Error("the key reached the journal")
	}
}

// A rejected publish and a failed TLS end the session with eirl-send's reason (errors: retried slowly).
func TestRTMPFailures(t *testing.T) {
	for mode, reason := range map[string]string{"reject": ReasonRejected, "tls": ReasonTLSFailed} {
		r := rtmpRig(t, mode)
		r.out.Set(true)
		i := r.waitState(StateError, reason)
		if i.Detail == "" {
			t.Errorf("%s: no detail", mode)
		}
		r.out.Close(2 * time.Second)
	}
}

// A new configuration ends the live session and the next one takes it: from SRT to RTMP without a failure.
func TestSetConfigRestartsTheSession(t *testing.T) {
	r := newRig(t, nil)
	t.Setenv(fakeSend, "publish")
	r.out.Set(true)
	r.waitState(StateOn, "")
	slt := pidOf(t, r.dir, "slt")
	next := *r.cfg
	next.Transport, next.OutputURL, next.OutputKey, next.SendBin = config.ProtoRTMP, "rtmp://127.0.0.1/app",
		config.NewSecret(sendKey), os.Args[0]
	r.out.SetConfig(&next)
	waitFor(t, "eirl-send", func() bool {
		_, err := os.Stat(filepath.Join(r.dir, "send.pid"))
		return err == nil
	})
	i := r.waitState(StateOn, "")
	if i.Protocol != config.ProtoRTMP {
		t.Errorf("protocol: %+v", i)
	}
	waitFor(t, "srt-live-transmit stopped", func() bool { return !alive(slt) })
	if strings.Contains(r.logs.String(), "output_retry") {
		t.Errorf("a reconfiguration counted as a failure:\n%s", r.logs.String())
	}
}

// UDP: no sender, the pipeline sends to the destination's host and port and the output is on at once.
func TestUDPSession(t *testing.T) {
	var mu sync.Mutex
	var sinks []pipeline.Sink
	r := newRig(t, func(c *config.Config) { c.Transport, c.OutputURL = config.ProtoUDP, "rtp://[ff02::1]:5004" })
	r.out.desc = func(_ *config.Config, s pipeline.Sink) string {
		mu.Lock()
		sinks = append(sinks, s)
		mu.Unlock()
		return strings.Replace(testDesc, "filesink location=@FIFO@", "udpsink", 1)
	}
	r.out.Set(true)
	r.waitState(StateOn, "")
	mu.Lock()
	defer mu.Unlock()
	if len(sinks) != 1 || sinks[0] != (pipeline.Sink{Kind: "rtp", Host: "ff02::1", Port: 5004}) {
		t.Errorf("sinks: %+v", sinks)
	}
}

func TestParseEvent(t *testing.T) {
	ev, kv := parseEvent(`event=failed reason=rejected detail="publish refused: \"BadName\""`)
	if ev != "failed" || kv["reason"] != "rejected" || kv["detail"] != `publish refused: "BadName"` {
		t.Errorf("%q %v", ev, kv)
	}
	if ev, _ := parseEvent("connecting to the server"); ev != "" {
		t.Errorf("not an event: %q", ev)
	}
	if ev, kv := parseEvent("event=dropped frames=12 queue_ms=3100"); ev != "dropped" || kv["frames"] != "12" || kv["queue_ms"] != "3100" {
		t.Errorf("%q %v", ev, kv)
	}
}

// RTMP's link: the healthiest, then cable, Wi-Fi, modem; no ranking without links.json.
func TestPickLink(t *testing.T) {
	sel := []uplink{{"wwan0", "10.0.0.2"}, {"wlan0", "192.168.5.20"}, {"eth0", "192.168.1.20"}}
	if _, ok := pickLink(sel, nil); ok {
		t.Error("picked without links.json")
	}
	links := map[string]linkInfo{"wwan0": {"modem", "good"}, "wlan0": {"wifi", "good"}, "eth0": {"ethernet", "weak"}}
	if u, ok := pickLink(sel, links); !ok || u.iface != "wlan0" {
		t.Errorf("got %+v", u)
	}
	links["eth0"] = linkInfo{"ethernet", "good"}
	if u, _ := pickLink(sel, links); u.iface != "eth0" {
		t.Errorf("got %+v", u)
	}
}

func TestReadLinksStale(t *testing.T) {
	p := filepath.Join(t.TempDir(), "links.json")
	write(t, p, `{"v":1,"at":"2020-01-01T00:00:00Z","links":[{"name":"eth0","kind":"ethernet","health":"good"}]}`)
	if readLinks(p) != nil {
		t.Error("a stale links.json was taken")
	}
	write(t, p, `{"v":1,"at":"`+time.Now().UTC().Format(time.RFC3339)+`","links":[{"name":"eth0","kind":"ethernet","health":"good"}]}`)
	if l := readLinks(p); l["eth0"].health != "good" {
		t.Errorf("links: %+v", l)
	}
}

// RIST: ristsender with a leg per link (bonded, bound to each source IP), on air when it connects, its
// statistics converted for the app and the ABR.
func TestRISTSession(t *testing.T) {
	log, err := filepath.Abs("testdata/rist/normal.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EIRL_OUTPUT_FAKE_RIST_LOG", log)
	r := newRig(t, func(c *config.Config) {
		c.Transport, c.OutputURL, c.RISTBin, c.RISTProfile = config.ProtoRIST, "rist://rx.example.com:8000", os.Args[0], "main"
		c.OutputPassphrase, c.OutputLatencyMs, c.RISTLocalPort = config.NewSecret("pskpskpsk1"), 1000, 6100
	})
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.20\nwwan0 10.64.1.2\n")
	r.out.Set(true)
	r.waitState(StateOn, "")
	b, err := os.ReadFile(filepath.Join(r.dir, "rist.args"))
	if err != nil {
		t.Fatal(err)
	}
	args := string(b)
	if !strings.Contains(args, "miface=192.168.1.20&weight=5,rist://rx.example.com:8000?") ||
		!strings.Contains(args, "miface=10.64.1.2&weight=5") || !strings.Contains(args, "udp://127.0.0.1:6100") {
		t.Errorf("args: %q", args)
	}
	waitFor(t, "statistics", func() bool { i := r.out.Info(); return i.SentKbps != nil && i.RTTMs != nil })
	waitFor(t, "srt-stats.json", func() bool {
		b, err := os.ReadFile(r.cfg.SRTStats)
		return err == nil && strings.Contains(string(b), `"link":{"rtt":`)
	})
	if strings.Contains(r.logs.String(), "pskpskpsk1") {
		t.Error("the pre-shared key reached the journal")
	}
}
