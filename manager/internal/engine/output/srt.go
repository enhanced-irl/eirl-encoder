package output

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The reasons of the state's output.reason: a closed list, part of the contract (MIXER.md §5,
// MANAGER.md §3, ENCODER_CLOUD.md §13.3). The app and the cloud translate them.
const (
	ReasonNoDestination    = "no_destination"
	ReasonNoUplinks        = "no_uplinks"
	ReasonRelayUnreachable = "relay_unreachable"
	ReasonRejected         = "rejected"
	ReasonConnectionLost   = "connection_lost"
	ReasonEncoderError     = "encoder_error"
	ReasonInternal         = "internal"
	// OUTPUTS.md §8: RTMPS's TLS, and a certificate not valid yet because the device's clock is behind
	ReasonTLSFailed      = "tls_failed"
	ReasonClockNotSynced = "clock_not_synced"
)

// Reasons is every reason, in the order of engine_output.REASONS, then the output protocols' (OUTPUTS.md §8).
var Reasons = []string{ReasonNoDestination, ReasonNoUplinks, ReasonRelayUnreachable, ReasonRejected,
	ReasonConnectionLost, ReasonEncoderError, ReasonInternal, ReasonTLSFailed, ReasonClockNotSynced}

// States of the state's output.state.
const (
	StateOff        = "off"
	StateConnecting = "connecting"
	StateOn         = "on"
	StateError      = "error"
)

// transient are the reasons the output retries by itself, quickly: while it waits it stays in
// «connecting» with the reason (srtla_send gives up after ~11 s; when the relay is back it reconnects in
// seconds). «error» is for what retrying does not fix (ENCODER_CLOUD §13.3).
var transient = map[string]bool{ReasonRelayUnreachable: true, ReasonConnectionLost: true, ReasonNoUplinks: true,
	ReasonClockNotSynced: true}

var (
	retryWaits          = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second}
	retryTransientWaits = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second}
)

// retryPlan is the state while waiting and the wait after fails (≥ 1) failures in a row with this reason.
func retryPlan(reason string, fails int) (string, time.Duration) {
	if transient[reason] {
		return StateConnecting, retryTransientWaits[clampIndex(fails, len(retryTransientWaits))]
	}
	return StateError, retryWaits[clampIndex(fails, len(retryWaits))]
}

// clampIndex is Python's [min(fails, n) - 1] (fails ≥ 1).
func clampIndex(fails, n int) int {
	return max(min(fails, n), 1) - 1
}

// --- srt-live-transmit's messages ---

var (
	reSecret    = regexp.MustCompile(`(passphrase|streamid)=[^&' ]*`)
	reTime      = regexp.MustCompile(`^\d\d:\d\d:\d\d\.\d+/`)
	reSocket    = regexp.MustCompile(`(@|w)\d+`)
	reSRTLAAddr = regexp.MustCompile(` \(0x[0-9a-f]+\)`)
)

// mask hides the stream id and the passphrase of a line (srt-live-transmit prints its target in
// «Media path: … --> 'srt://…?streamid=…&passphrase=…'»).
func mask(line string) string {
	return reSecret.ReplaceAllString(line, "${1}=***")
}

// connState is what a line of srt-live-transmit says of the connection.
type connState int

const (
	connNone connState = iota
	connConnected
	connError
)

// classify is what a line of srt-live-transmit says of the connection: connected, an error with its
// reason, or nothing. Measured with srt-live-transmit 1.5.7 on the board: «SRT target connected», «SRT
// target disconnected» (it reconnects by itself) and, with a refused passphrase or stream id, bursts of
// «REJECT …».
func classify(line string) (connState, string) {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "target connected"):
		return connConnected, ""
	case strings.Contains(l, "reject"):
		return connError, ReasonRejected
	case strings.Contains(l, "target disconnected"), strings.Contains(l, "connection lost"):
		return connError, ReasonConnectionLost
	case strings.Contains(l, "timed out"), strings.Contains(l, "connection setup failure"), strings.Contains(l, "refused"):
		return connError, ReasonRelayUnreachable
	}
	return connNone, ""
}

// classifySRTLA is why srtla_send gives up (what it prints before exiting: BELABOX 37862da, and irlserver's
// Rust sender, SRTLA.md §9.1), or "". «warning: no available connections» does not count: it says so without
// exiting while no uplink answers, and it cannot tell a relay that is down from uplinks without internet.
func classifySRTLA(line string) string {
	switch {
	case strings.Contains(line, "Failed to establish any initial connections"),
		strings.Contains(line, "Failed to re-establish any connections"),
		strings.Contains(line, "Failed to resolve"):
		return ReasonRelayUnreachable
	case strings.Contains(line, "source ip list"),
		strings.Contains(line, "Failed to parse any IP addresses"),
		strings.Contains(line, "Failed to open and bind to any of the IP addresses"),
		strings.Contains(line, "no uplinks available"),
		strings.Contains(line, "no IPs in list"):
		return ReasonNoUplinks
	case strings.Contains(line, "bind local SRT UDP listener"):
		return ReasonInternal
	}
	return ""
}

var (
	reANSI     = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	reRustTime = regexp.MustCompile(`^\d{4}-\d\d-\d\dT[0-9:.]+Z\s+`)
)

// plainSRTLA is a line of a sender without colours and without the Rust sender's timestamp (the journal has
// its own): «WARN …», «ERROR …» stay.
func plainSRTLA(line string) string {
	return reRustTime.ReplaceAllString(reANSI.ReplaceAllString(line, ""), "")
}

// normalize is the same libsrt line without the time, the socket or the thread: so that it is not
// repeated in the journal, and so that each line of a burst does not change the state.
func normalize(line string) string {
	return reSocket.ReplaceAllString(reTime.ReplaceAllString(line, ""), "${1}")
}

// cut keeps the first n characters (Python's s[:n]: characters, not bytes).
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// --- statistics ---

// statsInterval is srt-live-transmit's -s, which counts packets (of payload bytes): ~perSecond samples per
// second at kbps + abitrate.
func statsInterval(kbps, abitrate, perSecond, payload int) int {
	return (kbps+abitrate)*1000/8/payload/perSecond + 1
}

type rateSample struct {
	t     float64
	bytes int64
}

// rateWindow is the output's real bitrate: the bytes srt-live-transmit sent in the last span seconds.
// Each of its statistics covers a few dozen packets (50-70 ms) and its mbitRate jumps from 2.5 to 8.8
// Mbps from one to the next; this is the true average. Not safe for concurrent use.
type rateWindow struct {
	span    float64
	samples []rateSample
}

// newRateWindow is a window of span seconds.
func newRateWindow(span float64) *rateWindow { return &rateWindow{span: span} }

// add records the bytes of one sample at t (monotonic seconds).
func (w *rateWindow) add(t float64, bytes int64) {
	w.samples = append(w.samples, rateSample{t, bytes})
	i := 0
	for i < len(w.samples) && w.samples[i].t < t-w.span {
		i++
	}
	w.samples = w.samples[i:]
}

// kbps is the kbit/s in the window, false without enough samples (less than half a second of them).
func (w *rateWindow) kbps(now float64) (int, bool) {
	var s []rateSample
	for _, x := range w.samples {
		if x.t >= now-w.span {
			s = append(s, x)
		}
	}
	if len(s) < 2 || s[len(s)-1].t-s[0].t < 0.5 {
		return 0, false
	}
	// The first sample opens the window: its bytes are from before
	var sum int64
	for _, x := range s[1:] {
		sum += x.bytes
	}
	return int(float64(sum) * 8 / (s[len(s)-1].t - s[0].t) / 1000), true
}
