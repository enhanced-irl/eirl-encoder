// Package fan: the encoder's own fan control (FAN.md), the first step of IMAGE.md's D-I7: one temperature
// curve, run by the root daemon eirl-fan instead of the kernel's control (which leaves the fan stopped until
// about 51 °C) or the test board's old automagic-fan. Thermal profiles chosen in the interface and the
// "camera" mode (more fan off the air, quieter on it) come later, on top of this.
package fan

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// MaxPWM: the fan at full speed (the pwm-fan driver's scale, /sys/devices/pwm-fan/target_pwm).
const MaxPWM = 255

// HotC: at or above this temperature the fan goes to full speed whatever the curve says. The chip throttles
// at about 100 °C (soctherm) and shuts down a few degrees above.
const HotC = 80.0

// Point: the fan's PWM at a temperature (°C).
type Point struct {
	C   float64
	PWM int
}

// Curve: PWM by temperature, linear between its points, which go up in temperature. Below the first point the
// fan is stopped; from the last one on, at the last point's PWM.
type Curve []Point

// Default: the curve the encoder ships with (FAN.md §2, from what the test board measured on Oct 15: idle, the fan
// at 80 held the chip at 45 °C and at 130 at 36 °C). It starts at 38 °C at a speed it surely spins at (the
// driver's own table puts 1000 rpm at 80) and is at full speed from 65 °C.
var Default = Curve{{38, 80}, {50, 140}, {58, 200}, {65, MaxPWM}}

// Validate: points in strictly increasing temperature, PWM within 0-255 and never going down.
func (c Curve) Validate() error {
	if len(c) == 0 {
		return fmt.Errorf("empty curve")
	}
	for i, p := range c {
		if p.PWM < 0 || p.PWM > MaxPWM || math.IsNaN(p.C) || math.IsInf(p.C, 0) {
			return fmt.Errorf("point %d out of range: %v", i, p)
		}
		if i > 0 && (p.C <= c[i-1].C || p.PWM < c[i-1].PWM) {
			return fmt.Errorf("point %d does not go up from the one before: %v after %v", i, p, c[i-1])
		}
	}
	return nil
}

// At: the curve's PWM at t °C.
func (c Curve) At(t float64) int {
	if len(c) == 0 || t < c[0].C {
		return 0
	}
	for i := 1; i < len(c); i++ {
		if t < c[i].C {
			a, b := c[i-1], c[i]
			return a.PWM + int(math.Round(float64(b.PWM-a.PWM)*(t-a.C)/(b.C-a.C)))
		}
	}
	return c[len(c)-1].PWM
}

// String: the curve as people read it ("38°C→80 50°C→140 …").
func (c Curve) String() string {
	s := make([]string, len(c))
	for i, p := range c {
		s[i] = fmt.Sprintf("%g°C→%d", p.C, p.PWM)
	}
	return strings.Join(s, " ")
}

// Controller: the speed for each new reading. It speeds up at once; it slows down only when the temperature is
// Hyst degrees below what the current speed was set for, so a temperature that hovers around a point (the
// sensors move in steps of 0.5 °C) does not make the fan hunt, and the fan stops at the first point minus Hyst.
type Controller struct {
	Curve Curve
	Hyst  float64
	pwm   int
}

// Next: the PWM for a reading of t °C (ok = false: the reading failed, which runs the fan at full speed).
func (c *Controller) Next(t float64, ok bool) int {
	if !ok || math.IsNaN(t) || t >= HotC {
		c.pwm = MaxPWM
		return c.pwm
	}
	if up := c.Curve.At(t); up >= c.pwm {
		c.pwm = up
	} else if down := c.Curve.At(t + c.Hyst); down < c.pwm {
		c.pwm = down
	}
	return c.pwm
}

// PWM: the current speed.
func (c *Controller) PWM() int { return c.pwm }

// Switch: a fan that is only on or off (the second fan, FAN.md §5), by temperature: on at OnC or above, off at OffC or
// below once it has been on for MinOn, so a temperature around a threshold does not click it on and off. Safety as for
// the curve: at HotC or above, or with a reading that failed, on at once (MinOn never delays switching on).
//
// Switched off in Settings (SetOff), the thresholds move up to HotC and OffHotC: only the safety switches it on.
type Switch struct {
	OnC, OffC float64
	MinOn     time.Duration
	on        bool
	off       bool
	since     time.Time
}

// OffHotC: switched off in Settings and on because of HotC, where it goes off again (5 °C below, as the defaults'
// hysteresis, so a temperature around HotC does not click it either).
const OffHotC = HotC - 5

// DefaultSwitch: the second fan's thresholds (FAN.md §5). Off idle (40-46 °C) and in an ordinary stream (49.5-53 °C,
// measured on the test board on Nov 4); on from where the curve has the main fan at about 70 % (178 of 255 at 55 °C)
// on its way to full speed at 65 °C.
func DefaultSwitch() Switch { return Switch{OnC: 55, OffC: 50, MinOn: 2 * time.Minute} }

// MaxMinOn: the longest MinOn it accepts.
const MaxMinOn = 30 * time.Minute

// Validate: finite thresholds, off below on, on below HotC (where it is on anyway) and MinOn within 0-MaxMinOn.
func (s Switch) Validate() error {
	for _, v := range []float64{s.OnC, s.OffC} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("threshold out of range: %v", v)
		}
	}
	switch {
	case s.OffC >= s.OnC:
		return fmt.Errorf("off at %g °C is not below on at %g °C", s.OffC, s.OnC)
	case s.OnC >= HotC:
		return fmt.Errorf("on at %g °C: from %g °C it is on anyway", s.OnC, HotC)
	case s.MinOn < 0 || s.MinOn > MaxMinOn:
		return fmt.Errorf("time on %s out of range (0-%s)", s.MinOn, MaxMinOn)
	}
	return nil
}

// Start: the state it starts from (the pin as the daemon finds it), counted as set at now.
func (s *Switch) Start(on bool, now time.Time) { s.on, s.since = on, now }

// SetOff: switched off in Settings (true) or back to automatic (false). Switching it off stops it at once, without
// waiting for MinOn (it was asked for): the next Next switches it on again if it is at HotC or the reading fails.
// Back to automatic, the next Next applies the thresholds.
func (s *Switch) SetOff(off bool, now time.Time) {
	if off == s.off {
		return
	}
	s.off = off
	if off && s.on {
		s.on, s.since = false, now
	}
}

// Off: whether it is switched off in Settings.
func (s *Switch) Off() bool { return s.off }

// Next: whether the fan is on after a reading of t °C at now (ok = false: the reading failed).
func (s *Switch) Next(t float64, ok bool, now time.Time) bool {
	onC, offC := s.OnC, s.OffC
	if s.off {
		onC, offC = HotC, OffHotC
	}
	switch {
	case !ok || math.IsNaN(t) || t >= HotC || t >= onC:
		if !s.on {
			s.on, s.since = true, now
		}
	case s.on && t <= offC && now.Sub(s.since) >= s.MinOn:
		s.on, s.since = false, now
	}
	return s.on
}

// On: whether it is on. Since: when it was last switched (or started).
func (s *Switch) On() bool { return s.on }

func (s *Switch) Since() time.Time { return s.since }

// String: the thresholds as people read them.
func (s Switch) String() string {
	return fmt.Sprintf("on at %g°C, off at %g°C after %s on", s.OnC, s.OffC, s.MinOn)
}
