package ui

import (
	"strconv"
	"strings"
)

// The Uplinks page (Stream → Uplinks, MIXER.md §21): the web's «Uplinks» card. One row per internet uplink (its kind,
// IP and what it sends); the whole row is its switch: in or out of the SRTLA share (PUT /uplinks/{name}, applied
// live, without a cut). Taking out the last usable one while streaming over SRTLA needs a second tap.

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
				l.Buttons["s_upl_"+strconv.Itoa(i)] = Rect{c.X, top + i*(rh+gap), c.W, rh}
			}
		},
		draw:  (*UI).drawUplinksPage,
		keys:  (*UI).uplinkKeys,
		touch: (*UI).uplinkTouch,
		watch: func(_ *UI, w *AppWatch) { w.Status = true },
	})
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
	var keys []string
	for i := range uplinksShown(app.Status) {
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
	list := uplinksShown(status)
	srtla := status.Transport == "srtla"
	x0, w0 := float64(pg.X)+4*s, float64(pg.W)-8*s
	if len(list) == 0 {
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
	for i, ul := range list {
		key := "s_upl_" + strconv.Itoa(i)
		sub, tone := u.uplinkState(ul, live)
		parts := []string{st.UplinkKind[ul.Kind]}
		if parts[0] == "" {
			parts[0] = ul.Kind
		}
		title := ul.Name
		if ul.Label != "" { // a modem: its operator, and the interface goes to the second line
			title = ul.Label
			parts = append(parts, ul.Name)
		} else if ul.IP != "" {
			parts = append(parts, ul.IP)
		}
		sub = strings.Join(append(parts, sub), tx.Sep)
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
		u.rowDot(d, key, icon, title, RowState{kn, busy, sub, tone}, ul.Health, on && tone == "good", 0)
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
	last := u.Lay.Buttons["s_upl_"+strconv.Itoa(len(list)-1)]
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
	}
	mb := strings.ReplaceAll(sprintf("%.1f", float64(ul.TxKbps)/1000), ".", u.Tx.Decimal)
	return sprintf(st.UplinkRateF, mb), "good"
}

// confirmingPrefix: something whose «what» starts with p waits for its second tap.
func (u *UI) confirmingPrefix(p string) bool {
	return u.confirm != nil && strings.HasPrefix(u.confirm.what, p) && u.now().Before(u.confirm.until)
}

func (u *UI) uplinkTouch(what string) bool {
	s, ok := strings.CutPrefix(what, "s_upl_")
	if !ok {
		return false
	}
	app := u.app().AppSnapshot()
	list := uplinksShown(app.Status)
	i, err := strconv.Atoi(s)
	if err != nil || i >= len(list) || app.Pending == "uplink" {
		return true
	}
	ul := list[i]
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
