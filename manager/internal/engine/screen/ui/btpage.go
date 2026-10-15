package ui

import (
	"strconv"
	"strings"
)

// The Bluetooth page (Settings › Wireless › Bluetooth, BLUETOOTH.md §3.6): the web's card in rows a finger can hit.
// Without Bluetooth (no module, no stack, no audio), one line that says why and nothing else (D3). With it: the
// switch row; «Search» and «Make visible» (the 2-minute window in which a phone can pair, with the time left: D4);
// the paired devices (a tap opens the device's view) and then the ones a search found (with «Pair»), in pages when
// they do not fit. A device's view: Connect / Disconnect, what it is used for when it can be used for more than one
// thing, and Forget behind a second tap. The app takes one order at a time: while one runs, the others are greyed out.
// Everything comes from the app (AppSnapshot.Bluetooth, asked only while the page is on view) and goes to it
// (bt_power, bt_scan, bt_visible, bt_device, bt_use).

// btPage is what the screen keeps of the Bluetooth page.
type btPage struct {
	view    string   // the paired device open in its view (its address); "" the list
	page    int      // the list's page
	pages   int      // as drawn
	shown   []string // the device of each list row, as drawn: a tap goes to what was seen
	found   []bool   // whether each row shown is a found (not paired) device: its «Pair»
	powerTo bool     // what the switch under way asked for
}

// btRowGap is the gap between the list's rows (smaller than the page's: five rows fit at 1024×600).
const btRowGap = 4

// btMaxUses: the most uses a device has (listen, mic, input).
const btMaxUses = 3

func init() {
	registerPage("bluetooth", &pageDef{
		layout: btLayout,
		draw:   (*UI).drawBTPage,
		title:  (*UI).btTitle,
		keys:   (*UI).btKeys,
		touch:  (*UI).btTouch,
		pickAt: (*UI).btPickAt,
		watch:  func(_ *UI, w *AppWatch) { w.Bluetooth = true },
		leave:  func(u *UI) { u.btp = btPage{} },
	})
}

// btLayout places the page: the switch row; «Search» and «Make visible» side by side under it; the list's rows
// (each ≥ 48 px, a found one with «Pair» at its right) down to the pager (s_prev, s_next: the page's own); and a
// device's view: its uses as rows under its two lines, and at the bottom Connect / Disconnect and Forget. The main
// view moves down under a warning band when there is one (btPlace).
func btLayout(l *Layout) {
	c, m, hb := l.SettingsPage, l.M, l.HB
	b := l.Buttons
	gap := m / 2
	rh := hb + hb/6
	l.btWarn = -1
	l.btPlace(false)
	// A device's view
	fh := max(52, hb+6)
	uy := c.Y + 96 // its two lines and the uses' title
	for i := range btMaxUses {
		b["s_btuse_"+strconv.Itoa(i)] = Rect{c.X, uy + i*(rh+gap), c.W, rh}
	}
	bw := max(220, c.W*36/100)
	b["s_btd_conn"] = Rect{c.X, c.Y + c.H - fh, bw, fh}
	b["s_btd_forget"] = Rect{c.X + c.W - bw, c.Y + c.H - fh, bw, fh}
}

// btWarnH is the warning band's height (two lines): Bluetooth works but its audio does not (no_audio).
func (l *Layout) btWarnH() int { return max(40, l.HB*4/5) }

// btPlace places the main view, under the warning band or not. Only when that changes: it moves the buttons, as
// SettingsRows does for the Wi-Fi's client row.
func (l *Layout) btPlace(warn bool) {
	w := 0
	if warn {
		w = 1
	}
	if w == l.btWarn {
		return
	}
	l.btWarn = w
	c, m, hb := l.SettingsPage, l.M, l.HB
	b := l.Buttons
	gap := m / 2
	rh := hb + hb/6
	y := l.btTop()
	b["s_bt_power"] = Rect{c.X, y, c.W, rh}
	bh := 48
	y += rh + gap
	half := (c.W - gap) / 2
	b["s_bt_scan"] = Rect{c.X, y, half, bh}
	b["s_bt_visible"] = Rect{c.X + half + gap, y, c.W - half - gap, bh}
	top, foot := l.btListBand()
	n := l.btRows()
	rowH := max(48, min(60, (foot-top-(n-1)*btRowGap)/n))
	pw := max(150, c.W/5) // «Emparejando…» fits
	for i := range btRowsMax {
		k, pk := "s_btrow_"+strconv.Itoa(i), "s_btpair_"+strconv.Itoa(i)
		if i >= n { // fewer rows under the band: none left where it was
			delete(b, k)
			delete(b, pk)
			continue
		}
		r := Rect{c.X, top + i*(rowH+btRowGap), c.W, rowH}
		b[k] = r
		ph := min(r.H-4, 44) // a finger's, inside its row
		b[pk] = Rect{r.X + r.W - m - pw, r.Y + (r.H-ph)/2, pw, ph}
	}
	if l.keys != nil {
		l.sortKeys()
	}
}

// btTop is where the main view's controls start: the page's top, or under the warning band.
func (l *Layout) btTop() int {
	if l.btWarn == 1 {
		return l.SettingsPage.Y + l.btWarnH() + l.M/2
	}
	return l.SettingsPage.Y
}

// btListBand is where the list goes: from under «Search» to over the pager.
func (l *Layout) btListBand() (top, foot int) {
	c, m, hb := l.SettingsPage, l.M, l.HB
	gap := m / 2
	return l.btTop() + hb + hb/6 + gap + 48 + gap, c.Y + c.H - 48 - gap
}

// btRowsMax: the most rows the list has (at 1920×1080).
const btRowsMax = 8

// btRows is how many devices the list shows at once.
func (l *Layout) btRows() int {
	top, foot := l.btListBand()
	return max(3, min(btRowsMax, (foot-top+btRowGap)/(48+btRowGap)))
}

// btWarned: Bluetooth works but its audio does not (the app's no_audio): a warning over the controls.
func btWarned(st *BTState) bool { return st != nil && st.Available && st.Reason == "no_audio" }

// btList is the devices as the list shows them: the paired ones, then the ones a search found (each group in the
// app's order).
func btList(st *BTState) []BTDevice {
	var paired, found []BTDevice
	for _, dv := range st.Devices {
		if dv.Paired {
			paired = append(paired, dv)
		} else {
			found = append(found, dv)
		}
	}
	return append(paired, found...)
}

// btBusy: an order is under way (queued for the app, or a device's running there): the others wait.
func btBusy(app *AppSnapshot) bool {
	return strings.HasPrefix(app.Pending, "bt_") || app.Bluetooth.Busy()
}

// btIcon is a device's icon by its kind.
func btIcon(kind string) string {
	switch kind {
	case "headphones", "headset", "speaker", "phone", "computer":
		return kind
	}
	return "speaker"
}

// btLabel is a device's name, else its address.
func btLabel(dv *BTDevice) string {
	if dv.Name != "" {
		return dv.Name
	}
	return dv.Address
}

// btDeviceState is what a device is doing, in words, and its tone.
func (u *UI) btDeviceState(dv *BTDevice) (string, string) {
	bt := u.Tx.BT
	switch {
	case dv.Pending != "":
		return bt.Pending[dv.Pending], "quiet"
	case dv.Error != "":
		return bt.Errors[dv.Error], "warn"
	case !dv.Paired:
		return bt.NotPaired, "quiet"
	case dv.Output:
		return bt.Playing, "good"
	case dv.Connected && dv.Ready:
		return bt.Connected, "good"
	case dv.Connected:
		return bt.NotReady, "quiet"
	}
	return bt.NotConnected, "quiet"
}

// btClock is the visible window's time left, m:ss.
func btClock(sec int) string {
	sec = max(0, sec)
	return sprintf("%d:%02d", sec/60, sec%60)
}

// btViewOK: the device view can stay (Bluetooth on, and its device still paired); else back to the list (it was
// forgotten, here or on the web).
func btViewOK(app *AppSnapshot, addr string) bool {
	st := app.Bluetooth
	if app.BluetoothAPI != Yes || st == nil || !st.Available || !st.On {
		return false
	}
	dv := st.Device(addr)
	return dv != nil && dv.Paired
}

// btTitle: in a device's view, its name and ←. A view whose device is gone goes back to the list here, the first
// thing Settings asks of the page on every drawing and every touch.
func (u *UI) btTitle() (string, bool, bool) {
	if u.btp.view == "" {
		return "", false, false
	}
	app := u.app().AppSnapshot()
	if !btViewOK(&app, u.btp.view) {
		u.btp.view, u.confirm = "", nil
		return "", false, false
	}
	return btLabel(app.Bluetooth.Device(u.btp.view)), true, true
}

func (u *UI) btKeys(app *AppSnapshot) []string {
	st := app.Bluetooth
	if u.btp.view != "" {
		keys := []string{"s_back"}
		dv := st.Device(u.btp.view)
		if dv == nil {
			return keys
		}
		if len(dv.Uses) > 1 {
			for i := range min(btMaxUses, len(dv.Uses)) {
				keys = append(keys, "s_btuse_"+strconv.Itoa(i))
			}
		}
		return append(keys, "s_btd_conn", "s_btd_forget")
	}
	if app.BluetoothAPI != Yes || st == nil || !st.Available {
		return nil
	}
	u.Lay.btPlace(btWarned(st))
	keys := []string{"s_bt_power"}
	if !st.On {
		return keys
	}
	keys = append(keys, "s_bt_scan", "s_bt_visible")
	for i := range u.btp.shown {
		if u.btp.found[i] { // its «Pair» is inside the row: tried first
			keys = append(keys, "s_btpair_"+strconv.Itoa(i))
		}
		keys = append(keys, "s_btrow_"+strconv.Itoa(i))
	}
	if u.btp.page > 0 {
		keys = append(keys, "s_prev")
	}
	if u.btp.page < u.btp.pages-1 {
		keys = append(keys, "s_next")
	}
	return keys
}

// btPickAt: the device of a list row, as drawn, when the finger goes down on it (a search reorders the list).
func (u *UI) btPickAt(key string) (string, bool) {
	for _, p := range []string{"s_btrow_", "s_btpair_"} {
		if s, ok := strings.CutPrefix(key, p); ok {
			if i, err := strconv.Atoi(s); err == nil && i < len(u.btp.shown) {
				return u.btp.shown[i], true
			}
		}
	}
	return "", false
}

// --- drawing -----------------------------------------------------------------------------------------------------

func (u *UI) drawBTPage(d *dc, app *AppSnapshot) {
	tx, bt, s := u.Tx, u.Tx.BT, d.s
	lay := u.Lay
	m := float64(lay.M)
	u.btp.shown, u.btp.found = u.btp.shown[:0], u.btp.found[:0]
	if !u.devicePageReady(d, app.BluetoothAPI, app.Bluetooth != nil) {
		return
	}
	st := app.Bluetooth
	if !st.Available { // D3: one line, why, and nothing else
		t, ok := bt.Unavailable[st.Reason]
		if !ok {
			t = bt.Unavailable["no_stack"]
		}
		u.pageMessage(d, t, "", false)
		return
	}
	if u.btp.view != "" {
		u.drawBTDevice(d, app)
		return
	}
	// The warning band: Bluetooth can be managed, but its devices are not heard
	warned := btWarned(st)
	lay.btPlace(warned)
	pg := lay.SettingsPage
	x0, w0 := float64(pg.X), float64(pg.W)
	if warned {
		band := RR{x0, float64(pg.Y), w0, float64(lay.btWarnH()), 10 * s}
		d.c.FillRR(band, cWarn.WithAlpha(0.14))
		d.c.StrokeRR(band, 1.25, cWarn)
		u.btLines(d, bt.NoAudioWarn, x0+w0/2, band.Y+band.H/2+5*s, w0-4*m, 14*s, cWarn, true, true)
	}
	// The switch row
	busy := btBusy(app)
	powerBusy := app.Pending == "bt_power"
	kn := knob(st.On)
	sub, tone := bt.PowerOff, "quiet"
	switch {
	case powerBusy && u.btp.powerTo:
		kn, sub = knob(true), tx.TurningOn
	case powerBusy:
		kn, sub = knob(false), tx.TurningOff
	case st.PowerFailed: // the adapter does not follow: said so, not «Turning on…» for ever
		sub, tone = bt.PowerFailed, "warn"
	case st.On && !st.Powered:
		sub = tx.TurningOn
	case st.On && st.Name != "":
		sub, tone = sprintf(bt.PowerOnNameF, st.Name), "good"
	case st.On:
		sub, tone = bt.PowerOn, "good"
	}
	u.row(d, "s_bt_power", "bluetooth", bt.Power, RowState{kn, powerBusy, sub, tone}, tone == "good", 0)
	if !st.On {
		top, foot := lay.btListBand()
		u.btLines(d, bt.OffHelp, x0+w0/2, float64(top+foot)/2-30*s, w0-8*m, 17*s, cQuiet, false, true)
		u.btFootNote(d, app, 1)
		return
	}
	// Search and Make visible
	scan := bt.Search
	if st.Scanning {
		scan = bt.Searching
	}
	d.button("s_bt_scan", btn{label: scan, active: st.Scanning, dim: !st.Powered || busy && !st.Scanning, size: 19})
	vis := bt.MakeVisible
	if st.Visible {
		vis = sprintf(bt.VisibleF, btClock(st.VisibleLeft))
	}
	// The window is not an order on a device: it can be opened or closed while one runs (not while the app has another
	// of the page's queued)
	queued := strings.HasPrefix(app.Pending, "bt_")
	d.button("s_bt_visible", btn{label: vis, active: st.Visible, dim: !st.Powered || queued && !st.Visible, size: 19})
	// The list, in pages
	list := btList(st)
	rows := lay.btRows()
	pages := max(1, (len(list)+rows-1)/rows)
	u.btp.page = max(0, min(u.btp.page, pages-1))
	u.btp.pages = pages
	lo := u.btp.page * rows
	if len(list) == 0 {
		t := bt.NoDevices
		if st.Scanning {
			t = bt.SearchingNone
		}
		top, foot := lay.btListBand()
		u.btLines(d, t, x0+w0/2, float64(top+foot)/2-12*s, w0-8*m, 17*s, cQuiet, false, true)
	}
	for i, dv := range list[min(lo, len(list)):min(len(list), lo+rows)] {
		u.btp.shown = append(u.btp.shown, dv.Address)
		u.btp.found = append(u.btp.found, !dv.Paired)
		u.drawBTRow(d, i, &dv, busy)
	}
	if pages > 1 {
		if u.btp.page > 0 {
			d.button("s_prev", btn{label: tx.PrevPage})
		}
		if u.btp.page < pages-1 {
			d.button("s_next", btn{label: tx.NextPage})
		}
	}
	u.btFootNote(d, app, pages)
}

// drawBTRow is one device of the list: its icon, name and «kind · state»; a paired one opens its view (›), a found
// one has «Pair» (greyed out while another order runs).
func (u *UI) drawBTRow(d *dc, i int, dv *BTDevice, busy bool) {
	lay, bt, s := u.Lay, u.Tx.BT, d.s
	m := float64(lay.M)
	key := "s_btrow_" + strconv.Itoa(i)
	r := lay.Buttons[key]
	state, tone := u.btDeviceState(dv)
	sub := joinNonEmpty([]string{bt.Kind[dv.Kind], state}, u.Tx.Sep)
	right := 0.0
	pk := "s_btpair_" + strconv.Itoa(i)
	if dv.Paired {
		right = float64(r.X+r.W) - 2*m - 18*s
	} else {
		right = float64(lay.Buttons[pk].X) - m
	}
	live := dv.Output || dv.Connected && dv.Ready
	u.row(d, key, btIcon(dv.Kind), btLabel(dv), RowState{KnobNone, false, sub, tone}, live, right)
	if live { // what plays or is ready: its outline in green, like the Wi-Fi in use
		d.frameR(r, cGood, 1.5, 10*s)
	}
	switch {
	case dv.Paired:
		u.chevron(d, r)
	case dv.Pending == "pair": // its own order: the button says what it does
		d.button(pk, btn{label: bt.Pending["pair"], active: true, size: 18})
	default:
		d.button(pk, btn{label: bt.Pair, dim: busy, size: 18})
	}
	if d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
		d.lit(r, 10*s, 0.12)
	}
}

// btFootNote is the list's bottom line (between ‹ and › when there are pages): a refusal of the app, what to do on
// the phone while visible, the page, or how the list works.
func (u *UI) btFootNote(d *dc, app *AppSnapshot, pages int) {
	tx, bt, s := u.Tx, u.Tx.BT, d.s
	lay := u.Lay
	m := float64(lay.M)
	st := app.Bluetooth
	note, warn := "", false
	switch {
	case u.btErrNote(app) != "":
		note, warn = u.btErrNote(app), true
	case st.Visible && st.Name != "":
		note = sprintf(bt.VisibleHelpF, st.Name)
	case st.Visible:
		note = bt.VisibleHelp
	case pages > 1:
		note = sprintf(tx.PageF, u.btp.page+1, pages)
	case len(u.btp.shown) > 0 && !u.btp.found[0]:
		note = bt.TapDevice
	}
	if note == "" {
		return
	}
	bp := lay.Buttons["s_prev"]
	pg := lay.SettingsPage
	x, w := float64(pg.X)+float64(pg.W)/2, float64(pg.W)-4*s
	if pages > 1 {
		w = float64(pg.W) - 2*float64(bp.W) - 4*m
	}
	col := cQuiet
	if warn {
		col = cWarn
	}
	u.btLines(d, note, x, float64(bp.Y)+float64(bp.H)/2+6*s, w, 15*s, col, warn, true)
}

// btErrNote: the page's own notice, or the app's refusal of the page's last order (for 8 s).
func (u *UI) btErrNote(app *AppSnapshot) string {
	if t, w := u.pageNoteText(); t != "" && w {
		return t
	}
	return u.resultNote(app, "bt_power", "bt_scan", "bt_visible", "bt_device", "bt_use")
}

// btLines draws a text cut in lines (at most two) around y: centred at x when centred, else from x.
func (u *UI) btLines(d *dc, t string, x, y, w, size float64, col Color, bold, centred bool) {
	style := Sans
	if bold {
		style = SansBold
	}
	lines := d.wrapText(t, size, style, w)
	if len(lines) > 2 {
		lines = append(lines[:1], strings.Join(lines[1:], " "))
	}
	lh := size * 1.35
	y -= float64(len(lines)-1) * lh / 2
	al := left
	if centred {
		al = center
	}
	for _, l := range lines {
		d.text(x, y, l, size, textOpt{col: col, bold: bold, align: al, width: w})
		y += lh
	}
}

// drawBTDevice is a paired device's view: its kind and state, its address, what it is used for (a choice when it
// has more than one use), and Connect / Disconnect and Forget.
func (u *UI) drawBTDevice(d *dc, app *AppSnapshot) {
	lay, tx, bt, c, s := u.Lay, u.Tx, u.Tx.BT, d.c, d.s
	m := float64(lay.M)
	pg := lay.SettingsPage
	st := app.Bluetooth
	dv := st.Device(u.btp.view)
	if dv == nil {
		return // btTitle went back to the list
	}
	x0, w0 := float64(pg.X)+4*s, float64(pg.W)-8*s
	state, tone := u.btDeviceState(dv)
	col := map[string]Color{"good": cGood, "warn": cWarn, "quiet": cInk}[tone]
	y := float64(pg.Y) + 22*s
	u.icon(c, btIcon(dv.Kind), x0+12*s, y-6*s, 12*s, max(2.0, 2.4*s), col)
	tx0 := x0 + 34*s
	adv := d.text(tx0, y, bt.Kind[dv.Kind], 19*s, textOpt{col: cInk, bold: true, width: w0 - 34*s})
	d.text(tx0+adv, y, tx.Sep+state, 19*s, textOpt{col: col, bold: tone != "quiet", width: w0 - 34*s - adv})
	mono := MonoBold
	d.text(tx0, y+24*s, dv.Address, 14*s, textOpt{col: cQuiet, style: &mono})
	busy := btBusy(app)
	// What it is used for
	u0 := lay.Buttons["s_btuse_0"]
	d.text(x0, float64(u0.Y)-10*s, bt.UsedFor, 15*s, textOpt{col: cQuiet, bold: true, width: w0})
	if len(dv.Uses) > 1 {
		for i, use := range dv.Uses[:min(btMaxUses, len(dv.Uses))] {
			u.drawBTUse(d, "s_btuse_"+strconv.Itoa(i), use, use == dv.Use, busy)
		}
	} else if len(dv.Uses) == 1 { // nothing to choose: what it is for, as text
		use := dv.Uses[0]
		d.text(x0, float64(u0.Y)+22*s, bt.Uses[use], 19*s, textOpt{col: cInk, bold: true, width: w0})
		d.text(x0, float64(u0.Y)+44*s, bt.UseHelp[use], 14*s, textOpt{col: cQuiet, width: w0})
	}
	// Connect / Disconnect and Forget
	addr := dv.Address
	conn := bt.Connect
	if dv.Connected {
		conn = bt.Disconnect
	}
	discConf := u.confirming("bt_disconnect:" + addr)
	if discConf {
		conn = tx.TapAgain
	}
	d.button("s_btd_conn", btn{label: conn, dim: busy, active: discConf, col: colp(cWarn), size: 19})
	forgetConf := u.confirming("bt_forget:" + addr)
	fl := tx.Forget
	if forgetConf {
		fl = tx.TapAgain
	}
	d.button("s_btd_forget", btn{label: fl, dim: busy, active: forgetConf, col: colp(cWarn), size: 19})
	// The note over the buttons: a second tap waiting, a refusal of the app
	note, warn := "", true
	switch {
	case forgetConf:
		note = bt.ForgetAsk
	case discConf:
		note = bt.DisconnectAsk
	default:
		note = u.btErrNote(app)
	}
	if note == "" {
		return
	}
	cb := lay.Buttons["s_btd_conn"]
	lc := cQuiet
	if warn {
		lc = cWarn
	}
	u.btLines(d, note, x0, float64(cb.Y)-14*s-m, w0, 15*s, lc, warn, false)
}

// drawBTUse is one use of a device, a choice (the one it has, outlined with ✓), with a quiet line of what it does.
func (u *UI) drawBTUse(d *dc, key, use string, active, dim bool) {
	lay, bt, c, s := u.Lay, u.Tx.BT, d.c, d.s
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
	if !dim && d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
		d.lit(r, 10*s, 0.12)
	}
	mid := float64(r.Y) + float64(r.H)/2
	x0 := float64(r.X) + 2*m
	right := float64(r.X+r.W) - 2*m
	if active {
		u.icon(c, "check", right-12*s, mid, 12*s, max(2.0, 2.8*s), cBrand)
		right -= 34 * s
	}
	tc := cInk
	if dim && !active {
		tc = cQuiet
	}
	d.text(x0, mid-2*s, bt.Uses[use], 19*s, textOpt{col: tc, bold: active, width: right - x0})
	d.text(x0, mid+17*s, bt.UseHelp[use], 14*s, textOpt{col: cQuiet, width: right - x0})
}

// --- taps --------------------------------------------------------------------------------------------------------

func (u *UI) btTouch(what string) bool {
	app := u.app().AppSnapshot()
	st := app.Bluetooth
	if u.btp.view != "" {
		return u.btDeviceTouch(what, &app)
	}
	if app.BluetoothAPI != Yes || st == nil || !st.Available {
		return false
	}
	busy := btBusy(&app)
	switch {
	case what == "s_bt_power":
		if strings.HasPrefix(app.Pending, "bt_") {
			return true
		}
		if u.btDo(AppAction{What: "bt_power", Body: map[string]any{"on": !st.On}}) {
			u.btp.powerTo = !st.On
		}
	case what == "s_bt_scan":
		if !busy && st.On && st.Powered && !st.Scanning {
			u.btDo(AppAction{What: "bt_scan"})
		}
	case what == "s_bt_visible":
		if !strings.HasPrefix(app.Pending, "bt_") && st.On && st.Powered {
			u.btDo(AppAction{What: "bt_visible", Body: map[string]any{"on": !st.Visible}})
		}
	case what == "s_prev":
		u.btp.page = max(0, u.btp.page-1)
	case what == "s_next":
		u.btp.page = min(u.btp.page+1, max(0, u.btp.pages-1))
	case strings.HasPrefix(what, "s_btrow_"), strings.HasPrefix(what, "s_btpair_"):
		dv := st.Device(u.tapPick) // the one seen when the finger went down
		if !u.tapHasPick || dv == nil {
			return true
		}
		switch {
		case dv.Paired:
			u.btp.view, u.confirm = dv.Address, nil
			u.pageSince = u.now()
		case !busy:
			u.btDo(AppAction{What: "bt_device", ID: dv.Address, Value: "pair"})
		}
	default:
		return false
	}
	return true
}

func (u *UI) btDeviceTouch(what string, app *AppSnapshot) bool {
	if what == "s_back" {
		u.btp.view, u.confirm = "", nil
		return true
	}
	dv := app.Bluetooth.Device(u.btp.view)
	if dv == nil {
		return true
	}
	if btBusy(app) { // one order at a time: greyed out until it ends
		return true
	}
	addr := dv.Address
	switch {
	case what == "s_btd_conn" && dv.Connected:
		// What the monitor plays through goes back to the screen's jack: no question. An input on air, a second tap
		if u.btOnAir(dv) && !u.confirmTap("bt_disconnect:"+addr) {
			return true
		}
		u.btDo(AppAction{What: "bt_device", ID: addr, Value: "disconnect"})
	case what == "s_btd_conn":
		u.btDo(AppAction{What: "bt_device", ID: addr, Value: "connect"})
	case what == "s_btd_forget":
		if u.confirmTap("bt_forget:" + addr) {
			u.btDo(AppAction{What: "bt_device", ID: addr, Value: "forget"})
		}
	case strings.HasPrefix(what, "s_btuse_"):
		i, err := strconv.Atoi(what[len("s_btuse_"):])
		if err != nil || i >= len(dv.Uses) || len(dv.Uses) < 2 {
			return true
		}
		if use := dv.Uses[i]; use != dv.Use {
			u.btDo(AppAction{What: "bt_use", ID: addr, Body: map[string]any{"use": use}})
		}
	default:
		return false
	}
	return true
}

// btOnAir: the device's audio is an input the program is using while streaming (its id bt:<address without colons>
// is the audio on air, or in the mix).
func (u *UI) btOnAir(dv *BTDevice) bool {
	au := u.st.Audio
	if au == nil || !u.streaming() || dv.Use == "listen" {
		return false
	}
	id := "bt:" + strings.ReplaceAll(dv.Address, ":", "")
	if au.Mode == "mix" {
		for _, a := range au.Sources {
			if a.ID == id && a.InMix {
				return true
			}
		}
		return false
	}
	return str(au.Main) == id
}

// btDo sends an order to the app (a full queue says so).
func (u *UI) btDo(a AppAction) bool {
	ok := u.app().Do(a)
	u.appBusy(ok)
	u.Dirty = true
	return ok
}
