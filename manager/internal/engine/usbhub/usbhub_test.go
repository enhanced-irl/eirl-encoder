package usbhub

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSysfs is like the board (12-oct): USB 2 hub "1-2" (0bda:5411) and USB 3 "2-1" (0bda:0411); on
// 1-2.1 a capture card (video + audio), on 1-2.3 the touch screen (HID only) and on 2-1.4 the Logi.
// It returns the root (Sys.Root).
func fakeSysfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	devs := filepath.Join(root, "sys/bus/usb/devices")
	put := func(path, v string) {
		if err := os.WriteFile(path, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dev := func(name, cls string, ifaces []string, attrs ...string) {
		d := filepath.Join(devs, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		put(filepath.Join(d, "bDeviceClass"), cls)
		for i := 0; i+1 < len(attrs); i += 2 {
			put(filepath.Join(d, attrs[i]), attrs[i+1])
		}
		for n, c := range ifaces {
			id := fmt.Sprintf("%s:1.%d", name, n)
			if err := os.MkdirAll(filepath.Join(d, id), 0o755); err != nil {
				t.Fatal(err)
			}
			put(filepath.Join(d, id, "bInterfaceClass"), c)
		}
	}
	dev("1-2", "09", nil, "idVendor", "0bda", "maxchild", "4", "speed", "480", "busnum", "1", "devnum", "2")
	dev("2-1", "09", nil, "idVendor", "0bda", "maxchild", "4", "speed", "5000", "busnum", "2", "devnum", "2")
	dev("usb1", "09", nil, "idVendor", "1d6b", "maxchild", "5", "speed", "480", "busnum", "1", "devnum", "1")
	dev("1-2.1", "00", []string{"0e", "0e", "01", "01", "03"})
	dev("1-2.3", "00", []string{"03"})
	dev("2-1.4", "00", []string{"0e", "0e", "0e", "01", "01", "03"})
	return root
}

// fakeHub answers the hubs' control transfers: descriptors (per-port power unless gang), port status
// and power, which it records.
type fakeHub struct {
	mu       sync.Mutex
	gang     map[string]bool // node → ganged power
	status   uint16          // GET_STATUS answer
	fail     map[string]bool // "node set|clear|desc|status" → error
	calls    []string        // "node port on|off"
	descReqs []string        // "node 0x29|0x2a"
}

func (f *fakeHub) control(node string, reqType, req uint8, value, index uint16, length int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case reqType == 0xA0 && req == 6:
		f.descReqs = append(f.descReqs, fmt.Sprintf("%s 0x%02x", node, value>>8))
		if f.fail[node+" desc"] {
			return nil, errors.New("EACCES")
		}
		ch := byte(0xa9)
		if f.gang[node] {
			ch = 0xa8
		}
		return []byte{9, byte(value >> 8), 4, ch, 0, 50, 100, 0, 0xff, 0, 0, 0}[:length], nil
	case reqType == 0xA3 && req == getStatus:
		if f.fail[node+" status"] {
			return nil, errors.New("EPIPE")
		}
		return []byte{byte(f.status), byte(f.status >> 8), 0, 0}, nil
	case reqType == 0x23 && value == portPower:
		on := req == setFeature
		f.calls = append(f.calls, fmt.Sprintf("%s %d %v", node, index, on))
		if (on && f.fail[node+" set"]) || (!on && f.fail[node+" clear"]) {
			return nil, errors.New("EPIPE")
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected request %#x %d", reqType, req)
}

const (
	node12 = "/dev/bus/usb/001/002" // hub 1-2
	node21 = "/dev/bus/usb/002/002" // hub 2-1
)

func newFake(t *testing.T) (Sys, *fakeHub) {
	f := &fakeHub{gang: map[string]bool{}, fail: map[string]bool{}}
	return Sys{Root: fakeSysfs(t), Control: f.control, Sleep: func(time.Duration) {}}, f
}

func yes(string) bool { return true }

// UsbTest.test_ioctl_layout
func TestIoctlLayout(t *testing.T) {
	// struct usbdevfs_ctrltransfer on arm64: 8 bytes of header, timeout, padding and the pointer
	if got := reflect.TypeOf(ctrlTransfer{}).Size(); got != 24 {
		t.Fatalf("size %d", got)
	}
	if usbdevfsControl != 0xC0185500 {
		t.Fatalf("USBDEVFS_CONTROL %#x", usbdevfsControl)
	}
	if off := reflect.TypeOf(ctrlTransfer{}).Field(6).Offset; off != 16 {
		t.Fatalf("data at %d", off)
	}
}

// UsbTest.test_split_and_twins
func TestSplitAndTwins(t *testing.T) {
	s, _ := newFake(t)
	for _, c := range []struct {
		dev, hub string
		port     int
		ok       bool
	}{{"1-2.1", "1-2", 1, true}, {"2-1.4.2", "2-1.4", 2, true}, {"1-2", "", 0, false}, {"../x", "", 0, false}, {"", "", 0, false}} {
		hub, port, ok := SplitPort(c.dev)
		if hub != c.hub || port != c.port || ok != c.ok {
			t.Errorf("SplitPort(%q) = %q %d %v", c.dev, hub, port, ok)
		}
	}
	if got := s.TwinHubs("1-2"); !reflect.DeepEqual(got, []string{"2-1"}) {
		t.Errorf("twins of 1-2: %v", got)
	}
	if got := s.TwinHubs("2-1"); !reflect.DeepEqual(got, []string{"1-2"}) {
		t.Errorf("twins of 2-1: %v", got)
	}
	if got := s.HubNode("2-1"); got != node21 {
		t.Errorf("node %q", got)
	}
}

// UsbTest.test_hub_descriptor
func TestHubDescriptor(t *testing.T) {
	for _, c := range []struct {
		desc []byte
		want bool
	}{
		{[]byte{9, 0x29, 4, 0xa9, 0, 50, 100, 0, 0xff}, true}, // RTS5411 USB 2
		{[]byte{12, 0x2a, 4, 0x09, 0, 50, 100, 0, 0, 0, 0, 0}, true},
		{[]byte{9, 0x29, 4, 0xa8, 0, 50, 100, 0, 0xff}, false}, // ganged
		{nil, false},
	} {
		if got := HubPowerMode(c.desc); got != c.want {
			t.Errorf("% x: %v", c.desc, got)
		}
	}
}

// UsbTest.test_plan
func TestPlan(t *testing.T) {
	s, _ := newFake(t)
	p := NewPlan(s, "1-2.1", false, yes)
	if !p.OK() || !reflect.DeepEqual(p.Hubs, []string{"1-2", "2-1"}) || p.Port != 1 {
		t.Fatalf("1-2.1: %+v", p)
	}
	// A cut powers off the whole hub and its twin (measured 13-oct): the touch screen and the Logi too
	if !reflect.DeepEqual(p.Affected, []string{"1-2.3", "2-1.4"}) {
		t.Errorf("affected %v", p.Affected)
	}
	p = NewPlan(s, "2-1.4", false, yes)
	if !reflect.DeepEqual(p.Hubs, []string{"2-1", "1-2"}) || p.Port != 4 || !reflect.DeepEqual(p.Affected, []string{"1-2.1", "1-2.3"}) {
		t.Errorf("2-1.4: %+v", p)
	}
	// The twin that does not switch is not touched, but what hangs from it powers off too
	p = NewPlan(s, "1-2.1", false, func(h string) bool { return h == "1-2" })
	if !reflect.DeepEqual(p.Hubs, []string{"1-2"}) || !reflect.DeepEqual(p.Affected, []string{"1-2.3", "2-1.4"}) {
		t.Errorf("no twin: %+v", p)
	}
	// Never for the touch screen (not a camera)
	if NewPlan(s, "1-2.3", false, yes).OK() {
		t.Error("touch screen")
	}
	// Dropped off the bus: fine if it was known to be a camera
	if NewPlan(s, "1-2.2", false, yes).OK() || !NewPlan(s, "1-2.2", true, yes).OK() {
		t.Error("dropped off the bus")
	}
	if NewPlan(s, "1-2.1", false, func(string) bool { return false }).OK() {
		t.Error("hub without per-port power")
	}
	if p := NewPlan(s, "", false, yes); p.OK() || p.Text() != ": not a USB device" {
		t.Errorf("no device: %+v", p)
	}
	if got := NewPlan(s, "2-1.4", false, yes).Text(); got != "2-1.4: port 4 of 2-1+1-2" {
		t.Errorf("text %q", got)
	}
}

// UsbTest.test_blockers
func TestBlockers(t *testing.T) {
	s, _ := newFake(t)
	p := NewPlan(s, "1-2.1", false, yes)
	for _, c := range []struct {
		air  map[string]OnAir
		want []OnAir
	}{
		{map[string]OnAir{}, []OnAir{}},
		{map[string]OnAir{"2-1.4": {ID: "logi", Name: "Logi"}}, []OnAir{{ID: "logi", Name: "Logi"}}},
		{map[string]OnAir{"2-1.4": {ID: "logi", Name: "Logi"}, "3-1": {ID: "o", Name: "otra"}, "1-2.1": {ID: "me", Name: "ella misma"}},
			[]OnAir{{ID: "logi", Name: "Logi"}}},
		// Two cameras with the same name: by id, always in the same order (recovery compares the lists)
		{map[string]OnAir{"2-1.4": {ID: "usb-b", Name: "USB Video"}, "1-2.3": {ID: "usb-a", Name: "USB Video"}},
			[]OnAir{{ID: "usb-a", Name: "USB Video"}, {ID: "usb-b", Name: "USB Video"}}},
	} {
		if got := Blockers(p, c.air); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: %v", c.air, got)
		}
	}
	if got := Blockers(NewPlan(s, "1-2.3", false, yes), map[string]OnAir{"2-1.4": {ID: "logi", Name: "Logi"}}); len(got) != 0 {
		t.Errorf("touch screen plan: %v", got)
	}
	// The same name is said once
	held := []OnAir{{ID: "a", Name: "Logi", Audio: true}, {ID: "usb-a", Name: "USB Video"}, {ID: "usb-b", Name: "USB Video"}}
	if got := shownOnAir(held); !reflect.DeepEqual(got, []string{"el audio de Logi", "USB Video"}) {
		t.Errorf("shown %v", got)
	}
}

// UsbTest.test_claim
func TestClaim(t *testing.T) {
	s, _ := newFake(t)
	h := NewHubs(s)
	t0 := time.Now()
	at := func(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }
	a := NewPlan(s, "1-2.1", false, yes)
	b := NewPlan(s, "2-1.4", false, yes)
	if _, busy := h.Claim(a, at(100), 20*time.Second); busy {
		t.Fatal("first claim")
	}
	if last, busy := h.Claim(b, at(105), 20*time.Second); !busy || !last.Equal(at(100)) { // same hub, 5 s ago: no
		t.Fatalf("second claim: %v %v", last, busy)
	}
	if _, busy := h.Claim(b, at(121), 20*time.Second); busy {
		t.Fatal("after 21 s")
	}
}

// UsbTest.test_card_device
func TestCardDevice(t *testing.T) {
	s, _ := newFake(t)
	iface := filepath.Join(s.Root, "sys/bus/usb/devices/1-2.1/1-2.1:1.2")
	card := filepath.Join(s.Root, "sys/class/sound/card2")
	asound := filepath.Join(s.Root, "proc/asound")
	for _, d := range []string{iface, card, filepath.Join(asound, "card2")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(iface, filepath.Join(card, "device")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("card2", filepath.Join(asound, "MS2109")); err != nil {
		t.Fatal(err)
	}
	if got := s.DeviceOfCard("MS2109"); got != "1-2.1" {
		t.Errorf("MS2109: %q", got)
	}
	if got := s.DeviceOfCard("../MS2109"); got != "" {
		t.Errorf("../MS2109: %q", got)
	}
	if got := s.DeviceOfCard("nada"); got != "" {
		t.Errorf("nada: %q", got)
	}
}

// New: a video node (or its by-id link) → its USB device.
func TestDeviceOf(t *testing.T) {
	s, _ := newFake(t)
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(s.Root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("dev/v4l/by-id")
	mk("sys/class/video4linux/video3")
	mk("sys/class/video4linux/video5")
	if err := os.WriteFile(filepath.Join(s.Root, "dev/video3"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, "dev/video5"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Like the board: by-id links are relative; the class device links to the interface
	if err := os.Symlink("../../video3", filepath.Join(s.Root, "dev/v4l/by-id/usb-Logi-video-index0")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.Root, "sys/bus/usb/devices/2-1.4/2-1.4:1.0"), filepath.Join(s.Root, "sys/class/video4linux/video3/device")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.Root, "sys/devices/platform/csi"), filepath.Join(s.Root, "sys/class/video4linux/video5/device")); err != nil {
		t.Fatal(err)
	}
	mk("sys/devices/platform/csi")
	for node, want := range map[string]string{
		"/dev/video3": "2-1.4", "/dev/v4l/by-id/usb-Logi-video-index0": "2-1.4",
		"/dev/video5": "", // the CSI: not USB
		"/dev/video9": "", // missing
	} {
		if got := s.DeviceOf(node); got != want {
			t.Errorf("%s: %q, want %q", node, got, want)
		}
	}
}

// UsbTest.test_cycle_always_powers_back
func TestCycleAlwaysPowersBack(t *testing.T) {
	s, f := newFake(t)
	f.fail[node21+" clear"] = true
	slept := time.Duration(0)
	s.Sleep = func(d time.Duration) { slept += d }
	done, errs := s.Cycle(NewPlan(s, "1-2.1", false, yes), OffFor)
	want := []string{node12 + " 1 false", node21 + " 1 false", node21 + " 1 true", node12 + " 1 true"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls %v", f.calls)
	}
	if !reflect.DeepEqual(done, []string{"1-2"}) || len(errs) != 1 || !strings.HasPrefix(errs[0], "2-1: ") {
		t.Errorf("done %v errs %v", done, errs)
	}
	if slept != 2*time.Second {
		t.Errorf("slept %v", slept)
	}
}

// New: the hub is asked once (USB 3 → 0x2a); a failure is not remembered; not a hub → false.
func TestPerPortPower(t *testing.T) {
	s, f := newFake(t)
	h := NewHubs(s)
	f.fail[node12+" desc"] = true
	if h.PerPortPower("1-2") {
		t.Error("1-2 without access")
	}
	f.fail[node12+" desc"] = false
	if !h.PerPortPower("1-2") || !h.PerPortPower("1-2") || !h.PerPortPower("2-1") {
		t.Error("per port")
	}
	if h.PerPortPower("1-2.1") || h.PerPortPower("9-9") {
		t.Error("not a hub")
	}
	if want := []string{node12 + " 0x29", node12 + " 0x29", node21 + " 0x2a"}; !reflect.DeepEqual(f.descReqs, want) {
		t.Errorf("requests %v", f.descReqs)
	}
	f.gang[node12] = true
	h2 := NewHubs(s)
	if p := h2.Plan("1-2.1", false); p.OK() {
		t.Errorf("ganged hub: %+v", p)
	}
}

// New: plugged = in sysfs, else the hub's connection bit, else unknown.
func TestPlugged(t *testing.T) {
	s, f := newFake(t)
	if p, k := s.Plugged("2-1.4"); !p || !k {
		t.Error("enumerated")
	}
	if p, k := s.Plugged("2-1.2"); p || !k {
		t.Error("port empty")
	}
	f.status = 0x0101
	if p, k := s.Plugged("2-1.2"); !p || !k {
		t.Error("connected, not enumerated")
	}
	f.fail[node21+" status"] = true
	if _, k := s.Plugged("2-1.2"); k {
		t.Error("status unreadable")
	}
	if _, k := s.Plugged(""); k {
		t.Error("no device")
	}
	if _, k := s.Plugged("3-1"); k {
		t.Error("root port, not enumerated: no hub to ask")
	}
}
