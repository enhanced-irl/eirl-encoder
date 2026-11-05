package fan

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	bad := []Curve{
		{},
		{{40, 300}},
		{{40, -1}},
		{{math.NaN(), 10}},
		{{40, 100}, {40, 120}},
		{{40, 100}, {50, 90}},
		{{50, 100}, {40, 120}},
	}
	for _, c := range bad {
		if c.Validate() == nil {
			t.Errorf("%v: accepted", c)
		}
	}
}

func TestAt(t *testing.T) {
	c := Curve{{40, 80}, {50, 180}, {60, 255}}
	cases := []struct {
		t    float64
		want int
	}{{-10, 0}, {39.9, 0}, {40, 80}, {45, 130}, {50, 180}, {55, 218}, {60, 255}, {99, 255}}
	for _, k := range cases {
		if got := c.At(k.t); got != k.want {
			t.Errorf("At(%g) = %d, want %d", k.t, got, k.want)
		}
	}
	if (Curve{}).At(50) != 0 {
		t.Error("an empty curve should stop the fan")
	}
}

func TestControllerHysteresis(t *testing.T) {
	c := &Controller{Curve: Curve{{40, 80}, {50, 180}, {60, 255}}, Hyst: 3}
	steps := []struct {
		t    float64
		want int
	}{
		{35, 0},
		{40, 80},    // starts at the first point
		{45, 130},   // up at once
		{44.5, 130}, // a small drop: unchanged
		{42, 130},   // 42+3 = 45: still the speed it was set for
		{41, 120},   // 41+3 = 44: down, along the curve shifted by Hyst
		{38, 90},    // 38+3 = 41
		{37.5, 85},
		{36.9, 0}, // below the first point minus Hyst: stops
		{39, 0},   // and does not start again until the first point
		{40, 80},
	}
	for i, s := range steps {
		if got := c.Next(s.t, true); got != s.want {
			t.Fatalf("step %d: Next(%g) = %d, want %d", i, s.t, got, s.want)
		}
	}
}

func TestControllerSafety(t *testing.T) {
	c := &Controller{Curve: Default, Hyst: 3}
	if c.Next(30, true) != 0 {
		t.Fatal("cool: the fan should be stopped")
	}
	if c.Next(0, false) != MaxPWM {
		t.Error("a failed reading must run the fan at full speed")
	}
	c = &Controller{Curve: Curve{{40, 80}, {90, 100}}, Hyst: 3}
	if c.Next(HotC, true) != MaxPWM {
		t.Error("at HotC the fan must be at full speed whatever the curve says")
	}
	if c.Next(math.NaN(), true) != MaxPWM {
		t.Error("NaN must run the fan at full speed")
	}
}

func TestDefaultSwitchIsValid(t *testing.T) {
	if err := DefaultSwitch().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSwitchValidate(t *testing.T) {
	bad := []Switch{
		{OnC: 50, OffC: 50},
		{OnC: 50, OffC: 55},
		{OnC: math.NaN(), OffC: 50},
		{OnC: 55, OffC: math.Inf(-1)},
		{OnC: HotC, OffC: 50},
		{OnC: 55, OffC: 50, MinOn: -time.Second},
		{OnC: 55, OffC: 50, MinOn: MaxMinOn + time.Second},
	}
	for _, s := range bad {
		if s.Validate() == nil {
			t.Errorf("%v: accepted", s)
		}
	}
	if err := (Switch{OnC: 40, OffC: 35}).Validate(); err != nil {
		t.Errorf("no time on: %v", err)
	}
}

func TestSwitchHysteresis(t *testing.T) {
	s := DefaultSwitch()
	t0 := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	s.Start(false, t0)
	steps := []struct {
		at   time.Duration
		t    float64
		want bool
	}{
		{0, 40, false},
		{2 * time.Second, 54.5, false},
		{4 * time.Second, 55, true},                  // on at OnC
		{time.Minute, 52, true},                      // between the thresholds: stays on
		{time.Minute + 30*time.Second, 50, true},     // at OffC, but not on for MinOn yet
		{2*time.Minute + 2*time.Second, 50.5, true},  // on for MinOn, but above OffC
		{2*time.Minute + 4*time.Second, 50, false},   // at OffC and on for MinOn: off
		{3 * time.Minute, 54.5, false},               // between the thresholds: stays off
		{3*time.Minute + 2*time.Second, 55.5, true},  // on again at once (MinOn never delays switching on)
		{3*time.Minute + 4*time.Second, 30, true},    // just on: MinOn again
		{5*time.Minute + 4*time.Second, 49.5, false}, // MinOn after it came on
	}
	for i, k := range steps {
		if got := s.Next(k.t, true, t0.Add(k.at)); got != k.want {
			t.Fatalf("step %d: Next(%g, +%s) = %v, want %v", i, k.t, k.at, got, k.want)
		}
	}
	if !s.Since().Equal(t0.Add(5*time.Minute + 4*time.Second)) {
		t.Fatalf("since %s: want the last switch", s.Since())
	}
}

func TestSwitchSafety(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	s := DefaultSwitch()
	s.Start(false, t0)
	if !s.Next(0, false, t0) {
		t.Fatal("a failed reading must switch it on")
	}
	if !s.Next(30, true, t0.Add(time.Minute)) {
		t.Fatal("cool again, but not on for MinOn yet")
	}
	if s.Next(30, true, t0.Add(2*time.Minute)) {
		t.Fatal("cool and on for MinOn: off")
	}
	if !s.Next(math.NaN(), true, t0.Add(2*time.Minute+time.Second)) {
		t.Fatal("NaN must switch it on")
	}
	hot := Switch{OnC: 90, OffC: 40} // an unvalidated one: HotC still wins
	if !hot.Next(HotC, true, t0) {
		t.Fatal("at HotC it must be on whatever its thresholds say")
	}
}

func TestSwitchStart(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	s := DefaultSwitch()
	s.Start(true, t0) // found on (a restart while it was on): MinOn counts from the start, so a restart loop does not click it
	if !s.Next(30, true, t0.Add(time.Minute)) {
		t.Fatal("found on: switched off before MinOn")
	}
	if s.Next(30, true, t0.Add(2*time.Minute)) {
		t.Fatal("found on: still on after MinOn")
	}
	s.Start(false, t0)
	if s.Next(52, true, t0) {
		t.Fatal("found off between the thresholds: switched on")
	}
}

func TestSwitchOff(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	s := DefaultSwitch()
	s.Start(false, t0)
	if !s.Next(60, true, t0) {
		t.Fatal("automatic: on at 60 °C")
	}
	s.SetOff(true, t0.Add(10*time.Second))
	if s.On() || !s.Off() {
		t.Fatal("switched off in Settings: off at once, before MinOn")
	}
	steps := []struct {
		at   time.Duration
		temp float64
		ok   bool
		want bool
		why  string
	}{
		{20 * time.Second, 70, true, false, "off: 70 °C is below HotC"},
		{30 * time.Second, 79.5, true, false, "off: 79.5 °C is below HotC"},
		{40 * time.Second, 80, true, true, "off, but HotC: on"},
		{50 * time.Second, 76, true, true, "on for HotC: not off above OffHotC"},
		{60 * time.Second, 75, true, true, "on for HotC: not off before MinOn"},
		{3 * time.Minute, 75, true, false, "on for HotC: off at OffHotC after MinOn"},
		{4 * time.Minute, 0, false, true, "off, but a failed reading: on"},
		{7 * time.Minute, 60, true, false, "the reading is back: off after MinOn"},
	}
	for _, c := range steps {
		if got := s.Next(c.temp, c.ok, t0.Add(c.at)); got != c.want {
			t.Fatalf("%s: got %v", c.why, got)
		}
	}
	s.SetOff(false, t0.Add(8*time.Minute))
	if s.On() || s.Off() {
		t.Fatal("back to automatic: nothing changes until the next reading")
	}
	if !s.Next(60, true, t0.Add(8*time.Minute)) {
		t.Fatal("back to automatic: on at 60 °C")
	}
	s.SetOff(true, t0.Add(9*time.Minute))
	s.SetOff(true, t0.Add(10*time.Minute)) // again: no change
	if s.Since() != t0.Add(9*time.Minute) {
		t.Fatal("switching off twice must not move Since")
	}
}

func TestParseControl(t *testing.T) {
	for _, c := range []struct {
		in, mode string
		ok       bool
	}{
		{`{"fan2":"auto"}`, ModeAuto, true},
		{`{"fan2":"off","bootId":"b1"}`, ModeOff, true},
		{`{"fan2":"off","bootId":"b0"}`, ModeAuto, true}, // asked in another boot
		{`{"fan2":"off"}`, "", false},
		{`{"fan2":"loud"}`, "", false},
		{`not json`, "", false},
	} {
		got, err := ParseControl([]byte(c.in))
		if (err == nil) != c.ok {
			t.Fatalf("%s: err %v", c.in, err)
		}
		if c.ok && got.Mode("b1") != c.mode {
			t.Fatalf("%s: mode %s, want %s", c.in, got.Mode("b1"), c.mode)
		}
	}
}

func TestStateFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadState(filepath.Join(dir, "none.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing state: %v", err)
	}
	p := filepath.Join(dir, "state.json")
	os.WriteFile(p, []byte(`{"pwm":90,"fan2":{"on":true,"mode":"off","hotC":80}}`), 0o644)
	st, err := ReadState(p)
	if err != nil || st.PWM != 90 || st.Fan2 == nil || !st.Fan2.On || st.Fan2.Mode != ModeOff {
		t.Fatalf("state %+v %v", st, err)
	}
	os.WriteFile(p, make([]byte, MaxStateSize+1), 0o644)
	if _, err := ReadState(p); err == nil {
		t.Fatal("an oversized state file must be refused")
	}
	b := filepath.Join(dir, "boot_id")
	os.WriteFile(b, []byte("c7028e6b-1633-4919-8912-5a84e4ddd2a3\n"), 0o444)
	if id, err := ReadBootID(b); err != nil || id != "c7028e6b-1633-4919-8912-5a84e4ddd2a3" {
		t.Fatalf("boot id %q %v", id, err)
	}
}
