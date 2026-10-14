package modem

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// What the app changes on a ModemManager modem (MODEMS.md §7): its NetworkManager profile (APN, roaming, PIN),
// its network mode and the reset. Written from ModemManager's D-Bus API documentation (1.10); to be checked with
// real modems (M0).

// MMModemMode bits.
const (
	modeCS = 1 << 0
	mode2G = 1 << 1
	mode3G = 1 << 2
	mode4G = 1 << 3
	mode5G = 1 << 4 // ModemManager ≥ 1.14
)

// combo: a supported (allowed, preferred) pair of SupportedModes.
type combo struct{ Allowed, Preferred uint32 }

// mmCombos: SupportedModes, an a(uu).
func mmCombos(v dbus.Variant) []combo {
	var out []combo
	add := func(pair []any) {
		if len(pair) != 2 {
			return
		}
		a, ok1 := pair[0].(uint32)
		p, ok2 := pair[1].(uint32)
		if ok1 && ok2 {
			out = append(out, combo{a, p})
		}
	}
	switch list := v.Value().(type) {
	case [][]any:
		for _, pair := range list {
			add(pair)
		}
	case []any:
		for _, e := range list {
			if pair, ok := e.([]any); ok {
				add(pair)
			}
		}
	}
	return out
}

// mmModeNow: the mode in force (CurrentModes, a (uu)) in our words, and what can be chosen.
func mmModeNow(cur dbus.Variant, combos []combo) (string, []string) {
	modes := []string{"auto"}
	if pickCombo(combos, "4g") != nil {
		modes = append(modes, "4g")
	}
	if pickCombo(combos, "5g") != nil {
		modes = append(modes, "5g")
	}
	now := "auto"
	if pair, ok := cur.Value().([]any); ok && len(pair) == 2 {
		a, _ := pair[0].(uint32)
		p, _ := pair[1].(uint32)
		switch {
		case a&^modeCS == mode4G:
			now = "4g"
		case a&mode5G != 0 && (p == mode5G || a&^modeCS == mode4G|mode5G):
			now = "5g"
		}
	}
	return now, modes
}

// pickCombo: the supported pair for a mode: auto = the widest with no preference; 4g = 4G only; 5g = 4G+5G
// preferring 5G (else any with 5G).
func pickCombo(combos []combo, mode string) *combo {
	var best *combo
	for i := range combos {
		c := &combos[i]
		a := c.Allowed &^ modeCS
		switch mode {
		case "auto":
			if best == nil || bitsOf(a) > bitsOf(best.Allowed&^modeCS) || bitsOf(a) == bitsOf(best.Allowed&^modeCS) && c.Preferred == 0 {
				best = c
			}
		case "4g":
			if a == mode4G {
				return c
			}
		case "5g":
			if a&mode5G != 0 && (best == nil || c.Preferred == mode5G && a&mode4G != 0) {
				best = c
			}
		}
	}
	return best
}

func bitsOf(v uint32) int {
	n := 0
	for ; v != 0; v &= v - 1 {
		n++
	}
	return n
}

// ErrWrongPIN: the SIM refused the PIN; ErrPUK: it is blocked (the PUK is entered on a phone).
var (
	ErrWrongPIN = errors.New("modem: wrong PIN")
	ErrPUK      = errors.New("modem: SIM blocked (PUK)")
	ErrNotHere  = errors.New("modem: not available")
	// ErrNotLocked: a PIN for a SIM that does not ask for one
	ErrNotLocked = errors.New("modem: the SIM does not ask for its PIN")
)

// pinError: ModemManager's error for a SendPin/EnablePin, in our terms.
func pinError(err error) error {
	if err == nil {
		return nil
	}
	var de dbus.Error
	name := err.Error()
	if errors.As(err, &de) {
		name = de.Name
	}
	switch {
	case strings.Contains(name, "SimPuk"), strings.Contains(name, "Puk"):
		return ErrPUK
	case strings.Contains(name, "IncorrectPassword"), strings.Contains(name, "IncorrectPin"), strings.Contains(name, "SimPin"):
		return ErrWrongPIN
	}
	return err
}

// Apply: the profile (APN, roaming) and the network mode as c says. A Mode of "" leaves the modem's as it is (a
// modem nobody configured); "auto" sets the widest one back.
func (b *MM) Apply(ctx context.Context, md Modem, c Config) error {
	if _, err := ensureProfile(ctx, b.nm, b.profile(md, c), md.path, false); err != nil {
		return err
	}
	if c.Mode != "" && c.Mode != md.Mode && md.path != "" {
		cb := pickCombo(md.combos, c.Mode)
		if cb == nil {
			return ErrConfig
		}
		return b.bus.call(ctx, dbus.ObjectPath(md.path), mmModem+".SetCurrentModes", *cb)
	}
	return nil
}

// profile: what the modem's NetworkManager profile should say for c (the automatic APN resolved).
func (b *MM) profile(md Modem, c Config) gsmProfile {
	apn := c.APN
	if apn == "" {
		apn = AutoAPN(b.Providers, md.netCode, md.simName)
	}
	return gsmProfile{ID: md.ID, DeviceID: md.devID, SimID: md.simID, APN: apn, Roaming: c.Roaming}
}

// Unlock: the PIN of a SIM that asks for it (SendPin), kept in the profile for that SIM so that it unlocks by itself
// next time (D-M6). remove: then the SIM's PIN is turned off (EnablePin false) and the stored one forgotten; a locked
// SIM is unlocked first, since the lock cannot be changed while the SIM waits for its PIN. A PIN for a SIM that does
// not ask for one is refused: it could not be checked, and a wrong one stored would be tried at the next start.
func (b *MM) Unlock(ctx context.Context, md Modem, c Config, pin string, remove bool) error {
	if md.simPath == "" || md.simPath == "/" {
		return ErrNotHere
	}
	locked := md.SIM == "pin"
	if !locked && !remove {
		return ErrNotLocked
	}
	sim := dbus.ObjectPath(md.simPath)
	if locked {
		if err := pinError(b.bus.call(ctx, sim, mmSim+".SendPin", pin)); err != nil {
			return err
		}
	}
	if remove {
		if err := pinError(b.bus.call(ctx, sim, mmSim+".EnablePin", pin, false)); err != nil {
			return err
		}
	}
	p := b.profile(md, c)
	if p.SimID == "" { // a locked SIM may not have said who it is: unlocked, it does
		if sp, err := b.bus.props(ctx, sim, mmSim); err == nil {
			p.SimID = str(sp, "SimIdentifier")
		}
	}
	if remove {
		p.DropPIN = true
	} else {
		p.PIN = pin
	}
	// Activated again in any case: NetworkManager gave up on the modem while its SIM was locked
	if _, err := ensureProfile(ctx, b.nm, p, md.path, true); err != nil {
		return fmt.Errorf("unlocked, but the profile was not written: %w", err)
	}
	return nil
}

// Reset: the modem starts again (it leaves the SRTLA share for ~30 s).
func (b *MM) Reset(ctx context.Context, md Modem) error {
	if md.path == "" {
		return ErrNotHere
	}
	return b.bus.call(ctx, dbus.ObjectPath(md.path), mmModem+".Reset")
}
