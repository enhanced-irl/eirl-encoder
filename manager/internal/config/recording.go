package config

import (
	"slices"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// Local recording to a USB disk (RECORDING.md §5.1, §6.1): REC_MODE and REC_QUALITY are app settings in engine.env,
// with the same closed lists the engine checks (internal/engine/config/reccfg.go). The engine takes them live with
// output_config and leaves the stream alone; whether it is recording is never saved (D6).

// Recording modes.
const (
	RecOff        = "off"
	RecManual     = "manual"      // the REC button, with or without the stream
	RecWithOutput = "with_output" // Go live also records, Stop stops what Go live started
)

// RecModes in the order the API lists them.
var RecModes = []string{RecOff, RecManual, RecWithOutput}

// RecQuality: what a recording quality encodes (RECORDING.md D2), always at the program's size and rate.
type RecQuality struct {
	ID        string `json:"id"`
	Codec     string `json:"codec"` // h264 | h265
	VideoKbps int    `json:"videoKbps"`
	AudioKbps int    `json:"audioKbps"`
}

// RecQualities: the qualities, as the engine encodes them (reccfg.go), in the order the API lists them.
var RecQualities = []RecQuality{
	{"standard", "h264", 15000, 192},
	{"high", "h264", 25000, 256},
	{"max", "h264", 40000, 256},
	{"efficient", "h265", 20000, 256},
}

// DefaultRecQuality: REC_QUALITY when it is not set.
const DefaultRecQuality = "high"

// ValidRecMode and ValidRecQuality: the closed lists.
func ValidRecMode(m string) bool { return slices.Contains(RecModes, m) }

func ValidRecQuality(q string) bool {
	return slices.ContainsFunc(RecQualities, func(r RecQuality) bool { return r.ID == q })
}

// RecQualityIDs: the qualities' ids, for a message.
func RecQualityIDs() string {
	ids := make([]string, len(RecQualities))
	for i, q := range RecQualities {
		ids[i] = q.ID
	}
	return strings.Join(ids, ", ")
}

// validateRecording: REC_MODE and REC_QUALITY. Field names are PUT /recording's.
func (s Settings) validateRecording(errs FieldErrors) {
	if !ValidRecMode(s.RecMode) {
		errs["recordingMode"] = i18n.Ref("config.one_of", strings.Join(RecModes, ", "))
	}
	if !ValidRecQuality(s.RecQuality) {
		errs["recordingQuality"] = i18n.Ref("config.one_of", RecQualityIDs())
	}
}
