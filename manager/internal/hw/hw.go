// Package hw reads the hardware state without spawning processes: HDMI timings by ioctl on the
// TC358743 subdev, AU_STATUS0 over I2C and sysfs telemetry (temperature, power, CPU, GPU).
// Queries only: nothing here writes to the chip.
package hw

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrNoSignal = errors.New("no HDMI signal")

type Signal struct {
	Present      bool    `json:"present"`
	Width        int     `json:"width,omitempty"`
	Height       int     `json:"height,omitempty"`
	FPS          float64 `json:"fps,omitempty"`
	Interlaced   bool    `json:"interlaced,omitempty"`
	PixelClockHz uint64  `json:"pixelClockHz,omitempty"`
	HTotal       int     `json:"-"` // total line width (active + blanking)
	// CSILoad: fraction of each HDMI line the CSI takes to send it (1080p50 ≈ 0.89;
	// 1080p60 ≈ 1.07). Supported: fits with margin (≤ 0.95) and is not interlaced (the Nano's VI
	// cannot capture it). Same rule as the engine.
	CSILoad   float64 `json:"csiLoad,omitempty"`
	Supported bool    `json:"supported"`
}

// csiBps: 2 lanes × 972 Mbps (the DT's link-frequencies). maxCSILoad leaves 5 % of each line for
// the CSI's LP↔HS transitions.
const (
	csiBps     = 2 * 972e6
	maxCSILoad = 0.95
)

func (s *Signal) fill() {
	if s.HTotal > 0 && s.PixelClockHz > 0 {
		s.CSILoad = float64(s.Width) * 16 * float64(s.PixelClockHz) / (float64(s.HTotal) * csiBps)
	}
	s.Supported = !s.Interlaced && s.CSILoad > 0 && s.CSILoad <= maxCSILoad
}

type Audio struct {
	State     string `json:"state"` // playing | pll_unlocked | no_samples | unknown
	AuStatus0 string `json:"auStatus0,omitempty"`
}

// AudioFromStatus decodes AU_STATUS0 (0x8523): bit 0 samples arriving, bit 5 PLL frequency-locked.
// 0x61 = playing; 0x41 = samples without lock (the PLL failure, see DEVELOPMENT.md).
func AudioFromStatus(v byte) Audio {
	a := Audio{AuStatus0: "0x" + strconv.FormatUint(uint64(v)|0x100, 16)[1:]}
	switch {
	case v&0x21 == 0x21:
		a.State = "playing"
	case v&0x01 != 0:
		a.State = "pll_unlocked"
	default:
		a.State = "no_samples"
	}
	return a
}

// FindSubdev finds the /dev/v4l-subdevN node whose name contains `name` (tc358743).
func FindSubdev(name string) (string, error) {
	matches, _ := filepath.Glob("/sys/class/video4linux/v4l-subdev*/name")
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err == nil && strings.Contains(string(b), name) {
			return "/dev/" + filepath.Base(filepath.Dir(m)), nil
		}
	}
	return "", errors.New("subdev not found: " + name)
}

func readInt(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
}
