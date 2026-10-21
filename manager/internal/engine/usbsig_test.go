package engine

import (
	"bytes"
	"image"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/detail"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/usbsignal"
)

// ms2109 is a fake MS2109 behind its HID interface: the chip id and the flags byte the test sets.
type ms2109 struct {
	mu     sync.Mutex
	flags  byte
	err    error
	closed bool
}

func (c *ms2109) set(flags byte, err error) { c.mu.Lock(); c.flags, c.err = flags, err; c.mu.Unlock() }

func (c *ms2109) isClosed() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.closed }

func (c *ms2109) Exchange(out [9]byte) ([9]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return [9]byte{}, c.err
	}
	in := out
	in[4] = c.flags
	if out[2] == 0xF8 {
		in[4] = 0xA7
	}
	return in, nil
}

func (c *ms2109) Close() error { c.mu.Lock(); c.closed = true; c.mu.Unlock(); return nil }

// devNode is a path that exists, as the source's id: stillValid checks that its device is still there.
func devNode(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "usb-video-index0")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ms2109Board is a sysfs tree with the card on 1-2.1 and its hidraw node.
func ms2109Board(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dev := filepath.Join(root, "sys/bus/usb/devices/1-2.1")
	if err := os.MkdirAll(filepath.Join(root, "sys/bus/usb/devices/1-2.1:1.4/0003:534D:2109.0004/hidraw/hidraw1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"idVendor": "534d", "idProduct": "2109"} {
		if err := os.WriteFile(filepath.Join(dev, k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A USB capture card that streams black without a signal (the MS2109): its frames are held out of the
// program and the source says «no HDMI signal» (the slate goes in, slateTick); with the signal back, after
// two reads, its picture again. The flag failing in a row gives it up: never a slate it cannot vouch for.
func TestUSBCardWithoutSignal(t *testing.T) {
	old := signalEvery
	signalEvery = 50 * time.Millisecond
	t.Cleanup(func() { signalEvery = old })
	e, m, _ := startEngine(t, nil, usbCatalogue())
	logs := &lockedLog{}
	elog.SetOutput(logs)
	card := &ms2109{flags: 0x30}
	e.usbSig = usbsignal.Sys{Root: ms2109Board(t), OpenHID: func(node string) (usbsignal.HID, error) {
		if node != "/dev/hidraw1" {
			t.Errorf("opened %s", node)
		}
		return card, nil
	}}
	s := newSource(e, 3, devNode(t), "usb", "USB3. 0 capture", "/dev/video1")
	s.usbDev = "1-2.1"
	b := m.Bridges[3]
	done := make(chan detail.D, 1)
	go func() { done <- s.stream("v4l2src ! appsink name=snk") }()
	state := func() string { st, _, _ := s.snapshot(); return st }
	held := func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.held }
	frames := func(what string, cond func() bool) {
		t.Helper()
		waitUntil(t, what, func() bool { b.Frames(1); return cond() })
	}

	frames("no signal", func() bool { return state() == StateNoSignal })
	if !b.IsHeld() || !held() || s.Info().DetailCode != "hdmi_no_signal" {
		t.Fatalf("held %v/%v, detail %q", b.IsHeld(), held(), s.Info().DetailCode)
	}
	if !strings.Contains(logs.String(), "event=usb_signal_watch") || !strings.Contains(logs.String(), "chip=a7") {
		t.Errorf("log: %s", logs.String())
	}
	card.set(0x00, nil) // plugged in
	frames("signal back", func() bool { return state() == StateLive })
	if b.IsHeld() || held() {
		t.Error("still held with the signal back")
	}
	card.set(0x18, nil) // unplugged
	frames("no signal again", func() bool { return state() == StateNoSignal && b.IsHeld() })
	card.set(0x18, os.ErrClosed)
	frames("the flag given up", func() bool { return card.isClosed() })
	if b.IsHeld() || held() || !strings.Contains(logs.String(), "event=usb_signal_failed") {
		t.Errorf("after giving up: held %v/%v", b.IsHeld(), held())
	}
	frames("its frames again", func() bool { return state() == StateLive })
	s.stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end")
	}
}

// Ending an opening releases the card and the hold; a device without the flag (a webcam) is watched by its
// picture, and a camera's frames keep it live.
func TestUSBSignalOpenings(t *testing.T) {
	old := signalEvery
	signalEvery = 50 * time.Millisecond
	t.Cleanup(func() { signalEvery = old })
	e, m, _ := startEngine(t, nil, usbCatalogue())
	logs := &lockedLog{}
	elog.SetOutput(logs)
	card := &ms2109{flags: 0x30}
	e.usbSig = usbsignal.Sys{Root: ms2109Board(t), OpenHID: func(string) (usbsignal.HID, error) { return card, nil }}
	s := newSource(e, 3, devNode(t), "usb", "A", "/dev/video1")
	s.usbDev = "1-2.1"
	b := m.Bridges[3]
	done := make(chan detail.D, 1)
	go func() { done <- s.stream("v4l2src ! appsink name=snk") }()
	waitUntil(t, "held", func() bool { b.Frames(1); return b.IsHeld() })
	s.stop()
	<-done
	s.mu.Lock()
	h := s.held
	s.mu.Unlock()
	if !card.isClosed() || h || s.sig != nil {
		t.Errorf("after the opening: closed %v, held %v", card.isClosed(), h)
	}

	// A webcam (not on 1-2.1's ids): its picture, and with no repeated thumbnails it is live
	s2 := newSource(e, 3, devNode(t), "usb", "B", "/dev/video2")
	s2.usbDev = "2-1.3"
	done2 := make(chan detail.D, 1)
	go func() { done2 <- s2.stream("v4l2src ! appsink name=snk") }()
	waitUntil(t, "live", func() bool { b.Frames(1); st, _, _ := s2.snapshot(); return st == StateLive })
	if b.IsHeld() || !strings.Contains(logs.String(), "source=B method=picture") {
		t.Errorf("a webcam: held %v, log %s", b.IsHeld(), logs.String())
	}
	s2.stop()
	<-done2
	if s2.sig != nil {
		t.Error("the picture watch outlived its opening")
	}
}

// jpegOf is a small thumbnail: uniform (luma y) or, with noise, a camera's.
func jpegOf(t *testing.T, y uint8, noise bool) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 64, 36))
	for i := range img.Pix {
		img.Pix[i] = y
		if noise {
			img.Pix[i] += uint8(rand.Intn(40))
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Any capture card without the flag: its thumbnails the same black picture again and again → no signal (held,
// the slate); a moving picture → live again.
func TestUSBCardPicture(t *testing.T) {
	old, oldLimit := signalEvery, usbsignal.StillLimit
	signalEvery, usbsignal.StillLimit = 50*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { signalEvery, usbsignal.StillLimit = old, oldLimit })
	e, m, _ := startEngine(t, nil, usbCatalogue())
	e.usbSig = usbsignal.Sys{Root: t.TempDir()} // no card with a flag
	s := newSource(e, 3, devNode(t), "usb", "Cam Link 4K", "/dev/video1")
	s.usbDev = "2-1.2"
	b := m.Bridges[3]
	done := make(chan detail.D, 1)
	go func() { done <- s.stream("v4l2src ! appsink name=snk") }()
	state := func() string { st, _, _ := s.snapshot(); return st }
	black := jpegOf(t, 16, false)
	feed := func(what string, jpg func() []byte, cond func() bool) {
		t.Helper()
		waitUntil(t, what, func() bool {
			b.Frames(1)
			e.pictures[3].Add(jpg(), time.Now())
			return cond()
		})
	}
	feed("live first", func() []byte { return black }, func() bool { return state() == StateLive })
	feed("no signal", func() []byte { return black }, func() bool { return state() == StateNoSignal && b.IsHeld() })
	if s.Info().DetailCode != "hdmi_no_signal" {
		t.Errorf("detail %q", s.Info().DetailCode)
	}
	feed("a picture again", func() []byte { return jpegOf(t, 100, true) }, func() bool { return state() == StateLive && !b.IsHeld() })
	s.stop()
	<-done
}
