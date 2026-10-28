package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/audio"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/phones"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/scene"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// --- scene: everything here runs on the actor ---

func (e *Engine) loadScene() {
	sv := scene.Load(e.sceneFile, e.configMain)
	e.scene, e.locked, e.extra = sv.Scene, sv.Locked, sv.Extra
	if sv.Restored {
		b, _ := json.Marshal(e.scene.Snapshot())
		elog.Event("mixer", "scene_loaded", "scene", string(b))
	}
	e.syncHidden()
}

func (e *Engine) saveScene() {
	if e.audio != nil {
		var ph json.RawMessage
		if e.phones != nil {
			ph, _ = json.Marshal(e.phones.Settings())
		}
		e.extra = e.audio.Extra(ph)
	}
	if err := scene.Save(e.sceneFile, e.configMain, &e.scene, e.locked, e.extra); err != nil {
		elog.Event("mixer", "scene_save_failed", "err", err)
	}
}

func (e *Engine) slotOf() map[string]int {
	m := map[string]int{}
	for i, s := range e.sources {
		if s != nil {
			m[s.id] = i
		}
	}
	return m
}

func (e *Engine) currentAspects() map[int]scene.Aspect {
	m := map[int]scene.Aspect{}
	for i, s := range e.sources {
		if s == nil {
			continue
		}
		if _, _, a := s.snapshot(); a.W > 0 && a.H > 0 {
			m[i] = a
		}
	}
	return m
}

func (e *Engine) targetRects() map[int]scene.Layer {
	e.aspects = e.currentAspects()
	return scene.SceneRects(&e.scene, e.slotOf(), e.nslots, e.width, e.height, e.aspects, e.pauseSlot)
}

func (e *Engine) applyScene() {
	e.fading = nil
	e.applied = e.targetRects()
	e.show([]map[int]scene.Layer{e.applied})
	if e.scr != nil {
		e.scr.SetAspects(e.aspects) // the screen's tiles get the same bars
	}
}

// npads: the compositor's layers, the slots' and the Pause's (the last one) if there is one.
func (e *Engine) npads() int {
	if e.pauseSlot == scene.NoPauseSlot {
		return e.nslots
	}
	return e.nslots + 1
}

// show puts layers on the compositor, one step per output frame, the last one to stay. Before the program
// plays they go straight onto its pads; after, through the animator, which sets every pad at once between
// two frames from the compositor's own thread: a fade moves exactly one step per frame of the program
// whatever the actor is doing, and no frame comes out with half a change.
func (e *Engine) show(steps []map[int]scene.Layer) {
	if e.anim == nil || !e.playing {
		e.applyRects(steps[len(steps)-1])
		return
	}
	n := e.npads()
	table := make([][]gst.Layer, len(steps))
	for k, rects := range steps {
		table[k] = make([]gst.Layer, n)
		for i, l := range rects {
			if i >= 0 && i < n {
				table[k][i] = gstLayer(l)
			}
		}
	}
	e.anim.Play(table)
}

func gstLayer(l scene.Layer) gst.Layer {
	return gst.Layer{X: l.X, Y: l.Y, W: l.W, H: l.H, Alpha: l.Alpha, Z: l.Z}
}

// fadeSteps is a FadePlan fade at the program's rate: one FadeRects per frame (t = k/n), then the new
// scene itself. The first step is still the old picture: the fade fills the n frames after it.
func fadeSteps(old, nw map[int]scene.Layer, roles map[int]scene.Role, length time.Duration, fps int) []map[int]scene.Layer {
	n := max(1, int(math.Round(length.Seconds()*float64(fps))))
	steps := make([]map[int]scene.Layer, 0, n+1)
	for k := range n {
		steps = append(steps, scene.FadeRects(old, nw, roles, float64(k)/float64(n)))
	}
	return append(steps, nw)
}

func (e *Engine) applyRects(rects map[int]scene.Layer) {
	for i, l := range rects {
		pad := "sink_" + strconv.Itoa(i)
		for _, kv := range gstLayer(l).Props() {
			e.comp.SetPad(pad, kv[0], kv[1])
		}
	}
}

// sceneChanged puts the new scene on the program, with a fade if it calls for one (FadePlan). A change
// halfway through a fade starts from the scene that fade was going to (e.applied), not from the picture
// half way: the layers that were still fading out go at once.
func (e *Engine) sceneChanged() {
	e.syncHidden()
	nw := e.targetRects()
	old := e.applied
	if e.scene.Transition == scene.TransitionFade && old != nil && !sameLayers(old, nw) {
		roles := scene.FadePlan(old, nw, e.width, e.height)
		length := scene.FadeLength(roles)
		e.fading = &fadeState{t0: time.Now(), length: length}
		e.applied = nw
		e.show(fadeSteps(old, nw, roles, length, e.fps))
	} else {
		e.applyScene()
	}
	e.saveScene()
	e.changed()
}

func sameLayers(a, b map[int]scene.Layer) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func (e *Engine) fadeTick() {
	if e.audio != nil && e.audio.Fading() {
		e.audio.Tick(time.Now())
	}
	f := e.fading
	if f == nil {
		return
	}
	// The animator plays the fade; the actor waits for its end and then places the scene again: what
	// changed meanwhile (a source found or moved to another slot, another aspect) was left for now. If
	// nothing did, the animator has nothing to change. A program that gives no frames does not hold the
	// actor: after the fade's length and fadeGrace the scene goes in one step.
	if e.anim == nil || !e.anim.Busy() || time.Since(f.t0) > f.length+fadeGrace {
		e.applyScene()
	}
}

// --- commands ---

// command runs a socket command (the screen's go through here too in M3). (ok, error code).
func (e *Engine) command(cmd string, msg map[string]any) (bool, string) {
	if audio.IsCommand(cmd) {
		if e.audio == nil {
			return false, "unsupported"
		}
		r := e.audio.Command(cmd, msg, e.audioView())
		if r.Changed {
			if r.LogCommand {
				e.logCommand(msg)
			}
			e.saveScene()
			e.publish()
		}
		return r.OK, r.Code
	}
	switch cmd {
	case "phones":
		ok, code, changed := phones.Command(e.phones, msg)
		if changed {
			e.logCommand(msg)
			e.saveScene()
			e.publish()
		}
		return ok, code
	case "slots":
		return e.setSlots(msg)
	case "preview":
		on, ok := msg["on"].(bool)
		if !ok {
			return false, "bad_request"
		}
		return e.setPreview(on)
	case "output_config":
		return e.reloadOutput()
	case "record":
		on, ok := msg["on"].(bool)
		if !ok {
			return false, "bad_request"
		}
		return e.setRecord(on, "socket")
	case "output", "lock":
		on, ok := msg["on"].(bool)
		if !ok {
			return false, "bad_request"
		}
		if cmd == "output" {
			return e.setOutput(on, "socket")
		}
		return e.setLocked(on)
	case "pause":
		if e.pauseSlot == scene.NoPauseSlot {
			return false, "unsupported"
		}
	}
	text, oldMain := e.scene.Pause.Text, e.scene.Main
	known := map[string]bool{}
	for id := range e.slotOf() {
		known[id] = true
	}
	changed, code := scene.ApplyCommand(&e.scene, msg, known)
	if code != "" {
		return false, code
	}
	if changed {
		e.logCommand(msg)
		if e.scene.Pause.Text != text {
			e.newPauseStill(e.scene.Pause.Text)
		}
		if e.audio != nil {
			e.audio.SetSilenced(e.scene.Pause.On && e.scene.Pause.Mute)
			if e.scene.Main != oldMain {
				e.audio.Follow(e.audioView())
			}
		}
		e.sceneChanged()
		e.publish() // before answering: whoever sent it reads the new state (mixer.json) already
	}
	return true, ""
}

// newPauseStill makes the Pause image with its new text off the actor (a still pipeline takes from
// hundreds of ms to seconds on the board) and swaps it in on the actor; only the newest text wins.
func (e *Engine) newPauseStill(text string) {
	e.pauseSeq++
	seq := e.pauseSeq
	go func() {
		b, err := e.still(pipeline.StillPause, text)
		if err != nil {
			elog.Event("mixer", "pause_still_failed", "err", err)
			return
		}
		e.post(func() {
			if seq != e.pauseSeq {
				b.Unref()
				return
			}
			old := e.pauseBuf
			e.pauseBuf = b
			if old != nil {
				old.Unref()
			}
		})
	}()
}

// logCommand logs a command that changed something. Its JSON (≤200 bytes) can carry the Pause text the
// user typed: it is data in a quoted value, and the app classifies lines by their event code.
func (e *Engine) logCommand(msg map[string]any) {
	b, _ := json.Marshal(msg)
	s := string(b)
	if len(s) > 200 {
		s = s[:200]
	}
	elog.Event("mixer", "command", "msg", s)
}

// setPreview: the WebRTC preview. The app asks for it while someone watches; without anyone its two
// valves drop before scaling and encoding. Without valves (LIVE=0): unsupported. A new engine starts
// with it on: the app repeats the command every 30 s.
func (e *Engine) setPreview(on bool) (bool, string) {
	if len(e.liveValves) == 0 {
		return false, "unsupported"
	}
	if on != e.previewOn {
		e.previewOn = on
		for _, v := range e.liveValves {
			v.SetBool("drop", !on)
		}
		elog.Event("mixer", "preview_toggled", "on", on)
		e.publish()
	}
	return true, ""
}

func (e *Engine) setLocked(on bool) (bool, string) {
	if on != e.locked {
		e.locked = on
		code := "screen_unlocked"
		if on {
			code = "screen_locked"
		}
		elog.Event("mixer", code)
		e.saveScene()
		e.publish()
	}
	return true, ""
}

// --- state ---

func (e *Engine) state() State {
	st := State{
		V: 1,
		Program: ProgramState{Snapshot: e.scene.Snapshot(), Width: e.width, Height: e.height, FPS: e.fps,
			MeasuredFPS: e.measured, NewFPS: e.newFPS},
		Slots:      e.nslots,
		SlotConfig: e.slotStates(),
		Catalog:    e.catalogState(),
		Sources:    []SourceState{},
		Screen:     e.screenState(),
		Output:     e.outputState(),
		Record:     e.recordState(),
	}
	for _, s := range e.sources {
		if s != nil {
			si := s.Info()
			si.Alias = e.slotCfg[s.slot].Alias
			st.Sources = append(st.Sources, si)
		}
	}
	if len(e.liveValves) > 0 {
		st.Preview = &PreviewState{On: e.previewOn}
	}
	if e.audio != nil {
		a := e.audio.State(e.audioView())
		if e.phones != nil {
			a.Phones = e.phones.Info()
		}
		st.Audio = &a
	}
	return st
}

// outputState: the output's info with who changed its mode last and how many times.
func (e *Engine) outputState() OutputState {
	o := OutputState{Mode: "off", State: "off", By: e.outBy, Seq: e.outSeq}
	if e.out == nil {
		return o
	}
	i := e.out.Info()
	o.Mode, o.State, o.Reason, o.Detail = i.Mode, i.State, i.Reason, i.Detail
	o.Kbps, o.InFPS, o.EncFPS, o.EncMbps, o.SentKbps, o.RTTMs = i.Kbps, i.InFPS, i.EncFPS, i.EncMbps, i.SentKbps, i.RTTMs
	o.Protocol, o.Codec, o.Size, o.FPS = i.Protocol, i.Codec, i.Size, i.FPS
	return o
}

// recordState: the recording's info with who changed its mode last; nil without a recorder.
func (e *Engine) recordState() *RecordState {
	if e.rec == nil {
		return nil
	}
	i := e.rec.Info()
	c := e.rec.Config()
	setting, quality := c.RecMode, c.RecQuality
	if setting == "" {
		setting = "off"
	}
	if quality == "" {
		quality = "high"
	}
	return &RecordState{Mode: i.Mode, State: i.State, Setting: setting, Quality: quality, Reason: i.Reason, Detail: i.Detail,
		By: e.recBy, Seq: e.recSeq, ElapsedS: i.ElapsedS, Kbps: i.Kbps, DroppedS: i.DroppedS, File: i.File, Disk: i.Disk}
}

// publish writes mixer.json and tells subscribers, only when something changed. A source with another
// aspect (4:3, portrait) re-places the scene for its bars.
func (e *Engine) publish() {
	if e.fading == nil && !sameAspects(e.currentAspects(), e.aspects) {
		e.applyScene()
	}
	if e.srv == nil {
		return
	}
	b, err := json.Marshal(e.state())
	if err != nil {
		elog.Event("mixer", "state_invalid", "err", err)
		return
	}
	changed, err := e.srv.Publish(json.RawMessage(b))
	if err != nil {
		elog.Event("mixer", "state_invalid", "err", err)
	}
	if changed {
		e.screenStatus(b)
	}
}

func sameAspects(a, b map[int]scene.Aspect) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func fmtMs(d time.Duration) string { return fmt.Sprintf("%.0f", float64(d)/float64(time.Millisecond)) }
