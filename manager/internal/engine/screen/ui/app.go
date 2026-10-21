package ui

import (
	"strings"
	"time"
)

// --- the app's device, setup, update, sources and cloud API, as the screen reads it (CONTRACT §1-§5) ---

// App is the app's device API for the screen: the device language, name, time zone and clock, the first-boot
// setup, the updates, the source slots and the link to EnhancedIRL. Like Net, every method returns at once (the
// requests run on the implementation's own goroutine) and is safe from any goroutine; the implementation calls
// the changed callback it was built with when there is something new to draw. Its Go implementation is
// appapi.DeviceState, which the screen reaches through the Net it is given (AppProvider): no other wiring.
type App interface {
	AppSnapshot() AppSnapshot
	// Watch says what the screen shows, so the implementation asks the app for what it needs and only that.
	Watch(w AppWatch)
	// Do queues an action (they run one at a time, in order); false if the queue is full or there is no app.
	// Its result comes back in AppSnapshot().Last.
	Do(a AppAction) bool
	// SetupPassword sets the web's admin password during setup (POST /setup/password, no code over the local
	// socket). The implementation copies the password, sends it once and never logs, publishes or keeps it; the
	// caller wipes its own buffer right after the call.
	SetupPassword(password []byte) bool
}

// AppProvider is a Net that also carries the app's device API (the engine's appapi client does).
type AppProvider interface {
	App() App
}

// AppWatch is what is on the screen. Without anything, the device and the setup state are asked every ~30 s
// (the device language; whether the wizard must show).
type AppWatch struct {
	Device bool // Settings or the wizard: the device every ~2 s (the clock, a language changed on the web)
	Setup  bool // the wizard: the setup state every ~3 s (and the setup code while a password is needed)
	Update bool // the Updates page: every ~2 s
	Slots  bool // the Sources page: every ~2 s
	Zones  bool // the time zone picker: the list, once
	Cloud  bool // the wizard's link step: the link state every ~3 s
	About  bool // the About page: the IP addresses and the build's commit
	// MIXER.md §21: the pages of everything the web configures
	Dest    bool // the Destination page: GET /destinations (an older app: GET /destination) every ~2 s
	Status  bool // the Uplinks and Input pages: GET /status every ~2 s
	Sources bool // the Input page: GET /sources every ~10 s (a camera just plugged in shows up)
	// Bluetooth: the Bluetooth page (BLUETOOTH.md §3.6): GET /bluetooth every ~2 s, every second while it searches,
	// is visible or an order on a device runs
	Bluetooth bool
}

// AppAction is a change asked of the app.
type AppAction struct {
	What  string // language | name | timezone | ntp | time | setup_step | setup_complete | setup_restart | update_check | update_install | slots | cloud_pair, and MIXER.md §21's (AppActions)
	Value string // language, name, timezone; setup_step: the step id; bt_device: the order (pair | connect | disconnect | forget)
	State string // setup_step: done | skipped
	On    bool   // ntp
	Time  time.Time
	Slots []SlotAssign // slots: all of them
	// MIXER.md §21's actions: the route's {id} (an uplink's name, a camera's id), the JSON body's fields (nil:
	// none) and its secret fields (a stream key, a password: Do copies them; the caller wipes its own right after)
	ID      string
	Body    map[string]any
	Secrets map[string][]byte
}

// AppActions are MIXER.md §21's actions (their route is the client's): what each one sends.
//
//	dest_save     PUT /destination  Body: url, latencyMs, videoBitrateKbps, audioBitrateKbps, adaptiveBitrate,
//	              videoMinBitrateKbps, apply; streamId / passphrase: null clears it; Secrets: streamId, passphrase
//	dest_reveal   GET /destination/secrets → AppSnapshot.DestSecrets (until DropSecrets)
//	The saved destinations (an app with GET /destinations; OUTPUTS.md §9.3):
//	dest_create   POST /destinations  Body: template, protocol and the patch's fields (name, url, latencyMs, ristProfile,
//	              username, videoCodec, videoBitrateKbps, videoMinBitrateKbps, adaptiveBitrate, audioBitrateKbps,
//	              outputSize, outputFps); Secrets: streamId, passphrase, key, password → AppResult.ID
//	dest_update   PUT /destinations/{ID}  Body: the patch's changed fields and apply; streamId / passphrase / key /
//	              password: null clears it; Secrets: the typed ones
//	dest_delete   DELETE /destinations/{ID}
//	dest_activate POST /destinations/{ID}/activate  Body: apply (true) → AppSnapshot.Dests
//	dest_secrets  GET /destinations/{ID}/secrets → AppSnapshot.DestSecrets (with its ID; until DropSecrets)
//	restart       POST /service/restart (apply what is saved)
//	uplink        PUT /uplinks/{ID}  Body: disabled
//	input_save    PUT /input  Body: source, device, format, mode, audio, edidPreferred, apply
//	sources_scan  GET /sources → AppSnapshot.Sources
//	cloud_unlink  DELETE /cloud (also cancels a pairing)
//	password      PUT /password  Secrets: current, new
//	cam_add       POST /cameras  Body: name
//	cam_rename    PATCH /cameras/{ID}  Body: name
//	cam_rotate    PATCH /cameras/{ID}  (rotateKey)
//	cam_remove    DELETE /cameras/{ID}
//	cam_creds     GET /network/credentials?cameras=1 → AppSnapshot.CamCreds (until DropSecrets)
//	ap_enabled    PUT /network/ap  Body: enabled (the cameras over the board's Wi-Fi)
//	ap_channel    PUT /network/ap  Body: channel (auto | 1-11)
//	ap_rotate     PUT /network/ap  (rotatePassword)
//	lan_enabled   PUT /network/lan  Body: enabled (the cameras by cable)
//	Bluetooth (BLUETOOTH.md §3.4); each answers the state → AppSnapshot.Bluetooth:
//	bt_power      PUT /bluetooth  Body: on
//	bt_scan       POST /bluetooth/scan (30 s)
//	bt_visible    POST /bluetooth/visible  Body: on (the 2-minute window, D4)
//	bt_device     POST /bluetooth/devices/{ID}/{Value}  ID: the address; Value: pair | connect | disconnect | forget
//	bt_use        PUT /bluetooth/devices/{ID}  Body: use (listen | mic | input)
var AppActions = []string{"dest_save", "dest_reveal", "dest_create", "dest_update", "dest_delete", "dest_activate",
	"dest_secrets", "restart", "uplink", "input_save", "sources_scan",
	"cloud_unlink", "password", "cam_add", "cam_rename", "cam_rotate", "cam_remove", "cam_creds", "ap_enabled",
	"ap_channel", "ap_rotate", "lan_enabled", "modem_save", "modem_pin", "modem_reset", "modem_subnet",
	"bt_power", "bt_scan", "bt_visible", "bt_device", "bt_use"}

// SecretsDropper is an App that can forget the secrets it read for the screen (DestSecrets, CamCreds): the page
// that showed them calls it when it goes.
type SecretsDropper interface {
	DropSecrets()
}

// dropSecrets forgets what the app revealed, if the App can.
func (u *UI) dropSecrets() {
	if d, ok := u.app().(SecretsDropper); ok {
		d.DropSecrets()
	}
}

// AppSnapshot is the app's device state for the screen. Pointers and slices are read-only.
type AppSnapshot struct {
	// Each part: nil = not known yet; its API: Unknown (not asked yet), Yes (the app answers), No (an older app
	// without the route: 403 local_forbidden or 404, or no socket at all).
	Device    *DeviceInfo
	DeviceAPI Tri
	Setup     *SetupInfo
	SetupAPI  Tri
	Update    *UpdateInfo
	UpdateAPI Tri
	Slots     *SlotsInfo
	SlotsAPI  Tri
	Zones     []TimeZone // nil: not asked yet (or failed: ZonesErr)
	ZonesErr  string
	Cloud     *CloudInfo
	CloudAPI  Tri
	// MIXER.md §21
	Dest    *DestInfo
	DestAPI Tri
	// The saved destinations (OUTPUTS.md §9.3): DestsAPI No is an older app (the single destination of Dest)
	Dests      *DestList
	DestsAPI   Tri
	Status     *StatusInfo
	StatusAPI  Tri
	Sources    *SourcesInfo
	SourcesAPI Tri
	// The secrets an eye asked for (nil: not asked, or dropped): the stream key and passphrase, the cameras' URLs
	DestSecrets *DestSecrets
	CamCreds    []CamConnect
	// The board's Bluetooth (BLUETOOTH.md §3.4): BluetoothAPI No is an app without it
	Bluetooth    *BTState
	BluetoothAPI Tri
	// Local facts the engine knows itself (About): the build's commit and the board's IP addresses
	Commit string
	IPs    []string
	// Pending is the action under way or the next queued ("" none); Last, the last one's result
	Pending string
	Last    *AppResult
}

// DeviceInfo is GET /device.
type DeviceInfo struct {
	Language  string
	Languages []string
	Name      string
	Hostname  string
	Timezone  string
	Now       time.Time // the device's clock when asked
	At        time.Time // when it was asked (monotonic clock of the screen's process): Now + (now − At)
	NTP       bool
	Synced    bool
	RTC       *bool
	Release   string
	AppVer    string
	EngineVer string
}

// SetupInfo is GET /setup.
type SetupInfo struct {
	Done          bool
	Steps         []SetupStep
	NeedsPassword bool
	CodeRequired  bool
	// Code is the setup code (CONTRACT §2.1) to finish from the web, only while the wizard needs a password
	Code string
}

// SetupStep is one step of the setup and its state: todo | done | skipped.
type SetupStep struct {
	ID, State string
}

// StepState is the state of step id ("" if the app did not list it).
func (s *SetupInfo) StepState(id string) string {
	if s == nil {
		return ""
	}
	for _, st := range s.Steps {
		if st.ID == id {
			return st.State
		}
	}
	return ""
}

// UpdateInfo is GET /update.
type UpdateInfo struct {
	Current   string
	State     string // idle | checking | available | downloading | ready | installing | rolled_back | error
	Available *UpdateRelease
	Progress  float64 // 0-1
	LastCheck *time.Time
	Error     string // a stable code
	// Additive (an older app gives none of these: the screen falls back to the code and its own rules)
	Message        string // Error in the device language
	CanInstall     *bool  // «Update» can be pressed now
	Blocked        string // why not: streaming | not_ready | busy
	BlockedMessage string // and in words
	LastResult     *UpdateRun
	TestBuild      bool // this build trusts a test key (never on a customer device)
	Versions       UpdateVersions
}

// UpdateRun is the installer's last run: installed | rolled_back | failed | refused | installing.
type UpdateRun struct {
	State, From, To string
	Error, Message  string
	At              time.Time
}

// UpdateVersions are the installed versions as the updater reads them.
type UpdateVersions struct {
	Release, App, Engine, Commit string
}

// UpdateRelease is the release that can be installed: its notes in the device language.
type UpdateRelease struct {
	Version string
	Notes   string
	Size    int64
}

// SlotsInfo is GET /sources/slots.
type SlotsInfo struct {
	Slots          []SlotInfo
	Candidates     []SlotCandidate
	EngineSupports bool
}

// SlotInfo is one slot: what it is set to, its alias and the source in it now (nil: none).
type SlotInfo struct {
	Slot    int
	Assign  string // auto | off | a source id
	Alias   string
	Current *SlotSource
}

// SlotSource is the source in a slot now.
type SlotSource struct {
	ID, Name, Kind, State string
}

// SlotCandidate is a source a slot can be pinned to (connected: the engine sees it now).
type SlotCandidate struct {
	ID, Name, Kind string
	Connected      bool
}

// SlotAssign is one slot of PUT /sources/slots.
type SlotAssign struct {
	Slot   int    `json:"slot"`
	Assign string `json:"assign"`
	Alias  string `json:"alias"`
}

// TimeZone is one zone of GET /device/timezones.
type TimeZone struct {
	ID, Offset string
}

// CloudInfo is GET /cloud: the link to an EnhancedIRL account.
type CloudInfo struct {
	State        string // unlinked | pairing | linked | disabled (503 cloud_disabled)
	Code         string
	ExpiresAt    *time.Time
	LinkURL      string
	LinkQR       []string // the QR of LinkURL in rows of 0 and 1 (made by the client)
	AccountLabel string
	Online       *bool
	LastError    string
}

// DestInfo is GET /destination: the stream's destination as the web shows it (the key and the passphrase never:
// only whether they are set, and the key masked). It is also one saved destination of GET /destinations (its
// public view, OUTPUTS.md §9.3), which carries the fields after PendingRestart.
type DestInfo struct {
	ManagedBy      string // "eirl": EnhancedIRL sets it (read-only here and on the web)
	URL            string // without its secrets ("" if none, or URLInvalid)
	URLInvalid     bool
	Mode           string // caller | listener
	LatencyMs      int
	StreamIDSet    bool
	StreamIDMasked string
	PassphraseSet  bool
	VideoKbps      int
	AudioKbps      int
	ABR            bool
	VideoMinKbps   int
	Transport      string // srt | srtla (the saved URL's)
	Output         string // on | off (saved)
	PendingRestart bool   // saved, not applied: «Apply» restarts the stream
	// A saved destination's (GET /destinations); empty from an older app
	ID          string // eirl | d-<8 hex>
	Name        string
	Template    string // eirl | twitch | youtube | kick | facebook | custom
	Managed     bool   // the cloud's (eirl): it can be activated, not edited or deleted
	Active      bool
	Protocol    string // srt | srtla | rtmp | rtmps | rist | udp
	KeySet      bool   // the RTMP stream key
	KeyMasked   string
	RistProfile string // simple | main
	Username    string // RIST main profile
	PasswordSet bool
	VideoCodec  string // h264 | h265
	OutputSize  string // "" (the program's) | 1280x720 | 960x540
	OutputFps   string // full | half
}

// DestList is GET /destinations: the saved destinations, the active one and the templates.
type DestList struct {
	Active         string // the active one's id ("" none)
	ManagedBy      string // local | eirl
	Destinations   []DestInfo
	Templates      []DestTemplate
	PendingRestart bool
}

// ByID is a saved destination by its id (nil: none).
func (l *DestList) ByID(id string) *DestInfo {
	if l == nil || id == "" {
		return nil
	}
	for i := range l.Destinations {
		if l.Destinations[i].ID == id {
			return &l.Destinations[i]
		}
	}
	return nil
}

// Template is a template by its id (nil: the app did not list it).
func (l *DestList) Template(id string) *DestTemplate {
	if l == nil {
		return nil
	}
	for i := range l.Templates {
		if l.Templates[i].ID == id {
			return &l.Templates[i]
		}
	}
	return nil
}

// DestTemplate is one of the app's templates (OUTPUTS.md §9.2): the protocols and codecs a destination of it takes
// (the first ones, its defaults), its server, the most video it takes, and whether it needs a stream key.
type DestTemplate struct {
	ID           string
	Protocols    []string
	URL          string
	URLs         map[string]string // the server of each protocol, when the template has its own (Twitch: rtmp and rtmps)
	Codecs       []string
	MaxVideoKbps int // 0: not said (the app's own maximum)
	KeyRequired  bool
}

// DestSecrets is GET /destination/secrets (local socket only): what the eye of the key and of the passphrase shows;
// or GET /destinations/{ID}/secrets, with the destination's ID, its RTMP key and RIST password too.
type DestSecrets struct {
	ID                   string // the saved destination they are of ("" the single destination of an older app)
	StreamID, Passphrase string
	Key, Password        string
}

// StatusInfo is what the pages use of GET /status: the uplinks (SRTLA) and the saved input.
type StatusInfo struct {
	Transport      string // the running stream's: srt | srtla
	Output         string // on | off
	PendingRestart bool
	HasUplinks     bool // the board has the uplinks dispatcher (else the page says SRTLA is not available)
	Uplinks        []UplinkInfo
	Input          InputInfo
	// Modems and every connection's dot (MODEMS.md §4.2); empty from an app without them
	Modems []ModemInfo
	Links  []LinkDot
}

// ModemInfo is one cellular modem (GET /status modems[], MODEMS.md §4.2), validated.
type ModemInfo struct {
	ID, Iface, Manufacturer, Model string
	Backend                        string // mm | hilink (a HiLink forgets the PIN when it powers off)
	State, SIM, Registration       string
	Operator, Tech                 string
	RSRP, RSRQ, SINR, RSSI         *float64
	IMEITail, ICCIDTail            string
	Alias, APN, APNAuto            string
	Roaming                        bool
	Mode                           string   // auto | 4g | 5g
	Modes                          []string // what Mode can be set to
	Can                            []string // apn | roaming | mode | pin | reset | subnet
}

// Label: the modem's name: its alias, its operator, else its model.
func (m ModemInfo) Label() string {
	switch {
	case m.Alias != "":
		return m.Alias
	case m.Operator != "":
		return m.Operator
	}
	return strings.TrimSpace(m.Manufacturer + " " + m.Model)
}

// CanDo: the modem lets that be done from here.
func (m ModemInfo) CanDo(what string) bool {
	for _, c := range m.Can {
		if c == what {
			return true
		}
	}
	return false
}

// UplinkInfo is one internet uplink (GET /status uplinks[]).
type UplinkInfo struct {
	Name, IP, Kind string // kind: ethernet | wifi | modem | ""
	InUse          bool   // the running SRTLA stream uses it
	Disabled       bool   // taken out of the bonding by hand
	Conflict       bool   // the same IP as another one: not used
	NoInternet     bool
	TxKbps         int
	// The app's judgement of the link (MODEMS.md §5); empty from an app without it
	ModemID    string // the modem behind it (StatusInfo.Modems)
	Label      string // the modem's operator or its alias: the row's title instead of the interface
	Health     string // good | weak | bad | off: the row's dot
	HealthCode string // why: the closed list of MODEMS.md §5, translated by Stream.UplinkHealth
	// The Rust SRTLA sender's view of a link in use (SRTLA.md §9.4); HasStats false = none (another sender, or
	// not streaming)
	HasStats  bool
	RTTMs     int
	SendKbps  int
	NakPerSec float64
	Bond      string // "" | active | stalled | weak | down
}

// InputInfo is the saved input (GET /status input) and what runs now.
type InputInfo struct {
	Source        string // hdmi | usb | test
	Device        string // usb: a /dev/v4l path or auto
	Format, Mode  string // usb: auto, or one of the source's options
	Audio         string // auto | none | i2s | alsa:<card>
	EDIDPreferred string
	EDIDActive    string
	ActiveState   string // starting | waiting_device | ready | ""
	ActiveSource  string // what runs: hdmi | usb | test ("" an older app)
	ActiveName    string
	ActiveFormat  string
	ActiveMode    string
}

// SourcesInfo is GET /sources: what is connected and what each USB source can be captured with.
type SourcesInfo struct {
	Sources []SourceChoice
	Audio   []AudioChoice
}

// SourceChoice is one video source of the catalogue.
type SourceChoice struct {
	ID, Kind, Name string // kind: hdmi-csi | usb | test
	USB3           bool
	USBKnown       bool           // the app said the bus speed (else neither USB 3 nor USB 2: just USB)
	Auto           *CaptureOption // what «automatic» takes (usb)
	Options        []CaptureOption
	AudioName      string // the same device's audio, if it has
	Warnings       []string
}

// CaptureOption is one way to capture a USB source.
type CaptureOption struct {
	Format, Mode        string // the format; WxH@fps (VIDEO_MODE)
	FPS                 float64
	OutWidth, OutHeight int
	BusShare            float64
}

// AudioChoice is one audio input of the catalogue.
type AudioChoice struct {
	ID, Kind, Name string // kind: i2s | usb | network
}

// CamConnect is a network camera's URLs (with its key): GET /network/credentials?cameras=1.
type CamConnect struct {
	ID   string
	Wifi CamURLs  // over the board's Wi-Fi
	LAN  *CamURLs // by cable (nil: the board has no IP on its cable)
}

// CamURLs are a camera's URLs over one way in, and the SRT one's QR in rows of 0 and 1 (made by the client).
type CamURLs struct {
	SRT, RTMP, RTMPServer, RTMPKey string
	SRTQR                          []string
}

// BTState is GET /bluetooth (BLUETOOTH.md §3.4), validated by the client: whether there is Bluetooth (else why
// not), whether it is on, the visible window (D4), the search, and the audio devices (paired first, then the ones a
// search found).
type BTState struct {
	Available   bool
	Reason      string // why not: no_stack | no_adapter; with Available, no_audio (devices managed, not heard) or ""
	On          bool   // what was asked (saved)
	Powered     bool   // how the adapter is
	PowerFailed bool   // the adapter does not follow On (blocked, or its firmware fails): the app retries every minute
	Name        string // how the encoder shows to other devices
	Visible     bool
	VisibleLeft int // seconds of the visible window left
	Scanning    bool
	Devices     []BTDevice
}

// BTDevice is one Bluetooth audio device.
type BTDevice struct {
	Address   string // AA:BB:CC:DD:EE:FF
	Name      string
	Kind      string // headphones | headset | speaker | phone | computer | audio
	Paired    bool
	Connected bool
	Ready     bool     // its audio for its use is up
	Use       string   // listen | mic | input ("" not paired)
	Uses      []string // what it can be used for, in the order offered
	RSSI      *int
	Pending   string // an order running: pair | connect | disconnect | forget
	Error     string // why the last order failed: rejected | timeout | not_reachable | gone | failed
	Output    bool   // the monitor plays through it now
}

// Device is a device by its address (nil: not listed).
func (b *BTState) Device(addr string) *BTDevice {
	if b == nil || addr == "" {
		return nil
	}
	for i := range b.Devices {
		if b.Devices[i].Address == addr {
			return &b.Devices[i]
		}
	}
	return nil
}

// Busy: an order on a device runs (the app takes one at a time).
func (b *BTState) Busy() bool {
	if b == nil {
		return false
	}
	for _, d := range b.Devices {
		if d.Pending != "" {
			return true
		}
	}
	return false
}

// AppResult is how an action went: OK, or the HTTP code with the app's error, message (in the device language)
// and field errors (field → message).
type AppResult struct {
	What    string
	OK      bool
	Status  int
	Code    string
	Message string
	Fields  map[string]AppFieldError
	At      time.Time
	// Applied: PUT /sources/slots's applied (the engine took it now; false: at its next start); nil if not given
	Applied *bool
	// Restarted: PUT /destination's or /input's (with apply): the stream restarted to take it; nil if not given
	Restarted *bool
	// ID: the destination POST /destinations created
	ID string
}

// AppFieldError is a field's error: its code (duplicate, invalid…) and the app's message.
type AppFieldError struct {
	Code, Message string
}

// noApp is the App of a screen without the app's device API (tests, an engine without it).
type noApp struct{}

func (noApp) AppSnapshot() AppSnapshot  { return AppSnapshot{} }
func (noApp) Watch(AppWatch)            {}
func (noApp) Do(AppAction) bool         { return false }
func (noApp) SetupPassword([]byte) bool { return false }

// app is the device API: App if it was set, else the Net's (AppProvider), else none.
func (u *UI) app() App {
	if u.App != nil {
		return u.App
	}
	if p, ok := u.Net.(AppProvider); ok {
		if a := p.App(); a != nil {
			u.App = a
			return a
		}
	}
	return noApp{}
}
