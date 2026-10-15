package bluetooth

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakeBT is BlueZ and BlueALSA in memory: for the tests and for eirl-encoder-sim (NewSim), so the web and the screen
// can be worked on without a board. Devices answer orders like real ones: a search shows them, pairing takes a moment,
// one refuses, connecting gives them their PCMs.
type fakeBT struct {
	mu      sync.Mutex
	stack   bool // bluetoothd running
	audio   bool // bluealsa running
	adapter *adapter
	devs    map[string]*fakeDev
	owner   int
	wait    time.Duration // how long pairing and connecting take
	ticks   chan struct{}
	agentOK bool
	trusted map[string]bool
	calls   []string
	failSet map[string]error // setAdapter of that property fails (rfkill, a refused change)
}

type fakeDev struct {
	rawDevice
	nearby  bool   // in range: a search sees it
	found   bool   // a search found it (BlueZ keeps it listed, without RSSI, after the search)
	refuse  bool   // it refuses to pair
	absent  bool   // it does not answer (off, far)
	delayMs int    // what BlueALSA reports for its A2DP output
	hfp     bool   // it has a microphone (hands-free)
	kind    string // for the fake's icon
	bonded  bool   // its pairing left a key (persists over a restart)
}

func newFake() *fakeBT {
	return &fakeBT{stack: true, audio: true, devs: map[string]*fakeDev{}, trusted: map[string]bool{},
		adapter: &adapter{Path: "/org/bluez/hci0", Address: "00:1A:7D:DA:71:13", Alias: "eirl-encoder", Powered: true},
		ticks:   make(chan struct{}, 1)}
}

func (f *fakeBT) tick() {
	select {
	case f.ticks <- struct{}{}:
	default:
	}
}

func (f *fakeBT) add(addr, name, icon string, class uint32, uuids []string, nearby bool) *fakeDev {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := &fakeDev{rawDevice: rawDevice{Path: f.adapter.Path + "/dev_" + strings.ReplaceAll(addr, ":", "_"), Address: addr,
		Name: name, HasName: name != "", Icon: icon, Class: class, UUIDs: uuids}, nearby: nearby, delayMs: 150}
	f.devs[addr] = d
	return d
}

func (f *fakeBT) record(c string) {
	f.calls = append(f.calls, c)
}

func (f *fakeBT) watch(ctx context.Context, ag *agent) (<-chan struct{}, error) {
	f.mu.Lock()
	ok := f.stack
	f.mu.Unlock()
	if !ok {
		return nil, errNoStack
	}
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case <-f.ticks:
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out, nil
}

func (f *fakeBT) snapshot(context.Context) (snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.stack {
		return snapshot{}, errNoStack
	}
	s := snapshot{Owner: ":1." + string(rune('0'+f.owner))}
	if f.adapter == nil {
		return s, nil
	}
	a := *f.adapter
	s.Adapter = &a
	addrs := make([]string, 0, len(f.devs))
	for a := range f.devs {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	for _, addr := range addrs {
		d := f.devs[addr]
		seen := d.nearby && f.adapter.Discovering
		if !d.Paired && !d.found {
			continue // BlueZ only lists what a search found (and keeps it a while)
		}
		r := d.rawDevice
		r.Trusted = f.trusted[addr]
		r.UUIDs = append([]string(nil), d.UUIDs...)
		if seen {
			v := -45 - int(addr[len(addr)-1]-'0')*6 // stable: the list does not jump around
			r.RSSI = &v
		} else {
			r.RSSI = nil
		}
		s.Devices = append(s.Devices, r)
	}
	return s, nil
}

func (f *fakeBT) pcms(context.Context) ([]pcm, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.audio {
		return nil, errNoAudio
	}
	var out []pcm
	for _, d := range f.devs {
		if !d.Connected || f.adapter == nil || !f.adapter.Powered {
			continue
		}
		u := uses(d.UUIDs)
		for _, x := range u {
			switch x {
			case UseListen:
				out = append(out, pcm{Address: d.Address, Profile: "a2dp", Out: true, DelayMs: d.delayMs})
			case UseInput:
				out = append(out, pcm{Address: d.Address, Profile: "a2dp"})
			case UseMic:
				out = append(out, pcm{Address: d.Address, Profile: "sco", Out: true}, pcm{Address: d.Address, Profile: "sco"})
			}
		}
	}
	return out, nil
}

func (f *fakeBT) registerAgent(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agentOK = true
	f.record("agent")
	return nil
}

func (f *fakeBT) setAdapter(_ context.Context, _ string, prop string, v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("set " + prop)
	if err := f.failSet[prop]; err != nil {
		return err
	}
	a := f.adapter
	switch prop {
	case "Powered":
		a.Powered = v.(bool)
		if !a.Powered {
			a.Discovering, a.Discoverable, a.Pairable = false, false, false
			for _, d := range f.devs {
				d.Connected = false
			}
		}
	case "Discoverable":
		a.Discoverable = v.(bool)
	case "Pairable":
		a.Pairable = v.(bool)
	case "Alias":
		a.Alias = v.(string)
	}
	f.tick()
	return nil
}

func (f *fakeBT) discovery(_ context.Context, _ string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("discovery " + map[bool]string{true: "on", false: "off"}[on])
	f.adapter.Discovering = on
	for _, d := range f.devs {
		d.found = d.found || on && d.nearby
	}
	f.tick()
	return nil
}

func (f *fakeBT) dev(path string) *fakeDev {
	for _, d := range f.devs {
		if d.Path == path {
			return d
		}
	}
	return nil
}

func (f *fakeBT) slow(ctx context.Context) error {
	if f.wait <= 0 {
		return nil
	}
	t := time.NewTimer(f.wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (f *fakeBT) pair(ctx context.Context, path string) error {
	if err := f.slow(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("pair " + path)
	d := f.dev(path)
	switch {
	case d == nil:
		return dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownObject", Body: []any{"no such device"}}
	case d.absent:
		return dbus.Error{Name: "org.bluez.Error.ConnectionAttemptFailed", Body: []any{"Page Timeout"}}
	case d.refuse:
		return dbus.Error{Name: "org.bluez.Error.AuthenticationRejected", Body: []any{"Authentication Rejected"}}
	case d.Paired:
		return dbus.Error{Name: "org.bluez.Error.AlreadyExists", Body: []any{"Already Exists"}}
	}
	d.Paired = true
	d.bonded = f.adapter.Pairable // the kernel makes it non-bonding when the adapter is not bondable
	f.tick()
	return nil
}

// restart is the adapter coming back (btusb reloaded, a reboot): only bonded pairings survive.
func (f *fakeBT) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devs {
		d.Connected = false
		if !d.bonded {
			d.Paired = false
		}
	}
	f.tick()
}

func (f *fakeBT) connect(ctx context.Context, path string) error {
	if err := f.slow(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("connect " + path)
	d := f.dev(path)
	switch {
	case d == nil:
		return dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownObject", Body: []any{"no such device"}}
	case d.absent:
		return dbus.Error{Name: "org.bluez.Error.Failed", Body: []any{"Host is down"}}
	}
	d.Connected = true
	f.tick()
	return nil
}

func (f *fakeBT) disconnect(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("disconnect " + path)
	if d := f.dev(path); d != nil {
		d.Connected = false
	}
	f.tick()
	return nil
}

func (f *fakeBT) setTrusted(_ context.Context, path string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("trust " + path)
	if d := f.dev(path); d != nil {
		f.trusted[d.Address] = on
	}
	return nil
}

func (f *fakeBT) remove(_ context.Context, _, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("remove " + path)
	if d := f.dev(path); d != nil {
		d.Paired, d.Connected = false, false
		delete(f.trusted, d.Address)
	}
	f.tick()
	return nil
}

// Sim profiles, in BlueZ's 128-bit form
var (
	simHeadphones = []string{uuidA2DPSink, uuidHandsfree, "0000110e-0000-1000-8000-00805f9b34fb"}
	simSpeaker    = []string{uuidA2DPSink}
	simHeadset    = []string{uuidHandsfree, uuidHeadset}
	simPhone      = []string{uuidA2DPSource, "0000111f-0000-1000-8000-00805f9b34fb", "00001112-0000-1000-8000-00805f9b34fb"}
)

// NewSim is a manager over the fake BlueZ for eirl-encoder-sim: an adapter and a few devices around (headphones with a
// microphone, a phone, a headset, a speaker that refuses to pair and one that is off), already paired: none.
func NewSim(o Options) *Manager {
	f := newFake()
	f.wait = 1500 * time.Millisecond
	f.add("38:18:4C:12:A0:01", "WH-1000XM4", "audio-headphones", 0x240404, simHeadphones, true)
	f.add("A4:C3:F0:55:21:02", "Pixel 8", "phone", 0x5a020c, simPhone, true)
	f.add("50:C2:ED:7B:10:03", "Jabra Evolve 65", "audio-headset", 0x240404, simHeadset, true)
	f.add("FC:58:FA:30:44:04", "JBL Flip 5", "audio-card", 0x240414, simSpeaker, true).refuse = true
	f.add("00:0D:44:AB:CD:05", "Old speaker", "audio-card", 0x240414, simSpeaker, true).absent = true
	return newManager(o, f)
}
