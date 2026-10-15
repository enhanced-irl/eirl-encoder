package main

import (
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The Image tab's previews (Sources → Image, SOURCES.md §7): the cameras' settings as the engine publishes them, with
// the values read on the board (§7.1, §7.3):
//
//	settings-camera (the Logi, Image) · settings-camera-exposure (its Exposure, automatic off) · settings-camera-framing
//	(its Focus & framing) · settings-camera-reset («Factory settings» waiting for its second tap) · settings-camera-two
//	(the Logi and the MS2109 capture card: the first one, the MS2109) · settings-camera-empty (no camera with settings)

// ctl is one setting as the state carries it.
func ctl(id, group, typ string, lo, hi, step, def, v int, extra ...any) map[string]any {
	c := m("id", id, "group", group, "type", typ, "min", lo, "max", hi, "step", step, "default", def, "value", v)
	for k, x := range m(extra...) {
		c[k] = x
	}
	return c
}

// logiControls are the Logi 4K's settings: everything automatic, as it comes (manualExposure: its automatic off and
// the exposure set by hand).
func logiControls(manualExposure bool) []any {
	expAuto, exp := 1, ctl("exposure", "exposure", "int", 3, 2047, 1, 250, 250, "inactive", true)
	gain := ctl("gain", "exposure", "int", 0, 255, 1, 0, 0)
	if manualExposure {
		expAuto, exp, gain = 0, ctl("exposure", "exposure", "int", 3, 2047, 1, 250, 78), ctl("gain", "exposure",
			"int", 0, 255, 1, 0, 64)
	}
	return []any{
		ctl("brightness", "image", "int", 0, 255, 1, 128, 140),
		ctl("contrast", "image", "int", 0, 255, 1, 128, 128),
		ctl("saturation", "image", "int", 0, 255, 1, 128, 150),
		ctl("sharpness", "image", "int", 0, 255, 1, 128, 128),
		ctl("exposure_auto", "exposure", "bool", 0, 1, 1, 1, expAuto),
		exp,
		gain,
		ctl("backlight", "exposure", "bool", 0, 1, 1, 1, 1),
		ctl("power_line", "exposure", "menu", 0, 2, 1, 2, 1, "options", []int{0, 1, 2}),
		ctl("wb_auto", "white_balance", "bool", 0, 1, 1, 1, 1),
		ctl("wb_temperature", "white_balance", "int", 2000, 7500, 10, 4000, 3320, "inactive", true),
		ctl("focus_auto", "framing", "bool", 0, 1, 1, 1, 1),
		ctl("focus", "framing", "int", 0, 255, 5, 0, 10, "inactive", true),
		ctl("zoom", "framing", "int", 100, 500, 1, 100, 140),
		ctl("pan", "framing", "int", -36000, 36000, 3600, 0, -7200),
		ctl("tilt", "framing", "int", -36000, 36000, 3600, 0, 0),
	}
}

// ms2109Controls are the MS2109 capture card's: four, with defaults that are not neutral.
func ms2109Controls() []any {
	return []any{
		ctl("brightness", "image", "int", -128, 127, 1, -11, -11),
		ctl("contrast", "image", "int", 0, 255, 1, 148, 148),
		ctl("saturation", "image", "int", 0, 255, 1, 180, 180),
		ctl("hue", "image", "int", -128, 127, 1, 0, 0),
	}
}

// cameraFake gives the fixture's sources their settings for a settings-camera… variant: its Logi (slot 3) and, with
// two, fakeUSB1 (slot 2) as the MS2109.
func cameraFake(variant string, sources []any) {
	if !strings.HasPrefix(variant, "settings-camera") || variant == "settings-camera-empty" {
		return
	}
	for _, x := range sources {
		src := x.(map[string]any)
		switch src["id"] {
		case fakeLogi:
			src["controls"] = logiControls(variant == "settings-camera-exposure")
		case fakeUSB1:
			if variant == "settings-camera-two" {
				src["controls"] = ms2109Controls()
			}
		}
	}
}

// cameraPreview sets what a settings-camera… variant shows on the screen itself (a group, a confirmation).
func cameraPreview(p ui.Preview, variant string) {
	switch variant {
	case "settings-camera-exposure":
		p.CameraShow(fakeLogi, "exposure")
	case "settings-camera-framing":
		p.CameraShow(fakeLogi, "framing")
	case "settings-camera-reset":
		p.Confirming("camera_reset")
	}
}
