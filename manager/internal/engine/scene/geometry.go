package scene

// Rect is a rectangle in canvas pixels.
type Rect struct{ X, Y, W, H int }

// Layer is where a slot goes on the program: its rectangle, its alpha and its zorder.
type Layer struct {
	Rect
	Alpha float64
	Z     int
}

// Aspect is a source's width:height. The zero value means unknown (Python's None).
type Aspect struct{ W, H int }

func (a Aspect) valid() bool { return a.W > 0 && a.H > 0 }

// fdiv is Python's // (floors; Go's / truncates). Only differs for negative operands, which valid
// sizes never have, but the port keeps the arithmetic exact.
func fdiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// even rounds down to an even number: the VIC works in NV12/RGBA and an odd coordinate shifts chroma.
func even(v int) int { return fdiv(v, 2) * 2 }

// SourceAspect is a source's aspect from its resolution. SD over HDMI (720×480/576) is 4:3 with
// non-square pixels. Non-positive sizes give the zero Aspect (unknown).
func SourceAspect(w, h int) Aspect {
	if w == 720 && (h == 480 || h == 576) {
		return Aspect{4, 3}
	}
	if w > 0 && h > 0 {
		return Aspect{w, h}
	}
	return Aspect{}
}

// PipRect is the PiP rectangle on a w×h canvas. An unknown size falls back to 33 %.
func PipRect(w, h int, corner string, size int) Rect {
	frac, ok := PipSizes[size]
	if !ok {
		frac = [2]int{1, 3}
	}
	pw, ph := even(fdiv(w*frac[0], frac[1])), even(fdiv(h*frac[0], frac[1]))
	mx, my := even(fdiv(w*3, 100)), even(fdiv(h*3, 100))
	r := Rect{X: w - pw - mx, Y: h - ph - my, W: pw, H: ph}
	if corner == "tl" || corner == "bl" {
		r.X = mx
	}
	if corner == "tl" || corner == "tr" {
		r.Y = my
	}
	return r
}

// FitRect fits a source of aspect a inside r, centred with black bars and never cropped
// (nvcompositor does not crop). The zero Aspect leaves r as is (the canvas aspect).
func FitRect(r Rect, a Aspect) Rect {
	if !a.valid() {
		return r
	}
	wide, tall := a.W*r.H, a.H*r.W
	diff := wide - tall
	if diff < 0 {
		diff = -diff
	}
	if diff*100 <= max(wide, tall) { // same aspect (±1 %)
		return r
	}
	if wide > tall { // wider than r: bars above and below
		nh := even(fdiv(r.W*a.H, a.W))
		return Rect{X: r.X, Y: r.Y + fdiv(r.H-nh, 4)*2, W: r.W, H: nh}
	}
	nw := even(fdiv(r.H*a.W, a.H)) // narrower (portrait, 4:3): bars at the sides
	return Rect{X: r.X + fdiv(r.W-nw, 4)*2, Y: r.Y, W: nw, H: r.H}
}

// PauseOff is the Pause layer while off: small and invisible, inside the canvas (MIXER.md §3 trap
// 2), and never scaled down more than 8 times: the VIC does not go below 1/16 ("nvbuffer_composite
// Failed", trap 22).
func PauseOff(w, h int) Rect {
	return Rect{0, 0, even(fdiv(w, 8)), even(fdiv(h, 8))}
}

// NoPauseSlot tells SceneRects that the pipeline has no Pause layer.
const NoPauseSlot = -1

// SceneRects is the geometry of every program slot. Unused slots go full-canvas with alpha 0:
// nvcompositor rejects rectangles outside the canvas (MIXER.md §3, trap 2). slotOf maps source ids
// to slots; aspects holds each slot's source aspect, for the bars (nil is fine). pauseSlot is the
// Pause layer, on top of everything, or NoPauseSlot.
func SceneRects(s *Scene, slotOf map[string]int, nslots, w, h int, aspects map[int]Aspect, pauseSlot int) map[int]Layer {
	full := Rect{0, 0, w, h}
	rects := make(map[int]Layer, nslots+1)
	for i := range nslots {
		rects[i] = Layer{Rect: full}
	}
	if pauseSlot >= 0 {
		if s.Pause.On {
			rects[pauseSlot] = Layer{Rect: full, Alpha: 1, Z: 10}
		} else {
			rects[pauseSlot] = Layer{Rect: PauseOff(w, h)}
		}
	}
	m, okM := lookup(slotOf, s.Main)
	if !okM {
		return rects
	}
	sec, okS := lookup(slotOf, s.Secondary)
	switch {
	case s.Layout == LayoutFull || !okS || sec == m:
		rects[m] = Layer{Rect: FitRect(full, aspects[m]), Alpha: 1, Z: 1}
	case s.Layout == LayoutPip:
		rects[m] = Layer{Rect: FitRect(full, aspects[m]), Alpha: 1, Z: 1}
		rects[sec] = Layer{Rect: FitRect(PipRect(w, h, s.Pip.Corner, s.Pip.Size), aspects[sec]), Alpha: 1, Z: 2}
	default: // side by side: each in its half, as tall as it fits and centred
		hw := even(fdiv(w, 2))
		rects[m] = Layer{Rect: FitRect(Rect{0, 0, hw, h}, orCanvas(aspects[m], w, h)), Alpha: 1, Z: 1}
		rects[sec] = Layer{Rect: FitRect(Rect{w - hw, 0, hw, h}, orCanvas(aspects[sec], w, h)), Alpha: 1, Z: 1}
	}
	return rects
}

func lookup(slotOf map[string]int, id string) (int, bool) {
	if id == "" {
		return 0, false
	}
	slot, ok := slotOf[id]
	return slot, ok
}

// orCanvas: a source without a known aspect keeps the canvas one in a half (it is not a full fit).
func orCanvas(a Aspect, w, h int) Aspect {
	if a.valid() {
		return a
	}
	return Aspect{w, h}
}
