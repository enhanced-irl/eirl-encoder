package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

const usbA = "/dev/v4l/by-id/usb-A-video-index0"

func decodeList(t *testing.T, s string) []any {
	t.Helper()
	var l []any
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestParseSlots(t *testing.T) {
	ok := []struct{ in, want string }{
		{`[]`, "auto,auto,auto,auto"},
		{`[{"slot":2,"assign":"off"},{"slot":4,"assign":"net:cam1","alias":"Phone"}]`, "auto,off,auto,net:cam1/Phone"},
		{`[{"slot":1,"assign":"` + usbA + `","alias":"Cámara 1 (front)"}]`, usbA + "/Cámara 1 (front),auto,auto,auto"},
		{`[{"slot":1,"assign":"hdmi","alias":""},{"slot":3,"assign":"test"}]`, "hdmi,auto,test,auto"},
		{`[{"slot":1,"assign":"auto","alias":"Rock & Roll #1 + 2/3"}]`, "auto/Rock & Roll #1 + 2/3,auto,auto,auto"},
		{`[{"slot":1,"assign":"auto","alias":"ÀÉÎÕÜàéîõüçñ漢字ابت12345"}]`, "auto/ÀÉÎÕÜàéîõüçñ漢字ابت12345,auto,auto,auto"},
	}
	for _, c := range ok {
		got, err := parseSlots(decodeList(t, c.in), 4)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		var parts []string
		for i, s := range got {
			if s.Slot != i+1 {
				t.Errorf("%s: slot %d at %d", c.in, s.Slot, i)
			}
			p := s.Assign
			if s.Alias != "" {
				p += "/" + s.Alias
			}
			parts = append(parts, p)
		}
		if strings.Join(parts, ",") != c.want {
			t.Errorf("%s: %s, want %s", c.in, strings.Join(parts, ","), c.want)
		}
	}
	bad := []string{
		`[{"slot":1,"assign":"auto"},{"slot":1,"assign":"off"}]`,  // repeated slot
		`[{"slot":0,"assign":"auto"}]`,                            // out of range
		`[{"slot":5,"assign":"auto"}]`,                            // out of range
		`[{"slot":1.5,"assign":"auto"}]`,                          // not an integer
		`[{"slot":"1","assign":"auto"}]`,                          // not a number
		`[{"slot":1,"assign":"hdmi"},{"slot":2,"assign":"hdmi"}]`, // the same source pinned twice
		`[{"slot":1,"assign":"usb:/dev/v4l/by-id/usb-A"}]`,        // ids go exactly as in sources[].id
		`[{"slot":1,"assign":"/dev/video0"}]`,                     // only by-id/by-path
		`[{"slot":1,"assign":"net:cam9"}]`,                        // net:cam1-8
		`[{"slot":1,"assign":"/dev/v4l/by-id/../../etc/passwd"}]`, // no slashes after by-id/
		`[{"slot":1}]`, // no assign
		`[{"slot":1,"assign":"auto","alias":"1234567890123456789012345"}]`, // 25 characters
		`[{"slot":1,"assign":"auto","alias":"a\nb"}]`,                      // control
		`[{"slot":1,"assign":"auto","alias":"a‮b"}]`,                       // bidi override
		`[{"slot":1,"assign":"auto","alias":"🎥"}]`,                         // not a letter
		`[{"slot":1,"assign":"auto","alias":"a<b>"}]`,                      // not allowed punctuation
		`[{"slot":1,"assign":"auto","alias":7}]`,                           // not a string
		`["auto"]`,
	}
	for _, in := range bad {
		if _, err := parseSlots(decodeList(t, in), 4); err == nil {
			t.Errorf("%s: accepted", in)
		}
	}
	// Slots beyond MIXER_SLOTS are ignored (still validated), missing ones are auto
	got, err := parseSlots(decodeList(t, `[{"slot":3,"assign":"off"},{"slot":2,"assign":"test","alias":"Bars"}]`), 2)
	if err != nil || len(got) != 2 || got[1] != (SlotConfig{2, "test", "Bars"}) || got[0].Assign != AssignAuto {
		t.Errorf("2 slots: %+v %v", got, err)
	}
}

// startWithSlots starts an engine whose app directory holds slots.json (nothing written = no file).
func startWithSlots(t *testing.T, env map[string]string, file, list string) (*Engine, *runner, *lockedLog) {
	t.Helper()
	dir := t.TempDir()
	if file != "" {
		if err := os.WriteFile(filepath.Join(dir, slotsFile), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if env == nil {
		env = map[string]string{}
	}
	env["EIRL_CONF"] = filepath.Join(dir, "engine.env")
	e, _, r := startEngine(t, env, list)
	log := &lockedLog{}
	elog.SetOutput(log)
	return e, r, log
}

// lockedLog is the engine's log as a test reads it while the sources' goroutines write.
type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func slotIDs(e *Engine) string {
	var ids []string
	for _, s := range e.sources {
		if s == nil {
			ids = append(ids, "-")
		} else {
			ids = append(ids, s.id)
		}
	}
	return strings.Join(ids, ",")
}

const catAll = `{"sources":[{"id":"` + usbA + `","kind":"usb","name":"A"},
	{"id":"net:cam1","kind":"network","node":"rtsp://127.0.0.1:8554/cam1","name":"Phone"},
	{"id":"tc","kind":"hdmi-csi","node":"/dev/video0"}],"audio":[]}`

func TestSlotsFileAtStart(t *testing.T) {
	// The USB pinned to slot 1, slot 2 off: the HDMI takes the first auto slot, the bars the next one,
	// and the network camera finds no room (it stays in the catalogue)
	file := `{"v":1,"slots":[{"slot":1,"assign":"` + usbA + `","alias":"Main camera"},{"slot":2,"assign":"off"}]}`
	e, _, _ := startWithSlots(t, map[string]string{"SOURCE": "test"}, file, catAll)
	if want := usbA + ",-,hdmi,test"; slotIDs(e) != want {
		t.Fatalf("slots %s, want %s", slotIDs(e), want)
	}
	st := e.state()
	if len(st.SlotConfig) != 4 || st.SlotConfig[0].Alias != "Main camera" || !st.SlotConfig[0].Present ||
		st.SlotConfig[1].Assign != "off" || st.SlotConfig[1].Present || !st.SlotConfig[3].Present {
		t.Errorf("slotConfig: %+v", st.SlotConfig)
	}
	if len(st.Sources) != 3 || st.Sources[0].Alias != "Main camera" || st.Sources[1].Alias != "" {
		t.Errorf("sources: %+v", st.Sources)
	}
	var ids []string
	for _, c := range st.Catalog {
		ids = append(ids, c.ID+":"+c.Kind)
	}
	if strings.Join(ids, ",") != "hdmi:hdmi-csi,test:test,"+usbA+":usb,net:cam1:network" {
		t.Errorf("catalog: %v", ids)
	}
	b, _ := json.Marshal(st)
	for _, k := range []string{`"slotConfig":[{"slot":1,"assign":"`, `"present":true`, `"catalog":[{"id":"hdmi","name":`, `"alias":"Main camera"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("state JSON lacks %s", k)
		}
	}
}

func TestSlotsFileInvalid(t *testing.T) {
	for _, file := range []string{
		`{"v":1,"slots":[{"slot":1,"assign":"off"},{"slot":1,"assign":"auto"}]}`,
		`{"v":2,"slots":[]}`,
		`{"slots":{}}`,
		`not json`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, slotsFile), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		log := &strings.Builder{}
		elog.SetOutput(log)
		c := testConfig(t, map[string]string{"EIRL_CONF": filepath.Join(dir, "engine.env")})
		e := &Engine{cfg: c, nslots: 4}
		e.loadSlots()
		elog.SetOutput(nil)
		for _, s := range e.slotCfg {
			if s.Assign != AssignAuto || s.Alias != "" {
				t.Errorf("%s: %+v, want all auto", file, e.slotCfg)
			}
		}
		if !strings.Contains(log.String(), "event=slots_config_invalid") {
			t.Errorf("%s: no event: %q", file, log.String())
		}
	}
}

func TestReservedSlot(t *testing.T) {
	// Slot 2 waits for the network camera: no other source takes it, and it shows black
	file := `{"v":1,"slots":[{"slot":2,"assign":"net:cam1"}]}`
	cat := `{"sources":[{"id":"tc","kind":"hdmi-csi","node":"/dev/video0"},{"id":"` + usbA + `","kind":"usb","name":"A"},
		{"id":"/dev/v4l/by-id/usb-B-video-index0","kind":"usb","name":"B"}],"audio":[]}`
	e, r, log := startWithSlots(t, nil, file, cat)
	if want := "hdmi,-," + usbA + ",/dev/v4l/by-id/usb-B-video-index0"; slotIDs(e) != want {
		t.Fatalf("slots %s, want %s", slotIDs(e), want)
	}
	r.mu.Lock()
	r.list = `{"sources":[{"id":"tc","kind":"hdmi-csi","node":"/dev/video0"},{"id":"` + usbA + `","kind":"usb","name":"A"},
		{"id":"/dev/v4l/by-id/usb-B-video-index0","kind":"usb","name":"B"},{"id":"/dev/v4l/by-id/usb-C-video-index0","kind":"usb","name":"C"}]}`
	r.mu.Unlock()
	e.discovered(e.probeList())
	if want := "hdmi,-," + usbA + ",/dev/v4l/by-id/usb-B-video-index0"; slotIDs(e) != want || !strings.Contains(log.String(), "event=no_free_slot") {
		t.Fatalf("a fourth camera took the reserved slot: %s", slotIDs(e))
	}
	st := e.state()
	if st.SlotConfig[1].Present || len(st.Sources) != 3 || len(st.Catalog) != 4 {
		t.Errorf("state: %+v %d sources %d in the catalogue", st.SlotConfig, len(st.Sources), len(st.Catalog))
	}
	// The camera appears: straight to its slot
	r.mu.Lock()
	r.list = strings.TrimSuffix(r.list, "]}") + `,{"id":"net:cam1","kind":"network","node":"rtsp://127.0.0.1:8554/cam1","name":"Phone"}]}`
	r.mu.Unlock()
	e.discovered(e.probeList())
	if want := "hdmi,net:cam1," + usbA + ",/dev/v4l/by-id/usb-B-video-index0"; slotIDs(e) != want {
		t.Fatalf("slots %s, want %s", slotIDs(e), want)
	}
}

func TestSlotsCommand(t *testing.T) {
	e, _, log := startWithSlots(t, map[string]string{"SOURCE": "test"}, "", catAll)
	if want := "hdmi,test," + usbA + ",net:cam1"; slotIDs(e) != want {
		t.Fatalf("slots %s, want %s", slotIDs(e), want)
	}
	oldUSB, oldHDMI := e.sources[2], e.sources[0]
	cmd := func(list string) (bool, string) {
		return e.command("slots", map[string]any{"cmd": "slots", "slots": decodeList(t, list)})
	}
	// Bad: nothing changes
	if ok, code := cmd(`[{"slot":1,"assign":"nope"}]`); ok || code != "bad_request" {
		t.Errorf("bad assign: %v %q", ok, code)
	}
	if ok, code := e.command("slots", map[string]any{"cmd": "slots"}); ok || code != "bad_request" {
		t.Errorf("no slots: %v %q", ok, code)
	}
	// The USB pinned to slot 1: the HDMI leaves slot 1 for the first auto slot left (3, the USB's), and
	// both new sources wait for the old ones (a device is opened once; a bridge has one owner)
	if ok, code := cmd(`[{"slot":1,"assign":"` + usbA + `","alias":"Main"},{"slot":4,"assign":"off"}]`); !ok || code != "" {
		t.Fatalf("slots: %v %q", ok, code)
	}
	if want := usbA + ",test,hdmi,-"; slotIDs(e) != want {
		t.Fatalf("slots %s, want %s", slotIDs(e), want)
	}
	if !oldUSB.stopping() || !oldHDMI.stopping() {
		t.Error("the moved sources were not stopped")
	}
	has := func(s *Source, p ...*Source) bool {
		for _, want := range p {
			found := false
			for _, q := range s.prev {
				found = found || q == want
			}
			if !found {
				return false
			}
		}
		return true
	}
	if !has(e.sources[0], oldHDMI, oldUSB) || !has(e.sources[2], oldUSB, oldHDMI) {
		t.Errorf("prev: %v %v", e.sources[0].prev, e.sources[2].prev)
	}
	if !strings.Contains(log.String(), "event=source_moved_out") || !strings.Contains(log.String(), `event=command`) {
		t.Errorf("log: %q", log.String())
	}
	st := e.state()
	if st.Sources[0].Alias != "Main" || st.SlotConfig[3].Assign != "off" || st.SlotConfig[3].Present {
		t.Errorf("state: %+v %+v", st.Sources, st.SlotConfig)
	}
	// The same again: nothing moves
	cur := e.sources[0]
	if ok, _ := cmd(`[{"slot":1,"assign":"` + usbA + `","alias":"Main"},{"slot":4,"assign":"off"}]`); !ok || e.sources[0] != cur {
		t.Error("an unchanged configuration restarted a source")
	}
	// Only an alias: nothing moves either
	if ok, _ := cmd(`[{"slot":1,"assign":"` + usbA + `","alias":"Front"},{"slot":4,"assign":"off"}]`); !ok || e.sources[0] != cur || e.state().Sources[0].Alias != "Front" {
		t.Error("an alias change restarted a source")
	}
	// Back to all auto: the network camera comes back to the free slot
	if ok, _ := cmd(`[]`); !ok || slotIDs(e) != usbA+",test,hdmi,net:cam1" {
		t.Errorf("all auto: %s", slotIDs(e))
	}
}

func TestSlotsCommandOnAir(t *testing.T) {
	e, _, _ := startWithSlots(t, map[string]string{"SOURCE": "test"}, "", catAll)
	if ok, code := e.command("output", map[string]any{"on": true}); !ok || code != "" {
		t.Fatalf("output: %v %q", ok, code)
	}
	cmd := func(list string) (bool, string) {
		return e.command("slots", map[string]any{"cmd": "slots", "slots": decodeList(t, list)})
	}
	// The main source (hdmi, slot 1) cannot move or go off while emitting
	for _, l := range []string{
		`[{"slot":1,"assign":"off"}]`,
		`[{"slot":1,"assign":"test"}]`,
		`[{"slot":3,"assign":"hdmi"}]`,
	} {
		before := slotIDs(e)
		if ok, code := cmd(l); ok || code != "on_air" || slotIDs(e) != before {
			t.Errorf("%s: %v %q %s", l, ok, code, slotIDs(e))
		}
	}
	// What is not on air can move; an alias for the main one is fine
	if ok, code := cmd(`[{"slot":1,"assign":"hdmi","alias":"Stage"},{"slot":4,"assign":"` + usbA + `"}]`); !ok || code != "" {
		t.Fatalf("off-air move: %v %q", ok, code)
	}
	if want := "hdmi,test,net:cam1," + usbA; slotIDs(e) != want {
		t.Errorf("slots %s, want %s", slotIDs(e), want)
	}
	// The secondary in PiP is on air too; in full it is not
	if ok, code := e.command("secondary", map[string]any{"cmd": "secondary", "source": "test"}); !ok {
		t.Fatalf("secondary: %q", code)
	}
	if ok, code := cmd(`[{"slot":1,"assign":"hdmi","alias":"Stage"},{"slot":2,"assign":"off"}]`); ok || code != "" {
		// full: the secondary is not seen, so it may go
		if !ok {
			t.Errorf("the secondary in full: %v %q", ok, code)
		}
	}
	if ok, _ := cmd(`[{"slot":1,"assign":"hdmi","alias":"Stage"}]`); !ok || e.sources[1] == nil || e.sources[1].id != "test" {
		t.Fatalf("back to auto: %s", slotIDs(e))
	}
	if ok, code := e.command("secondary", map[string]any{"cmd": "secondary", "source": "test"}); !ok {
		t.Fatalf("secondary: %q", code)
	}
	if ok, code := e.command("layout", map[string]any{"cmd": "layout", "layout": "pip"}); !ok {
		t.Fatalf("pip: %q", code)
	}
	if ok, code := cmd(`[{"slot":1,"assign":"hdmi","alias":"Stage"},{"slot":2,"assign":"off"}]`); ok || code != "on_air" {
		t.Errorf("the PiP's secondary: %v %q", ok, code)
	}
	// Not emitting: anything goes
	if ok, _ := e.command("output", map[string]any{"on": false}); !ok {
		t.Fatal("output off")
	}
	if ok, code := cmd(`[{"slot":1,"assign":"off"}]`); !ok || code != "" || e.sources[0] != nil {
		t.Errorf("off air: %v %q %s", ok, code, slotIDs(e))
	}
}
