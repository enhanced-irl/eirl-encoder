package usbsignal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// chip is a fake MS2109 behind its HID interface: its XDATA, and what it was asked.
type chip struct {
	mem    map[uint16]byte
	sent   [][9]byte
	closed bool
	err    error
	echo   bool // answer with another address
}

func (c *chip) Exchange(out [9]byte) ([9]byte, error) {
	c.sent = append(c.sent, out)
	if c.err != nil {
		return [9]byte{}, c.err
	}
	in := out
	if c.echo {
		in[3]++
	}
	in[4] = c.mem[uint16(out[2])<<8|uint16(out[3])]
	return in, nil
}

func (c *chip) Close() error { c.closed = true; return nil }

// board is a sysfs tree with one USB device on 1-2.1 and its hidraw node.
func board(t *testing.T, vendor, product string, hidraw bool) string {
	t.Helper()
	root := t.TempDir()
	dev := filepath.Join(root, "sys/bus/usb/devices/1-2.1")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"idVendor": vendor, "idProduct": product} {
		if err := os.WriteFile(filepath.Join(dev, k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if hidraw {
		n := filepath.Join(root, "sys/bus/usb/devices/1-2.1:1.4/0003:534D:2109.0004/hidraw/hidraw1")
		if err := os.MkdirAll(n, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestOpenAndSignal(t *testing.T) {
	c := &chip{mem: map[uint16]byte{addrChip: 0xA7, addrFlags: 0x30}}
	var opened string
	sys := Sys{Root: board(t, "534d", "2109", true), OpenHID: func(node string) (HID, error) { opened = node; return c, nil }}
	card, err := sys.Open("1-2.1")
	if err != nil {
		t.Fatal(err)
	}
	if opened != "/dev/hidraw1" || card.Node != "/dev/hidraw1" || card.Chip != 0xA7 {
		t.Errorf("opened %q, card %+v", opened, card)
	}
	// What the board read on Oct 21: 0x30 and 0x18 without a signal, 0x26 → 0x02 → 0x00 while it locks
	for _, tc := range []struct {
		flags   byte
		present bool
	}{{0x30, false}, {0x18, false}, {0x26, true}, {0x02, true}, {0x00, true}, {0x94, false}, {0x84, true}} {
		c.mem[addrFlags] = tc.flags
		got, err := card.Signal()
		if err != nil || got != tc.present {
			t.Errorf("flags %#02x: signal %v (%v), want %v", tc.flags, got, err, tc.present)
		}
	}
	// Only reads, of the chip id and the flags
	for _, out := range c.sent {
		addr := uint16(out[2])<<8 | uint16(out[3])
		if out[0] != 0 || out[1] != cmdReadXDATA || (addr != addrChip && addr != addrFlags) || out[4] != 0 {
			t.Errorf("sent % x", out)
		}
	}
	card.Close()
	if !c.closed {
		t.Error("not closed")
	}
}

func TestOpenUnsupported(t *testing.T) {
	never := func(string) (HID, error) { t.Error("opened"); return nil, os.ErrNotExist }
	for _, tc := range []struct {
		what, vendor, product string
		hidraw                bool
		dev                   string
	}{
		{"a webcam", "046d", "086b", true, "1-2.1"},
		{"the Cam Link", "0fd9", "0066", false, "1-2.1"},
		{"no hidraw node", "534d", "2109", false, "1-2.1"},
		{"not there", "534d", "2109", true, "1-2.4"},
		{"no device", "534d", "2109", true, ""},
		{"a path", "534d", "2109", true, "../1-2.1"},
	} {
		sys := Sys{Root: board(t, tc.vendor, tc.product, tc.hidraw), OpenHID: never}
		if _, err := sys.Open(tc.dev); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", tc.what, err)
		}
	}
	// Another chip behind the same ids is not read: its flag might mean something else
	c := &chip{mem: map[uint16]byte{addrChip: 0x00}}
	sys := Sys{Root: board(t, "345f", "2109", true), OpenHID: func(string) (HID, error) { return c, nil }}
	if _, err := sys.Open("1-2.1"); !errors.Is(err, ErrUnsupported) || !c.closed {
		t.Errorf("unknown chip: %v, closed %v", err, c.closed)
	}
}

func TestOpenErrors(t *testing.T) {
	root := board(t, "534d", "2109", true)
	// Without the udev rule: the error says so, it is not "unsupported"
	sys := Sys{Root: root, OpenHID: func(string) (HID, error) { return nil, os.ErrPermission }}
	if _, err := sys.Open("1-2.1"); !errors.Is(err, os.ErrPermission) {
		t.Errorf("permission: %v", err)
	}
	// An answer that does not echo the read
	c := &chip{mem: map[uint16]byte{addrChip: 0xA7}, echo: true}
	sys = Sys{Root: root, OpenHID: func(string) (HID, error) { return c, nil }}
	if _, err := sys.Open("1-2.1"); err == nil || errors.Is(err, ErrUnsupported) || !c.closed {
		t.Errorf("bad echo: %v, closed %v", err, c.closed)
	}
	// A failed read after opening
	c = &chip{mem: map[uint16]byte{addrChip: 0xA7}}
	sys = Sys{Root: root, OpenHID: func(string) (HID, error) { return c, nil }}
	card, err := sys.Open("1-2.1")
	if err != nil {
		t.Fatal(err)
	}
	c.err = os.ErrClosed
	if _, err := card.Signal(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("read: %v", err)
	}
}

func TestStable(t *testing.T) {
	var s Stable
	step := func(read, want, changed bool) {
		t.Helper()
		got, ch := s.Add(read)
		if got != want || ch != changed {
			t.Fatalf("read %v: %v %v, want %v %v", read, got, ch, want, changed)
		}
	}
	step(false, false, true) // the first read says it at once
	step(false, false, false)
	step(true, false, false) // one read is not enough
	step(false, false, false)
	step(true, false, false)
	step(true, true, true) // two in a row
	step(true, true, false)
	step(false, true, false)
	step(false, false, true)
}
