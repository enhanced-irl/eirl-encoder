package engine

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/detail"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/netcam"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/probe"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

const byIDDir = "/dev/v4l/by-id"

type discoverState struct {
	byID       []string
	cams       string
	probing    bool
	retryAt    time.Time
	noProbeAll bool // eirl-encoderd without `probe -all -want`
}

func listByID() []string {
	ents, err := os.ReadDir(byIDDir)
	if err != nil {
		return []string{}
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

func (e *Engine) probeList() sysx.Result {
	return e.run(10*time.Second, e.cfg.ProbeBin, "probe", "-list")
}

// discoverNow: at start, at once (afterwards discoverTick, in a goroutine).
func (e *Engine) discoverNow() {
	e.disc.byID = listByID()
	e.disc.cams = netcam.Stamp(netcam.CamerasFile(e.cfg.NetConfDir))
	e.discovered(e.probeList())
}

// discoverTick: `probe -list` takes up to 10 s with a USB going in and out; on the actor it froze the
// screen and the commands timed out. It runs in a goroutine and what it finds is applied on the actor.
// While one runs nothing else is looked at: when it ends, if something changed meanwhile, the next tick
// sees it and probes again.
func (e *Engine) discoverTick() {
	if e.disc.probing {
		return
	}
	names := listByID()
	cams := netcam.Stamp(netcam.CamerasFile(e.cfg.NetConfDir))
	retry := !e.disc.retryAt.IsZero() && !time.Now().Before(e.disc.retryAt)
	if slices.Equal(names, e.disc.byID) && cams == e.disc.cams && !retry && !(e.audio != nil && e.audio.CardsChanged()) {
		return
	}
	e.disc.retryAt = time.Time{}
	e.disc.byID, e.disc.cams, e.disc.probing = names, cams, true
	go func() {
		r := e.probeList()
		e.post(func() { e.discovered(r) })
	}()
}

// discovered applies the catalogue: the board's HDMI in the first slot and the USB and network cameras
// in catalogue order. A slot with a disconnected USB is reused only if another arrives and none is free.
func (e *Engine) discovered(r sysx.Result) {
	e.disc.probing = false
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
	if e.audio != nil {
		e.audio.Discover(cat) // before the video sources, like the Python engine
	}
	added := false
	retired := map[int]*Source{} // freed in this pass: a newcomer in the same slot waits for it
	if cat.OK {
		// A removed network camera frees its slot (a disconnected USB does not: it may come back); one
		// renamed in the web changes its name here too
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
	if cat.OK || !e.lastCat.OK {
		e.lastCat = cat
	}
	if e.place(cat, retired, nil) {
		added = true
	}
	if added && e.fading == nil {
		e.applyScene() // the scene names sources, not slots: it has to be placed again
	}
	e.changed()
}

// freeSlot: a free slot (the first one for the HDMI), else the slot of a USB gone for more than 10 s
// (the one gone longest).
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
	for _, w := range cat.Sources {
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
		s := newSource(e, slot, w.ID, w.Kind, w.Name, w.Node)
		s.prev = prev
		e.sources[slot] = s
		have[w.ID] = true
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

// freeSlot is where source id goes: the slot it is pinned to, or else the first empty auto slot (the
// HDMI comes first in the catalogue, so it takes the first one), or else an auto slot whose USB
// camera has been gone for over 10 s. An off slot never takes one.
func (e *Engine) freeSlot(id string) (int, bool) {
	if i, ok := e.pinnedSlot(id); ok {
		return i, true
	}
	for i, s := range e.sources {
		if s == nil && e.slotCfg[i].Assign == AssignAuto {
			return i, true
		}
	}
	best, bestSince := -1, time.Time{}
	for i, s := range e.sources {
		if s == nil || e.slotCfg[i].Assign != AssignAuto {
			continue
		}
		state, since, _ := s.snapshot()
		if s.kind == "usb" && state == StateGone && time.Since(since) > 10*time.Second && (best < 0 || since.Before(bestSince)) {
			best, bestSince = i, since
		}
	}
	return best, best >= 0
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
