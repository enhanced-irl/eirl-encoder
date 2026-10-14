package ui

import (
	"encoding/binary"
	"image"
	"math"

	"golang.org/x/image/vector"
)

// Color is a straight (not premultiplied) colour, 0…1, in screen order: the screen takes RGBA bytes as they
// are in memory (the Python engine swapped R and B on Cairo's BGRA to get the same bytes).
type Color struct{ R, G, B, A float64 }

// WithAlpha is the colour with another alpha (Python's rgba[:3] + (a,)).
func (c Color) WithAlpha(a float64) Color { c.A = a; return c }

// Canvas draws into a premultiplied RGBA image with the operations the screen needs, antialiased like Cairo:
// axis-aligned rectangles (exact box-filter coverage), rounded rectangles and circles (signed distance:
// exact on straight edges, within a few % on the curves), general paths for the icons, images and text.
// 8-bit blending rounds like pixman. Not safe for concurrent use.
type Canvas struct {
	Img   *image.RGBA
	Fonts *Fonts
	ras   vector.Rasterizer
	cov   []float32
	alpha *image.Alpha
}

// NewCanvas wraps img.
func NewCanvas(img *image.RGBA, fonts *Fonts) *Canvas { return &Canvas{Img: img, Fonts: fonts} }

// div255 is x/255 rounded, for x ≤ 255·255 (pixman's MUL_UN8).
func div255(x uint32) uint32 { x += 128; return (x + x>>8) >> 8 }

// comp8 is a colour component as Cairo makes it: to 16 bits (rounded), then the top 8.
func comp8(v float64) uint32 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint32(v*65535+0.5) >> 8
}

// premul is the colour as premultiplied 8-bit components.
func premul(c Color) (r, g, b, a uint32) {
	a = comp8(c.A)
	return comp8(c.R * c.A), comp8(c.G * c.A), comp8(c.B * c.A), a
}

type src8 struct{ r, g, b, a uint32 }

func (s src8) scaled(cov float32) src8 {
	if cov >= 1 {
		return s
	}
	m := uint32(cov*255 + 0.5)
	return src8{div255(s.r * m), div255(s.g * m), div255(s.b * m), div255(s.a * m)}
}

// over blends one premultiplied pixel at offset i.
func (c *Canvas) over(i int, s src8) {
	if s.a == 0 && s.r == 0 && s.g == 0 && s.b == 0 {
		return
	}
	p := c.Img.Pix[i : i+4 : i+4]
	if s.a == 255 {
		p[0], p[1], p[2], p[3] = uint8(s.r), uint8(s.g), uint8(s.b), 255
		return
	}
	inv := 255 - s.a
	p[0] = uint8(s.r + div255(uint32(p[0])*inv))
	p[1] = uint8(s.g + div255(uint32(p[1])*inv))
	p[2] = uint8(s.b + div255(uint32(p[2])*inv))
	p[3] = uint8(s.a + div255(uint32(p[3])*inv))
}

// span blends s over pixels [x0, x1) of row y (already clipped). Two channels per multiply, like pixman
// (the same rounding as div255 on each channel).
func (c *Canvas) span(y, x0, x1 int, s src8) {
	if x0 >= x1 || (s.a == 0 && s.r == 0 && s.g == 0 && s.b == 0) {
		return
	}
	i := c.Img.PixOffset(x0, y)
	n := (x1 - x0) * 4
	row := c.Img.Pix[i : i+n : i+n]
	packed := s.r | s.g<<8 | s.b<<16 | s.a<<24
	if s.a == 255 {
		for j := 0; j < n; j += 4 {
			binary.LittleEndian.PutUint32(row[j:], packed)
		}
		return
	}
	inv := 255 - s.a
	for j := 0; j < n; j += 4 {
		x := binary.LittleEndian.Uint32(row[j:])
		rb := (x & 0x00ff00ff) * inv
		rb = (rb + 0x00800080 + (rb>>8)&0x00ff00ff) >> 8 & 0x00ff00ff
		ag := (x >> 8 & 0x00ff00ff) * inv
		ag = (ag + 0x00800080 + (ag>>8)&0x00ff00ff) >> 8 & 0x00ff00ff
		binary.LittleEndian.PutUint32(row[j:], packed+(rb|ag<<8))
	}
}

func (c *Canvas) src(col Color) src8 {
	r, g, b, a := premul(col)
	return src8{r, g, b, a}
}

// Clear makes every pixel transparent (Cairo's OPERATOR_CLEAR paint).
func (c *Canvas) Clear() { clear(c.Img.Pix) }

// Paint covers the whole image with the colour (OVER).
func (c *Canvas) Paint(col Color) {
	b := c.Img.Rect
	s := c.src(col)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		c.span(y, b.Min.X, b.Max.X, s)
	}
}

// overlap is the length of [a0, a1) ∩ [b0, b1).
func overlap(a0, a1, b0, b1 float64) float64 {
	return max(0, min(a1, b1)-max(a0, b0))
}

// FillRect fills an axis-aligned rectangle with exact antialiased edges.
func (c *Canvas) FillRect(x, y, w, h float64, col Color) {
	c.fillRect(x, y, w, h, col, nil)
}

// FillRectInRR fills the rectangle clipped to a rounded rectangle (Cairo's clip + rectangle + fill: the
// tiles' name band, a held button's progress).
func (c *Canvas) FillRectInRR(x, y, w, h float64, clip RR, col Color) {
	c.fillRect(x, y, w, h, col, &clip)
}

func (c *Canvas) fillRect(x, y, w, h float64, col Color, clip *RR) {
	if w <= 0 || h <= 0 {
		return
	}
	b := c.Img.Rect
	x0, x1 := max(b.Min.X, int(math.Floor(x))), min(b.Max.X, int(math.Ceil(x+w)))
	y0, y1 := max(b.Min.Y, int(math.Floor(y))), min(b.Max.Y, int(math.Ceil(y+h)))
	if x0 >= x1 || y0 >= y1 {
		return
	}
	s := c.src(col)
	var rr rrGeom
	if clip != nil {
		rr = clip.geom()
	}
	for py := y0; py < y1; py++ {
		cy := overlap(y, y+h, float64(py), float64(py+1))
		if clip == nil {
			// Interior columns at full horizontal coverage: one span
			ix0, ix1 := int(math.Ceil(x)), int(math.Floor(x+w))
			ix0, ix1 = max(ix0, x0), min(ix1, x1)
			if ix0 < ix1 {
				c.span(py, ix0, ix1, s.scaled(float32(cy)))
			}
			for px := x0; px < x1; px++ {
				if px >= ix0 && px < ix1 {
					continue
				}
				cv := cy * overlap(x, x+w, float64(px), float64(px+1))
				c.over(c.Img.PixOffset(px, py), s.scaled(float32(cv)))
			}
			continue
		}
		for px := x0; px < x1; px++ {
			cv := cy * overlap(x, x+w, float64(px), float64(px+1)) * rr.fillCov(float64(px)+0.5, float64(py)+0.5)
			if cv > 0 {
				c.over(c.Img.PixOffset(px, py), s.scaled(float32(cv)))
			}
		}
	}
}

// RR is a rounded rectangle; the radius is clamped like Python's rr (≤ half of each side).
type RR struct{ X, Y, W, H, R float64 }

type rrGeom struct {
	cx, cy, hx, hy, r float64
}

func (q RR) geom() rrGeom {
	r := min(q.R, q.W/2, q.H/2)
	return rrGeom{cx: q.X + q.W/2, cy: q.Y + q.H/2, hx: q.W/2 - r, hy: q.H/2 - r, r: r}
}

// sd is the signed distance from (px, py) to the outline (negative inside).
func (g rrGeom) sd(px, py float64) float64 {
	qx, qy := math.Abs(px-g.cx)-g.hx, math.Abs(py-g.cy)-g.hy
	out := math.Hypot(max(qx, 0), max(qy, 0))
	return out + min(max(qx, qy), 0) - g.r
}

func clamp01(v float64) float64 { return max(0, min(1, v)) }

func (g rrGeom) fillCov(px, py float64) float64 { return clamp01(0.5 - g.sd(px, py)) }

// strokeCov is the coverage of a band of half-width hw around the outline, at distance t from it.
func strokeCov(t, hw float64) float64 { return clamp01(min(2*hw, hw+0.5-t)) }

// FillRR fills a rounded rectangle.
func (c *Canvas) FillRR(q RR, col Color) {
	if q.W <= 0 || q.H <= 0 {
		return
	}
	g := q.geom()
	b := c.Img.Rect
	x0, x1 := max(b.Min.X, int(math.Floor(q.X))), min(b.Max.X, int(math.Ceil(q.X+q.W)))
	y0, y1 := max(b.Min.Y, int(math.Floor(q.Y))), min(b.Max.Y, int(math.Ceil(q.Y+q.H)))
	s := c.src(col)
	// Columns whose centres are within the straight part (|X - cx| ≤ hx) share the row's coverage when the
	// radius is ≥ 0.5 px (always on the screen): one span; the rest per pixel
	ix0, ix1 := int(math.Ceil(g.cx-g.hx-0.5)), int(math.Floor(g.cx+g.hx-0.5))+1
	if g.r < 0.5 {
		ix0, ix1 = 0, 0
	}
	ix0, ix1 = max(ix0, x0), min(ix1, x1)
	for py := y0; py < y1; py++ {
		fy := float64(py) + 0.5
		if ix0 < ix1 {
			c.span(py, ix0, ix1, s.scaled(float32(g.fillCov(g.cx, fy))))
		}
		for px := x0; px < x1; px++ {
			if px >= ix0 && px < ix1 {
				px = ix1 - 1
				continue
			}
			if cv := g.fillCov(float64(px)+0.5, fy); cv > 0 {
				c.over(c.Img.PixOffset(px, py), s.scaled(float32(cv)))
			}
		}
	}
}

// StrokeRR strokes the outline of a rounded rectangle with width lw (Python: rr(…); stroke()).
func (c *Canvas) StrokeRR(q RR, lw float64, col Color) { c.strokeRR(q, lw, col, 0, 0) }

// StrokeRRDash is StrokeRR with a dash pattern (on, off) starting where Cairo's rr path starts: the top
// edge's right end, going clockwise. Butt ends.
func (c *Canvas) StrokeRRDash(q RR, lw, on, off float64, col Color) { c.strokeRR(q, lw, col, on, off) }

func (c *Canvas) strokeRR(q RR, lw float64, col Color, on, off float64) {
	if q.W < 0 || q.H < 0 || lw <= 0 {
		return
	}
	g := q.geom()
	hw := lw / 2
	ext := hw + 1
	b := c.Img.Rect
	x0, x1 := max(b.Min.X, int(math.Floor(q.X-ext))), min(b.Max.X, int(math.Ceil(q.X+q.W+ext)))
	y0, y1 := max(b.Min.Y, int(math.Floor(q.Y-ext))), min(b.Max.Y, int(math.Ceil(q.Y+q.H+ext)))
	s := c.src(col)
	dash := on > 0 && off > 0
	var per perim
	if dash {
		per = newPerim(q, g)
	}
	pix := func(px, py int) {
		fx, fy := float64(px)+0.5, float64(py)+0.5
		cv := strokeCov(math.Abs(g.sd(fx, fy)), hw)
		if cv <= 0 {
			return
		}
		if dash {
			cv *= dashCov(per.at(fx, fy), on, off)
			if cv <= 0 {
				return
			}
		}
		c.over(c.Img.PixOffset(px, py), s.scaled(float32(cv)))
	}
	for py := y0; py < y1; py++ {
		fy := float64(py) + 0.5
		if math.Abs(fy-g.cy) <= g.hy && math.Abs(fy-g.cy) < q.H/2-ext {
			// A middle row: only near the left and right edges
			for px := x0; px < min(x1, int(math.Ceil(q.X+ext))); px++ {
				pix(px, py)
			}
			for px := max(x0, int(math.Floor(q.X+q.W-ext))); px < x1; px++ {
				pix(px, py)
			}
			continue
		}
		for px := x0; px < x1; px++ {
			pix(px, py)
		}
	}
}

// perim maps a point near a rounded rectangle's outline to its arc length along Cairo's rr path: the top-right
// arc from (x+w-r, y), the right edge, bottom-right arc, bottom edge, bottom-left arc, left edge, top-left
// arc, and the closing top edge.
type perim struct {
	q           RR
	g           rrGeom
	arc, ew, eh float64 // a quarter arc's length, the straight top/bottom and left/right lengths
}

func newPerim(q RR, g rrGeom) perim {
	return perim{q: q, g: g, arc: g.r * math.Pi / 2, ew: 2 * g.hx, eh: 2 * g.hy}
}

func (p perim) at(px, py float64) float64 {
	g := p.g
	dx, dy := px-g.cx, py-g.cy
	qx, qy := math.Abs(dx)-g.hx, math.Abs(dy)-g.hy
	left, top := g.cx-g.hx, g.cy-g.hy
	right, bottom := g.cx+g.hx, g.cy+g.hy
	if qx > 0 && qy > 0 { // a corner arc
		var ccx, ccy, start, base float64
		switch {
		case dx > 0 && dy < 0: // top-right: −π/2 → 0
			ccx, ccy, start, base = right, top, -math.Pi/2, 0
		case dx > 0: // bottom-right: 0 → π/2
			ccx, ccy, start, base = right, bottom, 0, p.arc+p.eh
		case dy > 0: // bottom-left: π/2 → π
			ccx, ccy, start, base = left, bottom, math.Pi/2, 2*p.arc+p.eh+p.ew
		default: // top-left: π → 3π/2
			ccx, ccy, start, base = left, top, math.Pi, 3*p.arc+2*p.eh+p.ew
		}
		a := math.Atan2(py-ccy, px-ccx) - start
		for a < -math.Pi {
			a += 2 * math.Pi
		}
		for a > math.Pi {
			a -= 2 * math.Pi
		}
		return base + g.r*a
	}
	if qx > qy { // a vertical edge
		if dx > 0 {
			return p.arc + (py - top)
		}
		return 3*p.arc + 2*p.eh + p.ew - (py - top) + 0 // left edge goes up: from bottom (s at bottom) to top
	}
	if dy > 0 { // bottom edge, right to left
		return 2*p.arc + p.eh + (right - px)
	}
	// Top edge: the closing segment, from (left, y) to (right, y)
	return 4*p.arc + 2*p.eh + p.ew + (px - left)
}

// dashCov is the coverage along the path at arc length s for a dash pattern starting "on" at s = 0.
func dashCov(s, on, off float64) float64 {
	per := on + off
	m := math.Mod(s, per)
	if m < 0 {
		m += per
	}
	if m < on {
		return clamp01(min(m, on-m) + 0.5)
	}
	return clamp01(0.5 - min(m-on, per-m))
}

// FillCircle fills a disc (Python: arc(cx, cy, r, 0, 2π); fill()).
func (c *Canvas) FillCircle(cx, cy, r float64, col Color) {
	if r <= 0 {
		return
	}
	b := c.Img.Rect
	x0, x1 := max(b.Min.X, int(math.Floor(cx-r-1))), min(b.Max.X, int(math.Ceil(cx+r+1)))
	y0, y1 := max(b.Min.Y, int(math.Floor(cy-r-1))), min(b.Max.Y, int(math.Ceil(cy+r+1)))
	s := c.src(col)
	small := r < 0.5
	for py := y0; py < y1; py++ {
		for px := x0; px < x1; px++ {
			d := math.Hypot(float64(px)+0.5-cx, float64(py)+0.5-cy) - r
			cv := clamp01(0.5 - d)
			if small {
				cv = min(cv, math.Pi*r*r)
			}
			if cv > 0 {
				c.over(c.Img.PixOffset(px, py), s.scaled(float32(cv)))
			}
		}
	}
}

// DrawImage composites a premultiplied image at an integer position (OVER).
func (c *Canvas) DrawImage(src *image.RGBA, x, y int) {
	r := src.Rect.Sub(src.Rect.Min).Add(image.Pt(x, y)).Intersect(c.Img.Rect)
	if r.Empty() {
		return
	}
	for py := r.Min.Y; py < r.Max.Y; py++ {
		si := src.PixOffset(src.Rect.Min.X+r.Min.X-x, src.Rect.Min.Y+py-y)
		di := c.Img.PixOffset(r.Min.X, py)
		n := r.Dx() * 4
		sp, dp := src.Pix[si:si+n:si+n], c.Img.Pix[di:di+n:di+n]
		for j := 0; j < n; j += 4 {
			sa := uint32(sp[j+3])
			switch sa {
			case 0:
				if sp[j] == 0 && sp[j+1] == 0 && sp[j+2] == 0 {
					continue
				}
			case 255:
				copy(dp[j:j+4], sp[j:j+4])
				continue
			}
			inv := 255 - sa
			dp[j] = uint8(uint32(sp[j]) + div255(uint32(dp[j])*inv))
			dp[j+1] = uint8(uint32(sp[j+1]) + div255(uint32(dp[j+1])*inv))
			dp[j+2] = uint8(uint32(sp[j+2]) + div255(uint32(dp[j+2])*inv))
			dp[j+3] = uint8(sa + div255(uint32(dp[j+3])*inv))
		}
	}
}

// CopyFrom copies a rectangle of src into the same place (Cairo's OPERATOR_SOURCE).
func (c *Canvas) CopyFrom(src *image.RGBA, r image.Rectangle) {
	r = r.Intersect(c.Img.Rect).Intersect(src.Rect)
	for py := r.Min.Y; py < r.Max.Y; py++ {
		i := c.Img.PixOffset(r.Min.X, py)
		j := src.PixOffset(r.Min.X, py)
		n := r.Dx() * 4
		copy(c.Img.Pix[i:i+n], src.Pix[j:j+n])
	}
}

// --- general paths (the icons) ---

type pt struct{ x, y float64 }

type subpath struct {
	pts    []pt
	closed bool
}

// Path is a Cairo-like path, flattened as it is built: move_to, line_to, arc (with the line from the current
// point to its start), close_path and new_sub_path.
type Path struct {
	subs   []subpath
	hasCur bool
}

func (p *Path) cur() *subpath { return &p.subs[len(p.subs)-1] }

// NewPath empties the path.
func (p *Path) NewPath() { p.subs = p.subs[:0]; p.hasCur = false }

// NewSubPath starts a subpath without a current point.
func (p *Path) NewSubPath() { p.hasCur = false }

func (p *Path) MoveTo(x, y float64) {
	p.subs = append(p.subs, subpath{pts: []pt{{x, y}}})
	p.hasCur = true
}

// LineTo without a current point is a MoveTo (Cairo).
func (p *Path) LineTo(x, y float64) {
	if !p.hasCur {
		p.MoveTo(x, y)
		return
	}
	s := p.cur()
	if s.closed { // after close_path, a new subpath starts at the closing point
		start := s.pts[0]
		p.subs = append(p.subs, subpath{pts: []pt{start}})
		s = p.cur()
	}
	s.pts = append(s.pts, pt{x, y})
}

// ClosePath closes the current subpath.
func (p *Path) ClosePath() {
	if p.hasCur {
		p.cur().closed = true
	}
}

// Arc is Cairo's arc: clockwise on screen from a1 to a2 (a2 raised by 2π until ≥ a1), with a line from the
// current point to its start. Flattened to 0.1 px (Cairo's default tolerance).
func (p *Path) Arc(cx, cy, r, a1, a2 float64) {
	for a2 < a1 {
		a2 += 2 * math.Pi
	}
	x0, y0 := cx+r*math.Cos(a1), cy+r*math.Sin(a1)
	if p.hasCur {
		p.LineTo(x0, y0)
	} else {
		p.MoveTo(x0, y0)
	}
	n := arcSegments(r, a2-a1)
	for i := 1; i <= n; i++ {
		a := a1 + (a2-a1)*float64(i)/float64(n)
		p.LineTo(cx+r*math.Cos(a), cy+r*math.Sin(a))
	}
}

func arcSegments(r, sweep float64) int {
	const tol = 0.1
	if r <= tol {
		return max(1, int(math.Ceil(sweep/(math.Pi/2))))
	}
	step := 2 * math.Acos(1-tol/r)
	return max(1, int(math.Ceil(sweep/step)))
}

func (p *Path) bounds() (x0, y0, x1, y1 float64) {
	x0, y0, x1, y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, s := range p.subs {
		for _, q := range s.pts {
			x0, y0, x1, y1 = min(x0, q.x), min(y0, q.y), max(x1, q.x), max(y1, q.y)
		}
	}
	return
}

// Fill fills the path (non-zero rule for paths without overlaps) and empties it.
func (c *Canvas) Fill(p *Path, col Color) {
	defer p.NewPath()
	bx0, by0, bx1, by1 := p.bounds()
	if bx0 > bx1 {
		return
	}
	r := image.Rect(int(math.Floor(bx0)), int(math.Floor(by0)), int(math.Ceil(bx1)), int(math.Ceil(by1))).Intersect(c.Img.Rect)
	if r.Empty() {
		return
	}
	c.ras.Reset(r.Dx(), r.Dy())
	ox, oy := float32(r.Min.X), float32(r.Min.Y)
	for _, s := range p.subs {
		if len(s.pts) < 2 {
			continue
		}
		c.ras.MoveTo(float32(s.pts[0].x)-ox, float32(s.pts[0].y)-oy)
		for _, q := range s.pts[1:] {
			c.ras.LineTo(float32(q.x)-ox, float32(q.y)-oy)
		}
		c.ras.ClosePath()
	}
	if c.alpha == nil || c.alpha.Rect.Dx() < r.Dx() || c.alpha.Rect.Dy() < r.Dy() {
		c.alpha = image.NewAlpha(image.Rect(0, 0, max(r.Dx(), 64), max(r.Dy(), 64)))
	}
	m := c.alpha.SubImage(image.Rect(0, 0, r.Dx(), r.Dy())).(*image.Alpha)
	for y := 0; y < r.Dy(); y++ {
		clear(m.Pix[y*m.Stride : y*m.Stride+r.Dx()])
	}
	c.ras.Draw(m, m.Rect, image.Opaque, image.Point{})
	s := c.src(col)
	for y := 0; y < r.Dy(); y++ {
		for x := 0; x < r.Dx(); x++ {
			if a := m.Pix[y*m.Stride+x]; a > 0 {
				c.over(c.Img.PixOffset(r.Min.X+x, r.Min.Y+y), s.scaled(float32(a)/255))
			}
		}
	}
}

// Stroke strokes the path with round caps and joins (the icons) and empties it: every segment is a capsule
// and the coverage is the union (the largest), so overlaps never darken.
func (c *Canvas) Stroke(p *Path, lw float64, col Color) {
	defer p.NewPath()
	bx0, by0, bx1, by1 := p.bounds()
	if bx0 > bx1 {
		return
	}
	hw := lw / 2
	r := image.Rect(int(math.Floor(bx0-hw-1)), int(math.Floor(by0-hw-1)), int(math.Ceil(bx1+hw+1)), int(math.Ceil(by1+hw+1))).
		Intersect(c.Img.Rect)
	if r.Empty() {
		return
	}
	n := r.Dx() * r.Dy()
	if cap(c.cov) < n {
		c.cov = make([]float32, n)
	}
	cov := c.cov[:n]
	clear(cov)
	seg := func(a, b pt) {
		sx0, sx1 := int(math.Floor(min(a.x, b.x)-hw-1)), int(math.Ceil(max(a.x, b.x)+hw+1))
		sy0, sy1 := int(math.Floor(min(a.y, b.y)-hw-1)), int(math.Ceil(max(a.y, b.y)+hw+1))
		sx0, sx1 = max(sx0, r.Min.X), min(sx1, r.Max.X)
		sy0, sy1 = max(sy0, r.Min.Y), min(sy1, r.Max.Y)
		dx, dy := b.x-a.x, b.y-a.y
		l2 := dx*dx + dy*dy
		for py := sy0; py < sy1; py++ {
			for px := sx0; px < sx1; px++ {
				fx, fy := float64(px)+0.5, float64(py)+0.5
				t := 0.0
				if l2 > 0 {
					t = clamp01(((fx-a.x)*dx + (fy-a.y)*dy) / l2)
				}
				d := math.Hypot(fx-(a.x+t*dx), fy-(a.y+t*dy))
				v := float32(strokeCov(d, hw))
				k := (py-r.Min.Y)*r.Dx() + px - r.Min.X
				if v > cov[k] {
					cov[k] = v
				}
			}
		}
	}
	for _, s := range p.subs {
		if len(s.pts) == 1 {
			seg(s.pts[0], s.pts[0])
		}
		for i := 1; i < len(s.pts); i++ {
			seg(s.pts[i-1], s.pts[i])
		}
		if s.closed && len(s.pts) > 1 {
			seg(s.pts[len(s.pts)-1], s.pts[0])
		}
	}
	sc := c.src(col)
	for y := 0; y < r.Dy(); y++ {
		for x := 0; x < r.Dx(); x++ {
			if v := cov[y*r.Dx()+x]; v > 0 {
				c.over(c.Img.PixOffset(r.Min.X+x, r.Min.Y+y), sc.scaled(v))
			}
		}
	}
}
