package output

import (
	"bufio"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

// The SRTLA senders (SRTLA.md §9): BELABOX's in C, and irlserver's in Rust, which reports statistics per
// link over a control socket. Both take the same arguments, reread the IPs file on SIGHUP and print the
// same lines when they give up.

const (
	senderIRLServer = "irlserver"
	senderBELABOX   = "belabox"

	fileSRTLASock  = "srtla.sock"       // the Rust sender's control socket
	fileSRTLALinks = "srtla-links.json" // the statistics per link, for the app (SRTLA.md §9.3)
	linksFileMax   = 16
)

// Variables so that the tests can replace them.
var (
	executable = func(path string) bool { return syscall.Access(path, 0x1) == nil } // X_OK
	statsDial  = 10 * time.Second                                                   // how long the control socket may take to appear
	statsRetry = 200 * time.Millisecond
)

// sender is the SRTLA sender of a session and how to run it.
type sender struct {
	kind string // senderIRLServer | senderBELABOX
	bin  string
	argv []string // after bin
	env  []string
	sock string // the control socket (irlserver), "" for the C one
}

// pickSender is the sender the configuration asks for, or the other one when that one is not installed
// (an update that landed half-way does not leave the encoder without SRTLA). addr is the relay's address.
func pickSender(cfg *config.Config, runDir, addr, ips string) (sender, error) {
	bins := map[string]string{senderIRLServer: cfg.SRTLARSBin, senderBELABOX: cfg.SRTLABin}
	want := cfg.SRTLASender
	if want != senderBELABOX {
		want = senderIRLServer
	}
	kind := want
	if !executable(bins[kind]) {
		other := senderBELABOX
		if kind == senderBELABOX {
			other = senderIRLServer
		}
		if !executable(bins[other]) {
			return sender{}, errors.New("no SRTLA sender is installed")
		}
		elog.Event("engine", "srtla_sender_fallback", "wanted", want, "using", other)
		kind = other
	}
	pos := []string{strconv.Itoa(cfg.SRTLALocalPort), addr, strconv.Itoa(cfg.SRTLAPort()), ips}
	if kind == senderBELABOX {
		// printf: without a terminal it writes nothing until its buffer fills
		argv := append(append(append([]string{}, stdbufArgv...), bins[kind]), pos...)
		return sender{kind: kind, bin: argv[0], argv: argv[1:]}, nil
	}
	mode := cfg.SRTLAMode
	if mode != "enhanced" {
		mode = "classic"
	}
	sock := filepath.Join(runDir, fileSRTLASock)
	argv := []string{"--mode", mode}
	if net.ParseIP(addr) != nil {
		// Given an address there is no name to follow: when all its uplinks are dead it exits, and the next
		// session resolves the name again (SRTLA.md §9.7). Given the name (it did not resolve), it keeps its re-home
		argv = append(argv, "--no-rehome")
	}
	argv = append(append(argv, "--control-socket", sock), pos...)
	return sender{kind: kind, bin: bins[kind], argv: argv, sock: sock,
		env: []string{"RUST_LOG=warn", "NO_COLOR=1", "TOKIO_WORKER_THREADS=2"}}, nil
}

// --- the statistics per link ---

// rsLink is one link of the Rust sender's StatsSnapshot: only what the engine passes on.
type rsLink struct {
	IP        string  `json:"ip"`
	Connected bool    `json:"connected"`
	TimedOut  bool    `json:"timed_out"`
	RTTMs     float64 `json:"rtt_ms"`
	NAKCount  float64 `json:"nak_count"`
	BytesPS   float64 `json:"bitrate_bytes_per_sec"`
	Weak      bool    `json:"weak"`
	Stalled   bool    `json:"stall_gated"`
}

// rsMessage is a line of the control socket: a stats push (method stats.update) or the answer to a get_stats.
type rsMessage struct {
	Method string `json:"method"`
	Params struct {
		Data struct {
			Links []rsLink `json:"links"`
		} `json:"data"`
	} `json:"params"`
	Result *struct {
		Links []rsLink `json:"links"`
	} `json:"result"`
}

// registerPoll is how often get_stats is asked until the sender registers: its pushes come once a second, and
// every moment srt-live-transmit waits after the registration is latency for the whole session (waitRegistered).
var registerPoll = 100 * time.Millisecond

// LinkStat is one link of srtla-links.json (SRTLA.md §9.3).
type LinkStat struct {
	Iface     string  `json:"iface"`
	IP        string  `json:"ip"`
	Bond      string  `json:"bond"` // active | stalled | weak | down
	RTTMs     int     `json:"rttMs"`
	SendKbps  int     `json:"sendKbps"`
	NAKPerSec float64 `json:"nakPerSec"`
}

// LinksFile is srtla-links.json.
type LinksFile struct {
	V      int        `json:"v"`
	Sender string     `json:"sender"`
	Mode   string     `json:"mode"`
	Links  []LinkStat `json:"links"`
}

// nakMark is a link's cumulative NAK count when it was last seen.
type nakMark struct {
	count float64
	t     float64
}

// linkStats turns one snapshot into the file's links: interfaces from inUse (ip → interface), NAKs per second
// from the difference with prev (updated). Links of an IP that is not in use any more are left out.
func linkStats(links []rsLink, inUse map[string]string, prev map[string]nakMark, now float64) []LinkStat {
	var out []LinkStat
	seen := map[string]bool{}
	for _, l := range links {
		iface, ok := inUse[l.IP]
		if !ok || seen[l.IP] || len(out) == linksFileMax {
			continue
		}
		seen[l.IP] = true
		st := LinkStat{Iface: iface, IP: l.IP, Bond: "active",
			RTTMs: clampInt(l.RTTMs, 60000), SendKbps: clampInt(l.BytesPS*8/1000, 1000000)}
		switch {
		case !l.Connected || l.TimedOut:
			st.Bond = "down"
		case l.Stalled:
			st.Bond = "stalled"
		case l.Weak:
			st.Bond = "weak"
		}
		if p, ok := prev[l.IP]; ok && now > p.t && l.NAKCount >= p.count {
			st.NAKPerSec = min(math.Round((l.NAKCount-p.count)/(now-p.t)*10)/10, 100000)
		}
		prev[l.IP] = nakMark{l.NAKCount, now}
		out = append(out, st)
	}
	for ip := range prev {
		if !seen[ip] {
			delete(prev, ip)
		}
	}
	return out
}

func clampInt(v, hi float64) int {
	if v != v || v < 0 { // NaN or negative
		return 0
	}
	return int(math.Round(min(v, hi)))
}

// readInUse is the uplinks the session uses now (the run file uplinks, `<iface> <ipv4>`), by IP.
func readInUse(path string) map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	for _, line := range splitLines(string(b)) {
		p := strings.Fields(line)
		if len(p) >= 2 && reIface.MatchString(p[0]) && reIPv4.MatchString(p[1]) {
			m[p[1]] = p[0]
		}
	}
	return m
}

// followStats subscribes to the Rust sender's statistics and keeps srtla-links.json up to date until done
// closes or the sender goes away. The sender creates its socket a moment after starting: it is retried for
// statsDial. Without statistics the stream is the same: only the app's figures are missing.
//
// The first snapshot fires statsUp, and the first one with a connected uplink is the sender's registration: it
// fires ready.
func (o *Output) followStats(sock, mode string, done <-chan struct{}, ready, statsUp *oneShot) {
	var conn net.Conn
	deadline := time.Now().Add(statsDial)
	for {
		c, err := net.Dial("unix", sock)
		if err == nil {
			conn = c
			break
		}
		if time.Now().After(deadline) {
			elog.Event("engine", "srtla_stats_unavailable", "err", err)
			return
		}
		select {
		case <-done:
			return
		case <-time.After(statsRetry):
		}
	}
	defer conn.Close()
	go func() {
		<-done
		conn.Close() // unblocks the scanner
	}()
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"subscribe","params":{"topic":"stats"}}` + "\n")); err != nil {
		return
	}
	go func() { // until it registers: the only writer after the subscribe
		t := time.NewTicker(registerPoll)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ready.C():
				return
			case <-t.C:
				if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"get_stats"}` + "\n")); err != nil {
					return
				}
			}
		}
	}()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	prev := map[string]nakMark{}
	path := filepath.Join(o.runDir, fileSRTLALinks)
	for sc.Scan() {
		var p rsMessage
		if json.Unmarshal(sc.Bytes(), &p) != nil {
			continue
		}
		links := p.Params.Data.Links
		switch {
		case p.Method == "stats.update":
		case p.Result != nil && p.Result.Links != nil:
			links = p.Result.Links // an answer to get_stats: only for the registration (the file follows the pushes)
		default:
			continue
		}
		statsUp.fire()
		for _, l := range links {
			if l.Connected && !l.TimedOut {
				ready.fire()
				break
			}
		}
		if p.Method != "stats.update" {
			continue
		}
		f := LinksFile{V: 1, Sender: senderIRLServer, Mode: mode,
			Links: linkStats(p.Params.Data.Links, readInUse(filepath.Join(o.runDir, fileUplinks)), prev, mono())}
		if f.Links == nil {
			f.Links = []LinkStat{}
		}
		b, err := json.Marshal(f)
		if err == nil {
			err = sysx.WriteAtomic(path, b, 0o644)
		}
		if err != nil {
			elog.Event("engine", "srtla_stats_write_failed", "err", err)
		}
	}
}
