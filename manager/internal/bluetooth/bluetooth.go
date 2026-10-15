// Package bluetooth is the board's Bluetooth for audio (BLUETOOTH.md): BlueZ over D-Bus (the adapter, searching,
// pairing through our agent, connecting, forgetting), what each device is used for (listen, mic, input), and the file
// the engine reads to know which devices are ready for audio (internal/btaudio).
//
// Pairing in a public place (D4): the adapter is never visible by default. A pairing the app starts (search, then
// pair) is always allowed; one a device starts (a phone) only inside the visible window, two minutes opened from the
// screen or the web. Outside it the agent refuses and the adapter is not pairable. Paired devices are trusted, so they
// reconnect by themselves. Only audio devices are listed.
package bluetooth

import (
	"errors"
	"slices"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/btaudio"
)

var (
	ErrUnavailable = errors.New("bluetooth: unavailable")
	ErrOff         = errors.New("bluetooth: switched off")
	ErrUnknown     = errors.New("bluetooth: unknown device")
	ErrInvalid     = errors.New("bluetooth: invalid data")
	ErrBusy        = errors.New("bluetooth: another order is running")
	ErrNotPaired   = errors.New("bluetooth: the device is not paired")
)

// Uses of a device (D5): listen = the monitor plays through it (A2DP); mic = its microphone is an input and the
// monitor plays through its earpiece (both hands-free, phone quality); input = it sends its audio (a phone, a player).
const (
	UseListen = "listen"
	UseMic    = "mic"
	UseInput  = "input"
)

// Kinds, for the icon and the text
const (
	KindHeadphones = "headphones"
	KindHeadset    = "headset"
	KindSpeaker    = "speaker"
	KindPhone      = "phone"
	KindComputer   = "computer"
	KindAudio      = "audio"
)

// Why Bluetooth is not available (State.Reason)
const (
	ReasonNoStack   = "no_stack"   // bluetoothd is not running (or not installed: an image without BlueZ)
	ReasonNoAdapter = "no_adapter" // no adapter: no module on the M.2 card's USB lane, no dongle
	ReasonNoAudio   = "no_audio"   // BlueZ is there but BlueALSA is not: devices can be managed, not heard (available)
)

// Orders on a device
const (
	ActPair       = "pair"
	ActConnect    = "connect"
	ActDisconnect = "disconnect"
	ActForget     = "forget"
)

// Why the last order on a device failed (Device.Error)
const (
	FailRejected     = "rejected"      // the device (or its user) refused the pairing
	FailTimeout      = "timeout"       // it did not answer in time
	FailNotReachable = "not_reachable" // off, out of range, or not in pairing mode
	FailGone         = "gone"          // BlueZ forgot it meanwhile (a search that ended)
	FailOther        = "failed"
)

// Profile UUIDs (Bluetooth SIG 16-bit, in BlueZ's 128-bit form)
const (
	uuidA2DPSource = "0000110a-0000-1000-8000-00805f9b34fb" // the device sends: a phone, a player
	uuidA2DPSink   = "0000110b-0000-1000-8000-00805f9b34fb" // the device plays: headphones, a speaker
	uuidHeadset    = "00001108-0000-1000-8000-00805f9b34fb" // HSP, headset side
	uuidHandsfree  = "0000111e-0000-1000-8000-00805f9b34fb" // HFP, headset side
)

// Device is one device as the web and the screen see it.
type Device struct {
	Address   string   `json:"address"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Paired    bool     `json:"paired"`
	Connected bool     `json:"connected"`
	Ready     bool     `json:"ready"`         // its audio for its use is up (BlueALSA has its PCM)
	Use       string   `json:"use,omitempty"` // "" = none yet (not paired)
	Uses      []string `json:"uses"`          // what it can be used for, in the order offered
	RSSI      *int     `json:"rssi,omitempty"`
	Pending   string   `json:"pending,omitempty"` // an order running: pair | connect | disconnect | forget
	Error     string   `json:"error,omitempty"`   // why the last order failed (Fail*), until the next one
	// Output: the monitor plays through it right now (BLUETOOTH.md §3.3)
	Output bool `json:"output,omitempty"`
}

// State is GET /api/v1/bluetooth.
type State struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	On        bool   `json:"on"`      // what was asked (saved)
	Powered   bool   `json:"powered"` // how the adapter is
	// PowerFailed: the adapter refused to follow On (rfkill, firmware); retried every minute
	PowerFailed bool     `json:"powerFailed,omitempty"`
	Name        string   `json:"name,omitempty"`
	Visible     bool     `json:"visible"`
	VisibleLeft int      `json:"visibleLeft"` // seconds
	Scanning    bool     `json:"scanning"`
	Devices     []Device `json:"devices"`
}

// uses: what a device can be used for, from its profiles. Listening first: it is what headphones are for.
func uses(uuids []string) []string {
	out := []string{}
	has := func(u string) bool {
		return slices.ContainsFunc(uuids, func(x string) bool { return strings.EqualFold(x, u) })
	}
	if has(uuidA2DPSink) {
		out = append(out, UseListen)
	}
	if has(uuidHandsfree) || has(uuidHeadset) {
		out = append(out, UseMic)
	}
	if has(uuidA2DPSource) {
		out = append(out, UseInput)
	}
	return out
}

// defaultUse: what a newly paired device is used for (D5): listen if it can, then input (a phone), then mic.
func defaultUse(u []string) string {
	for _, w := range []string{UseListen, UseInput, UseMic} {
		if slices.Contains(u, w) {
			return w
		}
	}
	return ""
}

// Class of device: the major class (bits 8-12)
const (
	majorComputer = 1
	majorPhone    = 2
	majorAudio    = 4
)

func major(class uint32) uint32 { return class >> 8 & 0x1f }

// kindOf: what to call a device, from BlueZ's icon (which it derives from the class), else its profiles.
func kindOf(icon string, class uint32, uuids []string) string {
	switch icon {
	case "audio-headphones":
		return KindHeadphones
	case "audio-headset":
		return KindHeadset
	case "audio-card":
		return KindSpeaker
	case "phone":
		return KindPhone
	case "computer":
		return KindComputer
	}
	u := uses(uuids)
	switch {
	case major(class) == majorPhone:
		return KindPhone
	case slices.Contains(u, UseMic):
		return KindHeadset
	case slices.Contains(u, UseListen):
		return KindHeadphones
	case slices.Contains(u, UseInput):
		return KindPhone
	}
	return KindAudio
}

// isAudio: a device worth listing (D1: audio only). By its class (audio and video, a phone) or its profiles; a
// computer only if it sends audio.
func isAudio(icon string, class uint32, uuids []string) bool {
	if strings.HasPrefix(icon, "audio-") || icon == "phone" {
		return true
	}
	switch major(class) {
	case majorAudio, majorPhone:
		return true
	}
	return len(uses(uuids)) > 0
}

// cleanName: a device's name for the web, the screen and the logs (the engine's file rule).
func cleanName(s string) string { return btaudio.CleanName(s) }

// validUse: one of the three
func validUse(u string) bool { return u == UseListen || u == UseMic || u == UseInput }
