package ui

import "time"

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
}

// AppAction is a change asked of the app.
type AppAction struct {
	What  string // language | name | timezone | ntp | time | setup_step | setup_complete | setup_restart | update_check | update_install | slots | cloud_pair
	Value string // language, name, timezone; setup_step: the step id
	State string // setup_step: done | skipped
	On    bool   // ntp
	Time  time.Time
	Slots []SlotAssign // slots: all of them
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
