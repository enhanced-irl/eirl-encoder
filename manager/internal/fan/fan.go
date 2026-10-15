// Package fan: the encoder's own fan control (FAN.md), the first step of IMAGE.md's D-I7: one temperature
// curve, run by the root daemon eirl-fan instead of the kernel's control (which leaves the fan stopped until
// about 51 °C) or the test board's old automagic-fan. Thermal profiles chosen in the interface and the
// "camera" mode (more fan off the air, quieter on it) come later, on top of this.
package fan

import (
	"fmt"
	"math"
	"strings"
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
