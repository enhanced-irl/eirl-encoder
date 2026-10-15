package output

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testdata/rist holds ristsender's stderr, raw, from librist v0.2.20 (build/rist.sh) in linux/arm64
// containers on loopback (15-oct): ffmpeg (testsrc2 + sine, H.264 + AAC, ~1.7 Mbit/s) → UDP
// 127.0.0.1:6100 → ristsender -p <profile> -S 1000 -v 6 → ristreceiver → UDP → ffprobe. The scenarios:
//
//	normal              main profile, the receiver up
//	unreachable_host    nobody at the address (10.255.255.1)
//	dns                 a host that does not resolve
//	bad_args            a URL parameter it does not know
//	peer_lost           the receiver killed 5 s in and started again 15 s later
//	psk_wrong           both ends with a pre-shared key, different ones
//	psk_missing         the receiver with a key, the sender in clear
//	srp_ok, srp_wrong   SRP user and password right, the password wrong
//	srp_nouser          SRP with a user the receiver does not know
//	simple              simple profile (-p 0)
//	simple_peer_lost    simple profile, the receiver killed 4 s in and started again 12 s later
//	loss                5 % loss and 20 ms each way (netem on lo)
//	heavy_loss          25 % loss, 40 ms each way and a 300 ms buffer
//	congested           20 ms each way and a 1.2 Mbit/s bottleneck (tbf) under a ~1.8 Mbit/s stream
//	bind_balance        two legs bound to 127.0.0.2 and 127.0.0.3 (miface, weight=5), as nobody
//	bind_nonlocal       one leg bound to an address the host does not have (10.9.9.9)
//
// To refresh them for a new librist: the same chain with the binaries of build/out/rist.

func ristFixture(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "rist", name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func TestRISTArgv(t *testing.T) {
	base := ristParams{Host: "rist.example.com", Port: 8000, BufferMs: 1500, LocalPort: 6100, StatsMs: 500}
	tail := func(profile string) []string { return []string{"-p", profile, "-S", "500", "-v", "6"} }
	for _, c := range []struct {
		name string
		p    func(p *ristParams)
		out  string
		prof string
	}{
		{"main, nothing else", func(p *ristParams) {}, "rist://rist.example.com:8000?buffer=1500", "1"},
		{"main with key and SRP", func(p *ristParams) {
			p.Profile, p.Secret, p.User, p.Password = "main", "Correct-Horse-1", "encoder1", "Right-pass-123"
		}, "rist://rist.example.com:8000?buffer=1500&secret=Correct-Horse-1&aes-type=128&username=encoder1&password=Right-pass-123", "1"},
		{"simple: no encryption nor SRP", func(p *ristParams) {
			p.Profile, p.Secret, p.User, p.Password = "simple", "Correct-Horse-1", "encoder1", "x"
		}, "rist://rist.example.com:8000?buffer=1500", "0"},
		{"default buffer", func(p *ristParams) { p.BufferMs = 0 }, "rist://rist.example.com:8000?buffer=1000", "1"},
		{"IPv6", func(p *ristParams) { p.Host = "2001:db8::1" }, "rist://[2001:db8::1]:8000?buffer=1500", "1"},
		{"one link", func(p *ristParams) { p.Bind = []string{"192.0.2.10"} },
			"rist://rist.example.com:8000?buffer=1500&miface=192.0.2.10", "1"},
		{"two links: balanced", func(p *ristParams) { p.Secret, p.Bind = "Correct-Horse-1", []string{"192.0.2.10", "198.51.100.7"} },
			"rist://rist.example.com:8000?buffer=1500&secret=Correct-Horse-1&aes-type=128&miface=192.0.2.10&weight=5," +
				"rist://rist.example.com:8000?buffer=1500&secret=Correct-Horse-1&aes-type=128&miface=198.51.100.7&weight=5", "1"},
	} {
		p := base
		c.p(&p)
		want := append([]string{"/usr/local/bin/ristsender", "-i", "udp://127.0.0.1:6100", "-o", c.out}, tail(c.prof)...)
		if got := p.argv("/usr/local/bin/ristsender"); !slices.Equal(got, want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, want)
		}
	}
}

func TestMaskRIST(t *testing.T) {
	p := ristParams{Host: "192.0.2.1", Port: 8000, Secret: "Correct-Horse-1", User: "encoder1", Password: "Right-pass-123",
		Bind: []string{"192.0.2.10", "198.51.100.7"}}
	line := "1792051099.353739|0.0|[ERROR] Could not parse peer options for sender: " + p.argv("ristsender")[4]
	got := maskRIST(line)
	if strings.Contains(got, "Correct-Horse-1") || strings.Contains(got, "Right-pass-123") {
		t.Fatalf("a secret is left: %q", got)
	}
	if strings.Count(got, "secret=***") != 2 || strings.Count(got, "password=***") != 2 || !strings.Contains(got, "username=encoder1") {
		t.Errorf("maskRIST = %q", got)
	}
	// The real line, without secrets, stays as it is
	for _, l := range ristFixture(t, "bad_args") {
		if maskRIST(l) != l {
			t.Errorf("maskRIST changed %q", l)
		}
	}
}

// Single lines, each from the fixtures
func TestClassifyRIST(t *testing.T) {
	for _, c := range []struct {
		line   string
		st     connState
		reason string
	}{
		{"1792050919.501137|0.0|[INFO] Connection Status changed for Peer 187650495599056, new status is 0, peer connected count is 1", connConnected, ""},
		{"1792051001.712030|0.281473104359440|[INFO] Peer 1 was dead for 0 ms and it is now alive again", connConnected, ""},
		{"1792050988.629433|0.281473104359440|[WARNING] Listening peer 1 timed out after 2022 ms", connError, ReasonConnectionLost},
		{"1792050994.629030|0.0|[INFO] Connection Status changed for Peer 187651472843216, new status is 1, peer connected count is 0", connError, ReasonConnectionLost},
		{"1792050981.231368|0.281473104359440|[ERROR] Malformed packet, rtp flag value is fd instead of 0x80.", connError, ReasonRejected},
		{"1792050981.683165|0.281473104359440|[ERROR] [EAP-SRP] Authentication failed", connError, ReasonRejected},
		{"1792050983.362378|0.281473104359440|[ERROR] Failed to process EAPOL pkt, return code: -8", connError, ReasonRejected},
		{"1792050980.804893|0.281473104359440|[ERROR] Failure resolving host nonexistent.invalid: Name or service not known", connError, ReasonRelayUnreachable},
		{"1792050980.805859|0.281473104359440|[ERROR] Host nonexistent.invalid cannot be resolved", connError, ReasonRelayUnreachable},
		{"1792050950.959855|0.281473104359440|[ERROR] Couldn't bind to 10.9.9.9:0: Cannot assign requested address", connError, ReasonNoUplinks},
		{" Unknown or invalid parameter bogus", connError, ReasonInternal},
		{"1792050980.836410|0.0|[ERROR] Could not parse peer options for sender: rist://127.0.0.1:8000?buffer=1000&bogus=1", connError, ReasonInternal},
		// Nothing: progress, the benign warning of a good SRP session, the steps that follow a real cause
		{"1792050919.493044|0.281473104359440|[INFO] Successfully Authenticated peer 1", connNone, ""},
		{"1792050919.492069|0.281473104359440|[INFO] Initialized Sender Peer, connecting to receiver ...", connNone, ""},
		{"1792050984.610368|0.281473104359440|[WARNING] [EAP-SRP] Failed to receive requested passphrase in a timely manner", connNone, ""},
		{"1792050981.399452|0.281473104359440|[INFO] Waiting for EAP authentication to happen for peer connecting on port 16415", connNone, ""},
		{"1792050980.806155|0.281473104359440|[ERROR] Could not create socket", connNone, ""},
		{"1792050980.806596|0.0|[ERROR] Could not add peer connector to rist://nonexistent.invalid:8000", connNone, ""},
	} {
		if st, r := classifyRIST(c.line); st != c.st || r != c.reason {
			t.Errorf("classifyRIST(%q) = %v %q, want %v %q", c.line, st, r, c.st, c.reason)
		}
	}
}

// ristTransitions replays a fixture as the session would: the state after each line that changes it
// («on» or the pending reason, with keepRISTReason), without repeats.
func ristTransitions(lines []string) []string {
	var out []string
	pending := ""
	for _, l := range lines {
		var now string
		switch st, reason := classifyRIST(l); st {
		case connConnected:
			pending, now = "", "on"
		case connError:
			pending = keepRISTReason(pending, reason)
			now = pending
		default:
			continue
		}
		if len(out) == 0 || out[len(out)-1] != now {
			out = append(out, now)
		}
	}
	return out
}

func TestClassifyRISTFixtures(t *testing.T) {
	for name, want := range map[string][]string{
		"normal":           {"on"},
		"unreachable_host": nil, // silence: the session's timer says relay_unreachable
		"psk_missing":      nil, // the same: the receiver drops what it cannot decrypt, without answering
		"dns":              {ReasonRelayUnreachable},
		"bad_args":         {ReasonInternal},
		"peer_lost":        {"on", ReasonConnectionLost, "on"},
		"psk_wrong":        {ReasonRejected}, // its «timed out» does not hide the rejection
		"srp_ok":           {"on"},
		"srp_wrong":        {ReasonRejected},
		"srp_nouser":       nil, // only isRISTAuthWait tells it apart (below)
		"simple":           {"on"},
		"simple_peer_lost": {"on", ReasonConnectionLost, "on"},
		"loss":             {"on"},
		"heavy_loss":       {"on"},
		"congested":        {"on"},
		"bind_balance":     {"on"},
		"bind_nonlocal":    {ReasonNoUplinks, "on"}, // it connected through the default route
	} {
		if got := ristTransitions(ristFixture(t, name)); !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	count := func(name string) (n int) {
		for _, l := range ristFixture(t, name) {
			if isRISTAuthWait(l) {
				n++
			}
		}
		return n
	}
	if n := count("srp_nouser"); n < 10 {
		t.Errorf("srp_nouser: %d «Waiting for EAP authentication», want one a second", n)
	}
	if n := count("srp_ok"); n != 1 {
		t.Errorf("srp_ok: %d «Waiting for EAP authentication», want 1", n)
	}
}

func TestKeepRISTReason(t *testing.T) {
	for _, c := range [][3]string{
		{"", ReasonConnectionLost, ReasonConnectionLost},
		{ReasonRejected, ReasonConnectionLost, ReasonRejected},
		{ReasonRejected, ReasonRelayUnreachable, ReasonRejected},
		{ReasonConnectionLost, ReasonRejected, ReasonRejected},
		{ReasonRejected, ReasonNoUplinks, ReasonNoUplinks},
	} {
		if got := keepRISTReason(c[0], c[1]); got != c[2] {
			t.Errorf("keepRISTReason(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// Two real statistics lines, field by field
func TestParseRISTStats(t *testing.T) {
	normal := `1792050920.499353|0.0|[INFO] {"schema_version":5,"sender-stats":{"peers":[{"flow_id":2067231546,"id":1,"cname":"444433bf3442@127.0.0.1:8000","type":"data","profile":1,"profile_name":"main","advanced_active":false,"stats":{"quality":100,"sent":210,"ts_null":0,"received":23,"retransmitted":0,"sent_bytes":233312,"retransmitted_bytes":0,"bandwidth":1294453,"retry_bandwidth":0,"ts_nulls_bandwidth":0,"bandwidth_skipped":0,"bloat_skipped":0,"retransmit_skipped":0,"rtt":1.7908340157211917,"avg_rtt":13.510686985487897,"retry_buffer_size":1,"cooldown_time":0,"rtt_muted":false,"rtt_mute_events":0}}],"incoming_queue":{"size":190,"bytesize":226352,"time_length":932,"packets_per_second":203}}}`
	s, ok := parseRISTStats(normal)
	if !ok || s.rtt != 13.510686985487897 || s.bufMs != 0 || s.dropped != 0 || s.bytes != 233312 || !s.hasBytes {
		t.Errorf("normal: %+v %v", s, ok)
	}
	// heavy_loss: 41 resent, 2 that could not be (bloat_skipped, 40, are early repeats: not losses)
	heavy := ristFixture(t, "heavy_loss")
	var line string
	for _, l := range heavy {
		if strings.Contains(l, `"retransmit_skipped":2,`) {
			line = l
			break
		}
	}
	s, ok = parseRISTStats(line)
	if !ok || s.dropped != 2 || s.bufMs < 100 || s.rtt < 40 {
		t.Errorf("heavy_loss: %+v %v (%s)", s, ok, line)
	}
	for _, l := range []string{"", "1792050919.493044|0.281473104359440|[INFO] Successfully Authenticated peer 1",
		`1792050988.629740|0.0|[INFO] {"schema_version":5,"sender-stats":{"peers":[],"incoming_queue":{"size":0,"bytesize":0,"time_length":1133,"packets_per_second":0}}}`,
		`{"schema_version":5,"receiver-stats":{}}`, `{"schema_version":5,"sender-stats":`} {
		if s, ok := parseRISTStats(l); ok {
			t.Errorf("parseRISTStats(%q) = %+v, want false", l, s)
		}
	}
}

// What each scenario's statistics give the ABR
func TestParseRISTStatsFixtures(t *testing.T) {
	type agg struct {
		n, lines         int
		maxRTT, sumBuf   float64
		dropped, minByte int64
	}
	run := func(name string) agg {
		a := agg{minByte: math.MaxInt64}
		for _, l := range ristFixture(t, name) {
			if !strings.Contains(l, `"sender-stats"`) {
				continue
			}
			a.lines++
			s, ok := parseRISTStats(l)
			if !ok {
				continue
			}
			a.n++
			a.maxRTT = math.Max(a.maxRTT, s.rtt)
			a.sumBuf += s.bufMs
			a.dropped += int64(s.dropped)
			a.minByte = min(a.minByte, s.bytes)
		}
		return a
	}
	check := func(name string, cond bool, a agg) {
		t.Helper()
		if !cond {
			t.Errorf("%s: %+v", name, a)
		}
	}
	// Not connected: no sample (only keepalives, RTT never measured)
	for _, name := range []string{"unreachable_host", "psk_wrong", "psk_missing", "srp_nouser"} {
		a := run(name)
		check(name, a.lines >= 10 && a.n == 0, a)
	}
	// A clean link: low RTT, nothing resent, nothing lost
	for _, name := range []string{"normal", "srp_ok", "simple", "bind_balance"} {
		a := run(name)
		check(name, a.n >= 8 && a.maxRTT < 30 && a.sumBuf == 0 && a.dropped == 0, a)
	}
	// 5 % loss: resent (bufMs ~60 on average), all recovered
	a := run("loss")
	check("loss", a.n >= 10 && a.sumBuf/float64(a.n) > 20 && a.sumBuf/float64(a.n) < 150 && a.dropped == 0 && a.maxRTT < 100, a)
	// 25 % loss with a short buffer: much resent and some lost
	a = run("heavy_loss")
	check("heavy_loss", a.n >= 8 && a.sumBuf/float64(a.n) > 100 && a.dropped > 0, a)
	// A bottleneck: the RTT goes to ~1 s and requests go unanswered
	a = run("congested")
	check("congested", a.maxRTT > 500 && a.dropped > 50, a)
	// The receiver gone for 15 s: the peer leaves the statistics (no sample) and comes back
	a = run("peer_lost")
	check("peer_lost", a.lines-a.n >= 10 && a.n >= 10, a)
}

// srt-stats.json: the converted line reads back as the same sample
func TestRISTSRTStats(t *testing.T) {
	n := 0
	for _, name := range []string{"normal", "loss", "heavy_loss", "congested", "simple", "bind_balance", "peer_lost"} {
		for _, l := range ristFixture(t, name) {
			want, ok := parseRISTStats(l)
			b, ok2 := ristSRTStats(l)
			if ok != ok2 {
				t.Fatalf("%s: parseRISTStats %v, ristSRTStats %v: %s", name, ok, ok2, l)
			}
			if !ok {
				continue
			}
			n++
			got, ok := parseSample(string(b))
			if !ok || got != want {
				t.Errorf("%s: %s reads back as %+v %v, want %+v", name, b, got, ok, want)
			}
			if !strings.HasSuffix(string(b), "}\n") || !strings.Contains(string(b), `"mbitRate":`) {
				t.Errorf("%s: %s", name, b)
			}
		}
	}
	if n < 50 {
		t.Errorf("only %d statistics", n)
	}
}
