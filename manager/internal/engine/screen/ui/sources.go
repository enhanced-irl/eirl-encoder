package ui

import (
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The Sources page (CONTRACT §3): what each of the N slots is (automatic, off, or one source pinned) and its
// alias. Edits stay on the screen (a draft) until «Save»: PUT /sources/slots with all of them. The app validates
// (duplicate, invalid) and says «on air» if moving the source on air while streaming; its messages come in the
// device language.

// AliasMax is the longest alias (runes).
const AliasMax = 24

// aliasOK is the contract's alias: 0-24 runes of letters, digits, space and -_.'’&()#+/ (trimmed; the app also
// takes the typographic apostrophe, so an alias set on the web stays valid here).
func aliasOK(a string) bool {
	if len([]rune(a)) > AliasMax {
		return false
	}
	for _, r := range a {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || strings.ContainsRune("-_.'’&()#+/", r)) {
			return false
		}
	}
	return true
}

// slotsBase is the app's slots as N assignments (a missing slot is auto).
func (u *UI) slotsBase(app *AppSnapshot) []SlotAssign {
	out := make([]SlotAssign, u.Lay.N)
	for i := range out {
		out[i] = SlotAssign{Slot: i + 1, Assign: "auto"}
	}
	if app.Slots != nil {
		for _, s := range app.Slots.Slots {
			if s.Slot >= 1 && s.Slot <= len(out) {
				out[s.Slot-1] = SlotAssign{Slot: s.Slot, Assign: s.Assign, Alias: s.Alias}
			}
		}
	}
	return out
}

// srcSlots is what the page shows: the draft being edited, else the app's.
func (u *UI) srcSlots(app *AppSnapshot) []SlotAssign {
	if u.srcDraft != nil {
		return u.srcDraft
	}
	return u.slotsBase(app)
}

// srcEdit starts editing (a copy of what is shown).
func (u *UI) srcEdit() []SlotAssign {
	if u.srcDraft == nil {
		app := u.app().AppSnapshot()
		u.srcDraft = u.slotsBase(&app)
	}
	return u.srcDraft
}

// srcDirty: the draft differs from the app's.
func (u *UI) srcDirty(app *AppSnapshot) bool {
	if u.srcDraft == nil {
		return false
	}
	base := u.slotsBase(app)
	for i := range base {
		if i >= len(u.srcDraft) || base[i] != u.srcDraft[i] {
			return true
		}
	}
	return false
}

// candidate is the source id's candidate, if the app lists it.
func candidate(app *AppSnapshot, id string) (SlotCandidate, bool) {
	if app.Slots != nil {
		for _, c := range app.Slots.Candidates {
			if c.ID == id {
				return c, true
			}
		}
	}
	return SlotCandidate{}, false
}

// assignLabel is what a slot is set to, and its sub-text (what is in it now; disconnected).
func (u *UI) assignLabel(app *AppSnapshot, s SlotAssign) (string, string, bool) {
	tx := u.Tx
	var cur *SlotSource
	if app.Slots != nil {
		for _, x := range app.Slots.Slots {
			if x.Slot == s.Slot {
				cur = x.Current
			}
		}
	}
	switch s.Assign {
	case "auto":
		if cur != nil && cur.Name != "" {
			return tx.SrcAuto, u.sourceName(cur.ID, cur.Name), false
		}
		return tx.SrcAuto, tx.SrcAutoSub, false
	case "off":
		return tx.SrcOff, tx.SrcOffSub, false
	}
	name := u.sourceName(s.Assign, s.Assign)
	c, ok := candidate(app, s.Assign)
	if ok && c.Name != "" {
		name = u.sourceName(c.ID, c.Name)
	}
	if !ok || !c.Connected {
		return name, tx.SrcDisconnected, true
	}
	return name, tx.SrcPinned, false
}

// srcFieldError is the app's error for a slot's field (slots.<n>.assign | alias), from the last save.
func (u *UI) srcFieldError(app *AppSnapshot, slot int, field string) string {
	l := app.Last
	if l == nil || l.OK || l.What != "slots" || !l.At.After(u.srcSavedAt) || l.At.Before(u.pageSince) {
		return ""
	}
	f, ok := l.Fields["slots."+strconv.Itoa(slot)+"."+field]
	if !ok {
		return ""
	}
	if f.Message != "" {
		return f.Message
	}
	return sprintf(u.Tx.AppMessageF, f.Code)
}

func (u *UI) drawSourcesPage(d *dc, app *AppSnapshot) {
	lay, tx, s := u.Lay, u.Tx, d.s
	m := float64(lay.M)
	if !u.devicePageReady(d, app.SlotsAPI, app.Slots != nil) {
		return
	}
	u.srcSync(app)
	slots := u.srcSlots(app)
	var errs []string
	for i, sl := range slots {
		n := strconv.Itoa(i + 1)
		ak, lk := "s_src_"+n, "s_alias_"+n
		ar := lay.Buttons[ak]
		mid := float64(ar.Y) + float64(ar.H)/2
		lw := float64(ar.X-lay.SettingsPage.X) - m
		src := u.slotSource(i + 1)
		if src != nil && src.State == "hidden" { // its number, and «hidden» under it
			d.text(float64(lay.SettingsPage.X)+4*s, mid-1*s, sprintf(tx.SourceF, i+1), 19*s, textOpt{col: cInk, bold: true,
				width: lw})
			d.text(float64(lay.SettingsPage.X)+4*s, mid+16*s, tx.Input.Hidden, 14*s, textOpt{col: cWarn, bold: true, width: lw})
		} else {
			d.text(float64(lay.SettingsPage.X)+4*s, mid+7*s, sprintf(tx.SourceF, i+1), 19*s, textOpt{col: cInk, bold: true,
				width: lw})
		}
		if src != nil {
			u.drawHide(d, "s_hide_"+n, src)
		}
		label, sub, warn := u.assignLabel(app, sl)
		ea, el := u.srcFieldError(app, sl.Slot, "assign"), u.srcFieldError(app, sl.Slot, "alias")
		col := cBrand
		if ea != "" || warn {
			col = cWarn
		}
		b := btn{label: label, sub: sub, hasSub: true, col: colp(col), active: ea != ""}
		if warn {
			b.subCol = colp(cWarn)
		}
		d.button(ak, b)
		u.chevron(d, ar)
		alias, dim := sl.Alias, false
		if alias == "" {
			alias, dim = tx.SrcAliasEmpty, true
		}
		d.button(lk, btn{label: alias, dim: dim, col: colp(cWarn), active: el != ""})
		for _, e := range []string{ea, el} {
			if e != "" {
				errs = append(errs, sprintf(tx.SourceF, sl.Slot)+tx.Sep+e)
			}
		}
	}
	// The rules, under the rows: what a slot is, and Hide
	if last := lay.Buttons["s_alias_"+strconv.Itoa(len(slots))]; len(slots) > 0 {
		pg := lay.SettingsPage
		y := float64(last.Y+last.H) + 30*s
		for i, help := range []string{tx.SrcHelp, tx.Input.HideHelp} {
			if i > 0 {
				y += 6 * s
			}
			for _, line := range d.wrap(help, 15*s, Sans, float64(pg.W)-8*s) {
				d.text(float64(pg.X)+4*s, y, line, 15*s, textOpt{col: cQuiet, width: float64(pg.W) - 8*s})
				y += 21 * s
			}
		}
	}
	dirty := u.srcDirty(app)
	d.button("s_src_save", btn{label: tx.SrcSave, active: dirty, dim: !dirty || app.Pending == "slots"})
	// The note: the app's errors, else on air, else whether it is saved
	note, warn := "", true
	l := app.Last
	fresh := l != nil && l.What == "slots" && l.At.After(u.srcSavedAt) && !l.At.Before(u.pageSince) &&
		u.now().Sub(l.At) < 8*time.Second
	pnote, pwarn := u.pageNoteText() // the engine refused a Hide (the source on air)
	switch {
	case pnote != "":
		note, warn = pnote, pwarn
	case len(errs) > 0:
		note = errs[0]
	case fresh && !l.OK && l.Code == "on_air":
		note = u.resultText(l)
		if l.Message == "" {
			note = tx.SrcOnAir
		}
	case fresh && !l.OK:
		note = u.resultText(l)
	case u.srcNote != "" && u.now().Before(u.srcNoteUntil):
		note = u.srcNote
	case dirty:
		note, warn = tx.SrcUnsaved, false
	case fresh && l.OK && (!app.Slots.EngineSupports || (l.Applied != nil && !*l.Applied)):
		note, warn = tx.SrcEngineOld, false // saved; the engine takes it when it starts
	case fresh && l.OK:
		note, warn = tx.SrcSaved, false
	case !app.Slots.EngineSupports:
		note, warn = tx.SrcEngineOld, false
	}
	sb := lay.Buttons["s_src_save"]
	pg := lay.SettingsPage
	tc := cQuiet
	if warn {
		tc = cWarn
	}
	d.text(float64(pg.X)+4*s, float64(sb.Y)+float64(sb.H)/2+6*s, note, 15*s, textOpt{col: tc, bold: warn,
		width: float64(sb.X-pg.X) - 2*m})
}

// chevron is a › at the right of a button that opens a choice.
func (u *UI) chevron(d *dc, r Rect) {
	s := d.s
	d.text(float64(r.X+r.W)-10*s, float64(r.Y)+float64(r.H)/2+8*s, "›", 22*s, textOpt{col: cQuiet, align: right})
}

// srcSync: after a save the app answered OK, the draft goes (the app's is the saved one).
func (u *UI) srcSync(app *AppSnapshot) {
	l := app.Last
	if u.srcDraft != nil && l != nil && l.What == "slots" && l.OK && l.At.After(u.srcSavedAt) && !u.srcDirty(app) {
		u.srcDraft = nil
	}
}

// candidates are the choices of a slot: automatic, off, then each source (the app's list: connected ones and
// pinned ones that are not).
func (u *UI) candidates(app *AppSnapshot) []string {
	out := []string{"auto", "off"}
	if app.Slots != nil {
		for _, c := range app.Slots.Candidates {
			out = append(out, c.ID)
		}
	}
	return out
}

// drawCandidates is the picker of what a slot is, in the page: one row per choice, the current one marked.
func (u *UI) drawCandidates(d *dc, app *AppSnapshot) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	m := float64(lay.M)
	slots := u.srcSlots(app)
	if u.srcPick < 1 || u.srcPick > len(slots) {
		return
	}
	cur := slots[u.srcPick-1].Assign
	all := u.candidates(app)
	rows := lay.ListRows
	pages := max(1, (len(all)+rows-1)/rows)
	u.candPage = max(0, min(u.candPage, pages-1))
	lo := u.candPage * rows
	items := all[lo:min(len(all), lo+rows)]
	u.candShown = append(u.candShown[:0], items...)
	for i, id := range items {
		key := "s_cand_" + strconv.Itoa(i)
		r := lay.Buttons[key]
		q := rrOf(r, 10*s)
		active := id == cur
		if active {
			c.FillRR(q, cBrand.WithAlpha(0.24))
			c.StrokeRR(q, 2, cBrand)
		} else {
			c.FillRR(q, cPanel)
			c.StrokeRR(q, 1.25, cLine)
		}
		if d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
			d.lit(r, 10*s, 0.12)
		}
		mid := float64(r.Y) + float64(r.H)/2
		var title, sub string
		subCol := cQuiet
		switch id {
		case "auto":
			title, sub = tx.SrcAuto, tx.SrcAutoSub
		case "off":
			title, sub = tx.SrcOff, tx.SrcOffSub
		default:
			cd, _ := candidate(app, id)
			title = u.sourceName(id, cd.Name)
			if title == "" {
				title = id
			}
			parts := []string{}
			if k := tx.KindName[cd.Kind]; k != "" && k != title { // not «Board HDMI · Board HDMI»
				parts = append(parts, k)
			}
			if !cd.Connected {
				parts, subCol = append(parts, tx.SrcDisconnected), cWarn
			}
			for _, o := range slots {
				if o.Assign == id && o.Slot != u.srcPick {
					parts = append(parts, sprintf(tx.SrcInSlotF, o.Slot))
				}
			}
			sub = strings.Join(parts, tx.Sep)
		}
		x0 := float64(r.X) + 2*m
		right := float64(r.X+r.W) - 2*m
		if active {
			u.icon(c, "check", right-12*s, mid, 12*s, max(2.0, 2.8*s), cBrand)
			right -= 34 * s
		}
		d.text(x0, mid-2*s, title, 19*s, textOpt{col: cInk, bold: active, width: right - x0})
		d.text(x0, mid+17*s, sub, 14*s, textOpt{col: subCol, width: right - x0})
	}
	if pages > 1 {
		if u.candPage > 0 {
			d.button("s_prev", btn{label: tx.PrevPage})
		}
		if u.candPage < pages-1 {
			d.button("s_next", btn{label: tx.NextPage})
		}
		u.pagerNote(d, sprintf(tx.PageF, u.candPage+1, pages), false)
	}
}

// sourcesKeys are the Sources page's buttons on view.
func (u *UI) sourcesKeys(app *AppSnapshot) []string {
	if app.SlotsAPI != Yes || app.Slots == nil {
		return nil
	}
	var keys []string
	for i := range u.Lay.N {
		n := strconv.Itoa(i + 1)
		keys = append(keys, "s_src_"+n, "s_alias_"+n)
		if u.slotSource(i+1) != nil {
			keys = append(keys, "s_hide_"+n)
		}
	}
	return append(keys, "s_src_save")
}

// candKeys are the picker's buttons on view.
func (u *UI) candKeys() []string {
	keys := []string{"s_back"}
	for i := range u.candShown {
		keys = append(keys, "s_cand_"+strconv.Itoa(i))
	}
	return append(keys, "s_prev", "s_next")
}

// srcTouch is a tap on the Sources page or its picker; false if it was not one of theirs.
func (u *UI) srcTouch(what, pick string, hasPick bool) bool {
	switch {
	case strings.HasPrefix(what, "s_src_") && what != "s_src_save":
		n, err := strconv.Atoi(what[len("s_src_"):])
		if err != nil {
			return true
		}
		u.srcPick, u.candPage, u.settingsView = n, 0, "cands"
		u.candShown = u.candShown[:0]
	case strings.HasPrefix(what, "s_alias_"):
		n, err := strconv.Atoi(what[len("s_alias_"):])
		slots := u.srcEdit()
		if err != nil || n < 1 || n > len(slots) {
			return true
		}
		u.openTextKeyboard("alias", n, slots[n-1].Alias, AliasMax)
	case what == "s_src_save":
		u.srcSave()
	case strings.HasPrefix(what, "s_hide_"):
		if n, err := strconv.Atoi(what[len("s_hide_"):]); err == nil {
			u.hideSlot(n)
		}
	case strings.HasPrefix(what, "s_cand_") && hasPick:
		u.srcChoose(pick)
	case what == "s_back" && u.settingsView == "cands":
		u.settingsView = "main"
	case (what == "s_prev" || what == "s_next") && u.settingsView == "cands":
		if what == "s_next" {
			u.candPage++
		} else {
			u.candPage = max(0, u.candPage-1)
		}
	default:
		return false
	}
	u.Dirty = true
	return true
}

// srcChoose sets the picked slot to a choice; a source pinned to another slot is not pinned twice (says so).
func (u *UI) srcChoose(id string) {
	slots := u.srcEdit()
	if u.srcPick < 1 || u.srcPick > len(slots) {
		return
	}
	if id != "auto" && id != "off" {
		for _, o := range slots {
			if o.Assign == id && o.Slot != u.srcPick {
				app := u.app().AppSnapshot()
				cd, _ := candidate(&app, id)
				name := u.sourceName(id, cd.Name)
				if name == "" {
					name = id
				}
				u.srcSay(sprintf(u.Tx.SrcDuplicateF, name, o.Slot))
				u.settingsView = "main"
				return
			}
		}
	}
	slots[u.srcPick-1].Assign = id
	u.settingsView = "main"
}

// aliasDone is the alias keyboard's «Done»: trimmed and checked, into the draft.
func (u *UI) aliasDone() {
	kb := u.kb
	a := strings.TrimSpace(string(kb.text))
	if !aliasOK(a) {
		kb.err = u.Tx.SrcAliasBad
		return
	}
	slots := u.srcEdit()
	if kb.slot >= 1 && kb.slot <= len(slots) {
		slots[kb.slot-1].Alias = a
	}
	u.closeKeyboard()
}

// srcSave sends all the slots (only with changes).
func (u *UI) srcSave() {
	app := u.app().AppSnapshot()
	if !u.srcDirty(&app) || app.Pending == "slots" {
		return
	}
	u.srcSavedAt = u.now().Add(-time.Millisecond) // results from now on are this save's
	if !u.app().Do(AppAction{What: "slots", Slots: append([]SlotAssign(nil), u.srcDraft...)}) {
		u.appBusy(false)
	}
}

func (u *UI) srcSay(t string) {
	u.srcNote, u.srcNoteUntil = t, u.now().Add(6*time.Second)
}

// --- Hide: a source out of the mixer (the web's Hide/Show on each tile), live: not part of the draft ---

// slotSource is the source the engine has in a slot now (nil: none).
func (u *UI) slotSource(slot int) *SourceStatus {
	for i := range u.st.Sources {
		if u.st.Sources[i].Slot == slot {
			return &u.st.Sources[i]
		}
	}
	return nil
}

// drawHide is a slot's Hide (Show while hidden, in amber). The source on air is dimmed (the engine refuses hiding
// it; a tap says so).
func (u *UI) drawHide(d *dc, key string, src *SourceStatus) {
	tx := u.Tx.Input
	if src.State == "hidden" {
		d.button(key, btn{label: tx.Show, active: true, col: colp(cWarn)})
		return
	}
	d.button(key, btn{label: tx.Hide, dim: src.ID == u.st.mainID()})
}

// hideSlot hides the slot's source, or shows it again: the engine's command (a refusal shows on the page).
func (u *UI) hideSlot(slot int) {
	src := u.slotSource(slot)
	if src == nil {
		return
	}
	u.send("page", map[string]any{"cmd": "hide", "source": src.ID, "hidden": src.State != "hidden"}, 0, 0)
}
