package ui

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The Image tab (camera.go): a USB camera's image settings, straight to the engine.

// logiCtl are the Logi 4K's settings as the engine publishes them (SOURCES.md §7.1, §7.3): its automatics on.
func logiCtl() []CameraControl {
	return []CameraControl{
		{ID: "brightness", Group: "image", Type: "int", Min: 0, Max: 255, Step: 1, Default: 128, Value: 140},
		{ID: "contrast", Group: "image", Type: "int", Min: 0, Max: 255, Step: 1, Default: 128, Value: 128},
		{ID: "saturation", Group: "image", Type: "int", Min: 0, Max: 255, Step: 1, Default: 128, Value: 128},
		{ID: "sharpness", Group: "image", Type: "int", Min: 0, Max: 255, Step: 1, Default: 128, Value: 128},
		{ID: "exposure_auto", Group: "exposure", Type: "bool", Min: 0, Max: 1, Step: 1, Default: 1, Value: 1},
		{ID: "exposure", Group: "exposure", Type: "int", Min: 3, Max: 2047, Step: 1, Default: 250, Value: 250, Inactive: true},
		{ID: "gain", Group: "exposure", Type: "int", Min: 0, Max: 255, Step: 1, Default: 0, Value: 0},
		{ID: "backlight", Group: "exposure", Type: "bool", Min: 0, Max: 1, Step: 1, Default: 1, Value: 1},
		{ID: "power_line", Group: "exposure", Type: "menu", Min: 0, Max: 2, Step: 1, Default: 2, Value: 1, Options: []int{0, 1, 2}},
		{ID: "wb_auto", Group: "white_balance", Type: "bool", Min: 0, Max: 1, Step: 1, Default: 1, Value: 1},
		{ID: "wb_temperature", Group: "white_balance", Type: "int", Min: 2000, Max: 7500, Step: 10, Default: 4000, Value: 3320, Inactive: true},
		{ID: "focus_auto", Group: "framing", Type: "bool", Min: 0, Max: 1, Step: 1, Default: 1, Value: 1},
		{ID: "focus", Group: "framing", Type: "int", Min: 0, Max: 255, Step: 5, Default: 0, Value: 10, Inactive: true},
		{ID: "zoom", Group: "framing", Type: "int", Min: 100, Max: 500, Step: 1, Default: 100, Value: 100},
		{ID: "pan", Group: "framing", Type: "int", Min: -36000, Max: 36000, Step: 3600, Default: 0, Value: 0},
		{ID: "tilt", Group: "framing", Type: "int", Min: -36000, Max: 36000, Step: 3600, Default: 0, Value: 0},
	}
}

// ms2109Ctl are the MS2109 capture card's: four image settings, defaults that are not neutral.
func ms2109Ctl() []CameraControl {
	return []CameraControl{
		{ID: "brightness", Group: "image", Type: "int", Min: -128, Max: 127, Step: 1, Default: -11, Value: -11},
		{ID: "contrast", Group: "image", Type: "int", Min: 0, Max: 255, Step: 1, Default: 148, Value: 148},
		{ID: "saturation", Group: "image", Type: "int", Min: 0, Max: 255, Step: 1, Default: 180, Value: 180},
		{ID: "hue", Group: "image", Type: "int", Min: -128, Max: 127, Step: 1, Default: 0, Value: 0},
	}
}

// setCameras gives the harness's sources their settings: usb1 (slot 2) and usb2 (slot 3), nil = none; change, if
// not nil, edits usb2's (the Logi's) after.
func setCameras(h *harness, usb1, usb2 []CameraControl, change func(c []CameraControl)) {
	st := *h.u.st
	st.Sources = slices.Clone(st.Sources)
	for i := range st.Sources {
		switch st.Sources[i].ID {
		case "usb1":
			st.Sources[i].Controls = slices.Clone(usb1)
		case "usb2":
			st.Sources[i].Controls = slices.Clone(usb2)
			if change != nil {
				change(st.Sources[i].Controls)
			}
		}
	}
	h.u.SetStatus(&st)
}

// ctlAt is the index of a setting in logiCtl.
func ctlAt(id string) int {
	return slices.IndexFunc(logiCtl(), func(c CameraControl) bool { return c.ID == id })
}

func camCmd(src, ctl string, v int) map[string]any {
	return map[string]any{"cmd": "camera_control", "source": src, "control": ctl, "value": v}
}

// camHarness is the Image tab open with the Logi as usb2 (and usb1 with usb1's settings, if any).
func camHarness(t *testing.T, usb1 []CameraControl) *harness {
	h, _ := pagesHarness(t, "camera")
	setCameras(h, usb1, logiCtl(), nil)
	h.cmds = nil
	return h
}

func keysOf(h *harness) []string { return h.u.cameraKeys(nil) }

// The page fits: every button inside the page, at least 48 px, nothing of one view overlapping, at least five rows
// (the Logi's exposure) and four above the pager.
func TestCameraLayout(t *testing.T) {
	for _, sz := range [][2]int{{1024, 600}, {1280, 720}, {1920, 1080}} {
		l := NewLayout(sz[0], sz[1], 4)
		c, b, a := l.SettingsPage, l.Buttons, &l.Cam
		inside := func(r, in Rect) bool {
			return r.X >= in.X && r.Y >= in.Y && r.X+r.W <= in.X+in.W && r.Y+r.H <= in.Y+in.H
		}
		noOverlap := func(what string, rs []Rect) {
			for i, x := range rs {
				for _, y := range rs[i+1:] {
					if overlaps(x, y) {
						t.Errorf("%v %s: %v overlaps %v", sz, what, x, y)
					}
				}
			}
		}
		for k, r := range b {
			if !strings.HasPrefix(k, "s_cam_") {
				continue
			}
			if !inside(r, c) {
				t.Errorf("%v: %s %v outside the page %v", sz, k, r, c)
			}
			if r.W < 48 || r.H < 48 {
				t.Errorf("%v: %s %v under 48 px", sz, k, r)
			}
		}
		if len(a.Rows) < 5 || a.Paged < 4 || a.Paged >= len(a.Rows) {
			t.Errorf("%v: %d rows, %d paged", sz, len(a.Rows), a.Paged)
		}
		if last := a.Rows[a.Paged-1]; last.Y+last.H > b["s_prev"].Y {
			t.Errorf("%v: row %v over the pager", sz, last)
		}
		if last := a.Rows[len(a.Rows)-1]; last.Y+last.H > a.Hint.Y || !inside(a.Hint, c) {
			t.Errorf("%v: rows over the hint %v", sz, a.Hint)
		}
		// The top: each number of cameras' chips and «Factory settings»; the groups; the rows
		for n := 2; n <= l.N; n++ {
			rs := []Rect{b["s_cam_reset"]}
			for k := range n {
				rs = append(rs, b[camChip(n, k)])
			}
			noOverlap("cameras", rs)
		}
		rs := []Rect{b["s_cam_reset"], b[camChip(2, 0)], b[camChip(2, 1)]}
		for k := range CameraGroups {
			rs = append(rs, b["s_cam_g_"+itoa(k)])
		}
		rs = append(rs, a.Rows...)
		noOverlap("page", rs)
		for i, r := range a.Rows {
			in := []Rect{b["s_cam_m_"+itoa(i)], b["s_cam_p_"+itoa(i)]}
			for j := range camSegs {
				if s := b[camSeg(i, j)]; !inside(s, r) {
					t.Errorf("%v: segment %d of row %d outside it", sz, j, i)
				} else {
					noOverlap("segments", append([]Rect{}, s, b[camSeg(i, (j+1)%camSegs)]))
				}
			}
			for _, x := range in {
				if !inside(x, r) {
					t.Errorf("%v: %v outside row %d %v", sz, x, i, r)
				}
			}
			noOverlap("stepper", in)
		}
	}
}

// A tap moves ~2 % of the range and a long press ~10 %, a whole number of steps (at least one), on the grid and
// inside the range.
func TestCameraStep(t *testing.T) {
	ctl := func(id string) *CameraControl {
		c := logiCtl()[ctlAt(id)]
		return &c
	}
	ms := ms2109Ctl()[0]
	for _, tc := range []struct {
		c      *CameraControl
		v, dir int
		long   bool
		want   int
		why    string
	}{
		{c: ctl("brightness"), v: 140, dir: 1, want: 145, why: "tap"},
		{c: ctl("brightness"), v: 140, dir: 1, long: true, want: 165, why: "long"},
		{c: ctl("brightness"), v: 253, dir: 1, want: 255, why: "to the top"},
		{c: ctl("brightness"), v: 2, dir: -1, long: true, want: 0, why: "to the bottom"},
		{c: ctl("exposure"), v: 78, dir: 1, want: 118, why: "exposure: 4.0 ms"},
		{c: ctl("exposure"), v: 20, dir: -1, want: 3, why: "exposure's minimum"},
		{c: ctl("exposure"), v: 2040, dir: 1, long: true, want: 2047, why: "exposure's maximum"},
		{c: ctl("wb_temperature"), v: 3320, dir: 1, want: 3430, why: "110 K"},
		{c: ctl("wb_temperature"), v: 3320, dir: -1, long: true, want: 2770, why: "550 K"},
		{c: ctl("focus"), v: 10, dir: 1, want: 15, why: "one step of 5"},
		{c: ctl("focus"), v: 10, dir: 1, long: true, want: 35, why: "five steps of 5"},
		{c: ctl("focus"), v: 252, dir: 1, want: 255, why: "focus's top on its grid"},
		{c: ctl("pan"), v: 0, dir: 1, want: 3600, why: "one degree: the step"},
		{c: ctl("pan"), v: 0, dir: -1, long: true, want: -7200, why: "two degrees"},
		{c: ctl("pan"), v: 36000, dir: 1, want: 36000, why: "pan's end"},
		{c: ctl("zoom"), v: 100, dir: 1, want: 108, why: "zoom"},
		{c: &ms, v: -11, dir: -1, want: -16, why: "a negative range"},
		{c: &ms, v: -126, dir: -1, long: true, want: -128, why: "its bottom"},
	} {
		if got := camStep(tc.c, tc.v, tc.dir, tc.long); got != tc.want {
			t.Errorf("%s: %s %d %+d long=%v: %d, want %d", tc.why, tc.c.ID, tc.v, tc.dir, tc.long, got, tc.want)
		}
	}
	odd := CameraControl{ID: "x", Type: "int", Min: 3, Max: 100, Step: 4}
	if got := camStep(&odd, 3, 1, false); (got-odd.Min)%odd.Step != 0 || got != 7 {
		t.Errorf("off the step's grid: %d", got)
	}
	if got := camStep(&odd, 98, 1, false); got != 99 { // 3 + 24×4: the top on its grid
		t.Errorf("odd top: %d", got)
	}
}

// One camera: no chips to choose it; the groups it has; − + of what can change; taps and bursts of taps.
func TestCameraTouches(t *testing.T) {
	h := camHarness(t, nil)
	keys := keysOf(h)
	for _, k := range []string{"s_cam_reset", "s_cam_g_0", "s_cam_g_3", "s_cam_m_0", "s_cam_p_3"} {
		if !slices.Contains(keys, k) {
			t.Fatalf("no %s in %v", k, keys)
		}
	}
	if slices.ContainsFunc(keys, func(k string) bool { return strings.HasPrefix(k, "s_cam_c") }) ||
		slices.Contains(keys, "s_cam_m_4") || slices.Contains(keys, "s_next") {
		t.Fatalf("keys %v", keys)
	}
	// A tap: ~2 %; again before the engine publishes it, from what was sent (not the state's 140)
	h.press("s_cam_p_0", 0.1)
	lastCmd(t, h, camCmd("usb2", "brightness", 145))
	h.press("s_cam_p_0", 0.1)
	lastCmd(t, h, camCmd("usb2", "brightness", 150))
	h.press("s_cam_p_0", TapLong.Seconds()+0.1) // held: ~10 %
	lastCmd(t, h, camCmd("usb2", "brightness", 175))
	// The first one published: still the last one sent; the last one: the state's again
	setCameras(h, nil, logiCtl(), func(c []CameraControl) { c[0].Value = 145 })
	if v := h.u.camValue("usb2", &h.u.st.Sources[2].Controls[0]); v != 175 {
		t.Fatalf("shown %d", v)
	}
	setCameras(h, nil, logiCtl(), func(c []CameraControl) { c[0].Value = 175 })
	h.press("s_cam_m_1", 0.1)
	lastCmd(t, h, camCmd("usb2", "contrast", 123))
	// Another value from elsewhere (the web) wins over what was sent
	setCameras(h, nil, logiCtl(), func(c []CameraControl) { c[0].Value = 175; c[1].Value = 200 })
	h.press("s_cam_m_1", 0.1)
	lastCmd(t, h, camCmd("usb2", "contrast", 195))
	// What was sent and never published is too old: the state's
	setCameras(h, nil, logiCtl(), func(c []CameraControl) { c[0].Value = 175; c[1].Value = 200 })
	h.advance(camSent + time.Second)
	h.press("s_cam_p_1", 0.1)
	lastCmd(t, h, camCmd("usb2", "contrast", 205))
	// At its end: nothing
	setCameras(h, nil, logiCtl(), func(c []CameraControl) { c[3].Value = 255 })
	n := len(h.cmds)
	h.press("s_cam_p_3", 0.1)
	h.press("s_cam_p_3", TapLong.Seconds()+0.1)
	if len(h.cmds) != n {
		t.Fatalf("over the top: %v", h.cmds[n:])
	}
	for _, c := range h.cmds {
		if v := c["value"].(int); v < 0 || v > 255 {
			t.Errorf("out of the range: %v", c)
		}
	}
}

// Exposure: the switch, the inactive exposure (no keys, no command), the menu's choices; then manual.
func TestCameraExposure(t *testing.T) {
	h := camHarness(t, nil)
	h.press("s_cam_g_1", 0.1)
	if h.u.cam.group != "exposure" || len(h.cmds) != 0 {
		t.Fatalf("group %q, cmds %v", h.u.cam.group, h.cmds)
	}
	keys := keysOf(h)
	want := []string{"s_cam_row_0", "s_cam_m_2", "s_cam_p_2", "s_cam_row_3", camSeg(4, 1), camSeg(4, 2), camSeg(4, 3)}
	for _, k := range want {
		if !slices.Contains(keys, k) {
			t.Fatalf("no %s in %v", k, keys)
		}
	}
	if slices.Contains(keys, "s_cam_m_1") || slices.Contains(keys, "s_cam_p_1") || slices.Contains(keys, camSeg(4, 0)) {
		t.Fatalf("keys %v", keys)
	}
	// The inactive exposure sends nothing, even called directly
	h.press("s_cam_p_1", 0.1)
	h.u.pageTouch("s_cam_m_1", "", false)
	if len(h.cmds) != 0 {
		t.Fatalf("inactive: %v", h.cmds)
	}
	// The switch: its automatic off; the menu: 60 Hz, and the choice it has sends nothing
	h.press("s_cam_row_0", 0.1)
	lastCmd(t, h, camCmd("usb2", "exposure_auto", 0))
	h.press(camSeg(4, 3), 0.1)
	lastCmd(t, h, camCmd("usb2", "power_line", 2))
	setCameras(h, nil, logiCtl(), func(c []CameraControl) { c[ctlAt("power_line")].Value = 2 })
	n := len(h.cmds)
	h.press(camSeg(4, 3), 0.1) // 60 Hz, as it is now
	if len(h.cmds) != n {
		t.Fatalf("the same choice: %v", h.cmds[n:])
	}
	// Manual: the exposure answers (4.0 ms a tap)
	setCameras(h, nil, logiCtl(), func(c []CameraControl) {
		c[ctlAt("exposure_auto")].Value = 0
		c[ctlAt("exposure")].Inactive, c[ctlAt("exposure")].Value = false, 78
	})
	if !slices.Contains(keysOf(h), "s_cam_p_1") {
		t.Fatal("manual exposure without +")
	}
	h.press("s_cam_p_1", 0.1)
	lastCmd(t, h, camCmd("usb2", "exposure", 118))
	h.press("s_cam_row_3", 0.1) // the backlight switch, off
	lastCmd(t, h, camCmd("usb2", "backlight", 0))
	h.press("s_cam_row_3", 0.1) // again before the state has it: on
	lastCmd(t, h, camCmd("usb2", "backlight", 1))
}

// «Factory settings» needs a second tap (in time), and drops what was sent.
func TestCameraReset(t *testing.T) {
	h := camHarness(t, nil)
	h.press("s_cam_reset", 0.1)
	if len(h.cmds) != 0 || !h.u.confirming("camera_reset") {
		t.Fatalf("first tap: %v", h.cmds)
	}
	h.advance(ConfirmFor + time.Second)
	h.u.Tick()
	h.press("s_cam_reset", 0.1)
	if len(h.cmds) != 0 {
		t.Fatalf("a late second tap: %v", h.cmds)
	}
	h.advance(ConfirmFor + time.Second)
	h.u.Tick()
	h.press("s_cam_p_0", 0.1) // a value sent, then the two taps
	h.press("s_cam_reset", 0.1)
	if len(h.cmds) != 1 {
		t.Fatalf("first tap: %v", h.cmds)
	}
	h.press("s_cam_reset", 0.1)
	lastCmd(t, h, map[string]any{"cmd": "camera_reset", "source": "usb2"})
	if len(h.u.cam.sent) != 0 {
		t.Fatalf("sent %v", h.u.cam.sent)
	}
}

// A refusal says why on the page, and the setting goes back to the state's value.
func TestCameraRefused(t *testing.T) {
	h := camHarness(t, nil)
	h.press("s_cam_p_0", 0.1)
	for _, code := range []string{"unknown_control", "inactive", "busy", "camera_error"} {
		if Spanish.CmdErrors[code] == "" || English.CmdErrors[code] == "" || French.CmdErrors[code] == "" {
			t.Errorf("no words for %s", code)
		}
	}
	h.u.CommandDone(Command{Msg: h.cmds[0], Origin: originCamera}, false, "busy")
	txt, warn := h.u.pageNoteText()
	if !warn || !strings.Contains(txt, Spanish.CmdErrors["busy"]) {
		t.Fatalf("note %q %v", txt, warn)
	}
	if v := h.u.camValue("usb2", &h.u.st.Sources[2].Controls[0]); v != 140 {
		t.Fatalf("shown %d after the refusal", v)
	}
}

// Two cameras: chips to choose one (by slot), the group kept when the other has it; the page drops it all when it
// goes.
func TestCameraTwo(t *testing.T) {
	h := camHarness(t, ms2109Ctl())
	keys := keysOf(h)
	if !slices.Contains(keys, camChip(2, 0)) || !slices.Contains(keys, camChip(2, 1)) || slices.Contains(keys, "s_cam_g_1") {
		t.Fatalf("keys %v", keys)
	}
	h.press("s_cam_m_0", 0.1) // the first by slot: the MS2109 (usb1), −11 → −16
	lastCmd(t, h, camCmd("usb1", "brightness", -16))
	h.press(camChip(2, 1), 0.1)
	h.press("s_cam_g_3", 0.1) // the Logi's Focus & framing: pan
	h.press("s_cam_p_3", 0.1)
	lastCmd(t, h, camCmd("usb2", "pan", 3600))
	h.press(camChip(2, 0), 0.1) // the MS2109 has no framing: its Image; back to the Logi, its framing again
	if _, _, g := h.u.camNow(h.u.cameras()); g != "image" {
		t.Fatalf("group %q", g)
	}
	h.press(camChip(2, 1), 0.1)
	if cam, _, g := h.u.camNow(h.u.cameras()); cam.ID != "usb2" || g != "framing" {
		t.Fatalf("%s %q", cam.ID, g)
	}
	// Another tab: the chosen camera, group and what was sent go
	h.press("s_tab_input", 0.1)
	if !reflect.DeepEqual(h.u.cam, cameraState{}) {
		t.Fatalf("kept %+v", h.u.cam)
	}
	// The cameras go (an older engine, or unplugged): nothing answers
	setCameras(h, nil, nil, nil)
	h.press("s_tab_camera", 0.1)
	h.cmds = nil
	if keys := keysOf(h); len(keys) != 0 {
		t.Fatalf("keys %v", keys)
	}
	h.press("s_cam_p_0", 0.1)
	h.press("s_cam_reset", 0.1)
	h.press("s_cam_reset", 0.1)
	if len(h.cmds) != 0 {
		t.Fatalf("without cameras: %v", h.cmds)
	}
}

// A group with more rows than fit: pages, and the rows of the second one answer for their settings.
func TestCameraPaged(t *testing.T) {
	h := camHarness(t, nil)
	six := append(ms2109Ctl(), CameraControl{ID: "gamma", Group: "image", Type: "int", Min: 72, Max: 500, Step: 1,
		Default: 100, Value: 100}, CameraControl{ID: "sharpness", Group: "image", Type: "int", Min: 0, Max: 7, Step: 1,
		Default: 2, Value: 2})
	setCameras(h, nil, six, nil)
	per := h.lay.Cam.Paged
	keys := keysOf(h)
	if !slices.Contains(keys, "s_next") || slices.Contains(keys, "s_prev") || slices.Contains(keys, "s_cam_m_"+itoa(per)) {
		t.Fatalf("keys %v", keys)
	}
	h.press("s_next", 0.1)
	if keys := keysOf(h); !slices.Contains(keys, "s_prev") || slices.Contains(keys, "s_next") {
		t.Fatalf("page 2: %v", keys)
	}
	h.press("s_cam_p_"+itoa(5-per), 0.1)
	lastCmd(t, h, camCmd("usb2", "sharpness", 3))
	h.press("s_cam_g_0", 0.1) // the group again: its first page
	if h.u.cam.page != 0 {
		t.Fatalf("page %d", h.u.cam.page)
	}
}

// The values in words, with their units, in the language's way.
func TestCameraText(t *testing.T) {
	h := newHarness(t)
	c := logiCtl()
	for _, tc := range []struct {
		lang, id string
		v        int
		want     string
	}{
		{"es", "exposure", 78, "7,8 ms"}, {"en", "exposure", 250, "25.0 ms"}, {"es", "wb_temperature", 3320, "3320 K"},
		{"es", "pan", -7200, "−2°"}, {"fr", "tilt", 1800, "0,5°"}, {"es", "power_line", 1, "50 Hz"},
		{"fr", "power_line", 0, "Désactivé"}, {"en", "backlight", 1, "on"}, {"es", "brightness", 140, "140"},
	} {
		h.u.SetLanguage(tc.lang)
		if got := h.u.camText(&c[ctlAt(tc.id)], tc.v); got != tc.want {
			t.Errorf("%s %s %d: %q, want %q", tc.lang, tc.id, tc.v, got, tc.want)
		}
	}
	ms := ms2109Ctl()[0]
	if got := h.u.camText(&ms, -11); got != "−11" {
		t.Errorf("%q", got)
	}
	if h.u.camLabel("nope") != "nope" || h.u.camGroupLabel("nope") != "nope" {
		t.Error("an unknown id is shown by its id")
	}
	for _, id := range []string{"brightness", "contrast", "saturation", "hue", "gamma", "sharpness", "exposure_auto",
		"exposure", "gain", "backlight", "power_line", "wb_auto", "wb_temperature", "focus_auto", "focus", "zoom", "pan",
		"tilt"} {
		if Spanish.Camera.Control[id] == "" {
			t.Errorf("no name for %s", id)
		}
	}
	for _, g := range CameraGroups {
		if Spanish.Camera.Group[g] == "" {
			t.Errorf("no name for group %s", g)
		}
	}
}

// Every view of the tab draws in every language without cutting a text of the table.
func TestRenderCamera(t *testing.T) {
	cams := func(usb1 []CameraControl, group string, change func(c []CameraControl)) func(h *harness) {
		return func(h *harness) {
			pageApp(h, "camera", nil)
			setCameras(h, usb1, logiCtl(), change)
			h.u.cam.src, h.u.cam.group = "usb2", group
		}
	}
	manual := func(c []CameraControl) {
		c[ctlAt("exposure_auto")].Value = 0
		c[ctlAt("exposure")].Inactive, c[ctlAt("exposure")].Value = false, 78
	}
	checkRenders(t, []renderView{
		{"camera", cams(nil, "", nil)},
		{"camera-exposure", cams(nil, "exposure", nil)},
		{"camera-exposure-manual", cams(nil, "exposure", manual)},
		{"camera-wb", cams(nil, "white_balance", nil)},
		{"camera-framing", cams(nil, "framing", nil)},
		{"camera-two", cams(ms2109Ctl(), "image", nil)},
		{"camera-reset", func(h *harness) {
			cams(nil, "", nil)(h)
			h.u.confirmTap("camera_reset")
		}},
		{"camera-refused", func(h *harness) {
			cams(nil, "", nil)(h)
			h.u.CommandDone(Command{Msg: camCmd("usb2", "brightness", 145), Origin: originCamera}, false, "busy")
		}},
		{"camera-paged", func(h *harness) {
			pageApp(h, "camera", nil)
			setCameras(h, nil, append(ms2109Ctl(), CameraControl{ID: "gamma", Group: "image", Type: "int", Max: 500,
				Step: 1, Value: 100}, CameraControl{ID: "sharpness", Group: "image", Type: "int", Max: 7, Step: 1}), nil)
		}},
		{"camera-empty", func(h *harness) { pageApp(h, "camera", nil) }},
	})
}
