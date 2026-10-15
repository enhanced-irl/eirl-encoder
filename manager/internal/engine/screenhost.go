package engine

import (
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/appapi"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/camera"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/phones"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/server"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// startScreen: the multiview and the touch UI on the board's HDMI (M3), in their own pipeline that
// shares the program's timeline. If the screen fails or is unplugged, the broadcast goes on.
func (e *Engine) startScreen() {
	e.scr = screen.New(screen.Options{Media: e.media, Platform: e.plat, Host: screenHost{e},
		Slots: e.nslots, Width: e.width, Height: e.height, FPS: e.fps, Latency: e.latency, Sync: e.sync,
		RunDir: e.runDir, PauseSupported: e.pauseBridge != nil})
	e.appNet = appapi.New(e.scr.NetChanged, "")
	e.appNet.Start()
	e.scr.SetNet(screen.AppNet(e.appNet))
	e.scr.Start()
}

// screenView is the screen as the headphones see it (they open the HDMI's audio once it is stable).
type screenView struct{ s *screen.Screen }

func (v screenView) State() string                  { return v.s.Info().State }
func (v screenView) Latency() (time.Duration, bool) { return v.s.Latency() }

func (e *Engine) phonesScreen() phones.ScreenView {
	if e.scr == nil {
		return nil
	}
	return screenView{e.scr}
}

// screenHost is what the screen needs from the engine. Its methods run on the screen's goroutines.
type screenHost struct{ e *Engine }

// Command runs a screen command on the actor. Emitting from the screen records by=screen (the app sees
// it through by/seq and saves OUTPUT); the socket's commands can never say so.
func (h screenHost) Command(cmd string, msg map[string]any) (bool, string) {
	type res struct {
		ok   bool
		code string
	}
	ch := make(chan res, 1)
	h.e.post(func() {
		if camera.IsCommand(cmd) {
			h.e.cameraCommand(cmd, msg, func(ok bool, code string) { ch <- res{ok, code} })
			return
		}
		if cmd == "output" {
			on, ok := msg["on"].(bool)
			if !ok {
				ch <- res{false, "bad_request"}
				return
			}
			r1, r2 := h.e.setOutput(on, "screen")
			ch <- res{r1, r2}
			return
		}
		r1, r2 := h.e.command(cmd, msg)
		ch <- res{r1, r2}
	})
	// A camera answers once it has taken the setting: never longer than the socket waits, so the screen's next
	// command (stop streaming, take) is not left behind a camera that hardly answers
	var late <-chan time.Time
	if camera.IsCommand(cmd) {
		t := time.NewTimer(server.CommandTimeout)
		defer t.Stop()
		late = t.C
	}
	select {
	case r := <-ch:
		return r.ok, r.code
	case <-late:
		return false, server.ErrTimeout
	case <-h.e.stopped:
		return false, "unsupported"
	}
}

func (h screenHost) ProgramPeak(ch int) (float64, bool) {
	if h.e.audio == nil {
		return 0, false
	}
	p := h.e.audio.ProgramPeaks()
	if ch < 0 || ch >= len(p) {
		return 0, false
	}
	return p[ch], true
}

func (h screenHost) InputPeak(id string) (float64, bool) {
	if h.e.audio == nil {
		return 0, false
	}
	db, ok := h.e.audio.InputPeaks()[id]
	return db, ok
}

func (h screenHost) LinkInfo() (string, int) {
	if h.e.out == nil {
		return "srt", 0
	}
	return h.e.out.LinkInfo()
}

func (h screenHost) BaseTime() uint64 { return h.e.pgm.BaseTime() }

// AttachScreen points the sources' bridges and the program's tap at the screen's pipeline. Without
// waiting for the actor: the black for slots without a source is posted to it.
func (h screenHost) AttachScreen(mvs []gst.Element, mvp gst.Element) {
	e := h.e
	for i, b := range e.bridges {
		if i < len(mvs) {
			b.SetScreen(mvs[i], screen.MVGap)
		}
	}
	e.scrMu.Lock()
	if e.mvBridge == nil {
		if tap := e.pgm.ByName("pgmtap"); tap != nil {
			e.mvBridge = e.media.NewBridge(gst.BridgeShared, e.pgm, mvp)
			e.mvBridge.SetParams(gst.BridgeParams{Sync: e.sync, Extra: screen.ProgramDelay(e.latency)})
			e.mvBridge.Attach(tap)
		}
	} else {
		e.mvBridge.SetDst(mvp)
	}
	e.scrMu.Unlock()
	e.post(func() {
		for i, s := range e.sources {
			if s == nil {
				e.bridges[i].PushStill(e.black)
			}
		}
	})
}

func (h screenHost) DetachScreen() {
	e := h.e
	for _, b := range e.bridges {
		b.SetScreen(nil, 0)
	}
	e.scrMu.Lock()
	if e.mvBridge != nil {
		e.mvBridge.SetDst(nil)
	}
	e.scrMu.Unlock()
}

func (h screenHost) Changed() { h.e.changed() }

// screenState is the screen's part of the state.
func (e *Engine) screenState() ScreenState {
	st := ScreenState{State: "off", Locked: e.locked}
	if e.scr == nil {
		return st
	}
	in := e.scr.Info()
	st.State, st.Width, st.Height, st.Touch = in.State, in.Width, in.Height, in.Touch
	if in.Power != nil {
		st.Power = &PowerRequest{Want: in.Power.Want, Seq: in.Power.Seq, At: in.Power.At}
	}
	return st
}

// screenStatus hands the published state to the screen (it draws from it).
func (e *Engine) screenStatus(b []byte) {
	if e.scr == nil {
		return
	}
	if st, err := ui.ParseStatus(b); err == nil {
		e.scr.SetStatus(st)
	}
}
