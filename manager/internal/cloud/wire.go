package cloud

import (
	"bytes"
	"encoding/json"
	"net"
	"slices"
	"sort"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

// D9: when linked to the cloud, the device only streams to EnhancedIRL's ingest relays. The rule lives on
// the device: neither a compromised account nor a compromised backend can send the stream (with the key) to
// another server, not even to another name under enhancedirl.com. The relays the backend hands out (its
// relays table, the host of each zone) are <zone>.relay.enhancedirl.com in production (eu1, us1, as1) and
// relay.dev.enhancedirl.com in development; a new zone is one more DNS label in front of either, nothing else.
var relayZones = []string{".relay.enhancedirl.com", ".relay.dev.enhancedirl.com"}

// relayExact: relays that are a whole name, without a zone label (the development one).
var relayExact = []string{"relay.dev.enhancedirl.com"}

// ErrDestinationNotAllowed: the exact value the contract requires in config.applied.errors.destination.
const ErrDestinationNotAllowed = "destination_not_allowed"

// DestinationAllowed: a DNS host (not an IP) that is one of EnhancedIRL's relays: relayExact, or exactly one
// zone label (letters, digits and inner hyphens, at most 63) in front of a relayZones suffix. A trailing dot
// is not accepted (the URL parser keeps it).
func DestinationAllowed(host string) bool {
	h := strings.ToLower(host)
	if h == "" || net.ParseIP(h) != nil {
		return false
	}
	if slices.Contains(relayExact, h) {
		return true
	}
	for _, suf := range relayZones {
		if zone, ok := strings.CutSuffix(h, suf); ok && zoneLabel(zone) {
			return true
		}
	}
	return false
}

// zoneLabel: one DNS label of a relay zone (eu1, us1, as1…): 1-63 of [a-z0-9-], not starting or ending in "-".
func zoneLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for i := 0; i < len(l); i++ {
		if c := l[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
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
	// VideoCodec: h264 | h265, optional ("" = keep the cloud destination's, H.265 when it is created; OUTPUTS.md §10)
	VideoCodec string
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
			URL        *string `json:"url"`
			StreamID   *string `json:"streamId"`
			LatencyMs  *int    `json:"latencyMs"`
			VideoCodec *string `json:"videoCodec"`
		}
		if strict(v, &d) != nil {
			errs["destination"] = "invalid format"
		} else {
			need(errs, "destination.url", d.URL == nil)
			need(errs, "destination.streamId", d.StreamID == nil)
			need(errs, "destination.latencyMs", d.LatencyMs == nil)
			if d.VideoCodec != nil && !slices.Contains(config.Codecs, *d.VideoCodec) {
				errs["destination.videoCodec"] = "h264 or h265"
			}
			if d.URL != nil && d.StreamID != nil && d.LatencyMs != nil {
				w.Destination = &WireDestination{URL: *d.URL, StreamID: *d.StreamID, LatencyMs: *d.LatencyMs}
				if d.VideoCodec != nil {
					w.Destination.VideoCodec = *d.VideoCodec
				}
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
	"output": "output", "managedBy": "config", "videoCodec": "destination.videoCodec",
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
		s.URL, s.Mode, s.StreamID, s.Passphrase, s.Protocol = "", "caller", "", "", ""
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
			s.URL, s.Mode, s.Protocol = p.URL, "caller", config.ProtocolOf(p.URL)
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

// ApplyWireDestinations: a cloud configuration on the saved destinations (OUTPUTS.md §9.1, §10). The cloud manages
// only its own destination (eirl): ApplyWire validates the wire against it, flattened, with the local rules and D9;
// then it is created (and made active) the first time, updated later without changing which one is active, or
// removed with `destination: null` (if it was active, nothing is: not streaming, as before). Its bitrates and codec
// are the wire's. Output and the EDID stay global; with nothing active the output stays off. The result is the
// list and the settings with the active destination flattened (config.Destinations.Flatten).
func ApplyWireDestinations(ds config.Destinations, cur config.Settings, w WireConfig) (config.Destinations, config.Settings, []string, config.FieldErrors) {
	ds = ds.Clone()
	eirl, hasEIRL := ds.Get(config.CloudID)
	only := config.Empty()
	if hasEIRL {
		only.Destinations, only.Active = []config.Destination{eirl}, config.CloudID
	}
	next, disabled, fe := ApplyWire(only.Flatten(cur), w)
	if fe != nil {
		return ds, cur, nil, fe
	}
	switch {
	case w.KeepDestination:
	case w.Destination == nil:
		ds.Remove(config.CloudID)
	default:
		proto := config.ProtocolOf(next.URL)
		d := eirl
		if !hasEIRL {
			t, _ := config.TemplateByID(config.TemplateEIRL)
			d = config.NewDestination(t, proto)
			d.ID = config.CloudID
		}
		d.Protocol, d.URL, d.Mode, d.LatencyMs, d.StreamID, d.Passphrase = proto, next.URL, next.Mode, next.LatencyMs, next.StreamID, next.Passphrase
		if w.Destination.VideoCodec != "" {
			d.VideoCodec = w.Destination.VideoCodec
		}
		ds.Put(d)
		if !hasEIRL { // the first time it becomes the active one; later, the owner's choice stays
			ds.Active = config.CloudID
		}
	}
	if d, ok := ds.Get(config.CloudID); ok {
		d.VideoKbps, d.ABR, d.VideoMinKbps, d.AudioKbps = next.VideoKbps, next.ABR, next.VideoMinKbps, next.AudioKbps
		if err := d.Validate(); err != nil {
			return ds, cur, nil, wireErrors(err)
		}
		ds.Put(d)
	}
	s := cur
	s.Output, s.EDIDPreferred = next.Output, next.EDIDPreferred
	if w.Output == "on" || w.Output == "off" {
		s.Output = w.Output
	}
	if _, ok := ds.ActiveDestination(); !ok {
		// Nothing to stream to: rehearsal, as with null. The bitrates stay for when there is
		s.Output = "off"
		s.VideoKbps, s.ABR, s.VideoMinKbps, s.AudioKbps = next.VideoKbps, next.ABR, next.VideoMinKbps, next.AudioKbps
	}
	s = ds.Flatten(s)
	if err := s.Validate(); err != nil {
		return ds, cur, nil, wireErrors(err)
	}
	return ds, s, disabled, nil
}

// wireErrors: a local validation error with wire paths.
func wireErrors(err error) config.FieldErrors {
	fe, ok := err.(config.FieldErrors)
	if !ok {
		return config.FieldErrors{"config": err.Error()}
	}
	out := config.FieldErrors{}
	for k, v := range fe {
		key := wireField[k]
		if key == "" {
			key = k
		}
		out[key] = v
	}
	return out
}

// --- EncoderConfigReported (§5): what the device reports, without secrets ------------------------

type ReportedConfig struct {
	// Destination is the cloud's destination when it is the active one, in the shape the cloud already parses
	// (it refuses the whole report for a destination that is not srt/srtla with its host, port and latency);
	// null otherwise
	Destination *ReportedDestination `json:"destination"`
	// ActiveDestination is the active one, whichever it is: its kind, protocol and codec (OUTPUTS.md §10). New:
	// an older cloud ignores it
	ActiveDestination *ReportedActive `json:"activeDestination,omitempty"`
	Video             ReportedVideo   `json:"video"`
	Audio             ReportedAudio   `json:"audio"`
	Input             ReportedInput   `json:"input"`
	Output            string          `json:"output"`
	Uplinks           ReportedUplinks `json:"uplinks"`
	AccessPoint       string          `json:"accessPoint"`
}

// ReportedDestination: the ACTIVE destination. Of a local one, only its kind, protocol and codec (OUTPUTS.md §10);
// the cloud's also carries where it goes, without secrets.
type ReportedDestination struct {
	Kind        string `json:"kind"`     // eirl
	Protocol    string `json:"protocol"` // srt | srtla
	Codec       string `json:"codec"`    // h264 | h265
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Transport   string `json:"transport"` // srt | srtla
	LatencyMs   int    `json:"latencyMs"`
	StreamIDSet bool   `json:"streamIdSet"`
}

// ReportedActive is the active destination without where it goes: a local one is the user's business.
type ReportedActive struct {
	Kind     string `json:"kind"`     // eirl | local
	Protocol string `json:"protocol"` // srt | srtla | rtmp | rtmps | rist | udp
	Codec    string `json:"codec"`    // h264 | h265
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
// they are set). destination is the active one (Kind says whose); video and audio are the cloud destination's
// (what config.set sets) or, without it, the active one's. The client fills in accessPoint with what it stores.
func Reported(s config.Settings, ds config.Destinations, disabled []string) ReportedConfig {
	v := ReportedVideo{BitrateKbps: s.VideoKbps, Adaptive: s.ABR, MinBitrateKbps: s.VideoMinKbps}
	a := ReportedAudio{BitrateKbps: s.AudioKbps}
	if d, ok := ds.Get(config.CloudID); ok {
		v = ReportedVideo{BitrateKbps: d.VideoKbps, Adaptive: d.ABR, MinBitrateKbps: d.VideoMinKbps}
		a = ReportedAudio{BitrateKbps: d.AudioKbps}
	}
	r := ReportedConfig{
		Video:   v,
		Audio:   a,
		Input:   ReportedInput{EDIDPreferred: s.EDIDPreferred},
		Output:  s.Output,
		Uplinks: ReportedUplinks{Disabled: append([]string{}, disabled...)},
	}
	sort.Strings(r.Uplinks.Disabled)
	if d, ok := ds.ActiveDestination(); ok {
		r.ActiveDestination = &ReportedActive{Kind: "local", Protocol: d.Protocol, Codec: d.VideoCodec}
		if d.ID == config.CloudID {
			r.ActiveDestination.Kind = "eirl"
			if host, port, ok := config.SplitURL(d.URL); ok {
				r.Destination = &ReportedDestination{Kind: "eirl", Protocol: d.Protocol, Codec: d.VideoCodec, Host: host,
					Port: port, Transport: d.Protocol, LatencyMs: d.LatencyMs, StreamIDSet: d.StreamID != ""}
			}
		}
	}
	return r
}
