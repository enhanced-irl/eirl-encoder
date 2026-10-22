package ui

import "strconv"

// SettingsGroup is one entry of the Settings list: its pages, shown as tabs in the page's title row when there
// are several (a group of one page shows its name there).
type SettingsGroup struct {
	ID    string
	Pages []string
}

// SettingsGroups are the Settings list's entries, in order (seven: what fits at 1024×600 with entries a finger
// can hit). A list entry is s_page_<group>; a tab, s_tab_<page>. A group of one page has the page's id.
var SettingsGroups = []SettingsGroup{
	{"stream", []string{"dest", "uplinks", "pause"}},
	{"sources", []string{"sources", "input", "camera"}},
	{"wifi", []string{"wifi", "bluetooth"}}, // Wireless (BLUETOOTH.md D6: a tab, so the list keeps seven entries)
	{"cams", []string{"cams"}},
	{"cloud", []string{"cloud"}},
	{"device", []string{"language", "time", "account"}},
	{"system", []string{"updates", "about"}},
}

// SettingsPages are every Settings page, in the list's order.
var SettingsPages = func() []string {
	var out []string
	for _, g := range SettingsGroups {
		out = append(out, g.Pages...)
	}
	return out
}()

// groupOf is the group a page belongs to (nil: none).
func groupOf(page string) *SettingsGroup {
	for i := range SettingsGroups {
		for _, p := range SettingsGroups[i].Pages {
			if p == page {
				return &SettingsGroups[i]
			}
		}
	}
	return nil
}

// groupByID is a group by its id (nil: none).
func groupByID(id string) *SettingsGroup {
	for i := range SettingsGroups {
		if SettingsGroups[i].ID == id {
			return &SettingsGroups[i]
		}
	}
	return nil
}

// Stepper fields of the manual date and time (the Date & time page, NTP off).
var TimeFields = []string{"day", "mon", "year", "hour", "min"}

// settingsLayout places Settings: below the header, a panel with the list of pages on the left (its title row,
// then one entry per page, ≥ 48 px high) and the selected page on the right (a title row with its name, ← in a
// sub-view and the X; then its content). The wizard uses the same page area, so its pages are Settings' pages.
func (l *Layout) settingsLayout() {
	dw, dh, m, hb, ib := l.DW, l.DH, l.M, l.HB, l.IB
	sb := l.Status.H
	b := l.Buttons
	l.SettingsPanel = Rect{m, sb + m, dw - 2*m, dh - sb - 2*m}
	p := l.SettingsPanel
	gap := m / 2
	lw := max(200, p.W*24/100)
	l.SettingsHead = Rect{p.X + m, p.Y + m, lw, hb}
	l.SettingsList = Rect{p.X + m, p.Y + 2*m + hb, lw, p.Y + p.H - m - (p.Y + 2*m + hb)}
	n := len(SettingsGroups)
	l.SettingsEntryH = max(48, min(66, (l.SettingsList.H-(n-1)*gap)/max(1, n)))
	for i, g := range SettingsGroups {
		b["s_page_"+g.ID] = Rect{l.SettingsList.X, l.SettingsList.Y + i*(l.SettingsEntryH+gap), lw, l.SettingsEntryH}
	}
	px := p.X + m + lw + m
	pw := p.X + p.W - m - px
	l.SettingsTitle = Rect{px, p.Y + m, pw, hb}
	l.SettingsPage = Rect{px, p.Y + 2*m + hb, pw, p.Y + p.H - m - (p.Y + 2*m + hb)}
	b["s_close"] = Rect{px + pw - ib, p.Y + m, ib, hb}
	b["s_back"] = Rect{px, p.Y + m, ib, hb}
	// The tabs of a group of several pages: the title row up to the X, in equal parts (only one group's are on view)
	for _, g := range SettingsGroups {
		if len(g.Pages) < 2 {
			continue
		}
		tw := (pw - ib - m - (len(g.Pages)-1)*gap) / len(g.Pages)
		for i, pg := range g.Pages {
			b["s_tab_"+pg] = Rect{px + i*(tw+gap), p.Y + m, tw, hb}
		}
	}
	l.settingsClient = -1
	l.SettingsRows(true)
	c := l.SettingsPage
	rh := hb + hb/6
	// The client wifi's network list (WIFI.md §6): one row per network (with «Olvidar» on known ones) and pages
	pg := 48
	l.ListRows = max(3, (c.H-pg-gap)/(rh+gap))
	fw := max(110, dw/9)
	for i := range l.ListRows {
		y := c.Y + i*(rh+gap)
		b["s_row_"+strconv.Itoa(i)] = Rect{c.X, y, c.W, rh}
		b["s_forget_"+strconv.Itoa(i)] = Rect{c.X + c.W - m - fw, y + (rh-44)/2, fw, 44}
	}
	pw2 := max(150, dw/7)
	b["s_prev"] = Rect{c.X, c.Y + c.H - pg, pw2, pg}
	b["s_next"] = Rect{c.X + c.W - pw2, c.Y + c.H - pg, pw2, pg}
	// Language: one big row per language, by its own name
	lh := max(56, hb+hb/3)
	for i, code := range Languages {
		b["s_lang_"+code] = Rect{c.X, c.Y + i*(lh+m), c.W, lh}
	}
	// Date & time: the clock on top, the automatic time and the zone rows, and (automatic off) the steppers
	// of the date and of the time, and «Set»
	ch := max(64, dh/9)
	l.TimeClock = Rect{c.X, c.Y, c.W, ch}
	y := c.Y + ch + gap
	b["s_ntp"] = Rect{c.X, y, c.W, rh}
	y += rh + gap
	b["s_zone"] = Rect{c.X, y, c.W, rh}
	y += rh + m
	sh := 48
	sw := max(48, min(64, c.W/12))
	vw := max(72, c.W/8)
	step := 2*sw + vw + m
	for i, f := range TimeFields {
		row, col := 0, i
		if i >= 3 {
			row, col = 1, i-3
		}
		x := c.X + col*(step+m)
		yy := y + row*(sh+gap)
		b["s_t_"+f+"_m"] = Rect{x, yy, sw, sh}
		b["s_t_"+f+"_p"] = Rect{x + sw + vw, yy, sw, sh}
	}
	setW := max(160, c.W/4)
	b["s_t_set"] = Rect{c.X + c.W - setW, y + sh + gap, setW, sh}
	// The zone picker: the regions in two rows of chips, then the zones of one in a grid, with pages
	chipW := (c.W - 5*gap) / 6
	for i := range 12 {
		b["s_region_"+strconv.Itoa(i)] = Rect{c.X + (i%6)*(chipW+gap), c.Y + (i/6)*(44+gap), chipW, 44}
	}
	gy := c.Y + 2*(44+gap) + gap
	l.ZoneCols = 3
	if c.W/4 >= 160 {
		l.ZoneCols = 4
	}
	zh := 48
	l.ZoneRows = max(2, (c.Y+c.H-pg-gap-gy)/(zh+gap))
	zw := (c.W - (l.ZoneCols-1)*gap) / l.ZoneCols
	for i := range l.ZoneCols * l.ZoneRows {
		col, row := i%l.ZoneCols, i/l.ZoneCols
		b["s_zone_"+strconv.Itoa(i)] = Rect{c.X + col*(zw+gap), gy + row*(zh+gap), zw, zh}
	}
	// Updates: the version and its state on top, the buttons, then the release notes
	bw := max(200, c.W*2/5)
	uy := c.Y + 120 // under the version, a state of up to two lines and the last check
	b["s_up_check"] = Rect{c.X, uy, bw, sh + 8}
	b["s_up_install"] = Rect{c.X + bw + m, uy, c.W - bw - m, sh + 8}
	l.UpdateNotes = Rect{c.X, uy + sh + 8 + 3*m, c.W, c.Y + c.H - (uy + sh + 8 + 3*m)}
	// The restart that finishes an update (UPDATES.md §12.5): a row under the buttons, its words under «Check now»
	// and its button under «Update» (the notes go below it while it is there)
	l.UpdateRestart = Rect{c.X, uy + sh + 8 + m, c.W, sh + 16}
	b["s_up_reboot"] = Rect{c.X + bw + m, l.UpdateRestart.Y + 4, c.W - bw - m - 4, sh + 8}
	// Sources: one row per slot (its number; what it is; its alias; Hide/Show while a source is in it), and «Save»
	// at the bottom. The picker of what a slot is uses the network list's rows (s_cand_i)
	saveH := sh + 8
	rowH := max(52, min(64, (c.H-saveH-m)/max(1, l.N)))
	lab := max(96, c.W/7)
	hw := max(112, c.W/6)
	aw := (c.W - lab - hw - 2*m) / 2
	for i := range l.N {
		n := strconv.Itoa(i + 1)
		y := c.Y + i*rowH
		bh := min(48, rowH-4)
		b["s_src_"+n] = Rect{c.X + lab, y + (rowH-bh)/2, aw, bh}
		b["s_alias_"+n] = Rect{c.X + lab + aw + m, y + (rowH-bh)/2, c.W - lab - aw - hw - 2*m, bh}
		b["s_hide_"+n] = Rect{c.X + c.W - hw, y + (rowH-bh)/2, hw, bh}
	}
	b["s_src_save"] = Rect{c.X + c.W - max(200, c.W/4), c.Y + c.H - saveH, max(200, c.W/4), saveH}
	for i := range l.ListRows {
		b["s_cand_"+strconv.Itoa(i)] = b["s_row_"+strconv.Itoa(i)]
	}
	// The wizard (CONTRACT §2): its top bar instead of the header (‹ Back on the left; Skip and Next › on the
	// right), the steps as the list (non-interactive) with «Later» at the bottom, and its pages in the page area;
	// its own buttons in the page: choose the password, change the name, get a link code
	nw := max(170, dw/6)
	skw := max(140, dw/8)
	b["w_back"] = Rect{m, m, max(ib, 150), hb}
	b["w_next"] = Rect{dw - m - nw, m, nw, hb}
	b["w_skip"] = Rect{dw - 2*m - nw - skw, m, skw, hb}
	b["w_later"] = Rect{l.SettingsList.X, p.Y + p.H - m - 52, lw, 52}
	l.WizardList = Rect{l.SettingsList.X, l.SettingsList.Y, lw, b["w_later"].Y - m - l.SettingsList.Y}
	wb := Rect{c.X, c.Y + 84, max(300, c.W/2), 56}
	b["w_pw"], b["w_pair"] = wb, wb
	b["w_name"] = Rect{c.X, c.Y + 136, wb.W, wb.H} // under the name
	// About: the lines, and «Run setup again» at the bottom
	b["s_setup_again"] = Rect{c.X, c.Y + c.H - sh - 8, max(260, c.W/2), sh + 8}
	l.AboutLines = Rect{c.X, c.Y, c.W, c.H - sh - 8 - m}
	// The generic picker (a choice of a page's field): the network list's rows and pages
	for i := range l.ListRows {
		b["s_pick_"+strconv.Itoa(i)] = b["s_row_"+strconv.Itoa(i)]
	}
	// The pages that register their own layout (pagedef.go)
	l.pageLayouts()
}

// FormRows is the page area cut in rows for a form page: n rows of the same height (at most 64, at least 48) with
// a gap, and what is left at the bottom for its note and buttons. Pages use it so their fields line up.
func (l *Layout) FormRows(n int) (rows []Rect, bottom Rect) {
	c := l.SettingsPage
	gap := l.M / 2
	foot := max(52, l.HB+6)
	rh := max(48, min(64, (c.H-foot-l.M-(n-1)*gap)/max(1, n)))
	for i := range n {
		rows = append(rows, Rect{c.X, c.Y + i*(rh+gap), c.W, rh})
	}
	return rows, Rect{c.X, c.Y + c.H - foot, c.W, foot}
}

// halves splits a row in two with a gap between them.
func (l *Layout) halves(r Rect) (Rect, Rect) {
	w := (r.W - l.M) / 2
	return Rect{r.X, r.Y, w, r.H}, Rect{r.X + w + l.M, r.Y, r.W - w - l.M, r.H}
}

// stepperKeys places a stepper inside a field rectangle: − and + at its right, with room for the value between
// them (a number: its unit goes in the field's title).
func (l *Layout) stepperKeys(r Rect, minus, plus string) {
	bw := max(48, min(r.H, 56))
	l.Buttons[plus] = Rect{r.X + r.W - bw, r.Y, bw, r.H}
	l.Buttons[minus] = Rect{r.X + r.W - 2*bw - max(84, r.W/4), r.Y, bw, r.H}
}

// eyeKey places the eye of a secret field at the right of its rectangle, square.
func (l *Layout) eyeKey(r Rect, key string) Rect {
	bw := max(48, min(r.H, 60))
	l.Buttons[key] = Rect{r.X + r.W - bw, r.Y, bw, r.H}
	return Rect{r.X, r.Y, r.W - bw - l.M/2, r.H}
}

// SettingsRows places the Wi-Fi and Network cameras pages' rows: «Connect to a Wi-Fi» (if the app has it,
// client), «Create my Wi-Fi», its QR and the column beside it; the cameras' reception switch (the rest of that page is
// camsLayout's) and, for an older app, its list. Without the client row, the one below moves up. Recomputed only
// when client changes.
func (l *Layout) SettingsRows(client bool) {
	c := 0
	if client {
		c = 1
	}
	if c == l.settingsClient {
		return
	}
	l.settingsClient = c
	dw, m, hb := l.DW, l.M, l.HB
	pg := l.SettingsPage
	rh := hb + hb/6
	top := pg.Y
	x, rw := pg.X, pg.W
	ya := top
	if client {
		ya += rh + m/2
	}
	b := l.Buttons
	b["s_client"] = Rect{x, top, rw, rh}
	b["s_ap"] = Rect{x, ya, rw, rh}
	b["s_net"] = Rect{x, top, rw, rh}
	l.SettingsQR = Rect{x, ya + rh + m, rw, pg.Y + pg.H - (ya + rh + m)}
	// Beside the QR (it is at most as wide as the area is high), a column from the bottom: «Rename» the AP (next to
	// the name with it off), «New password», the 2.4 GHz channel and the password with its eye (MIXER.md §21);
	// above them, the network's name or the page's notes
	q := l.SettingsQR
	col := Rect{q.X + q.H + m, q.Y, q.W - q.H - m, q.H}
	gap := m / 2
	bh, fh := max(52, hb+6), max(56, hb*6/5)
	b["s_ssid"] = Rect{col.X, col.Y + col.H - bh, col.W, bh}
	b["s_wifi_rotate"] = Rect{col.X, b["s_ssid"].Y - gap - bh, col.W, bh}
	b["s_wifi_ch"] = Rect{col.X, b["s_wifi_rotate"].Y - gap - fh, col.W, fh}
	b["s_wifi_pw"] = l.eyeKey(Rect{col.X, b["s_wifi_ch"].Y - gap - fh, col.W, fh}, "s_wifi_pw_eye")
	l.SettingsCams = Rect{x, top + rh + m, rw, pg.Y + pg.H - (top + rh + m)}
	nw := max(120, dw/8)
	sw := 64 * dw / 1280
	b["s_networks"] = Rect{x + rw - 2*m - sw - m - nw, top + (rh-44)/2, nw, 44}
	if l.keys != nil {
		l.sortKeys()
	}
}
