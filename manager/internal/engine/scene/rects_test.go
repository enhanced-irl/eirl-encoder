package scene

import "testing"

func checkInside(t *testing.T, rects map[int]Layer, w, h int) {
	t.Helper()
	for k, r := range rects {
		if r.X < 0 || r.Y < 0 || r.X+r.W > w || r.Y+r.H > h {
			t.Errorf("slot %d outside the canvas: %+v", k, r.Rect)
		}
	}
}

func TestRectsLayouts(t *testing.T) {
	slots := map[string]int{"a": 0, "b": 2}
	s := New("a")
	s.Secondary = "b"
	for _, layout := range Layouts {
		s.Layout = layout
		r := SceneRects(&s, slots, 4, 1920, 1080, nil, NoPauseSlot)
		checkInside(t, r, 1920, 1080)
		want2 := 1.0
		if layout == LayoutFull {
			want2 = 0
		}
		if r[0].Alpha != 1 || r[1].Alpha != 0 || r[3].Alpha != 0 || r[2].Alpha != want2 {
			t.Errorf("%s: alphas %v %v %v %v", layout, r[0].Alpha, r[1].Alpha, r[2].Alpha, r[3].Alpha)
		}
	}
	s.Layout = LayoutPip
	if got := SceneRects(&s, slots, 4, 1920, 1080, nil, NoPauseSlot)[2].Rect; got != (Rect{1224, 688, 640, 360}) {
		t.Errorf("pip rect %+v", got)
	}
}

func TestRectsMainWithoutSlotShowsNothing(t *testing.T) {
	s := New("usb:auto")
	for k, v := range SceneRects(&s, map[string]int{"a": 0}, 2, 1920, 1080, nil, NoPauseSlot) {
		if v.Alpha != 0 {
			t.Errorf("slot %d visible", k)
		}
	}
}

func TestRectsPipCornersAndSizes(t *testing.T) {
	for _, corner := range Corners {
		for size := range PipSizes {
			r := PipRect(1920, 1080, corner, size)
			if r.X%2 != 0 || r.Y%2 != 0 || r.W%2 != 0 || r.H%2 != 0 {
				t.Errorf("%s/%d odd: %+v", corner, size, r)
			}
			if r.W*1080 != r.H*1920 { // the canvas aspect
				t.Errorf("%s/%d aspect: %+v", corner, size, r)
			}
			if !(0 < r.X && 0 < r.Y && r.X+r.W < 1920 && r.Y+r.H < 1080) {
				t.Errorf("%s/%d not strictly inside: %+v", corner, size, r)
			}
		}
	}
	if got := PipRect(1920, 1080, "tl", 25); got != (Rect{56, 32, 480, 270}) {
		t.Errorf("tl/25: %+v", got)
	}
	if got := PipRect(1920, 1080, "br", 33); got != (Rect{1224, 688, 640, 360}) {
		t.Errorf("default: %+v", got)
	}
}

func TestRectsLetterbox(t *testing.T) {
	full := Rect{0, 0, 1920, 1080}
	cases := []struct {
		a    Aspect
		want Rect
	}{
		{Aspect{}, full},
		{Aspect{1280, 720}, full},
		{Aspect{1080, 1920}, Rect{656, 0, 606, 1080}}, // portrait: bars at the sides
		{Aspect{4, 3}, Rect{240, 0, 1440, 1080}},
		{Aspect{2560, 1080}, Rect{0, 134, 1920, 810}}, // wider: above and below
	}
	for _, c := range cases {
		if got := FitRect(full, c.a); got != c.want {
			t.Errorf("FitRect(%v) = %+v, want %+v", c.a, got, c.want)
		}
	}
	if got := SourceAspect(720, 576); got != (Aspect{4, 3}) {
		t.Errorf("SourceAspect(720, 576) = %v", got)
	}
	s := New("a")
	s.Secondary = "b"
	s.Layout = LayoutSide
	r := SceneRects(&s, map[string]int{"a": 0, "b": 1}, 2, 1920, 1080, map[int]Aspect{1: {1080, 1920}}, NoPauseSlot)
	if r[0].Rect != (Rect{0, 270, 960, 540}) { // 16:9 in its half, centred
		t.Errorf("side main %+v", r[0].Rect)
	}
	if r[1].Rect != (Rect{1136, 0, 606, 1080}) { // the portrait one, full height
		t.Errorf("side secondary %+v", r[1].Rect)
	}
	checkInside(t, r, 1920, 1080)
}

func TestRectsPauseLayer(t *testing.T) {
	s := New("a")
	r := SceneRects(&s, map[string]int{"a": 0}, 2, 1920, 1080, nil, 2)
	if r[2] != (Layer{Rect{0, 0, 240, 134}, 0, 0}) { // never below 1/16: the VIC cannot go there
		t.Errorf("pause off %+v", r[2])
	}
	s.Pause.On = true
	r = SceneRects(&s, map[string]int{"a": 0}, 2, 1920, 1080, nil, 2)
	if r[2] != (Layer{Rect{0, 0, 1920, 1080}, 1, 10}) {
		t.Errorf("pause on %+v", r[2])
	}
	if r[0].Alpha != 1 { // what is below keeps going: when removed, it comes back as it was
		t.Errorf("main under the pause %+v", r[0])
	}
}

func fade(t *testing.T, s0, s1 Scene, slots map[string]int, n, pauseSlot int) (old, new map[int]Layer, roles map[int]Role) {
	t.Helper()
	old = SceneRects(&s0, slots, n, 1920, 1080, nil, pauseSlot)
	new = SceneRects(&s1, slots, n, 1920, 1080, nil, pauseSlot)
	roles = FadePlan(old, new, 1920, 1080)
	for _, f := range []float64{0, 0.3, 0.6, 0.9, 1} {
		checkInside(t, FadeRects(old, new, roles, f), 1920, 1080)
	}
	return old, new, roles
}

func sc(main, secondary, layout string) Scene {
	s := New(main)
	s.Secondary, s.Layout = secondary, layout
	return s
}

func TestRectsFadeTakeIsACrossfade(t *testing.T) {
	old, new, roles := fade(t, sc("a", "", "full"), sc("b", "", "full"), map[string]int{"a": 0, "b": 1}, 4, NoPauseSlot)
	if roles[0] != RoleBase || roles[1] != RoleIn {
		t.Fatalf("roles %v", roles)
	}
	r := FadeRects(old, new, roles, 0.5)
	if r[0].Alpha != 1 || r[1].Alpha != 0.5 { // B comes in over A, which stays opaque
		t.Errorf("alphas %v %v", r[0].Alpha, r[1].Alpha)
	}
	if r[1].Z <= r[0].Z {
		t.Errorf("z %d %d", r[1].Z, r[0].Z)
	}
	if FadeLength(roles) != Fade {
		t.Errorf("length %v", FadeLength(roles))
	}
}

func TestRectsFadeTakeWithPipKeepsPipOnTop(t *testing.T) {
	old, new, roles := fade(t, sc("a", "c", "pip"), sc("b", "c", "pip"), map[string]int{"a": 0, "b": 1, "c": 2}, 4, NoPauseSlot)
	if roles[0] != RoleBase || roles[1] != RoleIn || roles[2] != RoleStatic {
		t.Fatalf("roles %v", roles)
	}
	r := FadeRects(old, new, roles, 0.5)
	if r[2].Z <= r[1].Z || r[1].Z <= r[0].Z { // the PiP above the one coming in
		t.Errorf("z %d %d %d", r[0].Z, r[1].Z, r[2].Z)
	}
}

func TestRectsFadePipInAndOut(t *testing.T) {
	slots := map[string]int{"a": 0, "b": 1}
	_, _, roles := fade(t, sc("a", "b", "full"), sc("a", "b", "pip"), slots, 4, NoPauseSlot)
	if roles[0] != RoleStatic || roles[1] != RoleIn {
		t.Fatalf("roles in %v", roles)
	}
	old, new, roles := fade(t, sc("a", "b", "pip"), sc("a", "b", "full"), slots, 4, NoPauseSlot)
	if roles[1] != RoleOut {
		t.Fatalf("roles out %v", roles)
	}
	r := FadeRects(old, new, roles, 0.25)
	if r[1].Alpha != 0.75 || r[1].Z <= r[0].Z { // fades out, on top
		t.Errorf("out %+v under %+v", r[1], r[0])
	}
}

func TestRectsFadeSwapInPip(t *testing.T) {
	old, new, roles := fade(t, sc("a", "b", "pip"), sc("b", "a", "pip"), map[string]int{"a": 0, "b": 1}, 4, NoPauseSlot)
	if roles[0] != RoleLaterFade || roles[1] != RoleIn {
		t.Fatalf("roles %v", roles)
	}
	if FadeLength(roles) != Fade*3/2 {
		t.Errorf("length %v", FadeLength(roles))
	}
	r := FadeRects(old, new, roles, 0.3) // 1st part: B comes in full-screen over A
	if r[0].Rect != old[0].Rect || r[0].Alpha != old[0].Alpha || r[1].Rect != new[1].Rect {
		t.Errorf("t=0.3: %+v %+v", r[0], r[1])
	}
	r = FadeRects(old, new, roles, 0.85) // 2nd: A comes into the PiP, over B
	if r[0].Rect != new[0].Rect || !(0 < r[0].Alpha && r[0].Alpha < 1) || r[0].Z <= r[1].Z {
		t.Errorf("t=0.85: %+v %+v", r[0], r[1])
	}
	r = FadeRects(old, new, roles, 1)
	if len(r) != len(new) {
		t.Fatalf("t=1: %d slots, want %d", len(r), len(new))
	}
	for k, v := range new {
		if r[k].Rect != v.Rect || r[k].Alpha != v.Alpha {
			t.Errorf("t=1 slot %d: %+v, want %+v", k, r[k], v)
		}
	}
}

func TestRectsFadeFullToSideCutsTheMove(t *testing.T) {
	_, _, roles := fade(t, sc("a", "b", "full"), sc("a", "b", "side"), map[string]int{"a": 0, "b": 1}, 4, NoPauseSlot)
	if roles[0] != RoleLaterCut || roles[1] != RoleIn {
		t.Fatalf("roles %v", roles)
	}
}

func TestRectsFadePause(t *testing.T) {
	s1 := sc("a", "", "full")
	s1.Pause.On = true
	old, new, roles := fade(t, sc("a", "", "full"), s1, map[string]int{"a": 0}, 1, 1)
	if roles[0] != RoleStatic || roles[1] != RoleIn {
		t.Fatalf("roles on %v", roles)
	}
	if r := FadeRects(old, new, roles, 0.5); r[1] != (Layer{Rect{0, 0, 1920, 1080}, 0.5, 11}) {
		t.Errorf("pause at 0.5: %+v", r[1])
	}
	old, new, roles = fade(t, s1, sc("a", "", "full"), map[string]int{"a": 0}, 1, 1)
	if roles[1] != RoleOut { // when removed, it fades out and what is below shows
		t.Fatalf("roles off %v", roles)
	}
	if a := FadeRects(old, new, roles, 0.5)[1].Alpha; a != 0.5 {
		t.Errorf("pause off at 0.5: alpha %v", a)
	}
}

// Numbers taken from the Python engine for odd sizes and the two-part fade, where integer division
// and float rounding could drift.
func TestRectsMatchPythonNumbers(t *testing.T) {
	cases := []struct{ got, want Rect }{
		{PipRect(1280, 720, "tr", 33), Rect{816, 20, 426, 240}},
		{PipRect(1281, 721, "bl", 25), Rect{38, 521, 320, 180}},
		{PauseOff(1280, 720), Rect{0, 0, 160, 90}},
		{FitRect(Rect{10, 20, 601, 333}, Aspect{16, 10}), Rect{44, 20, 532, 333}},
		{FitRect(Rect{10, 20, 333, 601}, Aspect{9, 16}), Rect{10, 24, 333, 592}},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: %+v, want %+v", i, c.got, c.want)
		}
	}
	r := FadeRects(map[int]Layer{0: {Rect{0, 0, 1920, 1080}, 1, 1}}, map[int]Layer{0: {Rect{0, 0, 640, 360}, 1, 2}},
		map[int]Role{0: RoleLaterFade}, 0.85)
	if r[0] != (Layer{Rect{0, 0, 640, 360}, 0.5499999999999998, 3}) {
		t.Errorf("later-fade at 0.85: %+v", r[0])
	}
	if fdiv(-7, 2) != -4 || fdiv(7, -2) != -4 || fdiv(-8, 2) != -4 || fdiv(7, 2) != 3 {
		t.Error("fdiv is not Python's //")
	}
}
