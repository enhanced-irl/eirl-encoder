package cloud

import (
	"bytes"
	"encoding/json"
	"net"
	"sort"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

// AllowedDestinationSuffixes (D9): when linked to the cloud, the device only streams to hosts ending
// in one of these suffixes. The list lives on the device: neither a compromised account nor a
// compromised backend can send the stream (with the key) to another server.
var AllowedDestinationSuffixes = []string{".enhancedirl.com"}

// ErrDestinationNotAllowed: the exact value the contract requires in config.applied.errors.destination.
const ErrDestinationNotAllowed = "destination_not_allowed"

// DestinationAllowed: a DNS host (not an IP) ending in an allowed suffix.
func DestinationAllowed(host string) bool {
	h := strings.ToLower(host)
	if h == "" || net.ParseIP(h) != nil {
		return false
	}
	for _, suf := range AllowedDestinationSuffixes {
		if strings.HasSuffix(h, suf) && len(h) > len(suf) {
			return true
		}
	}
	return false
}

// Access point (D2): rescue (default) | always. Phase 3 uses it; here it is only validated and stored.
const (
	AccessPointRescue = "rescue"
	AccessPointAlways = "always"
)

// --- EncoderConfigWire (§5): what the cloud sends in config.set -----------------------------------

type WireConfig struct {
	// Destination: nil = remove the destination (the cloud manages it and the key is no longer valid),
	// unless KeepDestination says the field was absent: then the device's is left alone (no key chosen
	// in the app yet; ENCODER_CLOUD.md §5, G1 finding).
	Destination     *WireDestination
	KeepDestination bool
	Video           WireVideo
	Audio           WireAudio
	Input           WireInput
	Output          string
	Uplinks         WireUplinks
	AccessPoint     string // "" = absent (the current one is kept)
}

type WireDestination struct {
	URL       string // srt://host:port or srtla://host:port (accepts the publish URL's query)
	StreamID  string
	LatencyMs int
}

type WireVideo struct {
	BitrateKbps    int
	Adaptive       bool
	MinBitrateKbps int
}

type WireAudio struct{ BitrateKbps int }

type WireInput struct{ EDIDPreferred string }

type WireUplinks struct{ Disabled []string }

// ParseWireConfig decodes config.set.config against an allowlist: unknown or missing fields → an
// error per field (wire paths: "video.bitrateKbps"…). Nothing gets a default except accessPoint
// (phase 3, optional).
func ParseWireConfig(raw json.RawMessage) (WireConfig, config.FieldErrors) {
	var w WireConfig
	errs := config.FieldErrors{}
	var top map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &top) != nil || top == nil {
		return w, config.FieldErrors{"config": "not a JSON object"}
	}
	for k := range top {
		switch k {
		case "destination", "video", "audio", "input", "output", "uplinks", "accessPoint":
		default:
			errs[k] = "unknown field"
		}
	}
	section := func(name string, dst any) bool {
		v, ok := top[name]
		if !ok {
			errs[name] = "missing"
			return false
		}
		if err := strict(v, dst); err != nil {
			errs[name] = "invalid format"
			return false
		}
		return true
	}

	if v, ok := top["destination"]; !ok {
		w.KeepDestination = true // absent: the device's destination is left alone
	} else if !isNull(v) {
		var d struct {
			URL       *string `json:"url"`
			StreamID  *string `json:"streamId"`
			LatencyMs *int    `json:"latencyMs"`
		}
		if strict(v, &d) != nil {
			errs["destination"] = "invalid format"
		} else {
			need(errs, "destination.url", d.URL == nil)
			need(errs, "destination.streamId", d.StreamID == nil)
			need(errs, "destination.latencyMs", d.LatencyMs == nil)
			if d.URL != nil && d.StreamID != nil && d.LatencyMs != nil {
				w.Destination = &WireDestination{URL: *d.URL, StreamID: *d.StreamID, LatencyMs: *d.LatencyMs}
			}
		}
	}
	var video struct {
		BitrateKbps    *int  `json:"bitrateKbps"`
		Adaptive       *bool `json:"adaptive"`
		MinBitrateKbps *int  `json:"minBitrateKbps"`
	}
	if section("video", &video) {
		need(errs, "video.bitrateKbps", video.BitrateKbps == nil)
		need(errs, "video.adaptive", video.Adaptive == nil)
		need(errs, "video.minBitrateKbps", video.MinBitrateKbps == nil)
		if video.BitrateKbps != nil && video.Adaptive != nil && video.MinBitrateKbps != nil {
			w.Video = WireVideo{BitrateKbps: *video.BitrateKbps, Adaptive: *video.Adaptive, MinBitrateKbps: *video.MinBitrateKbps}
		}
	}
	var audio struct {
		BitrateKbps *int `json:"bitrateKbps"`
	}
	if section("audio", &audio) {
		need(errs, "audio.bitrateKbps", audio.BitrateKbps == nil)
		if audio.BitrateKbps != nil {
			w.Audio.BitrateKbps = *audio.BitrateKbps
		}
	}
	var input struct {
		EDIDPreferred *string `json:"edidPreferred"`
	}
	if section("input", &input) {
		need(errs, "input.edidPreferred", input.EDIDPreferred == nil)
		if input.EDIDPreferred != nil {
			w.Input.EDIDPreferred = *input.EDIDPreferred
		}
	}
	section("output", &w.Output)
	var up struct {
		Disabled *[]string `json:"disabled"`
	}
	if section("uplinks", &up) {
		need(errs, "uplinks.disabled", up.Disabled == nil)
		if up.Disabled != nil {
			w.Uplinks.Disabled = *up.Disabled
		}
	}
	if v, ok := top["accessPoint"]; ok {
		if strict(v, &w.AccessPoint) != nil || (w.AccessPoint != AccessPointRescue && w.AccessPoint != AccessPointAlways) {
			errs["accessPoint"] = "rescue or always"
		}
	}
	if len(errs) > 0 {
		return WireConfig{}, errs
	}
	return w, nil
}

func strict(raw json.RawMessage, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func need(errs config.FieldErrors, field string, missing bool) {
	if missing {
		errs[field] = "missing"
	}
}

// wireField: local validation field (config.Settings.Validate) → wire path.
var wireField = map[string]string{
	"url": "destination.url", "mode": "destination.url", "passphrase": "destination.url",
	"latencyMs": "destination.latencyMs", "streamId": "destination.streamId",
	"videoBitrateKbps": "video.bitrateKbps", "videoMinBitrateKbps": "video.minBitrateKbps",
	"audioBitrateKbps": "audio.bitrateKbps", "edidPreferred": "input.edidPreferred",
	"output": "output", "managedBy": "config",
}

// ApplyWire turns a cloud configuration into the device's, starting from the current one (what the
// cloud does not manage, like the source mode, is left alone) and validating with the SAME rules as
// the local API (config.Settings.Validate) plus D9. It also returns the normalised list of disabled
// uplinks. Errors carry wire paths.
func ApplyWire(cur config.Settings, w WireConfig) (config.Settings, []string, config.FieldErrors) {
	s := cur
	s.ManagedBy = "eirl"
	errs := config.FieldErrors{}
	s.Output = w.Output
	if w.KeepDestination {
		// No `destination` on the wire: the device's destination is kept (URL, key, passphrase and
		// latency). If it has none and streaming is requested, it stays in rehearsal, as with null.
		if s.URL == "" {
			s.Output = "off"
		}
	} else if w.Destination == nil {
		// No destination (key revoked, switched to RTMP or not chosen yet): URL, key and passphrase
		// are removed and, if streaming was requested, it stays in REHEARSAL while there is no
		// destination. Rejecting it would leave the device streaming with the revoked key. Reported
		// as such (output "off", destination null) in config.applied, hello and status.
		s.URL, s.Mode, s.StreamID, s.Passphrase = "", "caller", "", ""
		s.Output = "off"
	} else {
		d := w.Destination
		p, err := config.ParseURL(d.URL)
		switch {
		case err != nil:
			errs["destination.url"] = err.Error()
		case !DestinationAllowed(config.HostOf(p.URL)):
			errs["destination"] = ErrDestinationNotAllowed
		default:
			s.URL, s.Mode = p.URL, "caller"
			if p.Mode != "" {
				s.Mode = p.Mode
			}
			// The key goes in streamId; if the publish URL also carries ?streamid=, they must match
			// (otherwise it is unclear which one wins: it is rejected, not chosen)
			switch {
			case d.StreamID != "" && p.StreamID != "" && d.StreamID != p.StreamID:
				errs["destination.streamId"] = "does not match the URL's streamid"
			case d.StreamID != "":
				s.StreamID = d.StreamID
			default:
				s.StreamID = p.StreamID
			}
			s.Passphrase = p.Passphrase
			s.LatencyMs = d.LatencyMs
		}
	}
	s.VideoKbps, s.ABR, s.VideoMinKbps = w.Video.BitrateKbps, w.Video.Adaptive, w.Video.MinBitrateKbps
	s.AudioKbps = w.Audio.BitrateKbps
	s.EDIDPreferred = w.Input.EDIDPreferred
	if w.Output != "on" && w.Output != "off" {
		s.Output = w.Output // so that Validate rejects it even without a destination
	}
	disabled, err := uplinks.Normalize(w.Uplinks.Disabled)
	if err != nil {
		errs["uplinks.disabled"] = "invalid interface names or more than 16"
	}
	if len(errs) == 0 { // with the destination rejected, Validate would repeat the error under another name
		if err := s.Validate(); err != nil {
			if fe, ok := err.(config.FieldErrors); ok {
				for k, v := range fe {
					key := wireField[k]
					if key == "" {
						key = k
					}
					if _, dup := errs[key]; !dup {
						errs[key] = v
					}
				}
			} else {
				errs["config"] = err.Error()
			}
		}
	}
	if len(errs) > 0 {
		return cur, nil, errs
	}
	return s, disabled, nil
}

// --- EncoderConfigReported (§5): what the device reports, without secrets ------------------------

type ReportedConfig struct {
	Destination *ReportedDestination `json:"destination"`
	Video       ReportedVideo        `json:"video"`
	Audio       ReportedAudio        `json:"audio"`
	Input       ReportedInput        `json:"input"`
	Output      string               `json:"output"`
	Uplinks     ReportedUplinks      `json:"uplinks"`
	AccessPoint string               `json:"accessPoint"`
}

type ReportedDestination struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Transport   string `json:"transport"` // srt | srtla
	LatencyMs   int    `json:"latencyMs"`
	StreamIDSet bool   `json:"streamIdSet"`
}

type ReportedVideo struct {
	BitrateKbps    int  `json:"bitrateKbps"`
	Adaptive       bool `json:"adaptive"`
	MinBitrateKbps int  `json:"minBitrateKbps"`
}

type ReportedAudio struct {
	BitrateKbps int `json:"bitrateKbps"`
}

type ReportedInput struct {
	EDIDPreferred string `json:"edidPreferred"`
}

type ReportedUplinks struct {
	Disabled []string `json:"disabled"`
}

// Reported: the effective configuration without secrets (neither key nor passphrase: only whether
// they are set). The client fills in accessPoint with what it stores.
func Reported(s config.Settings, disabled []string) ReportedConfig {
	r := ReportedConfig{
		Video:   ReportedVideo{BitrateKbps: s.VideoKbps, Adaptive: s.ABR, MinBitrateKbps: s.VideoMinKbps},
		Audio:   ReportedAudio{BitrateKbps: s.AudioKbps},
		Input:   ReportedInput{EDIDPreferred: s.EDIDPreferred},
		Output:  s.Output,
		Uplinks: ReportedUplinks{Disabled: append([]string{}, disabled...)},
	}
	sort.Strings(r.Uplinks.Disabled)
	if host, port, ok := config.SplitURL(s.URL); ok {
		r.Destination = &ReportedDestination{
			Host: host, Port: port, Transport: config.Transport(s.URL), LatencyMs: s.LatencyMs,
			StreamIDSet: s.StreamID != "",
		}
	}
	return r
}
