package usbsignal

import (
	"bytes"
	"image"
	"image/jpeg"
	"sync"
	"time"
)

// The generic way, for any capture card that does not say whether it has a signal (SOURCES.md §9.2): what
// it sends without one. No card found reports it in a standard way (UVC's analog lock status is offered by
// none, and Linux 4.9 does not expose it), and without a signal each sends something else: black (the
// MS2109), blue, its own "No signal" picture (the MS2130, Magewell), or nothing at all (then the watchdog
// already shows the slate). What those pictures have in common: the SAME picture again and again, and
// nearly uniform. A camera never sends two identical pictures (its sensor's noise), even in the dark.
//
// It works on the source's thumbnail (the engine makes one a second for the web, 320x180 JPEG): two
// identical thumbnails are two identical pictures, and it costs one comparison a second (a picture is
// decoded once, when it repeats). Its blind spot: an HDMI source that really sends a still, nearly uniform
// picture (a console's black loading screen, a blank desktop) for StillLimit gets the slate until it
// changes.
var (
	StillLimit = 5 * time.Second // still and uniform this long: no signal
	thumbStale = 3 * time.Second // no thumbnail for this long: it cannot tell (the source gives no frames)
)

// Flatness: a picture is nearly uniform when this share of its pixels is within flatSpread of the most
// common luma (a black or blue screen, or one with a small "No signal" box).
const (
	flatShare  = 0.90
	flatSpread = 8
)

// Picture watches the thumbnails of one slot. Add comes from the thumbnails' goroutine, Signal from the
// source's.
type Picture struct {
	mu        sync.Mutex
	prev      []byte    // the last thumbnail
	prevAt    time.Time // when that picture first came
	flat      int8      // whether prev is nearly uniform: -1 not known yet (decoded when it repeats), 0 no, 1 yes
	stillFrom time.Time // the first of the identical, uniform thumbnails in a row (zero: the last one is not)
	last      time.Time // the last thumbnail
}

// NewPicture is a watch with no thumbnail yet.
func NewPicture() *Picture { return &Picture{flat: -1} }

// Add takes the slot's latest thumbnail (a JPEG).
func (p *Picture) Add(jpg []byte, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = now
	if p.prev == nil || !bytes.Equal(jpg, p.prev) {
		p.prev = append(p.prev[:0], jpg...)
		p.prevAt, p.flat, p.stillFrom = now, -1, time.Time{}
		return
	}
	if p.flat < 0 {
		p.flat = 0
		if flat(jpg) {
			p.flat = 1
		}
	}
	if p.flat == 1 && p.stillFrom.IsZero() {
		p.stillFrom = p.prevAt
	}
}

// Reset forgets the thumbnails: a new opening (or another source) in the slot.
func (p *Picture) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prev, p.prevAt, p.flat, p.stillFrom, p.last = nil, time.Time{}, -1, time.Time{}, time.Time{}
}

// Signal: false once the picture has been still and nearly uniform for StillLimit. True when it cannot
// tell (no recent thumbnail).
func (p *Picture) Signal(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stillFrom.IsZero() || now.Sub(p.last) > thumbStale {
		return true
	}
	return now.Sub(p.stillFrom) < StillLimit
}

// flat decodes a thumbnail and says whether it is nearly uniform (by its luma). One that does not decode
// is not.
func flat(jpg []byte) bool {
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return false
	}
	var hist [256]int
	n := 0
	switch m := img.(type) {
	case *image.YCbCr:
		b := m.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			row := m.Y[(y-m.Rect.Min.Y)*m.YStride:]
			for x := 0; x < b.Dx(); x++ {
				hist[row[x]]++
			}
		}
		n = b.Dx() * b.Dy()
	case *image.Gray:
		for _, v := range m.Pix {
			hist[v]++
		}
		n = len(m.Pix)
	default:
		return false
	}
	if n == 0 {
		return false
	}
	mode := 0
	for v := range hist {
		if hist[v] > hist[mode] {
			mode = v
		}
	}
	near := 0
	for v := max(0, mode-flatSpread); v <= min(255, mode+flatSpread); v++ {
		near += hist[v]
	}
	return float64(near) >= flatShare*float64(n)
}
