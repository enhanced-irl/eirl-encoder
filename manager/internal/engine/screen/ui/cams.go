package ui

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// The Network cameras page (MIXER.md §21, NETWORK_SOURCES.md §6.11-§6.12): the reception switch, the two ways in
// (over the encoder's Wi-Fi, by cable), the cameras with «Add camera», and each camera's own view: its name, how it
// connects over each way in (the SRT URL with its QR, the RTMP server and key: dots until their eye is opened),
// «New key» and «Remove». What the app says of the network comes through Net (GET /network, polled as before); the
// changes and the URLs with their keys, through App (cam_*, ap_enabled, lan_enabled, cam_creds). The URLs are asked
// only when an eye is opened and dropped when the camera's view or the page goes.

// CamNameMax is a camera's longest name (netcfg's).
const CamNameMax = 32

// reCamName is the app's rule for a camera's name (netcfg.ValidName; a test checks they agree): 1-32 letters,
// digits, spaces and . _ ( ) ' -, with no space at either end.
var reCamName = regexp.MustCompile(`^[\p{L}\p{N} ._()'-]{1,32}$`)

// CamNameOK: a valid camera name (the screen checks it before sending; the app checks it again).
func CamNameOK(n string) bool { return reCamName.MatchString(n) && strings.TrimSpace(n) == n }

// camNameRune: what the name's keyboard types (its keys are ASCII: the letters, digits and punctuation the app takes).
func camNameRune(r rune) bool {
	return r == ' ' || strings.ContainsRune("._()'-", r) || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// camsState is what the screen keeps of the Network cameras page: the camera open in its own view, its way in on
// view, the list's page and what was drawn on it (MIXER.md §21).
type camsState struct {
	view  string   // the camera open in its own view (its id); "" the list
	way   string   // its view's way in: wifi | lan ("" the first there is)
	page  int      // the list's page
	shown []string // the camera of each list row, as drawn: a tap goes to what was seen
	pages int
	added string // the name of the camera last added (its notice once the app takes it)
	viaOn bool   // what the way-in switch under way asked for (ap_enabled, lan_enabled)
	// wifiEyeWiz: the Wi-Fi password's eye was opened in the wizard (else in Settings): the Wi-Fi page's (settings.go)
	wifiEyeWiz bool
}

// The camera view's eyes.
const (
	camSRTEye  = "s_camv_srt_eye"
	camRTMPEye = "s_camv_rtmp_eye"
)

func init() {
	registerPage("cams", &pageDef{
		layout: (*Layout).camsLayout,
		draw: func(u *UI, d *dc, _ *AppSnapshot) {
			net := u.Net.Snapshot()
			var cams []SourceStatus
			for _, x := range u.st.Sources {
				if x.Kind == "network" {
					cams = append(cams, x)
				}
			}
			u.drawCamsPage(d, &net, cams, u.legacyCreds(&net))
		},
		title: (*UI).camsTitle,
		keys:  (*UI).camsKeys,
		touch: (*UI).camsTouch,
		leave: (*UI).camsLeave,
	})
}

// camsLayout places the page: under the reception row (s_net, SettingsRows'), the two ways in, the list's rows and
// at the bottom «Add camera» (with the list's pages at the left when they do not fit); and a camera's view: its name,
// «New key» and «Remove» on top, the way in under them, then its SRT and RTMP fields (with their eyes) on the left
// and the SRT QR's square on the right.
func (l *Layout) camsLayout() {
	c, m, hb := l.SettingsPage, l.M, l.HB
	b := l.Buttons
	gap := m / 2
	rh := hb + hb/6
	b["s_cams_wifi"] = Rect{c.X, c.Y + rh + gap, c.W, rh}
	b["s_cams_lan"] = Rect{c.X, c.Y + 2*(rh+gap), c.W, rh}
	bh := max(52, hb+6)
	aw := max(220, c.W*36/100)
	by := c.Y + c.H - bh
	b["s_cams_add"] = Rect{c.X + c.W - aw, by, aw, bh}
	pw := max(56, bh)
	b["s_cams_prev"] = Rect{c.X, by, pw, bh}
	b["s_cams_next"] = Rect{c.X + pw + gap, by, pw, bh}
	for i := range l.camRows() {
		b["s_cam_"+strconv.Itoa(i)] = Rect{c.X, c.Y + (3+i)*(rh+gap), c.W, rh}
	}
	// A camera's view
	fh := max(56, hb*6/5)
	bw := max(176, c.W/4) // «Touchez encore» fits when armed
	b["s_camv_remove"] = Rect{c.X + c.W - bw, c.Y, bw, fh}
	b["s_camv_key"] = Rect{c.X + c.W - 2*bw - gap, c.Y, bw, fh}
	b["s_camv_name"] = Rect{c.X, c.Y, c.W - 2*bw - 2*gap, fh}
	y1 := c.Y + fh + gap
	sh, ww := max(48, hb), max(150, c.W/5)
	b["s_camv_wifi"] = Rect{c.X, y1, ww, sh}
	b["s_camv_lan"] = Rect{c.X + ww + gap, y1, ww, sh}
	y2 := y1 + sh + gap
	lh := c.Y + c.H - y2
	qs := min(lh, c.W*2/5)
	b["s_camv_qr"] = Rect{c.X + c.W - qs, y2, qs, qs} // not a button: the QR's square
	lw := c.W - qs - m
	f2 := max(80, hb*7/4)
	b["s_camv_srt"] = l.eyeKey(Rect{c.X, y2, lw, f2}, camSRTEye)
	b["s_camv_rtmp"] = l.eyeKey(Rect{c.X, y2 + f2 + gap, lw, f2}, camRTMPEye)
	ny := y2 + 2*(f2+gap)
	b["s_camv_note"] = Rect{c.X, ny, lw, c.Y + c.H - ny} // not a button: the view's notes
}

// camRows is how many cameras the list shows at once: the rows that fit between the ways in and «Add camera».
func (l *Layout) camRows() int {
	c, m, hb := l.SettingsPage, l.M, l.HB
	gap := m / 2
	rh := hb + hb/6
	top := c.Y + 3*(rh+gap)
	bottom := c.Y + c.H - max(52, hb+6) - gap
	return max(1, (bottom-top+gap)/(rh+gap))
}

// camItem is a camera of the list: the app's (GET /network) with, if the engine has it, its source's state.
type camItem struct {
	NetCamera
	src *SourceStatus // the engine's net:<id> source (nil: not in the mixer)
}

// camItems are the cameras to list and whether they are the app's (else the engine's network sources: an app that
// does not list them; nothing can be done with those but look).
func (u *UI) camItems(net *NetSnapshot) ([]camItem, bool) {
	srcs := map[string]*SourceStatus{}
	var engine []camItem
	for i := range u.st.Sources {
		x := &u.st.Sources[i]
		if x.Kind != "network" {
			continue
		}
		id := strings.TrimPrefix(x.ID, "net:")
		srcs[id] = x
		engine = append(engine, camItem{NetCamera{ID: id, Name: x.Name, Connected: x.State == "live"}, x})
	}
	if st := net.Status; st != nil && st.CamItems != nil {
		out := make([]camItem, 0, len(st.CamItems))
		for _, c := range st.CamItems {
			out = append(out, camItem{c, srcs[c.ID]})
		}
		return out, true
	}
	return engine, false
}

// camByID is a camera of the app's list.
func (u *UI) camByID(net *NetSnapshot, id string) (camItem, bool) {
	items, app := u.camItems(net)
	for _, c := range items {
		if app && c.ID == id {
			return c, true
		}
	}
	return camItem{}, false
}

// camsReady: the page has the app's cameras (a switch or a camera can be touched).
func camsReady(net *NetSnapshot) bool {
	return net.API == Yes && net.Status != nil && net.Status.CamItems != nil
}

// camStateText is a camera's state: why the mixer does not get it well (the engine's detail), else the app's
// «Connected · SRT · over Wi-Fi · H.265 · 4.6 Mbps · 0.3 % lost» (short: «Connected · SRT · 4.6 Mbps · 0.3 % lost»,
// for a camera's view, where the way in and its picture are beside it), else «Waiting for the camera»; and its tone.
func (u *UI) camStateText(c camItem, short bool) (string, string) {
	tx, nx := u.Tx, u.Tx.NetX
	if x := c.src; x != nil && x.State != "live" && (x.DetailCode != "" || x.Detail != "") && x.DetailCode != "net_waiting" {
		tone := "quiet"
		if c.Connected {
			tone = "warn"
		}
		return capFirst(u.notLiveText(x.State, x.DetailCode, x.DetailArgs, x.Detail, true)), tone
	}
	if !c.Connected {
		return nx.CamWaiting, "quiet"
	}
	parts := []string{nx.CamConnected, strings.ToUpper(c.Protocol)}
	if short {
		c.Via, c.Codec, c.src = "", "", nil
	}
	switch c.Via {
	case "wifi":
		parts = append(parts, nx.CamByWifi)
	case "lan":
		parts = append(parts, nx.CamByCable)
	}
	switch c.Codec {
	case "h264":
		parts = append(parts, "H.264")
	case "h265":
		parts = append(parts, "H.265")
	}
	if c.Kbps != nil {
		parts = append(parts, strings.ReplaceAll(sprintf(tx.MbpsF, float64(*c.Kbps)/1000), ".", tx.Decimal))
	}
	if c.LossPct != nil && *c.LossPct > 0 {
		parts = append(parts, sprintf(nx.CamLostF, strings.ReplaceAll(sprintf("%.1f", *c.LossPct), ".", tx.Decimal)))
	}
	if x := c.src; x != nil && x.State == "live" && x.Mode != "" {
		parts = append(parts, ModeLabel(x.Mode, x.FPS, tx))
	}
	return joinNonEmpty(parts, tx.Sep), "good"
}

// --- the two ways in ---

// viaWifiRow is «Through the encoder's Wi-Fi»: the cameras over «Create my Wi-Fi» (PUT /network/ap {"enabled"}).
func (u *UI) viaWifiRow(st *NetStatus, app *AppSnapshot) RowState {
	tx, nx := u.Tx, u.Tx.NetX
	ap := st.AccessPoint
	on := ap.Enabled != nil && *ap.Enabled
	if app.Pending == "ap_enabled" {
		sub := tx.TurningOff
		if u.camp.viaOn {
			sub = tx.TurningOn
		}
		return RowState{knob(u.camp.viaOn), true, sub, "quiet"}
	}
	asked := ap.Policy == "always" || (ap.Policy == "" && on) // an app without the policy: this switch drove the Wi-Fi
	switch {
	case !on:
		return RowState{KnobOff, false, nx.ViaOff, "quiet"}
	case u.confirming("via_wifi"):
		return RowState{KnobOn, false, tx.TapAgain, "warn"}
	case !asked:
		return RowState{KnobOn, false, nx.ViaAPOff, "warn"}
	case ap.State == "on":
		return RowState{KnobOn, false, sprintf(nx.ViaReceivingF, ap.SSID), "good"}
	case ap.State == "starting":
		return RowState{KnobOn, false, tx.TurningOn, "quiet"}
	case ap.State == "error":
		why := ap.Error
		if why == "" {
			why = tx.AboutUnknown
		}
		return RowState{KnobOn, false, sprintf(nx.StateErrorF, why), "warn"}
	}
	return RowState{KnobOn, false, tx.Off, "quiet"}
}

// viaLANRow is «By cable»: the cameras on the same network as the encoder's eth0 (PUT /network/lan {"enabled"}).
func (u *UI) viaLANRow(st *NetStatus, app *AppSnapshot, cams int) RowState {
	tx, nx := u.Tx, u.Tx.NetX
	lan := st.LAN
	iface := lan.Iface
	if iface == "" {
		iface = "eth0"
	}
	ip := lan.IP
	if ip == "" {
		ip = nx.NoIP
	}
	where := iface + tx.Sep + ip
	if app.Pending == "lan_enabled" {
		sub := tx.TurningOff
		if u.camp.viaOn {
			sub = tx.TurningOn
		}
		return RowState{knob(u.camp.viaOn), true, sub, "quiet"}
	}
	switch {
	case !st.LANEnabled:
		return RowState{KnobOff, false, nx.ViaOff + tx.Sep + where, "quiet"}
	case u.confirming("via_lan"):
		return RowState{KnobOn, false, tx.TapAgain, "warn"}
	case lan.State == "error":
		why := lan.Error
		if why == "" {
			why = tx.AboutUnknown
		}
		return RowState{KnobOn, false, sprintf(nx.StateErrorF, why), "warn"}
	case lan.IP == "":
		return RowState{KnobOn, false, sprintf(nx.CableNoIPF, iface), "warn"}
	case lan.State == "on":
		return RowState{KnobOn, false, nx.ViaReceiving + tx.Sep + where, "good"}
	case lan.State == "starting":
		return RowState{KnobOn, false, tx.TurningOn + tx.Sep + where, "quiet"}
	case cams == 0:
		return RowState{KnobOn, false, where + tx.Sep + nx.CableWhenAdd, "quiet"}
	}
	return RowState{KnobOn, false, nx.ViaOff + tx.Sep + where, "quiet"}
}

// toggleVia turns a way in on or off (ap_enabled | lan_enabled). Off with cameras, a second tap: those coming in
// that way disconnect. Both off is the app's to refuse (409 no_input, its message on the page).
func (u *UI) toggleVia(what string) {
	snap := u.Net.Snapshot()
	if !camsReady(&snap) {
		return
	}
	st := snap.Status
	app := u.app().AppSnapshot()
	if app.Pending == "ap_enabled" || app.Pending == "lan_enabled" || (what == "ap_enabled" && u.apBusy()) {
		u.appBusy(false)
		return
	}
	on, ask := st.LANEnabled, "via_lan"
	if what == "ap_enabled" {
		on, ask = st.AccessPoint.Enabled != nil && *st.AccessPoint.Enabled, "via_wifi"
	}
	if on && len(st.CamItems) > 0 && !u.confirmTap(ask) {
		return
	}
	ok := u.app().Do(AppAction{What: what, Body: map[string]any{"enabled": !on}})
	if ok {
		u.camp.viaOn = !on
	}
	u.appBusy(ok)
}

// --- the page ---

// drawCamsPage is the Network cameras page: the reception row, the ways in and the list; or a camera's view.
func (u *UI) drawCamsPage(d *dc, net *NetSnapshot, cams []SourceStatus, legacy *WifiCreds) {
	if net.API == No {
		u.drawCamsOld(d, net, cams, legacy)
		return
	}
	if !u.netPageReady(d, net) {
		return
	}
	if u.camp.view != "" {
		u.drawCamView(d, net)
		return
	}
	lay, tx, nx, s := u.Lay, u.Tx, u.Tx.NetX, d.s
	m := float64(lay.M)
	st := net.Status
	app := u.app().AppSnapshot()
	items, ours := u.camItems(net)
	u.receptionRow(d, net)
	ready := camsReady(net)
	if ready {
		wr := u.viaWifiRow(st, &app)
		u.row(d, "s_cams_wifi", "hotspot", nx.ViaWifi, wr, wr.Tone == "good", 0)
		lr := u.viaLANRow(st, &app, len(items))
		u.row(d, "s_cams_lan", "globe", nx.ViaCable, lr, lr.Tone == "good", 0)
	}
	// The list, in pages
	rows := lay.camRows()
	pages := max(1, (len(items)+rows-1)/rows)
	u.camp.page = max(0, min(u.camp.page, pages-1))
	u.camp.pages = pages
	lo := u.camp.page * rows
	u.camp.shown = u.camp.shown[:0]
	first := lay.Buttons["s_cam_0"]
	if len(items) == 0 {
		add := lay.Buttons["s_cams_add"]
		x0, w0 := float64(first.X), float64(first.W)
		cy := (float64(first.Y) + float64(add.Y)) / 2
		d.text(x0+w0/2, cy-14*s, tx.NoCameras, 20*s, textOpt{col: cInk, bold: true, align: center})
		y := cy + 14*s
		for _, line := range d.wrapText(tx.NoCamerasHelp, 15*s, Sans, w0-16*s) {
			d.text(x0+w0/2, y, line, 15*s, textOpt{col: cQuiet, align: center, width: w0 - 8*s})
			y += 21 * s
		}
	}
	for i, c := range items[min(lo, len(items)):min(len(items), lo+rows)] {
		key := "s_cam_" + strconv.Itoa(i)
		if ours {
			u.camp.shown = append(u.camp.shown, c.ID)
		}
		sub, tone := u.camStateText(c, false)
		r := lay.Buttons[key]
		right := 0.0
		if ours {
			right = float64(r.X+r.W) - 2*m - 18*s
		}
		u.row(d, key, "camera", c.Name, RowState{KnobNone, false, sub, tone}, tone == "good", right)
		if ours {
			u.chevron(d, r)
			if d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
				d.lit(r, 10*s, 0.12)
			}
		}
	}
	// The bottom: the pages, «Add camera» and the page's notes
	nx0 := float64(lay.Buttons["s_cam_0"].X)
	if pages > 1 {
		d.button("s_cams_prev", btn{label: "‹", dim: u.camp.page == 0})
		d.button("s_cams_next", btn{label: "›", dim: u.camp.page >= pages-1})
		nb := lay.Buttons["s_cams_next"]
		nx0 = float64(nb.X+nb.W) + m
	}
	if !ready {
		return
	}
	full := st.CamMax > 0 && len(items) >= st.CamMax
	label := nx.AddCamera
	if app.Pending == "cam_add" {
		label = nx.Adding
	}
	d.button("s_cams_add", btn{label: label, dim: full || app.Pending == "cam_add" || !st.Enabled})
	note, warn := u.camsNote(&app, len(items), full)
	if note == "" && !st.Enabled { // the app adds a camera only with the reception on
		note = nx.AddNeedsReception
	}
	add := lay.Buttons["s_cams_add"]
	u.boxNote(d, nx0, float64(add.Y), float64(add.X)-m-nx0, float64(add.H), note, warn)
}

// receptionRow is the RTMP/SRT reception's row: the whole row is its switch (the app's PUT /network), with how the
// cameras come in.
func (u *UI) receptionRow(d *dc, net *NetSnapshot) {
	tx := u.Tx
	st := net.Status
	ap := st.AccessPoint
	hasCams := st.Cameras > 0
	viaWifi, viaLAN := ap.Enabled != nil && *ap.Enabled, st.LANEnabled
	pend := net.Pending
	busy := pend != nil && pend.What == "net"
	on := st.Enabled
	kn := KnobNone
	if NetSwitch(net) {
		kn = knob(on)
		if busy {
			kn = knob(pend.On)
		}
	}
	apOff := viaWifi && APSwitch(net) && ap.Policy == "rescue"
	var sub, t string
	switch {
	case !hasCams && on:
		sub, t = tx.CamsSubNone, "quiet"
	case busy:
		sub, t = tx.CamsSubTurningOff, "quiet"
		if kn == KnobOn {
			sub = tx.CamsSubTurningOn
		}
	case !on:
		sub, t = tx.CamsSubOff, "warn"
	case apOff && !viaLAN:
		sub, t = tx.CamsSubAPOff, "warn"
	default:
		via := tx.CamsViaNone
		switch {
		case viaWifi && viaLAN:
			via = tx.CamsViaBoth
		case viaWifi:
			via = tx.CamsViaWifi
		case viaLAN:
			via = tx.CamsViaCable
		}
		sub, t = tx.CamsSubOnPrefix+via, "warn"
		if viaWifi || viaLAN {
			t = "good"
		}
	}
	u.row(d, "s_net", "camera", tx.CamsRow, RowState{kn, busy, sub, t}, hasCams && on && !busy, 0)
}

// camsNote is the list's note: a switch waiting for its second tap, the app's error (8 s), what was just done, the
// page's own notice; else what to do.
func (u *UI) camsNote(app *AppSnapshot, cams int, full bool) (string, bool) {
	nx := u.Tx.NetX
	switch {
	case u.confirming("via_wifi"):
		return nx.ViaWifiOffAsk, true
	case u.confirming("via_lan"):
		return nx.ViaCableOffAsk, true
	}
	if t := u.resultNote(app, "cam_add", "cam_rename", "cam_rotate", "cam_remove", "ap_enabled", "lan_enabled"); t != "" {
		return t, true
	}
	switch {
	case u.okNote(app, "cam_add") && u.camp.added != "":
		return sprintf(nx.AddedF, u.camp.added), false
	case u.okNote(app, "cam_remove"):
		return nx.Removed, false
	}
	if t, warn := u.pageNoteText(); t != "" {
		return t, warn
	}
	switch {
	case full:
		return sprintf(nx.CamsFullF, cams), false
	case cams > 0:
		return nx.TapCamera, false
	}
	return "", false
}

// boxNote is a note in a box at the bottom of a page (up to the lines that fit, wrapped), vertically centred.
func (u *UI) boxNote(d *dc, x, y, w, h float64, t string, warn bool) {
	if t == "" || w < 40 {
		return
	}
	s := d.s
	style, col := Sans, cQuiet
	if warn {
		style, col = SansBold, cWarn
	}
	lines := d.wrapText(t, 15*s, style, w)
	most := max(1, int(h/(19*s)))
	if len(lines) > most {
		lines[most-1] = strings.Join(lines[most-1:], " ")
		lines = lines[:most]
	}
	ty := y + h/2 - float64(len(lines)-1)*19*s/2 + 5*s
	for _, l := range lines {
		d.text(x, ty, l, 15*s, textOpt{col: col, bold: warn, width: w})
		ty += 19 * s
	}
}

// drawCamsOld is the page for an older app without the local API: what the engine sees (its network sources) and
// how they come in, set up on the web.
func (u *UI) drawCamsOld(d *dc, net *NetSnapshot, cams []SourceStatus, legacy *WifiCreds) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	hasCams := legacy != nil
	u.row(d, "s_net", "camera", tx.CamsRow, RowState{KnobNone, false, tx.CamsSubOldApp, "quiet"}, false, 0)
	cr := lay.SettingsCams
	x0, y0, w0 := float64(cr.X), float64(cr.Y), float64(cr.W)
	y := y0 + 22*s
	for _, n := range netNotes(d, net) {
		d.text(x0+4*s, y, n, 16*s, textOpt{col: cWarn, bold: true, width: w0 - 8*s})
		y += 30 * s
	}
	if len(cams) > 4 {
		cams = cams[:4]
	}
	for _, cam := range cams { // each camera, as in its tile
		live := cam.State == "live"
		stTxt := tx.CamLive
		if !live {
			stTxt = u.notLiveText(cam.State, cam.DetailCode, cam.DetailArgs, cam.Detail, true)
		}
		if live && cam.Mode != "" {
			stTxt += tx.Sep + ModeLabel(cam.Mode, cam.FPS, tx)
		}
		dc := cQuiet
		if live {
			dc = cGood
		}
		c.FillCircle(x0+10*s, y-6*s, 5*s, dc)
		w1 := d.text(x0+24*s, y, cam.Name, 18*s, textOpt{col: cInk, bold: true, width: w0 * 0.5})
		d.text(x0+24*s+w1+12*s, y, stTxt, 16*s, textOpt{col: dc, width: w0 - w1 - 40*s})
		y += 32 * s
	}
	y += 10 * s
	var how []string
	if hasCams {
		if legacy.SSID != "" {
			how = append(how, sprintf(tx.HowWifiF, legacy.SSID))
		} else {
			how = append(how, tx.HowWifiOff)
		}
	}
	how = append(how, tx.HowURL)
	for _, h := range how {
		for _, line := range d.wrap(h, 15*s, Sans, w0-8*s) {
			d.text(x0+4*s, y, line, 15*s, textOpt{col: cQuiet, width: w0 - 8*s})
			y += 21 * s
		}
		y += 3 * s
	}
}

// --- a camera's view ---

// camsTitle: a camera's view has its name as the title, with ←.
func (u *UI) camsTitle() (string, bool, bool) {
	if u.camp.view == "" {
		return "", false, false
	}
	snap := u.Net.Snapshot()
	if c, ok := u.camByID(&snap, u.camp.view); ok {
		return c.Name, true, true
	}
	return u.Tx.PageTitle["cams"], true, true
}

// camWays are the ways in a camera's view shows: those switched on (over the Wi-Fi first); the Wi-Fi if none.
func camWays(st *NetStatus) []string {
	var w []string
	if st.AccessPoint.Enabled != nil && *st.AccessPoint.Enabled {
		w = append(w, "wifi")
	}
	if st.LANEnabled {
		w = append(w, "lan")
	}
	if len(w) == 0 {
		w = []string{"wifi"}
	}
	return w
}

// camWay is the way in on view.
func (u *UI) camWay(st *NetStatus) string {
	ways := camWays(st)
	for _, w := range ways {
		if w == u.camp.way {
			return w
		}
	}
	return ways[0]
}

// camURLs are the camera's URLs over a way in, if the app gave them (loaded: the app was asked and answered for
// this camera; urls nil with loaded: that way has none, the cable without an address).
func camURLs(app *AppSnapshot, id, way string) (urls *CamURLs, loaded bool) {
	for i := range app.CamCreds {
		cc := &app.CamCreds[i]
		if cc.ID != id {
			continue
		}
		if way == "lan" {
			return cc.LAN, true
		}
		return &cc.Wifi, true
	}
	return nil, false
}

// camNoURLs: the way in on view cannot have URLs (the cable without an address).
func camNoURLs(st *NetStatus, way string) bool { return way == "lan" && st.LAN.IP == "" }

func (u *UI) drawCamView(d *dc, net *NetSnapshot) {
	lay, tx, nx, c, s := u.Lay, u.Tx, u.Tx.NetX, d.c, d.s
	m := float64(lay.M)
	cam, ok := u.camByID(net, u.camp.view)
	if !ok {
		u.pageMessage(d, nx.CamGone, "", false)
		return
	}
	st := net.Status
	app := u.app().AppSnapshot()
	// Its name, «New key» and «Remove»
	u.field(d, "s_camv_name", nx.CamName, cam.Name, fieldOpt{chevron: true})
	key, rm := nx.NewKey, nx.Remove
	if app.Pending == "cam_rotate" {
		key = nx.Applying
	} else if u.confirming("cam_rotate") {
		key = tx.TapAgain
	}
	if u.confirming("cam_remove") {
		rm = tx.TapAgain
	}
	d.button("s_camv_key", btn{label: key, active: u.confirming("cam_rotate"), col: colp(cWarn),
		dim: app.Pending == "cam_rotate"})
	d.button("s_camv_remove", btn{label: rm, active: u.confirming("cam_remove"), col: colp(cBad),
		dim: app.Pending == "cam_remove"})
	// The way in (a choice when both are on), then the camera's state
	ways, way := camWays(st), u.camWay(st)
	wb := lay.Buttons["s_camv_wifi"]
	sx := float64(wb.X)
	mid := float64(wb.Y) + float64(wb.H)/2
	if len(ways) > 1 {
		d.button("s_camv_wifi", btn{label: nx.WayWifi, active: way == "wifi"})
		d.button("s_camv_lan", btn{label: nx.WayCable, active: way == "lan"})
		lb := lay.Buttons["s_camv_lan"]
		sx = float64(lb.X+lb.W) + 2*m
	} else {
		label := nx.ViaWifi
		if way == "lan" {
			label = nx.ViaCable
		}
		sx += 4*s + d.text(sx+4*s, mid+6*s, label, 17*s, textOpt{col: cInk, bold: true}) + 2*m
	}
	stt, tone := u.camStateText(cam, true)
	dot := map[string]Color{"good": cGood, "warn": cWarn, "quiet": cQuiet}[tone]
	right := float64(lay.SettingsPage.X + lay.SettingsPage.W)
	c.FillCircle(sx+5*s, mid, 5*s, dot)
	d.text(sx+16*s, mid+5*s, stt, 14*s, textOpt{col: dot, width: right - sx - 16*s})
	// Its URLs over that way: dots until an eye is opened (the app is asked then)
	urls, loaded := camURLs(&app, cam.ID, way)
	none := camNoURLs(st, way) || (loaded && urls == nil)
	reading := app.Pending == "cam_creds"
	lines := func(eye string, vals ...string) ([]string, string) {
		switch {
		case none:
			return nil, "none"
		case !u.revealed(eye):
			return nil, "hidden"
		case urls == nil && reading:
			return nil, "reading"
		case urls == nil:
			return nil, "none"
		}
		return vals, "shown"
	}
	var srt, server, rkey string
	var qr []string
	if urls != nil {
		srt, server, rkey, qr = urls.SRT, urls.RTMPServer, urls.RTMPKey, urls.SRTQR
	}
	sl, sst := lines(camSRTEye, srt)
	u.camSecret(d, "s_camv_srt", camSRTEye, nx.SRTTitle, sl, sst)
	rl, rst := lines(camRTMPEye, server, rkey)
	u.camSecret(d, "s_camv_rtmp", camRTMPEye, nx.RTMPTitle, rl, rst)
	// The SRT QR, with its eye open
	qb := lay.Buttons["s_camv_qr"]
	if sst == "shown" && len(qr) > 0 {
		n := len(qr) + 8
		side := max(2, qb.H/n) * n
		u.drawQR(c, qr, qb.X+(qb.W-side)/2, qb.Y+(qb.H-side)/2, qb.H)
	} else {
		q := rrOf(qb, 10*s)
		c.FillRR(q, cPanel)
		c.StrokeRRDash(q, 1.25, 6*s, 5*s, cLine)
		y := float64(qb.Y) + float64(qb.H)/2 - 8*s
		for _, line := range d.wrapText(nx.QRClosed, 15*s, Sans, float64(qb.W)-32*s) {
			d.text(float64(qb.X)+float64(qb.W)/2, y, line, 15*s, textOpt{col: cQuiet, align: center,
				width: float64(qb.W) - 16*s})
			y += 21 * s
		}
	}
	// Under the fields: a button waiting for its second tap, the app's error, what was just done; else what to do
	note, warn := u.camViewNote(&app, net, cam, way)
	nb := lay.Buttons["s_camv_note"]
	u.boxNote(d, float64(nb.X)+4*s, float64(nb.Y), float64(nb.W)-8*s, float64(nb.H), note, warn)
}

// camViewNote is the note under a camera's fields.
func (u *UI) camViewNote(app *AppSnapshot, net *NetSnapshot, cam camItem, way string) (string, bool) {
	tx, nx := u.Tx, u.Tx.NetX
	st := net.Status
	switch {
	case u.confirming("cam_rotate"):
		return nx.NewKeyAsk, true
	case u.confirming("cam_remove") && len(st.CamItems) <= 1:
		return nx.RemoveLastAsk, true
	case u.confirming("cam_remove"):
		return nx.RemoveAsk, true
	}
	if t := u.resultNote(app, "cam_rename", "cam_rotate", "cam_remove", "cam_creds"); t != "" {
		return t, true
	}
	if u.okNote(app, "cam_rotate") {
		return nx.NewKeyDone, false
	}
	if t, warn := u.pageNoteText(); t != "" {
		return t, warn
	}
	if way == "lan" {
		if camNoURLs(st, way) {
			iface := st.LAN.Iface
			if iface == "" {
				iface = "eth0"
			}
			return sprintf(nx.CableNoIPF, iface), true
		}
		return nx.StepsCable, false
	}
	ap := st.AccessPoint
	if ap.Policy == "rescue" {
		return nx.StepsWifiOff, true
	}
	ssid := ap.SSID
	if ssid == "" && net.Wifi != nil {
		ssid = net.Wifi.SSID
	}
	if ssid == "" {
		ssid = tx.AboutUnknown
	}
	return sprintf(nx.StepsWifiF, ssid), false
}

// camSecret draws a field of a camera's URLs: its title, and dots until its eye is opened (state hidden); then its
// values in monospaced type, each cut in lines that fit (shown), «Reading…» while the app is asked, or «—» when that
// way has none.
func (u *UI) camSecret(d *dc, key, eye, title string, vals []string, state string) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	m := float64(lay.M)
	r := lay.Buttons[key]
	q := rrOf(r, 10*s)
	c.FillRR(q, cPanel)
	c.StrokeRR(q, 1.25, cLine)
	x0 := float64(r.X) + 1.5*m
	w := float64(r.W) - 3*m
	y := float64(r.Y) + 22*s
	d.text(x0, y, title, 14*s, textOpt{col: cQuiet, width: w})
	y += 26 * s
	switch state {
	case "hidden":
		u.secretText(d, x0, y, w, "................", false)
	case "reading":
		d.text(x0, y, tx.NetX.Reading, 17*s, textOpt{col: cQuiet, width: w})
	case "none":
		d.text(x0, y, tx.AboutUnknown, 17*s, textOpt{col: cQuiet, width: w})
	default:
		style := MonoBold
		size := 15 * s
		var lines []string
		for _, v := range vals {
			lines = append(lines, d.wrapChars(v, size, style, w)...)
		}
		most := max(1, int((float64(r.Y+r.H)-y+12*s)/(19*s)))
		if len(lines) > most { // never on this layout; a longer URL ends in «…»
			lines[most-1] = lines[most-1] + strings.Join(lines[most:], "")
			lines = lines[:most]
		}
		for _, l := range lines {
			d.text(x0, y, l, size, textOpt{col: cInk, style: &style, width: w})
			y += 19 * s
		}
	}
	if state != "none" {
		u.eye(d, eye, state != "hidden")
	}
}

// wrapText is wrap that keeps French's marks with their words (a space after « and before » : ; ! ?): a line never
// ends with « nor starts with a closing mark.
func (d *dc) wrapText(t string, size float64, style FontStyle, width float64) []string {
	var words []string
	glue := false // the word before was «: this one goes with it
	for _, w := range strings.Fields(t) {
		switch {
		case glue:
			words[len(words)-1] += " " + w
		case len(words) > 0 && strings.Trim(w, "»:;!?,.)") == "":
			words[len(words)-1] += " " + w
		default:
			words = append(words, w)
		}
		glue = w == "«"
	}
	var out []string
	line := ""
	for _, w := range words {
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

// wrapChars cuts a text without spaces (a URL) in lines that fit a width, by characters.
func (d *dc) wrapChars(t string, size float64, style FontStyle, width float64) []string {
	var out []string
	r := []rune(t)
	for len(r) > 0 {
		n := len(r)
		for n > 1 {
			if adv, _ := d.c.Fonts.Extents(string(r[:n]), size, style); adv <= width {
				break
			}
			n--
		}
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return out
}

// --- keys and taps ---

func (u *UI) camsKeys(app *AppSnapshot) []string {
	snap := u.Net.Snapshot()
	n := &snap
	if u.camp.view != "" {
		keys := []string{"s_back"}
		cam, ok := u.camByID(n, u.camp.view)
		if !ok || !camsReady(n) {
			return keys
		}
		keys = append(keys, "s_camv_name", "s_camv_key", "s_camv_remove")
		st := n.Status
		way := u.camWay(st)
		if len(camWays(st)) > 1 {
			keys = append(keys, "s_camv_wifi", "s_camv_lan")
		}
		urls, loaded := camURLs(app, cam.ID, way)
		if !camNoURLs(st, way) && !(loaded && urls == nil) {
			keys = append(keys, camSRTEye, camRTMPEye)
		}
		return keys
	}
	var keys []string
	if NetSwitch(n) {
		keys = append(keys, "s_net")
	}
	if !camsReady(n) {
		return keys
	}
	keys = append(keys, "s_cams_wifi", "s_cams_lan")
	for i := range u.camp.shown {
		keys = append(keys, "s_cam_"+strconv.Itoa(i))
	}
	if u.camp.pages > 1 {
		keys = append(keys, "s_cams_prev", "s_cams_next")
	}
	return append(keys, "s_cams_add")
}

func (u *UI) camsTouch(what string) bool {
	if u.camp.view != "" {
		return u.camViewTouch(what)
	}
	switch {
	case what == "s_net":
		u.toggleNet()
	case what == "s_cams_wifi":
		u.toggleVia("ap_enabled")
	case what == "s_cams_lan":
		u.toggleVia("lan_enabled")
	case what == "s_cams_prev":
		u.camp.page = max(0, u.camp.page-1)
	case what == "s_cams_next":
		u.camp.page = min(u.camp.page+1, max(0, u.camp.pages-1))
	case what == "s_cams_add":
		u.addCamera()
	case strings.HasPrefix(what, "s_cam_"):
		i, err := strconv.Atoi(what[len("s_cam_"):])
		if err != nil || i >= len(u.camp.shown) {
			return false
		}
		u.openCam(u.camp.shown[i])
	default:
		return false
	}
	return true
}

// openCam shows a camera's view (its eyes closed).
func (u *UI) openCam(id string) {
	u.camp.view, u.camp.way, u.confirm = id, "", nil
	u.closeCamEyes()
	u.Dirty = true
}

// closeCam goes back to the list: the eyes closed and the URLs the app gave out of memory.
func (u *UI) closeCam() {
	u.camp.view, u.camp.way, u.confirm = "", "", nil
	u.closeCamEyes()
	u.dropSecrets()
	u.Dirty = true
}

// closeCamEyes closes the camera view's eyes.
func (u *UI) closeCamEyes() {
	for _, k := range []string{camSRTEye, camRTMPEye} {
		if u.revealed(k) {
			u.toggleReveal(k)
		}
	}
}

// camsLeave: the page goes (another page, Settings closed, the screen locked): back to the list, the URLs dropped.
func (u *UI) camsLeave() {
	u.camp.view, u.camp.way, u.camp.page = "", "", 0
	u.closeCamEyes()
	u.dropSecrets()
}

func (u *UI) camViewTouch(what string) bool {
	snap := u.Net.Snapshot()
	cam, ok := u.camByID(&snap, u.camp.view)
	if what == "s_back" {
		u.closeCam()
		return true
	}
	if !ok {
		return false
	}
	app := u.app().AppSnapshot()
	switch what {
	case "s_camv_name":
		u.renameCam(cam)
	case "s_camv_key":
		if app.Pending == "cam_rotate" {
			return true
		}
		if u.confirmTap("cam_rotate") {
			// The URLs shown carry the old key: closed and dropped (an eye asks for them again)
			u.closeCamEyes()
			u.dropSecrets()
			u.appBusy(u.app().Do(AppAction{What: "cam_rotate", ID: cam.ID}))
		}
	case "s_camv_remove":
		if app.Pending == "cam_remove" {
			return true
		}
		if u.confirmTap("cam_remove") {
			ok := u.app().Do(AppAction{What: "cam_remove", ID: cam.ID})
			u.appBusy(ok)
			if ok {
				u.closeCam()
			}
		}
	case "s_camv_wifi", "s_camv_lan":
		u.camp.way = strings.TrimPrefix(what, "s_camv_")
	case camSRTEye, camRTMPEye:
		if u.toggleReveal(what) && app.Pending != "cam_creds" {
			// Opened: the URLs with their keys, asked again every time (a new key, or the cable's address, may have
			// changed them since)
			u.app().Do(AppAction{What: "cam_creds"})
		}
	default:
		return false
	}
	return true
}

// addCamera: «Add camera» opens the name's keyboard; OK sends it (POST /cameras {"name"}).
func (u *UI) addCamera() {
	snap := u.Net.Snapshot()
	app := u.app().AppSnapshot()
	if !camsReady(&snap) || app.Pending == "cam_add" {
		return
	}
	st := snap.Status
	if st.CamMax > 0 && len(st.CamItems) >= st.CamMax {
		u.notePage(sprintf(u.Tx.NetX.CamsFullF, len(st.CamItems)), false)
		return
	}
	if !st.Enabled {
		u.notePage(u.Tx.NetX.AddNeedsReception, true)
		return
	}
	nx := u.Tx.NetX
	u.openSpecKeyboard(&kbSpec{title: nx.CamNameTitle, help: nx.CamNameHelp, max: CamNameMax, min: 1, allow: camNameRune,
		done: func(u *UI, t string, _ []byte) string {
			if !CamNameOK(t) {
				return u.Tx.NetX.CamNameBad
			}
			if !u.app().Do(AppAction{What: "cam_add", Body: map[string]any{"name": t}}) {
				return u.Tx.BusyChange
			}
			u.camp.added = t
			return ""
		}}, "")
}

// renameCam opens the name's keyboard on a camera's name (PATCH /cameras/{id} {"name"}).
func (u *UI) renameCam(cam camItem) {
	nx := u.Tx.NetX
	id, old := cam.ID, cam.Name
	u.openSpecKeyboard(&kbSpec{title: nx.CamNameTitle, help: nx.CamNameHelp, max: CamNameMax, min: 1, allow: camNameRune,
		done: func(u *UI, t string, _ []byte) string {
			if !CamNameOK(t) {
				return u.Tx.NetX.CamNameBad
			}
			if t == old {
				return ""
			}
			if !u.app().Do(AppAction{What: "cam_rename", ID: id, Body: map[string]any{"name": t}}) {
				return u.Tx.BusyChange
			}
			return ""
		}}, old)
}

// --- previews (eirl-ui-render) ---

// NetView opens a camera's view on the Network cameras page.
func (p Preview) NetView(id string) { p.u.openCam(id) }

// NetReveal opens an eye of the Wi-Fi or Network cameras pages (the Wi-Fi password's, a camera's).
func (p Preview) NetReveal(key string) {
	if key == wifiPwEye {
		p.u.toggleWifiPw()
		return
	}
	p.u.toggleReveal(key)
}

// NetConfirm shows a button of those pages waiting for its second tap (ap_rotate, cam_rotate, cam_remove,
// via_wifi, via_lan).
func (p Preview) NetConfirm(what string) { p.u.confirmTap(what) }

// NetWay shows a camera's view over one way in (wifi | lan).
func (p Preview) NetWay(way string) { p.u.camp.way = way }

// NetChannelPicker opens the Wi-Fi's channel picker.
func (p Preview) NetChannelPicker() { p.u.openChannelPicker() }
