// Package splash: the EnhancedIRL boot splash on the encoder's HDMI output (see SPLASH.md).
// It paints straight into the framebuffer (/dev/fb0, tegra_fb): no X, no DRM and no Plymouth.
package splash

import (
	"fmt"
	"image"
)

// Channel: position of a colour inside the framebuffer pixel (struct fb_bitfield).
type Channel struct{ Offset, Length uint32 }

// Format: the framebuffer's pixel format (from fb_var_screeninfo). A.Length == 0: no alpha.
type Format struct {
	BitsPerPixel uint32
	R, G, B, A   Channel
}

func (f Format) Validate() error {
	switch f.BitsPerPixel {
	case 16, 24, 32:
	default:
		return fmt.Errorf("%d bpp not supported", f.BitsPerPixel)
	}
	for _, c := range []Channel{f.R, f.G, f.B} {
		if c.Length == 0 || c.Length > 8 || c.Offset+c.Length > f.BitsPerPixel {
			return fmt.Errorf("channel %+v not supported at %d bpp", c, f.BitsPerPixel)
		}
	}
	if f.A.Length > 8 || (f.A.Length > 0 && f.A.Offset+f.A.Length > f.BitsPerPixel) {
		return fmt.Errorf("alpha %+v not supported at %d bpp", f.A, f.BitsPerPixel)
	}
	return nil
}

func (f Format) BytesPerPixel() int { return int(f.BitsPerPixel / 8) }

// pack: opaque colour → pixel value. Alpha, if any, at its maximum: tegradc uses it to blend the
// window with whatever is below.
func (f Format) pack(r, g, b uint8) uint32 {
	v := uint32(r)>>(8-f.R.Length)<<f.R.Offset |
		uint32(g)>>(8-f.G.Length)<<f.G.Offset |
		uint32(b)>>(8-f.B.Length)<<f.B.Offset
	if f.A.Length > 0 {
		v |= (1<<f.A.Length - 1) << f.A.Offset
	}
	return v
}

// Encode converts region r of img (opaque) to the framebuffer format: dst gets the rows back to
// back, each stride bytes long, starting at pixel r.Min.
func (f Format) Encode(dst []byte, stride int, img *image.RGBA, r image.Rectangle) {
	bpp := f.BytesPerPixel()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := dst[(y-r.Min.Y)*stride:]
		src := img.Pix[img.PixOffset(r.Min.X, y):]
		for x := 0; x < r.Dx(); x++ {
			v := f.pack(src[4*x], src[4*x+1], src[4*x+2])
			o := x * bpp
			row[o] = byte(v)
			row[o+1] = byte(v >> 8)
			if bpp > 2 {
				row[o+2] = byte(v >> 16)
			}
			if bpp > 3 {
				row[o+3] = byte(v >> 24)
			}
		}
	}
}
