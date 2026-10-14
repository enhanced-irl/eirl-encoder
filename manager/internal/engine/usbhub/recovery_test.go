package usbhub

import (
	"errors"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/detail"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

var t0 = time.Date(2026, 10, 13, 2, 0, 0, 0, time.UTC)

func at(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

// UsbTest.test_recovery
func TestRecovery(t *testing.T) {
	r := &Recovery{}
	r.Update(true, at(0), "1-2.1")
	r.Update(false, at(10), "")
	if r.Due(at(25), true) { // 15 s without frames: not yet
		t.Error("15 s")
	}
	if !r.Due(at(30), true) { // 20 s
		t.Error("20 s")
	}
	if r.Due(at(30), false) { // unplugged: never
		t.Error("unplugged")
	}
	r.Done(at(30))
	r.Update(false, at(31), "")
	if r.Due(at(60), true) { // once every 2 min
		t.Error("gap")
	}
	if !r.Due(at(151), true) {
		t.Error("after the gap")
	}
	r.Done(at(151))
	r.Update(false, at(152), "")
	r.Done(at(272))
	if r.Due(at(500), true) { // three in a row: gives up
		t.Error("given up")
	}
	if !r.GivenUp() {
		t.Error("GivenUp")
	}
	r.Update(true, at(501), "")
	if r.GivenUp() {
		t.Error("live again")
	}
	r.Update(true, at(561), "") // a minute live: counts back to zero
	if r.count != 0 {
		t.Errorf("count %d", r.count)
	}
	if (&Recovery{}).Due(at(100), true) { // without a known device, no
		t.Error("no device")
	}
}

// UsbTest.test_recovery_note
func TestRecoveryNote(t *testing.T) {
	r := &Recovery{}
	r.Update(true, at(0), "1-2.1")
	if r.Note() != "" {
		t.Error("live")
	}
	r.Update(false, at(1), "")
	if r.Note() != "" {
		t.Error("down, nothing held")
	}
	r.held = []OnAir{{ID: "/dev/v4l/by-id/usb-Logi-video-index0", Name: "Logi"}}
	if got := r.Note(); got != "colgada: reiniciarla apagaría Logi (al aire)" {
		t.Errorf("held: %q", got)
	}
	r.Update(true, at(2), "") // comes back by itself: nothing to say
	if r.Note() != "" || r.held != nil {
		t.Errorf("back: %q %v", r.Note(), r.held)
	}
	r.Update(false, at(3), "")
	r.held = []OnAir{unknownOnAir}
	if got := r.Note(); got != "colgada: emitiendo no la reinicio (no sé qué hay al aire)" {
		t.Errorf("unknown: %q", got)
	}
	r.count = MaxCuts
	if got := r.Note(); got != "colgada: desenchúfala y vuelve a enchufarla" {
		t.Errorf("given up: %q", got)
	}
	r.held = []OnAir{{ID: "hdmi-cap", Name: "CAP"}, {ID: "alsa:Logi", Name: "Logi", Audio: true}}
	r.count = 0
	if got := r.Note(); got != "colgada: reiniciarla apagaría CAP, el audio de Logi (al aire)" {
		t.Errorf("two held: %q", got)
	}
	if r.NoteFor("unsupported").Code != "" || r.NoteFor("gone").Code != "hung_on_air" || r.NoteFor("error").Code != "hung_on_air" {
		t.Error("NoteFor")
	}
	if a := r.NoteFor("error").Args; len(a) != 2 || a["video"] != "hdmi-cap" || a["audio"] != "alsa:Logi" {
		t.Errorf("NoteFor args: %v", a)
	}
}

// fakeHost is the source and the engine for Recover.
type fakeHost struct {
	on     bool
	air    map[string]OnAir
	broken bool // the engine cannot tell what is on air
	states [][2]string
	waited time.Duration
}

func (h *fakeHost) SetState(state string, d detail.D) {
	h.states = append(h.states, [2]string{state, d.Text})
}
func (h *fakeHost) Wait(d time.Duration) bool { h.waited += d; return false }
func (h *fakeHost) OnAirIfEmitting() (map[string]OnAir, error) {
	if h.broken {
		return nil, errors.New("engine not answering")
	}
	if !h.on {
		return nil, nil
	}
	return maps.Clone(h.air), nil
}

func (h *fakeHost) last() [2]string { return h.states[len(h.states)-1] }

// cuts counts the power cuts of the Logi's port (2-1, port 4).
func cuts(f *fakeHub) int {
	n := 0
	for _, c := range f.calls {
		if c == node21+" 4 false" {
			n++
		}
	}
	return n
}

// MixerTest.test_usb_recover
func TestUsbRecover(t *testing.T) {
	var logs strings.Builder
	elog.SetOutput(&logs)
	t.Cleanup(func() { elog.SetOutput(nil) })
	s, f := newFake(t)
	hubs := NewHubs(s)
	r := NewRecovery(hubs)
	h := &fakeHost{}
	const name, dev = "Logi", "2-1.4"
	r.Update(true, at(0), dev)
	try := func(state string, sec float64) bool { return r.Recover(h, name, state, dev, at(sec)) }

	if try("error", 5) { // just dropped
		t.Fatal("just dropped")
	}
	// 21 s without frames and plugged, but emitting with the capture card on the same hub on air: waits
	h.on, h.air = true, map[string]OnAir{"1-2.1": {ID: "cap", Name: "capturadora"}}
	if try("error", 26) || cuts(f) != 0 {
		t.Fatal("cut with the capture card on air")
	}
	if h.last() != [2]string{"error", "colgada: reiniciarla apagaría capturadora (al aire)"} {
		t.Errorf("state %v", h.last())
	}
	if !strings.Contains(logs.String(), `[mixer] event=usb_recovery_held source=Logi on_air=capturadora`) {
		t.Errorf("log: %s", logs.String())
	}
	h.air = map[string]OnAir{"3-1": {ID: "other", Name: "otra"}} // what is on air is no longer under the hub: cuts
	if !try("error", 27) || cuts(f) != 1 {
		t.Fatal("no cut")
	}
	if r.count != 1 { // the wait spends no attempts
		t.Errorf("count %d", r.count)
	}
	if h.last() != [2]string{"starting", "reiniciando su puerto USB"} || h.waited != SettleFor {
		t.Errorf("state %v, waited %v", h.last(), h.waited)
	}
	want := "[mixer] event=usb_power_cycle source=Logi stuck_s=20 plan=\"2-1.4: port 4 of 2-1+1-2\" attempt=1 max=3\n"
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log: %s", logs.String())
	}
	if try("error", 60) { // not before 2 min
		t.Fatal("gap")
	}
	h.broken = true // not knowing what is on air: while emitting, no cut
	if try("error", 200) || cuts(f) != 1 {
		t.Fatal("cut without knowing what is on air")
	}
	if !strings.Contains(h.last()[1], "no sé qué hay al aire") {
		t.Errorf("state %v", h.last())
	}
	if !strings.Contains(logs.String(), `[mixer] event=usb_recovery_held_unknown source=Logi err="engine not answering"`) {
		t.Errorf("log: %s", logs.String())
	}
	*h = fakeHost{}
	// Another source has just cut the same hub: no second cut (that cut restarted this one too)
	hubs.cuts["1-2"] = at(201)
	if try("error", 205) || cuts(f) != 1 {
		t.Fatal("cut twice")
	}
	if !r.downSince.Equal(at(201)) {
		t.Errorf("down since %v", r.downSince.Sub(t0))
	}
	if !try("error", 222) || cuts(f) != 2 { // 21 s since that cut: its turn
		t.Fatal("no cut after the other's")
	}
	// Outside error/gone (e.g. "does not fit in the bus") it does not count as hung
	if try("unsupported", 223) || !r.downSince.IsZero() {
		t.Fatal("unsupported")
	}
	// Unplugged: never
	if err := os.RemoveAll(filepath.Join(s.Root, "sys/bus/usb/devices", dev)); err != nil {
		t.Fatal(err)
	}
	if try("error", 300) || try("error", 500) || cuts(f) != 2 {
		t.Fatal("cut unplugged")
	}
}

// New: given up after MaxCuts, said in the state; a plan that cannot be done gives up at once.
func TestUsbRecoverGivesUp(t *testing.T) {
	var logs strings.Builder
	elog.SetOutput(&logs)
	t.Cleanup(func() { elog.SetOutput(nil) })
	s, f := newFake(t)
	r := NewRecovery(NewHubs(s))
	h := &fakeHost{}
	sec := 0.0
	for range MaxCuts {
		r.Recover(h, "Logi", "error", "2-1.4", at(sec))
		sec += Gap.Seconds()
		r.Recover(h, "Logi", "error", "2-1.4", at(sec))
	}
	if cuts(f) != MaxCuts {
		t.Fatalf("cuts %d", cuts(f))
	}
	if r.Recover(h, "Logi", "error", "2-1.4", at(sec+Gap.Seconds())) || h.last() != [2]string{"error", "colgada: desenchúfala y vuelve a enchufarla"} {
		t.Errorf("given up: %v", h.last())
	}
	// The touch screen is never cut: impossible, counts as given up
	r2 := NewRecovery(NewHubs(s))
	r2.Recover(h, "Pantalla", "error", "1-2.3", at(0))
	if r2.Recover(h, "Pantalla", "error", "1-2.3", at(30)) || r2.count != MaxCuts {
		t.Errorf("impossible: count %d", r2.count)
	}
	if !strings.Contains(logs.String(), `[mixer] event=usb_recovery_impossible source=Pantalla plan="1-2.3: not a camera or a capture card"`) {
		t.Errorf("log: %s", logs.String())
	}
	// Hidden: the wait is forgotten
	r2.Forget()
	if r2.Note() != "" {
		t.Error("forget")
	}
}

// New: a panic inside (a broken host) is logged and the slot goes on.
func TestUsbRecoverPanic(t *testing.T) {
	var logs strings.Builder
	elog.SetOutput(&logs)
	t.Cleanup(func() { elog.SetOutput(nil) })
	r := &Recovery{} // without hubs: Plugged panics on the nil pointer
	if r.Recover(&fakeHost{}, "Logi", "error", "2-1.4", at(0)) {
		t.Fatal("cut")
	}
	if !strings.Contains(logs.String(), "[mixer] event=usb_recovery_failed source=Logi") {
		t.Errorf("log: %s", logs.String())
	}
}

// MixerTest.test_usb_on_air
func TestUsbOnAir(t *testing.T) {
	now := at(1000)
	cap := VideoSlot{ID: "cap", Name: "CAP", Dev: "1-2.1", State: "live"}
	logi := VideoSlot{ID: "logi", Name: "LOGI", Dev: "2-1.4", State: "live"}
	hdmi := VideoSlot{ID: "hdmi", Name: "HDMI", State: "live"}
	air := Air{Program: Program{Main: "cap", Layout: "full"}, Now: now}
	set := func() { air.Videos = []VideoSlot{hdmi, logi, cap} }
	set()
	noCard := func(string) string { t.Fatal("no audio"); return "" }
	check := func(what string, me VideoSlot, want map[string]OnAir, card func(string) string) {
		t.Helper()
		if got := air.Devices(me.ID, me.Dev, card); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", what, got, want)
		}
	}
	check("main", logi, map[string]OnAir{"1-2.1": {ID: "cap", Name: "CAP"}}, noCard)
	check("itself", cap, map[string]OnAir{}, noCard)
	air.Program.Secondary = "logi"
	check("full screen", cap, map[string]OnAir{}, noCard)
	air.Program.Layout = "pip"
	check("pip", cap, map[string]OnAir{"2-1.4": {ID: "logi", Name: "LOGI"}}, noCard)
	logi.State = "error"
	set()
	check("no frames", cap, map[string]OnAir{}, noCard)
	logi.State, logi.LastFrame = "starting", now.Add(-5*time.Second)
	set()
	check("reopening after a frame 5 s ago", cap, map[string]OnAir{"2-1.4": {ID: "logi", Name: "LOGI"}}, noCard)
	logi.State, logi.LastFrame = "error", time.Time{}
	set()
	// The audio that sounds is also "on air"; the one at 0 is not
	air.Audios = []AudioSlot{
		{ID: "alsa:Logi", Kind: "usb", State: "live", Name: "Logi", Volume: 1},
		{ID: "alsa:MS2109", Kind: "usb", State: "live", Name: "MS2109", Volume: 0},
		{ID: "i2s", Kind: "i2s", State: "live", Name: "HDMI de la placa", Volume: 1},
	}
	card := func(c string) string { return map[string]string{"Logi": "2-1.4", "MS2109": "1-2.1"}[c] }
	check("audio", cap, map[string]OnAir{"2-1.4": {ID: "alsa:Logi", Name: "Logi", Audio: true}}, card)
	check("its own audio does not count", logi, map[string]OnAir{"1-2.1": {ID: "cap", Name: "CAP"}}, card)
}
