package ui

import (
	"strconv"
	"strings"
)

// The Connections page (Stream → Connections, formerly Uplinks; MIXER.md §21, MODEMS.md §6): the web's card. One row per
// internet uplink (its dot, kind, IP and what it sends); the whole row is its switch: in or out of the SRTLA share (PUT
// /uplinks/{name}, applied live, without a cut). Taking out the last usable one while streaming over SRTLA needs a
// second tap. A modem's row has a «›» to its own page (modempage.go); modems without a usable link (a SIM asking for
// its PIN) have a row of their own, which opens that page.

// UplinkRows is how many uplinks the page lists (more is rare: a board with five ways to the internet).
const UplinkRows = 5

func init() {
	registerPage("uplinks", &pageDef{
		layout: func(l *Layout) {
			c := l.SettingsPage
			gap := l.M / 2
			top := c.Y + 50 // the mode's or the warning's lines (two)
			rh := max(48, min(60, (c.H-50-60-UplinkRows*gap)/UplinkRows))
			for i := range UplinkRows {
				r := Rect{c.X, top + i*(rh+gap), c.W, rh}
				l.Buttons["s_upl_"+strconv.Itoa(i)] = r
				// «›» to the modem's page, left of the row's switch (64 × 0.8 px, 2m from the edge)
				bw := l.IB
				l.Buttons["s_uplm_"+strconv.Itoa(i)] = Rect{r.X + r.W - 3*l.M - 52 - bw, r.Y + (rh-l.HB)/2, bw, l.HB}
			}
			modemLayout(l)
		},
		draw:  (*UI).drawUplinksPage,
		keys:  (*UI).uplinkKeys,
		touch: (*UI).uplinkTouch,
		title: (*UI).modemTitle,
		watch: func(_ *UI, w *AppWatch) { w.Status = true },
		leave: func(u *UI) { u.modemView = "" },
	})
}

// connRow is one row of the page: an uplink, or a modem without a usable link (then Up is nil).
type connRow struct {
	Up    *UplinkInfo
	Modem *ModemInfo
	Dot   LinkDot // a modem without a link: its dot and reason
}

// connRows are the rows as the page lists them: the uplinks, then the modems without a link, UplinkRows at most.
func connRows(st *StatusInfo) []connRow {
	if st == nil {
		return nil
	}
	var out []connRow
	for i := range st.Uplinks {
		if len(out) == UplinkRows {
			return out
		}
		ul := &st.Uplinks[i]
		out = append(out, connRow{Up: ul, Modem: modemByID(st, ul.ModemID)})
	}
	for _, l := range st.Links {
		if len(out) == UplinkRows {
			break
		}
		if l.Kind != "modem" {
			continue
		}
		for i := range st.Modems {
			if m := &st.Modems[i]; m.Iface == l.Name || m.ID == l.Name {
				out = append(out, connRow{Modem: m, Dot: l})
				break
			}
		}
	}
	return out
}

func modemByID(st *StatusInfo, id string) *ModemInfo {
	if st == nil || id == "" {
		return nil
	}
	for i := range st.Modems {
		if st.Modems[i].ID == id {
			return &st.Modems[i]
		}
	}
	return nil
}

// uplinksShown are the uplinks as the page lists them.
func uplinksShown(st *StatusInfo) []UplinkInfo {
	if st == nil {
		return nil
	}
	return st.Uplinks[:min(len(st.Uplinks), UplinkRows)]
}

// uplinksLive: the stream is going out over SRTLA now (its uplinks' inUse counts).
func (u *UI) uplinksLive(st *StatusInfo) bool {
	return st.Transport == "srtla" && u.streaming()
}

func (u *UI) uplinkKeys(app *AppSnapshot) []string {
	if u.modemView != "" {
		return u.modemKeys(app)
	}
	var keys []string
	for i, row := range connRows(app.Status) {
		if row.Up != nil && row.Modem != nil {
			keys = append(keys, "s_uplm_"+strconv.Itoa(i)) // before its row: it is inside it
		}
		keys = append(keys, "s_upl_"+strconv.Itoa(i))
	}
	return keys
}

func (u *UI) drawUplinksPage(d *dc, app *AppSnapshot) {
	tx, st, s := u.Tx, u.Tx.Stream, d.s
	pg := u.Lay.SettingsPage
	status := app.Status
	if !u.devicePageReady(d, app.StatusAPI, status != nil) {
		return
	}
	if u.modemView != "" {
		u.drawModemPage(d, app)
		return
	}
	rows := connRows(status)
	list := uplinksShown(status)
	srtla := status.Transport == "srtla"
	x0, w0 := float64(pg.X)+4*s, float64(pg.W)-8*s
	if len(rows) == 0 {
		u.pageMessage(d, st.UplinksNone, st.UplinksNoneSub, false)
		return
	}
	live := u.uplinksLive(status)
	used, conflict := 0, false
	for _, ul := range list {
		if (live && ul.InUse) || (!live && !ul.Disabled && !ul.Conflict && !ul.NoInternet) {
			used++
		}
		conflict = conflict || ul.Conflict
	}
	mode := st.UplinksSrtla
	if !srtla {
		mode = st.UplinksNoSrtla
	}
	head, warn := mode, false
	switch {
	case conflict:
		head, warn = st.UplinksConflict, true
	case srtla && used == 0:
		head, warn = st.UplinksNoneUsed, true
	case srtla && used == 1:
		head, warn = st.UplinksSingle, true
	}
	col := cQuiet
	if warn {
		col = cWarn
	}
	style := Sans
	if warn {
		style = SansBold
	}
	hl := d.wrap(head, 14*s, style, w0)
	if len(hl) > 2 {
		hl = append(hl[:1], strings.Join(hl[1:], " "))
	}
	for i, l := range hl {
		d.text(x0, float64(pg.Y)+18*s+float64(i)*19*s, l, 14*s, textOpt{col: col, bold: warn, width: w0})
	}
	pend := app.Pending == "uplink"
	for i, row := range rows {
		key := "s_upl_" + strconv.Itoa(i)
		if row.Up == nil { // a modem without a usable link: its reason, and the row opens its page
			m := row.Modem
			why := st.UplinkHealth[row.Dot.Code]
			tone := "bad"
			if row.Dot.Health == "off" {
				tone = "quiet"
			}
			sub := joinNonEmpty([]string{st.UplinkKind["modem"], tailIMEI(m.IMEITail), why}, tx.Sep)
			u.rowDot(d, key, "modem", m.Label(), RowState{KnobNone, false, sub, tone}, row.Dot.Health, false, 0)
			r := u.Lay.Buttons[key]
			u.icon(d.c, "next", float64(r.X+r.W)-2*float64(u.Lay.M)-8*s, float64(r.Y)+float64(r.H)/2, 9*s, max(2.0, 2.6*s), cQuiet)
			continue
		}
		ul := *row.Up
		sub, tone := u.uplinkState(ul, live)
		parts := []string{st.UplinkKind[ul.Kind]}
		if parts[0] == "" {
			parts[0] = ul.Kind
		}
		title := ul.Name
		if ul.Label != "" { // a modem: its operator, and the interface goes to the second line
			title = ul.Label
			parts = append(parts, ul.Name)
		} else if ul.IP != "" && !ul.HasStats { // with the sender's figures there is no room for the address
			parts = append(parts, ul.IP)
		}
		parts = append(parts, sub)
		if ul.HasStats { // the Rust SRTLA sender's view of the link (SRTLA.md §9.4)
			nak := strings.ReplaceAll(sprintf("%.1f", ul.NakPerSec), ".", u.Tx.Decimal)
			parts = append(parts, sprintf(st.UplinkStatsF, ul.RTTMs, nak))
		}
		sub = strings.Join(parts, tx.Sep)
		on := !ul.Disabled
		kn := knob(on)
		busy := pend && u.uplinkPend == ul.Name
		if busy {
			kn = knob(!u.uplinkPendOff)
		}
		icon := map[string]string{"wifi": "wifi", "usb": "modem", "modem": "modem", "ethernet": "ethernet"}[ul.Kind]
		if icon == "" {
			icon = "ethernet"
		}
		right, mk := 0.0, ""
		if row.Modem != nil { // «›» to its page, left of the switch (drawn over the row)
			mk = "s_uplm_" + strconv.Itoa(i)
			right = float64(u.Lay.Buttons[mk].X) - float64(u.Lay.M)
		}
		u.rowDot(d, key, icon, title, RowState{kn, busy, sub, tone}, ul.Health, on && tone == "good", right)
		if mk != "" {
			d.button(mk, btn{icon: "next"})
		}
		if u.confirming("uplink_last:" + ul.Name) {
			d.frameR(u.Lay.Buttons[key], cWarn, 2, 10*s)
		}
	}
	note, warnNote := "", true
	switch {
	case u.confirmingPrefix("uplink_last:"):
		note = st.UplinksLastConfirm
	default:
		if t, w := u.pageNoteText(); t != "" {
			note, warnNote = t, w
		} else if t := u.resultNote(app, "uplink"); t != "" {
			note = t
		} else {
			note, warnNote = st.UplinksHint, false
		}
	}
	last := u.Lay.Buttons["s_upl_"+strconv.Itoa(len(rows)-1)]
	y := float64(last.Y+last.H) + 26*s
	for _, line := range d.wrap(note, 15*s, Sans, w0) {
		c := cQuiet
		if warnNote {
			c = cWarn
		}
		d.text(x0, y, line, 15*s, textOpt{col: c, bold: warnNote, width: w0})
		y += 21 * s
	}
}

// uplinkState is what an uplink is doing, in words, and its tone (as the web's rate column). With the app's
// judgement (MODEMS.md §5) the reason is its code's; fine, the upload as before.
func (u *UI) uplinkState(ul UplinkInfo, live bool) (string, string) {
	st := u.Tx.Stream
	if why := st.UplinkHealth[ul.HealthCode]; why != "" && ul.HealthCode != "ok" {
		switch ul.Health {
		case "off":
			return st.UplinkDisabled, "quiet"
		case "weak":
			return why, "warn"
		case "bad":
			return why, "bad"
		}
	}
	switch {
	case ul.Conflict:
		return st.UplinkDupIP, "warn"
	case ul.Disabled:
		return st.UplinkDisabled, "quiet"
	case ul.NoInternet:
		return st.UplinkNoInternet, "warn"
	case live && !ul.InUse:
		return u.Tx.NetConnecting, "quiet"
	case ul.Bond == "down":
		return st.UplinkBond["down"], "bad"
	case ul.Bond == "stalled" || ul.Bond == "weak":
		return st.UplinkBond[ul.Bond], "warn"
	}
	kbps := ul.TxKbps
	if ul.HasStats { // what the sender put on the link: the interface's counter counts all its traffic
		kbps = ul.SendKbps
	}
	mb := strings.ReplaceAll(sprintf("%.1f", float64(kbps)/1000), ".", u.Tx.Decimal)
	return sprintf(st.UplinkRateF, mb), "good"
}

// confirmingPrefix: something whose «what» starts with p waits for its second tap.
func (u *UI) confirmingPrefix(p string) bool {
	return u.confirm != nil && strings.HasPrefix(u.confirm.what, p) && u.now().Before(u.confirm.until)
}

func (u *UI) uplinkTouch(what string) bool {
	if u.modemView != "" {
		return u.modemTouch(what)
	}
	app := u.app().AppSnapshot()
	rows := connRows(app.Status)
	if s, ok := strings.CutPrefix(what, "s_uplm_"); ok {
		if i, err := strconv.Atoi(s); err == nil && i < len(rows) && rows[i].Modem != nil {
			u.openModem(rows[i].Modem.ID)
		}
		return true
	}
	s, ok := strings.CutPrefix(what, "s_upl_")
	if !ok {
		return false
	}
	i, err := strconv.Atoi(s)
	if err != nil || i >= len(rows) {
		return true
	}
	if rows[i].Up == nil { // a modem without a link: its page
		u.openModem(rows[i].Modem.ID)
		return true
	}
	if app.Pending == "uplink" {
		return true
	}
	ul := *rows[i].Up
	off := !ul.Disabled
	if off && app.Status.Transport == "srtla" && u.streaming() {
		// The ones really left: neither out, nor with a repeated IP, nor without internet (this one, out)
		left := 0
		for _, o := range app.Status.Uplinks {
			if o.Name != ul.Name && !o.Disabled && !o.Conflict && !o.NoInternet {
				left++
			}
		}
		if left == 0 && !u.confirmTap("uplink_last:"+ul.Name) {
			return true
		}
	}
	if u.app().Do(AppAction{What: "uplink", ID: ul.Name, Body: map[string]any{"disabled": off}}) {
		u.uplinkPend, u.uplinkPendOff = ul.Name, off
	} else {
		u.appBusy(false)
	}
	return true
}
