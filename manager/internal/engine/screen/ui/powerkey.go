package ui

import (
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// The board's power button (MIXER.md §14.1): J50's PWR_BTN, which L4T's device tree gives the kernel as gpio-keys
// KEY_POWER. logind ignores it (system/files/etc/systemd/logind.conf.d/90-eirl-powerkey.conf), so a bump no longer
// powers the board off at once: the screen decides. A press opens the Power menu; held PowerKeyHold, it powers off
// through the same request as the menu (screen.power), but never while streaming or recording, and then it says
// why. The PMIC's own long press (a hard cut, some seconds later) is the hardware's and stays: it is what is left
// when the engine is down.

// PowerKeyHold: the power button held this long powers off (well below the PMIC's hard cut).
const PowerKeyHold = 3 * time.Second

// PowerKeyNoteFor: how long the menu says why it does not power off.
const PowerKeyNoteFor = 6 * time.Second

// powerKey is the power button while it is down.
type powerKey struct {
	since time.Time
	done  bool // the hold was acted on: powered off, or refused
}

// powerBusy is why the board must not be powered off from the button now ("" = it may): streaming (or trying to,
// with the output on) or recording.
func (u *UI) powerBusy() string {
	if u.st.Output.Mode == "on" {
		return "streaming"
	}
	if recOn(u.st.Record) {
		return "recording"
	}
	return ""
}

func (u *UI) powerBusyText(why string) string {
	if why == "recording" {
		return u.Tx.PowerKeyRecording
	}
	return u.Tx.PowerKeyLive
}

// OnPowerKey is the power button going down or up. The hold works whatever the screen shows (off, the wizard,
// locked): only the menu needs the screen.
func (u *UI) OnPowerKey(down bool) {
	if !down {
		if u.pkey != nil {
			u.pkey = nil
			u.Dirty = true
		}
		return
	}
	if u.pkey != nil || u.powering != "" {
		return // already down, or already powering off
	}
	now := u.now()
	u.pkey = &powerKey{since: now}
	u.Dirty = true
	busy := u.powerBusy()
	elog.Event("mixer", "power_key_pressed", "busy", busy)
	switch {
	case u.State != "on" || u.Lay == nil || u.wizardOn():
		// Nothing to show (no screen, or the first-boot wizard): the hold still counts
	case u.locked():
		// Locked, the menu does not open (none of its buttons would answer)
		if busy != "" {
			u.say(u.powerBusyText(busy), PowerKeyNoteFor)
		} else {
			u.say(sprintf(u.Tx.PowerKeyHoldF, int(PowerKeyHold/time.Second)), 3*time.Second)
		}
	default:
		if u.settingsOpen {
			u.openSettings(false) // like locking from the web: the keyboard is wiped
		}
		u.menuOpen = true
		if busy != "" {
			u.menuNote, u.menuNoteUntil = u.powerBusyText(busy), now.Add(PowerKeyNoteFor)
		}
	}
}

// powerKeyTick is Tick's part: the hold done, and a press without "up" dropped.
func (u *UI) powerKeyTick(now time.Time) {
	pk := u.pkey
	if pk == nil {
		return
	}
	if !pk.done && now.Sub(pk.since) >= PowerKeyHold {
		pk.done = true
		u.powerKeyHeld(now)
	}
	if now.Sub(pk.since) > PressStale {
		u.pkey = nil
	}
}

// powerKeyHeld: held PowerKeyHold. Whether it may power off is decided now, not when it went down.
func (u *UI) powerKeyHeld(now time.Time) {
	if busy := u.powerBusy(); busy != "" {
		elog.Event("mixer", "power_key_refused", "why", busy)
		text := u.powerBusyText(busy)
		if u.menuOpen {
			u.menuNote, u.menuNoteUntil = text, now.Add(PowerKeyNoteFor)
		} else {
			u.say(text, PowerKeyNoteFor)
		}
		u.Dirty = true
		return
	}
	elog.Event("mixer", "power_key_poweroff", "held_s", PowerKeyHold.Seconds())
	u.requestPower("poweroff", now)
}

// powerKeyProgress is the hold's progress (0..1) while it can still power off, for the menu's Power off button.
func (u *UI) powerKeyProgress(now time.Time) (float64, bool) {
	pk := u.pkey
	if pk == nil || pk.done || u.powerBusy() != "" {
		return 0, false
	}
	return min(1, float64(now.Sub(pk.since))/float64(PowerKeyHold)), true
}

// menuHelp is the Power menu's subtitle: why the button does not power off, while that is said, or how to confirm.
func (u *UI) menuHelp(now time.Time) (string, bool) {
	if u.menuNote != "" && now.Before(u.menuNoteUntil) {
		return u.menuNote, true
	}
	return u.Tx.MenuHelp, false
}
