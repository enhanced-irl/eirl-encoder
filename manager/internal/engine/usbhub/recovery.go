package usbhub

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/detail"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// The recovery policy (MIXER.md §16): cut when a source has been StuckFor without being live and is
// still plugged, at most once every Gap and MaxCuts times in a row (a device that does not come back
// with this will not: it has to be unplugged by hand). The counts go back to zero after OKFor live.
const (
	StuckFor = 20 * time.Second
	Gap      = 120 * time.Second
	MaxCuts  = 3
	OKFor    = 60 * time.Second
	// SettleFor is the wait after a cut, for the device to enumerate again
	SettleFor = 3 * time.Second
	// RecentLive: a USB source on air that gave a frame less than this ago still counts as on air
	// (if it is reopening, a cut would lengthen its drop)
	RecentLive = 30 * time.Second
)

// Unknown, alone in what holds a cut: what is on air is not known.
const Unknown = "?"

// Recording names the local recording among what holds a cut.
const Recording = "recording"

// OnAir is something on air that a cut would power off: a video source or the audio input that sounds.
type OnAir struct {
	ID    string // the source's or the input's id (Unknown: cannot tell)
	Name  string // its name, as the user sees it
	Audio bool
}

// Text is how the Spanish detail and the log name it.
func (o OnAir) Text() string {
	if o.Audio {
		return "el audio de " + o.Name
	}
	return o.Name
}

var unknownOnAir = OnAir{ID: Unknown, Name: Unknown}

// ErrRecording is OnAirIfEmitting's answer while the local recording is on: the cut would power off its USB
// disk with the whole hub (RECORDING.md §5.5), so nothing is cut until it stops.
var ErrRecording = errors.New("recording to a USB disk")

// recordingOnAir is the hold while recording.
var recordingOnAir = OnAir{ID: Recording, Name: Recording}

// The source states the recovery looks at (engine.State*).
const (
	stateLive     = "live"
	stateError    = "error"
	stateGone     = "gone"
	stateStarting = "starting"
)

// Recovery decides when to cut the power of one USB source that gives no frames. One per source, used
// only by its goroutine. Without hubs (the zero value) it is the bare policy.
type Recovery struct {
	hubs      *Hubs
	downSince time.Time // since when it is not live (zero = it is, or it does not count)
	liveSince time.Time
	last      time.Time // last cut
	count     int
	dev       string  // the last USB device known (it is lost if it drops off the bus)
	held      []OnAir // on air and would power off with the cut: it waits for it to go off air
}

// NewRecovery is a source's recovery, with the engine's Hubs.
func NewRecovery(h *Hubs) *Recovery { return &Recovery{hubs: h} }

// Update says whether the source is live now. dev: its USB device, if known ("" keeps the last one).
func (r *Recovery) Update(live bool, now time.Time, dev string) {
	if dev != "" {
		r.dev = dev
	}
	if live {
		r.downSince, r.held = time.Time{}, nil
		if r.liveSince.IsZero() {
			r.liveSince = now
		}
		if r.count > 0 && now.Sub(r.liveSince) >= OKFor {
			r.count = 0
		}
		return
	}
	r.liveSince = time.Time{}
	if r.downSince.IsZero() {
		r.downSince = now
	}
}

// Due: is it time to cut now? plugged: the hub sees something on the port (enumerated or not).
func (r *Recovery) Due(now time.Time, plugged bool) bool {
	return plugged && r.dev != "" && !r.downSince.IsZero() && now.Sub(r.downSince) >= StuckFor &&
		r.count < MaxCuts && (r.last.IsZero() || now.Sub(r.last) >= Gap)
}

// GivenUp: MaxCuts in a row and still down.
func (r *Recovery) GivenUp() bool { return r.count >= MaxCuts && !r.downSince.IsZero() }

// NoteDetail is what the user is told while it gives no frames: why it does not recover by itself, or
// the zero detail. User-visible: the source's detail and its code.
func (r *Recovery) NoteDetail() detail.D {
	switch {
	case r.downSince.IsZero():
		return detail.D{}
	case r.GivenUp():
		return detail.New("hung_given_up")
	case len(r.held) == 1 && r.held[0] == unknownOnAir:
		return detail.New("hung_on_air_unknown")
	case len(r.held) == 1 && r.held[0] == recordingOnAir:
		return detail.New("hung_recording")
	case len(r.held) > 0:
		var video, audio []string
		for _, o := range r.held {
			if o.Audio {
				audio = append(audio, o.ID)
			} else {
				video = append(video, o.ID)
			}
		}
		return detail.HungOnAir(video, audio, shownOnAir(r.held))
	}
	return detail.D{}
}

// Note is NoteDetail's text ("" for none).
func (r *Recovery) Note() string { return r.NoteDetail().Text }

// NoteFor is Source.note_for: the note, which wins over each retry's error, while the source is in
// error or gone; the zero detail otherwise. Only for USB sources.
func (r *Recovery) NoteFor(state string) detail.D {
	if state == stateError || state == stateGone {
		return r.NoteDetail()
	}
	return detail.D{}
}

// Forget: the source is hidden (or otherwise not trying): the wait does not count.
func (r *Recovery) Forget() { r.downSince, r.held = time.Time{}, nil }

// Done notes a cut made now.
func (r *Recovery) Done(now time.Time) {
	r.last = now
	r.count++
	r.downSince = now
}

// Host is what Recover needs from the source and the engine.
type Host interface {
	// SetState is the source's setState
	SetState(state string, d detail.D)
	// OnAirIfEmitting is what a cut must not power off: {USB device: name} (Air.Devices for this
	// source) while the engine is emitting, nil when it is not. An error means it cannot tell, and then
	// nothing is cut while emitting (Python: an exception in emitting/usb_on_air).
	OnAirIfEmitting() (map[string]OnAir, error)
	// Wait is the source's stop-aware sleep; true if it is stopping
	Wait(d time.Duration) bool
}

// Recover is Source.recover, called at the top of a USB source's loop with its name, its state now and
// its USB device ("" if not known yet). A hung source (StuckFor without frames and still plugged) gets
// its port's power cut and comes back by itself. It returns true when it did that (the caller goes
// round its loop again). Given up after MaxCuts cuts in a row: its state says so, for someone to unplug
// it by hand. It never panics: a failure here must not leave the slot dead.
func (r *Recovery) Recover(h Host, name, state, dev string, now time.Time) (cut bool) {
	defer func() {
		if p := recover(); p != nil {
			elog.Event("mixer", "usb_recovery_failed", "source", name, "err", fmt.Sprint(p))
			cut = false
		}
	}()
	if state == stateLive {
		return false
	}
	if state != stateError && state != stateGone {
		// "Does not fit in the bus", no capturable format or coming back from a cut: cutting the power
		// does not fix it, and that time does not count as hung
		r.Forget()
		return false
	}
	r.Update(false, now, dev) // dev: also when it has never been live
	if r.GivenUp() {
		h.SetState(state, r.NoteDetail())
		return false
	}
	plugged, _ := r.hubs.Sys.Plugged(r.dev)
	if !plugged {
		r.held = nil // unplugged: nothing to wait for
		return false
	}
	if !r.Due(now, plugged) {
		return false
	}
	plan := r.hubs.Plan(r.dev, true)
	var held []OnAir
	if plan.OK() {
		onAir, err := h.OnAirIfEmitting()
		switch {
		case errors.Is(err, ErrRecording):
			held = []OnAir{recordingOnAir}
		case err != nil:
			if len(r.held) == 0 {
				elog.Event("mixer", "usb_recovery_held_unknown", "source", name, "err", err)
			}
			held = []OnAir{unknownOnAir}
		default:
			held = Blockers(plan, onAir)
		}
	}
	if len(held) > 0 {
		// The cut powers off the whole hub (§16): while emitting, what is on air is not powered off to
		// recover this one. It waits (without spending attempts) for it to go off air or for the
		// emission to stop.
		if !slices.Equal(held, r.held) {
			elog.Event("mixer", "usb_recovery_held", "source", name, "on_air", onAirText(held))
		}
		r.held = held
		h.SetState(state, r.NoteDetail())
		return false
	}
	r.held = nil
	if plan.OK() {
		if last, busy := r.hubs.Claim(plan, now, StuckFor); busy {
			// Another source has just cut this hub: that cut restarted this one too
			if last.After(r.downSince) {
				r.downSince = last
			}
			return false
		}
	}
	r.Done(now)
	if !plan.OK() {
		elog.Event("mixer", "usb_recovery_impossible", "source", name, "plan", plan.Text())
		r.count = MaxCuts // it will not try again until it comes back by itself
		return false
	}
	elog.Event("mixer", "usb_power_cycle", "source", name, "stuck_s", int(StuckFor.Seconds()), "plan", plan.Text(),
		"attempt", r.count, "max", MaxCuts)
	h.SetState(stateStarting, detail.New("port_reset"))
	_, errs := r.hubs.Sys.Cycle(plan, OffFor)
	for _, e := range errs {
		elog.Event("mixer", "usb_power_cycle_error", "source", name, "err", e)
	}
	h.Wait(SettleFor) // for it to enumerate again
	return true
}

// Program is the part of the scene that says which video is seen.
type Program struct {
	Main, Secondary string // source ids
	Layout          string // full | pip | side
}

// VideoSlot is one video source as the engine sees it.
type VideoSlot struct {
	ID, Name, Dev, State string    // Dev: its USB device, "" if none
	LastFrame            time.Time // its last frame (zero = never)
}

// AudioSlot is one audio input as the engine sees it.
type AudioSlot struct {
	ID          string // "alsa:<card>" for USB inputs
	Kind        string // usb | i2s | network
	Name, State string
	Volume      float64 // its target volume now (0 = it does not sound)
}

// Air is what goes out, for Devices.
type Air struct {
	Program Program
	Videos  []VideoSlot
	Audios  []AudioSlot
	Now     time.Time
}

// Devices is Engine.usb_on_air: {USB device: what} of what goes on air, except the source meID (whose
// device is meDev): the video seen (the main one and, in PiP or side by side, the secondary) and the
// audio that sounds. It is what cannot be powered off while emitting to recover another USB source on
// the same hub (Blockers). cardDev is Sys.DeviceOfCard. The names reach the source's detail (Note).
func (a Air) Devices(meID, meDev string, cardDev func(card string) string) map[string]OnAir {
	ids := map[string]bool{a.Program.Main: true}
	if a.Program.Layout != "full" && a.Program.Secondary != "" {
		ids[a.Program.Secondary] = true
	}
	out := map[string]OnAir{}
	for _, s := range a.Videos {
		// Also the one that was live a moment ago: if it is reopening, a cut would lengthen its drop
		if s.ID != meID && ids[s.ID] && s.Dev != "" &&
			(s.State == stateLive || (!s.LastFrame.IsZero() && a.Now.Sub(s.LastFrame) < RecentLive)) {
			out[s.Dev] = OnAir{ID: s.ID, Name: s.Name}
		}
	}
	for _, in := range a.Audios {
		if in.Kind != "usb" || in.State != stateLive || in.Volume <= 0 {
			continue
		}
		_, card, _ := strings.Cut(in.ID, ":")
		dev := cardDev(card)
		if dev != "" && (meDev == "" || dev != meDev) {
			if _, ok := out[dev]; !ok {
				out[dev] = OnAir{ID: in.ID, Name: in.Name, Audio: true}
			}
		}
	}
	return out
}

func onAirText(held []OnAir) string { return strings.Join(shownOnAir(held), ", ") }

// shownOnAir are the names of held (sorted by Blockers) without repeats: two cameras with the same name
// are said once, as before the ids.
func shownOnAir(held []OnAir) []string {
	var t []string
	for _, o := range held {
		if s := o.Text(); len(t) == 0 || t[len(t)-1] != s {
			t = append(t, s)
		}
	}
	return t
}
