// Package audio is the program's audio, independent of the video (F4 of MIXER.md; the Python
// engine's engine_audio.py and the audio parts of engine_mixer.py).
//
// Every audio input (the board's HDMI I2S, each USB sound card, each network camera's audio track)
// runs in its OWN pipeline, like the video sources, ending in an appsink. Its buffers cross to the
// program's appsrc aaN (→ queue aqN → audiomixer amix → pgmlevel → tee at) through a gst.Bridge in C:
// no audio buffer ever enters Go. One input sounds (decided on Oct 12): the one on air at volume 1
// and the others muted but capturing, to switch at once with a short fade; in "mix" mode the marked
// ones sound together. If one is unplugged or stops, only it falls.
//
// The Bus is owned by the engine's actor (commands, discovery, fades, state); each input has its own
// goroutine that only opens, watches and closes its pipeline.
package audio

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Caps is the format of every input and of the program's audio.
const Caps = "audio/x-raw,format=S16LE,rate=48000,channels=2"

const (
	// Fade is the volume fade on every change (no clicks).
	Fade = 250 * time.Millisecond
	// Watchdog: without samples this long, a USB or network input is reopened (not the I2S: the
	// TC358743 stops its clock without a camera or without sound, and closing it takes ~5 s).
	Watchdog = 6 * time.Second
	// MinGain and MaxGain bound each input's gain in dB (0 = unchanged).
	MinGain, MaxGain = -40.0, 12.0
	// MaxDelayMs bounds each input's delay against the video.
	MaxDelayMs = 500
	// maxVolume is the largest linear volume set on a mixer pad.
	maxVolume = 10.0
	// goneReuse: a slot whose input is gone this long can be given to a new one when none is free.
	goneReuse = 10 * time.Second
	// maxSaved bounds each map loaded from mixer-scene.json.
	maxSaved = 16
)

// Input kinds.
const (
	KindI2S       = "i2s"
	KindUSB       = "usb"
	KindNetwork   = "network"
	KindBluetooth = "bluetooth" // a headset's microphone or a phone, through BlueALSA (BLUETOOTH.md §3.3)
)

// Input states (the state contract).
const (
	StateStarting = "starting"
	StateLive     = "live"
	StateNoSignal = "no_signal"
	StateGone     = "gone"
	StateError    = "error"
)

// Modes.
const (
	ModeSingle = "single"
	ModeMix    = "mix"
)

// Names and details of the state contract: Spanish, exactly as in the Python engine.
const (
	nameI2S       = "HDMI de la placa"
	nameDefault   = "Audio"
	nameBluetooth = "Bluetooth"
)

// stateText is how the log names each state (STATE_TEXT, in English).
var stateText = map[string]string{StateStarting: "opening", StateLive: "live", StateNoSignal: "no_signal",
	StateGone: "gone", StateError: "error"}

var (
	// reID is an audio id: none, the I2S, a USB card by name, a network camera (F7) or a Bluetooth device by its
	// address without colons (BLUETOOTH.md §3.3).
	reID   = regexp.MustCompile(`^(none|i2s|alsa:[A-Za-z0-9_-]{1,32}|net:cam[1-8]|bt:[0-9A-F]{12})$`)
	rePeak = regexp.MustCompile(`peak=\(GValueArray\)< ?([^>]*?) ?>`)
)

// ValidID reports whether id is an audio id (RE_AUDIO_ID; "none" included).
func ValidID(id string) bool { return reID.MatchString(id) }

// NetDecoders is what goes behind rtspsrc for a network camera's audio track, per codec (engine_net.AUDIO).
var NetDecoders = map[string]string{
	"aac":  "rtpmp4gdepay ! aacparse ! avdec_aac",
	"opus": "rtpopusdepay ! opusdec",
}

// RTSPLatencyMs is rtspsrc's jitterbuffer for network cameras (engine_net.RTSP_LATENCY_MS).
const RTSPLatencyMs = 100

// Desc is an input's pipeline. provide-clock=false: the clock is the system's, like the program's; with
// the card's, one that stops (the I2S without a camera) would stop the pipeline. level: the meter for the
// web, 5 times a second (interval 50 ms, posted at most every 200 ms of 40 ms chunks). The appsink is
// driven by the bridge's callbacks (emit-signals=false). network: device is the camera's local RTSP
// URL and codec its audio codec (aac, opus); its own session, the audio track only.
func Desc(kind, device, codec string) string {
	var head string
	switch kind {
	case KindI2S:
		head = "alsasrc device=" + device + " provide-clock=false latency-time=40000 ! " + Caps
	case KindNetwork:
		head = "rtspsrc name=cap location=" + device + " protocols=tcp latency=" + strconv.Itoa(RTSPLatencyMs) +
			" drop-on-latency=true ! " + NetDecoders[codec] + " ! audioconvert ! audioresample ! " + Caps
	case KindBluetooth:
		// The BlueALSA plugin's device (bluealsa:DEV=…,PROFILE=…, built from a checked address and profile), quoted
		// for its commas. Hands-free is mono at 8 kHz, A2DP 44.1 or 48 kHz; a bigger buffer than a card's: what
		// arrives over the air comes in bursts
		head = "alsasrc device=\"" + device + "\" provide-clock=false latency-time=40000 buffer-time=200000" +
			" ! audioconvert ! audioresample ! " + Caps
	default:
		// audioconvert/audioresample: some microphones are mono or 44.1 kHz
		head = "alsasrc device=" + device + " provide-clock=false latency-time=40000 ! audioconvert ! audioresample ! " + Caps
	}
	return head + " ! level interval=50000000 post-messages=true" +
		" ! appsink name=snk emit-signals=false sync=false max-buffers=20 drop=true enable-last-sample=false"
}

// ParsePeaks reads the peaks (dB, one per channel, none below -90) of a `level` message from its
// structure as a string; nil if there are none. By text, as the Python engine did.
func ParsePeaks(text string) []float64 {
	m := rePeak.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	var vals []float64
	for _, p := range strings.Split(m[1], ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil
		}
		if math.IsNaN(v) || v < -90 { // Python's max(-90.0, v): NaN compares false, -90 stays
			v = -90
		}
		vals = append(vals, v)
	}
	return vals
}

// ParsePeak is the loudest channel's peak of a `level` message; ok=false if there is none.
func ParsePeak(text string) (float64, bool) {
	vals := ParsePeaks(text)
	if len(vals) == 0 {
		return 0, false
	}
	return slices.Max(vals), true
}

// pyRound1 is Python's round(x, 1): the exact binary value rounded half to even.
func pyRound1(x float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 1, 64), 64)
	return f
}

// number reads a decoded JSON number like Python's json would: an integer literal is an int, anything
// else a float. Bools and other types are not numbers. Numbers come as json.Number (the socket decodes
// with UseNumber); Go ints (tests) are ints and a float64 is a float.
func number(v any) (f float64, isInt, ok bool) {
	switch n := v.(type) {
	case json.Number:
		s := string(n)
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return float64(i), true, true
		}
		x, err := strconv.ParseFloat(s, 64)
		if err != nil && !math.IsInf(x, 0) {
			return 0, false, false
		}
		return x, !strings.ContainsAny(s, ".eE"), true
	case float64:
		return n, false, true
	case float32:
		return float64(n), false, true
	case int:
		return float64(n), true, true
	case int64:
		return float64(n), true, true
	case int32:
		return float64(n), true, true
	}
	return 0, false, false
}

// GainDB is a valid gain (a number, not a bool or NaN, MinGain…MaxGain dB) rounded to one decimal.
func GainDB(v any) (float64, bool) {
	f, _, ok := number(v)
	if !ok || math.IsNaN(f) || f < MinGain || f > MaxGain {
		return 0, false
	}
	return pyRound1(f), true
}

// DelayMs is a valid delay: an integer (not a bool, not a float) from 0 to MaxDelayMs.
func DelayMs(v any) (int, bool) {
	f, isInt, ok := number(v)
	if !ok || !isInt || f < 0 || f > MaxDelayMs {
		return 0, false
	}
	return int(f), true
}

// SlotID is an input in its slot (0-based).
type SlotID struct {
	Slot int
	ID   string
}

// TargetVolumes is the linear volume of each slot. single: main sounds; mix: those in the mix (by
// default only the one that was main). Each with its gain; silenced (the Pause with silence) puts all
// to 0.
func TargetVolumes(inputs []SlotID, mode, main string, mix map[string]bool, gains map[string]float64, silenced bool) map[int]float64 {
	out := make(map[int]float64, len(inputs))
	for _, in := range inputs {
		var on bool
		if !silenced {
			if mode == ModeMix {
				in2, set := mix[in.ID]
				on = in2 || (!set && in.ID == main)
			} else {
				on = in.ID == main
			}
		}
		if on {
			out[in.Slot] = math.Pow(10, gains[in.ID]/20)
		} else {
			out[in.Slot] = 0
		}
	}
	return out
}

// Blend is the volumes at fraction t (0…1, clamped) of a fade from a to b.
func Blend(a, b map[int]float64, t float64) map[int]float64 {
	t = max(0, min(1, t))
	out := make(map[int]float64, len(a)+len(b))
	for k := range a {
		out[k] = a[k] + (b[k]-a[k])*t
	}
	for k := range b {
		out[k] = a[k] + (b[k]-a[k])*t
	}
	return out
}

// Video is a video source with a slot, as the audio sees it.
type Video struct {
	ID   string
	Slot int    // 1-based
	Kind string // hdmi-csi | usb | network | test
	Card string // its ALSA card (USB), or its audio id (network: net:camN); "" if none
}

// View is what the audio needs of the video side: the sources with a slot, in slot order, and the one
// on air.
type View struct {
	Videos []Video
	Main   string
}

// DefaultLink is a video source's audio when nobody changed it: its own device's (the board's HDMI →
// the I2S; a USB with a sound card → that card; a network camera → its audio, same id). "" = no audio
// of its own.
func DefaultLink(kind, card string) string {
	switch {
	case kind == "hdmi-csi":
		return "i2s"
	case kind == "usb" && card != "" && ValidID("alsa:"+card):
		return "alsa:" + card
	case kind == "network" && card != "" && ValidID(card):
		return card
	}
	return ""
}

// ResolveLinks is {video id: audio id} for the video sources with a slot; "" = no audio. overrides is
// what was chosen by hand ({video id: audio id, "" = no audio of its own}); a key present overrides.
func ResolveLinks(videos []Video, overrides map[string]string) map[string]string {
	out := make(map[string]string, len(videos))
	for _, v := range videos {
		if a, ok := overrides[v.ID]; ok {
			out[v.ID] = a
		} else {
			out[v.ID] = DefaultLink(v.Kind, v.Card)
		}
	}
	return out
}

// SameDevice is {audio id: slot (1-based) of the video source of the same device}; the first wins.
func SameDevice(videos []Video) map[string]int {
	out := map[string]int{}
	for _, v := range videos {
		if a := DefaultLink(v.Kind, v.Card); a != "" {
			if _, ok := out[a]; !ok {
				out[a] = v.Slot
			}
		}
	}
	return out
}

// OrderInputs puts the inputs in the order of the video sources of their same device (the web shows
// them in the same row as the cameras), with that number (videoSlot); those of no camera, behind.
func OrderInputs(infos []InputState, same map[string]int) []InputState {
	out := slices.Clone(infos)
	key := func(d InputState) int {
		if s, ok := same[d.ID]; ok {
			return s
		}
		return 99
	}
	for i := range out {
		if s, ok := same[out[i].ID]; ok {
			out[i].VideoSlot = &s
		}
	}
	slices.SortStableFunc(out, func(a, b InputState) int {
		if ka, kb := key(a), key(b); ka != kb {
			return ka - kb
		}
		return a.Slot - b.Slot
	})
	return out
}

// FollowTarget is, with "the audio follows the video", what must sound when main goes on air; "" if
// that source has no audio of its own (then the one sounding keeps sounding).
func FollowTarget(links map[string]string, main string) string { return links[main] }
