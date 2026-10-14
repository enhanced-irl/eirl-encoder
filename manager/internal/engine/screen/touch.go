package screen

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// The touch screen by evdev (engine_mixer.py's Touch): "down" when the finger goes down and "up" when it is
// lifted (with how long it was down), at the position where it went down, in fractions 0..1 of the screen.
// Unplugged, it is looked for again.

const (
	evSyn, evKey, evAbs = 0, 1, 3
	synReport           = 0
	btnTouch            = 0x14a
	absMTPositionX      = 0x35
	absMTPositionY      = 0x36
	eventSize           = 24 // struct input_event on a 64-bit kernel: timeval (16) + type, code (2+2) + value (4)
	touchRetry          = 5 * time.Second
)

// TouchEvent is a finger going down or up.
type TouchEvent struct {
	Ev   string // down | up
	X, Y float64
	Held time.Duration
	At   time.Time // the kernel's time of the event (zero in tests): touch to screen latency
}

// touchDecoder turns evdev events into TouchEvents.
type touchDecoder struct {
	maxX, maxY   int32
	x, y         int32
	hasX, hasY   bool
	down         bool
	downAt       time.Duration
	hasXY        bool
	downX, downY float64
}

// event takes one evdev event (now: a monotonic time) and returns a TouchEvent when there is one.
func (d *touchDecoder) event(typ, code uint16, val int32, now time.Duration) (TouchEvent, bool) {
	switch {
	case typ == evAbs && code == absMTPositionX:
		d.x, d.hasX = val, true
	case typ == evAbs && code == absMTPositionY:
		d.y, d.hasY = val, true
	case typ == evKey && code == btnTouch:
		if val != 0 && !d.down {
			d.down, d.downAt, d.hasXY = true, now, false
		} else if val == 0 && d.down {
			d.down = false
			if d.hasXY {
				return TouchEvent{Ev: "up", X: d.downX, Y: d.downY, Held: now - d.downAt}, true
			}
		}
	case typ == evSyn && code == synReport && d.down && !d.hasXY && d.hasX && d.hasY:
		// The position when the finger went down
		d.downX, d.downY = float64(d.x)/float64(max(1, d.maxX)), float64(d.y)/float64(max(1, d.maxY))
		d.hasXY = true
		return TouchEvent{Ev: "down", X: d.downX, Y: d.downY}, true
	}
	return TouchEvent{}, false
}

// findTouch is the first input device with ABS_MT_POSITION_X: its /dev path and name.
func findTouch(root string) (string, string) {
	devs, _ := filepath.Glob(filepath.Join(root+"/", "/sys/class/input/event*"))
	sort.Strings(devs)
	for _, dev := range devs {
		words := strings.Fields(readText(filepath.Join(dev, "device/capabilities/abs")))
		if len(words) == 0 {
			continue
		}
		last, err := strconv.ParseUint(words[len(words)-1], 16, 64)
		if err != nil || last>>absMTPositionX&1 == 0 {
			continue
		}
		return filepath.Join(root+"/", "/dev/input", filepath.Base(dev)), readText(filepath.Join(dev, "device/name"))
	}
	return "", ""
}

// touchReader finds the touch screen, reads it and sends what it gets to out; it looks for it again if it
// goes away. present says whether it is there (the state's screen.touch).
type touchReader struct {
	root    string
	out     chan<- TouchEvent
	stop    <-chan struct{}
	present atomic.Bool
	start   time.Time
	file    atomic.Pointer[os.File]
}

func (t *touchReader) now() time.Duration { return time.Since(t.start) }

func (t *touchReader) run() {
	t.start = time.Now()
	warned := false
	for {
		path, name := findTouch(t.root)
		if path == "" {
			if !t.wait(touchRetry) {
				return
			}
			continue
		}
		f, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			if !warned {
				elog.Event("mixer", "touch_open_failed", "path", path, "err", err)
				warned = true
			}
			if !t.wait(touchRetry) {
				return
			}
			continue
		}
		warned = false
		t.file.Store(f)
		mx, my := absMax(f, absMTPositionX), absMax(f, absMTPositionY)
		elog.Event("mixer", "touch_found", "path", path, "name", name, "range", strconv.Itoa(int(mx))+"x"+strconv.Itoa(int(my)))
		t.present.Store(true)
		t.read(f, mx, my)
		t.present.Store(false)
		t.file.Store(nil)
		f.Close()
		select {
		case <-t.stop:
			return
		default:
		}
		elog.Event("mixer", "touch_lost")
	}
}

func (t *touchReader) wait(d time.Duration) bool {
	select {
	case <-t.stop:
		return false
	case <-time.After(d):
		return true
	}
}

// close unblocks a read in progress (Stop).
func (t *touchReader) close() {
	if f := t.file.Load(); f != nil {
		f.Close()
	}
}

func (t *touchReader) read(f *os.File, mx, my int32) {
	d := touchDecoder{maxX: mx, maxY: my}
	buf := make([]byte, eventSize*64)
	for {
		n, err := f.Read(buf)
		if err != nil || n == 0 {
			return
		}
		now := t.now()
		for i := 0; i+eventSize <= n; i += eventSize {
			e := buf[i : i+eventSize]
			typ := binary.LittleEndian.Uint16(e[16:])
			code := binary.LittleEndian.Uint16(e[18:])
			val := int32(binary.LittleEndian.Uint32(e[20:]))
			if ev, ok := d.event(typ, code, val, now); ok {
				// evdev stamps with CLOCK_REALTIME by default
				ev.At = time.Unix(int64(binary.LittleEndian.Uint64(e[0:])), int64(binary.LittleEndian.Uint64(e[8:]))*1000)
				select {
				case t.out <- ev:
				default: // the screen is not keeping up: a touch is dropped rather than blocking the reader
				}
			}
		}
	}
}
