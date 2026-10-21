package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/usbsignal"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// signalEvery: how often a USB capture card's signal flag is read while its source streams (a variable for
// the tests).
var signalEvery = time.Second

// signalFails: reads failed in a row before giving up the flag for this opening.
const signalFails = 3

// signalWatch is whether a USB capture card's HDMI input has a signal (usbsignal, SOURCES.md §9), read
// while its source streams: what the card says if it says it (the MS2109's flag), otherwise its picture
// (the same nearly uniform picture again and again). Without a signal its frames are held out of the
// program and the slot shows the slate with «sin señal HDMI», as for the board's HDMI. Only the source's
// goroutine uses it.
type signalWatch struct {
	card   signalCard
	stable usbsignal.Stable
	errs   int
	next   time.Time
}

// signalCard is what the watch reads: a usbsignal.Card or pictureCard (tests replace them).
type signalCard interface {
	Signal() (bool, error)
	Close() error
}

// pictureCard is the generic signal: the slot's picture (usbsignal.Picture, fed by the thumbnails).
type pictureCard struct{ p *usbsignal.Picture }

func (c pictureCard) Signal() (bool, error) { return c.p.Signal(time.Now()), nil }

func (c pictureCard) Close() error { return nil }

// openSignal: at each opening of a USB source, its card's flag if it has one, otherwise its picture.
// Logged when the way changes; a flag that cannot be read (without the udev rule, a permission error) is
// logged too, and the picture stands in for it.
func (s *Source) openSignal() {
	s.mu.Lock()
	dev, name := s.usbDev, s.name
	s.mu.Unlock()
	card, err := s.e.usbSig.Open(dev)
	method, kv := "flag", []any{"source", name}
	if err == nil {
		s.sig = &signalWatch{card: card}
		kv = append(kv, "chip", fmt.Sprintf("%02x", card.Chip), "node", card.Node)
	} else {
		method = "picture"
		if !errors.Is(err, usbsignal.ErrUnsupported) {
			kv = append(kv, "flag_err", err) // a flag it cannot read (without the udev rule: permission)
		}
		pic := s.e.pictures[s.slot]
		pic.Reset() // another source, or another opening: nothing of what the slot showed before
		s.sig = &signalWatch{card: pictureCard{pic}}
	}
	if method != s.sigLogged {
		elog.Event("mixer", "usb_signal_watch", append(kv, "method", method)...)
	}
	s.sigLogged = method
}

// checkSignal reads the flag once every signalEvery and holds or releases snk's frames.
func (s *Source) checkSignal(snk gst.Element, now time.Time) {
	w := s.sig
	if w == nil || now.Before(w.next) {
		return
	}
	w.next = now.Add(signalEvery)
	present, err := w.card.Signal()
	if err != nil {
		// Unplugged or hung: stillValid and the watchdog see to that. Failing in a row, back to the frames
		// alone for this opening: the flag never keeps the slate over a picture it cannot vouch for
		if w.errs++; w.errs >= signalFails {
			s.mu.Lock()
			name := s.name
			s.mu.Unlock()
			elog.Event("mixer", "usb_signal_failed", "source", name, "err", err)
			s.closeSignal(snk)
		}
		return
	}
	w.errs = 0
	if present, changed := w.stable.Add(present); changed {
		s.setHeld(snk, !present)
	}
}

// setHeld holds (or releases) snk's frames; the state follows with the next frame (stream).
func (s *Source) setHeld(snk gst.Element, held bool) {
	s.bridge.HoldIf(snk, held)
	s.mu.Lock()
	s.held = held
	s.mu.Unlock()
}

// closeSignal: the end of an opening, or of reading its flag. The frames go back to the program.
func (s *Source) closeSignal(snk gst.Element) {
	if s.sig == nil {
		return
	}
	s.sig.card.Close()
	s.sig = nil
	s.setHeld(snk, false)
}
