package config

import (
	"bytes"
	"encoding/json"
)

// Optional tells apart, in JSON, a missing field (leave it), null (clear it) and a value (change it).
// Used for the secrets (streamId, passphrase, key, password): they do not have to be typed again to change the
// latency.
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

// Public is the API view of engine.env's destination (GET /destination): the secrets only as "set" and masked.
type Public struct {
	ManagedBy  string `json:"managedBy"`
	URL        string `json:"url"`
	URLInvalid bool   `json:"urlInvalid,omitempty"` // saved by hand in a format not allowed: not shown
	Mode       string `json:"mode"`
	LatencyMs  int    `json:"latencyMs"` // SRT's latency, or RIST's buffer
	StreamID   Secret `json:"streamId"`
	Passphrase Secret `json:"passphrase"` // SRT's passphrase, or RIST's pre-shared key
	VideoKbps  int    `json:"videoBitrateKbps"`
	AudioKbps  int    `json:"audioBitrateKbps"`
	Output     string `json:"output"`
	Transport  string `json:"transport"` // the SAVED protocol (srt when there is no destination)
	ABR        bool   `json:"adaptiveBitrate"`
	VideoMin   int    `json:"videoMinBitrateKbps"`
	EDID       string `json:"edidPreferred"`
	// Output protocols and encoding settings (OUTPUTS.md §9.3); protocol "" = no destination
	Protocol    string `json:"protocol"`
	Key         Secret `json:"key"`
	RISTProfile string `json:"ristProfile"`
	Username    string `json:"username"`
	Password    Secret `json:"password"`
	VideoCodec  string `json:"videoCodec"`
	OutputSize  string `json:"outputSize"`
	OutputFPS   string `json:"outputFps"`
}

type Secret struct {
	Set    bool   `json:"set"`
	Masked string `json:"masked,omitempty"`
}

func (s Settings) Public() Public {
	p := s.EffectiveProtocol()
	url, ok := SafeURL(s.URL)
	latency, passphrase := s.LatencyMs, s.Passphrase
	if !usesSRT(p) && p != "" {
		url, ok = safeOutputURL(s.OutputURL)
	}
	switch {
	case p == ProtoRIST:
		latency, passphrase = s.OutputLatencyMs, s.OutputPassphrase
		if latency == 0 {
			latency = RISTLatencyDefault
		}
	case p != "" && !usesLatency(p):
		latency = 0 // like the saved destination: rtmp(s) and udp have none
	}
	transport := p
	if transport == "" {
		transport = ProtoSRT
	}
	fps := s.OutputFPS
	if fps == "" {
		fps = "full"
	}
	return Public{
		ManagedBy: s.ManagedBy, URL: url, URLInvalid: !ok, Mode: s.Mode, LatencyMs: latency,
		StreamID:   Secret{Set: s.StreamID != "", Masked: MaskStreamID(s.StreamID)},
		Passphrase: Secret{Set: passphrase != ""},
		VideoKbps:  s.VideoKbps, AudioKbps: s.AudioKbps, Output: s.Output, EDID: s.EDIDPreferred,
		Transport: transport, ABR: s.ABR, VideoMin: s.VideoMinKbps,
		Protocol: p, Key: Secret{Set: s.Key != "", Masked: MaskKey(s.Key)}, RISTProfile: s.RISTProfile,
		Username: s.OutputUser, Password: Secret{Set: s.OutputPassword != ""}, VideoCodec: s.Codec(),
		OutputSize: s.OutputSize, OutputFPS: fps,
	}
}

// safeOutputURL: like SafeURL for the other protocols' URLs (OUTPUT_URL).
func safeOutputURL(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if _, err := checkOutputURL(raw); err != nil {
		return "", false
	}
	return raw, true
}

// MaskKey shows the ends of a stream key: live…wXyZ (all dots if it is short).
func MaskKey(k string) string {
	switch {
	case k == "":
		return ""
	case len(k) < 12:
		return "••••"
	}
	return k[:4] + "…" + k[len(k)-4:]
}

// DestinationView: the public view of a saved destination (GET /destinations): the secrets only as "set" and
// masked, like the stream ID of GET /destination.
type DestinationView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Template    string `json:"template"`
	Managed     bool   `json:"managed"`
	Active      bool   `json:"active"`
	Protocol    string `json:"protocol"`
	URL         string `json:"url"`
	URLInvalid  bool   `json:"urlInvalid"`
	Mode        string `json:"mode"`
	LatencyMs   int    `json:"latencyMs"`
	StreamID    Secret `json:"streamId"`
	Passphrase  Secret `json:"passphrase"`
	Key         Secret `json:"key"`
	RISTProfile string `json:"ristProfile"`
	Username    string `json:"username"`
	Password    Secret `json:"password"`
	VideoCodec  string `json:"videoCodec"`
	VideoKbps   int    `json:"videoBitrateKbps"`
	VideoMin    int    `json:"videoMinBitrateKbps"`
	ABR         bool   `json:"adaptiveBitrate"`
	AudioKbps   int    `json:"audioBitrateKbps"`
	OutputSize  string `json:"outputSize"`
	OutputFPS   string `json:"outputFps"`
}

// View: the public view; active says whether it is the active one.
func (d Destination) View(active bool) DestinationView {
	url, ok := SafeURL(d.URL)
	if !usesSRT(d.Protocol) {
		url, ok = safeOutputURL(d.URL)
	}
	return DestinationView{ID: d.ID, Name: d.Name, Template: d.Template, Managed: d.Managed, Active: active,
		Protocol: d.Protocol, URL: url, URLInvalid: !ok, Mode: d.Mode, LatencyMs: d.LatencyMs,
		StreamID:   Secret{Set: d.StreamID != "", Masked: MaskStreamID(d.StreamID)},
		Passphrase: Secret{Set: d.Passphrase != ""}, Key: Secret{Set: d.Key != "", Masked: MaskKey(d.Key)},
		RISTProfile: d.RISTProfile, Username: d.Username, Password: Secret{Set: d.Password != ""},
		VideoCodec: d.VideoCodec, VideoKbps: d.VideoKbps, VideoMin: d.VideoMinKbps, ABR: d.ABR, AudioKbps: d.AudioKbps,
		OutputSize: d.OutputSize, OutputFPS: d.OutputFPS}
}

// Secrets: the four secrets in clear ("" when unset), for the screen's eye (local socket only).
type Secrets struct {
	StreamID   string `json:"streamId"`
	Passphrase string `json:"passphrase"`
	Key        string `json:"key"`
	Password   string `json:"password"`
}

func (d Destination) Secrets() Secrets {
	return Secrets{StreamID: d.StreamID, Passphrase: d.Passphrase, Key: d.Key, Password: d.Password}
}
