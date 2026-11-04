package screen

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

func TestLEDPattern(t *testing.T) {
	for _, c := range []struct {
		out  ui.OutputStatus
		rec  *ui.RecordStatus
		want string
	}{
		{ui.OutputStatus{Mode: "off", State: "off"}, nil, ""},
		{ui.OutputStatus{Mode: "on", State: "on"}, nil, ledLive},
		{ui.OutputStatus{Mode: "on", State: "connecting"}, nil, ledTrying},
		{ui.OutputStatus{Mode: "on", State: "error"}, nil, ledTrying},
		{ui.OutputStatus{Mode: "off", State: "off"}, &ui.RecordStatus{Mode: "on", State: "on"}, ledRecording},
		{ui.OutputStatus{Mode: "off", State: "off"}, &ui.RecordStatus{Mode: "off", State: "off"}, ""},
		{ui.OutputStatus{Mode: "on", State: "on"}, &ui.RecordStatus{Mode: "on", State: "on"}, ledLive}, // streaming wins
	} {
		if got := ledPattern(&ui.Status{Output: c.out, Record: c.rec}); got != c.want {
			t.Errorf("%+v %+v: %q, want %q", c.out, c.rec, got, c.want)
		}
	}
	if ledPattern(nil) != "" {
		t.Fatal("nil")
	}
}

// fakeLED records the writes (the LED's directory exists so run drives it).
type fakeLED struct {
	mu     sync.Mutex
	writes []string
	fail   bool
}

func (f *fakeLED) write(name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return os.ErrPermission
	}
	f.writes = append(f.writes, name+"="+value)
	return nil
}

func (f *fakeLED) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.writes
	f.writes = nil
	return w
}

func startLED(t *testing.T, f *fakeLED) (*led, chan struct{}, chan struct{}) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ledDir), 0o755); err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	l := newLED(root, stop)
	l.write = f.write
	l.patterns = map[string][]ledStep{
		ledLive:      {{true, 20 * time.Millisecond}, {false, 20 * time.Millisecond}},
		ledTrying:    {{true, 5 * time.Millisecond}, {false, 5 * time.Millisecond}},
		ledRecording: {{true, 5 * time.Millisecond}, {false, 30 * time.Millisecond}},
	}
	go func() { l.run(); close(done) }()
	return l, stop, done
}

func TestLEDBlinksAndGoesBack(t *testing.T) {
	f := &fakeLED{}
	l, stop, done := startLED(t, f)
	l.set("") // idle at start: nothing written
	time.Sleep(20 * time.Millisecond)
	if w := f.take(); len(w) != 0 {
		t.Fatalf("idle wrote %v", w)
	}
	l.set(ledLive)
	time.Sleep(110 * time.Millisecond)
	w := strings.Join(f.take(), " ")
	if !strings.HasPrefix(w, "trigger=none brightness=255 brightness=0 brightness=255") {
		t.Fatalf("live: %s", w)
	}
	// The same pattern on every publish does not restart it: no two lit steps in a row
	for i := 0; i < 20; i++ {
		l.set(ledLive)
		time.Sleep(2 * time.Millisecond)
	}
	if w := strings.Join(f.take(), " "); strings.Contains(w, "brightness=255 brightness=255") || strings.Contains(w, "trigger") {
		t.Fatalf("restarted: %s", w)
	}
	l.set("")
	time.Sleep(20 * time.Millisecond)
	// (a step may land just before the change: only the end counts)
	if w := strings.Join(f.take(), " "); !strings.HasSuffix(w, "trigger="+ledKernelTrigger+" brightness=255") ||
		strings.Count(w, "trigger") != 1 {
		t.Fatalf("back to steady: %v", w)
	}
	// Stopped while blinking: steady again
	l.set(ledTrying)
	time.Sleep(20 * time.Millisecond)
	close(stop)
	<-done
	ws := f.take()
	if len(ws) < 3 || ws[0] != "trigger=none" || ws[len(ws)-2] != "trigger="+ledKernelTrigger || ws[len(ws)-1] != "brightness=255" {
		t.Fatalf("stopped: %v", ws)
	}
}

func TestLEDWithoutPermission(t *testing.T) {
	// An older unit (no group write): one failure, then nothing more is tried
	f := &fakeLED{fail: true}
	l, stop, done := startLED(t, f)
	l.set(ledLive)
	time.Sleep(10 * time.Millisecond)
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	l.set(ledTrying)
	time.Sleep(20 * time.Millisecond)
	close(stop)
	<-done
	if w := f.take(); len(w) != 0 {
		t.Fatalf("kept writing: %v", w)
	}
}

func TestLEDWithoutTheLED(t *testing.T) {
	stop := make(chan struct{})
	l := newLED(t.TempDir(), stop)
	done := make(chan struct{})
	go func() { l.run(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run waits for an LED that is not there")
	}
	l.set(ledLive) // never blocks, even with nobody reading
	l.set(ledTrying)
}
