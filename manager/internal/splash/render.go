package splash

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
	"golang.org/x/sys/unix"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// Logo for dark backgrounds from the landing and the app (eirl-landing/public/images/logo/logo-dark.png,
// 742×155). The SVGs in that folder come from the TailAdmin template: they are not the logo.
//
//go:embed assets/logo.png
var logoPNG []byte

// eirl theme colours (the encoder web's, manager/web/static/app.css)
var (
	colBackground = color.RGBA{0x0c, 0x11, 0x1d, 0xff} // gray-950
	colTrack      = color.RGBA{0x1d, 0x29, 0x39, 0xff} // gray-800
	colAccent     = color.RGBA{0x46, 0x5f, 0xff, 0xff} // brand-500
	colText       = color.RGBA{0x98, 0xa2, 0xb3, 0xff} // gray-400
)

// Scene: what is shown.
type Scene string

const (
	SceneBoot     Scene = "boot"     // logo + animated bar, during boot
	SceneReady    Scene = "ready"    // still logo: what stays when boot finishes (D2)
	SceneReboot   Scene = "reboot"   // logo + "Reiniciando…" (restarting)
	ScenePoweroff Scene = "poweroff" // logo + "Apagando…" (shutting down)
)

// sceneText: the catalog key of the text shown under the logo, rendered in Renderer.Lang (the device language).
var sceneText = map[Scene]string{SceneReboot: "splash.rebooting", ScenePoweroff: "splash.powering_off"}

// DeviceLanguage: the "language" of the app's device.json (SETUP.md §2), read as data: a regular file of at most
// 64 KB; "" if missing or unreadable (the text then falls back to English). The splash runs as root and the
// directory is writable by its group: no symlink is followed and the open never blocks (a FIFO under that name
// would hang the reboot and poweroff splashes).
func DeviceLanguage(path string) string {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return ""
	}
	var d struct {
		Language string `json:"language"`
	}
	if json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&d) != nil || !i18n.Supported(d.Language) {
		return ""
	}
	return d.Language
}

func ParseScene(s string) (Scene, error) {
	switch sc := Scene(s); sc {
	case SceneBoot, SceneReady, SceneReboot, ScenePoweroff:
		return sc, nil
	}
	return "", fmt.Errorf("unknown scene %q (boot, ready, reboot, poweroff)", s)
}

// BarPeriod: one sweep of the bright segment of the loading bar.
const BarPeriod = 1600 * time.Millisecond

// Renderer draws the scenes at whatever resolution the screen has (it changes: each panel announces
// its mode and the test one alternates between two EDIDs).
type Renderer struct {
	logo image.Image
	font *opentype.Font

	w, h       int
	logoRect   image.Rectangle
	barRect    image.Rectangle
	textBase   int
	textSizePx float64

	// Lang: the language of the scene texts (es | en | fr; "" = English)
	Lang string
}

func NewRenderer() (*Renderer, error) {
	logo, err := png.Decode(bytes.NewReader(logoPNG))
	if err != nil {
		return nil, fmt.Errorf("logo: %w", err)
	}
	f, err := opentype.Parse(gomedium.TTF)
	if err != nil {
		return nil, fmt.Errorf("font: %w", err)
	}
	return &Renderer{logo: logo, font: f}, nil
}

// Layout recomputes the positions for a w×h screen.
func (r *Renderer) Layout(w, h int) {
	r.w, r.h = w, h
	fw, fh := float64(w), float64(h)
	lb := r.logo.Bounds()
	ar := float64(lb.Dx()) / float64(lb.Dy())
	// Logo at ~15 % of the height and at most 60 % of the width, slightly above the centre so the
	// whole (logo + bar or text) looks centred.
	lh := 0.15 * fh
	lw := lh * ar
	if lw > 0.6*fw {
		lw = 0.6 * fw
		lh = lw / ar
	}
	x0, y0 := (fw-lw)/2, (fh-lh)/2-0.04*fh
	r.logoRect = image.Rect(iround(x0), iround(y0), iround(x0+lw), iround(y0+lh))

	bw := iround(lw * 0.4)
	bh := max(2, iround(fh/150))
	by := r.logoRect.Max.Y + iround(0.08*fh)
	r.barRect = image.Rect((w-bw)/2, by, (w-bw)/2+bw, by+bh)

	r.textSizePx = math.Max(12, 0.035*fh)
	r.textBase = r.logoRect.Max.Y + iround(0.11*fh)
}

func (r *Renderer) Size() (int, int)            { return r.w, r.h }
func (r *Renderer) LogoBounds() image.Rectangle { return r.logoRect }

// BarBounds: the only region that changes in the boot animation.
func (r *Renderer) BarBounds() image.Rectangle { return r.barRect }

// Frame draws the whole scene, without the animated bar (DrawBar puts it on top).
func (r *Renderer) Frame(s Scene) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, r.w, r.h))
	draw.Draw(img, img.Bounds(), image.NewUniform(colBackground), image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(img, r.logoRect, r.logo, r.logo.Bounds(), xdraw.Over, nil)
	if key := sceneText[s]; key != "" {
		r.drawText(img, i18n.Msg(r.Lang, key))
	}
	return img
}

// DrawBar paints the loading bar at instant t: the track and a bright segment sweeping along it.
// It only touches BarBounds (dst must contain it).
func (r *Renderer) DrawBar(dst draw.Image, t time.Duration) {
	b := r.barRect
	draw.Draw(dst, b, image.NewUniform(colBackground), image.Point{}, draw.Src)
	fillRounded(dst, b, float32(b.Min.X), float32(b.Max.X), colTrack)

	p := float64(t%BarPeriod) / float64(BarPeriod)
	p = p * p * (3 - 2*p) // smoothstep: eases in and out
	seg := 0.3 * float64(b.Dx())
	x0 := float64(b.Min.X) - seg + p*(float64(b.Dx())+seg)
	x1 := math.Min(x0+seg, float64(b.Max.X))
	x0 = math.Max(x0, float64(b.Min.X))
	if x1-x0 >= 0.5 {
		fillRounded(dst, b, float32(x0), float32(x1), colAccent)
	}
}

func (r *Renderer) drawText(img *image.RGBA, txt string) {
	face, err := opentype.NewFace(r.font, &opentype.FaceOptions{Size: r.textSizePx, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return
	}
	defer face.Close()
	d := font.Drawer{Dst: img, Src: image.NewUniform(colText), Face: face}
	w := d.MeasureString(txt)
	d.Dot = fixed.Point26_6{X: fixed.I(r.w/2) - w/2, Y: fixed.I(r.textBase)}
	d.DrawString(txt)
}

// fillRounded: a rectangle with rounded ends (radius = half the height) between x0 and x1, the full
// height of b, antialiased.
func fillRounded(dst draw.Image, b image.Rectangle, x0, x1 float32, c color.Color) {
	h := float32(b.Dy())
	x0 -= float32(b.Min.X)
	x1 -= float32(b.Min.X)
	rad := min(h/2, (x1-x0)/2)
	const k = 0.5523 // quarter-circle approximation with a cubic
	z := vector.NewRasterizer(b.Dx(), b.Dy())
	z.MoveTo(x0+rad, 0)
	z.LineTo(x1-rad, 0)
	z.CubeTo(x1-rad+k*rad, 0, x1, rad-k*rad, x1, rad)
	z.LineTo(x1, h-rad)
	z.CubeTo(x1, h-rad+k*rad, x1-rad+k*rad, h, x1-rad, h)
	z.LineTo(x0+rad, h)
	z.CubeTo(x0+rad-k*rad, h, x0, h-rad+k*rad, x0, h-rad)
	z.LineTo(x0, rad)
	z.CubeTo(x0, rad-k*rad, x0+rad-k*rad, 0, x0+rad, 0)
	z.ClosePath()
	z.Draw(dst, b, image.NewUniform(c), image.Point{})
}

func iround(f float64) int { return int(math.Round(f)) }
