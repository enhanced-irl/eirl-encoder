package usbsignal

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"testing"
	"time"
)

// thumb is a 320x180 JPEG like the engine's thumbnails: a background, an optional box and noise.
func thumb(t *testing.T, bg color.YCbCr, box int, noise int, seed int64) []byte {
	t.Helper()
	img := image.NewYCbCr(image.Rect(0, 0, 320, 180), image.YCbCrSubsampleRatio420)
	r := rand.New(rand.NewSource(seed))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			v := int(bg.Y)
			if x < box && y < box {
				v = 235
			}
			if noise > 0 {
				v += r.Intn(2*noise+1) - noise
			}
			img.Y[y*img.YStride+x] = uint8(max(0, min(255, v)))
		}
	}
	for i := range img.Cb {
		img.Cb[i], img.Cr[i] = bg.Cb, bg.Cr
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

var (
	black = color.YCbCr{Y: 16, Cb: 128, Cr: 128}
	blue  = color.YCbCr{Y: 41, Cb: 240, Cr: 110}
	grey  = color.YCbCr{Y: 120, Cb: 128, Cr: 128}
)

func TestFlat(t *testing.T) {
	for _, tc := range []struct {
		what string
		jpg  []byte
		want bool
	}{
		{"black", thumb(t, black, 0, 0, 1), true},
		{"blue", thumb(t, blue, 0, 0, 1), true},
		{"black with a small No-signal box (5 %)", thumb(t, black, 54, 0, 1), true},
		{"a big box (25 %)", thumb(t, black, 120, 0, 1), false},
		{"a dark camera (noise)", thumb(t, black, 0, 20, 1), false},
		{"not a JPEG", []byte("nope"), false},
	} {
		if got := flat(tc.jpg); got != tc.want {
			t.Errorf("%s: flat %v", tc.what, got)
		}
	}
}

func TestPicture(t *testing.T) {
	t0 := time.Now()
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	p := NewPicture()
	if !p.Signal(t0) {
		t.Fatal("no thumbnail yet: it cannot tell")
	}
	// A card without a signal: the same black picture every second
	b := thumb(t, black, 0, 0, 1)
	for s := 0; s <= 5; s++ {
		p.Add(b, at(float64(s)))
	}
	if !p.Signal(at(4.9)) {
		t.Error("4.9 s still: not yet")
	}
	if p.Signal(at(5.5)) {
		t.Error("still and black for 5 s: no signal")
	}
	// The picture changes: signal at once
	p.Add(thumb(t, grey, 0, 20, 2), at(6))
	if !p.Signal(at(6)) {
		t.Error("a new picture is a signal")
	}
	// No thumbnails (no frames): it cannot tell
	for s := 7; s <= 13; s++ {
		p.Add(b, at(float64(s)))
	}
	if p.Signal(at(13)) || !p.Signal(at(16.5)) {
		t.Error("stale thumbnails must not keep it without a signal")
	}
	// Still but not uniform (a desktop, a menu): a signal however long
	r := NewPicture()
	busy := thumb(t, grey, 120, 0, 3)
	for s := 0; s <= 20; s++ {
		r.Add(busy, at(float64(s)))
	}
	if !r.Signal(at(20)) {
		t.Error("a still picture with detail is a signal")
	}
	// A camera: never twice the same picture
	c := NewPicture()
	for s := 0; s <= 20; s++ {
		c.Add(thumb(t, black, 0, 3, int64(s)), at(float64(s)))
	}
	if !c.Signal(at(20)) {
		t.Error("a dark camera is a signal")
	}
	// Reset: a new opening starts from nothing
	p.Reset()
	if !p.Signal(at(14)) {
		t.Error("after reset")
	}
}
