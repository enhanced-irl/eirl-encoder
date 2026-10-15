package audio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/netcam"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/probe"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/scene"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// Error codes of the audio commands (MIXER.md §5).
const (
	CodeUnsupported   = "unsupported"
	CodeBadRequest    = "bad_request"
	CodeUnknownSource = "unknown_source"
)

// Commands are the socket commands the bus answers. Without a bus (MIXER_AUDIO_SLOTS=0) the engine
// answers all of them `unsupported`, like the Python engine.
var Commands = []string{"audio", "audio_follow", "audio_delay", "audio_link", "audio_mode", "audio_mix"}

// IsCommand reports whether cmd is one of Commands.
func IsCommand(cmd string) bool {
	for _, c := range Commands {
		if c == cmd {
			return true
		}
	}
	return false
}

// NetState is what an input asks about a network camera (*netcam.Client).
type NetState interface {
	State(path string) (netcam.Path, error)
}

// Host is what the engine hands the bus.
type Host struct {
	Media   gst.Media
	Program gst.Pipeline // has aa0…aaN, amix (and pgmlevel)
	Net     NetState     // nil: network cameras never get ready
	Changed func()       // ask for a publish, from any goroutine; nil = none
}

// Config is the bus's part of the configuration.
type Config struct {
	Slots      int    // MIXER_AUDIO_SLOTS: appsrcs aa0…aa<Slots-1>
	I2SDevice  string // the board's HDMI audio: hw:<ALSA_CARD>,<ALSA_ADMAIF-1>
	ConfigMain string // MIXER_AUDIO_MAIN: none | i2s | alsa:<card> ("" = none)
	// Sync is MIXER_SYNC (capture → program); 0 = arrival time, and then delays do not apply (like the
	// Python engine, which did not stamp at all without sync)
	Sync    time.Duration
	NetSync time.Duration // MIXER_NET_SYNC: the wifi's cushion, on a network camera's video and audio
	ASound  string        // "" = /proc/asound
}

// Bus is the program's audio slots (appsrc aaN → queue aqN → audiomixer amix) and which of them sound.
// Its methods may be called from any goroutine (they lock); normally the engine's actor calls them.
type Bus struct {
	media   gst.Media
	net     NetState
	changed func()
	cfg     Config
	asound  string
	amix    gst.Element
	links   []*slotLink
	now     func() time.Time
	descFor func(kind, device, codec string) string // Desc; tests replace it

	mu          sync.Mutex
	slots       []*Input
	retired     []*Input // stopped inputs whose goroutine may still be closing
	main        string   // the input on air, "" = no audio
	configAudio string   // the configuration's (MIXER_AUDIO_MAIN), "" = none: gate of the saved main
	silenced    bool     // the Pause with "mute": nothing sounds, without changing the choice
	follow      bool     // "the audio follows the video"
	delays      map[string]int
	overrides   map[string]string // the audio of each video source chosen by hand ("" = none)
	mode        string
	mix         map[string]bool
	gains       map[string]float64
	vols        map[int]float64      // the volume each slot has now (where a fade starts)
	lastSlot    map[string]int       // the slot of each input dropped (unplugged, or its camera without a slot): it goes back there
	unplugged   map[string]time.Time // dropped by DropGone (when), until a catalogue probed afterwards lists it (Seen)
	fading      *fadeState
	cards       string
	cardsRead   bool

	peaksMu  sync.Mutex
	pgmPeaks []float64
}

type fadeState struct {
	t0       time.Time
	old, new map[int]float64
}

// slotLink is a slot's bridge and which input owns it: an input that is still closing never detaches
// the bridge from the next input of its slot.
type slotLink struct {
	mu     sync.Mutex
	bridge gst.Bridge
	owner  *Input
}

func (l *slotLink) attach(in *Input, snk gst.Element) {
	l.mu.Lock()
	l.owner = in
	l.bridge.Attach(snk)
	l.mu.Unlock()
}

func (l *slotLink) detach(in *Input) {
	l.mu.Lock()
	if l.owner == in {
		l.bridge.Detach()
		l.owner = nil
	}
	l.mu.Unlock()
}

// PadName is the audiomixer pad of slot i: sink_0 is the background silence, then aq0, aq1… in the
// order of the program's description (checked with GStreamer in gst_real_test.go).
func PadName(slot int) string { return "sink_" + strconv.Itoa(slot+1) }

// New finds the slots in the program and makes a bridge per slot. The program must not be PLAYING yet
// for the volumes to start right; Start sets them.
func New(h Host, cfg Config) (*Bus, error) {
	if cfg.Slots <= 0 {
		return nil, fmt.Errorf("audio: no audio slots")
	}
	amix := h.Program.ByName("amix")
	if amix == nil {
		return nil, fmt.Errorf("audio: the program has no audiomixer amix")
	}
	b := &Bus{media: h.Media, net: h.Net, changed: h.Changed, cfg: cfg, asound: cfg.ASound, amix: amix,
		now: time.Now, descFor: Desc, slots: make([]*Input, cfg.Slots), mode: ModeSingle,
		delays: map[string]int{}, overrides: map[string]string{}, mix: map[string]bool{}, gains: map[string]float64{},
		vols: map[int]float64{}, lastSlot: map[string]int{}, unplugged: map[string]time.Time{}}
	if b.asound == "" {
		b.asound = "/proc/asound"
	}
	if b.changed == nil {
		b.changed = func() {}
	}
	if cfg.ConfigMain != "none" {
		b.configAudio = cfg.ConfigMain
	}
	b.main = b.configAudio
	for i := range cfg.Slots {
		src := h.Program.ByName(fmt.Sprintf("aa%d", i))
		if src == nil {
			return nil, fmt.Errorf("audio: the program has no audio slots aa0…aa%d", cfg.Slots-1)
		}
		if !amix.SetPad(PadName(i), "mute", "true") {
			return nil, fmt.Errorf("audio: the audiomixer has no input %s", PadName(i))
		}
		b.links = append(b.links, &slotLink{bridge: h.Media.NewBridge(gst.BridgeSource, h.Program, src)})
	}
	return b, nil
}

// --- persistence (mixer-scene.json: audio, configAudio, audioSettings) ---

// Load takes what was saved with the scene: the input on air (the saved one, unless the configuration's
// changed: then the configuration's) and the settings. It returns audioSettings.phones as it was, for
// the headphones (nil if none). Before Start.
func (b *Bus) Load(extra scene.Extra) json.RawMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.main = b.loadMain(extra)
	return b.loadSettings(extra.AudioSettings)
}

// loadMain is load_audio: the saved main if the saved configAudio is the configuration's and it is
// null or a string of at most 40 characters (no id check, as in Python).
func (b *Bus) loadMain(extra scene.Extra) string {
	var ca any
	if len(extra.ConfigAudio) > 0 && json.Unmarshal(extra.ConfigAudio, &ca) != nil {
		return b.configAudio
	}
	switch c := ca.(type) {
	case nil:
		if b.configAudio != "" {
			return b.configAudio
		}
	case string:
		if b.configAudio == "" || c != b.configAudio {
			return b.configAudio
		}
	default:
		return b.configAudio
	}
	var a any
	if len(extra.Audio) > 0 && json.Unmarshal(extra.Audio, &a) != nil {
		return b.configAudio
	}
	switch s := a.(type) {
	case nil:
		return ""
	case string:
		if utf8.RuneCountInString(s) <= 40 {
			return s
		}
	}
	return b.configAudio
}

// loadSettings is load_settings: each part only if it is valid, junk entries dropped.
func (b *Bus) loadSettings(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var d map[string]any
	if dec.Decode(&d) != nil || d == nil {
		return nil
	}
	if f, ok := d["follow"].(bool); ok {
		b.follow = f
	}
	if m, ok := d["delays"].(map[string]any); ok && len(m) <= maxSaved {
		b.delays = map[string]int{}
		for k, v := range m {
			if ms, ok := DelayMs(v); ok && ValidID(k) && k != "none" {
				b.delays[k] = ms
			}
		}
	}
	if m, ok := d["mode"].(string); ok && (m == ModeSingle || m == ModeMix) {
		b.mode = m
	}
	if m, ok := d["mix"].(map[string]any); ok && len(m) <= maxSaved {
		b.mix = map[string]bool{}
		for k, v := range m {
			if on, ok := v.(bool); ok && ValidID(k) && k != "none" {
				b.mix[k] = on
			}
		}
	}
	if m, ok := d["gains"].(map[string]any); ok && len(m) <= maxSaved {
		b.gains = map[string]float64{}
		for k, v := range m {
			if g, ok := GainDB(v); ok && ValidID(k) && k != "none" {
				b.gains[k] = g
			}
		}
	}
	if m, ok := d["links"].(map[string]any); ok && len(m) <= maxSaved {
		b.overrides = map[string]string{}
		for k, v := range m {
			if utf8.RuneCountInString(k) > 256 {
				continue
			}
			switch a := v.(type) {
			case nil:
				b.overrides[k] = ""
			case string:
				if ValidID(a) && a != "none" {
					b.overrides[k] = a
				}
			}
		}
	}
	if p, ok := d["phones"]; ok {
		out, err := json.Marshal(p)
		if err == nil {
			return out
		}
	}
	return nil
}

// settingsFile is audioSettings in the Python key order.
type settingsFile struct {
	Follow bool               `json:"follow"`
	Delays map[string]int     `json:"delays"`
	Links  map[string]*string `json:"links"`
	Mode   string             `json:"mode"`
	Mix    map[string]bool    `json:"mix"`
	Gains  map[string]float64 `json:"gains"`
	Phones json.RawMessage    `json:"phones,omitempty"`
}

// Extra is what goes into mixer-scene.json: audio (the input on air), configAudio and audioSettings
// (with the headphones' settings as phones, nil = none).
func (b *Bus) Extra(phones json.RawMessage) scene.Extra {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := settingsFile{Follow: b.follow, Delays: b.delays, Links: nullable(b.overrides), Mode: b.mode, Mix: b.mix,
		Gains: b.gains, Phones: phones}
	return scene.Extra{Audio: jsonNullable(b.main), ConfigAudio: jsonNullable(b.configAudio), AudioSettings: marshal(f)}
}

func marshal(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return nil
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func jsonNullable(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("null")
	}
	return marshal(s)
}

func nullable(m map[string]string) map[string]*string {
	out := make(map[string]*string, len(m))
	for k, v := range m {
		if v == "" {
			out[k] = nil
		} else {
			out[k] = &v
		}
	}
	return out
}

// --- start, discovery ---

// Start reads /proc/asound/cards (so the first CardsChanged only sees real changes), takes the Pause's
// silence and sets the volumes, without a fade. Once, before discovery.
func (b *Bus) Start(silenced bool) {
	b.CardsChanged()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.silenced = silenced
	b.apply(nil)
}

// CardsChanged reports whether /proc/asound/cards changed since the last call: the engine then probes
// again (a USB sound card plugged or unplugged).
func (b *Bus) CardsChanged() bool {
	data, _ := os.ReadFile(filepath.Join(b.asound, "cards"))
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cardsRead && string(data) == b.cards {
		return false
	}
	b.cards, b.cardsRead = string(data), true
	return true
}

type wanted struct{ id, kind, name, device string }

// Discover takes the inputs of the catalogue (probe -list → audio): the I2S in the first slot and the
// others behind. A slot whose USB input is gone is reused if another comes and none is free. With a
// catalogue that could not be read (OK=false), no network camera is removed or renamed.
func (b *Bus) Discover(c probe.Catalogue) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var want []wanted
	for _, a := range c.Audio {
		if !ValidID(a.ID) {
			continue
		}
		switch {
		case a.ID == "i2s":
			want = append([]wanted{{"i2s", KindI2S, nameI2S, b.cfg.I2SDevice}}, want...)
		case strings.HasPrefix(a.ID, "alsa:"):
			name := probe.CleanName(a.Name)
			if name == "" {
				name = nameDefault
			}
			want = append(want, wanted{a.ID, KindUSB, name, "hw:" + a.ID[5:] + ",0"})
		case strings.HasPrefix(a.ID, "net:") && a.Kind == KindNetwork:
			want = append(want, wanted{a.ID, KindNetwork, netcam.CleanName(a.Name), netcam.RTSP + netcam.PathOf(a.ID)})
		}
	}
	added := false
	if c.OK {
		// A removed network camera frees its slot; a renamed one changes its name
		nets := map[string]string{}
		for _, w := range want {
			if w.kind == KindNetwork {
				nets[w.id] = w.name
			}
		}
		for i, in := range b.slots {
			if in == nil || in.kind != KindNetwork {
				continue
			}
			if name, ok := nets[in.id]; ok {
				in.rename(name)
				continue
			}
			elog.Event("audio", "audio_input_removed", "slot", i+1, "input", in.Name())
			b.retire(i)
			added = true
		}
	}
	have := map[string]bool{}
	for _, in := range b.slots {
		if in != nil {
			have[in.id] = true
		}
	}
	// The ones coming back to their slot first: a newcomer must not take it
	slices.SortStableFunc(want, func(x, y wanted) int { return backFirst(b.lastSlot, x.id) - backFirst(b.lastSlot, y.id) })
	for _, w := range want {
		if have[w.id] {
			continue
		}
		slot, ok := b.freeSlot(w.id)
		if !ok {
			elog.Event("audio", "audio_no_free_slot", "input", w.name)
			continue
		}
		if b.slots[slot] != nil {
			b.retire(slot)
		}
		in := newInput(b, slot, w.id, w.kind, w.name, w.device)
		b.slots[slot] = in
		have[w.id] = true
		delete(b.lastSlot, w.id)
		delete(b.unplugged, w.id)
		b.links[slot].bridge.SetParams(b.params(in))
		go in.run()
		added = true
	}
	if added {
		b.apply(nil)
	}
	b.pruneRetired()
}

// retire stops the input of slot i and frees the slot (with b.mu).
func (b *Bus) retire(i int) {
	b.slots[i].stop()
	b.retired = append(b.retired, b.slots[i])
	b.slots[i] = nil
}

func (b *Bus) pruneRetired() {
	keep := b.retired[:0]
	for _, in := range b.retired {
		if !waitDone(in.done, 0) {
			keep = append(keep, in)
		}
	}
	clear(b.retired[len(keep):])
	b.retired = keep
}

// DropGone frees the slots of the USB inputs whose card has been gone for grace (the camera or the
// sound card unplugged), as Discover does with a removed network camera; it goes back to the same slot
// if it comes back while that one is free. Its settings (volume, delay, mix) are kept by id. keep: the
// audio of a camera that keeps its video slot (on air, or hung on its port): it goes with its video. It
// returns the ids it dropped.
func (b *Bus) DropGone(grace time.Duration, keep map[string]bool) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	var dropped []string
	for i, in := range b.slots {
		if in == nil || in.kind != KindUSB || keep[in.id] {
			continue
		}
		if state, since := in.snapshot(); state != StateGone || now.Sub(since) < grace {
			continue
		}
		elog.Event("audio", "audio_input_removed", "slot", i+1, "input", in.Name(), "reason", "unplugged")
		b.lastSlot[in.id] = i
		b.unplugged[in.id] = now
		dropped = append(dropped, in.id)
		b.retire(i)
	}
	if len(dropped) > 0 {
		b.apply(nil)
	}
	b.pruneRetired()
	return dropped
}

// Unplugged: was input id dropped by DropGone, and no catalogue probed since has listed it?
func (b *Bus) Unplugged(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.unplugged[id]
	return ok
}

// Seen: a catalogue probed from started lists ids: those dropped before it are back. Not one dropped while
// that probe ran (it may have seen it still there).
func (b *Bus) Seen(ids []string, started time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range ids {
		if t, ok := b.unplugged[id]; ok && t.Before(started) {
			delete(b.unplugged, id)
		}
	}
}

// Retire stops the inputs in ids and frees their slots, saying why in the log (no_video_slot: a camera's own
// audio while that camera has no video slot; no_audio_slot: a loose sound card giving its slot to a camera's).
// They go back to the same slot when they come again. The one on air is never retired. true if any was.
func (b *Bus) Retire(ids map[string]bool, reason string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	done := false
	for i, in := range b.slots {
		if in == nil || !ids[in.id] || in.id == b.main {
			continue
		}
		elog.Event("audio", "audio_input_removed", "slot", i+1, "input", in.Name(), "reason", reason)
		b.lastSlot[in.id] = i
		b.retire(i)
		done = true
	}
	if done {
		b.apply(nil)
	}
	b.pruneRetired()
	return done
}

// Slots is how many audio slots there are.
func (b *Bus) Slots() int { return len(b.slots) }

// Overrides is the audio of each video source chosen by hand ({video id: audio id, "" = none}), a copy.
func (b *Bus) Overrides() map[string]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return maps.Clone(b.overrides)
}

// freeSlot is where input id goes: the first slot for the I2S if free; the slot it had when it left if
// still free; else a free one; else the one gone the longest (for more than goneReuse). With b.mu.
func (b *Bus) freeSlot(id string) (int, bool) {
	if id == "i2s" && b.slots[0] == nil {
		return 0, true
	}
	if i, ok := b.lastSlot[id]; ok && i < len(b.slots) && b.slots[i] == nil {
		return i, true
	}
	for i, in := range b.slots {
		if in == nil {
			return i, true
		}
	}
	best, found := 0, false
	var bestSince time.Time
	now := b.now()
	for i, in := range b.slots {
		state, since := in.snapshot()
		if state == StateGone && now.Sub(since) > goneReuse && (!found || since.Before(bestSince)) {
			best, bestSince, found = i, since, true
		}
	}
	return best, found
}

// backFirst orders the ones coming back to a remembered slot (0) before the rest (1).
func backFirst(last map[string]int, id string) int {
	if _, ok := last[id]; ok {
		return 0
	}
	return 1
}

// params are an input's bridge timing: capture time + MIXER_SYNC, plus its delay and, for a network
// camera, the same cushion as its video. With b.mu.
func (b *Bus) params(in *Input) gst.BridgeParams {
	p := gst.BridgeParams{Sync: b.cfg.Sync}
	if b.cfg.Sync == 0 {
		return p
	}
	p.Extra = time.Duration(b.delays[in.id]) * time.Millisecond
	if in.kind == KindNetwork {
		p.Extra += b.cfg.NetSync
	}
	return p
}

func (b *Bus) slotOf(id string) (int, bool) {
	for i, in := range b.slots {
		if in != nil && in.id == id {
			return i, true
		}
	}
	return 0, false
}

func (b *Bus) nameOf(id string) string {
	if i, ok := b.slotOf(id); ok {
		return b.slots[i].Name()
	}
	return ""
}

// --- which ones sound ---

func (b *Bus) inMix(id string) bool {
	if on, ok := b.mix[id]; ok {
		return on
	}
	return id == b.main
}

func (b *Bus) targets() map[int]float64 {
	var ins []SlotID
	for i, in := range b.slots {
		if in != nil {
			ins = append(ins, SlotID{i, in.id})
		}
	}
	return TargetVolumes(ins, b.mode, b.main, b.mix, b.gains, b.silenced)
}

// apply sets each slot's volume (the targets, or vols during a fade); 0 also mutes. With b.mu.
func (b *Bus) apply(vols map[int]float64) {
	if vols == nil {
		vols = b.targets()
	}
	for i := range b.links {
		v := min(maxVolume, vols[i])
		b.amix.SetPad(PadName(i), "volume", strconv.FormatFloat(v, 'f', 6, 64))
		b.amix.SetPad(PadName(i), "mute", strconv.FormatBool(v <= 0))
	}
	b.vols = make(map[int]float64, len(vols))
	for k, v := range vols {
		b.vols[k] = v
	}
}

// refresh fades from the volumes of now to the targets in Fade. With b.mu.
func (b *Bus) refresh() {
	old := make(map[int]float64, len(b.vols))
	for k, v := range b.vols {
		old[k] = v
	}
	b.fading = &fadeState{t0: b.now(), old: old, new: b.targets()}
}

// Fading reports whether a fade is running: the engine then calls Tick every 20 ms.
func (b *Bus) Fading() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fading != nil
}

// Tick moves the fade to now; at its end, the target volumes.
func (b *Bus) Tick(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := b.fading
	if f == nil {
		return
	}
	t := float64(now.Sub(f.t0)) / float64(Fade)
	if t >= 1 {
		b.fading = nil
		b.apply(nil)
		return
	}
	b.apply(Blend(f.old, f.new, t))
}

// setMain puts id on air ("" = no audio), with a fade. With b.mu.
func (b *Bus) setMain(id string) bool {
	if id == b.main {
		return false
	}
	b.main = id
	b.refresh()
	name := b.nameOf(id)
	if id == "" {
		name = ""
	}
	elog.Event("audio", "audio_main_changed", "input", id, "name", name)
	return true
}

// Main is the input on air ("" = no audio).
func (b *Bus) Main() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.main
}

// SetSilenced: silence (with a fade) while the Pause with "mute" lasts, and back as it was. The engine
// calls it after every scene change with pause.on && pause.mute. Reports whether it changed.
func (b *Bus) SetSilenced(on bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if on == b.silenced {
		return false
	}
	b.silenced = on
	b.refresh()
	return true
}

func (b *Bus) setMode(mode string) {
	if mode == b.mode {
		return
	}
	if mode == ModeMix && b.main != "" {
		b.mix[b.main] = true // the mix always starts with what was sounding (never silent)
	}
	b.mode = mode
	b.refresh()
	elog.Event("audio", "audio_mode_changed", "mode", mode)
}

func (b *Bus) setMix(id string, on *bool, gain *float64) {
	if on != nil {
		b.mix[id] = *on
	}
	if gain != nil {
		if *gain != 0 {
			b.gains[id] = *gain
		} else {
			delete(b.gains, id)
		}
	}
	b.refresh()
}

// Follow: with "the audio follows the video" (and in single mode), the audio of the video source on air,
// if it has one and its input is here. The engine calls it when the scene's main changed. Reports
// whether the audio on air changed (the engine saves the scene anyway after a scene change).
func (b *Bus) Follow(v View) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.followLocked(v)
}

func (b *Bus) followLocked(v View) bool {
	if !b.follow || b.mode != ModeSingle {
		return false
	}
	id := FollowTarget(ResolveLinks(v.Videos, b.overrides), v.Main)
	if _, ok := b.slotOf(id); id != "" && ok {
		return b.setMain(id)
	}
	return false
}

// --- commands ---

// Result is the outcome of an audio command.
type Result struct {
	OK   bool
	Code string // error code when !OK
	// Changed: save mixer-scene.json (with Extra) and publish before answering
	Changed bool
	// LogCommand: log it like a scene command ([mixer] event=command); `audio` logs its own event
	LogCommand bool
}

// Command runs an audio command (MIXER.md §5) on the actor. v is the video side now (audio_link checks
// the video id; follow uses the main). Validation order and codes as in the Python engine.
func (b *Bus) Command(cmd string, msg map[string]any, v View) Result {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cmd == "audio" {
		// The input on air, independent of the video; null or "none" = no audio
		var id string
		switch s := msg["source"].(type) {
		case nil:
		case string:
			if s != "none" {
				if _, ok := b.slotOf(s); !ok {
					return Result{Code: CodeUnknownSource}
				}
				id = s
			}
		default:
			return Result{Code: CodeUnknownSource}
		}
		changed := b.setMain(id)
		return Result{OK: true, Changed: changed}
	}
	switch cmd {
	case "audio_follow":
		on, ok := msg["on"].(bool)
		if !ok {
			return Result{Code: CodeBadRequest}
		}
		b.follow = on
		if on {
			b.followLocked(v)
		}
	case "audio_mode":
		mode, _ := msg["mode"].(string)
		if mode != ModeSingle && mode != ModeMix {
			return Result{Code: CodeBadRequest}
		}
		b.setMode(mode)
	case "audio_mix":
		onv, gv := msg["on"], msg["gain"]
		var on *bool
		var gain *float64
		if onv == nil && gv == nil {
			return Result{Code: CodeBadRequest}
		}
		if onv != nil {
			o, ok := onv.(bool)
			if !ok {
				return Result{Code: CodeBadRequest}
			}
			on = &o
		}
		if gv != nil {
			g, ok := GainDB(gv)
			if !ok {
				return Result{Code: CodeBadRequest}
			}
			gain = &g
		}
		id, ok := msg["source"].(string)
		if _, found := b.slotOf(id); !ok || !found {
			return Result{Code: CodeUnknownSource}
		}
		b.setMix(id, on, gain)
	case "audio_delay":
		ms, ok := DelayMs(msg["ms"])
		if !ok {
			return Result{Code: CodeBadRequest}
		}
		id, ok := msg["source"].(string)
		slot, found := b.slotOf(id)
		if !ok || !found {
			return Result{Code: CodeUnknownSource}
		}
		if ms != 0 {
			b.delays[id] = ms
		} else {
			delete(b.delays, id)
		}
		b.links[slot].bridge.SetParams(b.params(b.slots[slot]))
	case "audio_link":
		vid, ok := msg["video"].(string)
		if !ok || !hasVideo(v, vid) {
			return Result{Code: CodeUnknownSource}
		}
		var id string
		switch s := msg["source"].(type) {
		case string:
			if s != "none" {
				if _, found := b.slotOf(s); !found {
					return Result{Code: CodeUnknownSource}
				}
				id = s
			}
		default: // null too: unlike `audio`, only "none" means no audio here
			return Result{Code: CodeUnknownSource}
		}
		b.overrides[vid] = id
		if vid == v.Main {
			b.followLocked(v)
		}
	default:
		return Result{Code: "unknown_command"}
	}
	// The settings commands always save and publish, also when nothing changed (as in Python)
	return Result{OK: true, Changed: true, LogCommand: true}
}

func hasVideo(v View, id string) bool {
	for _, x := range v.Videos {
		if x.ID == id {
			return true
		}
	}
	return false
}

// --- state ---

// InputState is one input in the state (MIXER.md §5, audio.sources[]).
type InputState struct {
	ID     string `json:"id"`
	Slot   int    `json:"slot"` // 1-based
	Kind   string `json:"kind"` // i2s | usb | network
	Name   string `json:"name"`
	State  string `json:"state"`  // starting | live | no_signal | gone | error
	Detail string `json:"detail"` // Spanish, as always; the interfaces translate DetailCode
	// DetailCode is the stable code of Detail (internal/engine/detail), "" for none; DetailArgs its arguments
	DetailCode string            `json:"detailCode"`
	DetailArgs map[string]string `json:"detailArgs,omitempty"`
	DelayMs    int               `json:"delayMs"`
	InMix      bool              `json:"inMix"`
	GainDb     float64           `json:"gainDb"`
	PeakDb     *float64          `json:"peakDb,omitempty"`    // live and a level has arrived
	VideoSlot  *int              `json:"videoSlot,omitempty"` // the video source of the same device
}

// State is the state's `audio` object.
type State struct {
	Main    *string            `json:"main"`
	Sources []InputState       `json:"sources"`
	Follow  bool               `json:"follow"`
	Links   map[string]*string `json:"links"` // resolved: by hand, else the same device's
	Mode    string             `json:"mode"`
	// Phones is the headphones' state, set by the engine when there is a phtap (nil = absent)
	Phones any `json:"phones,omitempty"`
}

// State is the `audio` object of the state, given the video side.
func (b *Bus) State(v View) State {
	b.mu.Lock()
	defer b.mu.Unlock()
	infos := []InputState{}
	for _, in := range b.slots {
		if in == nil {
			continue
		}
		d := in.info()
		d.DelayMs, d.InMix, d.GainDb = b.delays[in.id], b.inMix(in.id), b.gains[in.id]
		infos = append(infos, d)
	}
	st := State{Sources: OrderInputs(infos, SameDevice(v.Videos)), Follow: b.follow,
		Links: nullable(ResolveLinks(v.Videos, b.overrides)), Mode: b.mode}
	if b.main != "" {
		m := b.main
		st.Main = &m
	}
	return st
}

// InputPeaks is the peak (dB) of each live input with a level, by id (the screen's meters).
func (b *Bus) InputPeaks() map[string]float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]float64{}
	for _, in := range b.slots {
		if in == nil {
			continue
		}
		if d := in.info(); d.PeakDb != nil {
			out[in.id] = in.rawPeak()
		}
	}
	return out
}

// OnProgramMessage takes the program's `level` messages (element pgmlevel, every 50 ms): the program's
// peaks for the screen and the headphones. The engine's program watcher must pop MessageElement too
// and hand them here (popping with a filter drops the other types). Reports whether it was one.
func (b *Bus) OnProgramMessage(m *gst.Message) bool {
	if m == nil || m.Type != gst.MessageElement || m.Src != "pgmlevel" || m.Text != "level" {
		return false
	}
	p := ParsePeaks(m.Debug)
	b.peaksMu.Lock()
	b.pgmPeaks = p
	b.peaksMu.Unlock()
	return true
}

// ProgramPeaks is the level of what goes on air (dB per channel), nil if unknown.
func (b *Bus) ProgramPeaks() []float64 {
	b.peaksMu.Lock()
	defer b.peaksMu.Unlock()
	if b.pgmPeaks == nil {
		return nil
	}
	return append([]float64(nil), b.pgmPeaks...)
}

// InputInfo is one input as other parts of the engine see it.
type InputInfo struct {
	ID, Kind, Name, State string
	Volume                float64 // its target volume now (0 = it does not sound)
}

// Inputs is every input with its target volume: the audio part of usb_on_air (what sounds cannot be
// powered off while emitting to recover another USB device of the same hub). The engine maps it to
// usbhub.AudioSlot; usbhub.Air.Devices keeps the live USB ones with Volume > 0 as "el audio de <name>".
func (b *Bus) Inputs() []InputInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	vols := b.targets()
	var out []InputInfo
	for i, in := range b.slots {
		if in == nil {
			continue
		}
		state, _ := in.snapshot()
		out = append(out, InputInfo{ID: in.id, Kind: in.kind, Name: in.Name(), State: state, Volume: vols[i]})
	}
	return out
}

// TimingsKV is the audio part of the engine's `timings` event (every 60 s): per live input, the age of
// its buffers on arrival and the longest gap. The window restarts each time.
func (b *Bus) TimingsKV() []any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var kv []any
	for i, in := range b.slots {
		if in == nil {
			continue
		}
		st := b.links[i].bridge.Stats(true, false)
		if state, _ := in.snapshot(); state != StateLive || st.N == 0 {
			continue
		}
		kv = append(kv, fmt.Sprintf("audio%d", i+1), fmt.Sprintf("age=%s/%s/%sms gap=%sms",
			fmtMs(st.AgeMin), fmtMs(st.AgeAvg), fmtMs(st.AgeMax), fmtMs(st.MaxGap)))
	}
	return kv
}

func fmtMs(d time.Duration) string { return fmt.Sprintf("%.0f", float64(d)/float64(time.Millisecond)) }

// --- stop ---

// Stop asks every input to stop; it does not wait (Wait does).
func (b *Bus) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, in := range b.slots {
		if in != nil {
			in.stop()
		}
	}
}

// Wait waits until every input's goroutine has ended or until the deadline, and returns how many are
// still closing. An I2S capture without clock takes ~5 s to reach NULL (MIXER.md §3, trap 19): it is
// left behind (its bridge already detached), and the process exit ends it.
func (b *Bus) Wait(until time.Time) int {
	b.mu.Lock()
	all := append([]*Input(nil), b.retired...)
	for _, in := range b.slots {
		if in != nil {
			all = append(all, in)
		}
	}
	b.mu.Unlock()
	pending := 0
	for _, in := range all {
		if !waitDone(in.done, time.Until(until)) {
			pending++
		}
	}
	return pending
}

// waitDone waits up to d for ch to close (d ≤ 0: just looks).
func waitDone(ch <-chan struct{}, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	}
}
