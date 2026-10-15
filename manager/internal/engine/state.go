package engine

import (
	"math"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/audio"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/camera"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/scene"
)

// The state the engine publishes in mixer.json and to subscribers (MIXER.md §5). The fields and their
// order follow the Python engine's _publish; the app's client (manager/internal/mixer) is the reader,
// and a test decodes this through it.

// State is the whole state.
type State struct {
	V       int          `json:"v"`
	Program ProgramState `json:"program"`
	Slots   int          `json:"slots"`
	// SlotConfig is every slot as the user arranged it (slots.go); a reserved slot without its source
	// is only here, not in Sources
	SlotConfig []SlotState `json:"slotConfig"`
	// Catalog is every source connected, also those without a slot (the app's slot editor)
	Catalog []CatalogEntry `json:"catalog"`
	Sources []SourceState  `json:"sources"`
	Screen  ScreenState    `json:"screen"`
	Output  OutputState    `json:"output"`
	Preview *PreviewState  `json:"preview,omitempty"`
	// Audio: without an audio bus the key is absent, like the Python engine with MIXER_AUDIO_SLOTS=0
	Audio *audio.State `json:"audio,omitempty"`
}

// ProgramState is the scene plus what the engine knows of the program.
type ProgramState struct {
	scene.Snapshot
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	FPS         int      `json:"fps"`
	MeasuredFPS *float64 `json:"measuredFps"`
	NewFPS      *float64 `json:"newFps"`
}

// SourceState is one occupied slot.
type SourceState struct {
	ID     string `json:"id"`
	Slot   int    `json:"slot"` // 1-based
	Kind   string `json:"kind"` // hdmi-csi | usb | network | test
	Name   string `json:"name"`
	Alias  string `json:"alias"`  // the slot's alias, "" = none (the name is the device's)
	State  string `json:"state"`  // starting | live | no_signal | unsupported | gone | error | hidden
	Detail string `json:"detail"` // Spanish, as always; the interfaces translate DetailCode
	// DetailCode is the stable code of Detail (internal/engine/detail: a closed list), "" when there is
	// nothing to say; DetailArgs its arguments, absent without any
	DetailCode string            `json:"detailCode"`
	DetailArgs map[string]string `json:"detailArgs,omitempty"`
	Format     string            `json:"format"`
	Mode       string            `json:"mode"`
	FPS        float64           `json:"fps"`
	// Controls: a USB camera's image settings (SOURCES.md §7.3); absent without any
	Controls []camera.Control `json:"controls,omitempty"`
}

// SlotState is a slot's configuration and whether a source is in it now.
type SlotState struct {
	SlotConfig
	Present bool `json:"present"`
}

// CatalogEntry is a connected source, with or without a slot.
type CatalogEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// ScreenState is the touch screen. Without a screen: off, sizes null.
type ScreenState struct {
	State  string        `json:"state"` // off | waiting | on | error
	Width  *int          `json:"width"`
	Height *int          `json:"height"`
	Touch  bool          `json:"touch"`
	Locked bool          `json:"locked"`
	Power  *PowerRequest `json:"power,omitempty"`
}

// PowerRequest is a reboot/poweroff asked for on the screen; the app does it.
type PowerRequest struct {
	Want string  `json:"want"` // reboot | poweroff
	Seq  int     `json:"seq"`
	At   float64 `json:"at"` // epoch s
}

// OutputState is the hot output (F3). Until the output phase (M2) the engine has no hot output.
type OutputState struct {
	Mode     string   `json:"mode"`  // on | off
	State    string   `json:"state"` // off | connecting | on | error
	Reason   string   `json:"reason,omitempty"`
	Detail   string   `json:"detail,omitempty"`
	Kbps     *int     `json:"kbps,omitempty"`
	InFPS    *float64 `json:"inFps,omitempty"`
	EncFPS   *float64 `json:"encFps,omitempty"`
	EncMbps  *float64 `json:"encMbps,omitempty"`
	SentKbps *int     `json:"sentKbps,omitempty"`
	RTTMs    *int     `json:"rttMs,omitempty"`
	By       string   `json:"by"` // start | socket | screen
	Seq      int      `json:"seq"`
	// OUTPUTS.md §8: the protocol and codec (also while off), the output's size and rate while on
	Protocol string `json:"protocol,omitempty"` // srt | srtla | rtmp | rtmps | rist | udp
	Codec    string `json:"codec,omitempty"`    // h264 | h265
	Size     string `json:"size,omitempty"`     // "1280x720"
	FPS      *int   `json:"fps,omitempty"`
}

// PreviewState is the web's WebRTC preview (present with LIVE=1).
type PreviewState struct {
	On bool `json:"on"`
}

// Source states and their log names.
const (
	StateStarting    = "starting"
	StateLive        = "live"
	StateNoSignal    = "no_signal"
	StateUnsupported = "unsupported"
	StateGone        = "gone"
	StateError       = "error"
	StateHidden      = "hidden"
)

// round1 rounds a measured rate to one decimal for the state (Python's round(x, 1); ties may differ,
// which is harmless for a measurement).
func round1(x float64) float64 { return math.Round(x*10) / 10 }
