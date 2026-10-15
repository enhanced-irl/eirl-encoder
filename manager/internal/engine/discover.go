package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/audio"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/detail"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/netcam"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/probe"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

// The capture devices' links: by-id changes when a camera is plugged or unplugged; by-path too, also
// for a second capture card with the same serial as another (they share one by-id link).
var devDirs = []string{"/dev/v4l/by-id", "/dev/v4l/by-path"}

// The life of a USB camera that comes and goes:
//   - settleProbe: a second `probe -list` this long after a change, for what was still binding (its
//     sound card, a slow camera) when the first one ran.
//   - unplugGrace: a USB camera unplugged this long leaves its slot (one that drops off the bus and comes
//     back at once, or is moved to another port, keeps it).
//   - cutSettle: after a hub's power cut everything under it drops and comes back by itself within ~10 s
//     (MIXER.md §16): none of that is a camera being unplugged.
var (
	settleProbe = 3 * time.Second
	unplugGrace = 3 * time.Second
	cutSettle   = 20 * time.Second
)

type discoverState struct {
	devs       []string
	cams       string
	probing    bool
	retryAt    time.Time
	settleAt   time.Time
	started    time.Time // when the probe being applied started (zero = just now)
	noProbeAll bool      // eirl-encoderd without `probe -all -want`
}

// listDevices is what is under the devices' link folders, by folder.
func listDevices() []string {
	names := []string{}
	for _, dir := range devDirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			names = append(names, filepath.Join(dir, e.Name()))
		}
	}
	slices.Sort(names)
	return names
}

func (e *Engine) probeList() sysx.Result {
	return e.run(10*time.Second, e.cfg.ProbeBin, "probe", "-list")
}

// discoverNow: at start, at once (afterwards discoverTick, in a goroutine).
func (e *Engine) discoverNow() {
	e.disc.devs = listDevices()
	e.disc.cams = netcam.Stamp(netcam.CamerasFile(e.cfg.NetConfDir))
	e.disc.started = time.Now()
	e.discovered(e.probeList())
}

// discoverTick: `probe -list` takes up to 10 s with a USB going in and out; on the actor it froze the
// screen and the commands timed out. It runs in a goroutine and what it finds is applied on the actor.
// While one runs nothing else is looked at: when it ends, if something changed meanwhile, the next tick
// sees it and probes again. Every tick also frees the slots of the cameras that have been unplugged.
func (e *Engine) discoverTick() {
	e.dropUnplugged()
	if e.disc.probing {
		return
	}
	now := time.Now()
	names := listDevices()
	cams := netcam.Stamp(netcam.CamerasFile(e.cfg.NetConfDir))
	retry := !e.disc.retryAt.IsZero() && !now.Before(e.disc.retryAt)
	settle := !e.disc.settleAt.IsZero() && !now.Before(e.disc.settleAt)
	changed := !slices.Equal(names, e.disc.devs) || cams != e.disc.cams
	if !changed && !retry && !settle && !(e.audio != nil && e.audio.CardsChanged()) {
		return
	}
	e.disc.retryAt, e.disc.settleAt = time.Time{}, time.Time{}
	if changed {
		e.disc.settleAt = now.Add(settleProbe)
	}
	e.disc.devs, e.disc.cams, e.disc.probing, e.disc.started = names, cams, true, now
	go func() {
		r := e.probeList()
		e.post(func() { e.discovered(r) })
	}()
}

// dropUnplugged frees the slots of the USB cameras unplugged for unplugGrace (Source.physicallyGone),
// like a network camera removed from the app: the slot goes black and empty, its audio input goes too,
// and the camera is placed again (in the same slot, if still free) when a catalogue probed afterwards
// lists it. One on air stays, with its slate, until it is taken off air: the engine never changes what
// goes out by itself. Nothing leaves for cutSettle after a hub's power cut.
func (e *Engine) dropUnplugged() {
	now := time.Now()
	e.leaving = slices.DeleteFunc(e.leaving, func(s *Source) bool { return waitDone(s.done, 0) })
	if cut := e.hubs.LastCut(); !cut.IsZero() && now.Sub(cut) < cutSettle {
		return
	}
	onAir := e.onAirIDs()
	dropped := false
	for i, s := range e.sources {
		if s == nil || s.kind != "usb" || s.unpluggedFor(now) < unplugGrace || slices.Contains(onAir, s.id) {
			continue
		}
		s.mu.Lock()
		name := s.name
		s.mu.Unlock()
		elog.Event("mixer", "source_removed", "source", name, "slot", i+1, "reason", "unplugged")
		s.stop()
		e.leaving = append(e.leaving, s)
		e.lastSlot[s.id], e.unpluggedAt[s.id] = i, now
		e.sources[i] = nil
		e.bridges[i].PushStill(e.black)
		dropped = true
	}
	// After the video: the audio of a camera that keeps its slot (on air, hung on its port) goes with it
	audioDropped := e.audio != nil && len(e.audio.DropGone(unplugGrace, e.slotAudio())) > 0
	if dropped {
		// A camera waiting without a slot (all were taken) gets the freed one now, not at the next change of
		// the catalogue
		e.place(e.placeable(e.lastCat), nil, nil)
		if e.fading == nil {
			e.applyScene()
		}
	}
	if dropped || audioDropped {
		e.syncAudio(e.lastCat)
		e.changed()
	}
}

// placeable is cat without the cameras dropped as unplugged that no catalogue probed since has listed
// (the last catalogue, or a probe that started before they left, may still have them).
func (e *Engine) placeable(cat probe.Catalogue) probe.Catalogue {
	cat.Sources = slices.DeleteFunc(slices.Clone(cat.Sources), func(w probe.Entry) bool {
		_, gone := e.unpluggedAt[w.ID]
		return gone
	})
	return cat
}

// slotAudio is the own audio of every source with a slot.
func (e *Engine) slotAudio() map[string]bool {
	out := map[string]bool{}
	for _, s := range e.sources {
		if s == nil {
			continue
		}
		s.mu.Lock()
		a := audio.DefaultLink(s.kind, s.audioCard)
		s.mu.Unlock()
		if a != "" {
			out[a] = true
		}
		for _, w := range e.lastCat.Sources {
			if w.ID == s.id && w.Audio != "" {
				out[w.Audio] = true
			}
		}
	}
	return out
}

// syncAudio opens the audio inputs that go with the video slots (Oct 15: the cameras are detected without a
// limit, but only the ones with a slot are used), from catalogue cat:
//   - A camera's own audio (Entry.Audio) only while that camera has a slot, or a camera with a slot was given it
//     by hand (audio_link). The board's HDMI audio (I2S) and a sound card that is no camera's, always.
//   - They take the audio slots in the order of their video slots, the loose cards last; with every audio slot
//     taken, a loose card gives its slot to the audio of a camera with a slot.
//   - The audio on air is never closed (Bus.Retire), nor what was unplugged and not seen since reopened.
func (e *Engine) syncAudio(cat probe.Catalogue) {
	if e.audio == nil {
		return
	}
	pairs := map[string]string{} // video id → its own audio
	cameras := map[string]bool{} // the audio inputs that are some camera's
	for _, w := range cat.Sources {
		if w.Audio != "" {
			pairs[w.ID], cameras[w.Audio] = w.Audio, true
		}
	}
	overrides := e.audio.Overrides()
	order := map[string]int{} // the audio each source with a slot uses → its slot
	for i, s := range e.sources {
		if s == nil {
			continue
		}
		mine := pairs[s.id]
		if s.kind == "hdmi-csi" {
			mine = "i2s" // always open (it is no camera's), but in the HDMI's place
		}
		// Its own audio stays open also when another was chosen by hand: audio_link only takes an open one,
		// and that is how the user goes back to it
		for _, a := range []string{overrides[s.id], mine} {
			if _, seen := order[a]; a != "" && !seen {
				order[a] = i
			}
		}
	}
	out := map[string]bool{} // a camera's audio that no camera with a slot uses
	for a := range cameras {
		if _, used := order[a]; !used {
			out[a] = true
		}
	}
	c := cat
	c.Audio = slices.DeleteFunc(slices.Clone(cat.Audio), func(a probe.AudioEntry) bool {
		return out[a.ID] || e.audio.Unplugged(a.ID)
	})
	rank := func(a probe.AudioEntry) int {
		if i, ok := order[a.ID]; ok {
			return i
		}
		return len(e.sources) // the loose sound cards, after the cameras'
	}
	slices.SortStableFunc(c.Audio, func(x, y probe.AudioEntry) int { return rank(x) - rank(y) })
	e.audio.Retire(out, "no_video_slot")
	// Audio slots for the cameras with a video slot: the loose cards that took one give it up, the last first
	open := map[string]bool{}
	for _, in := range e.audio.Inputs() {
		open[in.ID] = true
	}
	need, free := 0, e.audio.Slots()-len(open)
	var loose []string
	for _, a := range c.Audio {
		_, used := order[a.ID]
		switch {
		case used && !open[a.ID]:
			need++
		case !used && open[a.ID] && a.ID != e.audio.Main():
			loose = append(loose, a.ID)
		}
	}
	evict := map[string]bool{}
	for i := len(loose) - 1; i >= 0 && need > free; i-- {
		evict[loose[i]] = true
		free++
	}
	if len(evict) > 0 {
		e.audio.Retire(evict, "no_audio_slot")
		c.Audio = slices.DeleteFunc(c.Audio, func(a probe.AudioEntry) bool { return evict[a.ID] })
	}
	e.audio.Discover(c)
}

// discovered applies the catalogue: the board's HDMI in the first slot and the USB and network cameras
// in catalogue order. An unplugged USB camera leaves its slot through dropUnplugged, not here: a probe
// right after it drops off the bus does not tell unplugged from coming back.
func (e *Engine) discovered(r sysx.Result) {
	e.disc.probing = false
	started := e.disc.started
	if started.IsZero() {
		started = time.Now()
	}
	e.disc.started = time.Time{}
	out := r.Out
	if r.RC != 0 {
		out = ""
		elog.Event("mixer", "probe_list_failed", "rc", r.RC, "err", trim(r.Err, 160))
	}
	// Without a real catalogue nothing is removed or renamed (the bars still come with SOURCE=test)
	cat := probe.ParseList(out, e.cfg.MixerTest(), netcam.Valid, netcam.CleanName)
	if !cat.OK {
		e.disc.retryAt = time.Now().Add(30 * time.Second)
	}
	added := false
	retired := map[int]*Source{} // freed in this pass: a newcomer in the same slot waits for it
	if cat.OK {
		// A removed network camera frees its slot (an unplugged USB, in dropUnplugged); one renamed in
		// the web changes its name here too
		nets := map[string]string{}
		for _, w := range cat.Sources {
			if w.Kind == "network" {
				nets[w.ID] = w.Name
			}
		}
		for i, s := range e.sources {
			if s == nil || s.kind != "network" {
				continue
			}
			if name, ok := nets[s.id]; ok {
				s.mu.Lock()
				old := s.name
				s.name = name
				s.mu.Unlock()
				if old != name {
					elog.Event("mixer", "source_renamed", "from", old, "to", name)
				}
				continue
			}
			elog.Event("mixer", "source_removed", "source", s.name, "slot", i+1)
			s.stop()
			retired[i] = s
			e.sources[i] = nil
			e.bridges[i].PushStill(e.black)
			added = true
		}
	}
	if cat.OK {
		// What this probe lists, and was dropped as unplugged before it started, is back
		var ids []string
		for _, w := range cat.Sources {
			if t, ok := e.unpluggedAt[w.ID]; ok && t.Before(started) {
				delete(e.unpluggedAt, w.ID)
			}
		}
		for _, a := range cat.Audio {
			ids = append(ids, a.ID)
		}
		if e.audio != nil {
			e.audio.Seen(ids, started)
		}
	}
	if cat.OK || !e.lastCat.OK {
		e.lastCat = cat
	}
	if e.place(e.placeable(cat), retired, nil) {
		added = true
	}
	e.syncAudio(cat) // after the video: a camera's audio goes with its video slot
	if added && e.fading == nil {
		e.applyScene() // the scene names sources, not slots: it has to be placed again
	}
	e.changed()
}

// place starts every source of the catalogue that has no slot yet, in the slot the configuration gives
// it (slots.go). retired are the slots' previous sources and moved the sources taken out of another
// slot (by id): a new source waits for both to end. true if any source was placed.
func (e *Engine) place(cat probe.Catalogue, retired map[int]*Source, moved map[string]*Source) bool {
	have := map[string]bool{}
	for _, s := range e.sources {
		if s != nil {
			have[s.id] = true
		}
	}
	added := false
	// The ones coming back to their slot first: a newcomer must not take it
	ws := slices.Clone(cat.Sources)
	slices.SortStableFunc(ws, func(x, y probe.Entry) int { return e.backFirst(x.ID) - e.backFirst(y.ID) })
	for _, w := range ws {
		if have[w.ID] {
			continue
		}
		slot, ok := e.freeSlot(w.ID)
		if !ok {
			elog.Event("mixer", "no_free_slot", "source", w.Name, "slots", e.nslots)
			continue
		}
		var prev []*Source
		if p := retired[slot]; p != nil {
			prev = append(prev, p)
		}
		if old := e.sources[slot]; old != nil {
			elog.Event("mixer", "slot_reused", "slot", slot+1, "from", old.name, "to", w.Name)
			old.stop()
			prev = append(prev, old)
		}
		if m := moved[w.ID]; m != nil {
			prev = append(prev, m)
		}
		for _, l := range e.leaving { // freed when unplugged and maybe still closing
			if (l.slot == slot || l.id == w.ID) && !waitDone(l.done, 0) {
				prev = append(prev, l)
			}
		}
		s := newSource(e, slot, w.ID, w.Kind, w.Name, w.Node)
		s.prev = prev
		e.sources[slot] = s
		have[w.ID] = true
		delete(e.lastSlot, w.ID)
		e.wg.Add(1)
		go func() { defer e.wg.Done(); s.run() }()
		added = true
		if (e.scene.Main == "" || e.scene.Main == "usb:auto") && w.Kind == "usb" && e.configMain == "usb:auto" {
			id := w.ID
			e.post(func() { e.setMainDefault(id) })
		}
	}
	return added
}

// freeSlot is where source id goes: the slot it is pinned to; else the auto slot it had when it was
// unplugged, if still free (unplugging and plugging a camera keeps the layout); else the first empty auto
// slot (the HDMI comes first in the catalogue, so it takes the first one); else an auto slot whose USB
// camera has been gone for over 10 s and is not on air (a hung one, still on its port: see dropUnplugged).
// An off slot never takes one.
func (e *Engine) freeSlot(id string) (int, bool) {
	if i, ok := e.pinnedSlot(id); ok {
		return i, true
	}
	if i, ok := e.lastSlot[id]; ok && i < len(e.sources) && e.sources[i] == nil && e.slotCfg[i].Assign == AssignAuto {
		return i, true
	}
	for i, s := range e.sources {
		if s == nil && e.slotCfg[i].Assign == AssignAuto {
			return i, true
		}
	}
	onAir := e.onAirIDs()
	best, bestSince := -1, time.Time{}
	for i, s := range e.sources {
		if s == nil || e.slotCfg[i].Assign != AssignAuto {
			continue
		}
		state, since, _ := s.snapshot()
		if s.kind == "usb" && state == StateGone && time.Since(since) > 10*time.Second && !slices.Contains(onAir, s.id) &&
			(best < 0 || since.Before(bestSince)) {
			best, bestSince = i, since
		}
	}
	return best, best >= 0
}

// backFirst orders the cameras coming back to a remembered slot (0) before the rest (1).
func (e *Engine) backFirst(id string) int {
	if _, ok := e.lastSlot[id]; ok {
		return 0
	}
	return 1
}

func (e *Engine) setMainDefault(id string) {
	if e.scene.Main == "" || e.scene.Main == "usb:auto" {
		e.scene.Main = id
		e.sceneChanged()
	}
}

// chooseUSB splits the USB bus between every source (`eirl-encoderd probe -all`, SOURCES.md §1)
// respecting what is open: a live camera does not change mode because another arrives, and the new one
// takes what fits. Without -all in the binary, this source alone as if it had the whole bus. Runs in the
// source's goroutine; what it needs from the others comes from the actor.
func (e *Engine) chooseUSB(id string) (probe.Result, error) {
	type plan struct {
		all  bool
		args []string
	}
	ch := make(chan plan, 1)
	e.post(func() {
		if e.disc.noProbeAll {
			ch <- plan{}
			return
		}
		var keep []string
		n := 0
		for _, s := range e.sources {
			if s == nil || s.kind == "usb" {
				n++
			}
			if s == nil || s.kind != "usb" || s.id == id {
				continue
			}
			s.mu.Lock()
			if s.state == StateLive && s.choice != nil {
				keep = append(keep, "-keep", probe.KeepArg(s.id, s.choice.Probe))
			}
			s.mu.Unlock()
		}
		n = max(1, min(4, n))
		// -want: the one being opened goes in for sure, behind the kept ones and within the limit
		args := append([]string{"probe", "-all", "-max", strconv.Itoa(n)}, keep...)
		ch <- plan{all: true, args: append(args, "-want", id)}
	})
	var p plan
	select {
	case p = <-ch:
	case <-e.stopped:
		return probe.Result{}, &notReady{StateStarting, detail.D{}, 0}
	}
	if p.all {
		r := e.run(10*time.Second, e.cfg.ProbeBin, p.args...)
		if r.RC == 0 {
			if res, ok := probe.ParseAll(r.Out)[id]; ok {
				return res, nil
			}
			return probe.Result{Probe: pipeline.Probe{Warnings: "no_format"}}, nil
		}
		if strings.Contains(r.Err, "flag provided but not defined") {
			elog.Event("mixer", "probe_all_unsupported")
			e.post(func() { e.disc.noProbeAll = true })
		} else {
			elog.Event("mixer", "probe_all_failed", "rc", r.RC, "err", trim(r.Err, 160))
		}
	}
	r := e.run(10*time.Second, e.cfg.ProbeBin, "probe", "-device", id, "-format", "auto", "-mode", "auto")
	switch r.RC {
	case 0:
		if res, ok := probe.Parse(r.Out); ok {
			return res, nil
		}
	case 3:
		return probe.Result{}, &notReady{StateGone, detail.New("gone"), time.Second}
	case 4, 64:
		d := detail.New("no_format")
		if e := trim(r.Err, 160); e != "" {
			d = detail.New("unsupported", "error", e)
		}
		return probe.Result{}, &notReady{StateUnsupported, d, 10 * time.Second}
	}
	return probe.Result{}, &notReady{StateError, detail.New("probe_failed", "rc", strconv.Itoa(r.RC), "error", trim(r.Err, 120)), 3 * time.Second}
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
