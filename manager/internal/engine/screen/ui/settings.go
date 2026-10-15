package ui

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// groupIcon is each Settings group's icon in the list.
var groupIcon = map[string]string{"stream": "stream", "sources": "grid", "wifi": "wifi", "cams": "camera",
	"cloud": "cloud", "device": "sliders", "system": "info"}

// drawSettings is the Settings panel: the list of pages on the left (CONTRACT §0: only on the touch screen) and
// the selected page on the right. The Wi-Fi page («Connect to a Wi-Fi», «Create my Wi-Fi» and its QR, password,
// channel and new password) and the Network cameras page (cams.go: the RTMP/SRT reception, the ways in, each camera
// and its own view) were the two tabs of before (WIFI.md §6, NETWORK_SOURCES.md §6.4); their switches are done by
// the app (Net, and App for MIXER.md §21's). The device pages (language, date and time, updates, about) are the
// app's device API (App).
func (u *UI) drawSettings(d *dc, net *NetSnapshot, app *AppSnapshot, cams []SourceStatus, legacy *WifiCreds) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	m := float64(lay.M)
	c.Paint(cBlack.WithAlpha(0.7))
	p := lay.SettingsPanel
	pq := RR{float64(p.X), float64(p.Y), float64(p.W), float64(p.H), 12 * s}
	c.FillRR(pq, cBg.WithAlpha(0.98))
	c.StrokeRR(pq, 1.5, cLine)
	// The list: its title, then one entry per page
	hd := lay.SettingsHead
	mid := float64(hd.Y) + float64(hd.H)/2
	u.icon(c, "gear", float64(hd.X)+m+12*s, mid, 12*s, max(2.0, 2.4*s), cInk)
	d.text(float64(hd.X)+m+34*s, mid+8*s, tx.Settings, 22*s, textOpt{col: cInk, bold: true,
		width: float64(hd.W) - m - 34*s})
	cur := groupOf(u.settingsPage)
	for _, g := range SettingsGroups {
		dot := ""
		for _, p := range g.Pages {
			if t := u.pageDot(p, net, app, legacy); t == "warn" || (t != "" && dot == "") {
				dot = t
			}
		}
		u.drawEntry(d, "s_page_"+g.ID, groupIcon[g.ID], tx.GroupTitle[g.ID], cur != nil && cur.ID == g.ID, dot)
	}
	// The page: its title row (the tabs of its group, or its name), then its content
	switch u.settingsView {
	case "networks":
		u.drawPageTitle(d, tx.NetworksTitle, true)
		u.drawNetworks(d, net)
		return
	case "zones":
		u.drawPageTitle(d, tx.TimeZoneRow, true)
		u.drawZones(d, app)
		return
	case "cands":
		u.drawPageTitle(d, sprintf(tx.SourceF, u.srcPick), true)
		u.drawCandidates(d, app)
		return
	case "pick":
		if u.chooser != nil {
			u.drawPageTitle(d, u.chooser.title, true)
			u.drawPicker(d)
			return
		}
	}
	if p := pageDefs[u.settingsPage]; p != nil && p.title != nil {
		if t, back, ok := p.title(u); ok {
			u.drawPageTitle(d, t, back)
			p.draw(u, d, app)
			return
		}
	}
	if cur != nil && len(cur.Pages) > 1 {
		u.drawTabs(d, cur, net, app, legacy)
	} else {
		u.drawPageTitle(d, tx.PageTitle[u.settingsPage], false)
	}
	u.drawPage(d, u.settingsPage, net, app, cams, legacy)
}

// pageDot is a page's dot in the list and on its tab: "warn" (something to fix), "brand" (news), "" none.
func (u *UI) pageDot(page string, net *NetSnapshot, app *AppSnapshot, legacy *WifiCreds) string {
	switch {
	case page == "wifi" && WifiDown(net, legacy):
		return "warn"
	case page == "updates" && updateAvailable(app):
		return "brand"
	case page == "about" && setupPending(app):
		return "warn"
	}
	return ""
}

// drawTabs is the title row of a group of several pages: one tab per page (the one on view highlighted, each with
// its dot), and the X.
func (u *UI) drawTabs(d *dc, g *SettingsGroup, net *NetSnapshot, app *AppSnapshot, legacy *WifiCreds) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	for _, p := range g.Pages {
		key := "s_tab_" + p
		d.button(key, btn{label: tx.TabTitle[p], active: p == u.settingsPage, size: 19})
		if dot := u.pageDot(p, net, app, legacy); dot != "" {
			col := cWarn
			if dot == "brand" {
				col = cBrand
			}
			r := lay.Buttons[key]
			c.FillCircle(float64(r.X+r.W)-10*s, float64(r.Y)+10*s, 5*s, col)
		}
	}
	d.button("s_close", btn{icon: "close"})
}

// drawPage draws one page's content in the page area (Settings, and the wizard's steps that are Settings pages).
func (u *UI) drawPage(d *dc, page string, net *NetSnapshot, app *AppSnapshot, cams []SourceStatus, legacy *WifiCreds) {
	switch page {
	case "wifi":
		u.drawNetPage(d, "wifi", net, cams, legacy)
	case "language":
		u.drawLanguagePage(d, app)
	case "time":
		u.drawTimePage(d, app)
	case "updates":
		u.drawUpdatesPage(d, app)
	case "about":
		u.drawAboutPage(d, app)
	case "sources":
		u.drawSourcesPage(d, app)
	default:
		if p := pageDefs[page]; p != nil {
			p.draw(u, d, app)
		}
	}
}

// drawEntry is one entry of the list: its icon and name, the selected one highlighted, a dot if it has news.
func (u *UI) drawEntry(d *dc, key, icon, label string, active bool, dot string) {
	lay, c, s := u.Lay, d.c, d.s
	m := float64(lay.M)
	r := lay.Buttons[key]
	q := rrOf(r, 10*s)
	if active {
		c.FillRR(q, cBrand.WithAlpha(0.24))
		c.StrokeRR(q, 2, cBrand)
	} else {
		c.FillRR(q, cPanel)
		c.StrokeRR(q, 1.25, cLine)
	}
	if d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
		d.lit(r, 10*s, 0.14)
	}
	mid := float64(r.Y) + float64(r.H)/2
	ic := cQuiet
	if active {
		ic = cInk
	}
	u.icon(c, icon, float64(r.X)+m+12*s, mid, 12*s, max(2.0, 2.4*s), ic)
	x0 := float64(r.X) + m + 34*s
	d.text(x0, mid+7*s, label, 19*s, textOpt{col: cInk, bold: active, width: float64(r.X+r.W) - x0 - 2*m})
	if dot != "" {
		col := cWarn
		if dot == "brand" {
			col = cBrand
		}
		c.FillCircle(float64(r.X+r.W)-m-4*s, mid, 5*s, col)
	}
}

// drawPageTitle is the page's title row: ← in a sub-view, the name, and the X.
func (u *UI) drawPageTitle(d *dc, title string, back bool) {
	lay, s := u.Lay, d.s
	m := float64(lay.M)
	t := lay.SettingsTitle
	x := float64(t.X)
	if back {
		d.button("s_back", btn{icon: "back"})
		x = float64(lay.Buttons["s_back"].X+lay.Buttons["s_back"].W) + m
	}
	right := float64(t.X + t.W)
	if !u.wizardOn() { // the wizard has no X: it ends with its last step (or «Later»)
		d.button("s_close", btn{icon: "close"})
		right = float64(lay.Buttons["s_close"].X) - m
	}
	d.text(x, float64(t.Y)+float64(t.H)/2+8*s, title, 22*s, textOpt{col: cInk, bold: true, width: right - x})
}

// pageMessage is a line centred in the page (asking the app, it does not answer, an older app).
func (u *UI) pageMessage(d *dc, t1, t2 string, warn bool) {
	pg, s := u.Lay.SettingsPage, d.s
	cx, cy, w := float64(pg.X)+float64(pg.W)/2, float64(pg.Y)+float64(pg.H)/2, float64(pg.W)-24*s
	col := cInk
	if warn {
		col = cWarn
	}
	d.text(cx, cy-6*s, t1, 19*s, textOpt{col: col, bold: true, align: center, width: w})
	if t2 != "" {
		d.text(cx, cy+22*s, t2, 15*s, textOpt{col: cQuiet, align: center, width: w})
	}
}

// devicePageReady: the app's device API answers; else the page says why not (and is not drawn).
func (u *UI) devicePageReady(d *dc, api Tri, have bool) bool {
	tx := u.Tx
	switch {
	case api == No:
		u.pageMessage(d, tx.PageUnavailable, tx.PageUnavailableSub, false)
	case !have && api == Unknown:
		u.pageMessage(d, tx.AskingApp, "", false)
	case !have:
		u.pageMessage(d, tx.AppNotAnswering, "", true)
	default:
		return true
	}
	return false
}

// row is a Settings row: its icon, title and sub-text, and its switch; the whole row is the switch. rightX0, if
// not 0, is where the text must end (a button inside the row).
func (u *UI) row(d *dc, key, icon, title string, rs RowState, live bool, rightX0 float64) {
	u.rowDot(d, key, icon, title, rs, "", live, rightX0)
}

// rowDot is row with a coloured dot before the icon: a connection's health (MODEMS.md §5), good | weak | bad | off
// (a ring: switched off, not a fault); "" none.
func (u *UI) rowDot(d *dc, key, icon, title string, rs RowState, dot string, live bool, rightX0 float64) {
	lay, c, s := u.Lay, d.c, d.s
	m := float64(lay.M)
	r := lay.Buttons[key]
	q := rrOf(r, 10*s)
	c.FillRR(q, cPanel)
	c.StrokeRR(q, 1.25, cLine)
	if rs.Knob != KnobNone && d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
		c.FillRR(q, cWhite.WithAlpha(0.12))
	}
	rmid := float64(r.Y) + float64(r.H)/2
	ic := cQuiet
	if live {
		ic = cGood
	}
	ix, tx0 := float64(r.X)+2*m+10*s, float64(r.X)+2*m+34*s
	if col, ok := healthDot[dot]; ok {
		dx := float64(r.X) + m + 7*s
		if dot == "off" {
			c.FillCircle(dx, rmid, 7*s, col)
			c.FillCircle(dx, rmid, 4.5*s, cPanel) // a ring: switched off, not a fault
		} else {
			c.FillCircle(dx, rmid, 7*s, col)
		}
		ix, tx0 = ix+10*s, tx0+10*s
	}
	u.icon(c, icon, ix, rmid, 12*s, max(2.0, 2.4*s), ic)
	rightX := float64(r.X+r.W) - 2*m
	if rs.Knob != KnobNone {
		rightX = d.knob(rightX, rmid, rs.Knob, rs.Busy) - m
	}
	if rightX0 > 0 {
		rightX = min(rightX, rightX0)
	}
	tone := map[string]Color{"good": cGood, "warn": cWarn, "bad": cBad, "quiet": cQuiet}
	d.text(tx0, rmid-2*s, title, 19*s, textOpt{col: cInk, bold: true, width: rightX - tx0})
	d.text(tx0, rmid+17*s, rs.Sub, 14*s, textOpt{col: tone[rs.Tone], bold: rs.Tone == "warn" || rs.Tone == "bad", width: rightX - tx0})
}

// knob draws a row's switch ending at rightX, centred on mid (half-transparent while busy); it returns its left.
func (d *dc) knob(rightX, mid float64, k Knob, busy bool) float64 {
	c, s := d.c, d.s
	sw, shh := 64*s, 34*s
	sx, sy := rightX-sw, mid-shh/2
	kc := cLine
	if k == KnobOn {
		kc = cGood
	}
	a := 1.0
	if busy {
		a = 0.5
	}
	c.FillRR(RR{sx, sy, sw, shh, shh / 2}, kc.WithAlpha(a))
	kx := sx + shh/2
	if k == KnobOn {
		kx = sx + sw - shh/2
	}
	c.FillCircle(kx, mid, shh/2-4*s, cInk)
	return sx
}

// healthDot: the colour of a connection's dot (MODEMS.md §5), the same green, amber and red as the meters.
var healthDot = map[string]Color{"good": cGood, "weak": cWarn, "bad": cBad, "off": cQuiet}

// netPageReady: the app's network state is known; else the page says why not (asking, or the app does not
// answer) and is not drawn.
func (u *UI) netPageReady(d *dc, net *NetSnapshot) bool {
	tx := u.Tx
	if api := net.API; api == Unknown || (api == Yes && net.Status == nil) {
		msg, warn := tx.AskingApp, false
		if api != Unknown {
			msg, warn = net.Error, true
			if msg == "" {
				msg = tx.AppNotAnswering
			}
		}
		u.pageMessage(d, msg, "", warn)
		return false
	}
	return true
}

// netNotes are the network's notices for a page: the last change's (the app's message) for 6 s.
func netNotes(d *dc, net *NetSnapshot) []string {
	if net.Message != "" && d.now.Before(net.MsgUntil) {
		return []string{net.Message}
	}
	return nil
}

// drawNetPage is the Wi-Fi page: «Connect to a Wi-Fi», «Create my Wi-Fi» and, under them, its QR with the network,
// its password (dots, with an eye), its channel and «New password», and «Rename». The Network cameras page is
// cams.go's (drawCamsPage).
func (u *UI) drawNetPage(d *dc, page string, net *NetSnapshot, cams []SourceStatus, legacy *WifiCreds) {
	lay, tx := u.Lay, u.Tx
	m := float64(lay.M)
	if page == "cams" {
		u.drawCamsPage(d, net, cams, legacy)
		return
	}
	if !u.netPageReady(d, net) {
		return
	}
	keys := u.settingsKeys(net)
	notes := netNotes(d, net)
	if ClientSwitch(net) {
		rs := ClientRow(net, tx)
		right := 0.0
		if slices.Contains(keys, "s_networks") {
			right = float64(lay.Buttons["s_networks"].X) - m
		}
		u.row(d, "s_client", "wifi", tx.ClientRow, rs, rs.Tone == "good", right)
		if slices.Contains(keys, "s_networks") { // inside the row: drawn on top of it
			d.button("s_networks", btn{label: tx.NetworksButton})
		}
		if net.Warning == "two_channels" {
			notes = append(notes, tx.TwoChannels)
		}
	}
	ap := APRow(net, legacy, tx)
	u.row(d, "s_ap", "hotspot", tx.APRow, ap, ap.Tone == "good", 0)
	// What goes under the row: the QR with the wifi up; else why it is not
	qr, _ := u.wifiQR(net, legacy)
	u.drawWifiQR(d, net, legacy, qr, append(notes, u.wifiNotes()...))
}

// wifiQR is what goes under «Create my Wi-Fi»: the QR's state (on | starting | error | off: no QR, it says why) and
// the Wi-Fi to join (nil: not known). The drawing and the page's buttons agree on it.
func (u *UI) wifiQR(net *NetSnapshot, legacy *WifiCreds) (string, *WifiCreds) {
	ap := APRow(net, legacy, u.Tx)
	qr := "off"
	switch {
	case ap.Tone == "good":
		qr = "on"
	case ap.Tone == "warn":
		qr = "error"
	case ap.Knob == KnobOn && ap.Sub == u.Tx.TurningOn:
		qr = "starting"
	}
	creds := net.Wifi
	if net.Status == nil {
		creds = nil
		if net.API == No {
			creds = legacy
		}
	}
	return qr, creds
}

// legacyCreds is wifi.json's Wi-Fi for an older app without the local API (nil otherwise).
func (u *UI) legacyCreds(n *NetSnapshot) *WifiCreds {
	if n.API == No && u.Legacy != nil {
		return u.Legacy()
	}
	return nil
}

// wifiShown: the QR and the password are on view.
func (u *UI) wifiShown(net *NetSnapshot, legacy *WifiCreds) bool {
	qr, creds := u.wifiQR(net, legacy)
	return qr == "on" && creds != nil
}

// wifiExtras: beside the QR, the channel and «New password» (an app with the board's Wi-Fi, in Settings: the
// wizard's network step stays as simple as it was).
func (u *UI) wifiExtras(net *NetSnapshot, legacy *WifiCreds) bool {
	return u.wifiShown(net, legacy) && APSwitch(net) && !u.wizardOn()
}

// wifiPwEye is the Wi-Fi password's eye.
const wifiPwEye = "s_wifi_pw_eye"

// wifiPwShown: the Wi-Fi password is shown (its eye open here: one opened in the wizard does not stay open in
// Settings, nor the other way round).
func (u *UI) wifiPwShown() bool {
	return u.revealed(wifiPwEye) && u.camp.wifiEyeWiz == u.wizardOn()
}

func (u *UI) toggleWifiPw() {
	if u.camp.wifiEyeWiz != u.wizardOn() { // opened in the other place: here it starts closed
		u.camp.wifiEyeWiz = u.wizardOn()
		if u.revealed(wifiPwEye) {
			u.toggleReveal(wifiPwEye)
		}
	}
	u.toggleReveal(wifiPwEye)
}

// wifiNotes are the Wi-Fi page's own notices: «New password» waiting for its second tap, the app's error for the
// channel or the new password (8 s), what to do after a new password, and the page's own notice.
func (u *UI) wifiNotes() []string {
	app := u.app().AppSnapshot()
	tx := u.Tx.NetX
	var out []string
	if u.confirming("ap_rotate") {
		out = append(out, tx.NewPasswordAsk)
	}
	if t := u.resultNote(&app, "ap_channel", "ap_rotate"); t != "" {
		out = append(out, t)
	}
	if u.okNote(&app, "ap_rotate") {
		out = append(out, tx.NewPasswordDone)
	}
	if t, _ := u.pageNoteText(); t != "" {
		out = append(out, t)
	}
	return out
}

// okNote: the last action is what, taken by the app less than 8 s ago, on this page.
func (u *UI) okNote(app *AppSnapshot, what string) bool {
	l := app.Last
	return l != nil && l.OK && l.What == what && u.now().Sub(l.At) <= 8*time.Second && !l.At.Before(u.pageSince)
}

// channelText is the channel's field: the saved one (Automatic or its number) and, if different or automatic, the
// one in use.
func (u *UI) channelText(net *NetSnapshot) string {
	tx := u.Tx.NetX
	saved := ""
	if net.Wifi != nil {
		saved = net.Wifi.Channel
	}
	base := tx.ChannelAuto
	if saved != "" && saved != "auto" {
		base = saved
	}
	var inUse *int
	if net.Status != nil {
		inUse = net.Status.AccessPoint.Channel
	}
	if inUse == nil || *inUse <= 0 || base == strconv.Itoa(*inUse) {
		return base
	}
	return sprintf(tx.ChannelInUseF, base, *inUse)
}

// openChannelPicker: the 2.4 GHz channel, automatic or 1-11 (PUT /network/ap {"channel"}).
func (u *UI) openChannelPicker() {
	snap := u.Net.Snapshot()
	if !u.wifiExtras(&snap, nil) {
		return
	}
	if u.apBusy() {
		u.appBusy(false)
		return
	}
	tx := u.Tx.NetX
	cur := "auto"
	if snap.Wifi != nil && snap.Wifi.Channel != "" {
		cur = snap.Wifi.Channel
	}
	opts := []pickOpt{{ID: "auto", Title: tx.ChannelAuto, Sub: tx.ChannelAutoSub}}
	for ch := 1; ch <= 11; ch++ {
		opts = append(opts, pickOpt{ID: strconv.Itoa(ch), Title: sprintf(tx.ChannelNumF, ch)})
	}
	u.openPicker(tx.Channel, cur, opts, func(u *UI, id string) {
		if id == cur {
			return
		}
		if u.apBusy() { // another change of the Wi-Fi started while the list was open
			u.appBusy(false)
			return
		}
		ok := u.app().Do(AppAction{What: "ap_channel", Body: map[string]any{"channel": id}})
		u.appBusy(ok)
		if ok {
			u.notePage(u.Tx.NetX.ChannelApplying, false)
		}
	})
}

// rotateWifi: «New password», with a second tap (every camera and phone on the Wi-Fi has to join it again).
func (u *UI) rotateWifi() {
	snap := u.Net.Snapshot()
	if !u.wifiExtras(&snap, nil) {
		return
	}
	if u.apBusy() {
		u.appBusy(false)
		return
	}
	if u.confirmTap("ap_rotate") {
		u.appBusy(u.app().Do(AppAction{What: "ap_rotate"}))
	}
}

// drawWifiQR: under «Create my Wi-Fi», the QR to join (the WIFI: format of iOS's and Android's cameras) and, at
// its side, the network, the password (dots, with its eye), the channel, «New password» and the notes. state: on |
// starting | error | off (no QR: says so). With the app's API, «Rename» at the bottom right; with the AP not on, the
// name it has goes next to it (it is there from the first start: the wizard's network step shows it this way).
func (u *UI) drawWifiQR(d *dc, net *NetSnapshot, legacy *WifiCreds, state string, notes []string) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	m := float64(lay.M)
	q0 := lay.SettingsQR
	x0, y0, w0, h0 := float64(q0.X), float64(q0.Y), float64(q0.W), float64(q0.H)
	_, creds := u.wifiQR(net, legacy)
	edit := SSIDEditable(net)
	if edit {
		label, busy := tx.SSIDRename, net.Pending != nil && net.Pending.What == "ssid"
		if busy {
			label = tx.SSIDRenaming
		}
		d.button("s_ssid", btn{label: label, dim: busy})
	}
	// The notes (the app's refusals are long: «… is on air over this Wi-Fi…»), wrapped and capped so that they never
	// reach what is below
	noteLines := func(width float64, most int) []string {
		var out []string
		for _, n := range notes {
			out = append(out, d.wrap(n, 16*s, SansBold, width)...)
		}
		if len(out) > most {
			out[most-1] = strings.Join(out[most-1:], " ") // the last one, cut to fit when drawn
			out = out[:most]
		}
		return out
	}
	if state != "on" || creds == nil {
		ty := y0 + 26*s
		nl := noteLines(w0-8*s, 3)
		for _, l := range nl {
			d.text(x0+4*s, ty, l, 16*s, textOpt{col: cWarn, bold: true, width: w0 - 8*s})
			ty += 22 * s
		}
		if len(nl) > 0 {
			ty += 8 * s
		}
		bottom := y0 + h0
		if edit { // the name, next to «Rename»
			b := lay.Buttons["s_ssid"]
			mid, w := float64(b.Y)+float64(b.H)/2, float64(b.X)-x0-2*m
			d.text(x0+4*s, mid-8*s, tx.QRNetwork, 15*s, textOpt{col: cQuiet, width: w})
			d.text(x0+4*s, mid+20*s, net.Wifi.SSID, 22*s, textOpt{col: cInk, bold: true, width: w})
			bottom = float64(b.Y) - m
		}
		t1, t2 := tx.QRReading, ""
		switch state {
		case "starting":
			t1, t2 = tx.QRStarting, tx.QRStartingSub
		case "error":
			t1, t2 = tx.QRError, tx.QRErrorSub
		case "off":
			t1, t2 = tx.QROff, tx.QROffSub
		}
		cy := (ty + bottom) / 2
		d.text(x0+w0/2, cy-6*s, t1, 19*s, textOpt{col: cInk, bold: true, align: center, width: w0 - 24*s})
		if t2 != "" {
			d.text(x0+w0/2, cy+20*s, t2, 15*s, textOpt{col: cQuiet, align: center, width: w0 - 24*s})
		}
		return
	}
	u.drawQR(c, creds.QR, q0.X, q0.Y, int(h0))
	// The column beside it: the password's field and the rest under it; above, the notes or the network's name
	pw := lay.Buttons["s_wifi_pw"]
	tx0 := float64(pw.X)
	tw := x0 + w0 - tx0
	headH := float64(pw.Y-q0.Y) - m/2
	if nl := noteLines(tw, max(1, int((headH-8*s)/(22*s)))); len(nl) > 0 {
		ty := y0 + 18*s
		for _, l := range nl { // the network's name is also in «Create my Wi-Fi»'s row: the notes take its place
			d.text(tx0, ty, l, 16*s, textOpt{col: cWarn, bold: true, width: tw})
			ty += 22 * s
		}
	} else {
		d.text(tx0+4*s, y0+18*s, tx.QRNetwork, 15*s, textOpt{col: cQuiet, width: tw})
		d.text(tx0+4*s, y0+min(headH-8*s, 52*s), creds.SSID, 24*s, textOpt{col: cInk, bold: true, width: tw})
	}
	// Monospaced: the password has l, I, 1, O and 0. Dots until its eye is opened (the QR carries it anyway: it is
	// how cameras and phones join)
	shown := u.wifiPwShown()
	u.field(d, "s_wifi_pw", tx.QRPassword, creds.Password, fieldOpt{secret: true, set: true, shown: shown, dim: true})
	u.eye(d, wifiPwEye, shown)
	if u.wifiExtras(net, legacy) {
		app := u.app().AppSnapshot()
		val, busy := u.channelText(net), app.Pending == "ap_channel"
		if busy {
			val = tx.NetX.Applying
		}
		u.field(d, "s_wifi_ch", tx.NetX.Channel, val, fieldOpt{chevron: !busy, placeholder: busy})
		conf := u.confirming("ap_rotate")
		label := tx.NetX.NewPassword
		switch {
		case app.Pending == "ap_rotate":
			label = tx.NetX.Applying
		case conf:
			label = tx.TapAgain
		}
		d.button("s_wifi_rotate", btn{label: label, active: conf, col: colp(cWarn), dim: app.Pending == "ap_rotate"})
		return
	}
	// The wizard and an older app: the help where the channel and «New password» go
	ch := lay.Buttons["s_wifi_ch"]
	ty := float64(ch.Y) + 18*s
	for _, t := range tx.QRHelp {
		d.text(tx0+4*s, ty, t, 15*s, textOpt{col: cQuiet, width: tw - 4*s})
		ty += 21 * s
	}
}

// drawQR draws QR rows ("0"/"1") at (x, y) with its white margin of 4 modules, at most size px; its side.
func (u *UI) drawQR(c *Canvas, rows []string, x, y, size int) int {
	n := len(rows) + 8
	q := max(2, size/max(1, n))
	c.FillRect(float64(x), float64(y), float64(q*n), float64(q*n), cWhite)
	for j, row := range rows {
		for i := 0; i < len(row); i++ {
			if row[i] == '1' {
				c.FillRect(float64(x+(i+4)*q), float64(y+(j+4)*q), float64(q), float64(q), cBlack)
			}
		}
	}
	return q * n
}

// drawNetworks is the client wifi's network list (WIFI.md §6), in the page: the connected one, the known ones
// and then by signal (the app's order), with «Olvidar» on the known ones and pages if they do not fit.
func (u *UI) drawNetworks(d *dc, net *NetSnapshot) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	m := float64(lay.M)
	pg := lay.SettingsPage
	px, py, pw, ph := float64(pg.X), float64(pg.Y), float64(pg.W), float64(pg.H)
	note := net.ScanError
	if net.Message != "" && d.now.Before(net.MsgUntil) {
		note = net.Message
	}
	cl := net.Client
	if cl == nil {
		cl = &WifiClient{}
	}
	nets := net.Scan
	rows := lay.ListRows
	pages := max(1, (len(nets)+rows-1)/rows)
	u.listPage = min(u.listPage, pages-1)
	lo := min(len(nets), u.listPage*rows)
	items := nets[lo:min(len(nets), lo+rows)]
	u.listShown = u.listShown[:0]
	for _, n := range items {
		u.listShown = append(u.listShown, n.SSID)
	}
	clear(u.listForget)
	u.listPages = pages
	if len(items) == 0 {
		t := tx.NoNetworks
		if net.Scan == nil || net.ScanBusy {
			t = tx.Searching
		}
		d.text(px+pw/2, py+ph/2, t, 19*s, textOpt{col: cInk, bold: true, align: center})
		if note != "" {
			d.text(px+pw/2, py+ph/2+30*s, note, 15*s, textOpt{col: cWarn, bold: true, align: center, width: pw - 4*m})
		}
		return
	}
	lw := max(2.0, 2.4*s)
	for i, n := range items {
		key := "s_row_" + strconv.Itoa(i)
		r := lay.Buttons[key]
		q := rrOf(r, 10*s)
		c.FillRR(q, cPanel)
		bc := cLine
		if n.Connected {
			bc = cGood
		}
		c.StrokeRR(q, 1.25, bc)
		if d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
			c.FillRR(q, cWhite.WithAlpha(0.12))
		}
		rmid := float64(r.Y) + float64(r.H)/2
		ok := n.Security == "open" || n.Security == "wpa" || n.Security == "wpa2"
		ix := float64(r.X) + 2*m + 10*s
		u.icon(c, "wifi", ix, rmid, 12*s, lw, cLine) // the signal: the arcs it has, on top
		sc := cQuiet
		switch {
		case n.Connected:
			sc = cGood
		case ok:
			sc = cInk
		}
		u.icon(c, sprintf("wifi%d", SignalLevel(n.Signal)), ix, rmid, 12*s, lw, sc)
		tx0 := float64(r.X) + 2*m + 34*s
		rightX := float64(r.X+r.W) - 2*m
		if n.Known || n.Connected {
			u.listForget[i] = true
			conf := u.confirm != nil && u.confirm.what == "forget" && u.confirm.ssid == n.SSID
			label := tx.Forget
			if conf {
				label = tx.ForgetConfirm
			}
			fk := "s_forget_" + strconv.Itoa(i)
			d.button(fk, btn{label: label, active: conf, col: colp(cWarn)})
			rightX = float64(lay.Buttons[fk].X) - m
		}
		here := cl.SSID == n.SSID
		var stT string
		var stC Color
		switch {
		case n.Connected:
			stT, stC = tx.NetConnected, cGood
		case here && cl.State == "connecting":
			stT, stC = tx.NetConnecting, cQuiet
		case here && cl.State == "error":
			stT, stC = tx.NetFailed, cWarn
			if cl.Error == "wrong_password" {
				stT = tx.NetWrongPassword
			}
		case n.Known:
			stT, stC = tx.NetSaved, cQuiet
		}
		if stT != "" {
			rightX -= d.text(rightX, rmid+6*s, stT, 15*s, textOpt{col: stC, bold: stC != cQuiet, align: right}) + m
		}
		sec := tx.Security[n.Security]
		if n.Band != "" {
			sec += sprintf(tx.BandSuffixF, strings.ReplaceAll(n.Band, ".", tx.Decimal))
		}
		nc := cQuiet
		if ok {
			nc = cInk
		}
		d.text(tx0, rmid-2*s, n.SSID, 19*s, textOpt{col: nc, bold: true, width: rightX - tx0})
		d.text(tx0, rmid+17*s, sec, 14*s, textOpt{col: cQuiet, width: rightX - tx0})
	}
	if pages > 1 {
		if u.listPage > 0 {
			d.button("s_prev", btn{label: tx.PrevPage})
		}
		if u.listPage < pages-1 {
			d.button("s_next", btn{label: tx.NextPage})
		}
	}
	if u.confirm != nil && u.confirm.what == "forget" && note == "" {
		note = tx.ForgetAgain
	}
	t := note
	if note == "" {
		t = tx.TapNetwork
		if pages > 1 {
			t = sprintf(tx.PageF, u.listPage+1, pages)
		}
	}
	u.pagerNote(d, t, note != "")
}

// pagerNote is the line between ‹ and › at the bottom of a paged list.
func (u *UI) pagerNote(d *dc, t string, warn bool) {
	lay, s := u.Lay, d.s
	m := float64(lay.M)
	bp := lay.Buttons["s_prev"]
	pg := lay.SettingsPage
	col := cQuiet
	if warn {
		col = cWarn
	}
	d.text(float64(pg.X)+float64(pg.W)/2, float64(bp.Y)+float64(bp.H)/2+6*s, t, 15*s, textOpt{col: col, bold: warn,
		align: center, width: float64(pg.W) - 2*float64(bp.W) - 4*m})
}

// --- the device pages ---

// noteLine is a warning or a quiet line at the bottom of the page.
func (u *UI) noteLine(d *dc, t string, warn bool) {
	if t == "" {
		return
	}
	pg, s := u.Lay.SettingsPage, d.s
	col := cQuiet
	if warn {
		col = cWarn
	}
	d.text(float64(pg.X)+4*s, float64(pg.Y+pg.H)-8*s, t, 15*s, textOpt{col: col, bold: warn, width: float64(pg.W) - 8*s})
}

// resultNote is the last action's error for these actions, shown for 8 s.
func (u *UI) resultNote(app *AppSnapshot, whats ...string) string {
	l := app.Last
	if l == nil || l.OK || u.now().Sub(l.At) > 8*time.Second || l.At.Before(u.pageSince) {
		return ""
	}
	for _, w := range whats {
		if l.What == w {
			return u.resultText(l)
		}
	}
	return ""
}

// resultText is what to say of a failed action: the app's message (in the device language), else its code.
func (u *UI) resultText(l *AppResult) string {
	switch {
	case l.Status == 403 || l.Status == 404: // an app without this route over the socket: not a user's error
		return u.Tx.PageUnavailable
	case l.Message != "":
		return l.Message
	}
	return sprintf(u.Tx.AppMessageF, l.Code)
}

func (u *UI) drawLanguagePage(d *dc, app *AppSnapshot) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	m := float64(lay.M)
	if !u.devicePageReady(d, app.DeviceAPI, app.Device != nil) {
		return
	}
	cur := u.Tx.Code
	for _, code := range Languages {
		key := "s_lang_" + code
		r := lay.Buttons[key]
		active := code == cur
		q := rrOf(r, 10*s)
		if active {
			c.FillRR(q, cBrand.WithAlpha(0.24))
			c.StrokeRR(q, 2, cBrand)
		} else {
			c.FillRR(q, cPanel)
			c.StrokeRR(q, 1.25, cLine)
		}
		if d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
			d.lit(r, 10*s, 0.14)
		}
		mid := float64(r.Y) + float64(r.H)/2
		d.text(float64(r.X)+2*m, mid+9*s, LanguageNames[code], 26*s, textOpt{col: cInk, bold: active})
		if active {
			u.icon(c, "check", float64(r.X+r.W)-2*m-12*s, mid, 12*s, max(2.0, 2.8*s), cBrand)
			d.text(float64(r.X+r.W)-2*m-34*s, mid+6*s, tx.LanguageInUse, 15*s, textOpt{col: cQuiet, align: right})
		}
	}
	last := lay.Buttons["s_lang_"+Languages[len(Languages)-1]]
	help := tx.LanguageHelp
	if u.wizardOn() {
		help = tx.WizLanguageHelp
	}
	y := float64(last.Y+last.H) + 30*s
	for _, line := range d.wrap(help, 15*s, Sans, float64(last.W)-8*s) {
		d.text(float64(last.X)+4*s, y, line, 15*s, textOpt{col: cQuiet, width: float64(last.W)})
		y += 21 * s
	}
	u.noteLine(d, u.resultNote(app, "language"), true)
}

// deviceNow is the device's clock now: what the app said, moved on by the time since it said it.
func (u *UI) deviceNow(dev *DeviceInfo) time.Time {
	el := u.now().Sub(dev.At)
	if el < 0 || el > time.Hour || dev.At.IsZero() {
		el = 0
	}
	return dev.Now.Add(el)
}

// zoneOffset is the zone's UTC offset as the app lists it, else the device clock's own.
func zoneOffset(app *AppSnapshot, zone string, t time.Time) string {
	for _, z := range app.Zones {
		if z.ID == zone && z.Offset != "" {
			return z.Offset
		}
	}
	if t.IsZero() {
		return ""
	}
	_, off := t.Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	return sprintf("%s%02d:%02d", sign, off/3600, off/60%60)
}

func (u *UI) dateText(t time.Time) string {
	tx := u.Tx
	return sprintf(tx.DateF, tx.Weekdays[t.Weekday()], t.Day(), tx.MonthsShort[t.Month()-1], t.Year())
}

func (u *UI) drawTimePage(d *dc, app *AppSnapshot) {
	lay, tx, s := u.Lay, u.Tx, d.s
	m := float64(lay.M)
	dev := app.Device
	if !u.devicePageReady(d, app.DeviceAPI, dev != nil) {
		return
	}
	now := u.deviceNow(dev)
	ck := lay.TimeClock
	cy := float64(ck.Y) + float64(ck.H)/2
	w := d.text(float64(ck.X)+4*s, cy+16*s, sprintf("%02d:%02d", now.Hour(), now.Minute()), 46*s,
		textOpt{col: cInk, bold: true})
	d.text(float64(ck.X)+4*s+w+2*m, cy-2*s, u.dateText(now), 19*s, textOpt{col: cInk,
		width: float64(ck.W) - w - 3*m})
	zone := dev.Timezone
	zsub := tx.TimeZoneNone
	if zone != "" {
		zsub = strings.ReplaceAll(zone, "_", " ")
		if off := zoneOffset(app, zone, now); off != "" {
			zsub += tx.Sep + sprintf(tx.UTCOffsetF, off)
		}
	}
	d.text(float64(ck.X)+4*s+w+2*m, cy+22*s, zsub, 15*s, textOpt{col: cQuiet, width: float64(ck.W) - w - 3*m})
	busy := app.Pending == "ntp" || app.Pending == "timezone" || app.Pending == "time"
	sub, tone := tx.TimeAutoOff, "quiet"
	if dev.NTP {
		sub, tone = tx.TimeAutoWaiting, "warn"
		if dev.Synced {
			sub, tone = tx.TimeAutoSynced, "good"
		}
	}
	u.row(d, "s_ntp", "clock", tx.TimeAuto, RowState{knob(dev.NTP), busy, sub, tone}, dev.NTP && dev.Synced, 0)
	u.row(d, "s_zone", "globe", tx.TimeZoneRow, RowState{Sub: zsub, Tone: "quiet"}, false, 0)
	zr := lay.Buttons["s_zone"]
	d.text(float64(zr.X+zr.W)-2*m, float64(zr.Y)+float64(zr.H)/2+8*s, "›", 26*s, textOpt{col: cQuiet, align: right})
	if !dev.NTP {
		u.drawTimeSteppers(d, now)
	}
	note := u.resultNote(app, "ntp", "timezone", "time")
	if note == "" && !dev.NTP {
		u.noteLine(d, tx.TimeManualHelp, false)
	} else {
		u.noteLine(d, note, true)
	}
}

// drawTimeSteppers: − value + for the day, month, year, hour and minute of the draft, and «Set».
func (u *UI) drawTimeSteppers(d *dc, now time.Time) {
	lay, tx, s := u.Lay, u.Tx, d.s
	t := u.timeDraft
	if !u.timeDraftSet {
		t = now
	}
	vals := map[string]string{"day": strconv.Itoa(t.Day()), "mon": tx.MonthsShort[t.Month()-1],
		"year": strconv.Itoa(t.Year()), "hour": sprintf("%02d", t.Hour()), "min": sprintf("%02d", t.Minute())}
	for _, f := range TimeFields {
		mk, pk := "s_t_"+f+"_m", "s_t_"+f+"_p"
		d.button(mk, btn{label: tx.Minus})
		d.button(pk, btn{label: tx.Plus})
		a, b := lay.Buttons[mk], lay.Buttons[pk]
		cx := float64(a.X+a.W+b.X) / 2
		d.text(cx, float64(a.Y)+float64(a.H)/2+8*s, vals[f], 22*s, textOpt{col: cInk, bold: true, align: center,
			width: float64(b.X - a.X - a.W)})
	}
	d.button("s_t_set", btn{label: tx.TimeSet, active: true})
}

// regionOf is a zone's region for the picker: Europe/Madrid → Europe; UTC, Etc/… and the rest → Other.
func regionOf(zone string) string {
	r, _, ok := strings.Cut(zone, "/")
	if ok {
		switch r {
		case "Africa", "America", "Antarctica", "Arctic", "Asia", "Atlantic", "Australia", "Europe", "Indian", "Pacific":
			return r
		}
	}
	return "Other"
}

// Regions are the zone picker's regions, in order.
var Regions = []string{"Africa", "America", "Antarctica", "Arctic", "Asia", "Atlantic", "Australia", "Europe",
	"Indian", "Pacific", "Other"}

// zoneLabel is a zone without its region, with spaces: America/Argentina/Buenos_Aires → Argentina/Buenos Aires.
func zoneLabel(zone string) string {
	if regionOf(zone) != "Other" {
		_, zone, _ = strings.Cut(zone, "/")
	}
	return strings.ReplaceAll(zone, "_", " ")
}

// zonesIn is the zones of a region, in the app's order.
func zonesIn(zones []TimeZone, region string) []TimeZone {
	var out []TimeZone
	for _, z := range zones {
		if regionOf(z.ID) == region {
			out = append(out, z)
		}
	}
	return out
}

// drawZones is the time zone picker: the regions as chips, the zones of the selected one in a grid with pages;
// the current zone highlighted.
func (u *UI) drawZones(d *dc, app *AppSnapshot) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	m := float64(lay.M)
	if app.Zones == nil {
		t, warn := tx.ZonesLoading, false
		if app.ZonesErr != "" {
			t, warn = app.ZonesErr, true
		}
		u.pageMessage(d, t, "", warn)
		return
	}
	cur := ""
	if app.Device != nil {
		cur = app.Device.Timezone
	}
	for i, r := range Regions {
		key := "s_region_" + strconv.Itoa(i)
		d.button(key, btn{label: tx.Regions[r], active: r == u.zoneRegion})
	}
	zs := zonesIn(app.Zones, u.zoneRegion)
	per := lay.ZoneCols * lay.ZoneRows
	pages := max(1, (len(zs)+per-1)/per)
	u.zonePage = max(0, min(u.zonePage, pages-1))
	lo := u.zonePage * per
	items := zs[min(lo, len(zs)):min(len(zs), lo+per)]
	u.zoneShown = u.zoneShown[:0]
	for i, z := range items {
		key := "s_zone_" + strconv.Itoa(i)
		u.zoneShown = append(u.zoneShown, z.ID)
		r := lay.Buttons[key]
		active := z.ID == cur
		q := rrOf(r, 8*s)
		if active {
			c.FillRR(q, cBrand.WithAlpha(0.24))
			c.StrokeRR(q, 2, cBrand)
		} else {
			c.FillRR(q, cPanel)
			c.StrokeRR(q, 1.25, cLine)
		}
		if d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
			d.lit(r, 8*s, 0.14)
		}
		mid := float64(r.Y) + float64(r.H)/2
		ow := 0.0
		if z.Offset != "" {
			ow = d.text(float64(r.X+r.W)-m, mid+6*s, z.Offset, 13*s, textOpt{col: cQuiet, align: right}) + m/2
		}
		d.text(float64(r.X)+m, mid+6*s, zoneLabel(z.ID), 16*s, textOpt{col: cInk, bold: active,
			width: float64(r.W) - 2*m - ow})
	}
	if pages > 1 {
		if u.zonePage > 0 {
			d.button("s_prev", btn{label: tx.PrevPage})
		}
		if u.zonePage < pages-1 {
			d.button("s_next", btn{label: tx.NextPage})
		}
		u.pagerNote(d, sprintf(tx.PageF, u.zonePage+1, pages), false)
	}
}

// updateAvailable: there is a release to install (the Updates entry's dot, and the gear's).
func updateAvailable(app *AppSnapshot) bool {
	up := app.Update
	return up != nil && up.Available != nil && (up.State == "available" || up.State == "ready")
}

// streaming: the output is on (an update must not start; the app also refuses it).
func (u *UI) streaming() bool { return u.st.Output.Mode == "on" }

func (u *UI) drawUpdatesPage(d *dc, app *AppSnapshot) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	up := app.Update
	if !u.devicePageReady(d, app.UpdateAPI, up != nil) {
		return
	}
	pg := lay.SettingsPage
	x0, y0, w0 := float64(pg.X), float64(pg.Y), float64(pg.W)
	cur := up.Current
	if cur == "" && app.Device != nil {
		cur = app.Device.Release
	}
	if cur == "" {
		cur = tx.AboutUnknown
	}
	d.text(x0+4*s, y0+34*s, sprintf(tx.UpdVersionF, cur), 28*s, textOpt{col: cInk, bold: true, width: w0})
	var stTxt string
	tone := cQuiet
	ver := ""
	if up.Available != nil {
		ver = up.Available.Version
	}
	switch up.State {
	case "checking":
		stTxt = tx.UpdChecking
	case "available":
		stTxt, tone = sprintf(tx.UpdAvailableF, ver), cBrand
		if up.Available != nil && up.Available.Size > 0 {
			mb := strings.ReplaceAll(sprintf("%.1f", float64(up.Available.Size)/1e6), ".", tx.Decimal)
			stTxt += tx.Sep + sprintf(tx.UpdSizeF, mb)
		}
	case "downloading":
		stTxt = sprintf(tx.UpdDownloadingF, ver, int(up.Progress*100+0.5))
	case "ready":
		stTxt, tone = tx.UpdReady, cBrand
	case "installing":
		stTxt, tone = tx.UpdInstalling, cWarn
	case "rolled_back":
		stTxt, tone = tx.UpdRolledBack, cWarn
		if t, c := u.updateRunText(up); t != "" {
			stTxt, tone = t, c // the installer's own account: which version, and why
		}
	case "error":
		stTxt, tone = sprintf(tx.UpdErrorF, u.updateError(up)), cWarn
	default:
		stTxt, tone = tx.UpdUpToDate, cGood
		if t, c := u.updateRunText(up); t != "" {
			stTxt, tone = t, c // «Updated to X», or the last run's failure until it is dismissed
		}
	}
	style := Sans
	if tone != cQuiet {
		style = SansBold
	}
	sy := y0 + 64*s
	for i, line := range d.wrap(stTxt, 17*s, style, w0-8*s) { // up to two lines (the app's reasons are long)
		if i == 2 {
			break
		}
		d.text(x0+4*s, sy, line, 17*s, textOpt{col: tone, bold: tone != cQuiet, width: w0 - 8*s})
		sy += 22 * s
	}
	lc := tx.UpdNever
	if up.LastCheck != nil {
		t := up.LastCheck.In(time.Local)
		if app.Device != nil && !app.Device.Now.IsZero() {
			t = up.LastCheck.In(app.Device.Now.Location())
		}
		lc = sprintf(tx.UpdLastCheckF, sprintf("%d %s %02d:%02d", t.Day(), tx.MonthsShort[t.Month()-1], t.Hour(), t.Minute()))
	}
	d.text(x0+4*s, sy, lc, 14*s, textOpt{col: cQuiet, width: w0 - 8*s})
	if up.State == "downloading" || up.State == "installing" {
		br := lay.Buttons["s_up_check"]
		by := float64(br.Y) - 8*s
		c.FillRR(RR{x0, by - 4*s, w0, 4 * s, 2 * s}, cLine)
		f := up.Progress
		if up.State == "installing" {
			f = 1
		}
		c.FillRR(RR{x0, by - 4*s, w0 * f, 4 * s, 2 * s}, cBrand)
	}
	busy := up.State == "checking" || up.State == "downloading" || up.State == "installing" ||
		app.Pending == "update_check" || app.Pending == "update_install"
	d.button("s_up_check", btn{label: tx.UpdCheck, dim: busy})
	note := u.resultNote(app, "update_check", "update_install")
	if u.installShown(app) {
		conf := u.confirm != nil && u.confirm.what == "update"
		label := sprintf(tx.UpdInstallF, ver)
		if conf {
			label = tx.UpdConfirm
		}
		ok, why := u.installEnabled(app)
		d.button("s_up_install", btn{label: label, active: ok, col: colp(map[bool]Color{true: cWarn,
			false: cBrand}[conf]), dim: !ok})
		if !ok && note == "" {
			note = why
		}
	}
	if note == "" && up.TestBuild {
		note = tx.UpdTestBuild
	}
	// The release notes
	if up.Available != nil && up.Available.Notes != "" {
		nb := lay.UpdateNotes
		nx, ny, nw, nh := float64(nb.X), float64(nb.Y), float64(nb.W), float64(nb.H)
		d.text(nx+4*s, ny+4*s, tx.UpdNotes, 15*s, textOpt{col: cQuiet, bold: true})
		ly := ny + 28*s
		maxY := ny + nh - 26*s
		for _, para := range strings.Split(up.Available.Notes, "\n") {
			for _, line := range d.wrap(para, 15*s, Sans, nw-8*s) {
				if ly > maxY {
					break
				}
				d.text(nx+4*s, ly, line, 15*s, textOpt{col: cInk, width: nw - 8*s})
				ly += 21 * s
			}
		}
	}
	u.noteLine(d, note, true)
}

// installEnabled: «Update to X» can be pressed, else why not. The app says it (canInstall, blockedMessage, in
// the device language); an older app does not, and then the screen's own rule: not while streaming or busy.
func (u *UI) installEnabled(app *AppSnapshot) (bool, string) {
	up, tx := app.Update, u.Tx
	busy := up.State == "checking" || up.State == "downloading" || up.State == "installing" ||
		app.Pending == "update_check" || app.Pending == "update_install"
	if up.CanInstall != nil {
		if *up.CanInstall && !busy {
			return true, ""
		}
		switch {
		case up.BlockedMessage != "":
			return false, capFirst(up.BlockedMessage)
		case up.Blocked == "streaming":
			return false, tx.UpdStreaming
		}
		return false, ""
	}
	if u.streaming() {
		return false, tx.UpdStreaming
	}
	return !busy, ""
}

// updateError is what failed, in words: the app's message (device language), else its code.
func (u *UI) updateError(up *UpdateInfo) string {
	switch {
	case up.Message != "":
		return up.Message
	case up.Error != "":
		return up.Error
	}
	return u.Tx.AboutUnknown
}

// updateRunText is the installer's last run as a line: «Updated to X» (good) once the device runs it, or the
// update that was undone or failed, with the app's message (warn). "" when there is nothing to say.
func (u *UI) updateRunText(up *UpdateInfo) (string, Color) {
	r, tx := up.LastResult, u.Tx
	if r == nil {
		return "", cQuiet
	}
	msg := r.Message
	if msg == "" {
		msg = r.Error
	}
	to := r.To
	if to == "" {
		to = tx.AboutUnknown
	}
	switch r.State {
	case "installed":
		if r.To != "" && (up.Current == "" || up.Current == r.To) {
			return sprintf(tx.UpdInstalledF, r.To), cGood
		}
	case "rolled_back":
		if msg == "" {
			return tx.UpdRolledBack, cWarn
		}
		return sprintf(tx.UpdUndoneF, to, msg), cWarn
	case "failed", "refused":
		if msg == "" {
			msg = tx.AboutUnknown
		}
		return sprintf(tx.UpdFailedF, to, msg), cWarn
	}
	return "", cQuiet
}

// capFirst is a sentence's first letter in capitals (the app's messages are fragments: «deja de emitir…»).
func capFirst(t string) string {
	for i, r := range t {
		return strings.ToUpper(string(r)) + t[i+len(string(r)):]
	}
	return t
}

// installShown: «Update to X» is there (a release available or downloaded; or one installing).
func (u *UI) installShown(app *AppSnapshot) bool {
	up := app.Update
	return up != nil && up.Available != nil && (up.State == "available" || up.State == "ready" ||
		up.State == "downloading")
}

func (u *UI) drawAboutPage(d *dc, app *AppSnapshot) {
	lay, tx, s := u.Lay, u.Tx, d.s
	m := float64(lay.M)
	dev := app.Device
	if !u.devicePageReady(d, app.DeviceAPI, dev != nil) {
		return
	}
	commit := app.Commit
	if commit == "" && app.Update != nil {
		commit = app.Update.Versions.Commit // the updater reads BUILD too
	}
	or := func(v string) string {
		if v == "" {
			return tx.AboutUnknown
		}
		return v
	}
	web := tx.AboutUnknown
	if dev.Hostname != "" {
		web = "http://" + dev.Hostname + ".local"
	}
	ips := tx.AboutUnknown
	if len(app.IPs) > 0 {
		ips = strings.Join(app.IPs, tx.Sep)
	}
	lines := [][2]string{{tx.AboutRelease, or(dev.Release)}, {tx.AboutApp, or(dev.AppVer)}, {tx.AboutEngine, or(dev.EngineVer)},
		{tx.AboutCommit, or(commit)}, {tx.AboutName, or(dev.Name)}, {tx.AboutHost, or(dev.Hostname)},
		{tx.AboutIPs, ips}, {tx.AboutWeb, web}}
	al := lay.AboutLines
	x0, y0, w0 := float64(al.X), float64(al.Y), float64(al.W)
	lw := 0.0
	for _, l := range lines {
		lw = max(lw, d.advance(l[0], 16*s, false))
	}
	lw = min(lw+4*s, w0*0.45) // fit measures ink, a little wider than the advance
	step := min(40*s, float64(al.H)/float64(len(lines)))
	for i, l := range lines {
		y := y0 + 26*s + float64(i)*step
		d.text(x0+4*s, y, l[0], 16*s, textOpt{col: cQuiet, width: lw})
		d.text(x0+4*s+lw+2*m, y, l[1], 17*s, textOpt{col: cInk, bold: true, width: w0 - lw - 3*m})
	}
	if app.SetupAPI == Yes {
		conf := u.confirm != nil && u.confirm.what == "setup_again"
		label := tx.SetupAgain
		if app.Setup != nil && !app.Setup.Done {
			label = tx.SetupContinue
		}
		if conf {
			label = tx.SetupAgainConfirm
		}
		d.button("s_setup_again", btn{label: label, active: conf, col: colp(cWarn)})
		if note := u.resultNote(app, "setup_restart"); note != "" { // above the button (the page's bottom is it)
			b := lay.Buttons["s_setup_again"]
			d.text(x0+4*s, float64(b.Y)-10*s, note, 15*s, textOpt{col: cWarn, bold: true, width: w0 - 8*s})
		}
	}
}

// wrap splits a text in lines that fit a width (by words; a word longer than the line is cut by fit).
func (d *dc) wrap(t string, size float64, style FontStyle, width float64) []string {
	var out []string
	line := ""
	for _, w := range strings.Fields(t) {
		try := w
		if line != "" {
			try = line + " " + w
		}
		if adv, _ := d.c.Fonts.Extents(try, size, style); adv <= width || line == "" {
			line = try
			continue
		}
		out = append(out, line)
		line = w
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// setupPending: the setup is not done and its wizard was left for later (the gear's and About's amber dot).
func setupPending(app *AppSnapshot) bool {
	return app.SetupAPI == Yes && app.Setup != nil && !app.Setup.Done
}

// apBusy: a change of the board's access point is under way, by either of the screen's clients: NetState (its name,
// «Create my Wi-Fi») or the device client (its channel, its password, the cameras' way over it). The app applies them
// on the same netcfg: one at a time, so the AP does not restart twice under the cameras.
func (u *UI) apBusy() bool {
	if p := u.Net.Snapshot().Pending; p != nil && (p.What == "ssid" || p.What == "ap") {
		return true
	}
	return strings.HasPrefix(u.app().AppSnapshot().Pending, "ap_")
}
