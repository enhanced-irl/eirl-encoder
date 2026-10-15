package phones

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst/fake"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// The ports of scripts/test_engine_phones.py: one test per Python test, named Test<Class><Method>.

// What the board had on 13-oct (HEADPHONES.md §1)
const (
	cards = " 0 [tegrahda       ]: tegra-hda - tegra-hda\n" +
		"                      tegra-hda at 0x70038000 irq 83\n" +
		" 1 [tegrasndt210ref]: tegra-snd-t210r - tegra-snd-t210ref-mobile-rt565x\n" +
		"                      tegra-snd-t210ref-mobile-rt565x\n" +
		" 2 [MS2109         ]: USB-Audio - MS2109\n" +
		"                      MacroSilicon MS2109 at usb-70090000.xusb-2.4, high speed\n"
	pcm = "00-03: HDMI 0 : HDMI 0 : playback 1\n" +
		"01-00: ADMAIF1 CIF ADMAIF1-0 :  : playback 1 : capture 1\n" +
		"02-00: USB Audio : USB Audio : capture 1\n"
	eldOK = "monitor_present\t\t1\neld_valid\t\t1\nmonitor_name\t\tLEN L1950wD\nconnection_type\t\tHDMI\n" +
		"speakers\t\t[0x1] FL/FR\nsad_count\t\t1\nsad0_coding_type\t[0x1] LPCM\nsad0_channels\t\t2\n" +
		"sad0_rates\t\t[0xe0] 32000 44100 48000\nsad0_bits\t\t[0xe0000] 16 20 24\n"
	eldStale = "monitor_present\t\t1\neld_valid\t\t0\n"
	eldNoPCM = "monitor_present\t\t1\neld_valid\t\t1\nmonitor_name\t\tDVI\nsad_count\t\t0\n"
)

const ms = time.Millisecond

func quiet(t *testing.T) *syncBuilder {
	t.Helper()
	var b syncBuilder
	elog.SetOutput(&b)
	t.Cleanup(func() { elog.SetOutput(nil) })
	return &b
}

type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// --- PureTest ---

func TestPureGain(t *testing.T) {
	for in, want := range map[any]int{-12: -12, -11.6: -12, -40: -40, 0: 0, json.Number("-6"): -6, json.Number("-11.5"): -12, -10.5: -10} {
		if g, ok := GainDB(in); !ok || g != want {
			t.Errorf("GainDB(%v) = %d, %v; want %d", in, g, ok, want)
		}
	}
	for _, bad := range []any{1, -41, true, nil, "x", math.NaN(), []any{0}, json.Number("1e400"), json.Number("3")} {
		if g, ok := GainDB(bad); ok {
			t.Errorf("GainDB(%v) = %d, want none", bad, g)
		}
	}
}

func TestPureVolume(t *testing.T) {
	if VolumeOf(-40) != 0 { // -40 is silence, not -40 dB
		t.Error("-40")
	}
	if VolumeOf(0) != 1 {
		t.Error("0")
	}
	if v := VolumeOf(-12); math.Abs(v-0.2512) > 0.0005 {
		t.Errorf("-12: %v", v)
	}
	if VolumeOf(-38) <= 0 {
		t.Error("-38")
	}
}

func TestPureCommand(t *testing.T) {
	type res struct {
		on   any
		gain any
		code string
	}
	get := func(msg map[string]any) res {
		c, code := ParseCommand(msg)
		r := res{code: code}
		if c.On != nil {
			r.on = *c.On
		}
		if c.Gain != nil {
			r.gain = *c.Gain
		}
		return r
	}
	for _, tc := range []struct {
		msg  map[string]any
		want res
	}{
		{map[string]any{"on": true}, res{on: true}},
		{map[string]any{"gain": json.Number("-6")}, res{gain: -6}},
		{map[string]any{"on": false, "gain": json.Number("-40")}, res{on: false, gain: -40}},
		{map[string]any{}, res{code: "bad_request"}},
		{map[string]any{"on": json.Number("1")}, res{code: "bad_request"}},
		{map[string]any{"on": "yes"}, res{code: "bad_request"}},
		{map[string]any{"gain": json.Number("3")}, res{code: "bad_request"}},
		{map[string]any{"gain": "-6"}, res{code: "bad_request"}},
		{map[string]any{"on": true, "gain": json.Number("5")}, res{code: "bad_request"}},
	} {
		if got := get(tc.msg); got != tc.want {
			t.Errorf("%v: %+v, want %+v", tc.msg, got, tc.want)
		}
	}
}

func TestPureFindHDMI(t *testing.T) {
	check := func(pcm, cards string, want Device, wantOK bool) {
		t.Helper()
		if d, ok := FindHDMI(pcm, cards); ok != wantOK || d != want {
			t.Errorf("FindHDMI = %+v, %v; want %+v, %v", d, ok, want, wantOK)
		}
	}
	check(pcm, cards, Device{Name: "hw:tegrahda,3", Card: 0}, true)
	// If the HDMI card is not number 0 (another boot order), it still opens by name
	check(strings.Replace(pcm, "00-03", "04-03", 1), strings.Replace(cards, " 0 [tegrahda", " 4 [tegrahda", 1), Device{Name: "hw:tegrahda,3", Card: 4}, true)
	check(strings.Replace(pcm, "00-03", "07-03", 1), cards, Device{Name: "hw:7,3", Card: 7}, true) // no name: number
	check("02-00: USB Audio : USB Audio : capture 1\n", cards, Device{}, false)
	check("00-03: HDMI 0 : HDMI 0 : capture 1\n", cards, Device{}, false) // no playback
	check("", "", Device{}, false)
}

func TestPureELD(t *testing.T) {
	if p, v, l := ParseELD(eldOK); !p || !v || !l {
		t.Error("ok", p, v, l)
	}
	if p, v, l := ParseELD(eldStale); !p || v || l {
		t.Error("stale", p, v, l)
	}
	for _, tc := range []struct {
		texts []string
		want  ELD
	}{
		{[]string{eldOK}, ELDOK},
		{[]string{eldStale}, ELDNoScreen},
		{[]string{eldNoPCM}, ELDNoAudio},
		{[]string{eldStale, eldOK}, ELDOK}, // several pins: one is enough
		{nil, ELDUnknown},
	} {
		if got := ELDVerdict(tc.texts); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.texts, got, tc.want)
		}
	}
}

func TestPureStatus(t *testing.T) {
	for _, tc := range []struct {
		want, opened bool
		err          string
		eld          ELD
		state, det   string
	}{
		{false, true, "x", ELDOK, StateOff, ""},
		{true, false, "Device busy", ELDUnknown, StateError, "Device busy"},
		{true, false, "", ELDUnknown, StateWaiting, WaitScreen},
		{true, true, "", ELDNoScreen, StateWaiting, WaitScreen},
		{true, true, "", ELDNoAudio, StateWaiting, NoAudio},
		{true, true, "", ELDOK, StateOn, ""},
		{true, true, "", ELDUnknown, StateOn, ""}, // no ELD to read: unknown, it plays
	} {
		if s, d := StatusOf(tc.want, tc.opened, tc.err, tc.eld); s != tc.state || d != tc.det {
			t.Errorf("%+v: %q %q", tc, s, d)
		}
	}
}

func TestPureTargetDelay(t *testing.T) {
	if P90(nil) != 0 || P90([]time.Duration{5}) != 5 {
		t.Error("P90 small")
	}
	var v []time.Duration
	for i := 100; i >= 1; i-- {
		v = append(v, time.Duration(i))
	}
	if P90(v) != 91 { // the lone hiccup (100) does not count
		t.Errorf("P90 = %d", P90(v))
	}
	// In the real engine (13-oct): the screen at 193 ms and the audio arriving at 165 → it waits for the
	// audio and ALSA's buffer (40) with its cushion (30)
	if d := TargetDelay(193*ms, 165*ms); d != 235*ms {
		t.Error(d)
	}
	if d := TargetDelay(193*ms, 40*ms); d != 193*ms { // earlier: in step with the screen
		t.Error(d)
	}
	if d := TargetDelay(193*ms, 0); d != 193*ms { // no measure: the screen's
		t.Error(d)
	}
}

func TestPureDelayMoved(t *testing.T) {
	if DelayMoved(160*ms, 164*ms, true) { // noise: it stays
		t.Error("noise")
	}
	if !DelayMoved(160*ms, 180*ms, true) || !DelayMoved(160*ms, 150*ms, true) { // the screen was rebuilt
		t.Error("moved")
	}
	if DelayMoved(160*ms, 0, false) { // unknown now: leave it
		t.Error("unknown")
	}
}

func TestPureRetry(t *testing.T) {
	var got []time.Duration
	for _, n := range []int{0, 1, 2, 3, 4, 5, 9} {
		got = append(got, RetryWait(n)/time.Second)
	}
	if want := []time.Duration{2, 2, 4, 8, 15, 30, 30}; !equal(got, want) {
		t.Error(got)
	}
	if s := ShortError("a\n  b\tc"); s != "a b c" {
		t.Error(s)
	}
	if s := ShortError(strings.Repeat("x", 500)); len(s) != 120 {
		t.Error(len(s))
	}
	if s := ShortError(""); s != "error" {
		t.Error(s)
	}
}

func equal(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- PhonesTest ---

// fakeTap records what the listening does with it.
type fakeTap struct {
	mu        sync.Mutex
	connected []string // "" = Connect(nil)
	lates     []time.Duration
	freed     bool
}

func (f *fakeTap) Connect(el gst.Element) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := ""
	if el != nil {
		name = el.Name()
	}
	f.connected = append(f.connected, name)
}

func (f *fakeTap) TakeLate() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.lates) == 0 {
		return 0
	}
	l := f.lates[0]
	f.lates = f.lates[1:]
	return l
}

func (f *fakeTap) Free() { f.mu.Lock(); f.freed = true; f.mu.Unlock() }

func (f *fakeTap) conns() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.connected...)
}

type rig struct {
	media   *fake.Media
	pgm     *fake.Pipeline
	tap     *fakeTap
	mu      sync.Mutex
	changes int
	scr     ScreenView
	bt      string // the app's Bluetooth file (BLUETOOTH.md): absent = the screen
}

func (r *rig) count() int { r.mu.Lock(); defer r.mu.Unlock(); return r.changes }

func pipelineWith(m *fake.Media, names ...string) *fake.Pipeline {
	desc := ""
	for _, n := range names {
		desc += " name=" + n
	}
	p, _ := m.Parse("program", desc)
	return p.(*fake.Pipeline)
}

func makePhones(t *testing.T, saved string, names ...string) (*Phones, *rig, error) {
	t.Helper()
	if names == nil {
		names = []string{"phtap", "phvalve"}
	}
	m := fake.New()
	r := &rig{media: m, pgm: pipelineWith(m, names...), tap: &fakeTap{}, bt: filepath.Join(t.TempDir(), "bluetooth.json")}
	var raw json.RawMessage
	if saved != "" {
		raw = json.RawMessage(saved)
	}
	p, err := New(Options{
		Media: m, Program: r.pgm, Saved: raw, CompositorLatency: 60 * ms,
		Changed: func() { r.mu.Lock(); r.changes++; r.mu.Unlock() },
		Screen: func() ScreenView {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.scr
		},
		Run:       func(time.Duration, string, ...string) sysx.Result { return sysx.Result{} },
		NewTap:    func(gst.Media, gst.Pipeline, gst.Element) Tap { return r.tap },
		Bluetooth: r.bt,
	})
	if p != nil {
		p.asound = t.TempDir()
	}
	return p, r, err
}

func TestPhonesNeedsBranch(t *testing.T) {
	if _, _, err := makePhones(t, "", "phtap"); err != ErrNoBranch {
		t.Error(err)
	}
}

func TestPhonesDefaultsAndSaved(t *testing.T) {
	p, _, _ := makePhones(t, "")
	if s := p.Settings(); s != (Settings{On: true, GainDb: -12}) { // D1: on at -12 dB
		t.Error(s)
	}
	if i := p.Info(); !reflect.DeepEqual(i, mixer.Phones{On: true, GainDb: -12, State: "off", Target: "screen"}) {
		t.Error(i)
	}
	if b, _ := json.Marshal(p.Info()); string(b) != `{"on":true,"gainDb":-12,"state":"off","target":"screen"}` {
		t.Error(string(b))
	}
	p, _, _ = makePhones(t, `{"on": false, "gainDb": -20}`)
	if s := p.Settings(); s != (Settings{On: false, GainDb: -20}) {
		t.Error(s)
	}
	p, _, _ = makePhones(t, `{"on": "no", "gainDb": 6}`) // junk: the defaults
	if s := p.Settings(); s != (Settings{On: true, GainDb: -12}) {
		t.Error(s)
	}
	p, _, _ = makePhones(t, `["x"]`)
	if s := p.Settings(); s != (Settings{On: true, GainDb: -12}) {
		t.Error(s)
	}
}

func pb(b bool) *bool { return &b }
func pi(i int) *int   { return &i }

func TestPhonesSetWakesAndReports(t *testing.T) {
	quiet(t)
	p, r, _ := makePhones(t, "")
	if p.Set(Change{On: pb(true), Gain: pi(-12)}) { // nothing new
		t.Error("no change")
	}
	if len(p.wake) != 0 {
		t.Error("woken")
	}
	if !p.Set(Change{Gain: pi(-6)}) || len(p.wake) != 1 {
		t.Error("gain")
	}
	if !p.Set(Change{On: pb(false)}) {
		t.Error("off")
	}
	if s := p.Settings(); s != (Settings{On: false, GainDb: -6}) {
		t.Error(s)
	}
	p.setStatus(StateWaiting, WaitScreen)
	p.setStatus(StateWaiting, WaitScreen) // the same: no new publish
	if r.count() != 1 {
		t.Error(r.count())
	}
	if p.Info().Detail != WaitScreen {
		t.Error(p.Info())
	}
}

type screen struct {
	state string
	lat   time.Duration
	ok    bool
}

func (s *screen) State() string                  { return s.state }
func (s *screen) Latency() (time.Duration, bool) { return s.lat, s.ok }

func TestPhonesScreenReady(t *testing.T) {
	p, r, _ := makePhones(t, "")
	if !ScreenReady(p.screen()) { // no screen configured: it opens anyway
		t.Error("none")
	}
	r.scr = &screen{state: "waiting"}
	if ScreenReady(p.screen()) {
		t.Error("waiting")
	}
	r.scr = &screen{state: "on"}
	if !ScreenReady(p.screen()) {
		t.Error("on")
	}
}

func waitDone(t *testing.T, p *Phones, d time.Duration) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(d):
		t.Fatal("the goroutine does not end")
	}
}

func TestPhonesOffAndStop(t *testing.T) {
	// Off: the goroutine opens nothing and stops at once when asked
	p, _, _ := makePhones(t, `{"on": false}`)
	p.find = func() (Device, bool) { t.Error("off does not look for the HDMI"); return Device{}, false }
	p.Start()
	p.Stop()
	waitDone(t, p, 2*time.Second)
	if p.Info().State != StateOff {
		t.Error(p.Info())
	}
}

func TestPhonesNoDeviceThenStop(t *testing.T) {
	p, _, _ := makePhones(t, "")
	seen := make(chan struct{})
	var once sync.Once
	p.find = func() (Device, bool) { once.Do(func() { close(seen) }); return Device{}, false }
	p.Start()
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("find not called")
	}
	for i := 0; i < 100 && p.Info().State != StateError; i++ {
		time.Sleep(10 * ms)
	}
	if i := p.Info(); i.State != StateError || i.Detail != NoDevice {
		t.Error(i)
	}
	p.Stop()
	waitDone(t, p, 2*time.Second)
}

func TestPhonesRetriesAfterError(t *testing.T) {
	logs := quiet(t)
	p, _, _ := makePhones(t, "")
	p.find = func() (Device, bool) { return Device{Name: "hw:tegrahda,3", Card: 0}, true }
	var mu sync.Mutex
	var calls []string
	p.session = func(d Device) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, d.Name)
		if len(calls) >= 2 {
			p.Stop()
		}
		return "Device or resource busy", true
	}
	p.retry = []time.Duration{50 * ms, 50 * ms}
	p.Start()
	waitDone(t, p, 2*time.Second)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != "hw:tegrahda,3" || calls[1] != "hw:tegrahda,3" {
		t.Error(calls)
	}
	if !strings.Contains(logs.String(), `[headphones] event=phones_retry err="Device or resource busy" wait_s=0`) {
		t.Error(logs.String())
	}
}

// --- EngineTest: the `phones` command in the engine and the screen's delay ---

func TestEngineCommand(t *testing.T) {
	quiet(t)
	p, _, _ := makePhones(t, `{"on": true, "gainDb": -12}`)
	saved, published := 0, 0
	// What Engine.command does with it: log, save the scene and publish only when something changed
	command := func(p *Phones, msg map[string]any) (bool, string) {
		ok, code, changed := Command(p, msg)
		if changed {
			saved++
			published++
		}
		return ok, code
	}
	if ok, code := command(p, map[string]any{"cmd": "phones", "gain": json.Number("-20")}); !ok || code != "" {
		t.Error(ok, code)
	}
	if p.Settings().GainDb != -20 || saved != 1 || published != 1 {
		t.Error(p.Settings(), saved, published)
	}
	if ok, code := command(p, map[string]any{"cmd": "phones", "gain": json.Number("-20")}); !ok || code != "" || saved != 1 || published != 1 {
		t.Error("same gain", saved, published) // the same: neither saves nor publishes
	}
	if ok, code := command(p, map[string]any{"cmd": "phones", "on": false}); !ok || code != "" || p.Settings().On {
		t.Error("off")
	}
	if ok, code := command(p, map[string]any{"cmd": "phones", "gain": json.Number("2")}); ok || code != "bad_request" {
		t.Error(ok, code)
	}
	if ok, code := command(p, map[string]any{"cmd": "phones"}); ok || code != "bad_request" {
		t.Error(ok, code)
	}
	if ok, code := command(nil, map[string]any{"cmd": "phones", "on": true}); ok || code != "unsupported" {
		t.Error(ok, code)
	}
}

func TestEngineScreenDelay(t *testing.T) {
	lat := 60 * ms
	check := func(s ScreenView, known bool, want time.Duration, wantOK bool) {
		t.Helper()
		if d, ok := ScreenDelay(lat, s, known); d != want || ok != wantOK {
			t.Errorf("ScreenDelay(%v, %v) = %v, %v; want %v, %v", s, known, d, ok, want, wantOK)
		}
	}
	check(nil, false, 60*ms+MVMargin+60*ms, true) // no screen: the compositors'
	check(nil, true, 60*ms+MVMargin+60*ms, true)  // no screen configured: fine
	s := &screen{state: "on", lat: 75 * ms, ok: true}
	check(s, false, 60*ms+MVMargin+75*ms, true)
	check(s, true, 60*ms+MVMargin+75*ms, true)
	s.state = "off" // being rebuilt: its latency is unknown
	check(s, false, 60*ms+MVMargin+60*ms, true)
	check(s, true, 0, false)
}

// --- beyond the Python tests: a whole listening with the fake GStreamer ---

// latMedia gives its pipelines SetLatency (gst.Pipeline does not have it yet).
type latMedia struct {
	*fake.Media
	mu   sync.Mutex
	lats []time.Duration
	last *fake.Pipeline
}

type latPipe struct {
	*fake.Pipeline
	m *latMedia
}

func (p latPipe) SetLatency(d time.Duration) {
	p.m.mu.Lock()
	p.m.lats = append(p.m.lats, d)
	p.m.mu.Unlock()
}

func (m *latMedia) Parse(label, desc string) (gst.Pipeline, error) {
	p, err := m.Media.Parse(label, desc)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.last = p.(*fake.Pipeline)
	m.mu.Unlock()
	return latPipe{p.(*fake.Pipeline), m}, nil
}

func (m *latMedia) latencies() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]time.Duration(nil), m.lats...)
}

func TestListenSession(t *testing.T) {
	logs := quiet(t)
	p, r, _ := makePhones(t, "")
	lm := &latMedia{Media: r.media}
	p.media = lm
	r.pgm.FixBase(12345)
	p.measure = 10 * ms
	r.tap.lates = []time.Duration{0, 165 * ms, 200 * ms}
	if err := os.WriteFile(filepath.Join(p.asound, "pcm"), []byte(pcm), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(p.asound, "cards"), []byte(cards), 0o644)
	_ = os.MkdirAll(filepath.Join(p.asound, "card0"), 0o755)
	_ = os.WriteFile(filepath.Join(p.asound, "card0", "eld#3.0"), []byte(eldOK), 0o644)
	var amixer []string
	var amu sync.Mutex
	p.run = func(_ time.Duration, name string, args ...string) sysx.Result {
		amu.Lock()
		amixer = append(amixer, name+" "+strings.Join(args, " "))
		amu.Unlock()
		return sysx.Result{}
	}
	p.digital = p.digitalOn
	p.Start()
	for i := 0; i < 200 && p.Info().State != StateOn; i++ {
		time.Sleep(10 * ms)
	}
	if p.Info().State != StateOn {
		t.Fatal(p.Info())
	}
	lm.mu.Lock()
	lp := lm.last
	lm.mu.Unlock()
	if lp.El("phsink").Prop("device") != "hw:tegrahda,3" || lp.Base != 12345 || lp.State != gst.StatePlaying {
		t.Error("pipeline", lp.El("phsink").Props, lp.Base, lp.State)
	}
	if !strings.Contains(lp.Desc, "buffer-time=40000 latency-time=10000") || !strings.Contains(lp.Desc, `caps="`+ACaps+`"`) {
		t.Error(lp.Desc)
	}
	// screen 60+40+60 = 160; late 165 → 165+40+30 = 235
	if l := lm.latencies(); len(l) != 1 || l[0] != 235*ms {
		t.Error(l)
	}
	if r.pgm.El("phvalve").Prop("drop") != "false" {
		t.Error("valve closed while playing")
	}
	time.Sleep(Ramp + 50*ms)
	if v := lp.El("phvol").Prop("volume"); !strings.HasPrefix(v, "0.251") {
		t.Error("volume", v)
	}
	p.Set(Change{Gain: pi(-40)})
	time.Sleep(Ramp + 200*ms)
	if v := lp.El("phvol").Prop("volume"); v != "0" {
		t.Error("silence", v)
	}
	// After ELDEvery the late (200) is taken: 200+70 = 270
	time.Sleep(ELDEvery)
	if l := lm.latencies(); len(l) != 2 || l[1] != 270*ms {
		t.Error(l)
	}
	t0 := time.Now()
	p.Stop()
	waitDone(t, p, CloseWait+time.Second)
	if d := time.Since(t0); d > time.Second {
		t.Error("stop took", d)
	}
	if r.pgm.El("phvalve").Prop("drop") != "true" || !lp.Closed || p.Info().State != StateOff {
		t.Error("not closed", r.pgm.El("phvalve").Props, lp.Closed, p.Info())
	}
	if c := r.tap.conns(); len(c) != 2 || c[0] != "pha" || c[1] != "" {
		t.Error(c)
	}
	amu.Lock()
	if len(amixer) != 1 || amixer[0] != "amixer -q -c 0 cset name=IEC958 Playback Switch on" {
		t.Error(amixer)
	}
	amu.Unlock()
	out := logs.String()
	for _, want := range []string{
		`[headphones] event=phones_playing device=hw:tegrahda,3 gain_db=-12 delay_ms=235 screen_ms=160 late_ms=165 eld=ok`,
		`[headphones] event=phones_delay_changed delay_ms=270 was_ms=235 screen_ms=160 late_ms=200`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	p.Free()
	if !r.tap.freed {
		t.Error("tap not freed")
	}
}

func TestListenBusError(t *testing.T) {
	logs := quiet(t)
	p, r, _ := makePhones(t, "")
	p.measure = 0
	p.digital = func(int) {}
	go func() {
		for i := 0; i < 500 && p.Info().State != StateOn; i++ {
			time.Sleep(5 * ms)
		}
		r.media.Find("headphones").Post(&gst.Message{Type: gst.MessageError, Src: "phsink", Text: "Could not write", Debug: "a\nb last"})
	}()
	e, failed := p.listen(Device{Name: "hw:tegrahda,3", Card: 0})
	if !failed || e != "Could not write" {
		t.Error(e, failed)
	}
	if !r.media.Find("headphones").Closed || r.pgm.El("phvalve").Prop("drop") != "true" {
		t.Error("not closed")
	}
	if !strings.Contains(logs.String(), `event=phones_error element=phsink error="Could not write" debug="b last"`) {
		t.Error(logs.String())
	}
}

func TestListenCloseTimeout(t *testing.T) {
	logs := quiet(t)
	p, r, _ := makePhones(t, "")
	block := make(chan struct{})
	defer close(block)
	r.media.BlockClose["headphones"] = block
	pipe, _ := r.media.Parse("headphones", "")
	t0 := time.Now()
	p.close(pipe)
	if d := time.Since(t0); d < CloseWait || d > CloseWait+time.Second {
		t.Error(d)
	}
	if !strings.Contains(logs.String(), "[headphones] event=phones_close_timeout wait_s=3") {
		t.Error(logs.String())
	}
}

func TestBridgeTapReconnects(t *testing.T) {
	m := fake.New()
	pgm := pipelineWith(m, "phtap", "phvalve")
	lis, _ := m.Parse("headphones", "name=pha")
	tp := NewBridgeTap(m, pgm, pgm.ByName("phtap"))
	if len(m.Bridges) != 1 || m.Bridges[0].Kind != gst.BridgePass || m.Bridges[0].Dst != "" || m.Bridges[0].Attached != "phtap" {
		t.Fatal("idle bridge", m.Bridges)
	}
	tp.Connect(lis.ByName("pha"))
	if len(m.Bridges) != 2 || m.Bridges[1].Kind != gst.BridgePass || m.Bridges[1].Dst != "pha" || m.Bridges[1].Attached != "phtap" {
		t.Fatal("pass bridge", m.Bridges)
	}
	tp.Connect(nil)
	if m.Bridges[1].Attached != "" || m.Bridges[0].Attached != "phtap" {
		t.Error("back to idle")
	}
	if tp.TakeLate() != 0 {
		t.Error("measure")
	}
	tp.Free()
	for _, b := range m.Bridges {
		if !b.Freed {
			t.Error("not freed", b.Dst)
		}
	}
}

func TestDetailOf(t *testing.T) {
	for text, code := range map[string]string{
		"": "", WaitScreen: "phones_waiting_screen", NoAudio: "phones_screen_no_audio", NoDevice: "phones_no_device",
		notOpening: "phones_not_opening", cannotBuild + "no element \"x\"": "build_failed", "Internal data stream error.": "stream_error",
	} {
		d := DetailOf(text)
		if d.Code != code || d.Text != text {
			t.Errorf("%q: %+v", text, d)
		}
	}
	if d := DetailOf(cannotBuild + "boom"); d.Args["error"] != "boom" {
		t.Errorf("build_failed args: %v", d.Args)
	}
}
