// Package phones is the touch screen's headphones (HEADPHONES.md): the program's audio out of the
// board's HDMI, to hear it on headphones plugged into the screen's jack. A port of the Python engine's
// engine_phones.py, which stays the reference.
//
//	audio ─ tee at ─▶ queue ─▶ valve phvalve ─▶ appsink phtap ─┐  (program pipeline)
//	                                                           └▶ Tap (C) ─┐
//	                      appsrc pha ─▶ queue ─▶ volume phvol ─▶ alsasink  ◀┘  (listening pipeline)
//
// The listening runs in its own pipeline: an alsasink without a screen, or with one that takes no
// audio, fails to open, and in the program's pipeline that would stop the broadcast. Here only the
// listening falls, and it retries. With the program's clock and base_time and sync=true it plays at the
// program's marks plus the screen's delay: what is heard goes with what is seen. A reconnection of the
// screen does not bring it down: alsasink goes on and sounds again by itself (HEADPHONES.md §1.2). Off,
// the valve is closed and not a buffer moves; on, the buffers cross in C (Tap) and never enter Go.
//
// This file holds the parts without GStreamer; listen.go is the goroutine that runs the listening.
package phones

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/btaudio"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/detail"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// Settings and timings (engine_phones.py).
const (
	ACaps       = "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved"
	GainMin     = mixer.MinPhonesGain // dB; silence (−∞), not −40 dB (D4 of HEADPHONES.md)
	GainMax     = mixer.MaxPhonesGain
	DefaultOn   = true // D1: on, at −12 dB until someone changes it
	DefaultGain = -12

	Ramp         = 100 * time.Millisecond // volume ramp: no clicks when switching on, off or changing
	rampStep     = 10 * time.Millisecond
	NoDeviceWait = 30 * time.Second // before looking for the HDMI output again when there is none
	ELDEvery     = 3 * time.Second  // between ELD reads (whether the screen takes audio now)
	CloseWait    = 3 * time.Second  // at most closing the pipeline (like the I2S without clock, trap 19)
	Trim         = 0                // fine tuning of the sync with the screen (clap test, A3)
	Relatch      = 5 * time.Millisecond
	Measure      = time.Second // measuring when the program audio arrives, before opening the HDMI
	// SinkBuffer is alsasink's buffer-time: what ALSA reads ahead. Whatever is written closer to its time
	// falls into a chunk already read and is silently lost (HEADPHONES.md §1.3).
	SinkBuffer = 40 * time.Millisecond
	// BTSinkBuffer is alsasink's buffer-time on Bluetooth headphones (BLUETOOTH.md §3.3). The same as the HDMI's:
	// what cut the sound on the board was the clock slaving, not the buffer (BlueALSA's plugin keeps its pipe to a
	// page), and every ms here is a ms more behind the screen
	BTSinkBuffer = 40 * time.Millisecond
	LateMargin   = 30 * time.Millisecond // cushion for hiccups, on top of ALSA's buffer
	// TargetPoll: how often the monitor looks at where it should play (the app's Bluetooth file)
	TargetPoll = time.Second
	// MaxLates is how many arrival samples a Tap keeps between two takes (the Python list's cap).
	MaxLates = 400
	// MVMargin is what the screen adds to the program's marks (Python MV_MARGIN): keep it equal to the
	// screen's.
	MVMargin = 40 * time.Millisecond

	offWait    = 5 * time.Second
	screenPoll = time.Second
	busPoll    = 100 * time.Millisecond
	// A failure after this long playing counts as the first one again
	freshAfter = 60 * time.Second
)

// Retry is the wait between consecutive retries after an alsasink error.
var Retry = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second}

// User-visible details of audio.phones (contract: Spanish, as the Python engine; C5 translates them).
const (
	WaitScreen = "Esperando a la pantalla"
	NoAudio    = "La pantalla no admite audio"
	NoDevice   = "La placa no tiene salida de audio HDMI"
	// Other details: the GStreamer error (one line, ≤120 characters) and these two
	notOpening  = "la salida HDMI no se abre"
	cannotBuild = "no se puede construir: "
)

// DetailOf is a detail of audio.phones with its code (internal/engine/detail): the texts above, or a
// GStreamer error.
func DetailOf(text string) detail.D {
	switch {
	case text == "":
		return detail.D{}
	case text == WaitScreen:
		return detail.New("phones_waiting_screen")
	case text == NoAudio:
		return detail.New("phones_screen_no_audio")
	case text == NoDevice:
		return detail.New("phones_no_device")
	case text == notOpening:
		return detail.New("phones_not_opening")
	case strings.HasPrefix(text, cannotBuild):
		return detail.New("build_failed", "error", strings.TrimPrefix(text, cannotBuild))
	}
	return detail.New("stream_error", "error", text)
}

// States of audio.phones.
const (
	StateOff     = "off"
	StateWaiting = "waiting"
	StateOn      = "on"
	StateError   = "error"
)

// ELD is what the screen's ELD says.
type ELD string

const (
	ELDOK       ELD = "ok"        // the screen takes audio
	ELDNoAudio  ELD = "no_audio"  // it announces no PCM
	ELDNoScreen ELD = "no_screen" // no screen, or its ELD not valid yet
	ELDUnknown  ELD = ""          // nothing to read: unknown (Python None)
)

// GainDB is a valid gain (integer dB, GainMin…GainMax, rounded half to even like Python's round), or
// false. v is decoded JSON: json.Number (the socket), float64, or a Go integer (the screen). Booleans,
// strings, NaN and anything out of range are not gains.
func GainDB(v any) (int, bool) {
	var f float64
	switch x := v.(type) {
	case json.Number:
		g, err := x.Float64()
		if err != nil {
			return 0, false
		}
		f = g
	case float64:
		f = x
	case float32:
		f = float64(x)
	case int:
		f = float64(x)
	case int64:
		f = float64(x)
	case int32:
		f = float64(x)
	default:
		return 0, false
	}
	if math.IsNaN(f) || f < GainMin || f > GainMax {
		return 0, false
	}
	return int(math.RoundToEven(f)), true
}

// VolumeOf is the linear volume of a gain: GainMin is real silence.
func VolumeOf(gain int) float64 {
	if gain <= GainMin {
		return 0
	}
	return math.Pow(10, float64(gain)/20)
}

// Change is what a `phones` command asks for; nil = unchanged.
type Change struct {
	On   *bool
	Gain *int
}

// ParseCommand reads a `phones` command: on (bool) and/or gain (GainDB), at least one; JSON null is
// absent. The error is the socket's code (bad_request).
func ParseCommand(msg map[string]any) (Change, string) {
	ov, gv := msg["on"], msg["gain"]
	if ov == nil && gv == nil {
		return Change{}, "bad_request"
	}
	var c Change
	if ov != nil {
		on, ok := ov.(bool)
		if !ok {
			return Change{}, "bad_request"
		}
		c.On = &on
	}
	if gv != nil {
		g, ok := GainDB(gv)
		if !ok {
			return Change{}, "bad_request"
		}
		c.Gain = &g
	}
	return c, ""
}

// Device is where the monitor plays: the HDMI audio output (the ALSA device, by the card's name when it has one:
// its number changes with the USB cards, and the card's number, for its ELD and its mixer), or Bluetooth headphones
// (BT: the device of the app's file, and Name its BlueALSA device; no card).
type Device struct {
	Name string // hw:tegrahda,3 | bluealsa:DEV=…,PROFILE=…
	Card int
	BT   *btaudio.Device
}

// sameBT: the same Bluetooth target (nil: the HDMI).
func sameBT(a, b *btaudio.Device) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Addr == b.Addr && a.Profile == b.Profile
}

var (
	reCard = regexp.MustCompile(`^\s*(\d+)\s+\[([A-Za-z0-9_-]{1,32})\s*\]`)
	rePCM  = regexp.MustCompile(`^(\d+)-(\d+): HDMI[^:]* :.*\bplayback \d`)
)

// FindHDMI finds the HDMI audio output in /proc/asound/pcm («00-03: HDMI 0 : HDMI 0 : playback 1») and
// /proc/asound/cards (« 0 [tegrahda       ]: tegra-hda - tegra-hda»): the first HDMI device that plays.
func FindHDMI(pcm, cards string) (Device, bool) {
	ids := map[int]string{}
	for _, line := range splitLines(cards) {
		if m := reCard.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				ids[n] = m[2]
			}
		}
	}
	for _, line := range splitLines(pcm) {
		m := rePCM.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		card, err1 := strconv.Atoi(m[1])
		dev, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil {
			continue
		}
		if name, ok := ids[card]; ok {
			return Device{Name: "hw:" + name + "," + strconv.Itoa(dev), Card: card}, true
		}
		return Device{Name: "hw:" + strconv.Itoa(card) + "," + strconv.Itoa(dev), Card: card}, true
	}
	return Device{}, false
}

// ParseELD reads a /proc/asound/cardN/eld#*: monitor_present, eld_valid and whether it announces PCM.
func ParseELD(text string) (present, valid, lpcm bool) {
	kv := map[string]string{}
	for _, line := range splitLines(text) {
		s := strings.TrimLeftFunc(line, unicode.IsSpace)
		i := strings.IndexFunc(s, unicode.IsSpace)
		if i < 0 {
			continue
		}
		v := strings.TrimSpace(s[i:])
		if v == "" { // Python's split(None, 1) gives one field
			continue
		}
		kv[s[:i]] = v
	}
	for k, v := range kv {
		if strings.HasPrefix(k, "sad") && strings.HasSuffix(k, "_coding_type") && strings.Contains(v, "LPCM") {
			lpcm = true
		}
	}
	return kv["monitor_present"] == "1", kv["eld_valid"] == "1", lpcm
}

// ELDVerdict of the ELDs of a card's pins: one that takes PCM is enough. Mind: with the device closed
// the ELD goes stale (the codec sleeps and does not read it again, HEADPHONES.md §1): it is only worth
// reading with the pipeline open.
func ELDVerdict(texts []string) ELD {
	v := ELDUnknown
	for _, t := range texts {
		_, valid, lpcm := ParseELD(t)
		switch {
		case valid && lpcm:
			return ELDOK
		case valid:
			v = ELDNoAudio
		case v == ELDUnknown:
			v = ELDNoScreen
		}
	}
	return v
}

// StatusOf is the public (state, detail). err is the last error while retrying.
func StatusOf(want, opened bool, err string, eld ELD) (string, string) {
	switch {
	case !want:
		return StateOff, ""
	case err != "":
		return StateError, err
	case !opened || eld == ELDNoScreen:
		return StateWaiting, WaitScreen
	case eld == ELDNoAudio:
		return StateWaiting, NoAudio
	}
	return StateOn, ""
}

// P90 is the 90th percentile (what it takes almost always, without the lone hiccup), or 0 without
// samples. The Tap computes it in C over the same window; this is its reference.
func P90(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	v := slices.Clone(values)
	slices.Sort(v)
	return v[min(len(v)-1, int(float64(len(v))*0.9))]
}

// TargetDelay is the listening's latency: the screen's (in step with its picture), unless the program
// audio arrives later than that. Then its arrival plus ALSA's buffer (it reads SinkBuffer ahead: what is
// written closer to its time falls into a chunk already read and does not sound, HEADPHONES.md §1.3)
// and a cushion. late 0 = not measured.
func TargetDelay(screen, late time.Duration) time.Duration {
	return TargetDelayFor(screen, late, SinkBuffer)
}

// TargetDelayFor is TargetDelay with the sink's buffer (SinkBuffer on the HDMI, BTSinkBuffer on Bluetooth). On
// Bluetooth, screen comes minus what the headphones add after the ALSA device (BLUETOOTH.md §3.3), so it can go
// below zero: the arrival rules then.
func TargetDelayFor(screen, late, buffer time.Duration) time.Duration {
	if late == 0 {
		return max(screen, 0)
	}
	return max(screen, late+buffer+LateMargin)
}

// DelayMoved: whether the listening must change its latency (the screen was rebuilt with another one:
// mode change, another EDID) by more than Relatch. known false = unknown now: leave it alone.
func DelayMoved(cur, next time.Duration, known bool) bool {
	if !known {
		return false
	}
	d := next - cur
	return d > Relatch || d < -Relatch
}

// RetryWait is the wait after the fails-th failure in a row.
func RetryWait(fails int) time.Duration { return retryWait(fails, Retry) }

func retryWait(fails int, table []time.Duration) time.Duration {
	return table[min(max(fails, 1), len(table))-1]
}

// ShortError is GStreamer's message, short and on one line, for the state.
func ShortError(text string) string {
	if text == "" {
		text = "error"
	}
	s := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(s) > 120 {
		s = string([]rune(s)[:120])
	}
	return s
}

// ScreenView is the touch screen as the listening sees it (the engine's Screen, M3).
type ScreenView interface {
	// State is the screen's published state: off | waiting | on | error.
	State() string
	// Latency is its pipeline's latency (a latency query), false when unknown.
	Latency() (time.Duration, bool)
}

// ScreenReady: the listening may open. Without a screen configured (nil) it opens anyway; with one,
// only once it is on (stable: Screen knows it by SCREEN_STABLE).
func ScreenReady(s ScreenView) bool { return s == nil || s.State() == StateOn }

// ScreenDelay is how far the screen shows the program behind its marks (Engine.screen_delay_ns): the
// mark on_program gives it (the compositors' latency + MVMargin) plus its pipeline's latency. Without
// the screen's latency (none, or being rebuilt) the compositors' instead; with known, false: unknown.
func ScreenDelay(compositor time.Duration, s ScreenView, known bool) (time.Duration, bool) {
	var lat time.Duration
	have := false
	if s != nil && s.State() == StateOn {
		lat, have = s.Latency()
	}
	if !have {
		if known && s != nil {
			return 0, false
		}
		lat = compositor
	}
	return compositor + MVMargin + lat, true
}

// splitLines splits like Python's str.splitlines for the line ends /proc can have.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
