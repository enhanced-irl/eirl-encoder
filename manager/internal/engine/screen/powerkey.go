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

// The board's power button by evdev (MIXER.md §14.1): J50's PWR_BTN, which L4T's device tree gives the kernel as
// the gpio-keys device's KEY_POWER. Its presses and releases go to the UI (ui.OnPowerKey), which decides; logind
// ignores the key (system/files/etc/systemd/logind.conf.d/90-eirl-powerkey.conf). Not found (another board, the
// Mac), it is looked for again now and then.

const (
	keyPower       = 116 // KEY_POWER
	powerKeyDevice = "gpio-keys"
	powerKeyRetry  = 30 * time.Second
)

// hasKey: a sysfs capabilities/key bitmap (hex words of the kernel's long, 64 bits on the board, the most
// significant first) has that key code.
func hasKey(bitmap string, code int) bool {
	words := strings.Fields(bitmap)
	i := len(words) - 1 - code/64
	if code < 0 || i < 0 {
		return false
	}
	w, err := strconv.ParseUint(words[i], 16, 64)
	return err == nil && w>>(code%64)&1 == 1
}

// findPowerKey is the board's gpio-keys input device with KEY_POWER: its /dev path, or "". Only that one: a USB
// keyboard or remote with a power key is not the board's button.
func findPowerKey(root string) string {
	devs, _ := filepath.Glob(filepath.Join(root+"/", "/sys/class/input/event*"))
	sort.Strings(devs)
	for _, dev := range devs {
		if readText(filepath.Join(dev, "device/name")) != powerKeyDevice {
			continue
		}
		if hasKey(readText(filepath.Join(dev, "device/capabilities/key")), keyPower) {
			return filepath.Join(root+"/", "/dev/input", filepath.Base(dev))
		}
	}
	return ""
}

// powerKeyEvent is one evdev event as the power button: down (true) or up; ok false for anything else (the
// autorepeat's 2, the other keys of gpio-keys such as Force recovery, the reports).
func powerKeyEvent(typ, code uint16, val int32) (down, ok bool) {
	if typ != evKey || code != keyPower || (val != 0 && val != 1) {
		return false, false
	}
	return val == 1, true
}

// powerKeyReader finds the power button, reads it and sends its presses and releases to out. A release is never
// dropped (the UI would think it still held), so the send blocks until the UI takes it or the screen stops.
type powerKeyReader struct {
	root string
	out  chan<- bool
	stop <-chan struct{}
	file atomic.Pointer[os.File]
}

func (p *powerKeyReader) run() {
	warned := false
	for {
		path := findPowerKey(p.root)
		var f *os.File
		var err error
		if path != "" {
			f, err = os.OpenFile(path, os.O_RDONLY, 0)
		}
		if path == "" || err != nil {
			if !warned {
				elog.Event("mixer", "power_key_missing", "path", path, "err", err)
				warned = true
			}
			if !p.wait(powerKeyRetry) {
				return
			}
			continue
		}
		warned = false
		p.file.Store(f)
		elog.Event("mixer", "power_key_found", "path", path)
		down := p.read(f)
		p.file.Store(nil)
		f.Close()
		if down { // gone while held: it is not held any more
			p.send(false)
		}
		select {
		case <-p.stop:
			return
		default:
		}
		elog.Event("mixer", "power_key_lost")
		if !p.wait(touchRetry) {
			return
		}
	}
}

func (p *powerKeyReader) wait(d time.Duration) bool {
	select {
	case <-p.stop:
		return false
	case <-time.After(d):
		return true
	}
}

// close unblocks a read in progress (Stop).
func (p *powerKeyReader) close() {
	if f := p.file.Load(); f != nil {
		f.Close()
	}
}

func (p *powerKeyReader) send(down bool) bool {
	select {
	case p.out <- down:
		return true
	case <-p.stop:
		return false
	}
}

// read reads until the device goes away (or Stop closes it); it returns whether the button was down then.
func (p *powerKeyReader) read(f *os.File) bool {
	buf := make([]byte, eventSize*16)
	down := false
	for {
		n, err := f.Read(buf)
		if err != nil || n == 0 {
			return down
		}
		for i := 0; i+eventSize <= n; i += eventSize {
			e := buf[i : i+eventSize]
			d, ok := powerKeyEvent(binary.LittleEndian.Uint16(e[16:]), binary.LittleEndian.Uint16(e[18:]),
				int32(binary.LittleEndian.Uint32(e[20:])))
			if !ok || d == down {
				continue
			}
			down = d
			if !p.send(d) {
				return false
			}
		}
	}
}
