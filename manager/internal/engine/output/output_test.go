package output

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst/fake"
)

const (
	testRelayIP = "203.0.113.20" // what every relay name resolves to in these tests
	streamID    = "publish:SECRETO"
	passphrase  = "abcdefghijk"
	testDesc    = "appsrc name=ov ! nvvidconv ! nvv4l2h264enc name=venc bitrate=1 ! h264parse ! mux. " +
		"appsrc name=oa ! voaacenc ! mux. mpegtsmux name=mux ! filesink location=" + "@FIFO@"
)

// media is the fake GStreamer, keeping the counters and bridges the output makes.
type media struct {
	*fake.Media
	mu       sync.Mutex
	counters []*counter
	bridges  []*bridge
}

// bridge is the fake bridge with its destination and Free readable while the output runs.
type bridge struct {
	*fake.Bridge
	mu    sync.Mutex
	dst   string
	freed bool
}

func (b *bridge) SetDst(dst gst.Element) {
	b.Bridge.SetDst(dst)
	b.mu.Lock()
	b.dst = ""
	if dst != nil {
		b.dst = dst.Name()
	}
	b.mu.Unlock()
}

func (b *bridge) Free() { b.Bridge.Free(); b.mu.Lock(); b.freed = true; b.mu.Unlock() }

func (b *bridge) dstName() string { b.mu.Lock(); defer b.mu.Unlock(); return b.dst }

func (b *bridge) isFreed() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.freed }

// counter is a buffer counter the test feeds.
type counter struct {
	mu    sync.Mutex
	n, b  uint64
	freed bool
}

func (c *counter) feed(n, b uint64) { c.mu.Lock(); c.n, c.b = c.n+n, c.b+b; c.mu.Unlock() }

func (c *counter) Free() { c.mu.Lock(); c.freed = true; c.mu.Unlock() }

func (c *counter) isFreed() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.freed }

func (c *counter) Take() (uint64, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, b := c.n, c.b
	c.n, c.b = 0, 0
	return n, b
}

func (m *media) NewCounter(el gst.Element, pad string) gst.Counter {
	c := &counter{}
	m.mu.Lock()
	m.counters = append(m.counters, c)
	m.mu.Unlock()
	return c
}

func (m *media) NewBridge(kind gst.BridgeKind, pgm gst.Pipeline, dst gst.Element) gst.Bridge {
	b := &bridge{Bridge: m.Media.NewBridge(kind, pgm, dst).(*fake.Bridge)}
	m.mu.Lock()
	m.bridges = append(m.bridges, b)
	m.mu.Unlock()
	return b
}

func (m *media) bridge(i int) *bridge {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 {
		i += len(m.bridges)
	}
	if i < 0 || i >= len(m.bridges) {
		return nil
	}
	return m.bridges[i]
}

func (m *media) counter() *counter {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.counters) == 0 {
		return nil
	}
	return m.counters[len(m.counters)-1]
}

// logBuf is the journal, for the tests.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }

func (l *logBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

type rig struct {
	t       *testing.T
	dir     string
	cfg     *config.Config
	m       *media
	pgm     *fake.Pipeline
	out     *Output
	logs    *logBuf
	changes atomic.Int32
}

// fixedDesc is a pipeline builder that always gives d.
func fixedDesc(d string) func(*config.Config, pipeline.Sink) string {
	return func(*config.Config, pipeline.Sink) string { return d }
}

// newRig is an output on a fake program, with the test binary as srt-live-transmit and srtla_send.
func newRig(t *testing.T, edit func(c *config.Config)) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{t: t, dir: dir, m: &media{Media: fake.New()}, logs: &logBuf{}}
	elog.SetOutput(r.logs)
	t.Cleanup(func() { elog.SetOutput(nil) })
	t.Setenv(fakeEnv, dir)
	t.Setenv(fakeSLTMode, "connect")
	t.Setenv(fakeSRTLA, "run")
	t.Setenv("EIRL_ENGINE_SRT_TARGET", "srt://leak?streamid="+streamID) // a child must not inherit it
	oldBin, oldStdbuf, oldLookup := sltBin, stdbufArgv, lookupIPv4
	sltBin, stdbufArgv = os.Args[0], nil
	// Every relay name resolves to one address (SRTLA.md §9.7): the tests never ask a real resolver
	lookupIPv4 = func(_ context.Context, host string) ([]net.IP, error) {
		if host == "unresolvable.example" {
			return nil, errors.New("no such host")
		}
		return []net.IP{net.ParseIP(testRelayIP)}, nil
	}
	t.Cleanup(func() { sltBin, stdbufArgv, lookupIPv4 = oldBin, oldStdbuf, oldLookup })
	r.cfg = &config.Config{
		SRTURL: "srt://127.0.0.1:9", SRTMode: "caller", SRTLatencyMs: 2000,
		SRTStreamID: config.NewSecret(streamID), SRTPassphrase: config.NewSecret(passphrase),
		Transport: "srt", VBitrate: 6000, VBitrateMin: 1000, ABitrate: 128,
		SRTStats: filepath.Join(dir, "srt-stats.json"), RunDir: dir,
		SRTLABin: os.Args[0], SRTLALocalPort: 6000, UplinkWait: 1, SRTLASender: "belabox", SRTLARSBin: os.Args[0],
		SRTLAMode: "classic", StateDir: dir,
		UplinksFile: filepath.Join(dir, "available"), UplinksDisabled: filepath.Join(dir, "disabled"),
		UplinksNoInternet: filepath.Join(dir, "nointernet"),
	}
	if edit != nil {
		edit(r.cfg)
	}
	p, err := r.m.Parse("program", "videotestsrc ! appsink name=outv audiotestsrc ! appsink name=outa")
	if err != nil {
		t.Fatal(err)
	}
	r.pgm = p.(*fake.Pipeline)
	r.pgm.FixBase(123456789)
	r.out, err = New(Options{Config: r.cfg, Media: r.m, Program: r.pgm, Desc: fixedDesc(testDesc), RunDir: dir,
		OnChange: func() { r.changes.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !r.out.Close(6 * time.Second) {
			t.Error("output did not close")
		}
		r.out.Release()
	})
	return r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *rig) waitState(state, reason string) Info {
	r.t.Helper()
	var i Info
	waitFor(r.t, state+"/"+reason, func() bool {
		i = r.out.Info()
		return i.State == state && i.Reason == reason
	})
	return i
}

func (r *rig) output() *fake.Pipeline { return r.m.Find("output") }

func pidOf(t *testing.T, dir, role string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, role+".pid"))
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(string(b))
	return n
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func shorten(t *testing.T, v *time.Duration, d time.Duration) {
	old := *v
	*v = d
	t.Cleanup(func() { *v = old })
}

// MessagesTest.test_retry_state: transient, «connecting» with the reason (with SRTLA it alternated
// Conectando ↔ Sin conexión, cloud 13-oct)
func TestRetryState(t *testing.T) {
	for reason, state := range map[string]string{ReasonRelayUnreachable: StateConnecting, ReasonRejected: StateError} {
		r := newRig(t, nil)
		r.out.session = func() error {
			r.out.setState(StateConnecting, "", "")
			return fail(reason, "detalle de "+reason)
		}
		r.out.Set(true)
		i := r.waitState(state, reason)
		if i.Detail != "detalle de "+reason || i.Mode != "on" {
			t.Errorf("%s: %+v", reason, i)
		}
		if !r.out.Close(2 * time.Second) {
			t.Errorf("%s: not closed", reason)
		}
	}
}

// MessagesTest.test_stop_while_waiting_to_retry_is_off: stopping while waiting for a retry left {state:
// error} with the output off (cloud, 13-oct)
func TestStopWhileWaitingToRetryIsOff(t *testing.T) {
	r := newRig(t, nil)
	r.out.session = func() error {
		r.out.setState(StateConnecting, "", "")
		return fail(ReasonRejected, "detalle")
	}
	r.out.Set(true)
	r.waitState(StateError, ReasonRejected)
	r.out.Set(false)
	i := r.waitState(StateOff, "")
	r.out.mu.Lock()
	lastErr := r.out.lastErr
	r.out.mu.Unlock()
	if i.Detail != "" || i.Mode != "off" || lastErr != nil {
		t.Errorf("%+v, last error %v", i, lastErr)
	}
}

// A whole session: connecting → on with the measures, the bridges on the program's taps, the base
// time and the encoder's bitrate; off removes the processes, the pipeline and the files.
func TestSessionConnectsAndStops(t *testing.T) {
	r := newRig(t, nil)
	r.out.Set(true)
	if !r.out.Emitting() {
		t.Error("not emitting")
	}
	i := r.waitState(StateOn, "")
	if i.Kbps == nil || *i.Kbps != 6000 {
		t.Errorf("kbps %v", i.Kbps)
	}
	waitFor(t, "stats", func() bool { i = r.out.Info(); return i.RTTMs != nil && i.SentKbps != nil })
	if *i.RTTMs != 40 {
		t.Errorf("rtt %d", *i.RTTMs)
	}
	op := r.output()
	if op.BaseTime() != 123456789 {
		t.Errorf("output base %d", op.BaseTime())
	}
	if op.State != gst.StatePlaying || op.El("venc").Prop("bitrate") != "6000000" {
		t.Errorf("state %v bitrate %q", op.State, op.El("venc").Prop("bitrate"))
	}
	if strings.Contains(op.Desc, "@FIFO@") || !strings.Contains(op.Desc, filepath.Join(r.dir, "eirl-engine-out.")) {
		t.Errorf("desc %q", op.Desc)
	}
	for i, want := range []string{"ov", "oa"} {
		b := r.m.bridge(i) // one bridge per tap, retargeted to the session (gst.Bridge.SetDst)
		if b == nil {
			t.Fatalf("no bridge %d", i)
		}
		waitFor(t, "bridge to "+want, func() bool { return b.dstName() == want })
		if b.Kind != gst.BridgePass || b.Attached != []string{"outv", "outa"}[i] {
			t.Errorf("bridge %d: kind %v attached %q", i, b.Kind, b.Attached)
		}
	}
	args := read(t, filepath.Join(r.dir, "slt.args"))
	// 1,128-byte packets by default (SRTLA.md §9.5): the stats interval counts more of them per second
	if !strings.HasPrefix(args, "-chunk:1128\n-s:680\n-pf:json\n-statsout:/dev/fd/3\nfile://con\nsrt://127.0.0.1:9?") {
		t.Errorf("args %q", args)
	}
	waitFor(t, "srt-stats.json", func() bool {
		b, _ := os.ReadFile(r.cfg.SRTStats)
		return strings.Contains(string(b), `"rtt":40.4`)
	})
	pid := pidOf(t, r.dir, "slt")

	r.out.Set(false)
	i = r.waitState(StateOff, "")
	if i.Mode != "off" || i.Kbps != nil || r.out.Emitting() {
		t.Errorf("off: %+v", i)
	}
	waitFor(t, "teardown", func() bool { return op.Closed && !alive(pid) })
	for idx, tap := range []string{"outv", "outa"} {
		if b := r.m.bridge(idx); b.Attached != tap || b.dstName() != "" || b.isFreed() {
			t.Errorf("bridge %d after the session: attached %q dst %q freed %v", idx, b.Attached, b.dstName(), b.isFreed())
		}
	}
	if r.m.bridge(2) != nil {
		t.Error("a bridge per session")
	}
	if c := r.m.counter(); c == nil || !c.isFreed() {
		t.Error("the encoder's counter outlives its pipeline")
	}
	waitFor(t, "files removed", func() bool {
		ents, _ := os.ReadDir(r.dir)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "eirl-engine-out.") || e.Name() == "srt-stats.json" || e.Name() == "abr" {
				return false
			}
		}
		return true
	})
	if !strings.Contains(r.logs.String(), "event=output_on_requested") || !strings.Contains(r.logs.String(), "event=srt_connected") {
		t.Errorf("logs:\n%s", r.logs)
	}
	// Again, on the same bridges, retargeted to the new session's appsrcs
	r.out.Set(true)
	r.waitState(StateOn, "")
	waitFor(t, "retargeted", func() bool { return r.m.bridge(0).dstName() == "ov" })
	if r.m.bridge(2) != nil {
		t.Error("a bridge per session")
	}
	r.out.Set(false)
	r.waitState(StateOff, "")
	waitFor(t, "dst dropped", func() bool { return r.m.bridge(0).dstName() == "" && r.m.bridge(1).dstName() == "" })
	r.out.Close(4 * time.Second)
	r.out.Release()
	if !r.m.bridge(0).isFreed() || !r.m.bridge(1).isFreed() {
		t.Error("Release left a bridge")
	}
}

// The 10 s measure: fps into and out of the encoder; on with an encoder that produced nothing ends the
// session (encoder_error) instead of saying «EN DIRECTO» for ever
func TestMeasureAndEncoderRule(t *testing.T) {
	shorten(t, &measureEvery, 400*time.Millisecond)
	r := newRig(t, nil)
	r.out.Set(true)
	r.waitState(StateOn, "")
	r.m.counter().feed(12, 300000)
	r.m.bridge(0).Frames(12) // the video bridge, counting across sessions
	var i Info
	waitFor(t, "rates", func() bool { i = r.out.Info(); return i.EncFPS != nil })
	// 12 frames and 300 kB in 0.4-0.65 s
	if *i.EncFPS < 18 || *i.EncFPS > 30 || *i.InFPS != *i.EncFPS || *i.EncMbps < 3.6 || *i.EncMbps > 6 {
		t.Errorf("rates in %v enc %v mbps %v", *i.InFPS, *i.EncFPS, *i.EncMbps)
	}
	i = r.waitState(StateError, ReasonEncoderError)
	if i.Detail != "el codificador lleva 10 s sin producir" {
		t.Errorf("detail %q", i.Detail)
	}
	if !strings.Contains(r.logs.String(), "event=output_rates in_fps=") || !strings.Contains(r.logs.String(), "enc_fps=0 enc_mbps=0") {
		t.Errorf("logs:\n%s", r.logs)
	}
}

// A refused stream id: the session goes on (srt-live-transmit keeps trying) in «connecting» with the
// reason, and the detail is the libsrt line without time, socket or secrets
func TestRejectedStaysConnecting(t *testing.T) {
	r := newRig(t, nil)
	t.Setenv(fakeSLTMode, "reject")
	r.out.Set(true)
	i := r.waitState(StateConnecting, ReasonRejected)
	if strings.Contains(i.Detail, "14:29") || strings.Contains(i.Detail, "@3200190") || strings.Contains(i.Detail, "SECRETO") {
		t.Errorf("detail %q", i.Detail)
	}
	if !strings.HasPrefix(i.Detail, "SRT:RcvQ:w!W:SRT.cn: @: processConnectResponse") {
		t.Errorf("detail %q", i.Detail)
	}
	time.Sleep(300 * time.Millisecond)
	// one line per 30 s in the journal for the whole burst
	if n := strings.Count(r.logs.String(), "processConnectResponse"); n != 1 {
		t.Errorf("%d rejection lines logged", n)
	}
	before := r.changes.Load()
	time.Sleep(200 * time.Millisecond)
	if r.changes.Load() != before {
		t.Error("each line of the burst changes the state")
	}
}

// srt-live-transmit gives up: the session ends with what it said, and a transient reason retries in
// «connecting»
func TestProcessExitRetries(t *testing.T) {
	r := newRig(t, nil)
	t.Setenv(fakeSLTMode, "exit")
	r.out.Set(true)
	i := r.waitState(StateConnecting, ReasonRelayUnreachable)
	if !strings.HasPrefix(i.Detail, "Connection setup failure: connection timed out") {
		t.Errorf("detail %q", i.Detail)
	}
	waitFor(t, "retry", func() bool { return strings.Contains(r.logs.String(), "event=output_retry reason=relay_unreachable") })
}

// No line from srt-live-transmit for 10 s: the relay does not answer
func TestUnreachableAfterSilence(t *testing.T) {
	shorten(t, &unreachableAfter, 300*time.Millisecond)
	r := newRig(t, nil)
	t.Setenv(fakeSLTMode, "silent")
	r.out.Set(true)
	if i := r.waitState(StateConnecting, ReasonRelayUnreachable); i.Detail != "el relay no responde" {
		t.Errorf("detail %q", i.Detail)
	}
}

func TestNoDestination(t *testing.T) {
	r := newRig(t, func(c *config.Config) { c.SRTURL = "" })
	r.out.Set(true)
	if i := r.waitState(StateError, ReasonNoDestination); i.Detail != "falta el destino SRT" {
		t.Errorf("detail %q", i.Detail)
	}
	if r.output() != nil {
		t.Error("an output pipeline without destination")
	}
}

// A program that cannot start: internal, and the error names the program, never its arguments
func TestStartFailureIsInternal(t *testing.T) {
	r := newRig(t, nil)
	sltBin = filepath.Join(r.dir, "no-such-srt-live-transmit")
	r.out.Set(true)
	i := r.waitState(StateError, ReasonInternal)
	if i.Detail != "srt-live-transmit: no such file or directory" {
		t.Errorf("detail %q", i.Detail)
	}
}

func TestPipelineThatDoesNotBuildIsEncoderError(t *testing.T) {
	r := newRig(t, nil)
	r.m.ParseErr["output"] = &gst.ParseError{Msg: `no element "nvv4l2h264enc"`}
	r.out.Set(true)
	i := r.waitState(StateError, ReasonEncoderError)
	if i.Detail != `no se puede construir la salida: no element "nvv4l2h264enc"` {
		t.Errorf("detail %q", i.Detail)
	}
}

// An ERROR on the output's bus ends the session (encoder_error)
func TestBusErrorIsEncoderError(t *testing.T) {
	r := newRig(t, nil)
	r.out.Set(true)
	r.waitState(StateOn, "")
	r.output().Post(&gst.Message{Type: gst.MessageError, Src: "venc", Text: "Could not write to resource.", Debug: "a\nb: last line"})
	i := r.waitState(StateError, ReasonEncoderError)
	if i.Detail != "Could not write to resource." {
		t.Errorf("detail %q", i.Detail)
	}
	if !strings.Contains(r.logs.String(), `event=output_encoder_error element=venc error="Could not write to resource." debug="b: last line"`) {
		t.Errorf("logs:\n%s", r.logs)
	}
}

// srtlaRig: the shortened timings are restored after the rig closes (cleanups run in reverse)
func srtlaRig(t *testing.T) *rig {
	shortenUplinks(t)
	return newRig(t, func(c *config.Config) {
		c.SRTURL, c.Transport = "srtla://relay.example:5000", "srtla"
	})
}

func shortenUplinks(t *testing.T) {
	shorten(t, &uplinksEvery, 50*time.Millisecond)
	shorten(t, &uplinkPoll, 50*time.Millisecond)
}

// SRTLA: srtla_send with the uplinks of now, SIGHUP when they change, and what it says when it gives up
func TestSRTLASession(t *testing.T) {
	r := srtlaRig(t)
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\nwwan0 10.64.1.2\n")
	r.out.Set(true)
	r.waitState(StateOn, "")
	if got := read(t, filepath.Join(r.dir, "uplinks")); got != "eth0 192.168.1.232\nwwan0 10.64.1.2\n" {
		t.Errorf("uplinks %q", got)
	}
	args := read(t, filepath.Join(r.dir, "srtla.args"))
	if args != "6000\n"+testRelayIP+"\n5000\n"+filepath.Join(r.dir, "srtla-ips") {
		t.Errorf("srtla args %q", args)
	}
	if a := read(t, filepath.Join(r.dir, "slt.args")); !strings.Contains(a, "srt://127.0.0.1:6000?mode=caller&latency=2000&payloadsize=1128&passphrase=") {
		t.Errorf("slt args %q", a)
	}
	write(t, r.cfg.UplinksDisabled, "wwan0\n")
	waitFor(t, "SIGHUP", func() bool { b, _ := os.ReadFile(filepath.Join(r.dir, "srtla.hups")); return string(b) == "1" })
	if got := read(t, filepath.Join(r.dir, "srtla-ips")); got != "192.168.1.232\n" {
		t.Errorf("srtla-ips %q", got)
	}
	if r.out.Info().State != StateOn {
		t.Error("a change of uplinks cut the emission")
	}
	// The last one goes: srtla_send stops and the session ends in no_uplinks, transient
	write(t, r.cfg.UplinksNoInternet, "eth0\n")
	i := r.waitState(StateConnecting, ReasonNoUplinks)
	if i.Detail != "sin enlaces a internet" && i.Detail != "esperando un enlace a internet" {
		t.Errorf("detail %q", i.Detail)
	}
	logs := r.logs.String()
	for _, code := range []string{"event=srtla_uplinks", "event=srtla_no_uplinks", "[srtla] event=srtla_log"} {
		if !strings.Contains(logs, code) {
			t.Errorf("no %s in\n%s", code, logs)
		}
	}
}

func TestSRTLAGivesUp(t *testing.T) {
	r := srtlaRig(t)
	t.Setenv(fakeSRTLA, "fail")
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\n")
	r.out.Set(true)
	i := r.waitState(StateConnecting, ReasonRelayUnreachable)
	if i.Detail != "Failed to establish any initial connections to 203.0.113.20" {
		t.Errorf("detail %q", i.Detail)
	}
	logs := r.logs.String()
	if !strings.Contains(logs, "[srtla] event=srtla_link_down") || !strings.Contains(logs, "event=srtla_single_uplink") {
		t.Errorf("logs:\n%s", logs)
	}
}

func TestSRTLAWaitsForAnUplink(t *testing.T) {
	r := srtlaRig(t)
	r.out.Set(true)
	if i := r.waitState(StateConnecting, ReasonNoUplinks); i.Detail != "esperando un enlace a internet" {
		t.Errorf("detail %q", i.Detail)
	}
	// UPLINK_WAIT (1 s) without one: the session ends with no_uplinks and retries
	waitFor(t, "retry", func() bool { return strings.Contains(r.logs.String(), "event=output_retry reason=no_uplinks") })
	write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\n")
	r.waitState(StateOn, "")
}

// ABR: the controller lowers the encoder's bitrate in PLAYING and leaves it for the app
func TestABRLowersTheEncoder(t *testing.T) {
	r := newRig(t, func(c *config.Config) { c.ABR = true })
	t.Setenv(fakeStatsEnv, `{"link":{"rtt":45},"send":{"msBuf":50,"packetsDropped":3,"bytes":1316}}`)
	r.out.Set(true)
	r.waitState(StateOn, "")
	if a := read(t, filepath.Join(r.dir, "slt.args")); !strings.Contains(a, "-s:32\n") {
		t.Errorf("args %q", a)
	}
	waitFor(t, "minimum", func() bool { return r.output().El("venc").Prop("bitrate") == "1000000" })
	if got := read(t, filepath.Join(r.dir, "abr")); got != "KBPS=1000\nMIN=1000\nMAX=6000\n" {
		t.Errorf("abr %q", got)
	}
	if i := r.out.Info(); *i.Kbps != 1000 {
		t.Errorf("kbps %d", *i.Kbps)
	}
	// One line per 10 s for the severe decreases
	if n := strings.Count(r.logs.String(), "event=abr_down"); n != 1 {
		t.Errorf("%d abr_down lines:\n%s", n, r.logs)
	}
	if !strings.Contains(r.logs.String(), "[abr] event=abr_down from_kbps=6000 to_kbps=3600 cause=severe") {
		t.Errorf("logs:\n%s", r.logs)
	}
}

// Close lets go of the session and its processes within its budget, while emitting
func TestCloseWithinBudget(t *testing.T) {
	r := newRig(t, nil)
	t.Setenv(fakeSLTMode, "noread")
	r.out.Set(true)
	r.waitState(StateOn, "")
	pid := pidOf(t, r.dir, "slt")
	t0 := time.Now()
	if !r.out.Close(4 * time.Second) {
		t.Fatal("not closed")
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Errorf("close took %v", d)
	}
	if alive(pid) || !r.output().Closed || r.out.Info().State != StateOff {
		t.Errorf("after close: alive %v, info %+v", alive(pid), r.out.Info())
	}
	r.out.Set(true) // after Close: nothing starts
	if r.out.Emitting() {
		t.Error("emitting after close")
	}
}

func TestNewNeedsTheTaps(t *testing.T) {
	m := fake.New()
	p, _ := m.Parse("program", "videotestsrc ! appsink name=outv")
	if _, err := New(Options{Config: &config.Config{}, Media: m, Program: p}); err == nil || !strings.Contains(err.Error(), "outa") {
		t.Errorf("err %v", err)
	}
}

// 🔴 The stream id and the passphrase never reach the journal, the state, an error, a formatting of the
// output, a run file, srtla_send's argv or a child's environment
func TestSecretsNeverLeak(t *testing.T) {
	leaks := func(where, s string) {
		t.Helper()
		if strings.Contains(s, "SECRETO") || strings.Contains(s, passphrase) {
			t.Errorf("secret in %s: %s", where, s)
		}
	}
	for _, mode := range []string{"reject", "exit", "connect"} {
		r := srtlaRig(t)
		t.Setenv(fakeSLTMode, mode)
		write(t, r.cfg.UplinksFile, "eth0 192.168.1.232\n")
		r.out.Set(true)
		var states []string
		waitFor(t, mode, func() bool {
			i := r.out.Info()
			b, _ := json.Marshal(i)
			states = append(states, string(b))
			return (i.State == StateOn) || (i.Reason != "" && i.Reason != ReasonNoUplinks)
		})
		time.Sleep(100 * time.Millisecond)
		for _, s := range states {
			leaks("state", s)
		}
		r.out.Close(5 * time.Second)
		leaks("output %+v", fmt.Sprintf("%v %+v %#v", r.out, r.out, r.out))
		leaks("journal", r.logs.String())
		ents, _ := os.ReadDir(r.dir)
		for _, e := range ents {
			if e.Name() == "slt.args" { // srt-live-transmit takes its target in argv, as with Python
				if !strings.Contains(read(t, filepath.Join(r.dir, e.Name())), streamID) {
					t.Error("the fake did not get the target")
				}
				continue
			}
			if b, err := os.ReadFile(filepath.Join(r.dir, e.Name())); err == nil {
				leaks(e.Name(), string(b))
			}
		}
		for _, f := range []string{"slt.env", "srtla.env"} {
			if strings.Contains(read(t, filepath.Join(r.dir, f)), "EIRL_ENGINE_SRT_TARGET") {
				t.Errorf("%s inherits the target", f)
			}
		}
	}
	// An error made from a start failure names the program only
	err := (&startError{name: "srt-live-transmit", err: &os.PathError{Op: "fork/exec", Path: "srt://x?streamid=" + streamID, Err: syscall.ENOENT}}).Error()
	leaks("start error", err)
}

func TestMaskReplacesSecretValuesAnywhere(t *testing.T) {
	r := newRig(t, nil)
	if got := r.out.mask("x publish:SECRETO y abcdefghijk z"); got != "x *** y *** z" {
		t.Errorf("mask %q", got)
	}
}
