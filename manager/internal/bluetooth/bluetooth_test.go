package bluetooth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/btaudio"
	"github.com/godbus/dbus/v5"
)

const (
	hpAddr    = "38:18:4C:12:A0:01"
	phoneAddr = "A4:C3:F0:55:21:02"
	hsAddr    = "50:C2:ED:7B:10:03"
	jblAddr   = "FC:58:FA:30:44:04"
	oldAddr   = "00:0D:44:AB:CD:05"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	m      *Manager
	f      *fakeBT
	clk    *clock
	dir    string
	cancel context.CancelFunc
	done   chan struct{}
}

// newRig: the manager over the fake BlueZ of NewSim (without its delay), running.
func newRig(t *testing.T, saved string) *rig {
	t.Helper()
	dir := t.TempDir()
	if saved == "" {
		saved = `{"on": true}` // switched on by the operator (the default is off)
	}
	if saved != "" {
		if err := os.WriteFile(filepath.Join(dir, "bluetooth.json"), []byte(saved), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	clk := &clock{t: time.Date(2026, 10, 15, 22, 0, 0, 0, time.UTC)}
	f := newFake()
	f.add(hpAddr, "WH-1000XM4", "audio-headphones", 0x240404, simHeadphones, true)
	f.add(phoneAddr, "Pixel 8", "phone", 0x5a020c, simPhone, true)
	f.add(hsAddr, "Jabra Evolve 65", "audio-headset", 0x240404, simHeadset, true)
	f.add(jblAddr, "JBL Flip 5", "audio-card", 0x240414, simSpeaker, true).refuse = true
	f.add(oldAddr, "Old speaker", "audio-card", 0x240414, simSpeaker, true).absent = true
	m := newManager(Options{ConfigPath: filepath.Join(dir, "bluetooth.json"), EnginePath: filepath.Join(dir, "engine.json"),
		Name: func() string { return "Stage cam" }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: clk.now,
		Write: writeAtomic}, f)
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{m: m, f: f, clk: clk, dir: dir, cancel: cancel, done: make(chan struct{})}
	go func() { m.Run(ctx); close(r.done) }()
	t.Cleanup(func() { cancel(); <-r.done })
	r.wait(t, "first read", func(s State) bool { return s.Available })
	return r
}

// wait polls the state until ok (kicking a refresh: the fake's ticks may be coalesced).
func (r *rig) wait(t *testing.T, what string, ok func(State) bool) State {
	t.Helper()
	var s State
	for i := 0; i < 300; i++ {
		if s = r.m.State(); ok(s) {
			return s
		}
		r.m.Kick()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: never came; state %+v", what, s)
	return s
}

func dev(s State, addr string) (Device, bool) {
	for _, d := range s.Devices {
		if d.Address == addr {
			return d, true
		}
	}
	return Device{}, false
}

func (r *rig) engine(t *testing.T) btaudio.File {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, "engine.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, ok := btaudio.Parse(b)
	if !ok {
		t.Fatalf("engine file: %s", b)
	}
	return f
}

// engineWhen waits for the engine's file to say ok (it is written right after the state that shows it changes).
func (r *rig) engineWhen(t *testing.T, ok func(btaudio.File) bool) btaudio.File {
	t.Helper()
	var f btaudio.File
	for i := 0; i < 300; i++ {
		if f = r.engine(t); ok(f) {
			return f
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("engine file never came: %+v", f)
	return f
}

func (r *rig) saved(t *testing.T) saved {
	t.Helper()
	var s saved
	b, err := os.ReadFile(filepath.Join(r.dir, "bluetooth.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s // never saved
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("config: %s", b)
	}
	return s
}

// --- pure ---

func TestUsesKindsAndAudio(t *testing.T) {
	if u := uses(simHeadphones); !slices.Equal(u, []string{UseListen, UseMic}) || defaultUse(u) != UseListen {
		t.Error(u)
	}
	if u := uses(simPhone); !slices.Equal(u, []string{UseInput}) || defaultUse(u) != UseInput {
		t.Error(u)
	}
	if u := uses(simHeadset); !slices.Equal(u, []string{UseMic}) || defaultUse(u) != UseMic {
		t.Error(u)
	}
	if u := uses([]string{strings.ToUpper(uuidA2DPSink)}); !slices.Equal(u, []string{UseListen}) {
		t.Error("UUIDs are case-insensitive", u)
	}
	for _, c := range []struct {
		icon  string
		class uint32
		uuids []string
		kind  string
		audio bool
	}{
		{"audio-headphones", 0, nil, KindHeadphones, true},
		{"audio-card", 0, nil, KindSpeaker, true},
		{"phone", 0, nil, KindPhone, true},
		{"", 0x5a020c, nil, KindPhone, true}, // a phone by its class
		{"", 0x240404, nil, KindAudio, true}, // audio class, no profiles yet
		{"", 0, simHeadset, KindHeadset, true},
		{"input-keyboard", 0x002540, nil, KindAudio, false}, // D1: not listed
		{"computer", 0x10010c, nil, KindComputer, false},    // a computer only if it sends audio
		{"computer", 0x10010c, simPhone, KindComputer, true},
	} {
		if k := kindOf(c.icon, c.class, c.uuids); k != c.kind {
			t.Errorf("%+v: kind %s", c, k)
		}
		if a := isAudio(c.icon, c.class, c.uuids); a != c.audio {
			t.Errorf("%+v: audio %v", c, a)
		}
	}
	if addrOfPath("/org/bluez/hci0/dev_38_18_4C_12_A0_01") != hpAddr || addrOfPath("/org/bluez/hci0") != "" ||
		addrOfPath("/org/bluez/hci0/dev_zz") != "" {
		t.Error("addrOfPath")
	}
	for _, c := range []struct {
		transport, mode string
		profile         string
		out, ok         bool
	}{
		{"A2DP-source", "sink", "a2dp", true, true},  // we send to headphones
		{"A2DP-sink", "source", "a2dp", false, true}, // a phone sends to us
		{"HFP-AG", "source", "sco", false, true},     // a headset's microphone
		{"HSP-AG", "sink", "sco", true, true},
		{"HFP-HF", "sink", "sco", true, true},
		{"A2DP-source", "other", "", false, false},
		{"MIDI", "sink", "", false, false},
	} {
		p, ok := pcmOf("/org/bluez/hci0/dev_38_18_4C_12_A0_01", c.transport, c.mode, 150)
		if ok != c.ok || ok && (p.Profile != c.profile || p.Out != c.out || p.Address != hpAddr || p.DelayMs != 150) {
			t.Errorf("%+v: %+v %v", c, p, ok)
		}
	}
}

func TestFailOf(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, FailTimeout},
		{dbusErr("org.bluez.Error.AuthenticationRejected", "Authentication Rejected"), FailRejected},
		{dbusErr("org.bluez.Error.ConnectionAttemptFailed", "Page Timeout"), FailNotReachable},
		{dbusErr("org.bluez.Error.Failed", "Host is down"), FailNotReachable},
		{dbusErr("org.bluez.Error.AuthenticationTimeout", "Authentication Timeout"), FailTimeout},
		{dbusErr("org.freedesktop.DBus.Error.UnknownObject", "no such object"), FailGone},
		{errors.New("something"), FailOther},
	} {
		if got := failOf(c.err); got != c.want {
			t.Errorf("%v: %s, want %s", c.err, got, c.want)
		}
	}
}

// --- the manager over the fake BlueZ ---

func TestUnavailableReasons(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, c := range []struct {
		set    func(*fakeBT)
		reason string
	}{
		{func(f *fakeBT) { f.stack = false }, ReasonNoStack},
		{func(f *fakeBT) { f.adapter = nil }, ReasonNoAdapter},
	} {
		f := newFake()
		c.set(f)
		dir := t.TempDir()
		m := newManager(Options{EnginePath: filepath.Join(dir, "engine.json"), Log: quiet,
			Write: func(p string, b []byte, mode os.FileMode) error { return os.WriteFile(p, b, mode) }}, f)
		if s := m.State(); s.Available || s.Reason != ReasonNoStack {
			t.Error("before the first read", s)
		}
		m.refresh(context.Background())
		s := m.State()
		if s.Available || s.Reason != c.reason || s.Devices == nil {
			t.Errorf("%s: %+v", c.reason, s)
		}
		if !errors.Is(m.Scan(context.Background()), ErrUnavailable) || !errors.Is(m.Act(hpAddr, ActPair), ErrUnavailable) {
			t.Error(c.reason, "orders without Bluetooth")
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "engine.json")); string(b) != `{"v":1,"out":null,"in":[]}`+"\n" {
			t.Errorf("%s: engine file %s", c.reason, b)
		}
	}
}

// Without BlueALSA, Bluetooth can still be managed (switched off, devices forgotten), but nothing is heard.
func TestNoAudioStillManageable(t *testing.T) {
	r := newRig(t, `{"on": true, "uses": {"`+hpAddr+`": "listen"}}`)
	r.f.mu.Lock()
	r.f.audio = false
	r.f.devs[hpAddr].Paired, r.f.devs[hpAddr].Connected = true, true
	r.f.mu.Unlock()
	s := r.wait(t, "no audio", func(s State) bool { return s.Reason == ReasonNoAudio })
	if !s.Available {
		t.Error(s)
	}
	if d, _ := dev(s, hpAddr); d.Ready {
		t.Error("ready without BlueALSA", d)
	}
	r.engineWhen(t, func(f btaudio.File) bool { return f.Out == nil })
	if err := r.m.Act(hpAddr, ActForget); err != nil {
		t.Error("forget without BlueALSA", err)
	}
	if err := r.m.SetOn(context.Background(), false); err != nil {
		t.Error("off without BlueALSA", err)
	}
}

// D4: a window the adapter did not take is not open for the agent either; closing it closes it at once.
func TestVisibleFailureKeepsItClosed(t *testing.T) {
	r := newRig(t, "")
	r.f.mu.Lock()
	r.f.failSet = map[string]error{"Discoverable": errors.New("org.bluez.Error.Failed")}
	r.f.mu.Unlock()
	if err := r.m.SetVisible(context.Background(), true); err == nil {
		t.Fatal("no error")
	}
	if r.m.allowed(phoneAddr) || r.m.State().Visible {
		t.Error("the agent accepts with a window that did not open")
	}
	r.f.mu.Lock()
	pairable := r.f.adapter.Pairable
	r.f.mu.Unlock()
	if pairable {
		t.Error("left pairable after the failure")
	}
}

// The adapter refuses to switch on (rfkill): said in the state, tried again only after powerRetry, logged once.
func TestPowerRefused(t *testing.T) {
	r := newRig(t, "")
	r.f.mu.Lock()
	r.f.failSet = map[string]error{"Powered": errors.New("org.bluez.Error.Blocked")}
	r.f.adapter.Powered = false
	r.f.mu.Unlock()
	s := r.wait(t, "refused", func(s State) bool { return s.PowerFailed })
	if !s.On || s.Powered {
		t.Error(s)
	}
	count := func() int {
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		n := 0
		for _, c := range r.f.calls {
			if c == "set Powered" {
				n++
			}
		}
		return n
	}
	n := count()
	for i := 0; i < 5; i++ {
		r.m.Kick()
		time.Sleep(20 * time.Millisecond)
	}
	if count() != n {
		t.Error("retried before powerRetry", count(), n)
	}
	r.f.mu.Lock()
	r.f.failSet = nil
	r.f.mu.Unlock()
	r.clk.add(powerRetry + time.Second)
	r.wait(t, "on after the retry", func(s State) bool { return s.Powered && !s.PowerFailed })
}

// Switching off fails: nothing saved, so it does not come back by itself at the next refresh.
func TestOffRefusedIsNotSaved(t *testing.T) {
	r := newRig(t, "")
	r.f.mu.Lock()
	r.f.failSet = map[string]error{"Powered": errors.New("org.bluez.Error.Failed")}
	r.f.mu.Unlock()
	if err := r.m.SetOn(context.Background(), false); err == nil {
		t.Fatal("no error")
	}
	if s := r.m.State(); !s.On || r.saved(t).On == nil || !*r.saved(t).On {
		t.Error("a refused change was saved", s)
	}
}

// Security review: a stranger who pairs inside the window does not get the monitor nor a microphone: only a
// device that sends audio gets a use by itself; anything else waits for the operator.
func TestIncomingSinkGetsNoUse(t *testing.T) {
	r := newRig(t, `{"on": true, "uses": {"`+hsAddr+`": "listen"}}`)
	r.f.mu.Lock()
	r.f.devs[hsAddr].UUIDs = simHeadphones
	r.f.devs[hsAddr].Paired, r.f.devs[hsAddr].Connected = true, true
	r.f.trusted[hsAddr] = true
	r.f.mu.Unlock()
	r.engineWhen(t, func(f btaudio.File) bool { return f.Out != nil && f.Out.Addr == hsAddr })
	r.m.SetVisible(context.Background(), true)
	r.clk.add(time.Second)
	r.f.mu.Lock()
	r.f.devs[hpAddr].Paired, r.f.devs[hpAddr].Connected = true, true // a stranger's "headphones"
	r.f.mu.Unlock()
	s := r.wait(t, "window closed", func(s State) bool { return !s.Visible })
	if d, _ := dev(s, hpAddr); d.Use != "" || d.Output {
		t.Error("a device paired from its side got a use", d)
	}
	time.Sleep(50 * time.Millisecond)
	if f := r.engine(t); f.Out == nil || f.Out.Addr != hsAddr {
		t.Error("the stranger took the monitor", f)
	}
}

// The agent authorises audio services only, and only while pairing is allowed.
func TestAgentAudioOnly(t *testing.T) {
	r := newRig(t, "")
	path := dbus.ObjectPath("/org/bluez/hci0/dev_A4_C3_F0_55_21_02")
	hid := "00001124-0000-1000-8000-00805f9b34fb"
	if r.m.ag.AuthorizeService(path, uuidA2DPSource) == nil || r.m.ag.RequestAuthorization(path) == nil {
		t.Error("authorised outside the window")
	}
	r.m.SetVisible(context.Background(), true)
	if r.m.ag.AuthorizeService(path, strings.ToUpper(uuidA2DPSource)) != nil || r.m.ag.RequestAuthorization(path) != nil {
		t.Error("refused inside the window")
	}
	if r.m.ag.AuthorizeService(path, hid) == nil {
		t.Error("HID authorised")
	}
	if _, err := r.m.ag.RequestPasskey(path); err == nil {
		t.Error("passkey accepted")
	}
}

// Off until the operator switches it on: never powered, never pairable.
func TestOffByDefault(t *testing.T) {
	r := newRig(t, `{"uses": {}}`)
	s := r.wait(t, "off", func(s State) bool { return !s.Powered })
	r.f.mu.Lock()
	pairable := r.f.adapter.Pairable
	r.f.mu.Unlock()
	if s.On || pairable {
		t.Error(s, pairable)
	}
}

// A use whose pairing BlueZ no longer has (forgotten outside the app) is dropped.
func TestUsesPrunedWithThePairing(t *testing.T) {
	r := newRig(t, `{"on": true, "uses": {"`+hpAddr+`": "listen"}}`)
	time.Sleep(50 * time.Millisecond)
	if len(r.saved(t).Uses) != 1 {
		t.Error("pruned at once: bluetoothd may not have listed its devices yet")
	}
	r.clk.add(pruneAfter + time.Second)
	r.wait(t, "pruned", func(State) bool { return len(r.saved(t).Uses) == 0 })
}

func TestStartsHidden(t *testing.T) {
	r := newRig(t, "")
	r.f.mu.Lock()
	r.f.adapter.Discoverable, r.f.adapter.Pairable = true, true // something else made it visible
	r.f.mu.Unlock()
	r.m.Kick()
	s := r.wait(t, "hidden again", func(State) bool {
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		return !r.f.adapter.Discoverable && !r.f.adapter.Pairable
	})
	if !s.On || !s.Powered || s.Visible || s.Name != "Stage cam" || len(s.Devices) != 0 {
		t.Error(s)
	}
	r.f.mu.Lock()
	agent, alias := r.f.agentOK, r.f.adapter.Alias
	r.f.mu.Unlock()
	if !agent || alias != "Stage cam" {
		t.Error("agent or alias", agent, alias)
	}
}

func TestSearchPairAndListen(t *testing.T) {
	r := newRig(t, "")
	if err := r.m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := r.wait(t, "found", func(s State) bool { return len(s.Devices) == 5 })
	if !s.Scanning || s.Devices[0].RSSI == nil || *s.Devices[0].RSSI < *s.Devices[4].RSSI {
		t.Error("search, by signal", s)
	}
	if d, _ := dev(s, hpAddr); d.Kind != KindHeadphones || d.Paired || d.Use != "" || !slices.Equal(d.Uses, []string{UseListen, UseMic}) {
		t.Error(d)
	}
	// D4: the agent refuses anyone while the app has not started a pairing nor opened the window
	if r.m.allowed(hpAddr) {
		t.Error("allowed before pairing")
	}
	if err := r.m.Act(hpAddr, ActPair); err != nil {
		t.Fatal(err)
	}
	if !r.m.allowed(hpAddr) || r.m.allowed(phoneAddr) {
		t.Error("the agent must accept the pairing the app started, and only that one")
	}
	s = r.wait(t, "paired and ready", func(s State) bool { d, _ := dev(s, hpAddr); return d.Ready && d.Pending == "" })
	d, _ := dev(s, hpAddr)
	if !d.Paired || !d.Connected || d.Use != UseListen || !d.Output || d.Error != "" {
		t.Error(d)
	}
	if s.Devices[0].Address != hpAddr {
		t.Error("paired first", s.Devices)
	}
	// The others found stay listed after the search stopped for the pairing (seenFor)
	if _, ok := dev(s, phoneAddr); !ok || s.Scanning {
		t.Error("the found ones vanished", s)
	}
	f := r.engineWhen(t, func(f btaudio.File) bool { return f.Out != nil })
	if f.Out.Addr != hpAddr || f.Out.Profile != "a2dp" || f.Out.DelayMs != 150 || f.Out.Name != "WH-1000XM4" || len(f.In) != 0 {
		t.Errorf("engine %+v", f)
	}
	if r.saved(t).Uses[hpAddr] != UseListen || !r.f.trusted[hpAddr] {
		t.Error("use not saved or not trusted", r.saved(t), r.f.trusted)
	}
	// Its microphone instead: hands-free both ways
	if err := r.m.SetUse(hpAddr, UseMic); err != nil {
		t.Fatal(err)
	}
	r.wait(t, "mic", func(State) bool {
		f := r.engine(t)
		return f.Out != nil && f.Out.Profile == "sco" && len(f.In) == 1 && f.In[0].Profile == "sco"
	})
	if !errors.Is(r.m.SetUse(hpAddr, UseInput), ErrInvalid) || !errors.Is(r.m.SetUse(hpAddr, "loud"), ErrInvalid) ||
		!errors.Is(r.m.SetUse(phoneAddr, UseInput), ErrNotPaired) || !errors.Is(r.m.SetUse("11:22:33:44:55:66", UseListen), ErrUnknown) {
		t.Error("SetUse checks")
	}
	// Disconnect: nothing ready for the engine
	if err := r.m.Act(hpAddr, ActDisconnect); err != nil {
		t.Fatal(err)
	}
	r.wait(t, "disconnected", func(s State) bool { d, _ := dev(s, hpAddr); return !d.Connected && d.Pending == "" })
	r.engineWhen(t, func(f btaudio.File) bool { return f.Out == nil && len(f.In) == 0 })
}

// Found on the board: a pairing the encoder starts must bond (the adapter bondable while it runs), or BlueZ keeps no
// key and the device is unpaired when the adapter comes back. And the adapter is not pairable again afterwards.
func TestOwnPairingBonds(t *testing.T) {
	r := newRig(t, "")
	r.m.Scan(context.Background())
	r.wait(t, "found", func(s State) bool { return len(s.Devices) == 5 })
	if err := r.m.Act(hpAddr, ActPair); err != nil {
		t.Fatal(err)
	}
	r.wait(t, "paired", func(s State) bool { d, _ := dev(s, hpAddr); return d.Ready && d.Pending == "" })
	r.wait(t, "not pairable again", func(State) bool {
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		return !r.f.adapter.Pairable
	})
	r.f.restart()
	s := r.wait(t, "back", func(s State) bool { d, ok := dev(s, hpAddr); return ok && !d.Connected })
	if d, _ := dev(s, hpAddr); !d.Paired {
		t.Error("the pairing did not survive the adapter coming back", d)
	}
}

func TestPairFailures(t *testing.T) {
	r := newRig(t, "")
	r.m.Scan(context.Background())
	r.wait(t, "found", func(s State) bool { return len(s.Devices) == 5 })
	for addr, want := range map[string]string{jblAddr: FailRejected, oldAddr: FailNotReachable} {
		if err := r.m.Act(addr, ActPair); err != nil {
			t.Fatal(err)
		}
		s := r.wait(t, "failed", func(s State) bool { d, _ := dev(s, addr); return d.Error != "" })
		if d, _ := dev(s, addr); d.Error != want || d.Paired || d.Pending != "" {
			t.Error(addr, d)
		}
		if r.m.allowed(addr) {
			t.Error("still allowed after the pairing ended", addr)
		}
	}
	if _, ok := r.saved(t).Uses[jblAddr]; ok {
		t.Error("a refused device got a use")
	}
}

func TestOneOrderAtATime(t *testing.T) {
	r := newRig(t, "")
	r.f.wait = 300 * time.Millisecond
	r.m.Scan(context.Background())
	r.wait(t, "found", func(s State) bool { return len(s.Devices) == 5 })
	if err := r.m.Act(hpAddr, ActPair); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Act(phoneAddr, ActPair); !errors.Is(err, ErrBusy) {
		t.Error("second order", err)
	}
	if err := r.m.Scan(context.Background()); !errors.Is(err, ErrBusy) {
		t.Error("search during an order", err)
	}
	s := r.m.State()
	if d, _ := dev(s, hpAddr); d.Pending != ActPair {
		t.Error("pending", d)
	}
	for _, c := range []struct{ addr, act string }{{"38-18", ActPair}, {hpAddr, "dance"}} {
		if !errors.Is(r.m.Act(c.addr, c.act), ErrInvalid) {
			t.Error(c)
		}
	}
	if !errors.Is(r.m.Act("11:22:33:44:55:66", ActPair), ErrUnknown) || !errors.Is(r.m.Act(phoneAddr, ActConnect), ErrNotPaired) {
		t.Error("unknown / not paired")
	}
	r.wait(t, "done", func(s State) bool { d, _ := dev(s, hpAddr); return d.Ready })
}

func TestVisibleWindow(t *testing.T) {
	r := newRig(t, "")
	r.f.mu.Lock()
	r.f.calls = nil
	r.f.mu.Unlock()
	if err := r.m.SetVisible(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	s := r.m.State()
	if !s.Visible || s.VisibleLeft != 120 || !r.m.allowed(phoneAddr) {
		t.Error("window", s)
	}
	r.f.mu.Lock()
	again := slices.Contains(r.f.calls, "agent")
	r.f.mu.Unlock()
	if !again {
		t.Error("the agent was not made the default again when opening the window")
	}
	r.f.mu.Lock()
	vis := r.f.adapter.Discoverable && r.f.adapter.Pairable
	r.f.mu.Unlock()
	if !vis {
		t.Error("adapter not visible")
	}
	// The phone pairs from its side inside the window: trusted, used as an input, its audio ready, and the window
	// closes at once (one device per window)
	r.f.mu.Lock()
	r.f.devs[phoneAddr].Paired, r.f.devs[phoneAddr].Connected = true, true
	r.f.mu.Unlock()
	s = r.wait(t, "phone ready", func(s State) bool { d, _ := dev(s, phoneAddr); return d.Ready })
	if d, _ := dev(s, phoneAddr); d.Use != UseInput || !r.f.trusted[phoneAddr] || s.Visible || r.m.allowed(hsAddr) {
		t.Error(d, r.f.trusted, s.Visible)
	}
	if f := r.engineWhen(t, func(f btaudio.File) bool { return len(f.In) > 0 }); f.Out != nil || len(f.In) != 1 ||
		f.In[0].Addr != phoneAddr || f.In[0].Profile != "a2dp" {
		t.Errorf("engine %+v", f)
	}
	// The window ends: hidden, and nobody else may pair
	r.clk.add(VisibleFor + time.Second)
	r.wait(t, "hidden", func(s State) bool {
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		return !s.Visible && !r.f.adapter.Discoverable && !r.f.adapter.Pairable
	})
	if r.m.allowed(hsAddr) {
		t.Error("after the window")
	}
	// Closed by hand before its end
	r.m.SetVisible(context.Background(), true)
	if err := r.m.SetVisible(context.Background(), false); err != nil || r.m.State().Visible || r.m.allowed(hsAddr) {
		t.Error("closed", err)
	}
}

func TestLastConnectedListens(t *testing.T) {
	r := newRig(t, `{"on": true, "uses": {"`+hpAddr+`": "listen", "`+hsAddr+`": "mic"}}`)
	r.f.mu.Lock()
	for _, a := range []string{hpAddr, hsAddr} {
		r.f.devs[a].Paired = true
		r.f.trusted[a] = true
	}
	r.f.devs[hpAddr].Connected = true
	r.f.mu.Unlock()
	r.wait(t, "headphones", func(State) bool { f := r.engine(t); return f.Out != nil && f.Out.Addr == hpAddr })
	r.clk.add(time.Second)
	r.f.mu.Lock()
	r.f.devs[hsAddr].Connected = true
	r.f.mu.Unlock()
	r.wait(t, "headset after", func(State) bool {
		f := r.engine(t)
		return f.Out != nil && f.Out.Addr == hsAddr && f.Out.Profile == "sco" && len(f.In) == 1 && f.In[0].Addr == hsAddr
	})
}

func TestForgetAndOff(t *testing.T) {
	r := newRig(t, `{"on": true, "uses": {"`+hpAddr+`": "listen"}}`)
	r.f.mu.Lock()
	r.f.devs[hpAddr].Paired, r.f.devs[hpAddr].Connected = true, true
	r.f.trusted[hpAddr] = true
	r.f.mu.Unlock()
	r.wait(t, "ready", func(s State) bool { d, _ := dev(s, hpAddr); return d.Ready })
	if err := r.m.SetOn(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	s := r.wait(t, "off", func(s State) bool { return !s.Powered })
	if s.On || r.saved(t).On == nil || *r.saved(t).On {
		t.Error("off not saved", s)
	}
	r.engineWhen(t, func(f btaudio.File) bool { return f.Out == nil })
	if !errors.Is(r.m.Scan(context.Background()), ErrOff) || !errors.Is(r.m.SetVisible(context.Background(), true), ErrOff) {
		t.Error("orders while off")
	}
	// Forgetting works off too
	if err := r.m.Act(hpAddr, ActForget); err != nil {
		t.Fatal(err)
	}
	r.wait(t, "forgotten", func(s State) bool { _, ok := dev(s, hpAddr); return !ok })
	if _, ok := r.saved(t).Uses[hpAddr]; ok {
		t.Error("use kept")
	}
	// Back on: powered by the app (BlueZ's AutoEnable is off)
	r.m.SetOn(context.Background(), true)
	r.wait(t, "on", func(s State) bool { return s.Powered && s.On })
}

func TestReconnectAtPowerOn(t *testing.T) {
	r := newRig(t, `{"on": false, "uses": {"`+phoneAddr+`": "input"}}`)
	r.f.mu.Lock()
	r.f.devs[phoneAddr].Paired = true
	r.f.trusted[phoneAddr] = true
	r.f.wait = 200 * time.Millisecond
	r.f.mu.Unlock()
	r.wait(t, "off at start", func(s State) bool { return !s.Powered })
	r.m.SetOn(context.Background(), true)
	// Shown as an order running while it reconnects (the other orders wait: the interfaces grey them out)
	r.wait(t, "reconnecting", func(s State) bool { d, _ := dev(s, phoneAddr); return d.Pending == ActConnect })
	r.wait(t, "the phone reconnected", func(s State) bool { d, _ := dev(s, phoneAddr); return d.Connected && d.Ready && d.Pending == "" })
}

func TestConfigBounds(t *testing.T) {
	r := newRig(t, `{"on": true, "uses": {"`+hpAddr+`": "listen", "nope": "listen", "`+phoneAddr+`": "shout"}}`)
	r.m.mu.Lock()
	uses := r.m.cfg.Uses
	r.m.mu.Unlock()
	if len(uses) != 1 || uses[hpAddr] != UseListen {
		t.Error(uses)
	}
}

// writeAtomic: like main's config.WriteAtomic (a reader never sees half a file).
func writeAtomic(p string, b []byte, mode os.FileMode) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func dbusErr(name, msg string) error { return dbus.Error{Name: name, Body: []any{msg}} }
