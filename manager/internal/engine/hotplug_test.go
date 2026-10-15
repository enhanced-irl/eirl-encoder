package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/usbhub"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst/fake"
)

// shortWaits makes an unplugged camera's waits short for a test.
func shortWaits(t *testing.T) {
	g, c, s := unplugGrace, cutSettle, settleProbe
	unplugGrace, cutSettle, settleProbe = 50*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { unplugGrace, cutSettle, settleProbe = g, c, s })
}

// slots is what slotIDs should say: the ids, "-" for an empty slot.
func slots(ids ...string) string { return strings.Join(ids, ",") }

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

const (
	usbB = "/dev/v4l/by-id/usb-B-video-index0"
	usbC = "/dev/v4l/by-path/platform-70090000.xusb-usb-0:2.3:1.0-video-index0"
)

func usbCatalogue(ids ...string) string {
	parts := []string{`{"id":"tc","kind":"hdmi-csi","node":"/dev/video0","name":"HDMI"}`}
	for _, id := range ids {
		name := map[string]string{usbA: "A", usbB: "B", usbC: "C"}[id]
		parts = append(parts, `{"id":"`+id+`","kind":"usb","name":"`+name+`"}`)
	}
	return `{"sources":[` + strings.Join(parts, ",") + `],"audio":[]}`
}

// A camera unplugged leaves its slot (black, empty) after the grace; one on air stays with its slate until it
// is taken off air; they come back to their own slots if free, placed before a new one.
func TestUnpluggedCameraLeavesItsSlot(t *testing.T) {
	shortWaits(t)
	logs := &lockedLog{}
	e, m, r := startEngine(t, nil, usbCatalogue(usbA, usbB))
	elog.SetOutput(logs)
	if got := slotIDs(e); got != slots("hdmi", usbA, usbB, "-") {
		t.Fatalf("slots %s", got)
	}
	a, b := e.sources[1], e.sources[2]
	// Neither their node nor their device is here: unplugged
	waitUntil(t, "unplugged", func() bool { return a.unpluggedFor(time.Now()) > 0 && b.unpluggedFor(time.Now()) > 0 })
	if ok, code := e.command("take", map[string]any{"cmd": "take", "source": usbA}); !ok {
		t.Fatalf("take: %s", code)
	}
	grace := unplugGrace
	unplugGrace = time.Hour
	e.dropUnplugged()
	if got := slotIDs(e); got != slots("hdmi", usbA, usbB, "-") {
		t.Fatalf("dropped within the grace: %s", got)
	}
	unplugGrace = grace
	time.Sleep(unplugGrace)
	blacks := len(m.Bridges[2].Stills)
	e.dropUnplugged()
	if got := slotIDs(e); got != slots("hdmi", usbA, "-", "-") {
		t.Fatalf("B dropped and A (on air) kept: %s", got)
	}
	if !waitDone(b.stopCh, 0) || len(m.Bridges[2].Stills) <= blacks || e.lastSlot[usbB] != 2 {
		t.Errorf("B: stopped %v, stills %d→%d, remembered %v", waitDone(b.stopCh, 0), blacks, len(m.Bridges[2].Stills), e.lastSlot)
	}
	if !strings.Contains(logs.String(), `event=source_removed source=B slot=3 reason=unplugged`) {
		t.Errorf("log: %s", logs.String())
	}
	// Off air, A goes too
	if ok, code := e.command("take", map[string]any{"cmd": "take", "source": "hdmi"}); !ok {
		t.Fatalf("take: %s", code)
	}
	e.dropUnplugged()
	if got := slotIDs(e); got != slots("hdmi", "-", "-", "-") || e.lastSlot[usbA] != 1 {
		t.Fatalf("A not dropped off air: %s %v", got, e.lastSlot)
	}
	// B comes back with a new C listed before it: B to its slot 3 first, C to the first free one (A's 2)
	r.mu.Lock()
	r.list = usbCatalogue(usbC, usbB)
	r.mu.Unlock()
	e.discovered(e.probeList())
	if got := slotIDs(e); got != slots("hdmi", usbC, usbB, "-") {
		t.Fatalf("back: %s", got)
	}
	// The new B waits for the old one to end before taking the slot's bridge
	if nb := e.sources[2]; nb == b || len(nb.prev) == 0 && !waitDone(b.done, 0) {
		t.Errorf("new B: %p prev %d", nb, len(nb.prev))
	}
	r.mu.Lock()
	r.list = usbCatalogue(usbC, usbB, usbA)
	r.mu.Unlock()
	e.discovered(e.probeList())
	// A's slot is taken now: it takes the free one
	if got := slotIDs(e); got != slots("hdmi", usbC, usbB, usbA) || len(e.lastSlot) != 0 {
		t.Fatalf("A back: %s %v", got, e.lastSlot)
	}
}

// Right after a hub's power cut everything under it is off the bus for a few seconds: nothing is dropped
// until it settles.
func TestUnpluggedHeldAfterAHubCut(t *testing.T) {
	shortWaits(t)
	e, _, _ := startEngine(t, nil, usbCatalogue(usbA))
	a := e.sources[1]
	waitUntil(t, "unplugged", func() bool { return a.unpluggedFor(time.Now()) > unplugGrace })
	e.hubs.Claim(usbhub.Plan{Hubs: []string{"2-1"}}, time.Now(), usbhub.StuckFor)
	e.dropUnplugged()
	if got := slotIDs(e); got != slots("hdmi", usbA, "-", "-") {
		t.Fatalf("dropped during the cut: %s", got)
	}
	time.Sleep(cutSettle)
	e.dropUnplugged()
	if got := slotIDs(e); got != slots("hdmi", "-", "-", "-") {
		t.Fatalf("not dropped after the cut settled: %s", got)
	}
}

// A hidden camera that is unplugged goes too (and comes back hidden: the scene keeps it by id).
func TestHiddenUnpluggedCameraLeaves(t *testing.T) {
	shortWaits(t)
	e, _, _ := startEngine(t, nil, usbCatalogue(usbA))
	if ok, code := e.command("hide", map[string]any{"cmd": "hide", "source": usbA, "hidden": true}); !ok {
		t.Fatalf("hide: %s", code)
	}
	a := e.sources[1]
	waitUntil(t, "hidden and unplugged", func() bool {
		st, _, _ := a.snapshot()
		return st == StateHidden && a.unpluggedFor(time.Now()) > unplugGrace
	})
	e.dropUnplugged()
	if got := slotIDs(e); got != slots("hdmi", "-", "-", "-") {
		t.Fatalf("hidden one kept: %s", got)
	}
}

// physicallyGone tells an unplugged camera from one that only dropped off the bus on its port (the
// recovery's case), by what is on that port.
func TestPhysicallyGone(t *testing.T) {
	root := t.TempDir()
	devs := filepath.Join(root, "sys/bus/usb/devices")
	put := func(dev string, kv ...string) {
		d := filepath.Join(devs, dev)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i+1 < len(kv); i += 2 {
			if err := os.WriteFile(filepath.Join(d, kv[i]), []byte(kv[i+1]+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	put("2-1", "busnum", "2", "devnum", "2", "bDeviceClass", "09")
	var mu sync.Mutex
	status, fail := uint16(0), false
	control := func(node string, reqType, req uint8, value, index uint16, length int) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail || node != "/dev/bus/usb/002/002" || index != 4 {
			return nil, os.ErrPermission
		}
		return []byte{byte(status), byte(status >> 8), 0, 0}, nil
	}
	elog.SetOutput(&strings.Builder{})
	t.Cleanup(func() { elog.SetOutput(nil) })
	e := New(Options{Config: testConfig(t, nil), Media: fake.New(), Platform: pipeline.Tegra, Run: (&runner{}).run,
		MixerMain: "hdmi", FPS: 60, USB: usbhub.Sys{Root: root, Control: control, Sleep: func(time.Duration) {}}})
	s := &Source{e: e, kind: "usb", id: usbA}
	set := func(dev, ident string) { s.usbDev, s.usbIdent = dev, ident }
	check := func(what string, want bool) {
		t.Helper()
		if got := s.physicallyGone(); got != want {
			t.Errorf("%s: gone %v, want %v", what, got, want)
		}
	}
	check("never seen on the bus", true)
	set("2-1.4", "046d:086b:0B2CFE13")
	put("2-1.4", "idVendor", "046d", "idProduct", "086b", "serial", "0B2CFE13")
	check("itself on its port, without its node yet", false)
	put("2-1.4", "idVendor", "534d", "idProduct", "2109", "serial", "")
	check("another device on its port", true)
	if err := os.RemoveAll(filepath.Join(devs, "2-1.4")); err != nil {
		t.Fatal(err)
	}
	check("port empty", true)
	mu.Lock()
	status = 1
	mu.Unlock()
	check("dropped off the bus, still on its port (hung)", false)
	mu.Lock()
	fail = true
	mu.Unlock()
	check("the hub cannot tell", true)
	set("2-1.4", "")
	put("2-1.4", "idVendor", "534d", "idProduct", "2109")
	check("on its port, identity never read", false)
}

// by-path counts as a change (a second capture card with the same serial), and a change brings a second
// probe a little later, for what was still binding the first time.
func TestDiscoverWatchesByPathAndSettles(t *testing.T) {
	shortWaits(t)
	dir := t.TempDir()
	byID, byPath := filepath.Join(dir, "by-id"), filepath.Join(dir, "by-path")
	for _, d := range []string{byID, byPath} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := devDirs
	devDirs = []string{byID, byPath}
	t.Cleanup(func() { devDirs = old })
	e, _, r := startEngine(t, nil, catalogue)
	e.disc.devs = listDevices()
	r.mu.Lock()
	r.listCalls = 0
	r.mu.Unlock()
	calls := func() int { r.mu.Lock(); defer r.mu.Unlock(); return r.listCalls }
	// tick runs a discovery tick and applies its probe as the actor would
	tick := func() bool {
		e.discoverTick()
		if !e.disc.probing {
			return false
		}
		select {
		case fn := <-e.events:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal("the probe never reached the actor")
		}
		return true
	}
	e.audio.CardsChanged() // nothing pending from the start
	if tick() {
		t.Fatal("probed with nothing changed")
	}
	if err := os.WriteFile(filepath.Join(byPath, "platform-70090000.xusb-usb-0:2.4:1.0-video-index0"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !tick() || calls() != 1 {
		t.Fatalf("a new by-path link: calls %d", calls())
	}
	if tick() {
		t.Fatal("the second probe came before settleProbe")
	}
	time.Sleep(settleProbe)
	if !tick() || calls() != 2 {
		t.Fatalf("no second probe: calls %d", calls())
	}
	if tick() {
		t.Fatal("the second probe brought a third")
	}
}

// With every slot taken a new camera waits without one; when another is unplugged it gets the freed slot
// at once, not at the next change of the catalogue (which may still list the unplugged one).
func TestWaitingCameraTakesTheFreedSlot(t *testing.T) {
	shortWaits(t)
	cat := `{"sources":[{"id":"tc","kind":"hdmi-csi","node":"/dev/video0"},
		{"id":"net:cam1","kind":"network","node":"rtsp://127.0.0.1:8554/cam1","name":"Phone 1"},
		{"id":"net:cam2","kind":"network","node":"rtsp://127.0.0.1:8554/cam2","name":"Phone 2"},
		{"id":"` + usbA + `","kind":"usb","name":"A"},{"id":"` + usbB + `","kind":"usb","name":"B"}],"audio":[]}`
	e, _, _ := startEngine(t, nil, cat)
	if got := slotIDs(e); got != slots("hdmi", "net:cam1", "net:cam2", usbA) {
		t.Fatalf("slots %s", got)
	}
	a := e.sources[3]
	waitUntil(t, "A unplugged", func() bool { return a.unpluggedFor(time.Now()) > unplugGrace })
	e.dropUnplugged()
	if got := slotIDs(e); got != slots("hdmi", "net:cam1", "net:cam2", usbB) {
		t.Fatalf("B did not take A's slot: %s", got)
	}
}

// audioIDs are the audio inputs in their slots' order.
func audioIDs(e *Engine) string {
	var ids []string
	for _, in := range e.audio.Inputs() {
		ids = append(ids, in.ID)
	}
	return strings.Join(ids, ",")
}

// A camera's own audio is opened only while the camera has a video slot (Oct 15: cameras are detected without
// a limit, only four are used); a loose USB sound card always, behind the cameras'; in the video slots' order.
func TestAudioFollowsVideoSlots(t *testing.T) {
	usbD := "/dev/v4l/by-id/usb-D-video-index0"
	cam := func(id, card string) string {
		return `{"id":"` + id + `","kind":"usb","name":"` + card + `","audio":{"id":"alsa:` + card + `","kind":"usb","name":"` + card + `"}}`
	}
	card := func(c string) string { return `{"id":"alsa:` + c + `","kind":"usb","name":"` + c + `"}` }
	cat := `{"sources":[{"id":"tc","kind":"hdmi-csi","node":"/dev/video0"},` + cam(usbA, "CA") + `,` + cam(usbC, "CC") + `,` +
		cam(usbD, "CD") + `],"audio":[{"id":"i2s","kind":"i2s","name":"HDMI"},` + card("Mic") + `,` + card("CD") + `,` +
		card("CC") + `,` + card("CA") + `]}`
	e, _, _ := startEngine(t, nil, cat)
	if got := slotIDs(e); got != slots("hdmi", usbA, usbC, usbD) {
		t.Fatalf("video %s", got)
	}
	// Four audio slots: the cameras' first, in their video slots' order; the loose Mic does not fit
	if got := audioIDs(e); got != "i2s,alsa:CA,alsa:CC,alsa:CD" {
		t.Fatalf("audio %s", got)
	}
	// Slot 4 off: D leaves it and its audio goes; the Mic takes that audio slot
	e.slotCfg[3] = SlotConfig{Slot: 4, Assign: AssignOff}
	e.reassign()
	if got := audioIDs(e); got != "i2s,alsa:CA,alsa:CC,alsa:Mic" {
		t.Fatalf("D without a slot: audio %s", got)
	}
	// Slot 3 pinned to D: C leaves it (no slot left for it), D comes in, and so do their audios
	e.slotCfg[2] = SlotConfig{Slot: 3, Assign: usbD}
	e.reassign()
	if got := slotIDs(e); got != slots("hdmi", usbA, usbD, "-") {
		t.Fatalf("video %s", got)
	}
	if got := audioIDs(e); got != "i2s,alsa:CA,alsa:CD,alsa:Mic" {
		t.Fatalf("C without a slot, D with one: audio %s", got)
	}
	// A given D's audio by hand: when D leaves its slot, D's audio stays (A uses it) and so does A's own
	if ok, code := e.command("audio_link", map[string]any{"cmd": "audio_link", "video": usbA, "source": "alsa:CD"}); !ok {
		t.Fatalf("audio_link: %s", code)
	}
	e.slotCfg[2] = SlotConfig{Slot: 3, Assign: AssignOff}
	e.reassign()
	if got := slotIDs(e); got != slots("hdmi", usbA, "-", "-") {
		t.Fatalf("video %s", got)
	}
	if got := audioIDs(e); got != "i2s,alsa:CA,alsa:CD,alsa:Mic" {
		t.Fatalf("D's audio linked to A: audio %s", got)
	}
}

// netSlots is a catalogue with the HDMI and two network cameras (never unplugged) in front of the USB ones.
func netSlots(ids ...string) string {
	parts := []string{`{"id":"tc","kind":"hdmi-csi","node":"/dev/video0"}`,
		`{"id":"net:cam1","kind":"network","node":"rtsp://127.0.0.1:8554/cam1","name":"Phone 1"}`,
		`{"id":"net:cam2","kind":"network","node":"rtsp://127.0.0.1:8554/cam2","name":"Phone 2"}`}
	for _, id := range ids {
		parts = append(parts, `{"id":"`+id+`","kind":"usb","name":"cam"}`)
	}
	return `{"sources":[` + strings.Join(parts, ",") + `],"audio":[]}`
}

// A camera that comes back while every slot is taken waits; when another camera leaves, it gets that slot.
func TestReturnedCameraWaitsForAFreedSlot(t *testing.T) {
	shortWaits(t)
	e, _, r := startEngine(t, nil, netSlots(usbA))
	list := func(c string) {
		r.mu.Lock()
		r.list = c
		r.mu.Unlock()
		e.discovered(e.probeList())
	}
	a := e.sources[3]
	waitUntil(t, "A unplugged", func() bool { return a.unpluggedFor(time.Now()) > unplugGrace })
	e.dropUnplugged()
	list(netSlots(usbB)) // B arrives and takes A's slot
	if got := slotIDs(e); got != slots("hdmi", "net:cam1", "net:cam2", usbB) {
		t.Fatalf("B in: %s", got)
	}
	list(netSlots(usbB, usbA)) // A is back: no slot left
	if got := slotIDs(e); got != slots("hdmi", "net:cam1", "net:cam2", usbB) {
		t.Fatalf("A waits: %s", got)
	}
	b := e.sources[3]
	waitUntil(t, "B unplugged", func() bool { return b.unpluggedFor(time.Now()) > unplugGrace })
	e.dropUnplugged()
	if got := slotIDs(e); got != slots("hdmi", "net:cam1", "net:cam2", usbA) {
		t.Fatalf("A did not take B's slot: %s", got)
	}
}

// A probe that started before a camera was dropped (it may have seen it still there) does not put it back;
// one that started afterwards does.
func TestStaleProbeDoesNotBringBackAnUnpluggedCamera(t *testing.T) {
	shortWaits(t)
	e, _, _ := startEngine(t, nil, usbCatalogue(usbA))
	a := e.sources[1]
	waitUntil(t, "A unplugged", func() bool { return a.unpluggedFor(time.Now()) > unplugGrace })
	e.dropUnplugged()
	e.disc.started = time.Now().Add(-time.Minute)
	e.discovered(e.probeList()) // still lists A
	if got := slotIDs(e); got != slots("hdmi", "-", "-", "-") {
		t.Fatalf("an older probe put A back: %s", got)
	}
	e.discovered(e.probeList()) // started now (zero: a direct call)
	if got := slotIDs(e); got != slots("hdmi", usbA, "-", "-") {
		t.Fatalf("a newer probe did not: %s", got)
	}
}

// With every audio slot taken, a loose sound card gives its slot to the audio of a camera that gets a video
// slot, unless it is the audio on air; the audio on air is never closed, also a camera's without a slot.
func TestLooseCardGivesItsAudioSlot(t *testing.T) {
	usbD := "/dev/v4l/by-id/usb-D-video-index0"
	cam := func(id, card string) string {
		return `{"id":"` + id + `","kind":"usb","name":"` + card + `","audio":{"id":"alsa:` + card + `"}}`
	}
	card := func(c string) string { return `{"id":"alsa:` + c + `","kind":"usb","name":"` + c + `"}` }
	cat := `{"sources":[{"id":"tc","kind":"hdmi-csi","node":"/dev/video0"},` + cam(usbA, "CA") + `,` + cam(usbC, "CC") + `,` +
		cam(usbD, "CD") + `],"audio":[{"id":"i2s","kind":"i2s"},` + card("Mic") + `,` + card("CA") + `,` + card("CC") + `,` +
		card("CD") + `]}`
	logs := &lockedLog{}
	e, _, _ := startEngine(t, nil, cat)
	elog.SetOutput(logs)
	audioCmd := func(src string) {
		t.Helper()
		if ok, code := e.command("audio", map[string]any{"cmd": "audio", "source": src}); !ok {
			t.Fatalf("audio %s: %s", src, code)
		}
	}
	// D leaves its slot: the Mic gets its audio slot, and goes on air
	e.slotCfg[3] = SlotConfig{Slot: 4, Assign: AssignOff}
	e.reassign()
	if got := audioIDs(e); got != "i2s,alsa:CA,alsa:CC,alsa:Mic" {
		t.Fatalf("audio %s", got)
	}
	audioCmd("alsa:Mic")
	// D back in its slot: the Mic is on air, so it keeps its audio slot and D has none
	e.slotCfg[3] = SlotConfig{Slot: 4, Assign: AssignAuto}
	e.reassign()
	if got := audioIDs(e); got != "i2s,alsa:CA,alsa:CC,alsa:Mic" {
		t.Fatalf("the Mic on air: audio %s", got)
	}
	// Off air, the Mic gives it up to D's
	audioCmd("i2s")
	e.syncAudio(e.lastCat)
	if got := audioIDs(e); got != "i2s,alsa:CA,alsa:CC,alsa:CD" {
		t.Fatalf("the Mic off air: audio %s", got)
	}
	if !strings.Contains(logs.String(), `event=audio_input_removed slot=4 input=Mic reason=no_audio_slot`) {
		t.Errorf("log: %s", logs.String())
	}
	// A's audio on air and A without a video slot: its audio stays
	audioCmd("alsa:CA")
	e.slotCfg[1] = SlotConfig{Slot: 2, Assign: AssignOff}
	e.reassign()
	if got := audioIDs(e); !strings.Contains(got, "alsa:CA") {
		t.Fatalf("the audio on air was closed: %s", got)
	}
}
