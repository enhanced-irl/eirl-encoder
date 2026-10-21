package config

import "slices"

// Template: what a saved destination starts from and what it can be (OUTPUTS.md §9.2). The API serves them
// (GET /destinations); the web and the screen translate the name by its id.
type Template struct {
	ID        string   `json:"id"`
	Protocols []string `json:"protocols"` // the first one is the default
	// URL: the server of the default protocol ("" = the user gives it); URLs: the server per protocol
	URL  string            `json:"url"`
	URLs map[string]string `json:"urls,omitempty"`
	// Codecs: the ones it takes, the first one is the default; MaxVideoKbps bounds the video bitrate
	Codecs       []string `json:"codecs"`
	MaxVideoKbps int      `json:"maxVideoKbps"`
	// KeyRequired: the platform does not take a stream without its key (the forms say so; saving without it is allowed)
	KeyRequired bool `json:"keyRequired"`
}

// Template ids. eirl is the cloud's only (Destinations: id eirl, managed).
const (
	TemplateEIRL   = "eirl"
	TemplateCustom = "custom"
)

// MaxVideoKbps: the bound of every video bitrate (the engine's).
const MaxVideoKbps = 20000

// Templates in the order the API lists them.
var Templates = []Template{
	{ID: TemplateEIRL, Protocols: []string{ProtoSRTLA, ProtoSRT}, Codecs: []string{"h265", "h264"},
		MaxVideoKbps: MaxVideoKbps},
	{ID: "twitch", Protocols: []string{ProtoRTMP, ProtoRTMPS}, URL: "rtmp://live.twitch.tv/app",
		URLs:   map[string]string{ProtoRTMP: "rtmp://live.twitch.tv/app", ProtoRTMPS: "rtmps://live.twitch.tv:443/app"},
		Codecs: []string{"h264"}, MaxVideoKbps: 6000, KeyRequired: true},
	{ID: "youtube", Protocols: []string{ProtoRTMPS, ProtoRTMP}, URL: "rtmps://a.rtmps.youtube.com:443/live2",
		URLs: map[string]string{ProtoRTMPS: "rtmps://a.rtmps.youtube.com:443/live2",
			ProtoRTMP: "rtmp://a.rtmp.youtube.com/live2"},
		Codecs: []string{"h264", "h265"}, MaxVideoKbps: MaxVideoKbps, KeyRequired: true},
	// Kick gives every account its own server: the user pastes it
	{ID: "kick", Protocols: []string{ProtoRTMPS}, Codecs: []string{"h264"}, MaxVideoKbps: 8000, KeyRequired: true},
	{ID: "facebook", Protocols: []string{ProtoRTMPS}, URL: "rtmps://live-api-s.facebook.com:443/rtmp/",
		URLs:   map[string]string{ProtoRTMPS: "rtmps://live-api-s.facebook.com:443/rtmp/"},
		Codecs: []string{"h264"}, MaxVideoKbps: 6000, KeyRequired: true},
	{ID: TemplateCustom, Protocols: Protocols, Codecs: []string{"h264", "h265"}, MaxVideoKbps: MaxVideoKbps},
}

// TemplateByID: the template with that id.
func TemplateByID(id string) (Template, bool) {
	i := slices.IndexFunc(Templates, func(t Template) bool { return t.ID == id })
	if i < 0 {
		return Template{}, false
	}
	return Templates[i], true
}

func templateIDs() []string {
	ids := make([]string, len(Templates))
	for i, t := range Templates {
		ids[i] = t.ID
	}
	return ids
}

// templateNames: the default name of a new destination (brands are not translated; custom takes its protocol's).
var templateNames = map[string]string{TemplateEIRL: "EnhancedIRL", "twitch": "Twitch", "youtube": "YouTube",
	"kick": "Kick", "facebook": "Facebook"}

// protocolNames: how a protocol is written for people (the name of a custom destination).
var protocolNames = map[string]string{ProtoSRT: "SRT", ProtoSRTLA: "SRTLA", ProtoRTMP: "RTMP", ProtoRTMPS: "RTMPS",
	ProtoRIST: "RIST", ProtoUDP: "UDP"}

// NewDestination: a destination of template t and protocol with the template's defaults (OUTPUTS.md §9.2): its
// server, its first codec, ABR on with a 1000 kbps floor and min(6000, max) kbps. Without an id.
func NewDestination(t Template, protocol string) Destination {
	d := Destination{Template: t.ID, Managed: t.ID == TemplateEIRL, Protocol: protocol, URL: t.URLs[protocol],
		Mode: "caller", VideoKbps: min(6000, t.MaxVideoKbps), VideoMinKbps: 1000, ABR: true, AudioKbps: 128,
		OutputFPS: "full"}
	if len(t.Codecs) > 0 {
		d.VideoCodec = t.Codecs[0]
	}
	d.Name = templateNames[t.ID]
	if d.Name == "" {
		d.Name = protocolNames[protocol]
	}
	switch protocol {
	case ProtoSRT:
		d.LatencyMs = Defaults().LatencyMs
	case ProtoSRTLA:
		d.LatencyMs = DefaultLatencyMs
	case ProtoRIST:
		d.LatencyMs, d.RISTProfile = RISTLatencyDefault, "main"
	}
	return d
}
