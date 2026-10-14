package cloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
)

const testToken = "tok_0123456789abcdefghijklmnopqrstuv"

// --- The fake cloud: the backend side of the contract (§3.1 and §4) --------------------------------

type fakeCloud struct {
	t   *testing.T
	srv *httptest.Server

	mu           sync.Mutex
	pairBodies   []map[string]string
	polls        int
	approveAfter int    // pending polls before approving
	deny         string // reason: 403 on the poll
	delivered    bool
	deleted      []string // Authorization of each DELETE /encoders/self
	deleteStatus int
	rejectHTTP   int // answers this to the upgrade instead of accepting it
	upgrades     int
	conns        chan *cloudConn
}

type cloudConn struct {
	c    *websocket.Conn
	msgs chan map[string]any
	done chan struct{}
}

func newFakeCloud(t *testing.T) *fakeCloud {
	fc := &fakeCloud{t: t, conns: make(chan *cloudConn, 8), deleteStatus: 204}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /encoders/pairing", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		fc.mu.Lock()
		fc.pairBodies = append(fc.pairBodies, body)
		fc.mu.Unlock()
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"pairingId": "p1", "code": "ABCD2346", "pollSecret": "s3cret", "expiresIn": 600, "interval": 3})
	})
	mux.HandleFunc("POST /encoders/pairing/{id}/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		fc.mu.Lock()
		defer fc.mu.Unlock()
		if r.PathValue("id") != "p1" || body["pollSecret"] != "s3cret" {
			w.WriteHeader(404)
			return
		}
		fc.polls++
		switch {
		case fc.deny != "":
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]string{"status": "denied", "reason": fc.deny})
		case fc.delivered:
			w.WriteHeader(410)
			json.NewEncoder(w).Encode(map[string]string{"status": "gone"})
		case fc.polls <= fc.approveAfter:
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
		default:
			fc.delivered = true
			json.NewEncoder(w).Encode(map[string]string{"status": "approved", "encoderId": "enc-1", "token": testToken, "accountLabel": "u•••@example.com"})
		}
	})
	mux.HandleFunc("DELETE /encoders/self", func(w http.ResponseWriter, r *http.Request) {
		fc.mu.Lock()
		fc.deleted = append(fc.deleted, r.Header.Get("Authorization"))
		code := fc.deleteStatus
		fc.mu.Unlock()
		w.WriteHeader(code)
	})
	mux.HandleFunc("GET /encoders/connect", func(w http.ResponseWriter, r *http.Request) {
		fc.mu.Lock()
		fc.upgrades++
		reject := fc.rejectHTTP
		fc.mu.Unlock()
		if reject != 0 {
			w.WriteHeader(reject)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(401)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}})
		if err != nil {
			return
		}
		if c.Subprotocol() != Subprotocol {
			c.Close(4426, "subprotocol")
			return
		}
		c.SetReadLimit(MaxMessage)
		cc := &cloudConn{c: c, msgs: make(chan map[string]any, 256), done: make(chan struct{})}
		fc.conns <- cc
		defer close(cc.done)
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(data, &m) == nil {
				cc.msgs <- m
			}
		}
	})
	fc.srv = httptest.NewServer(mux)
	t.Cleanup(fc.srv.Close)
	return fc
}

func (fc *fakeCloud) conn(t *testing.T) *cloudConn {
	t.Helper()
	select {
	case cc := <-fc.conns:
		return cc
	case <-time.After(5 * time.Second):
		t.Fatal("the device has not connected")
		return nil
	}
}

func (fc *fakeCloud) noConn(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-fc.conns:
		t.Fatal("the device reconnected and should not have")
	case <-time.After(d):
	}
}

func (cc *cloudConn) send(t *testing.T, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := cc.c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatalf("send to device: %v", err)
	}
}

// next waits for the next message of type typ (drops the rest).
func (cc *cloudConn) next(t *testing.T, typ string) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-cc.msgs:
			if m["type"] == typ {
				return m
			}
		case <-deadline:
			t.Fatalf("%q does not arrive", typ)
			return nil
		}
	}
}

// nextWhere waits for a message of type typ that satisfies ok.
func (cc *cloudConn) nextWhere(t *testing.T, typ string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-cc.msgs:
			if m["type"] == typ && ok(m) {
				return m
			}
		case <-deadline:
			t.Fatalf("%q matching the condition does not arrive", typ)
			return nil
		}
	}
}

func (cc *cloudConn) count(typ string, d time.Duration) int {
	n := 0
	deadline := time.After(d)
	for {
		select {
		case m := <-cc.msgs:
			if m["type"] == typ {
				n++
			}
		case <-deadline:
			return n
		}
	}
}

// --- The fake device ----------------------------------------------------------------------------

type fakeDevice struct {
	mu        sync.Mutex
	cfg       config.Settings
	disabled  []string
	phase     string
	actions   []string
	passwords []string
	unlinked  []bool
	applies   int
	uplinks   []string
	actionErr error
	applyErr  error
	rebootErr error
	journal   chan service.Entry
	jpeg      []byte
	apPolicy  string // the one in its network.json; empty = no eirl-netsetup
}

func newFakeDevice(t *testing.T) *fakeDevice {
	return &fakeDevice{cfg: config.Defaults(), phase: PhaseRehearsal, uplinks: []string{"eth0"}, journal: make(chan service.Entry, 8), jpeg: testJPEG(t)}
}

func (d *fakeDevice) Info() DeviceInfo {
	return DeviceInfo{HardwareID: "0123456789abcdef", Model: "Jetson Nano + TC358743", FirmwareVersion: "v-test", Capabilities: []string{CapSRTLA, CapThumbnail}}
}
func (d *fakeDevice) LanURLs() []string { return []string{"http://jetson.local"} }
func (d *fakeDevice) Status(context.Context) EncoderStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	return EncoderStatus{Phase: d.phase, Service: StatusService{State: "active"}, Output: d.cfg.Output, Transport: "srt", At: time.Now().UTC()}
}
func (d *fakeDevice) Reported() (ReportedConfig, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rep := Reported(d.cfg, d.disabled)
	rep.AccessPoint = d.apPolicy
	return rep, nil
}
func (d *fakeDevice) ApplyConfig(ctx context.Context, w WireConfig, apply bool) (ApplyResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ctx.Err() != nil {
		return ApplyResult{}, ctx.Err()
	}
	next, dis, fe := ApplyWire(d.cfg, w)
	if fe != nil {
		return ApplyResult{}, fe
	}
	d.cfg, d.disabled = next, dis
	d.applies++
	return ApplyResult{Saved: true, Restarted: apply}, d.applyErr
}
func (d *fakeDevice) CanReboot(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rebootErr
}
func (d *fakeDevice) Action(ctx context.Context, name, pw string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.actions = append(d.actions, name)
	if pw != "" {
		d.passwords = append(d.passwords, pw)
	}
	return d.actionErr
}
func (d *fakeDevice) Thumbnail() (Thumbnail, bool) {
	return Thumbnail{JPEG: d.jpeg, Width: 320, Height: 180, At: time.Now().UTC()}, true
}
func (d *fakeDevice) Follow(ctx context.Context, fn func(service.Entry)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-d.journal:
			fn(e)
		}
	}
}
func (d *fakeDevice) Uplinks() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string{}, d.uplinks...)
}
func (d *fakeDevice) Unlinked(ctx context.Context, clear bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.unlinked = append(d.unlinked, clear)
	return nil
}
func (d *fakeDevice) set(fn func(d *fakeDevice)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(d)
}
func (d *fakeDevice) get(fn func(d *fakeDevice)) { d.set(fn) }

func testTiming() Timing {
	return Timing{
		StatusIdle: time.Hour, StatusWatch: 50 * time.Millisecond, ThumbEvery: 50 * time.Millisecond,
		WatchTTL: 400 * time.Millisecond, ReadTimeout: 5 * time.Second, UplinkEvery: 50 * time.Millisecond,
		Debounce: 30 * time.Millisecond, MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		StableAfter: time.Hour, TryLater: 150 * time.Millisecond, PollEvery: 20 * time.Millisecond,
		BurstFor: 400 * time.Millisecond,
	}
}

type harness struct {
	fc    *fakeCloud
	dev   *fakeDevice
	cl    *Client
	state *StateFile
	logs  *lockedBuffer
}

// lockedBuffer: the client's logs (to check they carry no secrets).
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// start: a running client. linked=true starts it already linked (stored state). setup runs before
// Run (to change timings or the cloud without races).
func start(t *testing.T, linked bool, setup ...func(h *harness)) *harness {
	t.Helper()
	fc := newFakeCloud(t)
	dev := newFakeDevice(t)
	dir := t.TempDir()
	st := &StateFile{Path: filepath.Join(dir, "cloud.json")}
	if linked {
		if err := st.Save(State{EncoderID: "enc-1", Token: testToken, APIBase: fc.srv.URL, AccountLabel: "u•••@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	logs := &lockedBuffer{}
	cl := &Client{APIBase: fc.srv.URL, AppBase: "https://app.example", State: st, Device: dev,
		Identity: &IdentityFile{Path: filepath.Join(dir, "identity")},
		Log:      slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Timing: testTiming()}
	h := &harness{fc: fc, dev: dev, cl: cl, state: st, logs: logs}
	for _, f := range setup {
		f(h)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	if err := cl.Init(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { cl.Run(ctx); close(done) }()
	return h
}

// waitUnlinked: dropLink calls Device.Unlinked AFTER setting the view to "unlinked".
func waitUnlinked(t *testing.T, d *fakeDevice) {
	t.Helper()
	waitFor(t, "Device.Unlinked", func() bool {
		var n int
		d.get(func(d *fakeDevice) { n = len(d.unlinked) })
		return n > 0
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- Tests ------------------------------------------------------------------------------------------

func TestPairingLinksAndSaysHello(t *testing.T) {
	h := start(t, false)
	h.fc.approveAfter = 2
	if v := h.cl.View(); v.State != "unlinked" || h.cl.ManagedBy() != "local" {
		t.Fatalf("at startup: %+v", v)
	}
	v, err := h.cl.Pair(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.State != "pairing" || v.Code != "ABCD-2346" || v.LinkURL != "https://app.example/encoders/link?pairing=ABCD-2346" || v.ExpiresAt == nil {
		t.Fatalf("view while pairing: %+v", v)
	}
	h.fc.mu.Lock()
	body := h.fc.pairBodies[0]
	h.fc.mu.Unlock()
	if body["hardwareId"] != "0123456789abcdef" || body["model"] != "Jetson Nano + TC358743" || body["firmwareVersion"] != "v-test" {
		t.Errorf("POST /encoders/pairing body: %v", body)
	}
	identity, _ := h.cl.Identity.LoadOrCreate()
	if body["identity"] != identity || len(identity) != 43 {
		t.Errorf("identity in the pairing: %q", body["identity"])
	}
	cc := h.fc.conn(t)
	hello := cc.next(t, "hello")
	if hello["protocol"] != float64(1) || hello["appliedVersion"] != float64(0) || hello["model"] != "Jetson Nano + TC358743" {
		t.Errorf("hello: %v", hello)
	}
	cfg := hello["config"].(map[string]any)
	if cfg["accessPoint"] != "rescue" || cfg["output"] != "on" || cfg["destination"] != nil {
		t.Errorf("hello.config: %v", cfg)
	}
	if caps := hello["capabilities"].([]any); len(caps) != 2 {
		t.Errorf("capabilities: %v", caps)
	}
	if st := cc.next(t, "status")["status"].(map[string]any); st["phase"] != PhaseRehearsal {
		t.Errorf("first snapshot: %v", st)
	}
	cc.send(t, map[string]string{"type": "ping"})
	cc.next(t, "pong")
	waitFor(t, "online", func() bool { v := h.cl.View(); return v.Online != nil && *v.Online })
	if v := h.cl.View(); v.State != "linked" || v.AccountLabel != "u•••@example.com" || h.cl.ManagedBy() != "eirl" {
		t.Errorf("linked: %+v", v)
	}
	st, ok, err := h.state.Load()
	if !ok || err != nil || st.Token != testToken || st.EncoderID != "enc-1" || st.APIBase != h.fc.srv.URL {
		t.Errorf("stored state: %+v %v %v", st, ok, err)
	}
	if fi, _ := os.Stat(h.state.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("cloud.json with permissions %v", fi.Mode().Perm())
	}
	if _, err := h.cl.Pair(context.Background()); err != ErrAlreadyLinked {
		t.Errorf("linking twice: %v", err)
	}
}

// The board's real policy ("Create my Wi-Fi") goes in hello and config.applied even if the cloud stored
// another one: until step 3 of the safe order (WIFI.md §7) the device does not obey the cloud's.
func TestHelloReportsBoardAPPolicy(t *testing.T) {
	h := start(t, true)
	h.dev.get(func(d *fakeDevice) { d.apPolicy = "always" })
	cc := h.fc.conn(t)
	if cfg := cc.next(t, "hello")["config"].(map[string]any); cfg["accessPoint"] != "always" {
		t.Fatalf("hello.config.accessPoint: %v", cfg["accessPoint"])
	}
	cc.send(t, configSet(1, false, func(m map[string]any) { m["accessPoint"] = "rescue" }))
	a := cc.next(t, "config.applied")
	if a["ok"] != true || a["config"].(map[string]any)["accessPoint"] != "always" {
		t.Fatalf("config.applied: %v", a)
	}
	if st, _, _ := h.state.Load(); st.AccessPoint != "rescue" {
		t.Errorf("the cloud's is stored in cloud.json: %+v", st)
	}
	// Without eirl-netsetup, the one stored from the cloud, as before
	h.dev.get(func(d *fakeDevice) { d.apPolicy = "" })
	cc.send(t, configSet(2, false, nil))
	if a := cc.next(t, "config.applied"); a["config"].(map[string]any)["accessPoint"] != "always" {
		t.Errorf("without netsetup, the cloud's (configSet sends always): %v", a)
	}
}

func TestPairingDenied(t *testing.T) {
	h := start(t, false)
	h.fc.mu.Lock()
	h.fc.deny = "subscription_required"
	h.fc.mu.Unlock()
	if _, err := h.cl.Pair(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "denied", func() bool { return h.cl.View().LastError == "subscription_required" })
	if v := h.cl.View(); v.State != "unlinked" {
		t.Errorf("after denial: %+v", v)
	}
	// Cancel a pairing in progress
	h.fc.mu.Lock()
	h.fc.deny, h.fc.approveAfter = "", 1_000_000
	h.fc.mu.Unlock()
	h.cl.Pair(context.Background())
	if !h.cl.Unlink(context.Background()) || h.cl.View().State != "unlinked" {
		t.Error("cancel the pairing")
	}
}

func TestWatchStatusThumbnailAndExpiry(t *testing.T) {
	h := start(t, true)
	cc := h.fc.conn(t)
	cc.next(t, "hello")
	cc.next(t, "status")
	if n := cc.count("status", 200*time.Millisecond); n != 0 {
		t.Fatalf("without watch, status only every StatusIdle: got %d", n)
	}
	cc.send(t, map[string]any{"type": "watch", "on": true})
	th := cc.next(t, "thumbnail")
	raw, err := base64.StdEncoding.DecodeString(th["jpeg"].(string))
	if err != nil || th["width"] != float64(320) || th["height"] != float64(180) {
		t.Fatalf("thumbnail: %v", th["width"])
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(raw)); err != nil {
		t.Errorf("the thumbnail is not a JPEG: %v", err)
	}
	if n := cc.count("status", 300*time.Millisecond); n < 3 {
		t.Errorf("with watch, status every StatusWatch (50 ms): %d in 300 ms", n)
	}
	// Renew before it expires: it keeps going
	cc.send(t, map[string]any{"type": "watch", "on": true})
	time.Sleep(250 * time.Millisecond)
	if n := cc.count("thumbnail", 100*time.Millisecond); n == 0 {
		t.Error("renewed, it should keep sending thumbnails")
	}
	// Not renewed: it expires after WatchTTL (400 ms)
	time.Sleep(300 * time.Millisecond)
	cc.count("thumbnail", 50*time.Millisecond) // drains whatever was in flight
	if n := cc.count("thumbnail", 300*time.Millisecond); n != 0 {
		t.Errorf("watch expired: %d thumbnails still arriving", n)
	}
	// explicit watch off
	cc.send(t, map[string]any{"type": "watch", "on": true})
	cc.next(t, "thumbnail")
	cc.send(t, map[string]any{"type": "watch", "on": false})
	cc.count("thumbnail", 50*time.Millisecond)
	if n := cc.count("thumbnail", 200*time.Millisecond); n != 0 {
		t.Errorf("watch off: %d thumbnails", n)
	}
}

func configSet(version int, apply bool, mut func(m map[string]any)) map[string]any {
	m := map[string]any{
		"destination": map[string]any{"url": "srt://eu1.relay.enhancedirl.com:5000", "streamId": "publish:" + testKey, "latencyMs": 500},
		"video":       map[string]any{"bitrateKbps": 6000, "adaptive": false, "minBitrateKbps": 1000},
		"audio":       map[string]any{"bitrateKbps": 128},
		"input":       map[string]any{"edidPreferred": "1080p30"},
		"output":      "on",
		"uplinks":     map[string]any{"disabled": []string{}},
		"accessPoint": "always",
	}
	if mut != nil {
		mut(m)
	}
	return map[string]any{"type": "config.set", "version": version, "apply": apply, "config": m}
}

func TestConfigSet(t *testing.T) {
	h := start(t, true)
	cc := h.fc.conn(t)
	cc.next(t, "hello")

	cc.send(t, configSet(1, true, nil))
	a := cc.next(t, "config.applied")
	if a["version"] != float64(1) || a["ok"] != true || a["restarted"] != true {
		t.Fatalf("valid config.set: %v", a)
	}
	h.dev.get(func(d *fakeDevice) {
		if d.cfg.URL != "srt://eu1.relay.enhancedirl.com:5000" || d.cfg.StreamID != "publish:"+testKey || d.cfg.ManagedBy != "eirl" {
			t.Errorf("device config: %+v", d.cfg)
		}
	})
	st, _, _ := h.state.Load()
	if st.AppliedVersion != 1 || !st.CloudDestination || st.AccessPoint != "always" {
		t.Errorf("state after applying: %+v", st)
	}

	cc.send(t, configSet(2, false, func(m map[string]any) {
		m["video"] = map[string]any{"bitrateKbps": 100, "adaptive": false, "minBitrateKbps": 1000}
	}))
	a = cc.next(t, "config.applied")
	if a["ok"] != false || a["errors"].(map[string]any)["video.bitrateKbps"] != CodeInvalid {
		t.Errorf("invalid bitrate: %v", a)
	}

	cc.send(t, configSet(3, true, func(m map[string]any) {
		m["destination"] = map[string]any{"url": "srt://attacker.example.com:5000", "streamId": "publish:" + testKey, "latencyMs": 500}
	}))
	a = cc.next(t, "config.applied")
	if a["ok"] != false || a["errors"].(map[string]any)["destination"] != ErrDestinationNotAllowed {
		t.Errorf("D9: %v", a)
	}
	h.dev.get(func(d *fakeDevice) {
		if d.cfg.URL != "srt://eu1.relay.enhancedirl.com:5000" || d.applies != 1 {
			t.Errorf("a rejected config must not touch anything: %+v (%d)", d.cfg, d.applies)
		}
	})

	cc.send(t, map[string]any{"type": "config.set", "version": 4, "apply": true, "config": map[string]any{"output": "on"}})
	if a = cc.next(t, "config.applied"); a["ok"] != false || a["errors"].(map[string]any)["video"] != CodeInvalid {
		t.Errorf("incomplete config: %v", a)
	}
	cc.send(t, configSet(0, true, nil))
	if a = cc.next(t, "config.applied"); a["ok"] != false || a["errors"].(map[string]any)["version"] != CodeInvalid {
		t.Errorf("version older than the applied one: %v", a)
	}

	h.dev.set(func(d *fakeDevice) { d.applyErr = errors.New("systemctl restart: failed") })
	cc.send(t, configSet(5, true, nil))
	if a = cc.next(t, "config.applied"); a["ok"] != false || a["errors"].(map[string]any)["config"] != CodeApplyFailed {
		t.Errorf("saved but not applied: %v", a)
	}
	h.dev.set(func(d *fakeDevice) { d.applyErr = nil })

	// On reconnect (backend deploy: 1012) hello carries the SUCCESSFULLY applied version (1)
	cc.c.Close(CloseServiceRestart, "deploy")
	cc = h.fc.conn(t)
	hello := cc.next(t, "hello")
	cfg := hello["config"].(map[string]any)
	dest := cfg["destination"].(map[string]any)
	if hello["appliedVersion"] != float64(1) || dest["host"] != "eu1.relay.enhancedirl.com" || dest["streamIdSet"] != true || cfg["accessPoint"] != "always" {
		t.Errorf("hello after reconnecting: %v", hello)
	}
	if b, _ := json.Marshal(hello); strings.Contains(string(b), testKey) {
		t.Error("the key must not go back to the cloud in hello")
	}

	// A config.set WITHOUT destination touches neither the destination on disk nor the flag saying the
	// cloud set it: it is still deleted on unlink (the key does not outlive the link)
	cc.send(t, configSet(6, true, func(m map[string]any) { delete(m, "destination") }))
	if a = cc.next(t, "config.applied"); a["ok"] != true {
		t.Fatalf("config.set without destination: %v", a)
	}
	if st, _, _ := h.state.Load(); !st.CloudDestination {
		t.Errorf("the cloud destination flag was lost: %+v", st)
	}
}

func TestStateFileRejectsHTTPBase(t *testing.T) {
	dir := t.TempDir()
	write := func(base string) *StateFile {
		f := &StateFile{Path: filepath.Join(dir, "cloud.json")}
		if err := f.Save(State{Token: "t", APIBase: base, EncoderID: "e"}); err != nil {
			t.Fatal(err)
		}
		return f
	}
	if _, _, err := write("http://attacker.example.com").Load(); err == nil {
		t.Error("a cloud.json with http:// to another host must not load on the device")
	}
	if _, ok, err := write("https://api.enhancedirl.com").Load(); err != nil || !ok {
		t.Errorf("https: %v", err)
	}
	f := write("http://attacker.example.com")
	f.AllowHTTP = true // the simulator
	if _, ok, err := f.Load(); err != nil || !ok {
		t.Errorf("simulator with http: %v", err)
	}
}

func TestHTTPClientsDoNotFollowRedirects(t *testing.T) {
	var hit atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Store(true) }))
	defer other.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	for name, c := range map[string]*http.Client{"defaultHTTP": defaultHTTP, "wsHTTP": wsHTTP} {
		resp, err := c.Post(redir.URL, "application/json", strings.NewReader(`{"identity":"secret"}`))
		if err != nil {
			t.Fatal(name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusTemporaryRedirect || hit.Load() {
			t.Errorf("%s followed the redirect: %d", name, resp.StatusCode)
		}
	}
}

func TestActions(t *testing.T) {
	h := start(t, true)
	cc := h.fc.conn(t)
	cc.next(t, "hello")
	for i, name := range []string{"start", "stop", "rehearsal", "restart"} {
		id := "a" + string(rune('0'+i))
		cc.send(t, map[string]any{"type": "action", "id": id, "name": name})
		if r := cc.nextWhere(t, "result", func(m map[string]any) bool { return m["id"] == id }); r["ok"] != true {
			t.Errorf("%s: %v", name, r)
		}
	}
	cc.send(t, map[string]any{"type": "action", "id": "pw", "name": "set_local_password", "password": "a-long-password"})
	if r := cc.nextWhere(t, "result", func(m map[string]any) bool { return m["id"] == "pw" }); r["ok"] != true {
		t.Errorf("set_local_password: %v", r)
	}
	cc.send(t, map[string]any{"type": "action", "id": "x", "name": "rm -rf"})
	if r := cc.nextWhere(t, "result", func(m map[string]any) bool { return m["id"] == "x" }); r["ok"] != false || r["error"] != "unknown_action" {
		t.Errorf("unknown action: %v", r)
	}
	h.dev.set(func(d *fakeDevice) { d.actionErr = &ActionError{Code: "no_destination", Message: "no destination"} })
	cc.send(t, map[string]any{"type": "action", "id": "e", "name": "start"})
	if r := cc.nextWhere(t, "result", func(m map[string]any) bool { return m["id"] == "e" }); r["ok"] != false || r["error"] != "no_destination" || r["message"] != "no destination" {
		t.Errorf("failed action: %v", r)
	}
	h.dev.set(func(d *fakeDevice) { d.actionErr = nil })
	// reboot: answered BEFORE rebooting
	cc.send(t, map[string]any{"type": "action", "id": "rb", "name": "reboot"})
	if r := cc.nextWhere(t, "result", func(m map[string]any) bool { return m["id"] == "rb" }); r["ok"] != true {
		t.Errorf("reboot: %v", r)
	}
	h.dev.get(func(d *fakeDevice) {
		if strings.Contains(strings.Join(d.actions, ","), "reboot") {
			t.Error("reboot run before answering")
		}
	})
	waitFor(t, "reboot run", func() bool {
		var ok bool
		h.dev.get(func(d *fakeDevice) { ok = strings.Contains(strings.Join(d.actions, ","), "reboot") })
		return ok
	})
	h.dev.get(func(d *fakeDevice) {
		if strings.Join(d.actions, ",") != "start,stop,rehearsal,restart,set_local_password,start,reboot" || strings.Join(d.passwords, ",") != "a-long-password" {
			t.Errorf("actions: %v %v", d.actions, d.passwords)
		}
	})
}

func TestEventsAndPhaseChanges(t *testing.T) {
	h := start(t, true)
	cc := h.fc.conn(t)
	cc.next(t, "hello")
	cc.next(t, "status")
	ok := true
	h.dev.journal <- service.Entry{Kind: "audio_recovery", Msg: "audio software recovery 'nco': AU_STATUS0=0x61", Step: "nco", OK: &ok}
	ev := cc.next(t, "event")
	d := ev["detail"].(map[string]any)
	if ev["name"] != "audio_recovery" || d["step"] != "nco" || d["ok"] != true || d["message"] == nil {
		t.Errorf("journal event: %v", ev)
	}
	// A line that is not an event (start) is not forwarded, but triggers a phase check: rehearsal → streaming
	h.dev.set(func(d *fakeDevice) { d.phase = PhaseStreaming })
	h.dev.journal <- service.Entry{Kind: "start", Msg: "Started Encoder"}
	st := cc.next(t, "status")["status"].(map[string]any)
	if st["phase"] != PhaseStreaming {
		t.Errorf("snapshot on phase change: %v", st)
	}
	if ev := cc.next(t, "event"); ev["name"] != EventStreamStarted {
		t.Errorf("stream_started: %v", ev)
	}
	h.dev.set(func(d *fakeDevice) { d.phase = PhaseWaitingSignal })
	h.dev.journal <- service.Entry{Kind: "signal_lost", Msg: "HDMI signal lost"}
	if ev := cc.next(t, "event"); ev["name"] != "signal_lost" {
		t.Errorf("signal_lost: %v", ev)
	}
	if ev := cc.next(t, "event"); ev["name"] != EventStreamStopped || ev["detail"].(map[string]any)["phase"] != PhaseWaitingSignal {
		t.Errorf("stream_stopped: %v", ev)
	}
	// Uplinks
	h.dev.set(func(d *fakeDevice) { d.uplinks = []string{"eth0", "usb0"} })
	if ev := cc.next(t, "event"); ev["name"] != EventUplinkUp || ev["detail"].(map[string]any)["uplink"] != "usb0" {
		t.Errorf("uplink_up: %v", ev)
	}
	h.dev.set(func(d *fakeDevice) { d.uplinks = []string{"usb0"} })
	if ev := cc.next(t, "event"); ev["name"] != EventUplinkDown || ev["detail"].(map[string]any)["uplink"] != "eth0" {
		t.Errorf("uplink_down: %v", ev)
	}
}

func TestCloseCodes(t *testing.T) {
	t.Run("1013 retries after a wait", func(t *testing.T) {
		h := start(t, true)
		cc := h.fc.conn(t)
		cc.next(t, "hello")
		at := time.Now()
		cc.c.Close(CloseTryLater, "maintenance")
		cc = h.fc.conn(t)
		cc.next(t, "hello")
		if d := time.Since(at); d < 140*time.Millisecond {
			t.Errorf("after 1013 it reconnects in %v (minimum TryLater = 150 ms)", d)
		}
	})
	t.Run("4409 does not retry", func(t *testing.T) {
		h := start(t, true)
		cc := h.fc.conn(t)
		cc.next(t, "hello")
		cc.c.Close(CloseReplaced, "replaced")
		h.fc.noConn(t, 400*time.Millisecond)
		waitFor(t, "halted", func() bool { return h.cl.View().Halted == "replaced" })
		if v := h.cl.View(); v.State != "linked" || *v.Online {
			t.Errorf("4409 stays linked, without connection: %+v", v)
		}
	})
	t.Run("4401 deletes the token and goes back to unlinked", func(t *testing.T) {
		h := start(t, true)
		cc := h.fc.conn(t)
		cc.next(t, "hello")
		cc.send(t, configSet(1, true, nil))
		cc.next(t, "config.applied")
		cc.c.Close(CloseRevoked, "revoked")
		waitFor(t, "unlinked", func() bool { return h.cl.View().State == "unlinked" })
		if v := h.cl.View(); v.LastError != ReasonRevoked || h.cl.ManagedBy() != "local" {
			t.Errorf("after 4401: %+v", v)
		}
		if _, err := os.Stat(h.state.Path); !os.IsNotExist(err) {
			t.Error("cloud.json is still there")
		}
		waitUnlinked(t, h.dev)
		h.dev.get(func(d *fakeDevice) {
			if len(d.unlinked) != 1 || !d.unlinked[0] {
				t.Errorf("the cloud destination must be deleted: %v", d.unlinked)
			}
		})
		h.fc.noConn(t, 300*time.Millisecond)
	})
	t.Run("401 twice on the upgrade = revoked", func(t *testing.T) {
		h := start(t, true, func(h *harness) { h.fc.rejectHTTP = 401 })
		waitFor(t, "unlinked", func() bool { return h.cl.View().State == "unlinked" })
		h.fc.mu.Lock()
		n := h.fc.upgrades
		h.fc.mu.Unlock()
		if n != 2 {
			t.Errorf("upgrades before treating the token as revoked: %d", n)
		}
		if h.cl.View().LastError != ReasonRevoked {
			t.Errorf("reason: %+v", h.cl.View())
		}
		waitUnlinked(t, h.dev)
		h.dev.get(func(d *fakeDevice) {
			if len(d.unlinked) != 1 || d.unlinked[0] {
				t.Errorf("without a cloud destination nothing is deleted: %v", d.unlinked)
			}
		})
	})
	t.Run("a single 401 does not unlink", func(t *testing.T) {
		h := start(t, true, func(h *harness) { h.fc.rejectHTTP = 401 })
		waitFor(t, "first attempt", func() bool { h.fc.mu.Lock(); defer h.fc.mu.Unlock(); return h.fc.upgrades >= 1 })
		h.fc.mu.Lock()
		h.fc.rejectHTTP = 0
		h.fc.mu.Unlock()
		// The second attempt may already have gone out with 401: then it unlinks (correct).
		// What is checked is that with ONE 401 and then success, it stays linked.
		select {
		case cc := <-h.fc.conns:
			cc.next(t, "hello")
			if h.cl.View().State != "linked" {
				t.Error("after a single 401 and reconnecting, it stays linked")
			}
		case <-time.After(5 * time.Second):
			if h.cl.View().State != "unlinked" {
				t.Error("neither reconnects nor unlinks")
			}
		}
	})
	t.Run("unlink from the cloud", func(t *testing.T) {
		h := start(t, true)
		cc := h.fc.conn(t)
		cc.next(t, "hello")
		cc.send(t, map[string]string{"type": "unlink"})
		waitFor(t, "unlinked", func() bool { return h.cl.View().State == "unlinked" })
		if h.cl.View().LastError != ReasonUnlinked {
			t.Errorf("reason: %+v", h.cl.View())
		}
		h.fc.noConn(t, 300*time.Millisecond)
	})
	t.Run("dead connection: nothing from the cloud in ReadTimeout", func(t *testing.T) {
		h := start(t, true, func(h *harness) { h.cl.Timing.ReadTimeout = 200 * time.Millisecond })
		cc := h.fc.conn(t)
		cc.next(t, "hello")
		h.fc.conn(t).next(t, "hello") // the cloud sends nothing (not even ping): the device closes and reconnects
	})
}

func TestLocalUnlink(t *testing.T) {
	h := start(t, true)
	cc := h.fc.conn(t)
	cc.next(t, "hello")
	if !h.cl.Unlink(context.Background()) {
		t.Error("the cloud answered 204: notified")
	}
	h.fc.mu.Lock()
	deleted := append([]string{}, h.fc.deleted...)
	h.fc.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "Bearer "+testToken {
		t.Errorf("DELETE /encoders/self: %v", deleted)
	}
	if v := h.cl.View(); v.State != "unlinked" || v.LastError != "" {
		t.Errorf("after unlinking: %+v", v)
	}
	select {
	case <-cc.done:
	case <-time.After(2 * time.Second):
		t.Error("the channel is still open")
	}
	h.fc.noConn(t, 200*time.Millisecond)

	// No network (the cloud fails): it unlinks anyway
	h2 := start(t, true)
	h2.fc.conn(t).next(t, "hello")
	h2.fc.mu.Lock()
	h2.fc.deleteStatus = 500
	h2.fc.mu.Unlock()
	if h2.cl.Unlink(context.Background()) {
		t.Error("with 500 it is not notified")
	}
	if _, err := os.Stat(h2.state.Path); !os.IsNotExist(err) || h2.cl.View().State != "unlinked" {
		t.Error("the local state is deleted even if the network fails")
	}
}

func TestInitWithBrokenState(t *testing.T) {
	st := &StateFile{Path: filepath.Join(t.TempDir(), "cloud.json")}
	os.WriteFile(st.Path, []byte("{broken"), 0o600)
	cl := &Client{APIBase: "https://api.example", State: st, Device: newFakeDevice(t), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := cl.Init(context.Background()); err != nil || cl.View().State != "unlinked" {
		t.Errorf("an unreadable state does not prevent startup: %v %+v", err, cl.View())
	}
	if _, err := os.Stat(st.Path); err != nil {
		t.Error("it is not deleted (it may be a transient failure)")
	}
}

// pairAndLink requests a code, lets the cloud approve it and waits for the channel.
func pairAndLink(t *testing.T, h *harness) *cloudConn {
	t.Helper()
	h.fc.mu.Lock()
	h.fc.delivered, h.fc.polls = false, 0
	h.fc.mu.Unlock()
	if _, err := h.cl.Pair(context.Background()); err != nil {
		t.Fatal(err)
	}
	cc := h.fc.conn(t)
	cc.next(t, "hello")
	return cc
}

func readIdentity(t *testing.T, h *harness) string {
	t.Helper()
	b, err := os.ReadFile(h.cl.Identity.Path)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// The identity is created once and survives the three ways of unlinking; the cloud always gets the
// same one. Neither the identity nor the token appears in the logs.
func TestIdentitySurvivesUnlink(t *testing.T) {
	h := start(t, false)
	if _, err := os.Stat(h.cl.Identity.Path); !os.IsNotExist(err) {
		t.Fatal("the identity is not created until needed (when requesting a code)")
	}
	cc := pairAndLink(t, h)
	id := readIdentity(t, h)
	if fi, _ := os.Stat(h.cl.Identity.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("identity with permissions %v", fi.Mode().Perm())
	}

	cc.c.Close(CloseRevoked, "revoked") // 4401
	waitFor(t, "unlinked (4401)", func() bool { return h.cl.View().State == "unlinked" })
	waitUnlinked(t, h.dev)
	if readIdentity(t, h) != id {
		t.Error("4401 does not change the identity")
	}

	cc = pairAndLink(t, h)
	cc.send(t, map[string]string{"type": "unlink"})
	waitFor(t, "unlinked (unlink)", func() bool { return h.cl.View().State == "unlinked" })
	if readIdentity(t, h) != id {
		t.Error("unlink does not change the identity")
	}

	pairAndLink(t, h)
	h.cl.Unlink(context.Background())
	if readIdentity(t, h) != id {
		t.Error("unlinking from the web does not change the identity")
	}

	h.fc.mu.Lock()
	bodies := append([]map[string]string{}, h.fc.pairBodies...)
	h.fc.mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("pairings: %d", len(bodies))
	}
	for i, b := range bodies {
		if b["identity"] != id {
			t.Errorf("pairing %d with another identity", i)
		}
	}
	logs := h.logs.String()
	if strings.Contains(logs, id) || strings.Contains(logs, testToken) || strings.Contains(logs, "s3cret") || strings.Contains(logs, "ABCD") {
		t.Errorf("secrets in the logs:\n%s", logs)
	}
}

func TestPairingDeniedHardwareClaimed(t *testing.T) {
	h := start(t, false)
	h.fc.mu.Lock()
	h.fc.deny = "hardware_claimed"
	h.fc.mu.Unlock()
	if _, err := h.cl.Pair(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "denied", func() bool { return h.cl.View().LastError == "hardware_claimed" })
	h.fc.mu.Lock()
	h.fc.deny = "something_new_from_the_backend"
	h.fc.mu.Unlock()
	h.cl.Pair(context.Background())
	waitFor(t, "unknown reason = denied", func() bool { return h.cl.View().LastError == "denied" })
}

func TestIdentityFile(t *testing.T) {
	dir := t.TempDir()
	f := &IdentityFile{Path: filepath.Join(dir, "identity")}
	id, err := f.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(raw) != 32 {
		t.Errorf("identity: 32 bytes in base64url: %q (%v)", id, err)
	}
	if fi, _ := os.Stat(f.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("permissions %v", fi.Mode().Perm())
	}
	again, _ := (&IdentityFile{Path: f.Path}).LoadOrCreate() // another instance (= app restart)
	if again != id {
		t.Error("once created, the identity does not change")
	}
	other, _ := (&IdentityFile{Path: filepath.Join(dir, "other")}).LoadOrCreate()
	if other == id {
		t.Error("two devices, two identities")
	}
	os.WriteFile(f.Path, []byte("broken\n"), 0o600)
	if _, err := f.LoadOrCreate(); err == nil || strings.Contains(err.Error(), "broken") {
		t.Errorf("an invalid identity is an error (without showing the content): %v", err)
	}
	if b, _ := os.ReadFile(f.Path); string(b) != "broken\n" {
		t.Error("an invalid identity is not overwritten")
	}
	h := start(t, false)
	os.WriteFile(h.cl.Identity.Path, []byte("broken"), 0o600)
	if _, err := h.cl.Pair(context.Background()); err != ErrNoIdentity {
		t.Errorf("without a valid identity no code is requested: %v", err)
	}
}

// After a command, with nobody watching (a snapshot every hour in the tests), a phase change that
// arrives later and without a journal line (the start takes a while to get a signal) is sent in the
// burst, not with the next periodic snapshot (60 s on the board: Stopped → Rehearsal showed up ~30 s late).
func TestStatusBurstAfterAction(t *testing.T) {
	h := start(t, true)
	cc := h.fc.conn(t)
	cc.next(t, "hello")
	cc.next(t, "status")
	cc.send(t, map[string]any{"type": "action", "id": "a1", "name": "rehearsal"})
	if r := cc.next(t, "result"); r["ok"] != true {
		t.Fatalf("result: %v", r)
	}
	time.Sleep(120 * time.Millisecond) // after the debounce: the change arrives "late"
	h.dev.set(func(d *fakeDevice) { d.phase = PhaseStreaming })
	st := cc.next(t, "status")["status"].(map[string]any)
	if st["phase"] != PhaseStreaming {
		t.Errorf("the burst did not send the phase change: %v", st)
	}
}
