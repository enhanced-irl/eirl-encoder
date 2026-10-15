package main

import (
	"slices"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/camera"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// The image settings of the simulated USB cameras (SOURCES.md §7): the controls a Logi 4K Stream Edition and an MS2109
// capture card report on the board (§7.1), and the engine's camera_control and camera_reset rules over them (§7.3).

const (
	simLogi   = "/dev/v4l/by-id/usb-046d_Logi_4K_Stream_Edition_0000ABCD-video-index0"
	simMS2109 = "/dev/v4l/by-id/usb-MACROSILICON_USB_Video_0002-video-index0"
)

func simCtl(id, group, typ string, lo, hi, step, def int, opts ...int) mixer.Control {
	return mixer.Control{ID: id, Group: group, Type: typ, Min: lo, Max: hi, Step: step, Default: def, Value: def, Options: opts}
}

// simCameras: each camera's controls as the engine reports them, in the order of SOURCES.md §7.2. The Logi has its
// anti-flicker on 50 Hz (default 60 Hz, as on the board); the MS2109's defaults are not neutral (§7.1).
func simCameras() map[string][]mixer.Control {
	logi := []mixer.Control{
		simCtl("brightness", "image", "int", 0, 255, 1, 128),
		simCtl("contrast", "image", "int", 0, 255, 1, 128),
		simCtl("saturation", "image", "int", 0, 255, 1, 128),
		simCtl("sharpness", "image", "int", 0, 255, 1, 128),
		simCtl("exposure_auto", "exposure", "bool", 0, 1, 1, 1),
		simCtl("exposure", "exposure", "int", 3, 2047, 1, 250),
		simCtl("gain", "exposure", "int", 0, 255, 1, 0),
		simCtl("backlight", "exposure", "bool", 0, 1, 1, 1),
		simCtl("power_line", "exposure", "menu", 0, 2, 1, 2, 0, 1, 2),
		simCtl("wb_auto", "white_balance", "bool", 0, 1, 1, 1),
		simCtl("wb_temperature", "white_balance", "int", 2000, 7500, 10, 4000),
		simCtl("focus_auto", "framing", "bool", 0, 1, 1, 1),
		simCtl("focus", "framing", "int", 0, 255, 5, 0),
		simCtl("zoom", "framing", "int", 100, 500, 1, 100),
		simCtl("pan", "framing", "int", -36000, 36000, 3600, 0),
		simCtl("tilt", "framing", "int", -36000, 36000, 3600, 0),
	}
	logi[8].Value = 1
	ms := []mixer.Control{
		simCtl("brightness", "image", "int", -128, 127, 1, -11),
		simCtl("contrast", "image", "int", 0, 255, 1, 148),
		simCtl("saturation", "image", "int", 0, 255, 1, 180),
		simCtl("hue", "image", "int", -128, 127, 1, 0),
	}
	cams := map[string][]mixer.Control{simLogi: logi, simMS2109: ms}
	for _, list := range cams {
		syncInactive(list)
	}
	return cams
}

func findCtl(list []mixer.Control, id string) *mixer.Control {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// syncInactive: a manual control is inactive while its automatic is on
func syncInactive(list []mixer.Control) {
	for i := range list {
		if auto := findCtl(list, camera.Master(list[i].ID)); auto != nil {
			list[i].Inactive = auto.Value == 1
		}
	}
}

// cameraControl applies one value with the engine's rules: an int is brought inside its range and to its step, a bool
// takes 0 or 1 and a menu one of its options; a control whose automatic is on is refused. "" = ok, else the code.
func cameraControl(list []mixer.Control, id string, v int) string {
	c := findCtl(list, id)
	switch {
	case c == nil:
		return "unknown_control"
	case c.Inactive:
		return "inactive"
	case c.Type == "bool" && v != 0 && v != 1, c.Type == "menu" && !slices.Contains(c.Options, v):
		return "bad_request"
	case c.Type == "int":
		v = min(max(v, c.Min), c.Max)
		v = c.Min + (v-c.Min+c.Step/2)/c.Step*c.Step
		if v > c.Max {
			v -= c.Step
		}
	}
	c.Value = v
	syncInactive(list)
	return ""
}

// cameraReset: every control back to its default (the automatics with them)
func cameraReset(list []mixer.Control) {
	for i := range list {
		list[i].Value = list[i].Default
	}
	syncInactive(list)
}
