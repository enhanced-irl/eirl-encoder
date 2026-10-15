package output

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
)

// The senders of the protocols other than SRT and SRTLA (OUTPUTS.md §4-§6): eirl-send for RTMP and
// RTMPS, ristsender for RIST, and none for UDP, where the pipeline sends by itself.

// sendQueueMs is eirl-send's -max-queue-ms: past it, it drops to the next keyframe.
const sendQueueMs = 3000

// linksStale: an older links.json means the app is not running (MODEMS.md §4.2).
var linksStale = 15 * time.Second

// reEventCode is an eirl-send event's code.
var reEventCode = regexp.MustCompile(`^[a-z_]{1,32}$`)

// runSend is an RTMP or RTMPS session: eirl-send reads the FIFO and publishes (OUTPUTS.md §5).
func (s *session) runSend() error {
	o, cfg := s.o, s.cfg
	fifo, fd, err := s.fifo()
	if err != nil {
		return err
	}
	defer fd.Close()
	args := []string{"rtmp", "-url", cfg.OutputURL, "-max-queue-ms", strconv.Itoa(sendQueueMs)}
	var extra []*os.File
	statsR, statsW, err := s.statsPipe()
	if err != nil {
		return err
	}
	if statsW != nil {
		defer statsW.Close()
		extra = []*os.File{statsW}
		args = append(args, "-stats-fd", "3", "-stats-ms", "250")
	}
	// One TCP connection: through the healthiest link (D-P7). Without a list of links, the default route
	if u, ok := pickLink(o.uplinkFiles(cfg).current(), readLinks(cfg.LinksFile)); ok {
		args = append(args, "-bind", u.ip)
		elog.Event("engine", "send_link", "link", u.iface)
	}
	// The key goes through the environment, never argv (OUTPUTS.md §4)
	p, stderr, err := startChild("eirl-send", cfg.SendBin, args, fd, false, extra, []string{"EIRL_SEND_KEY=" + cfg.OutputKey.Reveal()})
	if err != nil {
		if statsR != nil {
			statsR.Close()
		}
		return fail(ReasonInternal, err.Error())
	}
	for _, w := range extra {
		w.Close()
	}
	s.procs = append(s.procs, p)
	s.goRead(func() { o.readSend(stderr, s.res) }, stderr)
	if statsR != nil {
		s.goRead(func() { o.readStats(cfg, statsR) }, statsR)
	}
	if err := s.startPipeline(pipeline.Sink{}, fifo); err != nil {
		return err
	}
	fd.Close()
	return s.watch()
}

// readSend follows eirl-send's events (`event=<code> key=value…`, OUTPUTS.md §5): on air once it
// publishes; why it gave up when it fails (it exits then, and the session ends with that reason).
func (o *Output) readSend(r io.Reader, res *result) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		line := o.mask(strings.TrimRight(sc.Text(), trailingSpace))
		if line == "" {
			continue
		}
		ev, kv := parseEvent(line)
		if ev == "" {
			elog.Event("send", "send_log", "line", cut(line, 300))
			continue
		}
		elog.Event("send", "send_"+ev, "line", cut(line, 300))
		switch ev {
		case "publishing":
			res.setConn("", "")
			o.mu.Lock()
			o.lastErr = nil
			o.mu.Unlock()
			o.setState(StateOn, "", "")
		case "failed":
			reason := kv["reason"]
			if !slices.Contains(Reasons, reason) {
				reason = ReasonInternal
			}
			res.setConn(reason, cut(kv["detail"], detailMax))
		}
	}
}

// parseEvent splits an `event=<code> key=value key="quoted value"` line (values quoted with Go's %q).
// "" when the line is not an event.
func parseEvent(line string) (string, map[string]string) {
	kv := map[string]string{}
	rest := line
	for rest = strings.TrimLeft(rest, " "); rest != ""; rest = strings.TrimLeft(rest, " ") {
		k, after, ok := strings.Cut(rest, "=")
		if !ok || k == "" || strings.ContainsAny(k, " \"") {
			break
		}
		var v string
		if strings.HasPrefix(after, `"`) {
			q, err := strconv.QuotedPrefix(after)
			if err != nil {
				break
			}
			v, _ = strconv.Unquote(q)
			after = after[len(q):]
		} else {
			v, after, _ = strings.Cut(after, " ")
			after = " " + after
		}
		kv[k] = v
		rest = after
	}
	ev := kv["event"]
	if ev == "" || !reEventCode.MatchString(ev) {
		return "", nil
	}
	return ev, kv
}

// linkInfo is a link of links.json (the app's view, MODEMS.md §4.2): its kind and health.
type linkInfo struct{ kind, health string }

// readLinks reads links.json; nil when it is missing, stale or not understood.
func readLinks(path string) map[string]linkInfo {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 1<<20 {
		return nil
	}
	var f struct {
		At    time.Time `json:"at"`
		Links []struct {
			Name   string `json:"name"`
			Kind   string `json:"kind"`
			Health string `json:"health"`
		} `json:"links"`
	}
	if json.Unmarshal(b, &f) != nil || time.Since(f.At) > linksStale {
		return nil
	}
	out := map[string]linkInfo{}
	for _, l := range f.Links {
		if reIface.MatchString(l.Name) {
			out[l.Name] = linkInfo{kind: l.Kind, health: l.Health}
		}
	}
	return out
}

// pickLink is the uplink for a single-connection protocol (D-P7): the healthiest (good, weak, bad), then
// cable, Wi-Fi, modem, other USB, then the dispatcher's order. Without links.json there is no ranking and
// no pick: the default route decides.
func pickLink(sel []uplink, links map[string]linkInfo) (uplink, bool) {
	if len(sel) == 0 || links == nil {
		return uplink{}, false
	}
	rank := func(u uplink) (int, int) {
		l, ok := links[u.iface]
		if !ok {
			return 3, 4
		}
		h := map[string]int{"good": 0, "weak": 1, "bad": 2}
		k := map[string]int{"ethernet": 0, "wifi": 1, "modem": 2, "usb": 3}
		hr, ok := h[l.health]
		if !ok {
			hr = 3
		}
		kr, ok := k[l.kind]
		if !ok {
			kr = 4
		}
		return hr, kr
	}
	best := sel[0]
	bh, bk := rank(best)
	for _, u := range sel[1:] {
		if h, k := rank(u); h < bh || (h == bh && k < bk) {
			best, bh, bk = u, h, k
		}
	}
	return best, true
}

// runRIST is a RIST session: ristsender takes the TS over UDP on this machine and sends it (OUTPUTS.md §6).
// With two links or more it bonds them: one leg per link, bound to its source IP (the board's per-source
// routing, as for SRTLA), balanced by weight; with one, it goes through that one.
func (s *session) runRIST() error {
	o, cfg := s.o, s.cfg
	host, port, err := splitURLHostPort(cfg.OutputURL)
	if err != nil {
		return fail(ReasonNoDestination, "destino RIST no válido")
	}
	statsMs := 1000
	if cfg.ABR {
		statsMs = 250
	}
	params := ristParams{Host: host, Port: port, Profile: cfg.RISTProfile, BufferMs: cfg.OutputLatencyMs,
		Secret: cfg.OutputPassphrase.Reveal(), User: cfg.OutputUser, Password: cfg.OutputPassword.Reveal(),
		LocalPort: cfg.RISTLocalPort, StatsMs: statsMs}
	sel := o.uplinkFiles(cfg).current()
	for _, u := range sel {
		params.Bind = append(params.Bind, u.ip)
	}
	if len(sel) > 0 {
		elog.Event("engine", "rist_links", "links", uplinksDesc(sel))
	}
	// Its URL holds the secrets: argv is the only way ristsender takes them (an accepted risk, as with
	// srt-live-transmit), and nowhere else
	argv := params.argv(cfg.RISTBin)
	p, out, err := startChild("ristsender", argv[0], argv[1:], nil, true, nil, nil)
	if err != nil {
		return fail(ReasonInternal, err.Error())
	}
	s.procs = append(s.procs, p)
	st := o.newStats(cfg)
	s.goRead(func() { o.readRIST(out, s.res, st, len(params.Bind) > 1) }, out)
	if err := s.startPipeline(pipeline.Sink{Kind: "udp", Host: "127.0.0.1", Port: cfg.RISTLocalPort}, ""); err != nil {
		return err
	}
	return s.watch()
}

// ristAuthWaits: this many «Waiting for EAP authentication» in a row without connecting is a user the
// receiver does not know (it never says so): rejected rather than unreachable.
const ristAuthWaits = 5

// ristBondSilence: bonded, a leg that goes away says «timed out» while the others carry on, so the
// connection is judged by the statistics: none for this long is a lost connection.
var ristBondSilence = 5 * time.Second

// readRIST sends ristsender's log to the journal (masked, each different line at most every logEvery
// s) and takes its statistics and what they say of the connection.
func (o *Output) readRIST(r io.Reader, res *result, st *stats, bonded bool) {
	seen := map[string]float64{}
	pending, waits := "", 0
	lastStats := mono()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		line := o.mask(strings.TrimRight(sc.Text(), trailingSpace))
		if line == "" {
			continue
		}
		now := mono()
		if smp, ok := parseRISTStats(line); ok {
			lastStats = now
			if srt, ok := ristSRTStats(line); ok {
				st.take(append(srt, '\n'), smp, true)
			}
			continue
		}
		if key := normalize(line); seen[key] == 0 || now-seen[key] >= logEvery {
			seen[key] = now
			elog.Event("rist", "rist_log", "line", cut(line, 300))
		}
		cs, reason := classifyRIST(line)
		if isRISTAuthWait(line) {
			if waits++; waits >= ristAuthWaits {
				cs, reason = connError, ReasonRejected
			}
		}
		switch cs {
		case connConnected:
			pending, waits = "", 0
			res.setConn("", "")
			o.mu.Lock()
			o.lastErr = nil
			o.mu.Unlock()
			o.setState(StateOn, "", "")
		case connError:
			if bonded && reason == ReasonConnectionLost && now-lastStats < ristBondSilence.Seconds() {
				continue // one leg went away; the others still carry the stream
			}
			pending = keepRISTReason(pending, reason)
			d := cut(normalize(line), detailMax)
			res.setConn(pending, d)
			o.setState(StateConnecting, pending, d)
		}
	}
}

// runUDP is a UDP or RTP session: the pipeline sends to the network by itself, with nobody to answer.
func (s *session) runUDP() error {
	o, cfg := s.o, s.cfg
	host, port, err := splitURLHostPort(cfg.OutputURL)
	if err != nil {
		return fail(ReasonNoDestination, "destino UDP no válido")
	}
	kind := "udp"
	if strings.HasPrefix(cfg.OutputURL, "rtp://") {
		kind = "rtp"
	}
	if err := s.startPipeline(pipeline.Sink{Kind: kind, Host: host, Port: port}, ""); err != nil {
		return err
	}
	o.mu.Lock()
	o.lastErr = nil
	o.mu.Unlock()
	o.setState(StateOn, "", "")
	return s.watch()
}

// splitURLHostPort is the host (without the brackets of an IPv6) and port of scheme://host:port.
func splitURLHostPort(u string) (string, int, error) {
	_, hp, ok := strings.Cut(u, "://")
	if !ok {
		return "", 0, fmt.Errorf("no scheme")
	}
	host, ps, err := net.SplitHostPort(hp)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("bad port")
	}
	return host, port, nil
}
