package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/camera"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// fakeCamera: brightness (0-255, 128) and power line (menu 0-2, 2), as uvcvideo shows them.
type fakeCamera struct {
	mu      sync.Mutex
	val     map[uint32]int32
	refuse  bool
	opened  int
	gate    chan struct{} // set: Set waits for it (a camera slow to answer)
	setsLog []int32
}

func (f *fakeCamera) Query(cid uint32) (camera.Info, error) {
	switch cid {
	case 0x980900:
		return camera.Info{Type: 1, Min: 0, Max: 255, Step: 1, Default: 128}, nil
	case 0x980918:
		return camera.Info{Type: 3, Min: 0, Max: 2, Step: 1, Default: 2}, nil
	}
	return camera.Info{}, camera.ErrAbsent
}

func (f *fakeCamera) HasMenu(cid uint32, i int32) bool { return cid == 0x980918 && i >= 0 && i <= 2 }

func (f *fakeCamera) Get(cid uint32) (int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.val[cid], nil
}

func (f *fakeCamera) Set(cid uint32, v int32) error {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse {
		return errors.New("input/output error")
	}
	f.val[cid] = v
	f.setsLog = append(f.setsLog, v)
	return nil
}

func (f *fakeCamera) Close() error { return nil }

const camID = "/dev/v4l/by-id/usb-Cam-video-index0"

// cameraSource puts a USB source with the fake camera in slot 4 (opened as the engine opens it) and runs its
// camera goroutine.
func cameraSource(t *testing.T) (*Engine, *Source, *fakeCamera) {
	t.Helper()
	e, _, _ := startEngine(t, nil, catalogue)
	f := &fakeCamera{val: map[uint32]int32{0x980900: 128, 0x980918: 1}}
	old := camera.Open
	camera.Open = func(string) (camera.Device, error) {
		f.mu.Lock()
		f.opened++
		f.mu.Unlock()
		return f, nil
	}
	t.Cleanup(func() { camera.Open = old })
	s := newSource(e, 3, camID, "usb", "Cam", "")
	e.sources[3] = s
	s.openCamera("/dev/video9", "Cam")
	s.camWG.Add(1)
	go func() {
		defer s.camWG.Done()
		s.cameraLoop()
	}()
	t.Cleanup(func() { // before camera.Open is put back: the command in flight ends first, as in run
		s.stop()
		s.camWG.Wait()
	})
	return e, s, f
}

type answer struct {
	ok   bool
	code string
}

// command runs a camera command as the actor would, and the actor's part of its answer.
func command(t *testing.T, e *Engine, msg map[string]any) answer {
	t.Helper()
	ch := make(chan answer, 1)
	e.cameraCommand(msg["cmd"].(string), msg, func(ok bool, code string) { ch <- answer{ok, code} })
	for {
		select {
		case a := <-ch:
			return a
		case fn := <-e.events:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal("no answer")
		}
	}
}

func sourceState(e *Engine, id string) *SourceState {
	for _, s := range e.state().Sources {
		if s.ID == id {
			return &s
		}
	}
	return nil
}

func TestCameraControl(t *testing.T) {
	e, _, f := cameraSource(t)
	st := sourceState(e, camID)
	if st == nil || len(st.Controls) != 2 || st.Controls[0].ID != "brightness" || st.Controls[1].Value != 1 {
		t.Fatalf("controls at open: %+v", st)
	}
	b, _ := json.Marshal(st.Controls[0])
	if string(b) != `{"id":"brightness","group":"image","type":"int","min":0,"max":255,"step":1,"default":128,"value":128}` {
		t.Errorf("json: %s", b)
	}
	// Socket numbers come as json.Number; the screen's as Go ints
	if a := command(t, e, map[string]any{"cmd": "camera_control", "source": camID, "control": "brightness",
		"value": json.Number("300")}); !a.ok {
		t.Fatalf("set: %+v", a)
	}
	if v := sourceState(e, camID).Controls[0].Value; v != 255 || f.val[0x980900] != 255 {
		t.Errorf("clamped to 255: state %d camera %d", v, f.val[0x980900])
	}
	if got := e.cams.Values(camID); got["brightness"] != 255 {
		t.Errorf("saved: %v", got)
	}
	cases := []struct {
		msg  map[string]any
		code string
	}{
		{map[string]any{"cmd": "camera_control", "source": "hdmi", "control": "brightness", "value": 1}, "unsupported"},
		{map[string]any{"cmd": "camera_control", "source": "/dev/v4l/by-id/none", "control": "brightness", "value": 1}, "unknown_source"},
		{map[string]any{"cmd": "camera_control", "source": camID, "control": "zoom", "value": 1}, "unknown_control"},
		{map[string]any{"cmd": "camera_control", "source": camID, "control": "brightness", "value": "1"}, "bad_request"},
		{map[string]any{"cmd": "camera_control", "source": camID, "control": "brightness", "value": 1.5}, "bad_request"},
		{map[string]any{"cmd": "camera_control", "source": camID, "control": "brightness"}, "bad_request"},
		{map[string]any{"cmd": "camera_control", "source": camID, "control": "power_line", "value": 3}, "bad_request"},
	}
	for _, c := range cases {
		if a := command(t, e, c.msg); a.ok || a.code != c.code {
			t.Errorf("%v → %+v, want %s", c.msg, a, c.code)
		}
	}
	f.refuse = true
	if a := command(t, e, map[string]any{"cmd": "camera_control", "source": camID, "control": "power_line", "value": 0}); a.code != "camera_error" {
		t.Errorf("refused: %+v", a)
	}
	f.refuse = false
	if a := command(t, e, map[string]any{"cmd": "camera_reset", "source": camID}); !a.ok {
		t.Fatalf("reset: %+v", a)
	}
	if f.val[0x980900] != 128 || f.val[0x980918] != 2 || e.cams.Values(camID) != nil {
		t.Errorf("reset: camera %v saved %v", f.val, e.cams.Values(camID))
	}
}

// The app forwards and accepts exactly the engine's table (a control added to camera's table and not to the app's
// whitelist would be dropped by mixer.clean without a word).
func TestCameraTableMatchesTheApp(t *testing.T) {
	ids := camera.IDs()
	if len(ids) != len(mixer.ControlIDs) {
		t.Fatalf("engine %d controls, app %d", len(ids), len(mixer.ControlIDs))
	}
	for _, id := range ids {
		if !mixer.ControlIDs[id] {
			t.Errorf("%s: not in mixer.ControlIDs", id)
		}
	}
}

// The app's client reads the controls as the engine publishes them (mixer.json).
func TestCameraStateTheAppReads(t *testing.T) {
	e, _, _ := cameraSource(t)
	e.publish()
	var st mixer.State
	if err := readState(e, &st); err != nil {
		t.Fatal(err)
	}
	for _, s := range st.Sources {
		if s.ID != camID {
			if s.Controls != nil {
				t.Errorf("%s: controls %v", s.ID, s.Controls)
			}
			continue
		}
		want := []mixer.Control{
			{ID: "brightness", Group: "image", Type: "int", Min: 0, Max: 255, Step: 1, Default: 128, Value: 128},
			{ID: "power_line", Group: "exposure", Type: "menu", Min: 0, Max: 2, Step: 1, Default: 2, Value: 1, Options: []int{0, 1, 2}},
		}
		b1, _ := json.Marshal(s.Controls)
		b2, _ := json.Marshal(want)
		if string(b1) != string(b2) {
			t.Errorf("controls: %s", b1)
		}
		return
	}
	t.Fatal("the camera is not in the state")
}

// The saved values are put back when the camera opens again (unplugged, reset, shown again).
func TestCameraReopenApplies(t *testing.T) {
	e, s, f := cameraSource(t)
	command(t, e, map[string]any{"cmd": "camera_control", "source": camID, "control": "brightness", "value": 40})
	f.val[0x980900] = 128 // the camera lost it (power cut)
	s.clearControls()
	if sourceState(e, camID).Controls != nil {
		t.Fatal("gone: no controls in the state")
	}
	if a := command(t, e, map[string]any{"cmd": "camera_control", "source": camID, "control": "brightness", "value": 1}); a.code != "unsupported" {
		t.Errorf("without controls: %+v", a)
	}
	s.openCamera("/dev/video9", "Cam")
	if f.val[0x980900] != 40 || sourceState(e, camID).Controls[0].Value != 40 {
		t.Errorf("not put back: %v", f.val)
	}
}

// A camera slow to answer does not hold the actor, and commands beyond the queue are busy.
func TestCameraBusy(t *testing.T) {
	e, _, f := cameraSource(t)
	f.gate = make(chan struct{})
	type reply struct {
		v int
		answer
	}
	replies := make(chan reply, camQueue+4)
	for i := range camQueue + 3 {
		e.cameraCommand("camera_control", map[string]any{"cmd": "camera_control", "source": camID,
			"control": "brightness", "value": i}, func(ok bool, code string) { replies <- reply{i, answer{ok, code}} })
	}
	// Which ones found the queue full depends on when the camera's goroutine took the first one
	busy := map[int]bool{}
	for len(replies) > 0 {
		if r := <-replies; r.code == "busy" {
			busy[r.v] = true
		}
	}
	if len(busy) == 0 {
		t.Fatal("the queue never filled")
	}
	close(f.gate)
	// The rest are answered, in order: the camera ends with the last value queued
	last := -1
	for i := range camQueue + 3 {
		if !busy[i] {
			last = i
		}
	}
	got := 0
	deadline := time.After(5 * time.Second)
	for got < camQueue+3-len(busy) {
		select {
		case <-replies:
			got++
		case fn := <-e.events:
			fn()
		case <-deadline:
			t.Fatalf("answered %d", got)
		}
	}
	if w := f.setsLog[len(f.setsLog)-1]; w != int32(last) {
		t.Errorf("last value written %d, want %d (writes %v)", w, last, f.setsLog)
	}
}

// A reset the camera only partly takes still forgets the saved values: they would come back at the next opening.
func TestCameraResetPartlyRefused(t *testing.T) {
	e, _, f := cameraSource(t)
	command(t, e, map[string]any{"cmd": "camera_control", "source": camID, "control": "brightness", "value": 40})
	f.refuse = true
	if a := command(t, e, map[string]any{"cmd": "camera_reset", "source": camID}); a.code != "camera_error" {
		t.Fatalf("reset: %+v", a)
	}
	if v := e.cams.Values(camID); v != nil {
		t.Errorf("still saved: %v", v)
	}
}

// A camera hidden or unplugged while a command runs stays out of the state when the command ends.
func TestCameraGoneMidCommand(t *testing.T) {
	e, s, f := cameraSource(t)
	f.gate = make(chan struct{})
	ch := make(chan answer, 1)
	e.cameraCommand("camera_control", map[string]any{"cmd": "camera_control", "source": camID, "control": "brightness",
		"value": 50}, func(ok bool, code string) { ch <- answer{ok, code} })
	time.Sleep(50 * time.Millisecond) // the camera's goroutine is in Set
	s.clearControls()
	close(f.gate)
	for {
		select {
		case <-ch:
			if c := sourceState(e, camID).Controls; c != nil {
				t.Fatalf("controls back on a hidden camera: %v", c)
			}
			return
		case fn := <-e.events:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal("no answer")
		}
	}
}

// The screen does not wait for a camera that hardly answers longer than the socket would.
func TestScreenCameraCommandTimeout(t *testing.T) {
	e, _, f := cameraSource(t)
	f.gate = make(chan struct{})
	defer close(f.gate)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx) // the actor, as on the board
	t0 := time.Now()
	ok, code := screenHost{e}.Command("camera_control", map[string]any{"cmd": "camera_control", "source": camID,
		"control": "brightness", "value": 60})
	if ok || code != "timeout" || time.Since(t0) > 4*time.Second {
		t.Fatalf("%v %q after %v", ok, code, time.Since(t0))
	}
}
