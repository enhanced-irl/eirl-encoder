package ui

import (
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The first-boot wizard (CONTRACT §2): language → time zone and clock → network → web password → name → link to
// EnhancedIRL → done. It shows instead of the normal screen while the app says the setup is not done (GET /setup),
// never with an older app (no /setup) and never while the screen is locked; the engine keeps running behind it.
// From the second step on, «Later» goes back to the normal screen (the streaming controls) until the engine
// restarts or «Continue the setup» (Settings → About); the language and the web password are needed to finish.
// It reuses Settings' pages and keyboard in the same places: the steps are the list on the left, the step's page
// is on the right, and its top bar replaces the header.

// WizardSteps are the wizard's steps; all but the last are the app's step ids (POST /setup/step).
var WizardSteps = []string{"language", "time", "network", "password", "name", "cloud", "done"}

// wizardSkippable: the steps with «Skip».
var wizardSkippable = map[string]bool{"network": true, "cloud": true}

// NameMax is the longest device name (CONTRACT §2).
const NameMax = 40

// wizard is the wizard's own state (the pages' state is Settings', shared).
type wizard struct {
	step    int
	started bool
	panel   bool // the network step told Net its panel is open
}

// wizardOn: the wizard is on the screen.
func (u *UI) wizardOn() bool { return u.wiz.started }

// wizardWanted: the app says the setup is not done, and nothing hides the wizard.
func (u *UI) wizardWanted(app *AppSnapshot) bool {
	return app.SetupAPI == Yes && app.Setup != nil && !app.Setup.Done && !u.wizardPostponed && !u.locked() &&
		u.State == "on" && u.powering == ""
}

// wizardSync shows or hides the wizard as the app's setup state says (each Tick).
func (u *UI) wizardSync(app *AppSnapshot) {
	want := u.wizardWanted(app)
	switch {
	case want && !u.wiz.started:
		u.openSettings(false)
		u.menuOpen = false
		u.wiz = wizard{started: true, step: firstTodo(app.Setup)}
		u.settingsView, u.confirm, u.pageSince = "main", nil, u.now()
		u.Dirty = true
	case !want && u.wiz.started:
		u.wizardClose()
	}
	if u.wiz.started {
		// The network step needs the app's wifi state at the panel's pace, with the credentials to join
		panel := u.wizardStep() == "network"
		if panel != u.wiz.panel {
			u.wiz.panel = panel
			u.Net.SetPanel(panel)
			if !panel && u.settingsView == "networks" {
				u.closeNetworks()
			}
		}
	}
}

// wizardClose hides the wizard: what was typed out of memory, the wifi panel closed.
func (u *UI) wizardClose() {
	if u.wiz.panel {
		u.Net.SetPanel(false)
	}
	u.kbClear()
	u.wiz = wizard{}
	u.settingsView, u.confirm = "main", nil
	u.secretUntil = u.now().Add(SecretGrace)
	u.Dirty = true
}

// firstTodo is where a wizard starts: its first step still to do (the setup restarted keeps what was done).
func firstTodo(s *SetupInfo) int {
	for i, id := range WizardSteps[:len(WizardSteps)-1] {
		if st := s.StepState(id); st == "todo" || st == "" {
			return i
		}
	}
	return len(WizardSteps) - 1
}

func (u *UI) wizardStep() string { return WizardSteps[max(0, min(u.wiz.step, len(WizardSteps)-1))] }

// wizardCanSkip: the step has «Skip» (not the link once linked: there is nothing left to skip).
func (u *UI) wizardCanSkip(app *AppSnapshot) bool {
	step := u.wizardStep()
	return wizardSkippable[step] && !(step == "cloud" && app.Cloud != nil && app.Cloud.State == "linked")
}

// wizardNextOK: Next can be pressed (the password must exist before going on from its step, and to finish).
func (u *UI) wizardNextOK(app *AppSnapshot) bool {
	switch u.wizardStep() {
	case "password", "done":
		return app.Setup != nil && !app.Setup.NeedsPassword
	}
	return true
}

// wizardKeys are the wizard's buttons on view, in the order they are tried.
func (u *UI) wizardKeys(n *NetSnapshot) []string {
	step := u.wizardStep()
	switch u.settingsView {
	case "networks":
		keys := []string{"s_back"}
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
		return u.zoneKeys()
	}
	keys := []string{"w_next"}
	if u.wiz.step > 0 {
		keys = append(keys, "w_back", "w_later")
	}
	app := u.app().AppSnapshot()
	if u.wizardCanSkip(&app) {
		keys = append(keys, "w_skip")
	}
	switch step {
	case "language", "time":
		keys = append(keys, u.pageKeys(step, n)...)
	case "network":
		keys = append(keys, u.pageKeys("wifi", n)...)
	case "password":
		if app.Setup != nil && app.Setup.NeedsPassword {
			keys = append(keys, "w_pw")
		}
	case "name":
		if app.Device != nil {
			keys = append(keys, "w_name")
		}
	case "cloud":
		if c := app.Cloud; c != nil && (c.State == "unlinked" || c.State == "pairing") {
			keys = append(keys, "w_pair")
		}
	}
	return keys
}

// wizardTap is a tap on the wizard (the key it went down and up on).
func (u *UI) wizardTap(what, pick string, hasPick bool) {
	app := u.app().AppSnapshot()
	step := u.wizardStep()
	switch what {
	case "w_back":
		u.wizardGo(u.wiz.step - 1)
	case "w_next":
		if !u.wizardNextOK(&app) {
			return
		}
		if step == "done" {
			u.appBusy(u.app().Do(AppAction{What: "setup_complete"}))
			return
		}
		state := "done"
		if step == "cloud" && (app.Cloud == nil || app.Cloud.State != "linked") {
			state = "skipped" // Next without linking it: it was not done
		}
		u.app().Do(AppAction{What: "setup_step", Value: step, State: state})
		u.wizardGo(u.wiz.step + 1)
	case "w_skip":
		u.app().Do(AppAction{What: "setup_step", Value: step, State: "skipped"})
		u.wizardGo(u.wiz.step + 1)
	case "w_later":
		u.wizardPostponed = true
		u.wizardClose()
	case "w_pw":
		u.kbClear()
		u.kb = &keyboard{mode: "webpw", layer: "lower", buf: make([]byte, PSKMax)}
		u.settingsView = "keyboard"
	case "w_name":
		name := ""
		if app.Device != nil {
			name = app.Device.Name
		}
		u.openTextKeyboard("name", 0, name, NameMax)
	case "w_pair":
		u.appBusy(u.app().Do(AppAction{What: "cloud_pair"}))
	default:
		u.pageTouch(what, pick, hasPick)
	}
	u.Dirty = true
}

// wizardGo goes to step i (its page from the start).
func (u *UI) wizardGo(i int) {
	if i < 0 || i >= len(WizardSteps) {
		return
	}
	if u.settingsView == "networks" {
		u.Net.SetScan(false)
	}
	u.wiz.step = i
	u.settingsView, u.confirm, u.timeDraftSet, u.pageSince = "main", nil, false, u.now()
}

// wizardKeyboardDone is «OK» of the wizard's keyboards: the web password (twice, then to the app) and the name.
func (u *UI) wizardKeyboardDone() {
	kb, tx := u.kb, u.Tx
	switch kb.mode {
	case "webpw":
		if kb.n < WebPasswordMin {
			return
		}
		copy(kb.first[:], kb.buf[:kb.n])
		kb.firstN = kb.n
		clear(kb.buf[:])
		kb.n, kb.mode, kb.err, kb.layer = 0, "webpw2", "", "lower"
	case "webpw2":
		same := kb.n == kb.firstN
		for i := 0; same && i < kb.n; i++ {
			same = kb.buf[i] == kb.first[i]
		}
		if !same {
			kb.wipe()
			kb.mode, kb.err, kb.layer = "webpw", tx.WizPwMismatch, "lower"
			return
		}
		ok := u.app().SetupPassword(kb.chars())
		u.closeKeyboard() // wipes both copies: the app's client has its own until it is sent
		u.appBusy(ok)
	case "name":
		name := strings.TrimSpace(string(kb.text))
		if !nameOK(name) {
			kb.err = tx.WizNameBad
			return
		}
		u.closeKeyboard()
		u.appBusy(u.app().Do(AppAction{What: "name", Value: name}))
	default:
		u.closeKeyboard()
	}
}

// nameOK is the device name (CONTRACT §2): 1-40 letters, digits, spaces and -_.'’ (the typographic apostrophe:
// a name set on the web).
func nameOK(n string) bool {
	if n == "" || len([]rune(n)) > NameMax {
		return false
	}
	for _, r := range n {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || strings.ContainsRune("-_.'’", r)) {
			return false
		}
	}
	return true
}

func (u *UI) wizardKeyboardTitle() string {
	switch u.kb.mode {
	case "webpw":
		return u.Tx.WizPwKeyboard
	case "webpw2":
		return u.Tx.WizPwAgain
	}
	return u.Tx.WizNameKeyboard
}

func (u *UI) wizardKeyboardHelp() string {
	if u.kb.mode == "name" {
		return u.Tx.WizNameHelp
	}
	return u.Tx.WizPwHelp
}

// --- drawing ---

// drawWizard is the whole screen: its top bar, the steps on the left and the step's page on the right.
func (u *UI) drawWizard(d *dc, net *NetSnapshot, app *AppSnapshot, cams []SourceStatus, legacy *WifiCreds) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	m := float64(lay.M)
	step := u.wizardStep()
	c.Paint(cDeep)
	// The top bar: ‹ Back, the title, where it is (dots), Skip and Next ›
	sh, sw := float64(lay.Status.H), float64(lay.Status.W)
	c.FillRect(0, 0, sw, sh, cBg)
	c.FillRect(0, sh-1, sw, 1, cLine)
	mid := sh / 2
	if u.wiz.step > 0 && u.settingsView == "main" {
		d.button("w_back", btn{label: tx.WizBack})
	} else if logo := u.logoImage(int(float64(lay.HB) * 0.6)); logo != nil {
		c.DrawImage(logo, int(m), int(mid-float64(logo.Rect.Dy())/2))
	}
	sub := u.settingsView == "main"
	if sub {
		ok := u.wizardNextOK(app)
		label := tx.WizNext
		if step == "done" {
			label = tx.WizFinish
		}
		d.button("w_next", btn{label: label, active: ok, dim: !ok})
		if u.wizardCanSkip(app) {
			d.button("w_skip", btn{label: tx.WizSkip})
		}
	}
	cx := sw / 2
	d.text(cx, mid-2*s, tx.WizTitle, 19*s, textOpt{col: cInk, bold: true, align: center, width: sw * 0.3})
	n := len(WizardSteps)
	gap := 18 * s
	x0 := cx - gap*float64(n-1)/2
	for i := range n {
		col := cLine
		r := 4 * s
		switch {
		case i == u.wiz.step:
			col, r = cBrand, 6*s
		case i < u.wiz.step:
			col = cQuiet
		}
		c.FillCircle(x0+gap*float64(i), mid+17*s, r, col)
	}
	// The panel: the steps on the left, «Later», the page
	p := lay.SettingsPanel
	pq := RR{float64(p.X), float64(p.Y), float64(p.W), float64(p.H), 12 * s}
	c.FillRR(pq, cBg.WithAlpha(0.98))
	c.StrokeRR(pq, 1.5, cLine)
	hd := lay.SettingsHead
	d.text(float64(hd.X)+4*s, float64(hd.Y)+float64(hd.H)/2+7*s, sprintf(tx.WizStepF, u.wiz.step+1, n), 19*s,
		textOpt{col: cQuiet, bold: true, width: float64(hd.W)})
	wl := lay.WizardList
	eh := float64(wl.H) / float64(n)
	for i, id := range WizardSteps {
		y := float64(wl.Y) + float64(i)*eh
		ey := y + eh/2
		cur := i == u.wiz.step
		if cur {
			c.FillRR(RR{float64(wl.X), y + 2*s, float64(wl.W), eh - 4*s, 8 * s}, cBrand.WithAlpha(0.24))
		}
		st := app.Setup.StepState(id)
		numCol := cQuiet
		if cur {
			numCol = cBrand
		}
		cxn := float64(wl.X) + m + 11*s
		if st == "done" && !cur {
			u.icon(c, "check", cxn, ey, 10*s, max(2.0, 2.6*s), cGood)
		} else {
			c.FillCircle(cxn, ey, 11*s, numCol.WithAlpha(0.3))
			d.text(cxn, ey+5*s, strconv.Itoa(i+1), 14*s, textOpt{col: cInk, bold: true, align: center})
		}
		tc := cQuiet
		if cur {
			tc = cInk
		}
		lx := float64(wl.X) + m + 30*s
		w := d.text(lx, ey+6*s, tx.WizSteps[id], 16*s, textOpt{col: tc, bold: cur, width: float64(wl.X+wl.W) - lx - m})
		if st == "skipped" && !cur && w < float64(wl.W)*0.6 {
			d.text(lx+w+6*s, ey+6*s, tx.WizStepSkipped, 13*s, textOpt{col: cQuiet})
		}
	}
	if u.wiz.step > 0 && sub {
		d.button("w_later", btn{label: tx.WizLater})
	}
	// The page
	switch u.settingsView {
	case "networks":
		u.drawPageTitle(d, tx.NetworksTitle, true)
		u.drawNetworks(d, net)
		return
	case "zones":
		u.drawPageTitle(d, tx.TimeZoneRow, true)
		u.drawZones(d, app)
		return
	}
	u.drawPageTitle(d, tx.WizHeads[step], false)
	switch step {
	case "language":
		u.drawLanguagePage(d, app)
		// the help, under the languages (the page's own line says the same in Settings)
	case "time":
		u.drawTimePage(d, app)
	case "network":
		u.drawNetPage(d, "wifi", net, cams, legacy)
	case "password":
		u.drawWizardPassword(d, app)
	case "name":
		u.drawWizardName(d, app)
	case "cloud":
		u.drawWizardCloud(d, app)
	case "done":
		u.drawWizardDone(d, app)
	}
}

// wizText is a wrapped paragraph at the top of the page; the y below it.
func (u *UI) wizText(d *dc, y float64, t string, size float64, col Color) float64 {
	pg, s := u.Lay.SettingsPage, d.s
	for _, line := range d.wrap(t, size*s, Sans, float64(pg.W)-8*s) {
		d.text(float64(pg.X)+4*s, y, line, size*s, textOpt{col: col, width: float64(pg.W) - 8*s})
		y += (size + 6) * s
	}
	return y
}

func (u *UI) webAddress(app *AppSnapshot) string {
	if app.Device != nil && app.Device.Hostname != "" {
		return "http://" + app.Device.Hostname + ".local"
	}
	if len(app.IPs) > 0 {
		return "http://" + app.IPs[0]
	}
	return "http://eirl-encoder.local"
}

func (u *UI) drawWizardPassword(d *dc, app *AppSnapshot) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	pg := lay.SettingsPage
	x0 := float64(pg.X) + 4*s
	u.wizText(d, float64(pg.Y)+24*s, tx.WizPwText, 17, cInk)
	if app.Setup == nil || !app.Setup.NeedsPassword {
		b := lay.Buttons["w_pw"]
		mid := float64(b.Y) + float64(b.H)/2
		u.icon(c, "check", x0+12*s, mid, 12*s, max(2.0, 2.8*s), cGood)
		d.text(x0+34*s, mid+7*s, tx.WizPwSet, 20*s, textOpt{col: cGood, bold: true, width: float64(pg.W) - 40*s})
		u.noteLine(d, u.resultNote(app, "setup_password"), true)
		return
	}
	d.button("w_pw", btn{label: tx.WizPwButton, active: true})
	b := lay.Buttons["w_pw"]
	y := u.wizText(d, float64(b.Y+b.H)+40*s, sprintf(tx.WizPwWebF, u.webAddress(app)), 16, cQuiet)
	if code := app.Setup.Code; code != "" {
		d.text(x0, y+16*s, tx.WizPwCode, 15*s, textOpt{col: cQuiet})
		// Monospaced and in two groups (the alphabet has no O/0 or I/1 to confuse)
		c.Text(x0, y+64*s, code[:4]+" "+code[4:], 40*s, MonoBold, cInk)
	}
	u.noteLine(d, u.resultNote(app, "setup_password"), true)
}

func (u *UI) drawWizardName(d *dc, app *AppSnapshot) {
	lay, tx, s := u.Lay, u.Tx, d.s
	pg := lay.SettingsPage
	x0 := float64(pg.X) + 4*s
	if !u.devicePageReady(d, app.DeviceAPI, app.Device != nil) {
		return
	}
	y := u.wizText(d, float64(pg.Y)+24*s, tx.WizNameText, 17, cInk) + 40*s
	d.text(x0, y, app.Device.Name, 32*s, textOpt{col: cInk, bold: true, width: float64(pg.W) - 8*s})
	if app.Device.Hostname != "" {
		d.text(x0, y+32*s, sprintf(tx.WizNameHostF, app.Device.Hostname+".local"), 16*s, textOpt{col: cQuiet,
			width: float64(pg.W) - 8*s})
	}
	d.button("w_name", btn{label: tx.WizNameButton})
	u.noteLine(d, u.resultNote(app, "name"), true)
}

func (u *UI) drawWizardCloud(d *dc, app *AppSnapshot) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	pg := lay.SettingsPage
	x0 := float64(pg.X) + 4*s
	cl := app.Cloud
	if app.CloudAPI == No || (cl != nil && cl.State == "disabled") {
		u.pageMessage(d, tx.WizCloudOff, "", false)
		return
	}
	if cl == nil {
		u.pageMessage(d, tx.AskingApp, "", false)
		return
	}
	note := u.resultNote(app, "cloud_pair")
	switch cl.State {
	case "linked":
		mid := float64(pg.Y) + float64(pg.H)/2
		u.icon(c, "check", x0+16*s, mid-8*s, 16*s, max(2.0, 3.2*s), cGood)
		t := tx.WizCloudLinked
		if cl.AccountLabel != "" {
			t = sprintf(tx.WizCloudLinkedF, cl.AccountLabel)
		}
		d.text(x0+44*s, mid, t, 24*s, textOpt{col: cGood, bold: true, width: float64(pg.W) - 52*s})
	case "pairing":
		qr := 0
		if len(cl.LinkQR) > 0 {
			qr = u.drawQR(c, cl.LinkQR, pg.X, pg.Y, min(pg.H, pg.W/2))
		}
		tx0 := float64(pg.X+qr) + 24*s
		tw := float64(pg.X+pg.W) - tx0 - 8*s
		y := float64(pg.Y) + 30*s
		for _, line := range d.wrap(tx.WizCloudHelp, 16*s, Sans, tw) {
			d.text(tx0, y, line, 16*s, textOpt{col: cQuiet, width: tw})
			y += 22 * s
		}
		c.Text(tx0, y+44*s, cl.Code, 40*s, MonoBold, cInk)
		y += 80 * s
		if cl.LinkURL != "" {
			for _, line := range d.wrap(strings.TrimPrefix(cl.LinkURL, "https://"), 14*s, Sans, tw) {
				d.text(tx0, y, line, 14*s, textOpt{col: cInk, width: tw})
				y += 20 * s
			}
		}
		if cl.ExpiresAt != nil {
			t := cl.ExpiresAt.In(time.Local)
			if app.Device != nil && !app.Device.Now.IsZero() {
				t = cl.ExpiresAt.In(app.Device.Now.Location())
			}
			d.text(tx0, y+10*s, sprintf(tx.WizCloudExpiresF, sprintf("%02d:%02d", t.Hour(), t.Minute())), 14*s,
				textOpt{col: cQuiet, width: tw})
		}
		d.text(tx0, float64(pg.Y+pg.H)-40*s, tx.WizCloudWaiting, 15*s, textOpt{col: cBrand, bold: true, width: tw})
		if note == "" && cl.LastError != "" {
			note = cl.LastError
		}
		// a new code: the button under the text, small
		if note != "" {
			d.text(tx0, float64(pg.Y+pg.H)-12*s, note, 14*s, textOpt{col: cWarn, bold: true, width: tw})
		}
		return
	default:
		u.wizText(d, float64(pg.Y)+24*s, tx.WizCloudText, 17, cInk)
		d.button("w_pair", btn{label: tx.WizCloudGet, active: true, dim: app.Pending == "cloud_pair"})
		if note == "" {
			note = cl.LastError
		}
	}
	u.noteLine(d, note, true)
}

func (u *UI) drawWizardDone(d *dc, app *AppSnapshot) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	pg := lay.SettingsPage
	x0 := float64(pg.X) + 4*s
	y := u.wizText(d, float64(pg.Y)+24*s, tx.WizDoneText, 17, cInk) + 14*s
	for _, id := range WizardSteps[:len(WizardSteps)-1] {
		st := app.Setup.StepState(id)
		col, mark := cQuiet, tx.WizStepSkipped
		switch {
		case id == "password" && app.Setup != nil && app.Setup.NeedsPassword:
			col, mark = cWarn, tx.WizNeedsPassword
		case st == "done":
			col, mark = cGood, tx.WizStepDone
		case st != "skipped":
			mark = ""
		}
		if col == cGood {
			u.icon(c, "check", x0+10*s, y-6*s, 9*s, max(2.0, 2.4*s), cGood)
		}
		w := d.text(x0+30*s, y, tx.WizSteps[id], 17*s, textOpt{col: cInk, bold: true})
		if mark != "" {
			d.text(x0+30*s+w+10*s, y, mark, 15*s, textOpt{col: col, width: float64(pg.W) - w - 50*s})
		}
		y += 30 * s
	}
	u.noteLine(d, u.resultNote(app, "setup_complete"), true)
}
