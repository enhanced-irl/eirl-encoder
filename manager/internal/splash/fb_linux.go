//go:build linux

package splash

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ioctls from <linux/fb.h> (fixed numbers, no encoded size)
const (
	fbioGetVScreenInfo = 0x4600
	fbioGetFScreenInfo = 0x4602
	fbioPanDisplay     = 0x4606
	fbioPutCon2FBMap   = 0x4610
	fbioBlank          = 0x4611
	fbVisualTrueColor  = 2
	fbBlankUnblank     = 0
)

// FB: memory-mapped framebuffer.
type FB struct {
	f   *os.File
	mem []byte
	geo Geometry
	row []byte
}

func Open(path string) (*FB, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	fb := &FB{f: f}
	if fb.geo, err = readGeometry(f.Fd()); err != nil {
		f.Close()
		return nil, err
	}
	if err := fb.mmap(); err != nil {
		f.Close()
		return nil, err
	}
	return fb, nil
}

func (fb *FB) mmap() error {
	mem, err := unix.Mmap(int(fb.f.Fd()), 0, fb.geo.MemLen, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("mmap: %w", err)
	}
	fb.mem = mem
	return nil
}

func (fb *FB) Geometry() Geometry { return fb.geo }

// Refresh re-reads the mode: true if it changed (it redoes the mmap if needed, and everything must
// be repainted). An error keeps the previous geometry: the caller does not paint until it recovers.
func (fb *FB) Refresh() (bool, error) {
	g, err := readGeometry(fb.f.Fd())
	if err != nil {
		return false, err
	}
	if g == fb.geo {
		return false, nil
	}
	if g.MemLen != fb.geo.MemLen {
		_ = unix.Munmap(fb.mem)
		fb.mem = nil
		fb.geo = g
		if err := fb.mmap(); err != nil {
			return false, err
		}
	}
	fb.geo = g
	return true, nil
}

// Blit copies region r of img (opaque, in screen coordinates) to the framebuffer, row by row.
func (fb *FB) Blit(img *image.RGBA, r image.Rectangle) {
	if fb.mem == nil {
		return
	}
	g := fb.geo
	r = r.Intersect(image.Rect(0, 0, g.Width, g.Height)).Intersect(img.Bounds())
	bpp := g.Format.BytesPerPixel()
	n := r.Dx() * bpp
	if n <= 0 {
		return
	}
	if cap(fb.row) < n {
		fb.row = make([]byte, n)
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		g.Format.Encode(fb.row[:n], n, img, image.Rect(r.Min.X, y, r.Max.X, y+1))
		off := g.Offset + y*g.Stride + r.Min.X*bpp
		copy(fb.mem[off:off+n], fb.row[:n])
	}
}

// Show turns the output on if it is blanked and puts this framebuffer back on screen. Without the text
// console nobody else does it. The pan (to where it already is) brings back the framebuffer's window: the
// engine's screen takes that window and, since the HDMI output stays lit when it closes, leaves it switched off
// with the head on (MIXER.md §3, trap 27); an unblank alone does nothing then.
func (fb *FB) Show() error {
	fd := fb.f.Fd()
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, fbioBlank, fbBlankUnblank); e != 0 {
		return fmt.Errorf("FBIOBLANK: %w", e)
	}
	var v [40]uint32 // struct fb_var_screeninfo
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, fbioGetVScreenInfo, uintptr(unsafe.Pointer(&v[0]))); e != 0 {
		return fmt.Errorf("FBIOGET_VSCREENINFO: %w", e)
	}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, fbioPanDisplay, uintptr(unsafe.Pointer(&v[0]))); e != 0 {
		return fmt.Errorf("FBIOPAN_DISPLAY: %w", e)
	}
	return nil
}

// Close may be called more than once.
func (fb *FB) Close() error {
	if fb.mem != nil {
		_ = unix.Munmap(fb.mem)
		fb.mem = nil
	}
	if fb.f == nil {
		return nil
	}
	err := fb.f.Close()
	fb.f = nil
	return err
}

func readGeometry(fd uintptr) (Geometry, error) {
	var v [40]uint32 // struct fb_var_screeninfo: 40 × __u32
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, fbioGetVScreenInfo, uintptr(unsafe.Pointer(&v[0]))); e != 0 {
		return Geometry{}, fmt.Errorf("FBIOGET_VSCREENINFO: %w", e)
	}
	var fx [80]byte // struct fb_fix_screeninfo on arm64 (8-byte unsigned long)
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, fbioGetFScreenInfo, uintptr(unsafe.Pointer(&fx[0]))); e != 0 {
		return Geometry{}, fmt.Errorf("FBIOGET_FSCREENINFO: %w", e)
	}
	le := binary.LittleEndian
	if visual := le.Uint32(fx[36:]); visual != fbVisualTrueColor {
		return Geometry{}, fmt.Errorf("visual %d not supported (truecolor only)", visual)
	}
	g := Geometry{
		Width:  int(v[0]),
		Height: int(v[1]),
		Stride: int(le.Uint32(fx[48:])), // line_length
		MemLen: int(le.Uint32(fx[24:])), // smem_len
		Format: Format{
			BitsPerPixel: v[6],
			R:            Channel{v[8], v[9]},
			G:            Channel{v[11], v[12]},
			B:            Channel{v[14], v[15]},
			A:            Channel{v[17], v[18]},
		},
	}
	g.Offset = int(v[5])*g.Stride + int(v[4])*int(v[6])/8 // yoffset, xoffset (panning)
	return g, g.Validate()
}

// MapConsole hands the text console back to framebuffer path (FBIOPUT_CON2FBMAP). With fbcon=map:9
// nobody has it and the kernel binds it fully (do_fbcon_takeover): emergency mode and the login are
// visible and a keyboard works. It is the rescue when boot fails (D4).
func MapConsole(path string) error {
	n, err := strconv.Atoi(strings.TrimPrefix(path, "/dev/fb"))
	if err != nil {
		return fmt.Errorf("%s is not /dev/fbN", path)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	m := [2]uint32{1, uint32(n)} // struct fb_con2fbmap {console, framebuffer}; console 1 = tty1
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), fbioPutCon2FBMap, uintptr(unsafe.Pointer(&m[0]))); e != 0 {
		return errors.New("FBIOPUT_CON2FBMAP: " + e.Error())
	}
	return nil
}
