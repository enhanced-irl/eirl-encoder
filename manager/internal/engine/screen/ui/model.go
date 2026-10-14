package ui

import (
	"encoding/json"
	"strings"
	"time"
)

// --- the engine's state, as the screen reads it ---

// Status is the part of the engine's published state (mixer.json, MIXER.md §5) the screen draws. Decoded
// from the same JSON, so the engine can hand over what it publishes (ParseStatus) or fill it directly.
type Status struct {
	Program ProgramStatus  `json:"program"`
	Sources []SourceStatus `json:"sources"`
	Screen  struct {
		Locked bool `json:"locked"`
	} `json:"screen"`
	Output OutputStatus `json:"output"`
	Audio  *AudioStatus `json:"audio,omitempty"`
}

type ProgramStatus struct {
	Main       *string `json:"main"`
	Secondary  *string `json:"secondary"`
	Layout     string  `json:"layout"`
	Transition string  `json:"transition"`
	PiP        *struct {
		Corner string `json:"corner"`
		Size   int    `json:"size"`
	} `json:"pip,omitempty"`
	Pause *struct {
		On   bool   `json:"on"`
		Text string `json:"text"`
		Mute bool   `json:"mute"`
	} `json:"pause,omitempty"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type SourceStatus struct {
	ID     string `json:"id"`
	Slot   int    `json:"slot"` // 1-based
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Alias  string `json:"alias"` // the slot's alias (CONTRACT §3), "" = none: the name
	State  string `json:"state"`
	Detail string `json:"detail"` // Spanish: the screen shows DetailCode translated (ENGINE_GO.md §13)
	// DetailCode is why it is not live, a stable code ("" = nothing to say); DetailArgs its arguments
	DetailCode string            `json:"detailCode,omitempty"`
	DetailArgs map[string]string `json:"detailArgs,omitempty"`
	Mode       string            `json:"mode"`
	FPS        float64           `json:"fps"`
}

type OutputStatus struct {
	Mode     string `json:"mode"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	SentKbps *int   `json:"sentKbps,omitempty"`
	RTTMs    *int   `json:"rttMs,omitempty"`
}

type AudioStatus struct {
	Main    *string      `json:"main"`
	Mode    string       `json:"mode,omitempty"`
	Follow  bool         `json:"follow,omitempty"`
	Sources []AudioInput `json:"sources"`
	// Links is each camera's audio: video id → audio id, or null for none of its own (resolved: by hand, else the
	// same device's); nil from an engine without them
	Links  map[string]*string `json:"links,omitempty"`
	Phones *PhonesStatus      `json:"phones,omitempty"`
}

type AudioInput struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	State     string   `json:"state"`
	DelayMs   int      `json:"delayMs,omitempty"`
	VideoSlot int      `json:"videoSlot,omitempty"`
	InMix     bool     `json:"inMix,omitempty"`
	GainDb    *float64 `json:"gainDb,omitempty"`
	// Detail (Spanish), its code and arguments: why the input is not live (ENGINE_GO.md §13)
	Detail     string            `json:"detail,omitempty"`
	DetailCode string            `json:"detailCode,omitempty"`
	DetailArgs map[string]string `json:"detailArgs,omitempty"`
}

type PhonesStatus struct {
	On         bool              `json:"on"`
	GainDb     *int              `json:"gainDb,omitempty"`
	State      string            `json:"state"`
	Detail     string            `json:"detail,omitempty"`
	DetailCode string            `json:"detailCode,omitempty"`
	DetailArgs map[string]string `json:"detailArgs,omitempty"`
}

// ParseStatus decodes the engine's state JSON.
func ParseStatus(b []byte) (*Status, error) {
	var st Status
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Status) mainID() string { return str(s.Program.Main) }

// sourceName is a source's name in the screen's language: the engine's own sources (fixed ids: hdmi, i2s,
// test) by their translated name; the rest as the engine says (a device's or the user's name).
func (u *UI) sourceName(id, name string) string {
	if n, ok := u.Tx.SourceNames[id]; ok {
		return n
	}
	return name
}

// srcLabel is how a source is called on the screen: its alias, else its name (sourceName).
func (u *UI) srcLabel(x *SourceStatus) string {
	if a := strings.TrimSpace(x.Alias); a != "" {
		return a
	}
	return u.sourceName(x.ID, x.Name)
}
func (s *Status) secondaryID() string { return str(s.Program.Secondary) }

func (s *Status) pauseOn() bool { return s.Program.Pause != nil && s.Program.Pause.On }

func (s *Status) audioMode() string {
	if s.Audio == nil {
		return ""
	}
	return s.Audio.Mode
}

// --- the app's local API, as the screen reads it (engine_app.NetState) ---

// Net is the app's network state and actions for the screen: the Python engine's NetState (engine_app.py),
// whose Go port (internal/engine/appapi) the owner plugs in. Every method returns at once (the requests run
// on the implementation's own goroutine) and is safe from any goroutine. The implementation calls the
// changed callback it was built with when there is something new to draw.
type Net interface {
	Snapshot() NetSnapshot
	// SetPanel: Settings opened (ask now, also the wifi credentials) or closed (the credentials and the scan
	// out of memory; a queued connect is dropped with its password).
	SetPanel(on bool)
	// SetScan: the network list opened (scan now and every 2.5 s) or closed.
	SetScan(on bool)
	// The switches: false if another change is under way or the network state is unknown.
	SetEnabled(on bool) bool  // the RTMP/SRT reception (PUT /network)
	SetAP(on bool) bool       // «Crear mi wifi»: always / rescue (PUT /network/wifi)
	SetClient(on bool) bool   // «Conectarse a una wifi» (PUT /network/wifi)
	SetSSID(name string) bool // the name of «Crear mi wifi» (PUT /network/ap {"ssid"})
	// Connect the client to ssid: with the typed password, or nil (an open network, or a saved profile).
	// The implementation copies the password, sends it once and never logs, publishes or keeps it; the
	// caller wipes its own buffer right after the call.
	Connect(ssid string, password []byte) bool
	Forget(ssid string) bool
	// Say shows a message in the panel for 6 s.
	Say(text string)
}

// NetSnapshot is NetState.snapshot().
type NetSnapshot struct {
	// API: Unknown (not asked yet), Yes (the app answers) or No (no socket: an older app, wifi.json)
	API       Tri
	Status    *NetStatus // GET /network; nil if none
	Error     string     // why there is no Status (the app's message)
	Wifi      *WifiCreds // the board's wifi to join (only with the panel open)
	Client    *WifiClient
	Warning   string    // "two_channels" or ""
	Scan      []ScanNet // the networks around (with the list open)
	ScanBusy  bool      // no scan yet («Buscando redes…»); Scan nil and not busy = not asked
	ScanError string
	Pending   *Pending
	Message   string
	MsgUntil  time.Time
	LastPost  *LastPost
}

// Tri is a three-valued flag.
type Tri int

const (
	Unknown Tri = iota
	Yes
	No
)

// NetStatus is GET /network as the screen uses it.
type NetStatus struct {
	Enabled     bool
	AccessPoint AccessPoint
	LANEnabled  bool // lan.enabled is true
	Cameras     int  // cameras.items: how many are registered
	// The Network cameras page (MIXER.md §21): the cable, the most cameras and each one (CamItems nil: not given)
	LAN      LANStatus
	CamMax   int // cameras.max (0: not given)
	CamItems []NetCamera
}

// LANStatus is lan of GET /network: the cameras by cable.
type LANStatus struct {
	State string // off | starting | on | error | ""
	Iface string // eth0
	IP    string // its IPv4, the one the cameras point at ("" none)
	Error string
}

// NetCamera is one registered network camera (cameras.items of GET /network): no secrets.
type NetCamera struct {
	ID, Name  string
	Connected bool     // a sender is publishing
	Via       string   // wifi | lan | ""
	Protocol  string   // srt | rtmp | ""
	Codec     string   // h264 | h265 | ""
	Kbps      *int     // what it sends
	RTTMs     *float64 // SRT only
	LossPct   *float64 // SRT only
}

// AccessPoint is accessPoint (or GET /network/wifi's ap), only what validates.
type AccessPoint struct {
	Enabled *bool
	Policy  string // always | rescue | ""
	State   string // off | starting | on | error | ""
	SSID    string
	Channel *int
	Clients *int
	Error   string
}

// WifiCreds is the board's wifi to join: ssid, password and the QR rows ("0"/"1").
type WifiCreds struct {
	SSID, Password string
	QR             []string
	On             bool   // only for wifi.json (an older app)
	Channel        string // the saved 2.4 GHz channel: auto | 1-11 ("" not given); the one in use: AccessPoint.Channel
}

// WifiClient is GET /network/wifi's client.
type WifiClient struct {
	Enabled  bool
	State    string // off | disconnected | connecting | connected | error
	SSID     string
	Signal   *int
	Band     string // 2.4 | 5 | ""
	Internet *bool
	Error    string // wrong_password | not_found | connect_failed | unavailable | ""
}

// ScanNet is one network of GET /network/wifi/scan.
type ScanNet struct {
	SSID      string
	Security  string // open | wep | wpa | wpa2 | wpa3 | enterprise
	Signal    int
	Band      string
	Known     bool
	Connected bool
}

// Pending is the change under way: net/client (On), ap (On = always), connect/forget (SSID).
type Pending struct {
	What string
	On   bool
	SSID string
}

// LastPost is the last connect/forget: whether the app took it, and when.
type LastPost struct {
	What, SSID string
	OK         bool
	At         time.Time
}

// noNet is the Net of a screen without the app's API (tests, ui-render without a fake).
type noNet struct{}

func (noNet) Snapshot() NetSnapshot       { return NetSnapshot{} }
func (noNet) SetPanel(bool)               {}
func (noNet) SetScan(bool)                {}
func (noNet) SetEnabled(bool) bool        { return false }
func (noNet) SetAP(bool) bool             { return false }
func (noNet) SetClient(bool) bool         { return false }
func (noNet) SetSSID(string) bool         { return false }
func (noNet) Connect(string, []byte) bool { return false }
func (noNet) Forget(string) bool          { return false }
func (noNet) Say(string)                  {}

// --- what the screen sends and reads live ---

// Command is an order from the screen to the engine (the socket's commands, MIXER.md §5). Origin says what
// to do with the result: touch and audio failures are logged; a held button's failure is shown; a Settings
// page's (page) and the audio settings' (audioset) are shown in their view.
type Command struct {
	Msg    map[string]any
	Origin string // touch | audio | hold | page | audioset
	X, Y   int    // where the finger was (logs)
}

// Levels are the live audio levels for the VU meters (read 20 times a second: cheap and safe from the
// screen's goroutine).
type Levels interface {
	// ProgramPeak is the program's level of a channel in dB; ok false = no program level at all.
	ProgramPeak(ch int) (db float64, ok bool)
	// InputPeak is an audio input's level in dB while it is live.
	InputPeak(id string) (db float64, ok bool)
}

// PowerRequest is a reboot/poweroff asked on the screen (the engine publishes it as screen.power; the app
// does it).
type PowerRequest struct {
	Want string  `json:"want"`
	Seq  int     `json:"seq"`
	At   float64 `json:"at"`
}
