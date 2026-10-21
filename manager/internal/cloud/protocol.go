// Package cloud: the device linked to an EnhancedIRL account (contract in
// devops/docs/active/ENCODER_CLOUD.md, §3.1, §4, §5 and §8).
//
//   - Device-code style pairing: the device requests a code (POST /encoders/pairing), the local web
//     shows it, the user confirms it in the app and the device collects its token by polling.
//   - Channel: OUTBOUND WebSocket (wss://…/encoders/connect, Bearer, subprotocol eirl-encoder.v1)
//     with reconnection. The cloud configures and commands; the device validates with its own
//     allowlists (the same as the local API) and never runs anything the local web cannot already do.
//   - If the cloud goes down, the device keeps streaming with the last applied configuration.
//
// The package is generic over the device (Device interface): eirl-encoderd uses it with the real
// Jetson and eirl-encoder-sim with a simulated one.
package cloud

import (
	"encoding/json"
	"time"
)

// Channel subprotocol and version (§4).
const (
	Subprotocol     = "eirl-encoder.v1"
	ProtocolVersion = 1
)

// Close codes (§4, the backend's encoders.protocol.ts).
const (
	CloseRevoked         = 4401 // final: deletes the token and goes back to "unlinked"
	CloseReplaced        = 4409 // final: another connection with the same token won; no retry until restart
	CloseUpgradeRequired = 4426 // final: old protocol
	CloseServiceRestart  = 1012 // the backend is restarting (deploy): retry
	CloseTryLater        = 1013 // flag off or maintenance: retry after a wait
)

const (
	MaxMessage   = 128 << 10 // per message, both directions
	MaxThumbnail = 64 << 10  // thumbnail JPEG, before base64
)

// EncoderPhase phases (§3.2). The local API computes the phase (api.Status.Service.Phase) and it is
// passed through as is: also the ones newer versions add (waiting_device, from multiple sources).
const (
	PhaseStreaming     = "streaming"
	PhaseReady         = "ready"     // running with preview, not streaming (mixer F3)
	PhaseRehearsal     = "rehearsal" // the old rehearsal mode: a device older than F3
	PhaseWaitingSignal = "waiting_signal"
	PhaseWaitingDevice = "waiting_device" // USB source configured but not connected (multiple sources)
	PhaseRestarting    = "restarting"
	PhaseStopped       = "stopped"
	PhaseFailed        = "failed"
)

// Capabilities the device can announce in hello (§4.1).
const (
	CapWiFi      = "wifi"
	CapAP        = "ap"
	CapSRTLA     = "srtla"
	CapABR       = "abr"
	CapEDID      = "edid"
	CapThumbnail = "thumbnail"
	// Output protocols, H.265 and saved destinations (OUTPUTS.md §10)
	CapRTMP         = "rtmp"
	CapRTMPS        = "rtmps"
	CapRIST         = "rist"
	CapUDP          = "udp"
	CapH265         = "h265"
	CapDestinations = "destinations"
)

// Journal events forwarded to the cloud (§4.1, the `kind`s of service.ParseJournal) and the ones
// the client derives itself.
var journalEvents = map[string]bool{
	"signal_lost": true, "mode_changed": true, "audio_capture_stalled": true, "audio_pll_unlocked": true,
	"audio_recovery": true, "audio_hw_reset": true, "pipeline_exit": true,
	// multiple sources (feat/sources)
	"device_missing": true, "device_lost": true, "format_unsupported": true, "usb2": true,
	"audio_missing": true, "device": true,
}

const (
	EventStreamStarted = "stream_started"
	EventStreamStopped = "stream_stopped"
	EventUplinkUp      = "uplink_up"
	EventUplinkDown    = "uplink_down"
)

// --- Status (EncoderStatus, §4.1): the local snapshot WITHOUT secrets -----------------------------

type EncoderStatus struct {
	Phase          string         `json:"phase"`
	Service        StatusService  `json:"service"`
	Output         string         `json:"output"`
	PendingRestart bool           `json:"pendingRestart"`
	Input          StatusInput    `json:"input"`
	Signal         StatusSignal   `json:"signal"`
	Audio          StatusAudio    `json:"audio"`
	SRT            *StatusSRT     `json:"srt"`
	Transport      string         `json:"transport"`
	OutputState    StatusOutput   `json:"outputState"`
	Uplinks        []StatusUplink `json:"uplinks"`
	Bitrate        *StatusBitrate `json:"bitrate"`
	System         StatusSystem   `json:"system"`
	Network        StatusNetwork  `json:"network"`
	At             time.Time      `json:"at"`
}

// StatusOutput: how the output is doing (§13.3): off | connecting | on | error, and the reason (closed list)
type StatusOutput struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type StatusService struct {
	State    string     `json:"state"`
	Since    *time.Time `json:"since"`
	Restarts int        `json:"restarts"`
}

// StatusInput: the fixed contract fields + Extra, the fields the local API adds (multiple sources:
// source, device, format, mode, audio, active…), passed through unfiltered (the contract tolerates them).
type StatusInput struct {
	EDIDPreferred string
	EDIDActive    *string
	Extra         map[string]any
}

func (i StatusInput) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(i.Extra)+2)
	for k, v := range i.Extra {
		m[k] = v
	}
	m["edidPreferred"], m["edidActive"] = i.EDIDPreferred, i.EDIDActive
	return json.Marshal(m)
}

// StatusSignal: the fixed contract fields + Extra (only kind and measuredFps, from multiple sources:
// the local API has more diagnostic fields that are not forwarded).
type StatusSignal struct {
	Present    bool
	Width      int
	Height     int
	FPS        float64
	Interlaced bool
	Supported  bool
	Extra      map[string]any
}

func (s StatusSignal) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(s.Extra)+6)
	for k, v := range s.Extra {
		m[k] = v
	}
	m["present"], m["width"], m["height"], m["fps"] = s.Present, s.Width, s.Height, s.FPS
	m["interlaced"], m["supported"] = s.Interlaced, s.Supported
	return json.Marshal(m)
}

type StatusAudio struct {
	State string `json:"state"` // playing | pll_unlocked | no_samples | usb | silence | test | unknown
}

type StatusSRT struct {
	Connected  bool    `json:"connected"`
	RTTMs      float64 `json:"rttMs"`
	SendMbps   float64 `json:"sendMbps"`
	LossPct    float64 `json:"lossPct"`
	PktRetrans int     `json:"pktRetrans"`
	PktDropped int     `json:"pktDropped"`
}

type StatusUplink struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"` // ethernet | usb | wifi
	InUse    bool   `json:"inUse"`
	Disabled bool   `json:"disabled"`
	Conflict bool   `json:"conflict"`
	TxKbps   int    `json:"txKbps"`
	// NoInternet: the client Wi-Fi without internet (a captive portal), left out of the SRTLA split (WIFI.md §13)
	NoInternet bool `json:"noInternet,omitempty"`
	// The Rust SRTLA sender's figures for a link in use (SRTLA.md §9.3-§9.4; ENCODER_CLOUD.md §4.1). Today's backend
	// keeps only the uplink fields it knows, so these are ignored until it learns them.
	RTTMs     *int     `json:"rttMs,omitempty"`
	SendKbps  *int     `json:"sendKbps,omitempty"`
	NakPerSec *float64 `json:"nakPerSec,omitempty"`
	Bond      string   `json:"bond,omitempty"`
}

type StatusBitrate struct {
	Kbps    int `json:"kbps"`
	MinKbps int `json:"minKbps"`
	MaxKbps int `json:"maxKbps"`
}

type StatusSystem struct {
	CPUPct       []int        `json:"cpuPct"`
	TempC        StatusTemp   `json:"tempC"`
	Power        *StatusPower `json:"power"`
	Undervoltage bool         `json:"undervoltage"`
}

type StatusTemp struct {
	CPU float64 `json:"cpu"`
	GPU float64 `json:"gpu"`
}

type StatusPower struct {
	InMw int64 `json:"inMw"`
	InMv int64 `json:"inMv"`
}

// StatusNetwork: own access point and client Wi-Fi. They arrive in phase 3 (network and wizard);
// until then, off and without Wi-Fi.
type StatusNetwork struct {
	AccessPoint StatusAP    `json:"accessPoint"`
	WiFi        *StatusWiFi `json:"wifi"`
}

type StatusAP struct {
	On      bool    `json:"on"`
	SSID    *string `json:"ssid"`
	Clients int     `json:"clients"`
	// Policy: "Create my Wi-Fi", always | rescue (the effective one, from network.json; WIFI.md §7).
	// Absent without eirl-netsetup. The device does not obey config.set's accessPoint yet: the cloud
	// first adopts it from here
	Policy string `json:"policy,omitempty"`
}

type StatusWiFi struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal"`
}

// Normalize gets the snapshot ready for the wire: empty arrays instead of null and ≤ 16 elements
// (the backend drops anything beyond).
func (s *EncoderStatus) Normalize() {
	if s.Uplinks == nil {
		s.Uplinks = []StatusUplink{}
	}
	if len(s.Uplinks) > 16 {
		s.Uplinks = s.Uplinks[:16]
	}
	if s.System.CPUPct == nil {
		s.System.CPUPct = []int{}
	}
	if len(s.System.CPUPct) > 16 {
		s.System.CPUPct = s.System.CPUPct[:16]
	}
	switch s.Audio.State {
	case "playing", "pll_unlocked", "no_samples", "usb", "silence", "test", "unknown":
	default:
		s.Audio.State = "unknown"
	}
	switch s.OutputState.State {
	case "off", "connecting", "on", "error":
	default:
		s.OutputState = StatusOutput{State: "off"}
	}
	switch s.OutputState.Reason {
	case "", "no_destination", "no_uplinks", "relay_unreachable", "rejected", "connection_lost", "encoder_error", "internal",
		"tls_failed", "clock_not_synced":
	default:
		s.OutputState.Reason = "internal"
	}
	if r := []rune(s.OutputState.Detail); len(r) > 200 {
		s.OutputState.Detail = string(r[:200])
	}
}

// --- Messages ---------------------------------------------------------------------------------

// inbound: cloud → device (§4.2). A single type with every possible field; each `type` uses its
// own and the rest are ignored.
type inbound struct {
	Type     string          `json:"type"`
	Version  int             `json:"version"`
	Apply    bool            `json:"apply"`
	Config   json.RawMessage `json:"config"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Password string          `json:"password"`
	On       *bool           `json:"on"`
}

type helloMsg struct {
	Type            string         `json:"type"`
	Protocol        int            `json:"protocol"`
	Model           string         `json:"model"`
	FirmwareVersion string         `json:"firmwareVersion"`
	Capabilities    []string       `json:"capabilities"`
	AppliedVersion  int            `json:"appliedVersion"`
	Config          ReportedConfig `json:"config"`
	LanURLs         []string       `json:"lanUrls"`
}

type statusMsg struct {
	Type   string        `json:"type"`
	Status EncoderStatus `json:"status"`
}

type eventMsg struct {
	Type   string         `json:"type"`
	Name   string         `json:"name"`
	At     time.Time      `json:"at"`
	Detail map[string]any `json:"detail,omitempty"`
}

type appliedMsg struct {
	Type      string            `json:"type"`
	Version   int               `json:"version"`
	OK        bool              `json:"ok"`
	Restarted bool              `json:"restarted"`
	Errors    map[string]string `json:"errors,omitempty"`
	// Config: with ok, the resulting configuration (EncoderConfigReported, without secrets). Additive
	// to the contract: e.g. destination null + output on ends up in rehearsal (output "off").
	Config *ReportedConfig `json:"config,omitempty"`
}

type thumbnailMsg struct {
	Type   string    `json:"type"`
	JPEG   string    `json:"jpeg"`
	Width  int       `json:"width"`
	Height int       `json:"height"`
	At     time.Time `json:"at"`
}

type resultMsg struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

type pongMsg struct {
	Type string `json:"type"`
}
