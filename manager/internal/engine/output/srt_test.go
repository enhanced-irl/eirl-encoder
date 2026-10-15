package output

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// Real lines of srt-live-transmit 1.5.7 on the board (12-oct)
const reject = "14:29:44.823900/SRT:RcvQ:w1!W:SRT.cn: @32001902: processConnectResponse: rejecting per reception " +
	"of a rejection HS response: ERROR:BADSECRET"

var reject2 = strings.NewReplacer("14:29:44.823900", "14:29:44.835266", "w1", "w2", "32001902", "32001901").Replace(reject)

// MessagesTest.test_classify
func TestClassify(t *testing.T) {
	for _, c := range []struct {
		line   string
		st     connState
		reason string
	}{
		{"SRT target connected", connConnected, ""},
		{"SRT target disconnected", connError, ReasonConnectionLost},
		{reject, connError, ReasonRejected},
		{"Connection setup failure: connection timed out", connError, ReasonRelayUnreachable},
		{"Media path: 'file://con' --> 'srt://…'", connNone, ""},
	} {
		if st, r := classify(c.line); st != c.st || r != c.reason {
			t.Errorf("classify(%q) = %v %q, want %v %q", c.line, st, r, c.st, c.reason)
		}
	}
}

// MessagesTest.test_classify_srtla: srtla_send's messages (BELABOX 37862da) before exiting; the
// cloud's with dev, 13-oct
func TestClassifySRTLA(t *testing.T) {
	for line, want := range map[string]string{
		"Failed to establish any initial connections to 203.0.113.20":            ReasonRelayUnreachable,
		"Failed to re-establish any connections to 203.0.113.20":                 ReasonRelayUnreachable,
		"Failed to resolve relay.dev.enhancedirl.com: Name or service not known": ReasonRelayUnreachable,
		"Failed to open and bind to any of the IP addresses in /run/x/srtla-ips": ReasonNoUplinks,
		"warning: no available connections":                                      "", // it does not exit: not a reason
		"192.168.1.232: connection established":                                  "",
	} {
		if got := classifySRTLA(line); got != want {
			t.Errorf("classifySRTLA(%q) = %q, want %q", line, got, want)
		}
	}
}

// MessagesTest.test_normalize_dedupes_libsrt_bursts
func TestNormalizeDedupesLibsrtBursts(t *testing.T) {
	if normalize(reject) != normalize(reject2) {
		t.Errorf("%q != %q", normalize(reject), normalize(reject2))
	}
}

// MessagesTest.test_mask
func TestMask(t *testing.T) {
	line := "Media path: 'file://con' --> 'srt://127.0.0.1:6000?mode=caller&latency=2000&streamid=publish:SECRETO&passphrase=abcdefghijk'"
	m := mask(line)
	if strings.Contains(m, "SECRETO") || strings.Contains(m, "abcdefghijk") || !strings.Contains(m, "streamid=***") {
		t.Errorf("mask = %q", m)
	}
}

// MessagesTest.test_stats_interval: ~1 sample per second at 6000 + 128 kbps; with ABR, ~4 per second
// at the minimum
func TestStatsInterval(t *testing.T) {
	if got := statsInterval(6000, 128, 1); got != 583 {
		t.Errorf("got %d", got)
	}
	if got := statsInterval(1000, 128, 4); got != 27 {
		t.Errorf("got %d", got)
	}
	if statsInterval(0, 0, 1) <= 0 {
		t.Error("not positive")
	}
}

// MessagesTest.test_rate_window
func TestRateWindow(t *testing.T) {
	w := newRateWindow(2.0)
	if _, ok := w.kbps(0.0); ok {
		t.Error("kbps without samples")
	}
	for i := range 31 { // 750 B every 50 ms = 120 kbit/s, even if it jumps from sample to sample
		b := int64(250 + 1000)
		if i%2 == 1 {
			b = 750
		}
		w.add(float64(i)*0.05, b)
	}
	if k, ok := w.kbps(1.5); !ok || k <= 150 || k >= 220 {
		t.Errorf("kbps = %d %v", k, ok)
	}
	w.add(10.0, 1000)
	if _, ok := w.kbps(10.0); ok { // the old samples leave the window: not enough to measure
		t.Error("kbps with a single sample")
	}
}

// MessagesTest.test_retry_plan
func TestRetryPlan(t *testing.T) {
	type plan struct {
		state string
		wait  time.Duration
	}
	get := func(r string, n int) plan { s, w := retryPlan(r, n); return plan{s, w} }
	s := time.Second
	var got []plan
	for _, n := range []int{1, 2, 3, 4, 9} {
		got = append(got, get(ReasonRelayUnreachable, n))
	}
	want := []plan{{StateConnecting, 2 * s}, {StateConnecting, 4 * s}, {StateConnecting, 8 * s}, {StateConnecting, 10 * s}, {StateConnecting, 10 * s}}
	if !slices.Equal(got, want) {
		t.Errorf("relay_unreachable: %v", got)
	}
	if p := get(ReasonNoUplinks, 1); p != (plan{StateConnecting, 2 * s}) {
		t.Errorf("no_uplinks: %v", p)
	}
	if p := get(ReasonConnectionLost, 7); p != (plan{StateConnecting, 10 * s}) {
		t.Errorf("connection_lost: %v", p)
	}
	got = got[:0]
	for _, n := range []int{1, 5, 9} {
		got = append(got, get(ReasonRejected, n))
	}
	if !slices.Equal(got, []plan{{StateError, 2 * s}, {StateError, 30 * s}, {StateError, 30 * s}}) {
		t.Errorf("rejected: %v", got)
	}
	for _, r := range []string{ReasonNoDestination, ReasonEncoderError, ReasonInternal} {
		if p := get(r, 1); p.state != StateError {
			t.Errorf("%s: %v", r, p)
		}
	}
	for r := range transient {
		if !slices.Contains(Reasons, r) {
			t.Errorf("transient %q is not a reason", r)
		}
	}
}

// MessagesTest.test_reasons_are_the_contract, and the output protocols' (OUTPUTS.md §8)
func TestReasonsAreTheContract(t *testing.T) {
	want := []string{"no_destination", "no_uplinks", "relay_unreachable", "rejected", "connection_lost",
		"encoder_error", "internal", "tls_failed", "clock_not_synced"}
	got := slices.Clone(Reasons)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("reasons = %v", got)
	}
}

// Details are cut by characters, like Python's [:200], never in the middle of one
func TestCutCountsCharacters(t *testing.T) {
	s := strings.Repeat("ñ", 250)
	if c := cut(s, detailMax); c != strings.Repeat("ñ", 200) {
		t.Errorf("cut: %d bytes", len(c))
	}
	if cut("abc", 200) != "abc" {
		t.Error("short string changed")
	}
}
