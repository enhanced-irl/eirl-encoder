package ui

import (
	"strings"
	"time"
)

// A modem's page (Streaming › Connections › «›», MODEMS.md §7): its signal on the three scales the dot is judged
// by, its SIM and network, and what can be changed from here: the network mode (a picker), the APN and the name
// (keyboards), data roaming (a tap), the SIM's PIN (a secret keyboard: straight to the app, never in the engine's
// state or a log), a restart and, for two identical HiLink sticks, moving one to another subnet. Changes that
// reconnect the modem while it is the only connection on air need a second tap.

// The tiles' scales: low, the amber and green thresholds (linkhealth), high.
var modemScales = map[string][4]float64{
	"rsrp": {-120, -110, -100, -80},
	"sinr": {-5, 0, 5, 20},
	"rsrq": {-20, -15, -15, -5}, // RSRQ is amber below −15 and never red
}

func modemLayout(l *Layout) {
	c := l.SettingsPage
	gap := l.M / 2
	bh := max(48, l.HB+6)
	bw := (c.W - 2*gap) / 3
	y2 := c.Y + c.H - 2*bh - gap - 30 // two rows of buttons above the note line
	keys := [][]string{{"s_mdm_mode", "s_mdm_apn", "s_mdm_roam"}, {"s_mdm_name", "s_mdm_reset", "s_mdm_pin"}}
	for row, ks := range keys {
		for col, k := range ks {
			l.Buttons[k] = Rect{c.X + col*(bw+gap), y2 + row*(bh+gap), bw, bh}
		}
	}
}

// openModem shows a modem's page.
func (u *UI) openModem(id string) {
	u.modemView, u.confirm = id, nil
	u.pageSince = u.now()
	u.Dirty = true
}

// curModem: the open modem as the app says now (nil: gone, or not asked yet).
func (u *UI) curModem(app *AppSnapshot) *ModemInfo {
	return modemByID(app.Status, u.modemView)
}

func (u *UI) modemTitle() (string, bool, bool) {
	if u.modemView == "" {
		return "", false, false
	}
	app := u.app().AppSnapshot()
	m := u.curModem(&app)
	if m == nil {
		return u.Tx.Stream.UplinkKind["modem"], true, true
	}
	t := m.Label()
	if tech := techShort[m.Tech]; tech != "" {
		t += u.Tx.Sep + tech
	}
	return t, true, true
}

func (u *UI) modemKeys(app *AppSnapshot) []string {
	keys := []string{"s_back"}
	m := u.curModem(app)
	if m == nil {
		return keys
	}
	keys = append(keys, "s_mdm_mode", "s_mdm_apn", "s_mdm_roam", "s_mdm_name", "s_mdm_reset")
	if u.modemSecond(app, m) != "" {
		keys = append(keys, "s_mdm_pin")
	}
	return keys
}

// modemSecond: what the second row's middle button is: unlock (the SIM asks for its PIN), subnet (two identical
// HiLink sticks), pinoff (turn the SIM's PIN off) or nothing.
func (u *UI) modemSecond(app *AppSnapshot, m *ModemInfo) string {
	switch {
	case m.CanDo("pin") && m.SIM == "pin":
		return "unlock"
	case m.CanDo("subnet") && inConflict(app.Status, m.ID):
		return "subnet"
	}
	return ""
}

// inConflict: the modem's link has the same IP as another (two identical HiLink sticks: neither is used).
func inConflict(st *StatusInfo, id string) bool {
	if st == nil {
		return false
	}
	for _, ul := range st.Uplinks {
		if ul.ModemID == id && ul.Conflict {
			return true
		}
	}
	return false
}

// subnetCuts: moving the networks apart cuts the only link carrying the stream. The move reaches the stick with this
// modem's address that is routed (its twin, for one in conflict: MODEMS.md §7.3), so what counts is whether the
// only link in use has that address.
func (u *UI) subnetCuts(st *StatusInfo, m *ModemInfo) bool {
	if st == nil || !u.uplinksLive(st) {
		return false
	}
	ip := ""
	for _, ul := range st.Uplinks {
		if ul.ModemID == m.ID {
			ip = ul.IP
		}
	}
	used, ours := 0, false
	for _, ul := range st.Uplinks {
		if ul.InUse {
			used++
			ours = ours || ip != "" && ul.IP == ip
		}
	}
	return ours && used == 1
}

// onlyOnAir: this modem's link is the only one carrying the stream (a change that reconnects it would cut it).
func (u *UI) onlyOnAir(st *StatusInfo, id string) bool {
	if st == nil || !u.uplinksLive(st) {
		return false
	}
	used, ours := 0, false
	for _, ul := range st.Uplinks {
		if ul.InUse {
			used++
			ours = ours || ul.ModemID == id
		}
	}
	return ours && used == 1
}

func tailIMEI(t string) string {
	if t == "" {
		return ""
	}
	return "IMEI …" + t
}

func (u *UI) drawModemPage(d *dc, app *AppSnapshot) {
	tx, mt, s, c := u.Tx, u.Tx.Modem, d.s, d.c
	lay := u.Lay
	pg := lay.SettingsPage
	m := u.curModem(app)
	if m == nil {
		u.pageMessage(d, tx.Stream.UplinkKind["modem"], tx.AppNotAnswering, false)
		return
	}
	mf := float64(lay.M)
	x0, w0 := float64(pg.X), float64(pg.W)
	// Three tiles: RSRP, SINR, RSRQ, each with its scale and where it is now
	gap := mf / 2
	tw := (w0 - 2*gap) / 3
	ty, th := float64(pg.Y), 140*s
	vals := []struct {
		key, label string
		v          *float64
		unit       string
	}{{"rsrp", mt.RSRP, m.RSRP, " dBm"}, {"sinr", mt.SINR, m.SINR, " dB"}, {"rsrq", mt.RSRQ, m.RSRQ, " dB"}}
	if m.RSRP == nil && m.SINR == nil && m.RSRQ == nil {
		// No signal: why (the link's reason: the SIM asks for its PIN, searching…), else «no signal data»
		why, col := mt.NoSignal, cQuiet
		if h, code := modemHealth(app.Status, m); code != "" && code != "ok" {
			if t := tx.Stream.UplinkHealth[code]; t != "" {
				why, col = capFirst(t), healthDot[h]
			}
		}
		d.text(x0+4*s, ty+40*s, why, 24*s, textOpt{col: col, bold: col != cQuiet, width: w0})
		if m.RSSI != nil {
			d.text(x0+4*s, ty+72*s, sprintf("RSSI %s dBm", u.fmtSigned(*m.RSSI, 0)), 15*s, textOpt{col: cQuiet, width: w0})
		}
	} else {
		for i, t := range vals {
			tx0 := x0 + float64(i)*(tw+gap)
			q := RR{tx0, ty, tw, th, 10 * s}
			c.FillRR(q, cPanel)
			c.StrokeRR(q, 1.25, cLine)
			d.text(tx0+12*s, ty+24*s, t.label, 15*s, textOpt{col: cQuiet, width: tw - 24*s})
			sc := modemScales[t.key]
			if t.v == nil {
				d.text(tx0+12*s, ty+66*s, "—", 30*s, textOpt{col: cQuiet})
				continue
			}
			v := *t.v
			col := cGood
			switch {
			case v < sc[1] && t.key != "rsrq":
				col = cBad
			case v < sc[2]:
				col = cWarn
			}
			dec := 0
			if t.key == "sinr" {
				dec = 1
			}
			d.text(tx0+12*s, ty+66*s, u.fmtSigned(v, dec)+t.unit, 30*s, textOpt{col: col, bold: true, width: tw - 24*s})
			// the scale: red, amber, green, a mark where it is, and its ends
			bx, bw, by := tx0+12*s, tw-24*s, ty+th-46*s
			d.text(bx, by+30*s, u.fmtSigned(sc[0], 0), 12*s, textOpt{col: cQuiet})
			d.text(bx+bw, by+30*s, u.fmtSigned(sc[3], 0), 12*s, textOpt{col: cQuiet, align: right})
			pos := func(x float64) float64 { return bx + bw*min(1, max(0, (x-sc[0])/(sc[3]-sc[0]))) }
			low := cBad
			if t.key == "rsrq" {
				low = cWarn
			}
			c.FillRect(bx, by, pos(sc[1])-bx, 7*s, low)
			c.FillRect(pos(sc[1]), by, pos(sc[2])-pos(sc[1]), 7*s, cWarn)
			c.FillRect(pos(sc[2]), by, bx+bw-pos(sc[2]), 7*s, cGood)
			c.FillRect(pos(v)-2*s, by-5*s, 4*s, 17*s, cInk)
		}
	}
	// The facts: SIM, network, the modem
	fy := ty + th + 28*s
	sim := joinNonEmpty([]string{mt.SIMState[m.SIM], tailOf(m.ICCIDTail)}, tx.Sep)
	line1 := joinNonEmpty([]string{mt.SIMLabel + " " + sim, mt.NetLabel + " " + mt.Reg[orUnknown(m.Registration)], m.Operator}, "  ·  ")
	line2 := joinNonEmpty([]string{strings.TrimSpace(m.Manufacturer + " " + m.Model), tailIMEI(m.IMEITail), m.Iface}, tx.Sep)
	d.text(x0+4*s, fy, line1, 15*s, textOpt{col: cInk, width: w0 - 8*s})
	d.text(x0+4*s, fy+22*s, line2, 14*s, textOpt{col: cQuiet, width: w0 - 8*s})

	// The buttons
	pend := strings.HasPrefix(app.Pending, "modem_")
	dim := func(what string) bool { return pend || !m.CanDo(what) }
	mode := mt.Modes[orAuto(m.Mode)]
	apn := m.APN
	if apn == "" {
		apn = mt.APNAutoNet
		if m.APNAuto != "" {
			apn = sprintf(mt.APNAutoF, m.APNAuto)
		}
	}
	roam := mt.RoamingOff
	if m.Roaming {
		roam = mt.RoamingOn
	}
	sub := func(label, value string, dimmed bool) btn {
		return btn{label: label, sub: value, hasSub: true, dim: dimmed, size: 18}
	}
	d.button("s_mdm_mode", sub(mt.Network, mode, dim("mode")))
	d.button("s_mdm_apn", sub(mt.APN, apn, dim("apn")))
	d.button("s_mdm_roam", sub(mt.Roaming, roam, dim("roaming")))
	for key, btn := range map[string]string{"mode": "s_mdm_mode", "apn": "s_mdm_apn", "roam": "s_mdm_roam"} {
		if u.confirming("modem_force:" + key) { // waiting for the second tap
			d.frameR(lay.Buttons[btn], cWarn, 2, 10*s)
		}
	}
	name := m.Alias
	if name == "" {
		name = m.Label()
	}
	d.button("s_mdm_name", sub(mt.Name, name, pend))
	switch u.modemSecond(app, m) {
	case "unlock":
		d.button("s_mdm_pin", btn{label: mt.Unlock, col: colp(cWarn), active: true, dim: pend, size: 18})
	case "subnet":
		d.button("s_mdm_pin", btn{label: mt.Subnet, dim: pend, size: 18})
		if u.confirming("modem_subnet") {
			d.frameR(lay.Buttons["s_mdm_pin"], cWarn, 2, 10*s)
		}
	}
	d.button("s_mdm_reset", btn{label: mt.Reset, dim: dim("reset"), size: 18})
	if u.confirming("modem_reset") {
		d.frameR(lay.Buttons["s_mdm_reset"], cWarn, 2, 10*s)
	}
	// The note: a confirmation waiting, a result, or nothing
	note, warn := "", true
	switch {
	case u.confirmingPrefix("modem_force:"),
		u.confirming("modem_reset") && u.onlyOnAir(app.Status, m.ID),
		u.confirming("modem_subnet") && u.subnetCuts(app.Status, m):
		note = mt.ForceConfirm // it would cut the stream: said so
	case u.confirming("modem_reset"):
		note = mt.ResetConfirm
	case u.confirming("modem_subnet"):
		note = mt.SubnetConfirm
	default:
		if t, w := u.pageNoteText(); t != "" {
			note, warn = t, w
		} else if t := u.resultNote(app, "modem_save", "modem_pin", "modem_reset", "modem_subnet"); t != "" {
			note = t
		} else if t := modemDone(app, mt, u.pageSince, u.now()); t != "" {
			note, warn = t, false
		}
	}
	if note != "" {
		col := cQuiet
		if warn {
			col = cWarn
		}
		d.text(x0+4*s, float64(pg.Y+pg.H)-8*s, note, 15*s, textOpt{col: col, bold: warn, width: w0 - 8*s})
	}
}

func tailOf(t string) string {
	if t == "" {
		return ""
	}
	return "…" + t
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// fmtSigned: a number with the language's decimal mark and a real minus sign.
func (u *UI) fmtSigned(v float64, dec int) string {
	t := strings.ReplaceAll(sprintf("%.*f", dec, v), ".", u.Tx.Decimal)
	return strings.Replace(t, "-", "−", 1)
}

// --- taps --------------------------------------------------------------------------------------------------------

func (u *UI) modemTouch(what string) bool {
	if what == "s_back" {
		u.modemView, u.confirm = "", nil
		u.Dirty = true
		return true
	}
	app := u.app().AppSnapshot()
	m := u.curModem(&app)
	if m == nil || !strings.HasPrefix(what, "s_mdm_") {
		return what == "s_back"
	}
	if strings.HasPrefix(app.Pending, "modem_") {
		return true
	}
	mt := u.Tx.Modem
	id := m.ID
	// What reconnects the modem while it is the only one on air asks first: a second tap on the same button within
	// ConfirmFor, and only then the picker or the keyboard (what is typed is never thrown away), sent with force
	gate := func(key string) (force, ok bool) {
		if !u.onlyOnAir(app.Status, id) {
			return false, true
		}
		return true, u.confirmTap("modem_force:" + key)
	}
	can := func(what string) bool {
		if !m.CanDo(what) {
			u.notePage(mt.NotSupported, true)
			return false
		}
		return true
	}
	save := func(force bool, body map[string]any) {
		if force {
			body["force"] = true
		}
		u.modemDo(AppAction{What: "modem_save", ID: id, Body: body})
	}
	switch what {
	case "s_mdm_mode":
		if !can("mode") {
			return true
		}
		force, ok := gate("mode")
		if !ok {
			return true
		}
		var opts []pickOpt
		for _, md := range m.Modes {
			opts = append(opts, pickOpt{ID: md, Title: mt.Modes[md]})
		}
		u.openPicker(mt.Network, orAuto(m.Mode), opts, func(u *UI, pick string) {
			if pick != orAuto(m.Mode) {
				save(force, map[string]any{"mode": pick})
			}
		})
	case "s_mdm_apn":
		if !can("apn") {
			return true
		}
		force, ok := gate("apn")
		if !ok {
			return true
		}
		u.openSpecKeyboard(&kbSpec{title: mt.APN, help: mt.APNHelp, max: 63, allow: apnRune,
			done: func(u *UI, text string, _ []byte) string {
				if text != m.APN {
					save(force, map[string]any{"apn": text})
				}
				return ""
			}}, m.APN)
	case "s_mdm_roam":
		if !can("roaming") {
			return true
		}
		if force, ok := gate("roam"); ok {
			save(force, map[string]any{"roaming": !m.Roaming})
		}
	case "s_mdm_name": // the name never reconnects anything
		u.openSpecKeyboard(&kbSpec{title: mt.Name, help: mt.NameHelp, max: 12,
			done: func(u *UI, text string, _ []byte) string {
				if text != m.Alias {
					u.modemDo(AppAction{What: "modem_save", ID: id, Body: map[string]any{"alias": text}})
				}
				return ""
			}}, m.Alias)
	case "s_mdm_pin":
		switch u.modemSecond(&app, m) {
		case "unlock": // as the web's form: unlock, or unlock and turn the PIN off; then the PIN
			keep := mt.UnlockKeep
			if m.Backend == "hilink" {
				keep = mt.UnlockOnce // no profile to keep it in (MODEMS.md §7.3)
			}
			u.openPicker(mt.Unlock, "", []pickOpt{{ID: "keep", Title: mt.UnlockOnly, Sub: keep},
				{ID: "remove", Title: mt.UnlockRemove, Sub: mt.PINRemoveHelp}},
				func(u *UI, pick string) { u.modemPINKeyboard(id, pick == "remove") })
		case "subnet":
			if u.confirmTap("modem_subnet") {
				body := map[string]any{}
				if u.subnetCuts(app.Status, m) {
					body["force"] = true // the second tap said so: the note named the cut
				}
				u.modemDo(AppAction{What: "modem_subnet", ID: id, Body: body})
			}
		}
	case "s_mdm_reset":
		if !can("reset") || !u.confirmTap("modem_reset") {
			return true
		}
		body := map[string]any{}
		if u.onlyOnAir(app.Status, id) {
			body["force"] = true // the second tap said so: the note named the cut (ForceConfirm)
		}
		u.modemDo(AppAction{What: "modem_reset", ID: id, Body: body})
	}
	u.Dirty = true
	return true
}

// modemDone: what the last order did, for 8 s after it (resultNote says what failed).
func modemDone(app *AppSnapshot, mt ModemStrings, since, now time.Time) string {
	l := app.Last
	if l == nil || !l.OK || now.Sub(l.At) > 8*time.Second || l.At.Before(since) {
		return ""
	}
	return map[string]string{"modem_save": mt.Saved, "modem_pin": mt.Unlocked, "modem_reset": mt.Resetting,
		"modem_subnet": mt.SubnetDone}[l.What]
}

// modemPINKeyboard: the SIM's PIN, secret and digits only; to the app straight away (Do copies it, the keyboard is
// wiped right after), never to the engine.
func (u *UI) modemPINKeyboard(id string, remove bool) {
	mt := u.Tx.Modem
	u.openSpecKeyboard(&kbSpec{title: mt.PINTitle, help: mt.PINHelp, secret: true, max: 8, min: 4, layer: "sym1",
		allow: func(r rune) bool { return r >= '0' && r <= '9' },
		done: func(u *UI, _ string, pin []byte) string {
			u.modemDo(AppAction{What: "modem_pin", ID: id, Body: map[string]any{"remove": remove},
				Secrets: map[string][]byte{"pin": pin}})
			return ""
		}}, "")
}

// modemDo sends an order to the app.
func (u *UI) modemDo(a AppAction) {
	if !u.app().Do(a) {
		u.appBusy(false)
	}
}

// apnRune: the characters of an APN.
func apnRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-'
}

// modemHealth: the modem's dot and reason now: its uplink's, or (without a link) its entry in the links.
func modemHealth(st *StatusInfo, m *ModemInfo) (string, string) {
	if st == nil {
		return "", ""
	}
	for _, ul := range st.Uplinks {
		if ul.ModemID == m.ID {
			return ul.Health, ul.HealthCode
		}
	}
	for _, l := range st.Links {
		if l.Name == m.Iface || l.Name == m.ID {
			return l.Health, l.Code
		}
	}
	return "", ""
}
