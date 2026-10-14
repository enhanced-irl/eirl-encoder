package ui

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// BoardFontDir is where the board has DejaVu (Cairo's 'Sans' and 'Monospace' resolve to it there).
const BoardFontDir = "/usr/share/fonts/truetype/dejavu"

// Font files: Cairo 'Sans' → DejaVuSans(-Bold), 'Monospace' bold → DejaVuSansMono-Bold.
var fontFiles = [...]string{"DejaVuSans.ttf", "DejaVuSans-Bold.ttf", "DejaVuSansMono-Bold.ttf"}

// FontStyle is one of the three faces the screen uses.
type FontStyle int

const (
	Sans FontStyle = iota
	SansBold
	MonoBold
)

// Fonts holds the faces and a cache of rendered glyphs: text is drawn with the rules of the board's cairo
// 1.15 + FreeType (whole-pixel glyph positions, advances rounded: hint metrics on, no kerning in show_text,
// and fontconfig's hintslight: vertical-only hinting, approximated by snapping the font's blue zones —
// baseline, x-height, cap height, ascender, descender — to whole pixels). A glyph is rasterised once per
// face and size, not on every drawing.
type Fonts struct {
	fonts [3]*sfnt.Font
	faces map[faceKey]*face
	buf   sfnt.Buffer
	ras   vector.Rasterizer
}

type faceKey struct {
	style FontStyle
	size  fixed.Int26_6
}

type face struct {
	fs     *Fonts
	f      *sfnt.Font
	ppem   fixed.Int26_6
	zones  []zone // sorted by y (pixels, y down: negative above the baseline)
	glyphs map[rune]*glyph
}

// zone is a blue zone: its height in the outline and where it is snapped.
type zone struct{ y, to float64 }

type glyph struct {
	mask         *image.Alpha // nil for an empty glyph (space)
	off          image.Point  // mask's top-left relative to the pen
	adv          int          // advance, whole pixels
	inkX0, inkX1 float64      // ink extent relative to the pen (Cairo's text_extents, hint metrics: floor/ceil)
}

// LoadFonts reads the DejaVu files from the first directory that has them all: dirs in order, then
// BoardFontDir.
func LoadFonts(dirs ...string) (*Fonts, error) {
	var errs []error
	for _, d := range append(dirs, BoardFontDir) {
		if d == "" {
			continue
		}
		fs := &Fonts{faces: map[faceKey]*face{}}
		var err error
		for i, name := range fontFiles {
			var b []byte
			if b, err = os.ReadFile(filepath.Join(d, name)); err != nil {
				break
			}
			if fs.fonts[i], err = sfnt.Parse(b); err != nil {
				err = fmt.Errorf("%s: %w", name, err)
				break
			}
		}
		if err == nil {
			return fs, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

func (fs *Fonts) face(style FontStyle, size float64) *face {
	k := faceKey{style, fixed.Int26_6(math.Round(size * 64))}
	if f := fs.faces[k]; f != nil {
		return f
	}
	if len(fs.faces) > 96 { // sizes are a handful per screen size: never in practice
		clear(fs.faces)
	}
	f := &face{fs: fs, f: fs.fonts[style], ppem: k.size, glyphs: map[rune]*glyph{}}
	f.zones = f.blueZones()
	fs.faces[k] = f
	return f
}

// blueZones approximates FreeType's light auto-hinter (hintslight, what fontconfig gives Cairo on the board):
// first the vertical scale changes so the x-height's overshoot (the top of 'o') lands on a whole pixel,
// rounding up from 24/64 of a pixel (af_latin_metrics_scale_dim, threshold 40); then the flat zones (baseline,
// x-height, cap height, ascender, descender) are rounded to pixels and the overshoots (round letters) sit
// on them, or half a pixel or one beyond, as their distance says.
func (f *face) blueZones() []zone {
	fs := f.fs
	top := func(r rune, bottom bool) (float64, bool) {
		idx, err := f.f.GlyphIndex(&fs.buf, r)
		if err != nil || idx == 0 {
			return 0, false
		}
		b, _, err := f.f.GlyphBounds(&fs.buf, idx, f.ppem, font.HintingNone)
		if err != nil {
			return 0, false
		}
		if bottom {
			return float64(b.Max.Y) / 64, true
		}
		return float64(b.Min.Y) / 64, true
	}
	xs, okS := top('o', false)
	k := 1.0
	if okS && xs < -1 {
		scaled := -xs * 64
		fitted := math.Floor((scaled+40)/64) * 64
		k = fitted / scaled
	}
	var zs []zone
	add := func(flat rune, flatBottom bool, shoot rune, shootBottom bool) {
		yf, ok := top(flat, flatBottom)
		if !ok {
			return
		}
		to := math.Round(yf * k)
		zs = append(zs, zone{yf, to})
		if shoot == 0 {
			return
		}
		ys, ok := top(shoot, shootBottom)
		if !ok {
			return
		}
		d := math.Abs((ys - yf) * k)
		var snap float64
		switch {
		case d < 0.5:
			snap = 0
		case d < 0.75:
			snap = 0.5
		default:
			snap = 1
		}
		if ys < yf {
			snap = -snap
		}
		zs = append(zs, zone{ys, to + snap})
	}
	zs = append(zs, zone{0, 0})
	if ys, ok := top('o', true); ok && ys > 0 { // the baseline's overshoot
		d := ys * k
		snap := 0.0
		if d >= 0.75 {
			snap = 1
		} else if d >= 0.5 {
			snap = 0.5
		}
		zs = append(zs, zone{ys, snap})
	}
	add('x', false, 'o', false)
	add('H', false, 'O', false)
	add('d', false, 0, false)
	add('p', true, 0, false)
	sort.Slice(zs, func(i, j int) bool { return zs[i].y < zs[j].y })
	out := zs[:0]
	for _, z := range zs {
		if n := len(out); n > 0 && out[n-1].y == z.y {
			continue
		}
		out = append(out, z)
	}
	return out
}

// snapY maps an outline y (pixels) through the snapped blue zones: linear between two zones, shifted with
// the nearest one outside them.
func (f *face) snapY(y float64) float64 {
	z := f.zones
	if len(z) == 0 {
		return y
	}
	if y <= z[0].y {
		return y + z[0].to - z[0].y
	}
	for i := 1; i < len(z); i++ {
		if y <= z[i].y {
			a, b := z[i-1], z[i]
			return a.to + (y-a.y)*(b.to-a.to)/(b.y-a.y)
		}
	}
	l := z[len(z)-1]
	return y + l.to - l.y
}

func (f *face) glyph(r rune) *glyph {
	if g := f.glyphs[r]; g != nil {
		return g
	}
	g := &glyph{}
	f.glyphs[r] = g
	fs := f.fs
	idx, err := f.f.GlyphIndex(&fs.buf, r)
	if err != nil {
		return g
	}
	if adv, err := f.f.GlyphAdvance(&fs.buf, idx, f.ppem, font.HintingNone); err == nil {
		g.adv = adv.Round()
	}
	b, _, err := f.f.GlyphBounds(&fs.buf, idx, f.ppem, font.HintingNone)
	if err != nil || b.Max.X <= b.Min.X {
		return g
	}
	g.inkX0, g.inkX1 = math.Floor(float64(b.Min.X)/64), math.Ceil(float64(b.Max.X)/64)
	segs, err := f.f.LoadGlyph(&fs.buf, idx, f.ppem, nil)
	if err != nil || len(segs) == 0 {
		return g
	}
	pt := func(p fixed.Point26_6) (float64, float64) { return float64(p.X) / 64, f.snapY(float64(p.Y) / 64) }
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, s := range segs {
		n := map[sfnt.SegmentOp]int{sfnt.SegmentOpMoveTo: 1, sfnt.SegmentOpLineTo: 1, sfnt.SegmentOpQuadTo: 2,
			sfnt.SegmentOpCubeTo: 3}[s.Op]
		for _, p := range s.Args[:n] {
			x, y := pt(p)
			x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
		}
	}
	rect := image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1)), int(math.Ceil(y1)))
	if rect.Empty() {
		return g
	}
	ox, oy := float64(rect.Min.X), float64(rect.Min.Y)
	fs.ras.Reset(rect.Dx(), rect.Dy())
	at := func(p fixed.Point26_6) (float32, float32) { x, y := pt(p); return float32(x - ox), float32(y - oy) }
	for _, s := range segs {
		switch s.Op {
		case sfnt.SegmentOpMoveTo:
			fs.ras.MoveTo(at(s.Args[0]))
		case sfnt.SegmentOpLineTo:
			fs.ras.LineTo(at(s.Args[0]))
		case sfnt.SegmentOpQuadTo:
			ax, ay := at(s.Args[0])
			bx, by := at(s.Args[1])
			fs.ras.QuadTo(ax, ay, bx, by)
		case sfnt.SegmentOpCubeTo:
			ax, ay := at(s.Args[0])
			bx, by := at(s.Args[1])
			cx, cy := at(s.Args[2])
			fs.ras.CubeTo(ax, ay, bx, by, cx, cy)
		}
	}
	fs.ras.ClosePath()
	a := image.NewAlpha(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	fs.ras.Draw(a, a.Rect, image.Opaque, image.Point{})
	g.mask, g.off = a, rect.Min
	return g
}

// Extents is Cairo's text_extents: the advance and the ink width.
func (fs *Fonts) Extents(s string, size float64, style FontStyle) (adv, ink float64) {
	f := fs.face(style, size)
	if f == nil {
		return 0, 0
	}
	x := 0
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, r := range s {
		g := f.glyph(r)
		if g.inkX1 > g.inkX0 {
			lo, hi = min(lo, float64(x)+g.inkX0), max(hi, float64(x)+g.inkX1)
		}
		x += g.adv
	}
	if hi < lo {
		return float64(x), 0
	}
	return float64(x), hi - lo
}

// drawnText sees every text drawn (tests: nothing typed on the keyboard is ever drawn as text).
var drawnText func(s string)

// Text draws s with its baseline at y and its pen starting at x (Cairo's move_to + show_text) and returns
// the advance.
func (c *Canvas) Text(x, y float64, s string, size float64, style FontStyle, col Color) float64 {
	if drawnText != nil {
		drawnText(s)
	}
	f := c.Fonts.face(style, size)
	if f == nil {
		return 0
	}
	r, g, b, a := premul(col)
	src := image.NewUniform(color.RGBA{uint8(r), uint8(g), uint8(b), uint8(a)})
	px, py := int(math.Round(x)), int(math.Round(y))
	adv := 0
	for _, ch := range s {
		gl := f.glyph(ch)
		if gl.mask != nil {
			at := gl.mask.Rect.Add(image.Pt(px+adv, py).Add(gl.off))
			draw.DrawMask(c.Img, at, src, image.Point{}, gl.mask, image.Point{}, draw.Over)
		}
		adv += gl.adv
	}
	return float64(adv)
}

// GlyphCount is how many glyphs the cache holds (tests).
func (fs *Fonts) GlyphCount() int {
	n := 0
	for _, f := range fs.faces {
		n += len(f.glyphs)
	}
	return n
}

// The rune-level face check (tests): whether the style has a glyph for r.
func (fs *Fonts) has(style FontStyle, r rune) bool {
	i, err := fs.fonts[style].GlyphIndex(&fs.buf, r)
	return err == nil && i != 0
}
