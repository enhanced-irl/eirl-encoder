package audio

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/detail"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/netcam"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/probe"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/scene"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/usbhub"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst/fake"
)

// programDesc is the audio part of the program (pipeline.Program) with n slots.
func programDesc(n int) string {
	var b strings.Builder
	b.WriteString("audiotestsrc is-live=true wave=silence ! " + Caps + " ! amix. ")
	for i := range n {
		b.WriteString("appsrc name=aa" + string(rune('0'+i)) + " ! queue name=aq" + string(rune('0'+i)) + " ! amix. ")
	}
	b.WriteString("audiomixer name=amix ! level name=pgmlevel ! tee name=at")
	return b.String()
}

type fakeNet struct {
	mu   sync.Mutex
	path netcam.Path
	err  error
}

func (n *fakeNet) State(string) (netcam.Path, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.path, n.err
}

func (n *fakeNet) set(p netcam.Path, err error) { n.mu.Lock(); n.path, n.err = p, err; n.mu.Unlock() }

type rig struct {
	t       *testing.T
	media   *fake.Media
	pgm     *fake.Pipeline
	bus     *Bus
	net     *fakeNet
	asound  string
	changes atomic.Int64
	logs    *syncBuf
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newRig(t *testing.T, slots int, cfg Config) *rig {
	t.Helper()
	r := &rig{t: t, media: fake.New(), net: &fakeNet{}, asound: t.TempDir(), logs: &syncBuf{}}
	elog.SetOutput(r.logs)
	t.Cleanup(func() { elog.SetOutput(nil) })
	p, _ := r.media.Parse("program", programDesc(slots))
	r.pgm = p.(*fake.Pipeline)
	cfg.Slots, cfg.ASound = slots, r.asound
	if cfg.I2SDevice == "" {
		cfg.I2SDevice = "hw:tegrasndt210ref,1"
	}
	b, err := New(Host{Media: r.media, Program: r.pgm, Net: r.net, Changed: func() { r.changes.Add(1) }}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.bus = b
	t.Cleanup(func() { b.Stop(); b.Wait(time.Now().Add(3 * time.Second)) })
	return r
}

// card makes /proc/asound/<name> (and changes cards).
func (r *rig) card(name string, on bool) {
	if on {
		_ = os.MkdirAll(filepath.Join(r.asound, name), 0o755)
	} else {
		_ = os.RemoveAll(filepath.Join(r.asound, name))
	}
	_ = os.WriteFile(filepath.Join(r.asound, "cards"), []byte(name+fmtBool(on)), 0o644)
}

func fmtBool(b bool) string {
	if b {
		return " on"
	}
	return " off"
}

// place puts an input in a slot without a goroutine (like the Python tests' SimpleNamespace).
func (r *rig) place(slot int, id, kind, name, state string) *Input {
	b := r.bus
	in := newInput(b, slot, id, kind, name, "")
	in.state = state
	close(in.done) // no goroutine
	b.mu.Lock()
	b.slots[slot] = in
	b.mu.Unlock()
	return in
}

func (r *rig) pad(slot int, prop string) string { return r.pgm.El("amix").Pad(PadName(slot), prop) }

func stopped(in *Input) bool { return waitDone(in.stopCh, 0) }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- ports of scripts/test_engine_audio.py ---

func TestFollowMixStartsWithWhatWasOnAir(t *testing.T) {
	r := newRig(t, 2, Config{})
	b := r.bus
	b.mode, b.main, b.mix = ModeSingle, "alsa:Edition", map[string]bool{"alsa:Edition": false, "i2s": true}
	b.setMode(ModeMix)
	if !b.mix["alsa:Edition"] || !b.mix["i2s"] { // what was sounding goes in even if taken out before
		t.Errorf("mix: %v", b.mix)
	}
}

func settingsOf(t *testing.T, b *Bus) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(b.Extra(nil).AudioSettings, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFollowSettingsRoundtripIgnoresJunk(t *testing.T) {
	r := newRig(t, 2, Config{})
	b := r.bus
	b.loadSettings(json.RawMessage(`{"follow": true, "delays": {"alsa:C4K": 80, "i2s": 999, "x;y": 10, "none": 5},
		"links": {"hdmi": "alsa:Edition", "cam": null, "bad": "hw:0"}}`))
	want := map[string]any{"follow": true, "delays": map[string]any{"alsa:C4K": 80.0},
		"links": map[string]any{"hdmi": "alsa:Edition", "cam": nil}, "mode": "single", "mix": map[string]any{}, "gains": map[string]any{}}
	if got := settingsOf(t, b); !reflect.DeepEqual(got, want) {
		t.Errorf("settings:\n got %v\nwant %v", got, want)
	}
	b.loadSettings(json.RawMessage(`{"mode": "mix", "mix": {"alsa:C4K": true, "x;y": true, "i2s": "si"}, "gains": {"i2s": -6, "alsa:C4K": 40}}`))
	if b.mode != ModeMix || !reflect.DeepEqual(b.mix, map[string]bool{"alsa:C4K": true}) || !reflect.DeepEqual(b.gains, map[string]float64{"i2s": -6}) {
		t.Errorf("mode %s mix %v gains %v", b.mode, b.mix, b.gains)
	}
	b.loadSettings(json.RawMessage(`{"follow": "yes", "delays": [], "links": "x"}`))
	if !b.follow {
		t.Error("junk changed follow")
	}
}

func TestNetDiscoverFailedProbeKeepsSources(t *testing.T) {
	r := newRig(t, 4, Config{})
	cam := r.place(3, "net:cam1", KindNetwork, "Móvil", StateLive)
	r.bus.Discover(probe.Catalogue{OK: false})
	if r.bus.slots[3] != cam || stopped(cam) {
		t.Fatal("an unreadable catalogue removed the camera")
	}
	r.bus.Discover(probe.Catalogue{OK: true}) // a real catalogue without the camera: now yes
	if r.bus.slots[3] != nil || !stopped(cam) {
		t.Fatal("the removed camera kept its slot")
	}
	if !strings.Contains(r.logs.String(), "event=audio_input_removed slot=4") {
		t.Errorf("log: %s", r.logs.String())
	}
}

func TestNetDiscoverRenombrar(t *testing.T) {
	r := newRig(t, 4, Config{})
	cam := r.place(3, "net:cam1", KindNetwork, "Móvil", StateLive)
	r.bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{{ID: "net:cam1", Kind: "network", Name: "Cámara 2"}}})
	if r.bus.slots[3] != cam || cam.Name() != "Cámara 2" {
		t.Errorf("slot %v name %q", r.bus.slots[3], cam.Name())
	}
}

// --- port of the audio part of scripts/test_engine_mixer.py (MixerTest.test_usb_on_air) ---

func TestMixerUsbOnAirAudio(t *testing.T) {
	r := newRig(t, 3, Config{})
	r.place(1, "alsa:Logi", KindUSB, "Logi", StateLive)
	r.place(2, "alsa:MS2109", KindUSB, "MS2109", StateLive)
	r.bus.main = "alsa:Logi" // targets {1: 1.0, 2: 0.0}: what sounds is "on air"; what is at 0, not
	air := func() usbhub.Air {
		a := usbhub.Air{Program: usbhub.Program{Main: "cap", Layout: "full"}, Now: time.Now()}
		for _, in := range r.bus.Inputs() {
			a.Audios = append(a.Audios, usbhub.AudioSlot{ID: in.ID, Kind: in.Kind, Name: in.Name, State: in.State, Volume: in.Volume})
		}
		return a
	}
	dev := func(card string) string { return map[string]string{"Logi": "2-1.4", "MS2109": "1-2.1"}[card] }
	if got := air().Devices("cap", "1-2.1", dev); !reflect.DeepEqual(got, map[string]usbhub.OnAir{"2-1.4": {ID: "alsa:Logi", Name: "Logi", Audio: true}}) ||
		got["2-1.4"].Text() != "el audio de Logi" {
		t.Errorf("cap: %v", got)
	}
	if got := air().Devices("logi", "2-1.4", dev); len(got) != 0 { // its own audio does not count
		t.Errorf("logi: %v", got)
	}
	r.bus.slots[1].state = StateError // not live: nothing to cut
	if got := air().Devices("cap", "", dev); len(got) != 0 {
		t.Errorf("not live: %v", got)
	}
}

// --- new: the bus with the fake GStreamer ---

func TestBusNeedsTheProgramSlots(t *testing.T) {
	m := fake.New()
	p, _ := m.Parse("program", programDesc(2))
	if _, err := New(Host{Media: m, Program: p}, Config{Slots: 3}); err == nil {
		t.Error("aa2 missing: no error")
	}
	q, _ := m.Parse("program", "appsrc name=aa0 ! queue name=aq0 ! fakesink")
	if _, err := New(Host{Media: m, Program: q}, Config{Slots: 1}); err == nil {
		t.Error("amix missing: no error")
	}
	r := newRig(t, 2, Config{})
	if len(r.media.Bridges) != 2 || r.media.Bridges[0].Dst != "aa0" || r.media.Bridges[1].Dst != "aa1" ||
		r.media.Bridges[0].Kind != gst.BridgeSource {
		t.Errorf("bridges: %+v", r.media.Bridges)
	}
}

func view(main string) View {
	return View{Main: main, Videos: []Video{{ID: "hdmi", Slot: 1, Kind: "hdmi-csi"},
		{ID: "/dev/v4l/by-id/usb-logi", Slot: 2, Kind: "usb", Card: "Logi"}, {ID: "test", Slot: 3, Kind: "test"}}}
}

func TestCommandsValidation(t *testing.T) {
	r := newRig(t, 3, Config{})
	r.place(0, "i2s", KindI2S, nameI2S, StateLive)
	r.place(1, "alsa:Logi", KindUSB, "Logi", StateLive)
	n := func(s string) json.Number { return json.Number(s) }
	for _, c := range []struct {
		cmd  string
		msg  map[string]any
		code string
	}{
		{"audio", map[string]any{"source": "alsa:Nope"}, CodeUnknownSource},
		{"audio", map[string]any{"source": ""}, CodeUnknownSource},
		{"audio", map[string]any{"source": n("1")}, CodeUnknownSource},
		{"audio_follow", map[string]any{"on": "yes"}, CodeBadRequest},
		{"audio_follow", map[string]any{}, CodeBadRequest},
		{"audio_mode", map[string]any{"mode": "both"}, CodeBadRequest},
		{"audio_mix", map[string]any{"source": "i2s"}, CodeBadRequest},
		{"audio_mix", map[string]any{"source": "i2s", "on": "x"}, CodeBadRequest},
		{"audio_mix", map[string]any{"source": "i2s", "gain": n("13")}, CodeBadRequest},
		{"audio_mix", map[string]any{"source": "i2s", "gain": true}, CodeBadRequest},
		{"audio_mix", map[string]any{"source": "nope", "gain": n("13")}, CodeBadRequest}, // bad_request first
		{"audio_mix", map[string]any{"source": "nope", "on": true}, CodeUnknownSource},
		{"audio_mix", map[string]any{"source": n("3"), "on": true}, CodeUnknownSource},
		{"audio_delay", map[string]any{"source": "i2s", "ms": n("501")}, CodeBadRequest},
		{"audio_delay", map[string]any{"source": "i2s", "ms": n("12.5")}, CodeBadRequest},
		{"audio_delay", map[string]any{"source": "i2s"}, CodeBadRequest},
		{"audio_delay", map[string]any{"source": "nope", "ms": n("-1")}, CodeBadRequest}, // bad_request first
		{"audio_delay", map[string]any{"source": "nope", "ms": n("10")}, CodeUnknownSource},
		{"audio_link", map[string]any{"video": "nope", "source": "i2s"}, CodeUnknownSource},
		{"audio_link", map[string]any{"video": "hdmi", "source": "alsa:Nope"}, CodeUnknownSource},
		{"audio_link", map[string]any{"video": "hdmi", "source": nil}, CodeUnknownSource}, // null is not "none" here
		{"audio_link", map[string]any{"video": "hdmi"}, CodeUnknownSource},
	} {
		res := r.bus.Command(c.cmd, c.msg, view("hdmi"))
		if res.OK || res.Code != c.code || res.Changed {
			t.Errorf("%s %v: %+v, want %s", c.cmd, c.msg, res, c.code)
		}
	}
	// Valid ones: the settings always save and publish (also unchanged); `audio` only when it changes
	for _, c := range []struct {
		cmd string
		msg map[string]any
	}{
		{"audio_follow", map[string]any{"on": false}},
		{"audio_mode", map[string]any{"mode": "single"}},
		{"audio_mix", map[string]any{"source": "i2s", "gain": n("-3.25")}},
		{"audio_delay", map[string]any{"source": "i2s", "ms": n("0")}},
		{"audio_link", map[string]any{"video": "test", "source": "none"}},
	} {
		if res := r.bus.Command(c.cmd, c.msg, view("hdmi")); !res.OK || !res.Changed || !res.LogCommand {
			t.Errorf("%s %v: %+v", c.cmd, c.msg, res)
		}
	}
	if r.bus.gains["i2s"] != -3.2 || r.bus.overrides["test"] != "" {
		t.Errorf("gains %v links %v", r.bus.gains, r.bus.overrides)
	}
	if res := r.bus.Command("audio", map[string]any{"source": "none"}, view("hdmi")); !res.OK || res.Changed {
		t.Errorf("audio none while none: %+v", res)
	}
	if res := r.bus.Command("audio", map[string]any{"source": "i2s"}, view("hdmi")); !res.OK || !res.Changed || res.LogCommand {
		t.Errorf("audio i2s: %+v", res)
	}
	if res := r.bus.Command("audio", map[string]any{}, view("hdmi")); !res.OK || !res.Changed || r.bus.Main() != "" {
		t.Errorf("audio without source = none: %+v %q", res, r.bus.Main())
	}
	if !IsCommand("audio_mix") || IsCommand("phones") {
		t.Error("IsCommand")
	}
}

func TestAudioFadesBetweenInputs(t *testing.T) {
	r := newRig(t, 3, Config{ConfigMain: "i2s"})
	r.place(0, "i2s", KindI2S, nameI2S, StateLive)
	r.place(1, "alsa:Logi", KindUSB, "Logi", StateLive)
	now := time.Unix(1000, 0)
	r.bus.now = func() time.Time { return now }
	r.bus.Start(false)
	if r.pad(0, "volume") != "1.000000" || r.pad(0, "mute") != "false" || r.pad(1, "mute") != "true" || r.pad(2, "mute") != "true" {
		t.Fatalf("start: %v %v", r.pgm.El("amix").Pads, r.bus.vols)
	}
	if !r.bus.Command("audio", map[string]any{"source": "alsa:Logi"}, View{}).Changed || !r.bus.Fading() {
		t.Fatal("no fade")
	}
	r.bus.Tick(now.Add(Fade / 4))
	if r.pad(0, "volume") != "0.750000" || r.pad(1, "volume") != "0.250000" || r.pad(1, "mute") != "false" {
		t.Errorf("quarter: %v", r.pgm.El("amix").Pads)
	}
	r.bus.Tick(now.Add(Fade))
	if r.bus.Fading() || r.pad(0, "mute") != "true" || r.pad(1, "volume") != "1.000000" {
		t.Errorf("end: %v", r.pgm.El("amix").Pads)
	}
	// The Pause with silence: everything to 0, then back
	if !r.bus.SetSilenced(true) || r.bus.SetSilenced(true) {
		t.Error("SetSilenced reports")
	}
	r.bus.Tick(now.Add(2 * Fade))
	if r.pad(1, "mute") != "true" || r.bus.Main() != "alsa:Logi" {
		t.Errorf("silenced: %v", r.pgm.El("amix").Pads)
	}
	r.bus.SetSilenced(false)
	r.bus.Tick(now.Add(3 * Fade))
	if r.pad(1, "volume") != "1.000000" {
		t.Errorf("back: %v", r.pgm.El("amix").Pads)
	}
	// Gain: +6 dB ≈ 1.995 linear; above 10 is capped
	r.bus.Command("audio_mix", map[string]any{"source": "alsa:Logi", "gain": json.Number("6")}, View{})
	r.bus.Tick(now.Add(4 * Fade))
	if r.pad(1, "volume") != "1.995262" {
		t.Errorf("gain: %v", r.pad(1, "volume"))
	}
	if !strings.Contains(r.logs.String(), "event=audio_main_changed input=alsa:Logi name=Logi") {
		t.Errorf("log: %s", r.logs.String())
	}
}

func TestFollowOnTakeAndLink(t *testing.T) {
	r := newRig(t, 3, Config{})
	r.place(0, "i2s", KindI2S, nameI2S, StateLive)
	r.place(1, "alsa:Logi", KindUSB, "Logi", StateLive)
	r.place(2, "alsa:Mic", KindUSB, "Mic", StateLive)
	b := r.bus
	if b.Follow(view("hdmi")) || b.Main() != "" {
		t.Fatal("followed while off")
	}
	b.Command("audio_follow", map[string]any{"on": true}, view("/dev/v4l/by-id/usb-logi"))
	if b.Main() != "alsa:Logi" { // turning it on follows at once
		t.Fatalf("main %q", b.Main())
	}
	if !b.Follow(view("hdmi")) || b.Main() != "i2s" {
		t.Fatalf("take hdmi: %q", b.Main())
	}
	if b.Follow(view("test")) || b.Main() != "i2s" { // no audio of its own: the one sounding stays
		t.Fatalf("take test: %q", b.Main())
	}
	// A link by hand, for the video on air: follows at once
	b.Command("audio_link", map[string]any{"video": "hdmi", "source": "alsa:Mic"}, view("hdmi"))
	if b.Main() != "alsa:Mic" {
		t.Fatalf("link: %q", b.Main())
	}
	// Mix mode: no follow
	b.Command("audio_mode", map[string]any{"mode": "mix"}, view("hdmi"))
	if b.Follow(view("/dev/v4l/by-id/usb-logi")) || b.Main() != "alsa:Mic" || !b.mix["alsa:Mic"] {
		t.Fatalf("mix: %q %v", b.Main(), b.mix)
	}
}

func TestDelayGoesToTheBridge(t *testing.T) {
	r := newRig(t, 3, Config{Sync: 150 * time.Millisecond, NetSync: 150 * time.Millisecond})
	r.place(0, "i2s", KindI2S, nameI2S, StateLive)
	r.place(1, "net:cam1", KindNetwork, "Móvil", StateLive)
	r.bus.Command("audio_delay", map[string]any{"source": "i2s", "ms": json.Number("80")}, View{})
	r.bus.Command("audio_delay", map[string]any{"source": "net:cam1", "ms": json.Number("20")}, View{})
	if p := r.media.Bridges[0].Params; p.Sync != 150*time.Millisecond || p.Extra != 80*time.Millisecond || p.JudgeLimit != 0 {
		t.Errorf("i2s: %+v", p)
	}
	if p := r.media.Bridges[1].Params; p.Extra != 170*time.Millisecond { // + the wifi cushion
		t.Errorf("network: %+v", p)
	}
	r.bus.Command("audio_delay", map[string]any{"source": "i2s", "ms": json.Number("0")}, View{})
	if _, ok := r.bus.delays["i2s"]; ok || r.media.Bridges[0].Params.Extra != 0 {
		t.Error("delay 0 not removed")
	}
	// Without sync nothing is stamped: no delay either (as in Python)
	r2 := newRig(t, 1, Config{})
	r2.place(0, "i2s", KindI2S, nameI2S, StateLive)
	r2.bus.Command("audio_delay", map[string]any{"source": "i2s", "ms": json.Number("80")}, View{})
	if p := r2.media.Bridges[0].Params; p.Extra != 0 || p.Sync != 0 {
		t.Errorf("no sync: %+v", p)
	}
}

func TestStateShape(t *testing.T) {
	r := newRig(t, 3, Config{ConfigMain: "i2s"})
	r.place(0, "i2s", KindI2S, nameI2S, StateLive).setPeak(-12.25, true)
	r.place(1, "alsa:Mic", KindUSB, "Mic", StateError).detail = detail.New("no_samples", "seconds", "6")
	r.place(2, "alsa:Logi", KindUSB, "Logi", StateStarting).setPeak(-3, true) // not live: no peakDb
	r.bus.Command("audio_delay", map[string]any{"source": "alsa:Logi", "ms": json.Number("40")}, View{})
	r.bus.Command("audio_mix", map[string]any{"source": "alsa:Logi", "gain": json.Number("-6"), "on": true}, View{})
	r.bus.Command("audio_link", map[string]any{"video": "test", "source": "alsa:Mic"}, view("hdmi"))
	b, err := json.Marshal(r.bus.State(view("hdmi")))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"main":"i2s","sources":[` +
		`{"id":"i2s","slot":1,"kind":"i2s","name":"HDMI de la placa","state":"live","detail":"","detailCode":"","delayMs":0,"inMix":true,"gainDb":0,"peakDb":-12.2,"videoSlot":1},` +
		`{"id":"alsa:Logi","slot":3,"kind":"usb","name":"Logi","state":"starting","detail":"","detailCode":"","delayMs":40,"inMix":true,"gainDb":-6,"videoSlot":2},` +
		`{"id":"alsa:Mic","slot":2,"kind":"usb","name":"Mic","state":"error","detail":"sin muestras 6 s","detailCode":"no_samples","detailArgs":{"seconds":"6"},"delayMs":0,"inMix":false,"gainDb":0}],` +
		`"follow":false,"links":{"/dev/v4l/by-id/usb-logi":"alsa:Logi","hdmi":"i2s","test":"alsa:Mic"},"mode":"single"}`
	if string(b) != want {
		t.Errorf("state:\n got %s\nwant %s", b, want)
	}
	// No inputs, no videos, no audio: empty list and object, main null
	r2 := newRig(t, 1, Config{ConfigMain: "none"})
	b, _ = json.Marshal(r2.bus.State(View{}))
	if string(b) != `{"main":null,"sources":[],"follow":false,"links":{},"mode":"single"}` {
		t.Errorf("empty: %s", b)
	}
	if pk := r.bus.InputPeaks(); !reflect.DeepEqual(pk, map[string]float64{"i2s": -12.25}) {
		t.Errorf("peaks: %v", pk)
	}
}

func TestLoadMainGate(t *testing.T) {
	raw := func(s string) json.RawMessage {
		if s == "" {
			return nil
		}
		return json.RawMessage(s)
	}
	for _, c := range []struct {
		config, audio, configAudio, want string
	}{
		{"i2s", "", "", "i2s"},                       // no file: the configuration's
		{"none", "", "", ""},                         // nor with none
		{"i2s", `"alsa:Logi"`, `"i2s"`, "alsa:Logi"}, // saved under the same configuration
		{"i2s", `null`, `"i2s"`, ""},                 // saved: no audio
		{"alsa:Logi", `"i2s"`, `"i2s"`, "alsa:Logi"}, // the configuration changed: it rules
		{"none", `"i2s"`, `null`, "i2s"},             // configAudio null = none
		{"", `"i2s"`, ``, "i2s"},                     // absent configAudio = none too
		{"i2s", `"` + strings.Repeat("x", 41) + `"`, `"i2s"`, "i2s"},
		{"i2s", `"whatever"`, `"i2s"`, "whatever"}, // no id check, as in Python
		{"i2s", `5`, `"i2s"`, "i2s"},
		{"none", `"i2s"`, `""`, ""}, // "" is not None
	} {
		r := newRig(t, 1, Config{ConfigMain: c.config})
		r.bus.Load(scene.Extra{Audio: raw(c.audio), ConfigAudio: raw(c.configAudio)})
		if got := r.bus.Main(); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

func TestExtraRoundTrip(t *testing.T) {
	r := newRig(t, 2, Config{ConfigMain: "i2s"})
	r.place(0, "i2s", KindI2S, nameI2S, StateLive)
	r.place(1, "alsa:Logi", KindUSB, "Logi", StateLive)
	b := r.bus
	b.Command("audio_follow", map[string]any{"on": true}, View{})
	b.Command("audio_mode", map[string]any{"mode": "mix"}, View{})
	b.Command("audio_mix", map[string]any{"source": "alsa:Logi", "on": true, "gain": json.Number("-6")}, View{})
	b.Command("audio_delay", map[string]any{"source": "alsa:Logi", "ms": json.Number("120")}, View{})
	b.Command("audio_link", map[string]any{"video": "hdmi", "source": "none"}, view("test"))
	ex := b.Extra(json.RawMessage(`{"on": true, "gainDb": -12}`))
	data, err := scene.Marshal("hdmi", ptr(scene.New("hdmi")), false, ex)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"audio": "i2s", "configAudio": "i2s", "audioSettings": {"follow": true, "delays": {"alsa:Logi": 120}, `+
		`"links": {"hdmi": null}, "mode": "mix", "mix": {"alsa:Logi": true, "i2s": true}, "gains": {"alsa:Logi": -6}, "phones": {"on": true, "gainDb": -12}}`) {
		t.Errorf("file: %s", data)
	}
	path := filepath.Join(t.TempDir(), scene.FileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := newRig(t, 2, Config{ConfigMain: "i2s"})
	phones := r2.bus.Load(scene.Load(path, "hdmi").Extra)
	if string(phones) != `{"gainDb":-12,"on":true}` {
		t.Errorf("phones: %s", phones)
	}
	if !reflect.DeepEqual(settingsOf(t, r2.bus), settingsOf(t, b)) || r2.bus.Main() != "i2s" {
		t.Errorf("round trip: %v vs %v", settingsOf(t, r2.bus), settingsOf(t, b))
	}
}

func ptr[T any](v T) *T { return &v }

func TestProgramPeaks(t *testing.T) {
	r := newRig(t, 1, Config{})
	if r.bus.OnProgramMessage(&gst.Message{Type: gst.MessageElement, Src: "level0", Text: "level", Debug: level}) ||
		r.bus.OnProgramMessage(&gst.Message{Type: gst.MessageError, Src: "pgmlevel"}) || r.bus.ProgramPeaks() != nil {
		t.Fatal("took a message that is not pgmlevel's")
	}
	if !r.bus.OnProgramMessage(&gst.Message{Type: gst.MessageElement, Src: "pgmlevel", Text: "level", Debug: level}) ||
		!reflect.DeepEqual(r.bus.ProgramPeaks(), []float64{-12.25, -9.5}) {
		t.Errorf("peaks: %v", r.bus.ProgramPeaks())
	}
}

func TestDiscoverSlots(t *testing.T) {
	r := newRig(t, 2, Config{})
	b := r.bus
	r.card("Logi", true)
	r.card("Mic", true)
	// The USB first in the catalogue, the I2S still goes to the first slot; no room for the third
	b.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{{ID: "alsa:Logi", Name: "Logi C920 <x>"},
		{ID: "i2s"}, {ID: "alsa:Mic"}, {ID: "alsa:x;rm"}, {ID: "net:cam1", Kind: "usb"}}})
	if b.slots[0] == nil || b.slots[0].id != "i2s" || b.slots[0].Name() != nameI2S || b.slots[0].device != "hw:tegrasndt210ref,1" ||
		b.slots[1] == nil || b.slots[1].id != "alsa:Logi" || b.slots[1].Name() != "Logi C920 x" || b.slots[1].device != "hw:Logi,0" {
		t.Fatalf("slots: %+v %+v", b.slots[0], b.slots[1])
	}
	if !strings.Contains(r.logs.String(), "event=audio_no_free_slot input=Audio") {
		t.Errorf("log: %s", r.logs.String())
	}
	// Logi gone for more than 10 s: its slot goes to the next card
	b.mu.Lock()
	logi := b.slots[1]
	b.mu.Unlock()
	r.card("Logi", false)
	eventually(t, "Logi gone", func() bool { s, _ := logi.snapshot(); return s == StateGone })
	logi.mu.Lock()
	logi.since = time.Now().Add(-time.Minute)
	logi.mu.Unlock()
	b.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{{ID: "i2s"}, {ID: "alsa:Mic", Name: "Mic"}}})
	if b.slots[1] == nil || b.slots[1].id != "alsa:Mic" || !stopped(logi) {
		t.Fatalf("reuse: %+v", b.slots[1])
	}
	b.Stop()
	if b.Wait(time.Now().Add(3*time.Second)) != 0 {
		t.Error("inputs still running")
	}
}

func TestMuxI2S(t *testing.T) {
	dir := t.TempDir()
	var got []string
	rc := 0
	run := func(_ time.Duration, name string, args ...string) sysx.Result {
		got = append([]string{name}, args...)
		return sysx.Result{RC: rc, Err: "amixer: Cannot find the given element"}
	}
	if MuxI2S(run, dir, "tegrasndt210ref", 2) || got != nil {
		t.Fatal("ran without the card")
	}
	_ = os.Mkdir(filepath.Join(dir, "tegrasndt210ref"), 0o755)
	if !MuxI2S(run, dir, "tegrasndt210ref", 2) ||
		!reflect.DeepEqual(got, []string{"amixer", "-q", "-c", "tegrasndt210ref", "cset", "name=ADMAIF2 Mux", "I2S4"}) {
		t.Fatalf("args: %q", got)
	}
	rc = 1
	logs := &syncBuf{}
	elog.SetOutput(logs)
	defer elog.SetOutput(nil)
	if MuxI2S(run, dir, "tegrasndt210ref", 2) || !strings.Contains(logs.String(), "[audio] event=i2s_mux_failed card=tegrasndt210ref admaif=2 rc=1") {
		t.Errorf("failure: %s", logs.String())
	}
}

func TestTimingsKV(t *testing.T) {
	r := newRig(t, 2, Config{})
	r.place(0, "i2s", KindI2S, nameI2S, StateLive)
	r.place(1, "alsa:Mic", KindUSB, "Mic", StateError)
	r.media.Bridges[0].SetStats(gst.BridgeStats{N: 50, AgeMin: 20 * time.Millisecond, AgeAvg: 30 * time.Millisecond,
		AgeMax: 60 * time.Millisecond, MaxGap: 45 * time.Millisecond})
	r.media.Bridges[1].SetStats(gst.BridgeStats{N: 50})
	if kv := r.bus.TimingsKV(); !reflect.DeepEqual(kv, []any{"audio1", "age=20/30/60ms gap=45ms"}) {
		t.Errorf("kv: %v", kv)
	}
}

var errDown = errors.New("down")
