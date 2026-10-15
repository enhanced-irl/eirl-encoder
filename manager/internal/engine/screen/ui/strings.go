package ui

// Strings is every text the screen shows, one table per language (CONTRACT §1: es, en, fr): the drawing and
// the touch logic never carry a user-visible literal. Spanish is the Python engine's texts, verbatim. Fields
// ending in F are fmt formats with their verbs in the documented order, the same in every language (a test
// checks it). Every field is filled in every table (another test).
type Strings struct {
	Code string // the language code: es | en | fr
	// Header
	LogoA, LogoB      string // the name in text when the logo is missing ("Enhanced" + "IRL")
	LiveClockF        string // %d:%02d:%02d
	NoConnectionF     string // %s = reason
	RetryingReason    string
	ConnectingReasonF string // %s = reason
	Connecting        string
	NotEmitting       string
	TempF             string // %.0f (°C)
	VoltsF            string // %.2f, decimal point replaced by Decimal
	MbpsF             string // %.1f
	// LinkShort: a connection's short name in the header's dots, by kind (ethernet | usb | wifi | modem)
	LinkShort             map[string]string
	RTTF                  string // %d
	LinkSRT, LinkSRTLAOne string // the output's link: SRT; SRTLA with one uplink
	LinkSRTLAF            string // %d uplinks (0 or more than one)
	Decimal               string // the decimal separator
	Sep                   string // between parts of a line
	Reasons               map[string]string
	ProgramTag, PausedTag string
	// Video view
	LayoutLabel, TransitionLabel string
	Full, PiPF, Side, Pause      string // PiPF: %s = corner arrow
	PiPSubF                      string // %s = size (PiPQuarter / PiPThird)
	PiPQuarter, PiPThird         string
	Cut, Fade                    string
	EmitLive, EmitHoldStop       string
	EmitConnecting, Emit         string
	EmitHold                     string
	AudioMixerButton             string
	AudioMix, AudioSingle        string
	AudioFollowSuffix            string
	AudioSilencedSuffix          string
	TileFreeF                    string // %d
	TileNameF                    string // %d, %s
	OnAir, InPiP, InProgram      string
	Ready                        string
	ModeVerticalF                string // %s (width), %s (fps)
	StateText                    map[string]string
	// Lock, Power menu, powering
	Locked, LockedHelp       string
	LockedHint               string
	HoldForF                 string // %d s
	CannotF                  string // %s = error code
	PowerFailedF             string // %s = PowerVerbReboot / PowerVerbOff
	PowerVerbReboot          string
	PowerVerbOff             string
	MenuTitle, MenuHelp      string
	Reboot, PowerOff, Cancel string
	Rebooting, PoweringOff   string
	// Audio view
	OneSource, Mix, FollowVideo string
	ProgramStrip                string
	ProgramMix, ProgramSingle   string
	Headphones                  string
	PhonesOff, PhonesMute       string
	PhonesDbF                   string // %d
	PhonesRetrying              string
	PhonesNoAudio               string
	PhonesWaiting               string
	Minus, Plus                 string
	StripFree                   string
	AudioState                  map[string]string
	DelaySuffixF                string // %d ms
	InTheMix, OutOfMix          string
	Sounding, PutOnAir          string
	ZeroDb                      string
	GainF                       string // %+.1f dB
	// Settings: the list of pages (PageTitle by page id) and what every page shares
	Settings                               string
	PageTitle                              map[string]string
	PageUnavailable, PageUnavailableSub    string // an older app without the page's routes
	AppMessageF                            string // %s = the app's error code, when it gives no message
	AskingApp, AppNotAnswering             string
	NetworksButton                         string
	ClientRow, APRow, CamsRow              string
	TwoChannels                            string
	CamsSubOldApp, CamsSubNone             string
	CamsSubTurningOn, CamsSubTurningOff    string
	CamsSubOff, CamsSubAPOff               string
	CamsSubOnPrefix                        string
	CamsViaBoth, CamsViaWifi, CamsViaCable string
	CamsViaNone                            string
	NoCameras, NoCamerasHelp               string
	CamLive                                string
	HowWifiF                               string // %s = ssid
	HowWifiOff, HowURL                     string
	// «Crear mi wifi» row
	Off, TurningOn, TurningOff string
	LegacyOnF                  string // %s = ssid
	LegacyOff                  string
	NobodyConnected            string
	OneDevice                  string
	DevicesF                   string // %d
	ChannelF                   string // %d
	APFailed                   string
	ErrSuffixF                 string // ": %s"
	// «Conectarse a una wifi» row
	ConnectingToF          string // %s
	SignalF                string // %s = SignalGood/…
	SignalGood, SignalMid  string
	SignalWeak             string
	GHzF                   string // %s (2,4 | 5)
	NoInternet             string
	WrongPasswordForF      string // %s
	ConnectFailed          string
	ConnectFailedToSuffixF string // " a %s"
	NotConnected           string
	// QR panel
	QRStarting, QRStartingSub string
	QRError, QRErrorSub       string
	QROff, QROffSub           string
	QRReading                 string
	QRNetwork, QRPassword     string
	QRHelp                    [3]string
	// Renaming «Crear mi wifi» (its button under the QR, and its keyboard)
	SSIDRename, SSIDRenaming string
	SSIDKeyboard, SSIDHelp   string
	SSIDBad                  string
	// Networks list
	NetworksTitle         string
	Searching, NoNetworks string
	ForgetConfirm, Forget string
	NetConnected          string
	NetConnecting         string
	NetWrongPassword      string
	NetFailed             string
	NetSaved              string
	Security              map[string]string
	BandSuffixF           string // %s
	PrevPage, NextPage    string
	ForgetAgain           string
	PageF                 string // %d, %d
	TapNetwork            string
	UnsupportedSecurityF  string // %s ssid, %s security name
	SecurityName          map[string]string
	BusyChange            string
	// Keyboard
	KeyLabel           map[string]string
	ConnectButton      string
	PasswordForF       string // %s = ssid
	TypePassword       string
	CountF             string // %d
	Count64            string
	KeyboardHelp       string
	PasswordShortF     string // %d
	PasswordHex        string
	WrongPasswordAgain string
	// Language page
	LanguageHelp, LanguageInUse string
	// Date & time page
	TimeAuto, TimeAutoSynced, TimeAutoWaiting, TimeAutoOff string
	TimeZoneRow, TimeZoneNone                              string
	UTCOffsetF                                             string // %s = +02:00
	DateF                                                  string // %s weekday, %d day, %s month, %d year
	Weekdays                                               [7]string
	MonthsShort                                            [12]string
	TimeSet, TimeManualHelp, TimeSaved                     string
	ZonesLoading                                           string
	Regions                                                map[string]string
	// Updates page
	UpdVersionF, UpdUpToDate, UpdChecking  string // UpdVersionF: %s
	UpdAvailableF, UpdDownloadingF         string // %s version; UpdDownloadingF: %s version, %d percent
	UpdReady, UpdInstalling, UpdRolledBack string
	UpdErrorF, UpdLastCheckF               string // %s
	UpdNever                               string
	UpdCheck, UpdInstallF, UpdConfirm      string // UpdInstallF: %s version
	UpdStreaming, UpdNotes, UpdSizeF       string // UpdSizeF: %s (MB)
	UpdInstalledF                          string // %s version
	UpdUndoneF, UpdFailedF                 string // %s version, %s the app's message
	UpdTestBuild                           string
	// Sources page
	SourceF                     string // %d = slot
	SrcAuto, SrcAutoSub         string
	SrcAutoNowF                 string // %s = the source in it now
	SrcOff, SrcOffSub           string
	SrcDisconnected, SrcInSlotF string // SrcInSlotF: %d = the slot it is pinned to
	SrcAliasEmpty               string
	SrcSave, SrcSaved           string
	SrcUnsaved, SrcOnAir        string
	SrcEngineOld, SrcAliasBad   string
	SrcDuplicateF               string // %s source name, %d slot
	SrcNoSources                string
	SrcPinned, SrcHelp          string
	KindName                    map[string]string
	// SourceNames: the engine's own sources by their fixed id (it names them in Spanish): the board HDMI
	// (video hdmi, audio i2s) and the test bars. Other sources keep their device's or the user's name.
	SourceNames          map[string]string
	AliasForF, AliasHelp string // AliasForF: %d = slot
	AliasCountF          string // %d
	KeyDone              string
	// First-boot wizard (CONTRACT §2)
	WizTitle, WizStepF                       string // WizStepF: %d step, %d steps
	WizSteps, WizHeads                       map[string]string
	WizBack, WizNext, WizSkip                string
	WizLater, WizFinish                      string
	WizStepDone, WizStepSkipped              string
	WizLanguageHelp                          string
	WizPwText, WizPwButton, WizPwSet         string
	WizPwWebF                                string // %s = the web address
	WizPwCode, WizPwKeyboard, WizPwAgain     string
	WizPwMismatch, WizPwHelp                 string
	WizNameText, WizNameButton, WizNameHostF string // WizNameHostF: %s = hostname.local
	WizNameKeyboard, WizNameHelp, WizNameBad string
	WizCloudText, WizCloudGet, WizCloudHelp  string
	WizCloudExpiresF, WizCloudLinkedF        string // %s time; %s account
	WizCloudLinked, WizCloudOff              string
	WizCloudWaiting, WizCloudURL             string
	WizDoneText, WizNeedsPassword            string
	// Details: why a source, an audio input or the headphones are not live, by the engine's stable code
	// (ENGINE_GO.md §13, the closed list of internal/engine/detail). Arguments go in as {name}; {names} in
	// hung_on_air is the sources on air, named as on the screen. Short: they go on a tile's last line.
	Details map[string]string
	// About page
	AboutRelease, AboutApp, AboutEngine, AboutCommit string
	AboutName, AboutHost, AboutIPs, AboutWeb         string
	AboutUnknown                                     string
	SetupAgain, SetupAgainConfirm, SetupContinue     string
	// --- MIXER.md §21: everything the web configures, on the screen ---
	GroupTitle map[string]string // the Settings list's entries, by group (SettingsGroups)
	TabTitle   map[string]string // the tabs of a group of several pages, by page
	// Shared by the form pages
	Save, SaveApply, Apply  string
	NotSet, TapAgain, Saved string
	SavedRestarting         string // saved and applied: the stream restarts
	SavedStopped            string // saved; not streaming: taken when it starts
	SavedLater              string // saved, not applied: taken when the stream restarts
	PendingApply            string // saved changes not applied yet (the «Apply» button next to it)
	ApplyCuts               string // «Save and apply» while streaming: a few seconds' cut (tap again)
	Unsaved                 string // a draft not saved yet
	ManagedByCloud          string // the destination is set by the EnhancedIRL account: read-only
	// CmdErrors: why the engine refused a page's command, by its code (scene and audio codes)
	CmdErrors map[string]string
	// Each area's own (one file each: strings_<area>.go, with its three tables)
	Stream  StreamStrings  // Stream: Destination, Uplinks, Pause
	Modem   ModemStrings   // a modem's page (Streaming › Connections, MODEMS.md §7)
	Input   InputStrings   // Sources: the Input tab and the slots' Hide
	NetX    NetStrings     // Wi-Fi (channel, new password) and Network cameras (switches, add, a camera's page)
	Cloud   CloudStrings   // EnhancedIRL
	Account AccountStrings // Device: Name & password
	AudioX  AudioStrings   // the audio view's settings: delay and each camera's audio
	Camera  CameraStrings  // Sources: the Image tab (a USB camera's image settings)
	Dests   DestStrings    // Stream: the saved destinations (OUTPUTS.md §11)
}

// Languages are the screen's languages, in the order the Language page lists them.
var Languages = []string{"es", "en", "fr"}

// LanguageNames are the languages by their own names (never translated).
var LanguageNames = map[string]string{"es": "Español", "en": "English", "fr": "Français"}

// DefaultLanguage is the language before the app has said the device's (CONTRACT §1: requested → device → en).
const DefaultLanguage = "en"

// Lang is the table of a language; an unknown code is English.
func Lang(code string) *Strings {
	switch code {
	case "es":
		return Spanish
	case "fr":
		return French
	}
	return English
}

// KnownLanguage: code is one of Languages.
func KnownLanguage(code string) bool { return code == "es" || code == "en" || code == "fr" }

// phonesNoAudioDetail is the engine's detail for a screen without audio (phones.NoAudio): compared, never shown,
// so it is not translated.
const phonesNoAudioDetail = "La pantalla no admite audio"
