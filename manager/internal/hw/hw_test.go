package hw

import "testing"

func TestAudioFromStatus(t *testing.T) {
	for v, want := range map[byte]string{0x61: "playing", 0xE1: "playing", 0x41: "pll_unlocked", 0x80: "no_samples", 0x40: "no_samples"} {
		if a := AudioFromStatus(v); a.State != want {
			t.Errorf("0x%02x → %s, expected %s", v, a.State, want)
		}
	}
	if a := AudioFromStatus(0x05); a.AuStatus0 != "0x05" {
		t.Errorf("format %q", a.AuStatus0)
	}
}

func TestSupported(t *testing.T) {
	for _, c := range []struct {
		name         string
		w, h, htotal int
		pclk         uint64
		il           bool
		ok           bool
	}{
		{"1080p30", 1920, 1080, 2200, 74250000, false, true},
		{"1080p50", 1920, 1080, 2640, 148500000, false, true},
		{"720p60", 1280, 720, 1650, 74250000, false, true},
		{"480p60", 720, 480, 858, 27000000, false, true},
		{"1080p60", 1920, 1080, 2200, 148500000, false, false},
		{"1080p60 CVT-RB", 1920, 1080, 2080, 138500000, false, false},
		{"1080i60", 1920, 1080, 2200, 74250000, true, false},
		{"no timings", 1920, 1080, 0, 0, false, false},
	} {
		s := Signal{Width: c.w, Height: c.h, HTotal: c.htotal, PixelClockHz: c.pclk, Interlaced: c.il}
		s.fill()
		if s.Supported != c.ok {
			t.Errorf("%s: supported=%v (CSI load %.3f), expected %v", c.name, s.Supported, s.CSILoad, c.ok)
		}
	}
}
