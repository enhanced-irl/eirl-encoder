// Package tcaudio watches the board's HDMI audio (TC358743 → I2S) for the two failures that do not fix
// themselves (the launcher's watcher, scripts/eirl-engine.sh; README «Audio que no vuelve»).
//
// Every 2 s it reads AU_STATUS0 (0x8523) of the TC358743: bit 0 = samples arrive, bit 5 = the audio PLL
// is locked in frequency.
//   - Samples + lock but the capture (the PCM's hw_ptr) does not move: the capture started without an
//     I2S clock, the I2S4 reset failed and it is dead. Restarting the engine fixes it.
//   - Samples WITHOUT lock (0x41 instead of 0x61) for AUDIO_LOCK_TIMEOUT s: the chip's PLL is stuck
//     (intermittent, after HDMI renegotiations) and gives no I2S clock. Neither the SYSCTL software reset
//     nor rebooting the Jetson fix it; the hardware reset (the board's RST pin, wired: see README) does.
//     First, without cutting the video, it tries the chip's audio commands once (reinitialise the
//     buffer, restart the frequency detection, restart the NCO); then, only if the engine allows it
//     (not broadcasting and the HDMI not on air), a hotplug pulse: the EDID rewritten, the source
//     renegotiates HDMI from scratch (Sys.Hotplug). It logs which step worked; if none did, it asks for
//     the hardware reset at the next start (STATE_DIR/hw-reset.request, consumed by hdmi.Sys.Prepare)
//     and the engine exits so that systemd restarts it.
//
// It runs only when the board has a TC358743 and the ALSA card exists (Enabled). In the engine it is a
// goroutine: Run returns a non-nil error when the engine must restart.
package tcaudio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

// Every is the watcher's period. The counters advance by it, not by the clock (like the launcher).
const Every = 2 * time.Second

// stallAfter is how long the capture may stand still with the audio locked.
const stallAfter = 6

// settle is the wait after each software recovery step before reading AU_STATUS0 again.
const settle = 3 * time.Second

// hotplugSettle is how long the source may take to renegotiate after the hotplug pulse (it is looked
// at every second): the driver raises the hotplug again after 100 ms and a source takes 1-5 s.
const hotplugSettle = 10 * time.Second

// The engine must restart (exit 1: systemd starts it again).
var (
	// ErrCaptureStalled: HDMI audio arrives but the I2S capture does not move
	ErrCaptureStalled = errors.New("hdmi audio arrives but the i2s capture does not move")
	// ErrHWReset: the hardware reset was asked for; it happens at the next start
	ErrHWReset = errors.New("tc358743 hardware reset requested")
)

var reLast = regexp.MustCompile(`^[0-9]{1,12}$`)

// Config is the watcher's part of the engine's configuration.
type Config struct {
	I2CBus        int    // TC_I2C (8)
	LockTimeout   int    // AUDIO_LOCK_TIMEOUT, s (10; 0 = never)
	ResetInterval int    // HW_RESET_INTERVAL, s (300): minimum between two hardware resets
	StateDir      string // STATE_DIR: hw-reset.request and hw-reset.last
	ALSACard      string // ALSA_CARD (tegrasndt210ref)
	ADMAIF        int    // ALSA_ADMAIF (2): the capture is pcm<ADMAIF-1>c
	ResetPin      bool   // the device tree declares the reset pin (hdmi.Sys.HasResetPin)
}

// Sys is where the watcher reads and what it runs; tests replace all of it.
type Sys struct {
	Root  string              // "" = /: prefix of /proc
	Run   sysx.Runner         // i2ctransfer
	Sleep func(time.Duration) // nil = real time
	Now   func() time.Time    // nil = time.Now (for hw-reset.last)
	// Hotplug is the last software step: the EDID rewritten drops the hotplug and the source
	// renegotiates HDMI, audio included (≈2 s without HDMI signal). False if it could not be done. nil =
	// no such step. MayHotplug says whether it may be done now (nil = never).
	Hotplug    func() bool
	MayHotplug func() bool
}

func (s Sys) path(p string) string { return filepath.Join(s.Root, p) }

// Enabled is the launcher's condition: the board has a TC358743 (hdmi.Sys.Prepare said so) and the ALSA
// card exists.
func Enabled(s Sys, tcPresent bool, card string) bool {
	return tcPresent && card != "" && sysx.Exists(s.path("/proc/asound/"+card))
}

// Watcher is the watcher's state between ticks. Only Run's goroutine uses it.
type Watcher struct {
	cfg Config
	sys Sys

	stall, nolock int // s
	lastPtr       string
	warned        bool // the "no reset pin" / "reset deferred" line was logged
	softTried     bool // the software recovery was tried since the PLL was last locked
}

// New makes the watcher.
func New(cfg Config, s Sys) *Watcher { return &Watcher{cfg: cfg, sys: s} }

// Run watches until ctx ends (nil) or the engine must restart (ErrCaptureStalled, ErrHWReset): the
// engine turns that into a fatal error (exit 1).
func (w *Watcher) Run(ctx context.Context) error {
	if w.cfg.StateDir != "" {
		_ = os.MkdirAll(w.cfg.StateDir, 0o755)
	}
	for {
		if w.pause(ctx, Every) {
			return nil
		}
		if err := w.tick(ctx); err != nil {
			return err
		}
	}
}

// pause sleeps d unless ctx ends first; true if it ended.
func (w *Watcher) pause(ctx context.Context, d time.Duration) bool {
	if w.sys.Sleep != nil {
		w.sys.Sleep(d)
		return ctx.Err() != nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

// hold sleeps d whatever happens: inside a recovery step, so that the chip is never left in manual mode.
func (w *Watcher) hold(d time.Duration) {
	if w.sys.Sleep != nil {
		w.sys.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (w *Watcher) now() time.Time {
	if w.sys.Now != nil {
		return w.sys.Now()
	}
	return time.Now()
}

func (w *Watcher) bus() string { return strconv.Itoa(w.cfg.I2CBus) }

// read is one register of the TC358743 as i2ctransfer prints it ("0x61"), or "0" if it cannot be read.
func (w *Watcher) read(hi, lo string) string {
	v, _ := w.readOK(hi, lo)
	return v
}

// readOK is read saying whether it could be read: a register that is written back must not become "0".
func (w *Watcher) readOK(hi, lo string) (string, bool) {
	r := w.sys.Run(5*time.Second, "i2ctransfer", "-f", "-y", w.bus(), "w2@0x0f", hi, lo, "r1")
	v := strings.TrimSpace(r.Out)
	if r.RC != 0 || v == "" {
		return "0", false
	}
	return v, true
}

// write writes one register (errors ignored, like the launcher).
func (w *Watcher) write(hi, lo, v string) {
	w.sys.Run(5*time.Second, "i2ctransfer", "-f", "-y", w.bus(), "w3@0x0f", hi, lo, v)
}

// Value is a register as printed ("0x61", or decimal) as a number; what does not parse is 0 (bash's
// arithmetic fails and the launcher took the "no samples" branch).
func Value(s string) uint64 {
	v, err := strconv.ParseUint(s, 0, 64)
	if err != nil {
		return 0
	}
	return v
}

// hwPtr is the capture PCM's hw_ptr ("" when it is closed or cannot be read).
func (w *Watcher) hwPtr() string {
	b, err := os.ReadFile(w.sys.path("/proc/asound/" + w.cfg.ALSACard + "/pcm" + strconv.Itoa(w.cfg.ADMAIF-1) + "c/sub0/status"))
	if err != nil {
		return ""
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "hw_ptr") {
			f := strings.Fields(line)
			if len(f) >= 3 {
				out = append(out, f[2])
			} else {
				out = append(out, "")
			}
		}
	}
	return strings.Join(out, "\n")
}

// tick is one look; non-nil when the engine must restart.
func (w *Watcher) tick(ctx context.Context) error {
	au := w.read("0x85", "0x23")
	ptr := w.hwPtr()
	v := Value(au)
	switch {
	case v&0x21 == 0x21:
		w.nolock, w.warned, w.softTried = 0, false, false
		if w.lastPtr != "" && ptr == w.lastPtr {
			w.stall += int(Every.Seconds())
		} else {
			w.stall = 0
		}
		if w.stall >= stallAfter {
			elog.Event("audio", "audio_capture_stalled", "seconds", w.stall)
			return ErrCaptureStalled
		}
	case v&0x01 != 0:
		w.stall = 0
		w.nolock += int(Every.Seconds())
		if w.cfg.LockTimeout != 0 && w.nolock >= w.cfg.LockTimeout {
			if !w.softTried {
				w.softTried = true
				elog.Event("audio", "audio_pll_unlocked", "au_status0", au, "seconds", w.nolock)
				locked, stopped := w.softRecover(ctx)
				if stopped {
					return nil
				}
				if locked {
					w.nolock, w.lastPtr = 0, ""
					return nil
				}
			}
			switch {
			case !w.cfg.ResetPin:
				if !w.warned {
					elog.Event("audio", "audio_pll_unlocked_no_reset_pin", "au_status0", au)
					w.warned = true
				}
			case w.now().Unix()-w.lastReset() >= int64(w.cfg.ResetInterval):
				elog.Event("audio", "audio_hw_reset_requested", "au_status0", au, "seconds", w.nolock)
				if f, err := os.OpenFile(filepath.Join(w.cfg.StateDir, "hw-reset.request"), os.O_WRONLY|os.O_CREATE, 0o644); err != nil {
					elog.Event("audio", "audio_hw_reset_request_failed", "err", err)
				} else {
					f.Close()
				}
				return ErrHWReset
			default:
				// There was a reset a moment ago: they are not chained (each renegotiates the HDMI)
				if !w.warned {
					elog.Event("audio", "audio_hw_reset_deferred", "au_status0", au, "interval", w.cfg.ResetInterval)
					w.warned = true
				}
			}
		}
	default:
		w.stall, w.nolock = 0, 0
	}
	w.lastPtr = ptr
	return nil
}

// lastReset is hw-reset.last (epoch seconds), 0 if missing or not only digits (it was an arithmetic
// expression in the launcher: F5 review).
func (w *Watcher) lastReset() int64 {
	b, err := os.ReadFile(filepath.Join(w.cfg.StateDir, "hw-reset.last"))
	if err != nil {
		return 0
	}
	s := strings.TrimRight(string(b), "\n")
	if !reLast.MatchString(s) {
		return 0
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// softRecover tries the chip's audio commands (CMD_AUD 0x8601) one after the other until the PLL locks.
// In automatic mode (AUTO_CMD2 0x8604 ≠ 0, as the driver leaves it) the chip ignores the host's
// commands: it goes to manual, MUTE first, and back to automatic. A step is never cut halfway (the
// chip would stay in manual mode); ctx is looked at in the waits between steps. stopped: ctx ended.
// A step whose saved register cannot be read is skipped: writing "0" back would leave AUTO_CMD2 in
// manual mode (no more automatic audio handling until the next reset) or the NCO stopped.
func (w *Watcher) softRecover(ctx context.Context) (locked, stopped bool) {
	auto2, okAuto := w.readOK("0x86", "0x04")
	nco, okNCO := w.readOK("0x86", "0x70")
	for _, step := range []string{"bufinit", "lockdet", "nco", "hotplug"} {
		if step == "hotplug" {
			return w.hotplug(ctx)
		}
		if (step == "nco" && !okNCO) || (step != "nco" && !okAuto) {
			elog.Event("audio", "audio_soft_recovery_skipped", "step", step, "reason", "register_unreadable")
			continue
		}
		switch step {
		case "bufinit":
			w.write("0x86", "0x04", "0x00")
			w.write("0x86", "0x01", "0x01")
			w.hold(100 * time.Millisecond)
			w.write("0x86", "0x01", "0x05")
			w.hold(time.Second)
			w.write("0x86", "0x01", "0x00")
			w.write("0x86", "0x04", auto2)
		case "lockdet":
			w.write("0x86", "0x04", "0x00")
			w.write("0x86", "0x01", "0x01")
			w.hold(100 * time.Millisecond)
			w.write("0x86", "0x01", "0x03")
			w.hold(1500 * time.Millisecond)
			w.write("0x86", "0x01", "0x05")
			w.hold(time.Second)
			w.write("0x86", "0x01", "0x00")
			w.write("0x86", "0x04", auto2)
		case "nco":
			w.write("0x86", "0x70", "0x02")
			w.hold(300 * time.Millisecond)
			w.write("0x86", "0x70", nco)
		}
		if w.pause(ctx, settle) {
			return false, true
		}
		a := w.read("0x85", "0x23")
		elog.Event("audio", "audio_soft_recovery", "step", step, "au_status0", a)
		if Value(a)&0x21 == 0x21 {
			return true, false
		}
	}
	return false, false
}

// hotplug is the recovery's last step: only with the engine's leave (MayHotplug), since it cuts the
// HDMI's video for a moment. It waits for the lock up to hotplugSettle.
func (w *Watcher) hotplug(ctx context.Context) (locked, stopped bool) {
	if w.sys.Hotplug == nil {
		return false, false
	}
	if w.sys.MayHotplug == nil || !w.sys.MayHotplug() {
		elog.Event("audio", "audio_soft_recovery_skipped", "step", "hotplug", "reason", "on_air")
		return false, false
	}
	if !w.sys.Hotplug() {
		elog.Event("audio", "audio_soft_recovery_skipped", "step", "hotplug", "reason", "edid_failed")
		return false, false
	}
	a := "0"
	for waited := time.Duration(0); waited < hotplugSettle; waited += time.Second {
		if w.pause(ctx, time.Second) {
			return false, true
		}
		if a = w.read("0x85", "0x23"); Value(a)&0x21 == 0x21 {
			break
		}
	}
	elog.Event("audio", "audio_soft_recovery", "step", "hotplug", "au_status0", a)
	return Value(a)&0x21 == 0x21, false
}
