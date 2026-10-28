package config

import "regexp"

// Local recording (RECORDING.md §5.1): REC_MODE and REC_QUALITY come from the app's file; REC_DIR and
// REC_STORAGE_STATE are board settings, never the app's (no key the app writes is a path).
const (
	RecOff        = "off"
	RecManual     = "manual"      // the REC button, with or without the stream
	RecWithOutput = "with_output" // Go live also records, Stop stops a recording Go live started
)

var (
	reRecMode    = regexp.MustCompile(`^(off|manual|with_output)$`)
	reRecQuality = regexp.MustCompile(`^(standard|high|max|efficient)$`)
)

// RecQuality is what a recording quality encodes (D2): the program's size and rate, always.
type RecQuality struct {
	Codec string // h264 | h265
	VKbps int
	AKbps int
}

var recQualities = map[string]RecQuality{
	"standard":  {"h264", 15000, 192},
	"high":      {"h264", 25000, 256},
	"max":       {"h264", 40000, 256},
	"efficient": {"h265", 20000, 256},
}

// Recording is the encoding of REC_QUALITY (high when it is unknown: the config already refused it).
func (c *Config) Recording() RecQuality {
	if q, ok := recQualities[c.RecQuality]; ok {
		return q
	}
	return recQualities["high"]
}

// RecordingEnabled: REC_MODE is not off.
func (c *Config) RecordingEnabled() bool { return c.RecMode != "" && c.RecMode != RecOff }

func validateRecording(raw map[string]string) error {
	switch {
	case !reRecMode.MatchString(raw["REC_MODE"]):
		return invalid("REC_MODE", "off, manual or with_output")
	case !reRecQuality.MatchString(raw["REC_QUALITY"]):
		return invalid("REC_QUALITY", "standard, high, max or efficient")
	case !rePath.MatchString(raw["REC_DIR"]):
		return invalid("REC_DIR", "")
	case !rePath.MatchString(raw["REC_STORAGE_STATE"]):
		return invalid("REC_STORAGE_STATE", "")
	}
	return nil
}
