package output

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// RIST output: ristsender (librist v0.2.20, build/rist.sh). These are pure functions: its command line,
// what its log lines say of the connection, and its statistics converted to the sample of the ABR.
//
// What ristsender prints, measured against ristreceiver (testdata/rist; OUTPUTS.md §6):
//   - Everything goes to stderr, one line per message: «<unix time>|<context>|[LEVEL] <text>». The
//     statistics are INFO lines too: with -v below 6 there are none.
//   - It does not exit when the receiver cannot be reached, goes away or refuses it: it keeps trying and
//     reconnects by itself. It exits (with status 0) only when it cannot start: a host that does not
//     resolve or a URL it does not accept.
//   - A receiver that never answers (down, wrong address, firewall, or one that expects encryption from
//     a sender in clear) prints nothing at all: the session's own «no answer after N s» timer applies.

// ristParams is one ristsender run.
type ristParams struct {
	Host      string   // DNS name, IPv4 or IPv6 (without brackets)
	Port      int      // the receiver's port (simple profile: the even one; RTCP goes to Port+1)
	Profile   string   // simple | main ("" = main)
	BufferMs  int      // the recovery buffer, both ends (OUTPUT_LATENCY; ≤ 0 = 1000)
	Secret    string   // pre-shared key, AES-128 (main profile only)
	User      string   // SRP user (main profile only; with Password)
	Password  string   // SRP password
	LocalPort int      // where the output pipeline sends the TS (127.0.0.1)
	StatsMs   int      // interval of the statistics (0 = none)
	Bind      []string // local source IPs, one RIST leg each (empty = the default route)
}

// ristVerbose is ristsender's -v: 6 (INFO) is the lowest level that still prints the connection's
// changes and the statistics; 7 (DEBUG) prints a line per retransmission.
const ristVerbose = 6

// ristBondWeight is each leg's share when there are several (Bind): equal weights balance the packets
// round-robin across the legs; 0 would send every packet on every leg instead (duplication).
const ristBondWeight = 5

// argv is the command line. Secrets go in the URL: ristsender has no other way (OUTPUTS.md §4). The
// values are not escaped: the app validates them to a charset without «&», «,», «=» or spaces
// (OUTPUTS.md §3).
//
// With Bind, each IP is one peer of the same flow («miface=<ip>»: librist binds the leg's socket to that
// source address with a plain bind(), no privileges; src/udp.c). The board's per-source routing (as for
// SRTLA) takes each leg out through its link. Never an interface name: that is SO_BINDTODEVICE, which
// needs CAP_NET_RAW on the board's kernel (4.9).
func (p ristParams) argv(bin string) []string {
	profile, main := "1", true
	if p.Profile == "simple" {
		profile, main = "0", false
	}
	buffer := p.BufferMs
	if buffer <= 0 {
		buffer = 1000
	}
	host := p.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	url := fmt.Sprintf("rist://%s:%d?buffer=%d", host, p.Port, buffer)
	if main && p.Secret != "" {
		url += "&secret=" + p.Secret + "&aes-type=128"
	}
	if main && p.User != "" {
		url += "&username=" + p.User + "&password=" + p.Password
	}
	outs := []string{url}
	if len(p.Bind) > 0 {
		outs = outs[:0]
		for _, ip := range p.Bind {
			leg := url + "&miface=" + ip
			if len(p.Bind) > 1 {
				leg += "&weight=" + strconv.Itoa(ristBondWeight)
			}
			outs = append(outs, leg)
		}
	}
	return []string{bin,
		"-i", fmt.Sprintf("udp://127.0.0.1:%d", p.LocalPort),
		"-o", strings.Join(outs, ","),
		"-p", profile,
		"-S", strconv.Itoa(max(p.StatsMs, 0)),
		"-v", strconv.Itoa(ristVerbose)}
}

// --- log lines ---

var reRISTSecret = regexp.MustCompile(`(secret|password)=[^&,' ]*`)

// maskRIST hides the pre-shared key and the SRP password of a line. ristsender prints its whole -o
// URL when it refuses it («Could not parse peer options for sender: rist://…»). The user name stays:
// it is not a secret, and it is what tells a wrong user from a wrong password.
func maskRIST(line string) string {
	return reRISTSecret.ReplaceAllString(line, "${1}=***")
}

// classifyRIST is what a line of ristsender says of the connection: connected, an error with its reason,
// or nothing. Measured with librist v0.2.20 (testdata/rist):
//
//   - connected: «Connection Status changed for Peer <p>, new status is 0, peer connected count is <n>»,
//     at the start and again when the receiver comes back. With SRP it only comes after «EAP
//     Authentication succeeded».
//   - the receiver went away: «Listening peer <n> timed out after <ms> ms» ~2 s later (the session
//     timeout), then «new status is 1» some seconds after. It keeps sending keepalives and reconnects
//     by itself («… it is now alive again» and «new status is 0»).
//   - a wrong pre-shared key: what the receiver answers does not decrypt, «Malformed packet, rtp flag
//     value is <xx> instead of 0x80.» (a few per second).
//   - a wrong SRP password: «[EAP-SRP] Authentication failed» and «Failed to process EAPOL pkt, return
//     code: -8». An unknown SRP user gives no error at all, only «Waiting for EAP authentication…»
//     about once a second (isRISTAuthWait).
//   - a host that does not resolve: «Failure resolving host <h>: …», «Host <h> cannot be resolved», and
//     it exits.
//   - a source IP it cannot bind (Bind): «Couldn't bind to <ip>:0: Cannot assign requested address».
//     It does NOT exit: it sends from the default route instead, which is never what the session
//     asked for, hence no_uplinks.
//   - a URL it does not accept: «Unknown or invalid parameter <key>», «Could not parse peer options for
//     sender: <url>», and it exits.
//
// With several legs (Bind), one leg that goes away prints the same «timed out» and «new status is 1»
// while the others keep carrying the stream: the session should then believe the statistics (a peer
// that received in the last interval) over these lines.
func classifyRIST(line string) (connState, string) {
	switch {
	case strings.Contains(line, "new status is 0"), strings.Contains(line, "it is now alive again"):
		return connConnected, ""
	case strings.Contains(line, "Malformed packet, rtp flag value"),
		strings.Contains(line, "[EAP-SRP] Authentication failed"),
		strings.Contains(line, "Failed to process EAPOL pkt"):
		return connError, ReasonRejected
	case strings.Contains(line, "Listening peer") && strings.Contains(line, "timed out"),
		strings.Contains(line, "new status is 1"):
		return connError, ReasonConnectionLost
	case strings.Contains(line, "Failure resolving host"), strings.Contains(line, "cannot be resolved"):
		return connError, ReasonRelayUnreachable
	case strings.Contains(line, "Couldn't bind to"):
		return connError, ReasonNoUplinks
	case strings.Contains(line, "Unknown or invalid parameter"),
		strings.Contains(line, "Could not parse peer options"):
		return connError, ReasonInternal
	}
	return connNone, ""
}

// isRISTAuthWait is the line ristsender repeats about once a second while the receiver answers but SRP
// does not complete: once just before a good authentication, and for ever with a user the receiver does
// not know (it never says so). The session can count them: several in a row, without connecting, is a
// rejection rather than an unreachable relay.
func isRISTAuthWait(line string) bool {
	return strings.Contains(line, "Waiting for EAP authentication")
}

// keepRISTReason is the reason to keep when a line says next while prev is the pending one: a rejection
// is not overwritten by the time-outs that follow it (a receiver that cannot decrypt the sender, or does
// not authenticate it, stops counting as alive and ristsender then says «timed out»).
func keepRISTReason(prev, next string) string {
	if prev == ReasonRejected && (next == ReasonConnectionLost || next == ReasonRelayUnreachable) {
		return prev
	}
	return next
}

// --- statistics ---

// ristStatsLine is ristsender's statistics (schema_version 5), only what the engine reads. Every counter
// is of the LAST interval (-S): librist resets them after each line.
type ristStatsLine struct {
	Sender *struct {
		Peers []struct {
			Type  string `json:"type"` // data | rtcp (the simple profile has one of each)
			Stats struct {
				Sent               float64 `json:"sent"`       // packets, data + retransmissions + RTCP
				Received           float64 `json:"received"`   // RTCP packets from the receiver
				SentBytes          float64 `json:"sent_bytes"` // bytes, the same packets
				RetransmittedBytes float64 `json:"retransmitted_bytes"`
				Bandwidth          float64 `json:"bandwidth"` // bit/s, a moving average
				BandwidthSkipped   float64 `json:"bandwidth_skipped"`
				RetransmitSkipped  float64 `json:"retransmit_skipped"`
				RTT                float64 `json:"rtt"`     // ms, the last one measured (0 = none yet)
				AvgRTT             float64 `json:"avg_rtt"` // ms, smoothed (before any RTT: the configured minimum)
			} `json:"stats"`
		} `json:"peers"`
	} `json:"sender-stats"`
}

// ristStats is one statistic converted: the ABR's sample plus the output rate for srt-stats.json.
type ristStats struct {
	sample
	mbps float64
}

// parseRIST reads a statistics line of ristsender; false if the line is not one or no peer has measured
// an RTT yet (not connected: there is nothing to adapt to).
func parseRIST(line string) (ristStats, bool) {
	i := strings.Index(line, `{"schema_version"`)
	if i < 0 {
		return ristStats{}, false
	}
	var d ristStatsLine
	if json.Unmarshal([]byte(strings.TrimSpace(line[i:])), &d) != nil || d.Sender == nil {
		return ristStats{}, false
	}
	var s ristStats
	var sent, resent float64
	rttLive, rttAny := -1.0, -1.0
	for _, p := range d.Sender.Peers {
		st := p.Stats
		sent += st.SentBytes
		resent += st.RetransmittedBytes
		s.mbps += st.Bandwidth / 1e6
		s.dropped += int(st.RetransmitSkipped + st.BandwidthSkipped)
		if st.RTT > 0 {
			rttAny = math.Max(rttAny, st.AvgRTT)
			if st.Received > 0 {
				rttLive = math.Max(rttLive, st.AvgRTT)
			}
		}
	}
	switch {
	case rttLive >= 0:
		s.rtt = rttLive
	case rttAny >= 0:
		s.rtt = rttAny
	default:
		return ristStats{}, false
	}
	if sent > 0 {
		s.bufMs = 1000 * resent / sent
	}
	s.bytes, s.hasBytes = int64(sent), true
	return s, true
}

// parseRISTStats is a statistics line of ristsender as the ABR's sample, or false (parseRIST). RIST has
// no send buffer like SRT's (it sends each datagram as it arrives and keeps a copy for retransmissions),
// so the mapping is:
//
//   - rtt: the smoothed RTT (avg_rtt) of the peers that heard from the receiver in the interval; the
//     highest, as with several legs the slowest one sets the delay. If none heard from it, the last
//     known one.
//   - bufMs: the retransmission pressure as time: the bytes resent per second of bytes sent
//     (1000 × retransmitted_bytes / sent_bytes, so 50 = 5 % of the output was resent). It does not
//     depend on -S. On a clean link it is 0, under 5 % loss ~60, under 25 % loss ~200-250 (measured).
//   - dropped: the packets the receiver asked for that the sender could not resend (retransmit_skipped:
//     too old, too many retries, or not in the buffer; bandwidth_skipped: over the bandwidth cap). The
//     receiver very likely lost them: the closest thing to SRT's dropped packets.
//   - bytes: every byte sent in the interval (data, retransmissions and RTCP), like SRT's.
func parseRISTStats(line string) (sample, bool) {
	s, ok := parseRIST(line)
	return s.sample, ok
}

// ristSRTStats is a statistics line of ristsender in the shape of srt-live-transmit's (OUTPUTS.md §4),
// for srt-stats.json; parseSample reads back the same sample as parseRISTStats.
func ristSRTStats(line string) ([]byte, bool) {
	s, ok := parseRIST(line)
	if !ok {
		return nil, false
	}
	type link struct {
		RTT float64 `json:"rtt"`
	}
	type send struct {
		Bytes          int64   `json:"bytes"`
		MsBuf          float64 `json:"msBuf"`
		PacketsDropped int     `json:"packetsDropped"`
		MbitRate       float64 `json:"mbitRate"`
	}
	b, err := json.Marshal(struct {
		Link link `json:"link"`
		Send send `json:"send"`
	}{link{s.rtt}, send{s.bytes, s.bufMs, s.dropped, round(s.mbps, 3)}})
	if err != nil {
		return nil, false
	}
	return append(b, '\n'), true
}
