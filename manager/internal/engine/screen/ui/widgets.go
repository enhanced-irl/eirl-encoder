package ui

import (
	"strconv"
	"strings"
	"time"
)

// The form pieces the pages share (MIXER.md §21): a field (its title above, its value below, › when a tap opens a
// keyboard or a choice), a secret's value (dots, or the text with its eye open), a stepper (− value +), the eye and
// the generic picker (a list of choices in the page area, with pages).

// fieldOpt says how a field looks.
type fieldOpt struct {
	sub     string // a quiet line under the value ("" none)
	chevron bool   // › at the right: the tap opens something
	secret  bool   // the value is a secret: dots unless shown
	shown   bool   // the secret is shown (its eye is open)
	set     bool   // the secret has a value (else «not set»)
	warn    bool   // the value in amber (an error from the app)
	dim     bool   // nothing to tap (read-only)
	changed bool   // a draft not saved yet: outlined in brand
	mono    bool   // the value in monospaced type (keys, URLs: l, I, 1, O and 0)
	tail    bool   // a long value shows its end, not its start (a URL: its host and port)
	// placeholder: the value is what applies while none is set (quiet, not bold), the field still answers
	placeholder bool
}

// field draws a field's box in its button's rectangle: the title above, the value below.
func (u *UI) field(d *dc, key, title, value string, o fieldOpt) {
	lay, c, s := u.Lay, d.c, d.s
	m := float64(lay.M)
	r := lay.Buttons[key]
	q := rrOf(r, 10*s)
	switch {
	case o.changed:
		c.FillRR(q, cBrand.WithAlpha(0.12))
		c.StrokeRR(q, 2, cBrand)
	default:
		c.FillRR(q, cPanel)
		c.StrokeRR(q, 1.25, cLine)
	}
	if !o.dim && d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
		d.lit(r, 10*s, 0.12)
	}
	x0 := float64(r.X) + 1.5*m
	right := float64(r.X+r.W) - 1.5*m
	if o.chevron && !o.dim {
		u.chevron(d, r)
		right -= 18 * s
	}
	mid := float64(r.Y) + float64(r.H)/2
	ty, vy := mid-6*s, mid+17*s
	if float64(r.H) < 56*s { // a short row: the title and the value on one line
		ty, vy = mid+6*s, mid+7*s
	}
	tw := right - x0
	if float64(r.H) < 56*s {
		tw = d.advance(title, 14*s, false)
		d.text(x0, ty, title, 14*s, textOpt{col: cQuiet, width: right - x0})
		x0 += tw + m
	} else {
		d.text(x0, ty, title, 14*s, textOpt{col: cQuiet, width: tw})
	}
	col := cInk
	if o.warn {
		col = cWarn
	}
	if o.dim || o.placeholder {
		col = cQuiet
	}
	w := right - x0
	switch {
	case o.secret && !o.set:
		d.text(x0, vy, value, 17*s, textOpt{col: cQuiet, width: w})
	case o.secret:
		u.secretText(d, x0, vy, w, value, o.shown)
	default:
		style := SansBold
		switch {
		case o.mono:
			style = MonoBold
		case o.placeholder:
			style = Sans
		}
		t := value
		if o.tail {
			t = d.fitTail(t, 17*s, style, w)
		}
		d.text(x0, vy, t, 17*s, textOpt{col: col, style: &style, width: w})
	}
	if o.sub != "" && float64(r.H) >= 56*s {
		// with a sub-line, the value goes up a little (three lines in the box)
		d.text(x0, float64(r.Y+r.H)-6*s, o.sub, 12*s, textOpt{col: cQuiet, width: w})
	}
}

// secretText is a secret's value at (x, y): its characters in monospaced type with its eye open, else dots (as
// many as it has, at most what fits; never a character of it).
func (u *UI) secretText(d *dc, x, y, w float64, value string, shown bool) {
	s := d.s
	if shown {
		style := MonoBold
		d.text(x, y, d.fitTail(value, 17*s, style, w), 17*s, textOpt{col: cInk, style: &style})
		return
	}
	n := min(len(value), 16)
	if n == 0 {
		n = 8 // set, but its length is not known (the app gives only «set»): eight dots
	}
	sp := min(16*s, w/float64(n))
	rad := min(4.5*s, sp*0.32)
	for i := range n {
		d.c.FillCircle(x+rad+sp*float64(i), y-6*s, rad, cInk)
	}
}

// fitTail shortens a text from its start («…» first) so its end fits: a URL keeps its host and port.
func (d *dc) fitTail(t string, size float64, style FontStyle, width float64) string {
	r := []rune(t)
	if _, ink := d.c.Fonts.Extents(t, size, style); ink <= width {
		return t
	}
	for len(r) > 3 {
		r = r[2:]
		if _, ink := d.c.Fonts.Extents("…"+string(r), size, style); ink <= width {
			break
		}
	}
	return "…" + string(r)
}

// eye draws a secret's eye button: open (the secret is shown) or crossed out.
func (u *UI) eye(d *dc, key string, shown bool) {
	icon := "eye"
	if shown {
		icon = "eyeoff"
	}
	d.button(key, btn{icon: icon, active: shown})
}

// stepper draws − and + (their buttons) and the value between them.
func (u *UI) stepper(d *dc, minus, plus, value string, dimMinus, dimPlus bool) {
	lay, tx, s := u.Lay, u.Tx, d.s
	d.button(minus, btn{label: tx.Minus, dim: dimMinus})
	d.button(plus, btn{label: tx.Plus, dim: dimPlus})
	a, b := lay.Buttons[minus], lay.Buttons[plus]
	cx := float64(a.X+a.W+b.X) / 2
	d.text(cx, float64(a.Y)+float64(a.H)/2+7*s, value, 19*s, textOpt{col: cInk, bold: true, align: center,
		width: float64(b.X-a.X-a.W) - 4*s})
}

// stepperTitle is the title (and a sub-line) of a stepper's field, at the left of its −.
func (u *UI) stepperTitle(d *dc, r Rect, minus, title, sub string) {
	lay, s := u.Lay, d.s
	m := float64(lay.M)
	c := d.c
	q := rrOf(r, 10*s)
	c.FillRR(q, cPanel)
	c.StrokeRR(q, 1.25, cLine)
	x0 := float64(r.X) + 1.5*m
	w := float64(lay.Buttons[minus].X) - x0 - m
	mid := float64(r.Y) + float64(r.H)/2
	lines := d.wrap(title, 15*s, SansBold, w)
	if sub != "" || len(lines) > 2 {
		lines = []string{title} // one line (cut if it must) and the sub-line
	}
	y := mid + 5*s - float64(len(lines)-1)*9*s
	if sub != "" {
		y = mid - 3*s
	}
	for _, l := range lines {
		d.text(x0, y, l, 15*s, textOpt{col: cInk, bold: true, width: w})
		y += 18 * s
	}
	if sub != "" {
		d.text(x0, mid+16*s, sub, 12*s, textOpt{col: cWarn, width: w})
	}
}

// stepIn moves v one step of step inside [lo, hi] (dir −1 or +1), snapping to the step's grid.
func stepIn(v, step, lo, hi, dir int) int {
	n := v + dir*step
	if r := v % step; r != 0 { // off the grid (a value from the web): to the grid in the direction asked
		n = v - r
		if dir > 0 {
			n += step
		}
	}
	return max(lo, min(hi, n))
}

// stepList moves v to the next or previous value of a list (the nearest in that direction if v is not in it).
func stepList(v int, list []int, dir int) int {
	if dir > 0 {
		for _, x := range list {
			if x > v {
				return x
			}
		}
		return list[len(list)-1]
	}
	for i := len(list) - 1; i >= 0; i-- {
		if list[i] < v {
			return list[i]
		}
	}
	return list[0]
}

// confirmTap: a button that needs a second tap within ConfirmFor (forget, remove, unlink…). True when this tap
// confirms; else it arms the confirmation (the button shows «tap again» meanwhile: confirming(what)).
func (u *UI) confirmTap(what string) bool {
	now := u.now()
	if u.confirm != nil && u.confirm.what == what && now.Before(u.confirm.until) {
		u.confirm = nil
		return true
	}
	u.confirm = &confirm{what: what, until: now.Add(ConfirmFor)}
	u.Dirty = true
	return false
}

// confirming: what is waiting for its second tap.
func (u *UI) confirming(what string) bool {
	return u.confirm != nil && u.confirm.what == what && u.now().Before(u.confirm.until)
}

// --- the generic picker: a choice of a field, in the page area ---

// pickOpt is one choice: its id, what it says and a quiet line.
type pickOpt struct {
	ID, Title, Sub string
	Warn           bool // the sub-line in amber (disconnected, not supported…)
}

// picker is the open choice: its title, its options, the current one, and what to do with the chosen one.
type picker struct {
	title string
	opts  []pickOpt
	cur   string
	page  int
	shown []string // the option of each row, as drawn: a tap goes to what was seen
	done  func(u *UI, id string)
}

// openPicker shows a choice in the page area (settingsView "pick"); ← goes back without choosing.
func (u *UI) openPicker(title, cur string, opts []pickOpt, done func(u *UI, id string)) {
	p := &picker{title: title, opts: opts, cur: cur, done: done}
	rows := max(1, u.Lay.ListRows)
	for i, o := range opts {
		if o.ID == cur {
			p.page = i / rows
		}
	}
	u.chooser, u.settingsView = p, "pick"
	u.Dirty = true
}

func (u *UI) closePicker() {
	u.chooser = nil
	u.settingsView = "main"
	u.Dirty = true
}

func (u *UI) drawPicker(d *dc) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	m := float64(lay.M)
	p := u.chooser
	rows := lay.ListRows
	pages := max(1, (len(p.opts)+rows-1)/rows)
	p.page = max(0, min(p.page, pages-1))
	lo := p.page * rows
	items := p.opts[lo:min(len(p.opts), lo+rows)]
	p.shown = p.shown[:0]
	for i, o := range items {
		p.shown = append(p.shown, o.ID)
		key := "s_pick_" + strconv.Itoa(i)
		r := lay.Buttons[key]
		q := rrOf(r, 10*s)
		active := o.ID == p.cur
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
		x0 := float64(r.X) + 2*m
		right := float64(r.X+r.W) - 2*m
		if active {
			u.icon(c, "check", right-12*s, mid, 12*s, max(2.0, 2.8*s), cBrand)
			right -= 34 * s
		}
		if o.Sub == "" {
			d.text(x0, mid+7*s, o.Title, 19*s, textOpt{col: cInk, bold: active, width: right - x0})
			continue
		}
		subCol := cQuiet
		if o.Warn {
			subCol = cWarn
		}
		d.text(x0, mid-2*s, o.Title, 19*s, textOpt{col: cInk, bold: active, width: right - x0})
		d.text(x0, mid+17*s, o.Sub, 14*s, textOpt{col: subCol, width: right - x0})
	}
	if pages > 1 {
		if p.page > 0 {
			d.button("s_prev", btn{label: tx.PrevPage})
		}
		if p.page < pages-1 {
			d.button("s_next", btn{label: tx.NextPage})
		}
		u.pagerNote(d, sprintf(tx.PageF, p.page+1, pages), false)
	}
}

// pickerKeys are the picker's buttons on view.
func (u *UI) pickerKeys() []string {
	keys := []string{"s_back"}
	if u.chooser == nil {
		return keys
	}
	for i := range u.chooser.shown {
		keys = append(keys, "s_pick_"+strconv.Itoa(i))
	}
	return append(keys, "s_prev", "s_next")
}

// pickerTouch is a tap in the picker: a choice (the one seen when the finger went down), ←, or a page.
func (u *UI) pickerTouch(what, pick string, hasPick bool) {
	p := u.chooser
	switch {
	case p == nil || what == "s_back":
		u.closePicker()
	case what == "s_next":
		p.page++
	case what == "s_prev":
		p.page = max(0, p.page-1)
	case strings.HasPrefix(what, "s_pick_") && hasPick:
		done := p.done
		u.closePicker()
		if done != nil {
			done(u, pick)
		}
	}
	u.Dirty = true
}

// noteFor is how long a page's own notice («Saved», a refusal) stays.
const noteFor = 6 * time.Second

// pageNote is a page's own notice (what the screen itself says after a tap), until it expires.
type pageNote struct {
	text  string
	warn  bool
	until time.Time
}

func (u *UI) notePage(t string, warn bool) {
	u.pnote = pageNote{t, warn, u.now().Add(noteFor)}
	u.Dirty = true
}

// pageNoteText is the page's notice, if it has not expired.
func (u *UI) pageNoteText() (string, bool) {
	if u.pnote.text != "" && u.now().Before(u.pnote.until) {
		return u.pnote.text, u.pnote.warn
	}
	return "", false
}
