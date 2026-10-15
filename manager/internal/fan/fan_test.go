package fan

import (
	"math"
	"testing"
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
