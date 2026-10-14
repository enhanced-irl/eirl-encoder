package ui

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"time"

	xdraw "golang.org/x/image/draw"
)

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

// render is the drawing's buffers and caches. Two bases (the UI without the VU meters) alternate: the new
// drawing is compared row by row with the previous one, and only the rows that changed go to the output
// image, where the meters are drawn on top 20 times a second over their own rectangles (MIXER.md §17: the
// meters repaint only their base). Nothing moved = no new image.
type render struct {
	bufs       [2]*image.RGBA
	cur        int // bufs[cur] is the current base
	out        *image.RGBA
	co         *Canvas
	force      bool
	meterSpecs []meterSpec
	meterState map[meterKey]MeterState
	looks      []meterLook
	looksSet   bool
	overlay    bool
	drawn      time.Time
	meterAt    time.Time
	t0         time.Time
	drawMax    time.Duration
	icons      map[iconKey]*image.RGBA
	logo       *image.RGBA
	logoH      int
	logoTried  bool
	kbBase     *image.RGBA
	kbLayer    string
	path       Path
}

type meterKey struct {
	pgm bool
	ch  int
	id  string
}

type meterSpec struct {
	key        meterKey
	x, y, w, h float64
	strong     bool
}

type meterLook struct{ bar, peak int }

func (u *UI) canDraw() bool { return u.Fonts != nil && u.r.out != nil }

// SetLayout (re)starts the drawing for a screen of this layout (a new screen pipeline): fresh buffers, and
// the next Tick produces a full image.
func (u *UI) SetLayout(lay *Layout) {
	u.Lay = lay
	r := &u.r
	rect := image.Rect(0, 0, lay.DW, lay.DH)
	if r.out == nil || r.out.Rect != rect {
		r.bufs = [2]*image.RGBA{image.NewRGBA(rect), image.NewRGBA(rect)}
		r.out = image.NewRGBA(rect)
		r.kbBase = nil
	} else {
		clear(r.bufs[0].Pix)
		clear(r.bufs[1].Pix)
		clear(r.out.Pix)
	}
	r.co = NewCanvas(r.out, u.Fonts)
	r.force, r.looksSet = true, false
	if r.meterState == nil {
		r.meterState = map[meterKey]MeterState{}
	}
	if r.t0.IsZero() {
		r.t0 = u.now()
	}
	u.Dirty = true
}

// Image is the last image (RGBA, premultiplied: the UI layer's appsrc caps).
func (u *UI) Image() *image.RGBA { return u.r.out }

// PushFailed: the image could not be pushed (no free buffer): the next Tick produces it again.
func (u *UI) PushFailed() { u.r.force = true }

// TakeDrawMax is the slowest full drawing since the last call (the engine's timings line).
func (u *UI) TakeDrawMax() time.Duration {
	d := u.r.drawMax
	u.r.drawMax = 0
	return d
}

func (u *UI) frame(now time.Time, hd *hold) bool {
	r := &u.r
	if !u.canDraw() {
		return false
	}
	// Everything when something changed (a touch, down and up) or every second, and 10 times a second with a
	// held button (its progress); otherwise only the meters over what is drawn (20 a second)
	if u.Dirty || r.force || now.Sub(r.drawn) >= time.Second || (hd != nil && now.Sub(r.drawn) >= 100*time.Millisecond) {
		t0 := time.Now()
		y0, y1 := u.drawBase(now)
		r.drawMax = max(r.drawMax, time.Since(t0))
		r.drawn, r.meterAt = now, now
		u.Dirty = false
		return u.compose(now, y0, y1)
	}
	if len(r.meterSpecs) > 0 && !r.overlay && now.Sub(r.meterAt) >= MeterEvery-5*time.Millisecond {
		r.meterAt = now
		return u.compose(now, 0, 0)
	}
	return false
}

// drawBase draws the UI into the back base, swaps, and returns the rows that differ from the previous one.
func (u *UI) drawBase(now time.Time) (int, int) {
	r := &u.r
	back := r.bufs[1-r.cur]
	c := NewCanvas(back, u.Fonts)
	u.draw(c, now)
	prev := r.bufs[r.cur]
	r.cur = 1 - r.cur
	stride := back.Stride
	y0, y1 := -1, 0
	for y := 0; y < back.Rect.Dy(); y++ {
		a, b := back.Pix[y*stride:(y+1)*stride], prev.Pix[y*stride:(y+1)*stride]
		if !bytes.Equal(a, b) {
			if y0 < 0 {
				y0 = y
			}
			y1 = y + 1
		}
	}
	if y0 < 0 {
		return 0, 0
	}
	return y0, y1
}

// compose puts the base's changed rows [y0, y1) into the output and the meters on top with their
// ballistics. Without changed rows, and no meter moved a pixel: no new image (false).
func (u *UI) compose(now time.Time, y0, y1 int) bool {
	r := &u.r
	t := now.Sub(r.t0).Seconds()
	var looks []meterLook
	if !r.overlay {
		looks = make([]meterLook, 0, len(r.meterSpecs))
		for _, sp := range r.meterSpecs {
			db, ok := u.meterLevel(sp.key)
			st := MeterStep(r.meterState[sp.key], db, ok, t)
			r.meterState[sp.key] = st
			n := sp.h
			looks = append(looks, meterLook{int(MeterFrac(st.Shown, true) * n), int(MeterFrac(st.Peak, true) * n)})
		}
	}
	same := r.looksSet && len(looks) == len(r.looks)
	if same {
		for i := range looks {
			if looks[i] != r.looks[i] {
				same = false
				break
			}
		}
	}
	if y0 >= y1 && same && !r.force {
		return false
	}
	r.looks, r.looksSet, r.force = looks, true, false
	base := r.bufs[r.cur]
	if y0 < y1 {
		r.co.CopyFrom(base, image.Rect(0, y0, base.Rect.Dx(), y1))
	}
	if r.overlay {
		return true
	}
	// One pixel more for the antialiasing; first all the restores, then all the meters: the two channels'
	// touch each other
	for _, sp := range r.meterSpecs {
		r.co.CopyFrom(base, image.Rect(int(sp.x)-1, int(sp.y)-1, int(sp.x)-1+int(sp.w)+3, int(sp.y)-1+int(sp.h)+3))
	}
	for _, sp := range r.meterSpecs {
		st := r.meterState[sp.key]
		drawMeter(r.co, sp.x, sp.y, sp.w, sp.h, st.Shown, st.Peak, true, sp.strong)
	}
	return true
}

func (u *UI) meterLevel(k meterKey) (float64, bool) {
	if u.Levels == nil {
		return 0, false
	}
	if k.pgm {
		return u.Levels.ProgramPeak(k.ch)
	}
	return u.Levels.InputPeak(k.id)
}

// Meter colours (engine_mixer.py M_*).
var (
	mTrack = Color{0.031, 0.043, 0.075, 0.75}
	mGood  = Color{0.071, 0.718, 0.416, 1}
	mWarn  = Color{0.969, 0.565, 0.035, 1}
	mBad   = Color{0.941, 0.267, 0.22, 1}
)

// drawMeter is a vertical VU meter from −60 to 0 dB: green, amber from −18 and red from −6, with the peak
// mark. ok false = no level.
func drawMeter(c *Canvas, x, y, w, h, db, peak float64, ok, strong bool) {
	c.FillRect(x, y, w, h, mTrack)
	f, a := MeterFrac(db, ok), 1.0
	if !strong {
		a = 0.55
	}
	for _, seg := range []struct {
		lo, hi float64
		col    Color
	}{{0, 0.7, mGood}, {0.7, 0.9, mWarn}, {0.9, 1, mBad}} {
		if f > seg.lo {
			top := min(f, seg.hi)
			c.FillRect(x+1, y+h*(1-top), w-2, h*(top-seg.lo), seg.col.WithAlpha(a))
		}
	}
	pf := MeterFrac(peak, ok)
	if pf > 0.02 {
		col := mGood
		if pf > 0.9 {
			col = mBad
		} else if pf > 0.7 {
			col = mWarn
		}
		c.FillRect(x+1, y+h*(1-pf), w-2, 2, col)
	}
}

// --- icons ---

type iconKey struct {
	kind  string
	n     int
	r, lw float64
	col   Color
}

// icon draws an icon of drawIcon once per kind, size and colour and then pastes it at a whole pixel
// (tracing it at every drawing was ~13 % of the drawing, measured in the Python engine).
func (u *UI) icon(c *Canvas, kind string, cx, cy, r, lw float64, col Color) {
	n := int(2*(r+lw)) + 4
	k := iconKey{kind, n, math.Round(r*10) / 10, math.Round(lw*10) / 10, col}
	if u.r.icons == nil {
		u.r.icons = map[iconKey]*image.RGBA{}
	}
	img := u.r.icons[k]
	if img == nil {
		img = image.NewRGBA(image.Rect(0, 0, n, n))
		ic := NewCanvas(img, nil)
		drawIcon(ic, &u.r.path, kind, float64(n)/2, float64(n)/2, r, lw, col)
		if len(u.r.icons) > 64 { // few kinds and colours: just in case
			clear(u.r.icons)
		}
		u.r.icons[k] = img
	}
	c.DrawImage(img, int(math.RoundToEven(cx-float64(n)/2)), int(math.RoundToEven(cy-float64(n)/2)))
}

// drawIcon is draw_icon: an icon of strokes (no fonts) centred on (cx, cy), radius r: wifi (wifi1-3: only
// the arcs of that signal), power, lock / unlock, gear, close, back, hotspot, camera, shift, backspace.
func drawIcon(c *Canvas, p *Path, kind string, cx, cy, r, lw float64, col Color) {
	p.NewPath()
	stroke := func() { c.Stroke(p, lw, col) }
	roundRect := func(bx, by, bw, bh, k float64) {
		p.NewSubPath()
		p.Arc(bx+bw-k, by+k, k, -math.Pi/2, 0)
		p.Arc(bx+bw-k, by+bh-k, k, 0, math.Pi/2)
		p.Arc(bx+k, by+bh-k, k, math.Pi/2, math.Pi)
		p.Arc(bx+k, by+k, k, math.Pi, 3*math.Pi/2)
		p.ClosePath()
	}
	switch kind {
	case "wifi", "wifi1", "wifi2", "wifi3":
		by := cy + 0.5*r // the dot at the bottom, where the three arcs come from
		arcs := 3
		if len(kind) > 4 {
			arcs = int(kind[4] - '0')
		}
		for _, k := range []float64{0.37, 0.69, 1.0}[:arcs] {
			p.NewSubPath()
			p.Arc(cx, by, k*r, -2.356, -0.785)
			stroke()
		}
		c.FillCircle(cx, by, max(lw*0.85, 0.12*r), col)
	case "shift": // upper case: a hollow arrow up
		p.MoveTo(cx, cy-0.85*r)
		p.LineTo(cx+0.8*r, cy)
		p.LineTo(cx+0.38*r, cy)
		p.LineTo(cx+0.38*r, cy+0.75*r)
		p.LineTo(cx-0.38*r, cy+0.75*r)
		p.LineTo(cx-0.38*r, cy)
		p.LineTo(cx-0.8*r, cy)
		p.ClosePath()
		stroke()
	case "backspace": // erase: a tag to the left with an X
		p.MoveTo(cx-0.95*r, cy)
		p.LineTo(cx-0.45*r, cy-0.6*r)
		p.LineTo(cx+0.95*r, cy-0.6*r)
		p.LineTo(cx+0.95*r, cy+0.6*r)
		p.LineTo(cx-0.45*r, cy+0.6*r)
		p.ClosePath()
		stroke()
		for _, a := range []float64{-1, 1} {
			p.MoveTo(cx-0.05*r, cy+a*0.28*r)
			p.LineTo(cx+0.5*r, cy-a*0.28*r)
			stroke()
		}
	case "power":
		p.Arc(cx, cy+0.06*r, 0.8*r, -1.5708+0.7, 4.7124-0.7)
		stroke()
		p.MoveTo(cx, cy-0.94*r)
		p.LineTo(cx, cy-0.12*r)
		stroke()
	case "lock", "unlock":
		bw, bh := 1.34*r, 0.96*r
		bx, by := cx-bw/2, cy-0.08*r
		roundRect(bx, by, bw, bh, 0.18*r)
		stroke()
		sr := 0.4 * r // the shackle: open, raised and with its right leg out
		top := by - 0.36*r
		if kind == "unlock" {
			top -= 0.3 * r
		}
		p.MoveTo(cx-sr, by)
		p.LineTo(cx-sr, top)
		p.Arc(cx, top, sr, math.Pi, 2*math.Pi)
		if kind == "lock" {
			p.LineTo(cx+sr, by)
		} else {
			p.LineTo(cx+sr, top+0.22*r)
		}
		stroke()
		c.FillCircle(cx, by+bh*0.48, max(lw*0.7, 0.1*r), col)
	case "back": // ←
		p.MoveTo(cx+0.75*r, cy)
		p.LineTo(cx-0.75*r, cy)
		p.MoveTo(cx-0.2*r, cy-0.55*r)
		p.LineTo(cx-0.75*r, cy)
		p.LineTo(cx-0.2*r, cy+0.55*r)
		stroke()
	case "close":
		for _, a := range []float64{-1, 1} {
			p.MoveTo(cx-0.55*r, cy+a*0.55*r)
			p.LineTo(cx+0.55*r, cy-a*0.55*r)
			stroke()
		}
	case "gear":
		n, ro, ri := 8, 0.98*r, 0.7*r
		step := 6.2832 / float64(n)
		for i := range n {
			a := float64(i)*step - 1.5708
			p.LineTo(cx+ri*math.Cos(a-0.3*step), cy+ri*math.Sin(a-0.3*step))
			p.LineTo(cx+ro*math.Cos(a-0.17*step), cy+ro*math.Sin(a-0.17*step))
			p.LineTo(cx+ro*math.Cos(a+0.17*step), cy+ro*math.Sin(a+0.17*step))
			p.LineTo(cx+ri*math.Cos(a+0.3*step), cy+ri*math.Sin(a+0.3*step))
			p.Arc(cx, cy, ri, a+0.3*step, a+0.7*step)
		}
		p.ClosePath()
		stroke()
		p.Arc(cx, cy, 0.3*r, 0, 6.2832)
		stroke()
	case "hotspot": // «Crear mi wifi»: a dot that emits, with two arcs on each side
		c.FillCircle(cx, cy, max(lw*0.9, 0.14*r), col)
		for _, k := range []float64{0.5, 0.92} {
			for _, a0 := range []float64{-0.75, 3.1416 - 0.75} {
				p.NewSubPath()
				p.Arc(cx, cy, k*r, a0, a0+1.5)
				stroke()
			}
		}
	case "globe": // the language: a circle, its equator and two meridians
		p.Arc(cx, cy, 0.9*r, 0, 2*math.Pi)
		stroke()
		p.MoveTo(cx-0.9*r, cy)
		p.LineTo(cx+0.9*r, cy)
		stroke()
		for _, k := range []float64{0.42, 0.0} {
			p.NewSubPath()
			if k == 0 {
				p.MoveTo(cx, cy-0.9*r)
				p.LineTo(cx, cy+0.9*r)
			} else {
				// an ellipse by its two halves (Arc of a scaled circle is not available: polyline)
				for i := 0; i <= 24; i++ {
					a := float64(i) / 24 * 2 * math.Pi
					x, y := cx+k*r*math.Sin(a), cy-0.9*r*math.Cos(a)
					if i == 0 {
						p.MoveTo(x, y)
					} else {
						p.LineTo(x, y)
					}
				}
			}
			stroke()
		}
	case "clock": // the date and time: a dial and two hands
		p.Arc(cx, cy, 0.9*r, 0, 2*math.Pi)
		stroke()
		p.MoveTo(cx, cy-0.55*r)
		p.LineTo(cx, cy)
		p.LineTo(cx+0.4*r, cy+0.25*r)
		stroke()
	case "update": // the updates: an arrow down onto a tray
		p.MoveTo(cx, cy-0.9*r)
		p.LineTo(cx, cy+0.3*r)
		p.MoveTo(cx-0.45*r, cy-0.15*r)
		p.LineTo(cx, cy+0.3*r)
		p.LineTo(cx+0.45*r, cy-0.15*r)
		stroke()
		p.MoveTo(cx-0.9*r, cy+0.35*r)
		p.LineTo(cx-0.9*r, cy+0.85*r)
		p.LineTo(cx+0.9*r, cy+0.85*r)
		p.LineTo(cx+0.9*r, cy+0.35*r)
		stroke()
	case "info": // about: an i in a circle
		p.Arc(cx, cy, 0.9*r, 0, 2*math.Pi)
		stroke()
		p.MoveTo(cx, cy-0.05*r)
		p.LineTo(cx, cy+0.5*r)
		stroke()
		c.FillCircle(cx, cy-0.38*r, max(lw*0.7, 0.1*r), col)
	case "check": // ✓
		p.MoveTo(cx-0.7*r, cy)
		p.LineTo(cx-0.2*r, cy+0.5*r)
		p.LineTo(cx+0.75*r, cy-0.5*r)
		stroke()
	case "grid": // the sources: four screens
		k := 0.12 * r
		for _, q := range [][2]float64{{-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
			bx, by := cx+q[0]*(0.45*r+k/2)-0.45*r, cy+q[1]*(0.35*r+k/2)-0.35*r
			roundRect(bx, by, 0.9*r, 0.7*r, 0.12*r)
			stroke()
		}
	case "stream": // the stream: a mast that emits, two arcs on each side of its top
		ty := cy - 0.35*r
		c.FillCircle(cx, ty, max(lw*0.9, 0.13*r), col)
		for _, k := range []float64{0.42, 0.78} {
			for _, a0 := range []float64{-0.7, 3.1416 - 0.7} {
				p.NewSubPath()
				p.Arc(cx, ty, k*r, a0, a0+1.4)
				stroke()
			}
		}
		p.MoveTo(cx, ty+0.2*r)
		p.LineTo(cx, cy+0.95*r)
		p.MoveTo(cx-0.35*r, cy+0.95*r)
		p.LineTo(cx+0.35*r, cy+0.95*r)
		stroke()
	case "cloud": // EnhancedIRL: a cloud of three bumps on a flat base
		by := cy + 0.5*r
		p.MoveTo(cx-0.6*r, by)
		p.Arc(cx-0.6*r, by-0.35*r, 0.35*r, math.Pi/2, 3*math.Pi/2)
		p.Arc(cx-0.1*r, by-0.6*r, 0.5*r, math.Pi, 1.85*math.Pi)
		p.Arc(cx+0.55*r, by-0.4*r, 0.4*r, 1.5*math.Pi, 2.5*math.Pi)
		p.ClosePath()
		stroke()
	case "sliders": // the device's settings: three lines, each with its knob
		for i, k := range []float64{-0.3, 0.45, -0.05} {
			y := cy + float64(i-1)*0.6*r
			p.MoveTo(cx-0.9*r, y)
			p.LineTo(cx+0.9*r, y)
			stroke()
			c.FillCircle(cx+k*r*1.6, y, max(lw*1.3, 0.17*r), col)
		}
	case "eye", "eyeoff": // a secret: an eye (shown: crossed out, tap to hide it again)
		for _, sg := range []float64{-1, 1} {
			p.NewSubPath()
			for i := 0; i <= 16; i++ {
				t := float64(i)/16*2 - 1 // −1…1 across the eye
				x := cx + 0.95*r*t
				y := cy + sg*0.55*r*(1-t*t)
				if i == 0 {
					p.MoveTo(x, y)
				} else {
					p.LineTo(x, y)
				}
			}
			stroke()
		}
		p.NewSubPath()
		p.Arc(cx, cy, 0.27*r, 0, 2*math.Pi)
		stroke()
		if kind == "eyeoff" {
			p.MoveTo(cx-0.8*r, cy+0.75*r)
			p.LineTo(cx+0.8*r, cy-0.75*r)
			stroke()
		}
	case "ethernet": // a cable uplink: an RJ45 socket
		roundRect(cx-0.85*r, cy-0.7*r, 1.7*r, 1.4*r, 0.18*r)
		stroke()
		p.MoveTo(cx-0.4*r, cy+0.7*r)
		p.LineTo(cx-0.4*r, cy+0.25*r)
		p.LineTo(cx+0.4*r, cy+0.25*r)
		p.LineTo(cx+0.4*r, cy+0.7*r)
		stroke()
		for _, k := range []float64{-0.4, -0.13, 0.13, 0.4} {
			p.MoveTo(cx+k*r, cy-0.45*r)
			p.LineTo(cx+k*r, cy-0.15*r)
			stroke()
		}
	case "modem": // a cellular uplink: four rising bars
		for i, h := range []float64{0.35, 0.7, 1.05, 1.4} {
			x := cx - 0.75*r + float64(i)*0.5*r
			p.MoveTo(x, cy+0.75*r)
			p.LineTo(x, cy+0.75*r-h*r)
			stroke()
		}
	case "camera": // the network cameras: the body and the lens
		bw, bh := 1.15*r, 0.9*r
		bx, by := cx-0.95*r, cy-bh/2
		roundRect(bx, by, bw, bh, 0.14*r)
		stroke()
		p.MoveTo(bx+bw+0.12*r, cy-0.12*r)
		p.LineTo(cx+0.95*r, cy-0.4*r)
		p.LineTo(cx+0.95*r, cy+0.4*r)
		p.LineTo(bx+bw+0.12*r, cy+0.12*r)
		p.ClosePath()
		stroke()
	}
	p.NewPath()
}

// logoImage is the logo at h px high, scaled once per height (draw runs 1-10 times a second). nil if
// there is none (the header writes the name).
func (u *UI) logoImage(h int) *image.RGBA {
	r := &u.r
	if r.logoTried && r.logoH == h {
		return r.logo
	}
	r.logoTried, r.logoH, r.logo = true, h, nil
	if u.Logo == nil || h <= 0 {
		return nil
	}
	b := u.Logo.Bounds()
	k := float64(h) / float64(b.Dy())
	out := image.NewRGBA(image.Rect(0, 0, int(float64(b.Dx())*k+0.5), h))
	xdraw.CatmullRom.Scale(out, out.Rect, u.Logo, b, xdraw.Src, nil)
	r.logo = out
	return out
}
