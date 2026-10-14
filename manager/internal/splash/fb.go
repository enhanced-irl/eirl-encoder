package splash

import "fmt"

// Geometry: the framebuffer's current mode. If it changes (screen plugged in later, another EDID),
// everything is repainted.
type Geometry struct {
	Width, Height int
	Stride        int // bytes per row (line_length)
	Offset        int // byte of the first visible pixel (panning)
	MemLen        int // mappable memory (smem_len)
	Format        Format
}

func (g Geometry) Validate() error {
	if g.Width <= 0 || g.Height <= 0 {
		return fmt.Errorf("invalid mode %dx%d (screen without a mode?)", g.Width, g.Height)
	}
	if err := g.Format.Validate(); err != nil {
		return err
	}
	if g.Stride < g.Width*g.Format.BytesPerPixel() {
		return fmt.Errorf("stride %d smaller than a row of %d px", g.Stride, g.Width)
	}
	if end := g.Offset + (g.Height-1)*g.Stride + g.Width*g.Format.BytesPerPixel(); end > g.MemLen {
		return fmt.Errorf("the visible area (%d bytes) does not fit in the framebuffer memory (%d)", end, g.MemLen)
	}
	return nil
}

func (g Geometry) String() string {
	f := g.Format
	return fmt.Sprintf("%dx%d %dbpp stride=%d R%d/%d G%d/%d B%d/%d A%d/%d", g.Width, g.Height, f.BitsPerPixel,
		g.Stride, f.R.Offset, f.R.Length, f.G.Offset, f.G.Length, f.B.Offset, f.B.Length, f.A.Offset, f.A.Length)
}
