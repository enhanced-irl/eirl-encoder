package screen

import (
	"os"
	"path/filepath"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The board's power LED (MIXER.md §14.2): the kernel's gpio-leds "pwr" (Tegra GPIO PI1), which is at once the carrier's
// green LED and J50's PC_LED, the power button's LED. Steady, as the kernel leaves it, unless the board is streaming or
// recording: then it blinks. Only on and off: the pin has no PWM, so no fade.
//
// Its files are root's. eirl-engine.service lets the engine's group write brightness and trigger while it runs, and
// puts the LED back when the engine stops, even killed (ExecStartPre and ExecStopPost with "+"): a dead engine never
// leaves the LED dark on a board that is on.

const (
	ledDir = "/sys/class/leds/pwr"
	// ledKernelTrigger: the kernel's own trigger, which only blinks when the board throttles (not enough current).
	// Put back, it does not light the LED by itself: brightness goes after it.
	ledKernelTrigger = "system-throttle"
)

// LED patterns, by what the board is doing ("" = steady).
const (
	ledLive      = "live"      // streaming
	ledTrying    = "trying"    // the output on but not streaming: connecting, or an error it retries
	ledRecording = "recording" // recording without streaming
)

type ledStep struct {
	on bool
	d  time.Duration
}

var ledPatterns = map[string][]ledStep{
	ledLive:      {{true, time.Second}, {false, time.Second}},
	ledTrying:    {{true, 250 * time.Millisecond}, {false, 250 * time.Millisecond}},
	ledRecording: {{true, 200 * time.Millisecond}, {false, 2800 * time.Millisecond}},
}

// ledPattern is what the LED shows for a published state. Streaming wins over recording.
func ledPattern(st *ui.Status) string {
	switch {
	case st == nil:
		return ""
	case st.Output.Mode == "on" && st.Output.State == "on":
		return ledLive
	case st.Output.Mode == "on":
		return ledTrying
	case ui.Recording(st.Record):
		return ledRecording
	}
	return ""
}

// led drives the power LED from its own goroutine: set hands it a pattern (the latest wins, the same one does not
// restart the blinking), run blinks it and, when the screen stops, leaves it steady.
type led struct {
	dir      string
	patterns map[string][]ledStep
	want     chan string
	stop     <-chan struct{}
	// write is a write to one of the LED's files (tests replace it)
	write func(name, value string) error
}

func newLED(root string, stop <-chan struct{}) *led {
	l := &led{dir: filepath.Join(root+"/", ledDir), patterns: ledPatterns, want: make(chan string, 1), stop: stop}
	l.write = func(name, value string) error {
		return os.WriteFile(filepath.Join(l.dir, name), []byte(value+"\n"), 0)
	}
	return l
}

// set never blocks: a pattern not taken yet is replaced by the newer one.
func (l *led) set(p string) {
	for {
		select {
		case l.want <- p:
			return
		default:
		}
		select {
		case <-l.want:
		default:
		}
	}
}

func (l *led) run() {
	if _, err := os.Stat(l.dir); err != nil {
		return // no such LED (another board, the Mac): nothing to drive
	}
	pattern, broken := "", false
	defer func() {
		if pattern != "" && !broken {
			l.steady() // the screen stops: steady again (and the unit's ExecStopPost too)
		}
	}()
	for {
		switch {
		case broken: // no permission (an older unit): it stays as the kernel has it
			<-l.stop
			return
		case pattern == "":
			select {
			case <-l.stop:
				return
			case p := <-l.want:
				if p != "" {
					broken = !l.take()
					pattern = p
				}
			}
		default:
			next, stopped, ok := l.blink(pattern)
			switch {
			case stopped:
				return
			case !ok:
				broken = true
			default:
				pattern = next
				if pattern == "" {
					broken = !l.steady()
				}
			}
		}
	}
}

// blink runs a pattern's steps until another pattern comes (returned), the screen stops, or a write fails (ok false).
// The same pattern again (each publish hands it) does not restart it.
func (l *led) blink(pattern string) (next string, stopped, ok bool) {
	steps := l.patterns[pattern]
	if len(steps) == 0 {
		return "", false, true
	}
	for i := 0; ; i = (i + 1) % len(steps) {
		v := "0"
		if steps[i].on {
			v = "255"
		}
		if err := l.write("brightness", v); err != nil {
			l.failed(err)
			return "", false, false
		}
		t := time.NewTimer(steps[i].d)
		for waiting := true; waiting; {
			select {
			case <-l.stop:
				t.Stop()
				return "", true, true
			case p := <-l.want:
				if p != pattern {
					t.Stop()
					return p, false, true
				}
			case <-t.C:
				waiting = false
			}
		}
	}
}

// take: the LED is ours (no trigger), to blink.
func (l *led) take() bool {
	if err := l.write("trigger", "none"); err != nil {
		l.failed(err)
		return false
	}
	return true
}

// steady: back to the kernel's trigger, and lit.
func (l *led) steady() bool {
	err := l.write("trigger", ledKernelTrigger)
	if err == nil {
		err = l.write("brightness", "255")
	}
	if err != nil {
		l.failed(err)
		return false
	}
	return true
}

func (l *led) failed(err error) {
	elog.Event("mixer", "led_unavailable", "dir", l.dir, "err", err)
}
