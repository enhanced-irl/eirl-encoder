package ui

// The connections' dots in the header (MODEMS.md §6): one per internet connection, green, amber, red or a grey ring
// (switched off), with a short name. They come from the app's links.json (the engine reads it; Links), the one
// judgement of every link; the whole area opens Settings › Streaming › Connections.

// LinkDot is one connection as the header draws it.
type LinkDot struct {
	Name     string
	Kind     string // ethernet | usb | wifi | modem
	Label    string // the modem's operator or its alias ("" = named by its kind)
	Tech     string // 2g | 3g | 4g | 5g-nsa | 5g (modems)
	Health   string // good | weak | bad | off
	Code     string // why (MODEMS.md §5): Stream.UplinkHealth translates it
	Disabled bool
}

// techShort: a modem's technology as the header shows it when names do not fit.
var techShort = map[string]string{"2g": "2G", "3g": "3G", "4g": "4G", "5g-nsa": "5G", "5g": "5G"}

// linkDots: the connections' dots, and whether the header shows them (the app's file is there and fresh).
func (u *UI) linkDots() ([]LinkDot, bool) {
	if u.Links == nil {
		return nil, false
	}
	l, ok := u.Links()
	return l, ok && len(l) > 0
}

// dotName: a connection's name at a level of detail: 0 its label (or its kind), 1 short (a modem's technology,
// else its kind), 2 none (only the dot).
func (u *UI) dotName(l LinkDot, level int) string {
	switch level {
	case 0:
		if l.Label != "" {
			return l.Label
		}
	case 2:
		return ""
	}
	if l.Kind == "modem" {
		if t := techShort[l.Tech]; t != "" {
			return t
		}
	}
	if n := u.Tx.LinkShort[l.Kind]; n != "" {
		return n
	}
	return l.Kind
}

// drawLinkDots draws the dots centred between x0 and x1 at height y, with the most detail that fits: names, names in
// a smaller size, short names, then dots only. It returns false if not even the dots fit.
func (u *UI) drawLinkDots(d *dc, x0, x1, y float64, dots []LinkDot) bool {
	s, c := d.s, d.c
	r, gap, space := 6.5*s, 6*s, 16*s
	width := func(level int, size float64) float64 {
		w := 0.0
		for i, l := range dots {
			if i > 0 {
				w += space
			}
			w += 2 * r
			if n := u.dotName(l, level); n != "" {
				w += gap + d.advance(n, size, false)
			}
		}
		return w
	}
	type fit struct {
		level int
		size  float64
	}
	var chosen *fit
	for _, f := range []fit{{0, 15 * s}, {0, 13 * s}, {1, 15 * s}, {2, 15 * s}} {
		if width(f.level, f.size) <= x1-x0 {
			chosen = &f
			break
		}
	}
	if chosen == nil {
		return false
	}
	level, size := chosen.level, chosen.size
	x := (x0+x1)/2 - width(level, size)/2
	for i, l := range dots {
		if i > 0 {
			x += space
		}
		col, ok := healthDot[l.Health]
		if !ok {
			col = cQuiet
		}
		cx := x + r
		c.FillCircle(cx, y, r, col)
		if l.Health == "off" || l.Disabled {
			c.FillCircle(cx, y, r-2.2*s, cBg) // a ring: switched off, not a fault
		}
		x += 2 * r
		if n := u.dotName(l, level); n != "" {
			tc := cInk
			if l.Disabled {
				tc = cQuiet
			}
			x += gap
			x += d.text(x, y+size*0.36, n, size, textOpt{col: tc})
		}
	}
	return true
}
