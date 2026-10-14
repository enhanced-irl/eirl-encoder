package engine

import (
	"math"
	"testing"
	"time"
)

// A fade goes to the compositor as one table with a step per program frame (the animator plays it from
// the compositor's thread): the old picture, then the incoming layer up by 1/n each frame, then the new
// scene. A cut is a table of one step.
func TestFadeIsOneStepPerProgramFrame(t *testing.T) {
	e, m, _ := startEngine(t, nil, catalogue)
	logi := "/dev/v4l/by-id/usb-Logi-video-index0"
	if len(m.Animators) != 1 || m.Animators[0].NPads != 5 || m.Animators[0].Frame != time.Second/60 {
		t.Fatalf("animators: %+v", m.Animators)
	}
	a := m.Animators[0]
	if ok, code := e.command("transition", map[string]any{"cmd": "transition", "transition": "fade"}); !ok {
		t.Fatal(code)
	}
	if ok, code := e.command("take", map[string]any{"cmd": "take", "source": logi}); !ok {
		t.Fatal(code)
	}
	steps := a.Last()
	// 400 ms at 60 fps: 24 frames of fade after the first step, which is still the old picture
	if len(steps) != 25 {
		t.Fatalf("%d steps, want 25", len(steps))
	}
	for k, s := range steps {
		if len(s) != 5 {
			t.Fatalf("step %d: %d pads, want the 4 slots and the Pause", k, len(s))
		}
		in, base := s[1], s[0] // the Logi (slot 2) comes in over the HDMI (slot 1)
		if k < 24 {
			if want := float64(k) / 24; math.Abs(in.Alpha-want) > 1e-9 || in.Z <= base.Z || base.Alpha != 1 {
				t.Errorf("step %d: in %+v base %+v, want the Logi at alpha %.3f over the HDMI", k, in, base, want)
			}
		}
	}
	last := steps[24]
	if last[1].Alpha != 1 || last[0].Alpha != 0 || last[1].W != 1920 {
		t.Errorf("last step %+v, want the Logi alone, full", last)
	}
	// The actor only waits for the end of the table
	if e.fading == nil {
		t.Fatal("no fade running")
	}
	a.Hold = true
	e.fadeTick()
	if e.fading == nil {
		t.Error("a fade still playing ended")
	}
	e.fading.t0 = time.Now().Add(-scene400 - fadeGrace - time.Millisecond)
	e.fadeTick()
	if e.fading != nil {
		t.Error("a program without frames holds the actor past the fade's length")
	}
	// …and the scene is placed again, in one step (what discovery left for the end of the fade)
	if s := a.Last(); len(s) != 1 || s[0][1].Alpha != 1 {
		t.Errorf("after the fade: %+v, want the scene in one step", s)
	}
	a.Hold = false
	// A cut: one step, the new scene
	if ok, code := e.command("transition", map[string]any{"cmd": "transition", "transition": "cut"}); !ok {
		t.Fatal(code)
	}
	if ok, code := e.command("take", map[string]any{"cmd": "take", "source": "hdmi"}); !ok {
		t.Fatal(code)
	}
	if s := a.Last(); len(s) != 1 || s[0][0].Alpha != 1 || s[0][1].Alpha != 0 {
		t.Errorf("cut: %+v", s)
	}
}

// fadeSteps follows the program's rate: 50 fps gives 20 frames for 400 ms and 30 for the swap's 600 ms.
func TestFadeStepsAtTheProgramRate(t *testing.T) {
	for _, c := range []struct {
		length time.Duration
		fps    int
		want   int
	}{{400 * time.Millisecond, 60, 25}, {400 * time.Millisecond, 50, 21}, {600 * time.Millisecond, 50, 31}, {600 * time.Millisecond, 60, 37}} {
		if n := len(fadeSteps(nil, nil, nil, c.length, c.fps)); n != c.want {
			t.Errorf("%v at %d fps: %d steps, want %d", c.length, c.fps, n, c.want)
		}
	}
}

const scene400 = 400 * time.Millisecond
