package ui

import (
	"image"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// UI is the screen's interface: what the touches change and what is drawn (the Python engine's Screen without
// its pipeline). It is driven from ONE goroutine (the screen's): OnTouch, Tick, SetStatus and CommandDone are
// not safe for concurrent use. Its inputs from other goroutines (Net, Levels, LinkInfo, Sensors) must be.
//
// Secrets (WIFI.md §6): the wifi password typed on the keyboard lives only in the keyboard's buffer, is
// wiped when sent, cancelled, when Settings closes and when the screen locks, is drawn only as dots, and
// never goes to a log, the state, a Command or a text of the drawing.
type UI struct {
	Lay *Layout
	// Tx is the current language's texts: the device language from the app (App), English until it answers
	Tx  *Strings
	Net Net
	// App is the app's device API (nil: the Net's, if it carries one: AppProvider)
	App   App
	Fonts *Fonts // nil: no drawing (the touch tests)
	// Send hands a command to the engine (asynchronously); its result comes back through CommandDone.
	Send func(Command)
	// Changed: the screen's published state changed (a power request).
	Changed func()
	Levels  Levels
	// LinkInfo: the output's transport ("srt" | "srtla") and, with SRTLA, its uplinks (the header words them)
	LinkInfo func() (transport string, links int)
	// Links: the internet connections' dots for the header (the app's links.json, MODEMS.md §6), and whether they
	// can be trusted now; nil or false: the header words the link as before («SRTLA · 2 links»).
	Links func() ([]LinkDot, bool)
	// Sensors: the CPU temperature (°C) and the board's input voltage (V), when known.
	Sensors func() (temp float64, okTemp bool, volts float64, okVolts bool)
	// Legacy: the board's wifi from wifi.json, for an older app without the local API (nil = none).
	Legacy func() *WifiCreds
	// PauseSupported: the program has the Pause layer (else its button is dimmed).
	PauseSupported bool
	// Logo is the header's logo (nil: the name in text).
	Logo image.Image
	// Clock is the monotonic clock (tests move it); Wall is for the power request's time.
	Clock, Wall func() time.Time

	// State is the screen's state: only "on" takes touches.
	State string
	Dirty bool

	st        *Status
	liveSince time.Time

	press        *press
	hold         *hold
	menuOpen     bool
	settingsOpen bool
	linksZone    Rect      // the header's dots, as last drawn: a tap there opens Settings › Streaming › Connections
	settingsPage string    // the page on the right: SettingsPages (the last one stays)
	settingsView string    // main | networks | zones | keyboard
	pageSince    time.Time // when the page was opened: older results are not its own
	listPage     int
	listShown    []string // the network of each row, as drawn: a tap goes to what was seen
	listForget   map[int]bool
	listPages    int
	pick         string // the network under the finger when it went down (the list reorders itself)
	hasPick      bool
	kb           *keyboard
	connecting   *connecting
	confirm      *confirm
	zoneRegion   string   // the zone picker's region
	zonePage     int      // and its page
	zoneShown    []string // the zone of each cell, as drawn
	timeDraft    time.Time
	timeDraftSet bool         // the manual date and time being set (else the device's clock)
	srcDraft     []SlotAssign // the Sources page being edited (nil: the app's)
	srcPick      int          // the slot whose choice is open (the picker)
	candPage     int
	candShown    []string // the choice of each picker row, as drawn
	srcSavedAt   time.Time
	srcNote      string
	srcNoteUntil time.Time
	view         string // video | audio
	powering     string // reboot | poweroff asked: the screen says so until it goes off
	poweringAt   time.Time
	power        *PowerRequest
	powerSeq     int
	hint         string
	hintUntil    time.Time
	secretUntil  time.Time
	// groupPage is the last page seen of each Settings group (its tab): the list's entry goes back to it
	groupPage map[string]string
	// chooser is the generic picker open in the page area (settingsView "pick")
	chooser *picker
	// reveal: the secrets whose eye is open, by their field's key. Closed when the page changes, Settings closes or
	// the screen locks
	reveal map[string]bool
	pnote  pageNote // a page's own notice (pageNoteText)
	// The state of each area's pages (their own files)
	inp  inputState
	camp camsState
	aset audioSetState
	dst  destState
	acct accountState
	// the uplink being switched (its row's switch halfway meanwhile) and to what
	uplinkPend    string
	uplinkPendOff bool

	lastAppLang string    // the device language the app last said (a change there is applied once)
	lastResult  time.Time // the last action result handled (a failed language change goes back)
	// wizardPostponed: «Later» in the wizard: the normal screen until the engine restarts or «Continue»
	wizardPostponed bool
	wiz             wizard
	watch           AppWatch // what was last asked of App.Watch
	watchSet        bool

	r render // the drawing's buffers and caches
}

type press struct {
	hit          Hit
	since, until time.Time // until zero: still down
}

type hold struct {
	key   string
	since time.Time
	done  bool
}

// keyboard is the open keyboard: a secret (the wifi password; the wizard's web password; a stream key…) typed
// into buf, drawn as dots unless its eye is open (show), which ONLY lives here; or a visible text (an alias, the
// device name, the AP's name, a URL…) in text. The modes after the first ones are described by a spec (kbSpec).
type keyboard struct {
	mode   string // wifi | alias | name | ssid | webpw | webpw2 | spec
	ssid   string // wifi: the network
	slot   int    // alias: the slot
	buf    []byte // a secret: PSKMax bytes, or its spec's max
	n      int
	first  [PSKMax]byte // webpw2: the web password typed the first time, to compare
	firstN int
	text   []rune
	max    int // the most characters (runes) of a visible text
	layer  string
	err    string
	show   bool    // the secret's eye is open: its characters are drawn
	spec   *kbSpec // mode spec
}

// kbSpec describes a keyboard of a page (MIXER.md §21): what it is for, what it takes and what OK does.
type kbSpec struct {
	title, help, ok string
	secret          bool
	max             int // the most runes (a text) or bytes (a secret)
	min             int // OK needs at least this many
	layer           string
	// allow says which characters it takes (nil: all of the keyboard's)
	allow func(r rune) bool
	// done is OK with what was typed (text, or the secret's bytes: the callee copies what it keeps, the keyboard is
	// wiped after); it returns the error to show, or "" to close the keyboard
	done func(u *UI, text string, secret []byte) string
	// cancel runs when the keyboard is closed with X (nil: nothing to undo)
	cancel func(u *UI)
}

func (k *keyboard) chars() []byte { return k.buf[:k.n] }

// secret: what is typed is drawn as dots (unless its eye is open) and never kept anywhere else.
func (k *keyboard) secret() bool {
	return k.mode == "wifi" || k.mode == "webpw" || k.mode == "webpw2" || (k.spec != nil && k.spec.secret)
}

func (k *keyboard) wipe() {
	clear(k.buf)
	k.n = 0
	clear(k.first[:])
	k.firstN = 0
	clear(k.text)
	k.text = k.text[:0]
}

type connecting struct {
	ssid  string
	since time.Time
	seen  bool // «conectando» seen: only an error after it counts
}

type confirm struct {
	what, ssid string
	until      time.Time
}

// New is a UI for a layout. tx is the language until the app says the device's (nil = English, CONTRACT
// §1); net nil = no app API.
func New(lay *Layout, tx *Strings, net Net) *UI {
	if tx == nil {
		tx = Lang(DefaultLanguage)
	}
	if net == nil {
		net = noNet{}
	}
	return &UI{Lay: lay, Tx: tx, Net: net, State: "on", Dirty: true, settingsPage: "wifi", settingsView: "main",
		view: "video", listPages: 1, listForget: map[int]bool{}, st: &Status{},
		Clock: time.Now, Wall: time.Now}
}

func (u *UI) now() time.Time { return u.Clock() }

// SetLanguage shows the screen in a language (an unknown code is English). The keyboard's cached keys carry
// labels: they are drawn again.
func (u *UI) SetLanguage(code string) {
	tx := Lang(code)
	if tx == u.Tx {
		return
	}
	u.Tx = tx
	u.r.kbBase = nil
	u.Dirty = true
}

// syncApp follows the app: the device language when it changes there (on the web, or by our own PUT), and what
// the screen shows, so the app's client asks only for that.
func (u *UI) syncApp() {
	a := u.app()
	snap := a.AppSnapshot()
	if d := snap.Device; d != nil && d.Language != u.lastAppLang {
		u.lastAppLang = d.Language
		if KnownLanguage(d.Language) {
			u.SetLanguage(d.Language)
		}
	}
	u.languageResult(&snap)
	u.wizardSync(&snap)
	if w := u.appWatch(); !u.watchSet || w != u.watch {
		u.watch, u.watchSet = w, true
		a.Watch(w)
	}
}

// appWatch is what the screen needs from the app now.
func (u *UI) appWatch() AppWatch {
	page := ""
	if u.settingsOpen {
		page = u.settingsPage
	}
	wiz := u.wizardOn()
	w := AppWatch{Device: u.settingsOpen || wiz, Setup: wiz, Update: page == "updates", Slots: page == "sources",
		Zones: (u.settingsOpen || wiz) && u.settingsView == "zones", About: page == "about" || wiz,
		Cloud: wiz && u.wizardStep() == "cloud"}
	if p := pageDefs[page]; p != nil && p.watch != nil && !wiz {
		p.watch(u, &w)
	}
	return w
}

// SetStatus takes the engine's newly published state (redraws on the next tick).
func (u *UI) SetStatus(st *Status) {
	if st == nil {
		st = &Status{}
	}
	on := st.Output.State == "on"
	if on && u.liveSince.IsZero() {
		u.liveSince = u.now()
	} else if !on {
		u.liveSince = time.Time{}
	}
	u.st = st
	u.Dirty = true
}

// SetLiveSince overrides when the broadcast started (ui-render).
func (u *UI) SetLiveSince(t time.Time) { u.liveSince = t }

func (u *UI) locked() bool { return u.st.Screen.Locked }

// Power is the pending power request, for the engine's state (screen.power), or nil.
func (u *UI) Power() *PowerRequest {
	if u.power == nil {
		return nil
	}
	p := *u.power
	return &p
}

// SecretOnScreen: Settings open, or closed less than SecretGrace ago (the compositor may be a few images
// behind the UI layer): screen.jpg is not updated.
func (u *UI) SecretOnScreen() bool {
	return u.settingsOpen || u.wizardOn() || u.now().Before(u.secretUntil)
}

func (u *UI) say(text string, d time.Duration) {
	u.hint, u.hintUntil = text, u.now().Add(d)
	u.Dirty = true
}

func (u *UI) send(origin string, msg map[string]any, x, y int) {
	if u.Send != nil {
		u.Send(Command{Msg: msg, Origin: origin, X: x, Y: y})
	}
}

// CommandDone is a command's result from the engine.
func (u *UI) CommandDone(c Command, ok bool, code string) {
	if ok {
		return
	}
	cmd, _ := c.Msg["cmd"].(string)
	switch c.Origin {
	case "hold":
		u.say(sprintf(u.Tx.CannotF, code), 3*time.Second)
	case "page": // a Settings page's change: the page says it could not
		u.notePage(sprintf(u.Tx.CannotF, u.commandError(code)), true)
	case originAudioSet: // the audio view's settings: that view says it could not
		u.audioSetRefused(c, code)
	default:
		elog.Event("mixer", "touch_command_failed", "x", c.X, "y", c.Y, "cmd", cmd, "error", code)
	}
}

// commandError is an engine refusal's code in words (the code itself if it has none).
func (u *UI) commandError(code string) string {
	if t, ok := u.Tx.CmdErrors[code]; ok {
		return t
	}
	return code
}

// OnTouch is a finger going down ("down") or up ("up", after held) at (fx, fy), in fractions of the screen.
// Emit and Unlock are held (Tick does them); the rest acts on release: a tap, or a long press (to the PiP).
// Locked, only Unlock answers.
func (u *UI) OnTouch(ev string, fx, fy float64, held time.Duration) {
	lay := u.Lay
	if lay == nil || u.State != "on" {
		return
	}
	now := u.now()
	x, y := int(fx*float64(lay.DW)), int(fy*float64(lay.DH))
	if (u.settingsOpen || u.wizardOn()) && u.settingsView == "keyboard" {
		u.Dirty = true
		if u.powering == "" {
			u.kbTouch(ev, x, y, held)
		}
		return
	}
	var keys []string
	if u.wizardOn() {
		snap := u.Net.Snapshot()
		keys = u.wizardKeys(&snap)
	} else if u.settingsOpen {
		snap := u.Net.Snapshot()
		keys = u.settingsKeys(&snap)
	}
	h := lay.Hit(x, y, u.menuOpen, u.hitView(), u.settingsOpen || u.wizardOn(), keys)
	if h.Key == "a_follow" && u.st.audioMode() == "mix" {
		h = Hit{} // «Sigue al vídeo» is not shown in the mix
	}
	u.Dirty = true
	if u.powering != "" {
		return
	}
	if u.settingsOpen || u.wizardOn() {
		u.settingsTouch(ev, h, x, y, now)
		return
	}
	if ev == "down" {
		// What is touched is seen highlighted while the finger is on it (if it answers: locked, only the lock)
		if h.Kind != HitNone && !(h.Kind == HitButton && strings.HasPrefix(h.Key, "a_fader_")) &&
			(!u.locked() || h.Key == "lock") {
			u.press = &press{hit: h, since: now}
		}
		if key := HoldKey(h, u.locked(), u.menuOpen); key != "" {
			u.hold = &hold{key: key, since: now}
		} else if u.locked() && !u.menuOpen {
			u.say(u.Tx.LockedHint, 3*time.Second)
		}
		return
	}
	if u.press != nil && u.press.until.IsZero() {
		u.press.until = now
	}
	hd := u.hold
	u.hold = nil
	if hd != nil {
		if !hd.done {
			u.say(sprintf(u.Tx.HoldForF, int(HoldTime(hd.key)/time.Second)), 3*time.Second)
		}
		return
	}
	if u.menuOpen {
		if h.Key == "m_cancel" || h.Kind == HitNone {
			u.menuOpen = false
		}
		return
	}
	if u.locked() {
		return
	}
	if h.Kind == HitButton && h.Key == "power" {
		u.menuOpen = true
		return
	}
	if h.Kind == HitButton && h.Key == "settings" {
		u.openSettings(true)
		return
	}
	if h.Kind == HitNone && u.linksZone.W > 0 && u.linksZone.Inside(x, y, 4) { // the header's dots → their tab
		u.openSettings(true)
		u.openPage("uplinks")
		return
	}
	if u.view == "audioset" { // the audio view's settings (audioset.go)
		u.audioSetTouch(h.Key, held, x, y)
		return
	}
	if u.view == "audio" {
		if h.Key == "a_back" {
			u.view = "video"
			return
		}
		if h.Key == "a_settings" {
			u.openAudioSet()
			return
		}
		if msg := AudioCommand(h.Key, y, u.st.Audio, lay); msg != nil {
			u.send("audio", msg, x, y)
		}
		return
	}
	if h.Kind == HitButton && h.Key == "audio" {
		u.view = "audio"
		return
	}
	u.fromTouch(h, held >= TapLong, x, y)
}

// fromTouch is the engine's from_touch: the command of a tap, built from the last published scene.
func (u *UI) fromTouch(h Hit, long bool, x, y int) {
	ids := make([]string, u.Lay.N)
	for _, s := range u.st.Sources {
		if s.Slot >= 1 && s.Slot <= len(ids) {
			ids[s.Slot-1] = s.ID
		}
	}
	msg := TouchCommand(h, long, u.st, ids)
	if msg == nil {
		what := "outside"
		if h.Kind == HitButton {
			what = h.Key
		}
		elog.Event("mixer", "touch_no_effect", "x", x, "y", y, "what", what)
		return
	}
	u.send("touch", msg, x, y)
}

func (u *UI) settingsTouch(ev string, h Hit, x, y int, now time.Time) {
	if ev == "down" {
		if h.Kind != HitNone {
			u.press = &press{hit: h, since: now}
		}
		// The network of the row under the finger, now: until it is lifted, the list may reorder itself
		u.pick, u.hasPick = "", false
		if i := rowIndex(h.Key); i >= 0 && i < len(u.listShown) {
			u.pick, u.hasPick = u.listShown[i], true
		}
		if s, ok := strings.CutPrefix(h.Key, "s_zone_"); ok {
			if i, err := strconv.Atoi(s); err == nil && i < len(u.zoneShown) {
				u.pick, u.hasPick = u.zoneShown[i], true
			}
		}
		if s, ok := strings.CutPrefix(h.Key, "s_cand_"); ok {
			if i, err := strconv.Atoi(s); err == nil && i < len(u.candShown) {
				u.pick, u.hasPick = u.candShown[i], true
			}
		}
		if s, ok := strings.CutPrefix(h.Key, "s_pick_"); ok && u.chooser != nil {
			if i, err := strconv.Atoi(s); err == nil && i < len(u.chooser.shown) {
				u.pick, u.hasPick = u.chooser.shown[i], true
			}
		}
		return
	}
	p := u.press
	down := ""
	if p != nil && p.hit.Kind == HitButton && p.until.IsZero() {
		down = p.hit.Key
	}
	if p != nil && p.until.IsZero() {
		p.until = now
	}
	pick, hasPick := u.pick, u.hasPick
	u.pick, u.hasPick = "", false
	what := h.Key
	if what != down {
		// Released on something else than where it went down: the rows move by themselves (the client row
		// appears, the list reorders), and a switch that was not touched never counts
		return
	}
	if u.wizardOn() {
		if what != "" {
			u.wizardTap(what, pick, hasPick)
		}
		return
	}
	switch {
	case what == "s_close" || (what == "" && !u.Lay.SettingsPanel.Inside(x, y, 0)):
		u.openSettings(false)
	case strings.HasPrefix(what, "s_page_"):
		u.openGroup(what[len("s_page_"):])
	case strings.HasPrefix(what, "s_tab_"):
		u.openPage(what[len("s_tab_"):])
	default:
		u.pageTouch(what, pick, hasPick)
	}
}

// pageTouch is a tap on a page's button (Settings, and the wizard's pages).
func (u *UI) pageTouch(what, pick string, hasPick bool) {
	if u.settingsView == "pick" {
		u.pickerTouch(what, pick, hasPick)
		return
	}
	if p := pageDefs[u.settingsPage]; p != nil && p.touch != nil && !u.wizardOn() {
		if p.touch(u, what) {
			u.Dirty = true
			return
		}
	}
	if u.srcTouch(what, pick, hasPick) {
		return
	}
	switch {
	case what == "s_net":
		u.toggleNet()
	case what == "s_ap":
		u.toggleAP()
	case what == "s_client":
		u.toggleClient()
	case what == "s_networks":
		u.openNetworks()
	case what == "s_ssid":
		u.renameAP()
	case what == wifiPwEye:
		u.toggleWifiPw()
	case what == "s_wifi_ch":
		u.openChannelPicker()
	case what == "s_wifi_rotate":
		u.rotateWifi()
	case what == "s_back" && u.settingsView == "zones":
		u.settingsView = "main"
	case what == "s_back":
		u.closeNetworks()
	case (what == "s_prev" || what == "s_next") && u.settingsView == "zones":
		if what == "s_next" {
			u.zonePage++
		} else {
			u.zonePage = max(0, u.zonePage-1)
		}
	case what == "s_prev" || what == "s_next":
		if what == "s_next" {
			u.listPage++
		} else {
			u.listPage = max(0, u.listPage-1)
		}
	case strings.HasPrefix(what, "s_row_") && hasPick:
		u.pickNetwork(pick)
	case strings.HasPrefix(what, "s_forget_") && hasPick:
		u.forgetNetwork(pick)
	case strings.HasPrefix(what, "s_lang_"):
		u.chooseLanguage(what[len("s_lang_"):])
	case what == "s_ntp":
		u.toggleNTP()
	case what == "s_zone":
		u.openZones()
	case strings.HasPrefix(what, "s_region_"):
		if i, err := strconv.Atoi(what[len("s_region_"):]); err == nil && i < len(Regions) {
			u.zoneRegion, u.zonePage = Regions[i], 0
		}
	case strings.HasPrefix(what, "s_zone_") && hasPick:
		u.chooseZone(pick)
	case strings.HasPrefix(what, "s_t_") && what != "s_t_set":
		u.stepTime(what)
	case what == "s_t_set":
		u.setTime()
	case what == "s_up_check":
		u.updateCheck()
	case what == "s_up_install":
		u.updateInstall()
	case what == "s_setup_again":
		u.setupAgain()
	}
}

// rowIndex is i of s_row_i / s_forget_i, or -1.
func rowIndex(key string) int {
	for _, p := range []string{"s_row_", "s_forget_"} {
		if s, ok := strings.CutPrefix(key, p); ok {
			if i, err := strconv.Atoi(s); err == nil {
				return i
			}
		}
	}
	return -1
}

func (u *UI) openSettings(on bool) {
	if u.settingsOpen && !on {
		u.secretUntil = u.now().Add(SecretGrace)
	}
	u.settingsOpen = on
	if !on { // closed: what was typed out of memory, and back to the page
		u.kbClear()
		u.leavePage(u.settingsPage)
		u.settingsView, u.confirm, u.chooser = "main", nil, nil
		u.pick, u.hasPick = "", false
		u.timeDraftSet, u.srcDraft = false, nil
		clear(u.reveal)
	} else {
		u.pageSince = u.now()
	}
	u.Net.SetPanel(on)
	u.Dirty = true
}

// openGroup shows a Settings group (the list's entry): the page of it last seen, else its first.
func (u *UI) openGroup(id string) {
	g := groupByID(id)
	if g == nil {
		return
	}
	page := g.Pages[0]
	if p, ok := u.groupPage[id]; ok {
		page = p
	}
	if cur := groupOf(u.settingsPage); cur != nil && cur.ID == id {
		page = u.settingsPage // its own entry again: back to the page's main view (out of a sub-view)
	}
	u.openPage(page)
}

// openPage shows a Settings page (a list's entry or a tab).
func (u *UI) openPage(page string) {
	if page == u.settingsPage && u.settingsView == "main" {
		return
	}
	if u.settingsView == "networks" {
		u.Net.SetScan(false)
	}
	if page != u.settingsPage {
		u.leavePage(u.settingsPage)
		clear(u.reveal)
	}
	u.settingsPage, u.settingsView, u.confirm, u.chooser = page, "main", nil, nil
	u.pageSince, u.timeDraftSet, u.srcDraft = u.now(), false, nil
	if g := groupOf(page); g != nil {
		if u.groupPage == nil {
			u.groupPage = map[string]string{}
		}
		u.groupPage[g.ID] = page
	}
	u.Dirty = true
}

// revealed: the secret of a field's eye is shown.
func (u *UI) revealed(key string) bool { return u.reveal[key] }

// toggleReveal opens or closes a secret's eye.
func (u *UI) toggleReveal(key string) bool {
	if u.reveal == nil {
		u.reveal = map[string]bool{}
	}
	u.reveal[key] = !u.reveal[key]
	u.Dirty = true
	return u.reveal[key]
}

func (u *UI) kbClear() {
	if u.kb != nil {
		u.kb.wipe()
	}
	u.kb = nil
}

func (u *UI) openNetworks() {
	u.settingsView, u.listPage, u.confirm = "networks", 0, nil
	u.Net.SetScan(true) // scans now and every 2.5 s while it is open
	u.Dirty = true
}

func (u *UI) closeNetworks() {
	u.settingsView, u.confirm = "main", nil
	u.Net.SetScan(false)
	u.Dirty = true
}

func (u *UI) openKeyboard(ssid, err string) {
	u.kbClear()
	u.kb = &keyboard{mode: "wifi", ssid: ssid, layer: "lower", err: err, buf: make([]byte, PSKMax)}
	u.settingsView = "keyboard"
	u.Net.SetScan(false) // while typing the list is not needed
	u.Dirty = true
}

// openTextKeyboard opens the keyboard for a visible text (an alias, the device name), with what it has now.
func (u *UI) openTextKeyboard(mode string, slot int, text string, maxLen int) {
	u.kbClear()
	r := []rune(text)
	u.kb = &keyboard{mode: mode, slot: slot, text: r[:min(len(r), maxLen)], max: maxLen, layer: "lower",
		buf: make([]byte, PSKMax)}
	u.settingsView = "keyboard"
	u.Dirty = true
}

// openSpecKeyboard opens a page's keyboard: a visible text starts with text; a secret, empty.
func (u *UI) openSpecKeyboard(sp *kbSpec, text string) {
	u.kbClear()
	layer := sp.layer
	if layer == "" {
		layer = "lower"
	}
	kb := &keyboard{mode: "spec", spec: sp, max: sp.max, layer: layer}
	if sp.secret {
		kb.buf = make([]byte, sp.max)
	} else {
		r := []rune(text)
		kb.text = r[:min(len(r), sp.max)]
	}
	u.kb = kb
	u.settingsView = "keyboard"
	u.Dirty = true
}

func (u *UI) closeKeyboard() {
	mode := ""
	if u.kb != nil {
		mode = u.kb.mode
	}
	u.kbClear()
	if mode == "wifi" {
		u.openNetworks()
		return
	}
	u.settingsView = "main"
	u.Dirty = true
}

// kbTouch: a key lights up when the finger goes down and acts when it is lifted (the key it went down on).
func (u *UI) kbTouch(ev string, x, y int, held time.Duration) {
	if ev == "down" {
		key := ""
		if u.kb != nil {
			key = u.Lay.KBHit(x, y, u.kb.layer)
			if key == "k_eye" && !u.kb.secret() {
				key = ""
			}
		}
		u.press = nil
		if key != "" {
			u.press = &press{hit: Hit{Kind: HitKey, Key: key}, since: u.now()}
		}
		return
	}
	p := u.press
	if p == nil || p.hit.Kind != HitKey {
		return
	}
	if p.until.IsZero() {
		p.until = u.now()
	}
	u.kbKey(p.hit.Key, held)
}

func (u *UI) kbKey(key string, held time.Duration) {
	kb := u.kb
	if kb == nil || key == "" {
		return
	}
	switch key {
	case "k_cancel":
		if kb.spec != nil && kb.spec.cancel != nil {
			kb.spec.cancel(u)
		}
		u.closeKeyboard()
	case "k_eye":
		kb.show = !kb.show
	case "SHIFT":
		if kb.layer == "lower" {
			kb.layer = "upper"
		} else {
			kb.layer = "lower"
		}
	case "SYM":
		kb.layer = "sym1"
	case "SYM2":
		kb.layer = "sym2"
	case "ABC":
		kb.layer = "lower"
	case "BKSP":
		switch {
		case held >= TapLong: // held: all of it
			kb.wipe()
		case kb.secret() && kb.n > 0:
			kb.n--
			kb.buf[kb.n] = 0
		case !kb.secret() && len(kb.text) > 0:
			kb.text[len(kb.text)-1] = 0
			kb.text = kb.text[:len(kb.text)-1]
		}
	case "OK":
		u.kbSend()
	default:
		if len(key) != 1 {
			return
		}
		if kb.spec != nil && kb.spec.allow != nil && !kb.spec.allow(rune(key[0])) {
			return
		}
		switch {
		case kb.secret() && kb.n < len(kb.buf):
			kb.buf[kb.n] = key[0]
			kb.n++
			kb.err = ""
		case !kb.secret() && len(kb.text) < kb.max:
			kb.text = append(kb.text, rune(key[0]))
			kb.err = ""
		}
	}
}

// kbSend is «OK»: each mode's.
func (u *UI) kbSend() {
	switch u.kb.mode {
	case "spec":
		kb := u.kb
		if ok, _ := u.kbReady(); !ok || kb.spec.done == nil {
			return
		}
		if e := kb.spec.done(u, strings.TrimSpace(string(kb.text)), kb.chars()); e != "" {
			kb.err = e
			return
		}
		if u.kb == kb { // done may have opened another keyboard (the next step): that one stays
			u.closeKeyboard()
		}
	case "alias":
		u.aliasDone()
	case "wifi":
		u.wifiSend()
	case "ssid":
		u.ssidDone()
	default:
		u.wizardKeyboardDone()
	}
}

// wifiSend: the password goes straight to the app (Net) and out of here.
func (u *UI) wifiSend() {
	kb := u.kb
	if !PSKOK(kb.chars()) {
		if kb.n < PSKMin {
			kb.err = sprintf(u.Tx.PasswordShortF, PSKMin)
		} else {
			kb.err = u.Tx.PasswordHex
		}
		return
	}
	if !u.Net.Connect(kb.ssid, kb.chars()) {
		kb.err = u.busyText()
		return
	}
	u.connecting = &connecting{ssid: kb.ssid, since: u.now()}
	u.kbClear()
	u.settingsView, u.settingsPage = "main", "wifi"
}

// renameAP opens the keyboard on the name of «Crear mi wifi» (Settings → Wi-Fi, and the wizard's network step).
func (u *UI) renameAP() {
	snap := u.Net.Snapshot()
	if !SSIDEditable(&snap) {
		return
	}
	if snap.Pending != nil || u.apBusy() { // another change under way: it says so instead of a keyboard that could not send
		u.busySay(false)
		return
	}
	u.openTextKeyboard("ssid", 0, snap.Wifi.SSID, SSIDMax)
}

// ssidDone is the AP name keyboard's «Done»: trimmed and checked, then to the app (PUT /network/ap). The same name
// changes nothing; the app may still refuse it (a camera on air over the Wi-Fi): its message shows on the page.
func (u *UI) ssidDone() {
	kb := u.kb
	name := strings.TrimSpace(string(kb.text))
	if !SSIDOK(name) {
		kb.err = u.Tx.SSIDBad
		return
	}
	if w := u.Net.Snapshot().Wifi; w != nil && w.SSID == name {
		u.closeKeyboard()
		return
	}
	if !u.Net.SetSSID(name) {
		kb.err = u.busyText()
		return
	}
	u.closeKeyboard()
}

func (u *UI) busyText() string {
	if u.Net.Snapshot().Status != nil {
		return u.Tx.BusyChange
	}
	return u.Tx.AppNotAnswering
}

// busySay: a change the app cannot take now says why (another one under way, or the network state unknown).
func (u *UI) busySay(ok bool) bool {
	if !ok {
		u.Net.Say(u.busyText())
	}
	return ok
}

// pickNetwork: a network of the list. Connected: nothing; not supported: says so; open or known: connects
// now (the saved profile); else the keyboard.
func (u *UI) pickNetwork(ssid string) {
	snap := u.Net.Snapshot()
	var n *ScanNet
	for i := range snap.Scan {
		if snap.Scan[i].SSID == ssid {
			n = &snap.Scan[i]
			break
		}
	}
	if n == nil || n.Connected {
		return
	}
	cl := snap.Client
	if cl == nil {
		cl = &WifiClient{}
	}
	switch {
	case n.Security == "wep" || n.Security == "wpa3" || n.Security == "enterprise":
		u.Net.Say(sprintf(u.Tx.UnsupportedSecurityF, ssid, u.Tx.SecurityName[n.Security]))
	case (n.Known && !(cl.SSID == ssid && cl.Error == "wrong_password")) || n.Security == "open":
		// Known (the saved profile) or open; if its saved password just failed, the keyboard
		if u.busySay(u.Net.Connect(ssid, nil)) {
			u.connecting = &connecting{ssid: ssid, since: u.now()}
			u.closeNetworks()
		}
	default:
		u.openKeyboard(ssid, "")
	}
}

// forgetNetwork: forget a known network (its saved password), with a second tap within ConfirmFor.
func (u *UI) forgetNetwork(ssid string) {
	now := u.now()
	if u.confirm != nil && u.confirm.what == "forget" && u.confirm.ssid == ssid && now.Before(u.confirm.until) {
		u.confirm = nil
		u.busySay(u.Net.Forget(ssid))
		return
	}
	u.confirm = &confirm{what: "forget", ssid: ssid, until: now.Add(ConfirmFor)}
}

func (u *UI) toggleNet() {
	snap := u.Net.Snapshot()
	if NetSwitch(&snap) {
		u.busySay(u.Net.SetEnabled(!snap.Status.Enabled))
	}
}

func (u *UI) toggleAP() {
	snap := u.Net.Snapshot()
	if APSwitch(&snap) {
		u.busySay(!strings.HasPrefix(u.app().AppSnapshot().Pending, "ap_") &&
			u.Net.SetAP(snap.Status.AccessPoint.Policy != "always"))
	}
}

func (u *UI) toggleClient() {
	snap := u.Net.Snapshot()
	if ClientSwitch(&snap) {
		u.busySay(u.Net.SetClient(!snap.Client.Enabled))
	}
}

// watchConnect: after connecting with a password, if the app says wrong_password for that network
// (NetworkManager takes ~25 s), the keyboard opens again empty with «Contraseña incorrecta» (if Settings is
// still open). Only an error seen after «conectando» counts: before, the previous attempt's may linger.
func (u *UI) watchConnect(now time.Time) {
	c := u.connecting
	snap := u.Net.Snapshot()
	cl := snap.Client
	if cl == nil {
		cl = &WifiClient{}
	}
	lp := snap.LastPost
	posted := lp != nil && lp.What == "connect" && lp.SSID == c.ssid && !lp.At.Before(c.since)
	if posted && !lp.OK {
		u.connecting = nil // the app did not take it (404, 409…): its message shows; the old error does not count
		return
	}
	switch {
	case cl.SSID == c.ssid && cl.State == "connecting":
		c.seen = true
	case cl.SSID == c.ssid && cl.State == "connected":
		u.connecting = nil
	case cl.SSID == c.ssid && cl.State == "error" && (c.seen || (posted && now.Sub(lp.At) > 5*time.Second)):
		u.connecting = nil
		if cl.Error == "wrong_password" && u.settingsOpen && u.settingsView != "keyboard" {
			u.openKeyboard(c.ssid, u.Tx.WrongPasswordAgain)
		}
	case now.Sub(c.since) > 90*time.Second:
		u.connecting = nil
	}
}

// settingsKeys: the Settings buttons on view that answer, in the order they are tried (Hit).
func (u *UI) settingsKeys(n *NetSnapshot) []string {
	keys := []string{"s_close"}
	switch u.settingsView {
	case "networks": // the list, as drawn
		keys = append(keys, "s_back")
		for i := range u.listShown {
			if u.listForget[i] {
				keys = append(keys, "s_forget_"+strconv.Itoa(i))
			}
			keys = append(keys, "s_row_"+strconv.Itoa(i))
		}
		if u.listPage > 0 {
			keys = append(keys, "s_prev")
		}
		if u.listPage < u.listPages-1 {
			keys = append(keys, "s_next")
		}
		return keys
	case "zones":
		return append(keys, u.zoneKeys()...)
	case "cands":
		return append(keys, u.candKeys()...)
	case "pick":
		return append(keys, u.pickerKeys()...)
	}
	if p := pageDefs[u.settingsPage]; p != nil && p.title != nil {
		if _, _, sub := p.title(u); sub { // a page's own sub-view: only its buttons (its ← among them)
			app := u.app().AppSnapshot()
			return append(keys, p.keys(u, &app)...)
		}
	}
	for _, g := range SettingsGroups {
		keys = append(keys, "s_page_"+g.ID)
	}
	if g := groupOf(u.settingsPage); g != nil && len(g.Pages) > 1 {
		for _, p := range g.Pages {
			keys = append(keys, "s_tab_"+p)
		}
	}
	return append(keys, u.pageKeys(u.settingsPage, n)...)
}

// pageKeys are a page's buttons on view (Settings, and the wizard's pages).
func (u *UI) pageKeys(page string, n *NetSnapshot) []string {
	var keys []string
	app := u.app().AppSnapshot()
	if p := pageDefs[page]; p != nil {
		if p.keys != nil {
			keys = p.keys(u, &app)
		}
		return keys
	}
	switch page {
	case "wifi":
		u.Lay.SettingsRows(ClientSwitch(n))
		if ClientSwitch(n) {
			if n.Client.Enabled {
				keys = append(keys, "s_networks")
			}
			keys = append(keys, "s_client")
		}
		if APSwitch(n) {
			keys = append(keys, "s_ap")
		}
		if SSIDEditable(n) {
			keys = append(keys, "s_ssid")
		}
		legacy := u.legacyCreds(n)
		if u.wifiShown(n, legacy) {
			keys = append(keys, wifiPwEye)
		}
		if u.wifiExtras(n, legacy) {
			keys = append(keys, "s_wifi_ch", "s_wifi_rotate")
		}
	case "language":
		if app.DeviceAPI == Yes && app.Device != nil {
			for _, c := range Languages {
				keys = append(keys, "s_lang_"+c)
			}
		}
	case "time":
		if app.DeviceAPI == Yes && app.Device != nil {
			keys = append(keys, "s_ntp", "s_zone")
			if !app.Device.NTP {
				for _, f := range TimeFields {
					keys = append(keys, "s_t_"+f+"_m", "s_t_"+f+"_p")
				}
				keys = append(keys, "s_t_set")
			}
		}
	case "sources":
		keys = append(keys, u.sourcesKeys(&app)...)
	case "updates":
		if app.UpdateAPI == Yes && app.Update != nil {
			keys = append(keys, "s_up_check")
			if u.installShown(&app) {
				keys = append(keys, "s_up_install")
			}
		}
	case "about":
		if app.SetupAPI == Yes && app.DeviceAPI == Yes && app.Device != nil {
			keys = append(keys, "s_setup_again")
		}
	}
	return keys
}

// zoneKeys are the zone picker's buttons on view.
func (u *UI) zoneKeys() []string {
	keys := []string{"s_back"}
	if u.app().AppSnapshot().Zones == nil {
		return keys
	}
	for i := range Regions {
		keys = append(keys, "s_region_"+strconv.Itoa(i))
	}
	for i := range u.zoneShown {
		keys = append(keys, "s_zone_"+strconv.Itoa(i))
	}
	keys = append(keys, "s_prev", "s_next")
	return keys
}

// fromHold is a held button done: Emit ↔ Stop, Unlock, Reboot or Power off.
func (u *UI) fromHold(key string, now time.Time) {
	switch key {
	case "unlock":
		u.send("hold", map[string]any{"cmd": "lock", "on": false}, 0, 0)
	case "reboot", "poweroff":
		u.requestPower(key, now)
	default:
		u.send("hold", map[string]any{"cmd": "output", "on": u.st.Output.Mode != "on", "by": "screen"}, 0, 0)
	}
}

// requestPower: the engine has no permission to reboot or power off; it publishes the request (screen.power,
// with its time) and the app, which has the exact sudoers rule, does it.
func (u *UI) requestPower(want string, now time.Time) {
	u.powerSeq++
	at := float64(u.Wall().UnixMilli()) / 1000
	u.power = &PowerRequest{Want: want, Seq: u.powerSeq, At: at}
	elog.Event("mixer", "power_requested", "want", want, "seq", u.powerSeq)
	u.powering, u.menuOpen, u.poweringAt = want, false, now
	if u.Changed != nil {
		u.Changed()
	}
}

// Tick runs every MeterEvery (and right after a touch): the held button, the timeouts, and the drawing.
// True when Image() has a new image to push.
func (u *UI) Tick() bool {
	now := u.now()
	u.syncApp()
	hd := u.hold
	if hd != nil && !hd.done && now.Sub(hd.since) >= HoldTime(hd.key) {
		hd.done = true
		u.fromHold(hd.key, now)
	}
	if u.powering != "" && now.Sub(u.poweringAt) > PowerWait {
		// The app has not done it (down, no permission…): the screen cannot stay dead
		want := u.powering
		u.powering = ""
		elog.Event("mixer", "power_timeout", "want", want, "s", int(PowerWait/time.Second))
		u.power = nil
		if u.Changed != nil {
			u.Changed()
		}
		verb := u.Tx.PowerVerbOff
		if want == "reboot" {
			verb = u.Tx.PowerVerbReboot
		}
		u.say(sprintf(u.Tx.PowerFailedF, verb), 6*time.Second)
	}
	if u.menuOpen && u.locked() {
		u.menuOpen = false // locked from the web with the menu open: it closes
		u.Dirty = true
	}
	if u.settingsOpen && (u.locked() || u.State != "on") {
		// Locked from the web, or the screen unplugged or in another mode, with Settings open: it closes (and
		// the keyboard, wiped: what was typed does not wait for the screen to come back)
		u.openSettings(false)
	}
	if u.connecting != nil {
		u.watchConnect(now)
	}
	if u.confirm != nil && !now.Before(u.confirm.until) {
		u.confirm = nil // no second tap: back to how it was
		u.Dirty = true
	}
	if p := u.press; p != nil {
		var gone bool
		if !p.until.IsZero() {
			gone = !now.Before(maxTime(p.until, p.since.Add(PressMin)))
		} else {
			gone = now.Sub(p.since) > PressStale
		}
		if gone {
			u.press = nil // released (or no "up" in 30 s): the highlight goes
			u.Dirty = true
		}
	}
	return u.frame(now, hd)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
