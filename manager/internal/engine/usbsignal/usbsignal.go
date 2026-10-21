// Package usbsignal tells whether a USB HDMI capture card has a signal on its HDMI input (SOURCES.md §9).
// The frames alone do not tell: with nothing plugged in, the MacroSilicon MS2109 keeps streaming black
// frames at its full rate, so the engine took it for a live source. Two ways: what the card says, for the
// chips whose way we know (Card, here), and otherwise its picture, for any card (Picture, picture.go).
//
// MS2109 (534d:2109, also sold as 345f:2109): the ROM's factory HID interface reads the 8051's memory
// with a 9-byte feature report (0xB5 + a 16-bit XDATA address → one byte; what the open-source
// ms-tools calls the RAM region). Only reads are ever sent. IRAM 0x20 bit 4 is set while the HDMI input has no
// signal: measured on the test board (chip 0xA7 at XDATA 0xF800, Oct 21) plugging and unplugging a
// source, and by an MS2109 LED project on chip 0xD7. The flag is only reliable while the card streams
// (idle, it may be stale), so it is read while the source's pipeline runs.
//
// Without root: the capture cards' udev rule (manager/deploy/70-eirl-capture.rules) gives the card's
// hidraw node to the group eirl-usbhub, which only the engine's service has.
package usbsignal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The MS2109 ROM's protocol and what is read with it.
const (
	cmdReadXDATA = 0xB5
	addrChip     = 0xF800 // set by the ROM: which chip it is
	addrFlags    = 0x0020 // IRAM 0x20 (IRAM is XDATA 0x00-0xFF here)
	bitNoSignal  = 0x10
)

// chips are the MS2109 revisions (XDATA 0xF800) whose flag has been seen to follow the HDMI input.
// Another chip is not read: a wrong "no signal" would put the slate over a live picture.
var chips = map[byte]bool{0xA7: true, 0xD7: true}

// ErrUnsupported: the device is not a capture card whose signal can be read (a webcam, the Cam Link, an
// unknown chip). The source keeps going by its frames alone.
var ErrUnsupported = errors.New("no readable signal flag")

// HID is one hidraw node: the exchange of a feature report (report id 0 + 8 bytes).
type HID interface {
	Exchange(out [9]byte) ([9]byte, error)
	Close() error
}

// Sys is where the board exposes its USB devices; tests point Root at a temporary tree and replace
// OpenHID.
type Sys struct {
	Root    string                         // "" = /: prefix of /sys and /dev
	OpenHID func(node string) (HID, error) // nil = the hidraw node itself
}

// Card is one capture card's signal flag, from Open. Not safe for concurrent use: one source reads it.
type Card struct {
	hid  HID
	Node string // its hidraw node, for the log
	Chip byte
}

// Open finds the hidraw node of the USB device dev ("1-2.1") and checks its chip. ErrUnsupported when
// the device is not one this package knows; another error when it is but cannot be read (permissions:
// the udev rule is missing).
func (s Sys) Open(dev string) (*Card, error) {
	if dev == "" || strings.ContainsAny(dev, "/:*?[") {
		return nil, ErrUnsupported
	}
	d := filepath.Join(s.Root, "/sys/bus/usb/devices", dev)
	if !known(read(filepath.Join(d, "idVendor")), read(filepath.Join(d, "idProduct"))) {
		return nil, ErrUnsupported
	}
	// 1-2.1:1.4/0003:534D:2109.0004/hidraw/hidraw1: the vendor HID interface (the chip has no other)
	nodes, _ := filepath.Glob(filepath.Join(s.Root, "/sys/bus/usb/devices", dev+":*", "*", "hidraw", "hidraw*"))
	if len(nodes) != 1 {
		return nil, ErrUnsupported
	}
	node := "/dev/" + filepath.Base(nodes[0])
	h, err := s.open(node)
	if err != nil {
		return nil, err
	}
	c := &Card{hid: h, Node: node}
	if c.Chip, err = c.read(addrChip); err != nil {
		h.Close()
		return nil, err
	}
	if !chips[c.Chip] {
		h.Close()
		return nil, fmt.Errorf("%w: chip %#02x", ErrUnsupported, c.Chip)
	}
	return c, nil
}

func (s Sys) open(node string) (HID, error) {
	if s.OpenHID != nil {
		return s.OpenHID(node)
	}
	return openHidraw(filepath.Join(s.Root, node))
}

// known: a MacroSilicon MS2109 (534d) or the same chip under another vendor id (345f).
func known(vendor, product string) bool {
	return (vendor == "534d" || vendor == "345f") && product == "2109"
}

// Signal reads the flag: true with a signal on the HDMI input.
func (c *Card) Signal() (bool, error) {
	v, err := c.read(addrFlags)
	if err != nil {
		return false, err
	}
	return v&bitNoSignal == 0, nil
}

// Close releases the hidraw node.
func (c *Card) Close() error { return c.hid.Close() }

// read is one byte of the chip's XDATA; the answer echoes the command and the address.
func (c *Card) read(addr uint16) (byte, error) {
	out := [9]byte{0, cmdReadXDATA, byte(addr >> 8), byte(addr)}
	in, err := c.hid.Exchange(out)
	if err != nil {
		return 0, err
	}
	if in[1] != out[1] || in[2] != out[2] || in[3] != out[3] {
		return 0, fmt.Errorf("%s: answer % x to a read of %#04x", c.Node, in, addr)
	}
	return in[4], nil
}

// Stable is the signal as the engine shows it: the first read says it, then a change counts after two
// equal reads in a row (one second apart: while it locks, a read can catch it halfway).
type Stable struct {
	known, present bool
	pending        int
}

// Add takes one read and returns the signal and whether it just changed (the first read counts as a
// change).
func (s *Stable) Add(present bool) (bool, bool) {
	switch {
	case !s.known:
		s.known, s.present = true, present
		return present, true
	case present == s.present:
		s.pending = 0
		return present, false
	}
	if s.pending++; s.pending < 2 {
		return s.present, false
	}
	s.present, s.pending = present, 0
	return present, true
}

func read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
