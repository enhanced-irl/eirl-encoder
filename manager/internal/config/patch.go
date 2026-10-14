package config

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// Optional tells apart, in JSON, a missing field (leave it), null (clear it) and a value (change it).
// Used for streamId and passphrase: the key does not have to be typed again to change the latency.
type Optional struct {
	Set   bool
	Null  bool
	Value string
}

func (o *Optional) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// Patch is the body of PUT /api/v1/destination.
type Patch struct {
	URL        *string  `json:"url"`
	Mode       *string  `json:"mode"`
	LatencyMs  *int     `json:"latencyMs"`
	StreamID   Optional `json:"streamId"`
	Passphrase Optional `json:"passphrase"`
	VideoKbps  *int     `json:"videoBitrateKbps"`
	AudioKbps  *int     `json:"audioBitrateKbps"`
	ABR        *bool    `json:"adaptiveBitrate"`
	VideoMin   *int     `json:"videoMinBitrateKbps"`
	Apply      bool     `json:"apply"`
}

// ApplyTo applies the patch on the current configuration and validates the result. What the query of
// a pasted URL brings (streamid, passphrase, latency, mode) wins over the separate fields.
func (p Patch) ApplyTo(cur Settings) (Settings, error) {
	s := cur
	if p.Mode != nil {
		s.Mode = *p.Mode
	}
	if p.LatencyMs != nil {
		s.LatencyMs = *p.LatencyMs
	}
	opt := func(o Optional, dst *string) {
		if o.Null {
			*dst = ""
		} else if o.Set {
			*dst = o.Value
		}
	}
	opt(p.StreamID, &s.StreamID)
	opt(p.Passphrase, &s.Passphrase)
	if p.VideoKbps != nil {
		s.VideoKbps = *p.VideoKbps
	}
	if p.AudioKbps != nil {
		s.AudioKbps = *p.AudioKbps
	}
	if p.ABR != nil {
		s.ABR = *p.ABR
	}
	if p.VideoMin != nil {
		s.VideoMinKbps = *p.VideoMin
	}
	if p.URL != nil {
		if *p.URL == "" {
			s.URL = ""
		} else {
			u, err := ParseURL(*p.URL)
			if err != nil {
				return cur, FieldErrors{"url": i18n.RefOf(err)}
			}
			s.URL = u.URL
			// The mode comes from the URL (with a host: caller to the relay; without: listener) unless
			// the query says otherwise. So pasting a key's URL while in listener mode does not fail.
			switch {
			case u.Mode != "":
				s.Mode = u.Mode
			case strings.HasPrefix(u.URL, "srt://:"):
				s.Mode = "listener"
			default:
				s.Mode = "caller"
			}
			switch {
			case u.LatencyMs != 0:
				s.LatencyMs = u.LatencyMs
			// Switching to SRTLA (the panel's URL has no latency), the LAN SRT latency falls short:
			// it goes up to 2000 ms unless this same request set another one.
			case Transport(u.URL) == "srtla" && Transport(cur.URL) != "srtla" &&
				(p.LatencyMs == nil || *p.LatencyMs == cur.LatencyMs) && s.LatencyMs < SRTLALatencyMs:
				s.LatencyMs = SRTLALatencyMs
			}
			if u.StreamID != "" {
				s.StreamID = u.StreamID
			}
			if u.Passphrase != "" {
				s.Passphrase = u.Passphrase
			}
		}
	}
	// Changing the server requires giving the secrets again (or removing them): otherwise a stolen
	// session could send the saved key to its own server by changing only the URL. Switching to
	// listener does not expose it (the caller sets the streamid).
	if newHost := HostOf(s.URL); newHost != "" && newHost != HostOf(cur.URL) {
		errs := FieldErrors{}
		if cur.StreamID != "" && s.StreamID == cur.StreamID && !p.StreamID.Set {
			errs["streamId"] = i18n.Ref("config.new_server_streamid")
		}
		if cur.Passphrase != "" && s.Passphrase == cur.Passphrase && !p.Passphrase.Set {
			errs["passphrase"] = i18n.Ref("config.new_server_passphrase")
		}
		if len(errs) > 0 {
			return cur, errs
		}
	}
	if err := s.Validate(); err != nil {
		return cur, err
	}
	return s, nil
}

// Public is the API view: the secrets only as "set" and masked.
type Public struct {
	ManagedBy  string `json:"managedBy"`
	URL        string `json:"url"`
	URLInvalid bool   `json:"urlInvalid,omitempty"` // saved by hand in a format not allowed: not shown
	Mode       string `json:"mode"`
	LatencyMs  int    `json:"latencyMs"`
	StreamID   Secret `json:"streamId"`
	Passphrase Secret `json:"passphrase"`
	VideoKbps  int    `json:"videoBitrateKbps"`
	AudioKbps  int    `json:"audioBitrateKbps"`
	Output     string `json:"output"`
	Transport  string `json:"transport"` // srt | srtla (the SAVED one)
	ABR        bool   `json:"adaptiveBitrate"`
	VideoMin   int    `json:"videoMinBitrateKbps"`
	EDID       string `json:"edidPreferred"`
}

type Secret struct {
	Set    bool   `json:"set"`
	Masked string `json:"masked,omitempty"`
}

func (s Settings) Public() Public {
	url, ok := SafeURL(s.URL)
	return Public{
		ManagedBy: s.ManagedBy, URL: url, URLInvalid: !ok, Mode: s.Mode, LatencyMs: s.LatencyMs,
		StreamID:   Secret{Set: s.StreamID != "", Masked: MaskStreamID(s.StreamID)},
		Passphrase: Secret{Set: s.Passphrase != ""},
		VideoKbps:  s.VideoKbps, AudioKbps: s.AudioKbps, Output: s.Output, EDID: s.EDIDPreferred,
		Transport: Transport(s.URL), ABR: s.ABR, VideoMin: s.VideoMinKbps,
	}
}
