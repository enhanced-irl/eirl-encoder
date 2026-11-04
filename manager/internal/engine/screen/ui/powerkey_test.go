package ui

import (
	"testing"
	"time"
)

// holdPowerKey: the board's power button down, held for d (the clock moved, a tick), and up.
func (h *harness) holdPowerKey(d time.Duration) {
	h.u.OnPowerKey(true)
	h.advance(d)
	h.u.Tick()
	h.u.OnPowerKey(false)
}

func TestPowerKeyPressOpensMenu(t *testing.T) {
	h := newHarness(t)
	h.holdPowerKey(time.Second)
	if !h.u.menuOpen || h.u.Power() != nil || len(h.cmds) != 0 {
		t.Fatalf("a press: menu %v, power %+v, cmds %v", h.u.menuOpen, h.u.Power(), h.cmds)
	}
	if help, warn := h.u.menuHelp(h.now()); warn || help != Spanish.MenuHelp {
		t.Fatalf("the menu's help: %q %v", help, warn)
	}
	// Released before PowerKeyHold: nothing later powers off
	h.advance(PowerKeyHold)
	h.u.Tick()
	if h.u.Power() != nil {
		t.Fatal("powered off after the release")
	}
	// The menu answers touches as always
	h.press("m_cancel", 0.1)
	if h.u.menuOpen {
		t.Fatal("cancel")
	}
}

func TestPowerKeyHoldPowersOff(t *testing.T) {
	h := newHarness(t)
	changed := 0
	h.u.Changed = func() { changed++ }
	h.u.OnPowerKey(true)
	h.advance(PowerKeyHold / 2)
	h.u.Tick()
	if p, ok := h.u.powerKeyProgress(h.now()); !ok || p < 0.49 || p > 0.51 || h.u.Power() != nil {
		t.Fatalf("halfway: %v %v %+v", p, ok, h.u.Power())
	}
	h.advance(PowerKeyHold / 2)
	h.u.Tick()
	p := h.u.Power()
	if p == nil || p.Want != "poweroff" || p.Seq != 1 || changed != 1 || h.u.powering != "poweroff" || h.u.menuOpen {
		t.Fatalf("held: %+v (changed %d, powering %q, menu %v)", p, changed, h.u.powering, h.u.menuOpen)
	}
	// Still held: it is not asked twice, and a release and a new press while powering off do nothing
	h.advance(time.Second)
	h.u.Tick()
	h.u.OnPowerKey(false)
	h.u.OnPowerKey(true)
	h.advance(PowerKeyHold)
	h.u.Tick()
	if h.u.Power().Seq != 1 || changed != 1 {
		t.Fatalf("asked again: %+v %d", h.u.Power(), changed)
	}
}

func TestPowerKeyRefusedWhileBusy(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*Status)
		want string
	}{
		{"live", func(st *Status) { st.Output = OutputStatus{Mode: "on", State: "on"} }, Spanish.PowerKeyLive},
		{"connecting", func(st *Status) { st.Output = OutputStatus{Mode: "on", State: "connecting"} }, Spanish.PowerKeyLive},
		{"recording", func(st *Status) { st.Record = &RecordStatus{Mode: "on", State: "on"} }, Spanish.PowerKeyRecording},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			c.set(h.u.st)
			h.u.OnPowerKey(true)
			if help, warn := h.u.menuHelp(h.now()); !h.u.menuOpen || !warn || help != c.want {
				t.Fatalf("on press: menu %v, %q %v", h.u.menuOpen, help, warn)
			}
			if _, ok := h.u.powerKeyProgress(h.now()); ok {
				t.Fatal("Power off fills up while busy")
			}
			h.advance(PowerKeyHold + time.Second)
			h.u.Tick()
			h.u.OnPowerKey(false)
			if h.u.Power() != nil || h.u.powering != "" || !h.u.menuOpen {
				t.Fatalf("held while busy: %+v %q %v", h.u.Power(), h.u.powering, h.u.menuOpen)
			}
			// The notice goes after a while
			h.advance(PowerKeyNoteFor)
			if _, warn := h.u.menuHelp(h.now()); warn {
				t.Fatal("the notice stays")
			}
		})
	}
}

func TestPowerKeyDecidedAtTheHold(t *testing.T) {
	// Pressed while idle, then Go live (from the web) before PowerKeyHold: it does not power off
	h := newHarness(t)
	h.u.OnPowerKey(true)
	h.advance(time.Second)
	st := *h.u.st
	st.Output = OutputStatus{Mode: "on", State: "connecting"}
	h.u.SetStatus(&st)
	h.advance(PowerKeyHold)
	h.u.Tick()
	if h.u.Power() != nil {
		t.Fatal("powered off while going live")
	}
	if help, warn := h.u.menuHelp(h.now()); !warn || help != Spanish.PowerKeyLive {
		t.Fatalf("%q %v", help, warn)
	}
}

func TestPowerKeyLocked(t *testing.T) {
	h := newHarness(t)
	h.u.st.Screen.Locked = true
	h.u.OnPowerKey(true)
	if h.u.menuOpen || h.u.hint != "Mantén el botón 3 s para apagar" {
		t.Fatalf("locked press: menu %v, hint %q", h.u.menuOpen, h.u.hint)
	}
	h.advance(PowerKeyHold)
	h.u.Tick()
	h.u.OnPowerKey(false)
	if p := h.u.Power(); p == nil || p.Want != "poweroff" {
		t.Fatalf("locked and idle, held: %+v", p)
	}

	// Locked and live: the hint says why, nothing powers off
	h = newHarness(t)
	h.u.st.Screen.Locked = true
	h.u.st.Output = OutputStatus{Mode: "on", State: "on"}
	h.holdPowerKey(PowerKeyHold + time.Second)
	if h.u.Power() != nil || h.u.menuOpen || h.u.hint != Spanish.PowerKeyLive {
		t.Fatalf("locked and live: %+v %v %q", h.u.Power(), h.u.menuOpen, h.u.hint)
	}
}

func TestPowerKeyClosesSettings(t *testing.T) {
	h := newHarness(t)
	h.press("settings", 0.1)
	if !h.u.settingsOpen {
		t.Fatal("settings")
	}
	h.u.OnPowerKey(true)
	h.u.OnPowerKey(false)
	if h.u.settingsOpen || !h.u.menuOpen {
		t.Fatalf("settings %v, menu %v", h.u.settingsOpen, h.u.menuOpen)
	}
}

func TestPowerKeyWithoutScreen(t *testing.T) {
	// The screen off (unplugged): no menu, but the hold still powers off
	h := newHarness(t)
	h.u.State = "off"
	h.u.OnPowerKey(true)
	if h.u.menuOpen {
		t.Fatal("a menu without a screen")
	}
	h.advance(PowerKeyHold)
	h.u.Tick()
	if p := h.u.Power(); p == nil || p.Want != "poweroff" {
		t.Fatalf("%+v", p)
	}
}

func TestPowerKeyStale(t *testing.T) {
	// A press whose release never comes is dropped (a new press counts from zero)
	h := newHarness(t)
	h.u.st.Output = OutputStatus{Mode: "on", State: "on"}
	h.u.OnPowerKey(true)
	h.advance(PressStale + time.Second)
	h.u.Tick()
	if h.u.pkey != nil {
		t.Fatal("not dropped")
	}
	h.u.st.Output = OutputStatus{Mode: "off", State: "off"}
	h.u.OnPowerKey(true)
	h.advance(PowerKeyHold - 100*time.Millisecond)
	h.u.Tick()
	if h.u.Power() != nil {
		t.Fatal("the new press counted the old one's time")
	}
}
