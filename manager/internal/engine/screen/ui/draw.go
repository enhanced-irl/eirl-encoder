package ui

import (
	"image"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/scene"
)

// The eirl theme (dark): greys 950/900/800/700/400/50, brand 500, success, warning, error.
var (
	cBg    = Color{0.047, 0.067, 0.114, 0.94}
	cPanel = Color{0.063, 0.094, 0.157, 0.92}
	cLine  = Color{0.204, 0.251, 0.329, 1}
	cQuiet = Color{0.596, 0.635, 0.702, 1}
	cInk   = Color{0.976, 0.98, 0.984, 1}
	cBrand = Color{0.275, 0.373, 1.0, 1}
	cGood  = Color{0.071, 0.718, 0.416, 1}
	cWarn  = Color{0.969, 0.565, 0.035, 1}
	cBad   = Color{0.941, 0.267, 0.22, 1}
	cDeep  = Color{0.031, 0.043, 0.075, 1} // the keyboard's background and the dark bands
	cWhite = Color{1, 1, 1, 1}
	cBlack = Color{0, 0, 0, 1}
)

// dc is one drawing: the canvas, the scale and the helpers of Python's draw() closures.
type dc struct {
	u   *UI
	c   *Canvas
	s   float64
	now time.Time
	pk  *Hit // what is under the finger
}

type align int

const (
	left align = iota
	center
	right
)

type textOpt struct {
	col   Color
	bold  bool
	align align
	width float64 // 0 = no limit
	style *FontStyle
}

// fitCut sees every text fit had to shorten (tests: no label is cut in any language).
var fitCut func(s string)

// fit shortens a text to a width, «…» at the end, as Python's fit (by ink width, 2 characters at a time).
func (d *dc) fit(t string, size float64, style FontStyle, width float64) string {
	r := []rune(t)
	for len(r) > 3 {
		if _, ink := d.c.Fonts.Extents(string(r), size, style); ink <= width {
			break
		}
		r = append(r[:len(r)-2:len(r)-2], '…')
	}
	if fitCut != nil && len(r) != len([]rune(t)) {
		fitCut(t)
	}
	return string(r)
}

// text draws t at (x, y) (baseline) and returns its advance.
func (d *dc) text(x, y float64, t string, size float64, o textOpt) float64 {
	style := Sans
	if o.bold {
		style = SansBold
	}
	if o.style != nil {
		style = *o.style
	}
	if o.width > 0 {
		t = d.fit(t, size, style, o.width)
	}
	adv, _ := d.c.Fonts.Extents(t, size, style)
	switch o.align {
	case center:
		x -= adv / 2
	case right:
		x -= adv
	}
	d.c.Text(x, y, t, size, style, o.col)
	return adv
}

func (d *dc) advance(t string, size float64, bold bool) float64 {
	style := Sans
	if bold {
		style = SansBold
	}
	adv, _ := d.c.Fonts.Extents(t, size, style)
	return adv
}

func rrOf(r Rect, radius float64) RR {
	return RR{float64(r.X), float64(r.Y), float64(r.W), float64(r.H), radius}
}

// frame strokes the inside of r (Python's frame).
func (d *dc) frame(x, y, w, h float64, col Color, width, radius float64) {
	if radius == 0 {
		radius = 8 * d.s
	}
	d.c.StrokeRR(RR{x + width/2, y + width/2, w - width, h - width, radius}, width, col)
}

func (d *dc) frameDash(x, y, w, h float64, col Color, width, radius float64) {
	s := d.s
	d.c.StrokeRRDash(RR{x + width/2, y + width/2, w - width, h - width, radius}, width, 6*s, 5*s, col)
}

func (d *dc) frameR(r Rect, col Color, width, radius float64) {
	d.frame(float64(r.X), float64(r.Y), float64(r.W), float64(r.H), col, width, radius)
}

// lit lightens what is under the finger.
func (d *dc) lit(r Rect, radius, alpha float64) {
	d.c.FillRR(rrOf(r, radius), cWhite.WithAlpha(alpha))
}

func (d *dc) pill(x, y float64, t string, size float64, fill Color) {
	s := d.s
	adv := d.advance(t, size, true)
	pad := 7 * s
	d.c.FillRR(RR{x, y - size - pad/2, adv + 2*pad, size + 1.6*pad, 6 * s}, fill)
	d.c.Text(x+pad, y, t, size, SansBold, cInk)
}

func (d *dc) meter(key meterKey, x, y, w, h float64, strong bool) {
	r := &d.u.r
	if r.overlay {
		st, ok := r.meterState[key]
		drawMeter(d.c, x, y, w, h, st.Shown, st.Peak, ok, strong)
		return
	}
	r.meterSpecs = append(r.meterSpecs, meterSpec{key, x, y, w, h, strong})
}

type btn struct {
	label    string
	active   bool
	col      *Color
	sub      string
	hasSub   bool
	progress float64
	hasProg  bool
	dim      bool
	icon     string
	tint     *Color
	subCol   *Color // the sub-text's colour (else quiet)
}

func (d *dc) button(key string, b btn) {
	r := d.u.Lay.Buttons[key]
	x, y, w, h := float64(r.X), float64(r.Y), float64(r.W), float64(r.H)
	s := d.s
	col := cBrand
	if b.col != nil {
		col = *b.col
	}
	q := RR{x, y, w, h, 8 * s}
	if b.active {
		d.c.FillRR(q, col.WithAlpha(0.24))
		d.c.StrokeRR(q, 2, col)
	} else {
		d.c.FillRR(q, cPanel)
		d.c.StrokeRR(q, 1.25, cLine)
	}
	if d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
		d.lit(r, 8*s, 0.16)
	}
	if b.hasProg { // held: fills from left to right
		d.c.FillRectInRR(x, y, w*min(1, b.progress), h, q, cWhite.WithAlpha(0.22))
	}
	if b.icon != "" { // the header's: an icon instead of the label
		ir := min(w, h) * 0.3
		tint := cInk
		switch {
		case b.tint != nil:
			tint = *b.tint
		case b.dim:
			tint = cQuiet
		case b.active:
			tint = col
		}
		d.u.icon(d.c, b.icon, x+w/2, y+h/2, ir, max(2.0, ir*0.17), tint)
		return
	}
	size := 21 * s
	cy := y + h/2 + size*0.36
	if b.hasSub {
		cy = y + h/2 - 2*s
	}
	tc := cInk
	if b.dim {
		tc = cQuiet
	}
	d.text(x+w/2, cy, b.label, size, textOpt{col: tc, bold: b.active, align: center, width: w - 16*s})
	if b.hasSub && b.sub != "" {
		sc := cQuiet
		if b.subCol != nil {
			sc = *b.subCol
		}
		d.text(x+w/2, cy+19*s, b.sub, 14*s, textOpt{col: sc, align: center, width: w - 16*s})
	}
}

func colp(c Color) *Color { return &c }

// draw is Screen.draw: the interface over the multiview, like the plan's sketch.
func (u *UI) draw(c *Canvas, now time.Time) {
	lay, tx := u.Lay, u.Tx
	r := &u.r
	r.meterSpecs = r.meterSpecs[:0]
	net := u.Net.Snapshot()
	app := u.app().AppSnapshot()
	st := u.st
	locked := st.Screen.Locked
	r.overlay = locked || u.menuOpen || u.settingsOpen || u.powering != ""
	c.Clear()
	d := &dc{u: u, c: c, s: lay.Scale, now: now}
	if u.press != nil {
		h := u.press.hit
		d.pk = &h
	}
	s := d.s
	out := st.Output
	live := out.Mode == "on" && out.State == "on"
	trying := out.Mode == "on" && !live

	if (u.settingsOpen || u.wizardOn()) && u.settingsView == "keyboard" && u.kb != nil {
		// The keyboard covers the whole screen: nothing below is drawn
		u.drawKeyboard(d)
		return
	}
	if u.wizardOn() { // the wizard covers the whole screen too (its own top bar)
		var legacy *WifiCreds
		if net.API == No && u.Legacy != nil {
			legacy = u.Legacy()
		}
		var cams []SourceStatus
		for _, x := range st.Sources {
			if x.Kind == "network" {
				cams = append(cams, x)
			}
		}
		u.drawWizard(d, &net, &app, cams, legacy)
		if u.powering != "" {
			c.Paint(cDeep.WithAlpha(0.96))
		}
		return
	}

	// --- header: logo, broadcast state, network, board and the icon buttons ---
	sh := float64(lay.Status.H)
	sw := float64(lay.Status.W)
	m := float64(lay.M)
	c.FillRect(0, 0, sw, sh, cBg)
	c.FillRect(0, sh-1, sw, 1, cLine)
	mid := sh / 2 // the centre of the buttons' row (from m to m + hb)
	base := mid + 6*s
	x := m
	if logo := u.logoImage(int(float64(lay.HB) * 0.6)); logo != nil {
		c.DrawImage(logo, int(x), int(mid-float64(logo.Rect.Dy())/2))
		x += float64(logo.Rect.Dx())
	} else {
		x += d.text(x, base, tx.LogoA, 20*s, textOpt{col: cInk, bold: true})
		x += d.text(x, base, tx.LogoB, 20*s, textOpt{col: cBrand, bold: true})
	}
	x = float64(int(x+1.5*m)) + 0.5 // a thin line separates the logo from the state
	c.FillRect(x-0.5, mid-float64(lay.HB)*0.28, 1, float64(lay.HB)*0.56, cLine)
	x += 1.5 * m
	dot := cQuiet
	if live {
		dot = cBad
	} else if trying {
		dot = cWarn
	}
	c.FillCircle(x+6*s, mid, 6*s, dot)
	why := tx.Reasons[out.Reason]
	hsize := 19.0
	var head string
	switch {
	case live:
		el := 0
		if !u.liveSince.IsZero() {
			el = int(now.Sub(u.liveSince).Seconds())
		}
		head = sprintf(tx.LiveClockF, el/3600, el/60%60, el%60)
	case trying:
		// «connecting» with a reason = a passing failure the output retries by itself
		switch {
		case out.State == "error":
			w := why
			if w == "" {
				w = tx.RetryingReason
			}
			head = sprintf(tx.NoConnectionF, w)
		case why != "":
			head, hsize = sprintf(tx.ConnectingReasonF, why), 17 // at 19, «el relay no responde» does not fit
		default:
			head = tx.Connecting
		}
	default:
		head = tx.NotEmitting
	}
	x += 20 * s
	leftEdge := x + d.text(x, base, head, hsize*s, textOpt{col: cInk, bold: true, width: sw * 0.3})
	// On the right, next to the buttons, the board's temperature and voltage
	var hw []string
	low := false
	if u.Sensors != nil {
		temp, okT, volts, okV := u.Sensors()
		if okT {
			hw = append(hw, sprintf(tx.TempF, temp))
		}
		if okV {
			hw = append(hw, strings.ReplaceAll(sprintf(tx.VoltsF, volts), ".", tx.Decimal))
			low = volts < 4.8
		}
	}
	rightEdge := float64(lay.Buttons["lock"].X) - m
	if len(hw) > 0 {
		col := cQuiet
		if low {
			col = cWarn
		}
		rightEdge -= d.text(rightEdge, base, strings.Join(hw, tx.Sep), 16*s, textOpt{col: col, align: right})
	}
	// At its left, the CLIENT wifi (WIFI.md §6; not a button)
	if level, tone, ok := Antenna(&net); ok {
		ar, alw := 13*s, max(2.0, 2.4*s)
		ax := rightEdge - m - ar
		ay := mid + 2*s
		base := cLine
		if level == 0 {
			base = cQuiet
			if tone == "warn" {
				base = cWarn
			}
		}
		u.icon(c, "wifi", ax, ay, ar, alw, base)
		if level > 0 {
			arcs := cInk
			if tone == "warn" {
				arcs = cWarn
			}
			u.icon(c, sprintf("wifi%d", level), ax, ay, ar, alw, arcs)
		}
		rightEdge = ax - ar - m
	}
	// In the middle, the network: what really goes out (2 s average), the link and the RTT. If it does not fit,
	// whole parts go (first the RTT, then the link), never half a word
	var mbps, rtt string
	if live && out.SentKbps != nil && *out.SentKbps != 0 {
		mbps = strings.ReplaceAll(sprintf(tx.MbpsF, float64(*out.SentKbps)/1000), ".", tx.Decimal)
	}
	if live && out.RTTMs != nil {
		rtt = sprintf(tx.RTTF, *out.RTTMs)
	}
	link := ""
	if u.LinkInfo != nil {
		link = u.linkText(u.LinkInfo())
	}
	x0, x1 := leftEdge+2*m, rightEdge-2*m
	last := []string{link}
	if mbps != "" {
		last = []string{mbps}
	}
	var netText string
	for _, parts := range [][]string{{mbps, link, rtt}, {mbps, link}, last} {
		netText = joinNonEmpty(parts, tx.Sep)
		if d.advance(netText, 16*s, false) <= x1-x0 {
			break
		}
	}
	if x1-x0 > 40*s {
		d.text((x0+x1)/2, base, netText, 16*s, textOpt{col: cQuiet, align: center, width: x1 - x0})
	}
	var hd *hold
	held := 0.0
	if u.hold != nil && !u.hold.done {
		hd = u.hold
		held = float64(now.Sub(hd.since)) / float64(HoldTime(hd.key))
	}
	holding := func(key string) (float64, bool) {
		if hd != nil && hd.key == key {
			return held, true
		}
		return 0, false
	}
	// Lock: open padlock; locked, closed and amber (held to unlock)
	lb := btn{active: locked, col: colp(cWarn), icon: "unlock"}
	if locked {
		lb.icon = "lock"
	}
	lb.progress, lb.hasProg = holding("unlock")
	d.button("lock", lb)
	d.button("power", btn{active: u.menuOpen, col: colp(cBad), dim: locked, icon: "power"})
	var cams []SourceStatus
	for _, x := range st.Sources {
		if x.Kind == "network" {
			cams = append(cams, x)
		}
	}
	d.button("settings", btn{active: u.settingsOpen, dim: locked, icon: "gear"})
	var legacy *WifiCreds
	if net.API == No && u.Legacy != nil {
		legacy = u.Legacy()
	}
	if g := lay.Buttons["settings"]; WifiDown(&net, legacy) || setupPending(&app) {
		c.FillCircle(float64(g.X+g.W)-10*s, float64(g.Y)+10*s, 5*s, cWarn)
	} else if updateAvailable(&app) {
		c.FillCircle(float64(g.X+g.W)-10*s, float64(g.Y)+10*s, 5*s, cBrand) // an update to install
	}

	// Under Settings nothing of the views is seen (its panel covers them): not drawn
	switch {
	case u.settingsOpen && !locked && u.powering == "":
	case u.view == "audio":
		u.drawAudioView(d)
	default:
		u.drawVideoView(d, live, trying, holding)
	}

	// --- locked: everything off but the program and the padlock ---
	if locked {
		shade := cBlack.WithAlpha(0.62)
		if u.view == "audio" {
			c.FillRect(0, sh, float64(lay.DW), float64(lay.DH)-sh, shade)
		} else {
			x0 := float64(lay.PGM.X + lay.PGM.W + lay.M/2)
			c.FillRect(x0, sh, float64(lay.DW)-x0, float64(lay.PGM.Y+lay.PGM.H)-sh+float64(lay.M/2), shade)
			ty := float64(lay.Tiles[0].Y - lay.M/2)
			c.FillRect(0, ty, float64(lay.DW), float64(lay.DH)-ty, shade)
		}
		ty := float64(lay.Tiles[0].Y) + float64(lay.Tiles[0].H)/2
		d.text(float64(lay.DW)/2, ty, tx.Locked, 26*s, textOpt{col: cWarn, bold: true, align: center})
		d.text(float64(lay.DW)/2, ty+28*s, tx.LockedHelp, 16*s, textOpt{col: cQuiet, align: center})
	}

	if u.menuOpen {
		c.Paint(cBlack.WithAlpha(0.7))
		mr := lay.Menu
		q := rrOf(mr, 12*s)
		c.FillRR(q, cBg.WithAlpha(0.98))
		c.StrokeRR(q, 1.5, cLine)
		cx := float64(mr.X) + float64(mr.W)/2
		d.text(cx, float64(mr.Y)+32*s, tx.MenuTitle, 20*s, textOpt{col: cInk, bold: true, align: center})
		d.text(cx, float64(mr.Y)+54*s, tx.MenuHelp, 14*s, textOpt{col: cQuiet, align: center})
		rb := btn{label: tx.Reboot, active: true, col: colp(cWarn)}
		rb.progress, rb.hasProg = holding("reboot")
		d.button("m_reboot", rb)
		pb := btn{label: tx.PowerOff, active: true, col: colp(cBad)}
		pb.progress, pb.hasProg = holding("poweroff")
		d.button("m_poweroff", pb)
		d.button("m_cancel", btn{label: tx.Cancel})
	}
	if u.settingsOpen {
		u.drawSettings(d, &net, &app, cams, legacy)
	}
	if u.powering != "" {
		c.Paint(cDeep.WithAlpha(0.96))
		t := tx.PoweringOff
		if u.powering == "reboot" {
			t = tx.Rebooting
		}
		d.text(float64(lay.DW)/2, float64(lay.DH)/2, t, 34*s, textOpt{col: cInk, bold: true, align: center})
	}
}

func (u *UI) drawVideoView(d *dc, live, trying bool, holding func(string) (float64, bool)) {
	lay, tx, st, c, s := u.Lay, u.Tx, u.st, d.c, d.s
	prog := st.Program
	mode := prog.Layout
	if mode == "" {
		mode = "full"
	}
	pauseOn := st.pauseOn()
	names := map[string]string{}
	for i := range st.Sources {
		names[st.Sources[i].ID] = u.srcLabel(&st.Sources[i])
	}
	// --- program ---
	pgmColor, pgmWidth := cLine, 2.0
	switch {
	case pauseOn:
		pgmColor, pgmWidth = cWarn, 4
	case live:
		pgmColor, pgmWidth = cBad, 4
	}
	d.frameR(lay.PGM, pgmColor, pgmWidth, 0)
	if d.pk != nil && d.pk.Kind == HitProgram {
		d.lit(lay.PGM, 8*s, 0.12)
	}
	tag, tagFill := tx.ProgramTag, Color{0.2, 0.25, 0.33, 0.9}
	if pauseOn {
		tag, tagFill = tx.PausedTag, cWarn.WithAlpha(0.9)
	} else if live {
		tagFill = cBad.WithAlpha(0.9)
	}
	px, py, pw, ph := float64(lay.PGM.X), float64(lay.PGM.Y), float64(lay.PGM.W), float64(lay.PGM.H)
	d.pill(px+14*s, py+34*s, tag, 16*s, tagFill)
	onName := names[st.mainID()]
	if pauseOn {
		onName = prog.Pause.Text
	}
	if onName != "" {
		d.text(px+14*s, py+ph-16*s, onName, 17*s, textOpt{col: cInk, bold: true, width: pw * 0.6})
	}
	corner, size := "br", 33
	if prog.PiP != nil {
		corner, size = prog.PiP.Corner, prog.PiP.Size
	}
	if mode == "pip" && st.secondaryID() != "" && !pauseOn {
		// Where the PiP is, like the sketch (the same computation as the program)
		W, H := prog.Width, prog.Height
		if W == 0 {
			W = 1920
		}
		if H == 0 {
			H = 1080
		}
		r := scene.PipRect(W, H, corner, size)
		k := pw / float64(W)
		d.frame(px+float64(r.X)*k, py+float64(r.Y)*k, float64(r.W)*k, float64(r.H)*k, cGood, 2.5, 4*s)
	}

	// --- composition, transition and emit ---
	d.text(float64(lay.Labels["layout"][0]), float64(lay.Labels["layout"][1]), tx.LayoutLabel, 16*s, textOpt{col: cQuiet})
	d.text(float64(lay.Labels["transition"][0]), float64(lay.Labels["transition"][1]), tx.TransitionLabel, 16*s,
		textOpt{col: cQuiet})
	d.button("full", btn{label: tx.Full, active: mode == "full"})
	arrow := ""
	if prog.PiP != nil {
		arrow = CornerArrow[prog.PiP.Corner]
	}
	pb := btn{label: sprintf(tx.PiPF, arrow), active: mode == "pip"}
	if mode == "pip" {
		frac := tx.PiPThird
		if prog.PiP != nil && prog.PiP.Size == 25 {
			frac = tx.PiPQuarter
		}
		pb.sub, pb.hasSub = sprintf(tx.PiPSubF, frac), true
	}
	d.button("pip", pb)
	d.button("side", btn{label: tx.Side, active: mode == "side"})
	d.button("pause", btn{label: tx.Pause, active: pauseOn, col: colp(cWarn), dim: !u.PauseSupported})
	d.button("cut", btn{label: tx.Cut, active: prog.Transition != "fade"})
	d.button("fade", btn{label: tx.Fade, active: prog.Transition == "fade"})
	eb := btn{active: true}
	eb.progress, eb.hasProg = holding("emit")
	switch {
	case live:
		eb.label, eb.col, eb.sub, eb.hasSub = tx.EmitLive, colp(cBad), tx.EmitHoldStop, true
	case trying:
		eb.label, eb.col, eb.sub, eb.hasSub = tx.EmitConnecting, colp(cWarn), tx.EmitHoldStop, true
	default:
		eb.label, eb.col, eb.sub, eb.hasSub = tx.Emit, colp(cBrand), tx.EmitHold, true
	}
	d.button("emit", eb)
	au := st.Audio
	if au != nil {
		// The audio view: the mixer (what sounds, volumes, mode)
		modeTxt := tx.AudioMix
		if au.Mode != "mix" {
			modeTxt = tx.AudioSingle
			if au.Follow {
				modeTxt += tx.AudioFollowSuffix
			}
		}
		if pauseOn && prog.Pause.Mute {
			modeTxt += tx.AudioSilencedSuffix
		}
		d.button("audio", btn{label: tx.AudioMixerButton, sub: modeTxt, hasSub: true})
	}
	if u.hint != "" && d.now.Before(u.hintUntil) {
		d.text(float64(lay.Info.X), float64(lay.Info.Y)-6*s, u.hint, 14*s, textOpt{col: cWarn, width: float64(lay.Info.W)})
	}
	// The program's meter (L/R), vertical between the preview and the buttons
	if u.Levels != nil {
		if _, ok := u.Levels.ProgramPeak(0); ok {
			mr := lay.PGMMeter
			bw0 := float64(mr.W-4) / 2
			d.meter(meterKey{pgm: true, ch: 0}, float64(mr.X), float64(mr.Y), bw0, float64(mr.H), true)
			d.meter(meterKey{pgm: true, ch: 1}, float64(mr.X)+bw0+4, float64(mr.Y), bw0, float64(mr.H), true)
		}
	}

	// --- tiles ---
	bySlot := map[int]*SourceStatus{}
	for i := range st.Sources {
		bySlot[st.Sources[i].Slot-1] = &st.Sources[i]
	}
	for i, r := range lay.Tiles {
		rx, ry, rw, rh := float64(r.X), float64(r.Y), float64(r.W), float64(r.H)
		src := bySlot[i]
		if src == nil {
			d.frameDash(rx, ry, rw, rh, cLine, 1.5, 6*s)
			d.text(rx+rw/2, ry+rh/2+6*s, sprintf(tx.TileFreeF, i+1), 16*s, textOpt{col: cQuiet, align: center})
			continue
		}
		on := src.ID == st.mainID()
		sec := src.ID == st.secondaryID() && mode != "full" && !on
		band := 50 * s
		c.FillRectInRR(rx, ry+rh-band, rw, band, rrOf(r, 6*s), cDeep.WithAlpha(0.72))
		switch {
		case on:
			d.frameR(r, cBad, 4, 6*s)
		case sec:
			d.frameR(r, cGood, 3, 6*s)
		default:
			d.frameR(r, cLine, 1.5, 6*s)
		}
		if d.pk != nil && d.pk.Kind == HitTile && d.pk.Slot == i {
			d.lit(r, 6*s, 0.14)
		}
		// The VU meter of its own device's audio (if it has one and it is live); strong if it is the one sounding
		if au != nil && u.Levels != nil {
			for _, a := range au.Sources {
				if a.VideoSlot == i+1 {
					if _, ok := u.Levels.InputPeak(a.ID); ok {
						tm := lay.TileMeters[i]
						d.meter(meterKey{id: a.ID}, float64(tm.X), float64(tm.Y), float64(tm.W), float64(tm.H), a.ID == str(au.Main))
					}
					break
				}
			}
		}
		var stTxt string
		if src.State != "live" {
			stTxt = u.notLiveText(src.State, src.DetailCode, src.DetailArgs, src.Detail, false)
		} else {
			switch {
			case on:
				stTxt = tx.OnAir
			case sec && mode == "pip":
				stTxt = tx.InPiP
			case sec:
				stTxt = tx.InProgram
			default:
				stTxt = tx.Ready
			}
			if src.Mode != "" {
				stTxt += tx.Sep + ModeLabel(src.Mode, src.FPS, tx)
			}
		}
		cx := rx + rw/2
		d.text(cx, ry+rh-band+21*s, sprintf(tx.TileNameF, i+1, u.srcLabel(src)), 16*s, textOpt{col: cInk, bold: true, align: center,
			width: rw - 14*s})
		stc := cQuiet
		if on {
			stc = cBad
		} else if sec {
			stc = cGood
		}
		d.text(cx, ry+rh-10*s, stTxt, 14*s, textOpt{col: stc, align: center, width: rw - 14*s})
	}
}

// drawAudioView is the second view: the audio mixer. It covers the video (the multiview goes on below).
func (u *UI) drawAudioView(d *dc) {
	lay, tx, st, c, s := u.Lay, u.Tx, u.st, d.c, d.s
	sh := float64(lay.Status.H)
	c.FillRect(0, sh, float64(lay.DW), float64(lay.DH)-sh, cBg.WithAlpha(1))
	au := st.Audio
	if au == nil {
		au = &AudioStatus{}
	}
	mix := au.Mode == "mix"
	d.button("a_back", btn{icon: "back"})
	d.button("a_single", btn{label: tx.OneSource, active: !mix})
	d.button("a_mix", btn{label: tx.Mix, active: mix})
	if !mix { // in the mix it does not apply: neither seen nor touched (OnTouch)
		d.button("a_follow", btn{label: tx.FollowVideo, active: au.Follow, col: colp(cGood)})
	}
	srcs := au.Sources
	if len(srcs) > 4 {
		srcs = srcs[:4]
	}
	for i, stp := range lay.Strips {
		nx, ny, sw := float64(stp.Name.X), float64(stp.Name.Y), float64(stp.Name.W)
		ax, ay, aw, ah := nx, ny-4, sw, float64(lay.DH-lay.M)-ny+4
		if i == len(lay.Strips)-1 {
			// The program: what goes on air, L/R
			c.FillRR(RR{ax, ay, aw, ah, 10 * s}, cPanel)
			d.frame(ax, ay, aw, ah, cLine, 1.25, 10*s)
			d.text(nx+sw/2, ny+18*s, tx.ProgramStrip, 16*s, textOpt{col: cInk, bold: true, align: center})
			sub := tx.ProgramSingle
			if mix {
				sub = tx.ProgramMix
			}
			d.text(nx+sw/2, ny+36*s, sub, 13*s, textOpt{col: cQuiet, align: center})
			my0, mh0 := float64(stp.Meter.Y), float64(stp.Meter.H)
			fw := sw - 32
			d.meter(meterKey{pgm: true, ch: 0}, nx+16, my0, fw/2-4, mh0, true)
			d.meter(meterKey{pgm: true, ch: 1}, nx+16+fw/2+4, my0, fw/2-4, mh0, true)
			if ph := au.Phones; ph != nil {
				// The screen's headphones: what is heard through its jack is this same audio
				vol, warnText := PhonesText(ph, tx)
				col := cGood
				if warnText != "" {
					col = cWarn
				}
				d.button("a_phones", btn{label: tx.Headphones, active: ph.On, col: colp(col), sub: warnText,
					hasSub: warnText != ""})
				vc := cQuiet
				if ph.On {
					vc = cInk
				}
				d.text(float64(stp.GainX)+sw/2, float64(stp.GainY), vol, 15*s, textOpt{col: vc, bold: ph.On, align: center})
				d.button("a_ph_minus", btn{label: tx.Minus})
				d.button("a_ph_plus", btn{label: tx.Plus})
			}
			continue
		}
		if i >= len(srcs) {
			d.frameDash(ax, ay, aw, ah, cLine, 1.5, 10*s)
			d.text(nx+sw/2, ay+ah/2, tx.StripFree, 15*s, textOpt{col: cQuiet, align: center})
			continue
		}
		a := srcs[i]
		sounding := a.ID == str(au.Main)
		if mix {
			sounding = a.InMix
		}
		c.FillRR(RR{ax, ay, aw, ah, 10 * s}, cPanel)
		if sounding {
			d.frame(ax, ay, aw, ah, cGood, 2.5, 10*s)
		} else {
			d.frame(ax, ay, aw, ah, cLine, 1.25, 10*s)
		}
		name := u.sourceName(a.ID, a.Name)
		if a.VideoSlot != 0 {
			for j := range st.Sources {
				if x := &st.Sources[j]; x.Slot == a.VideoSlot && x.Alias != "" {
					name = u.srcLabel(x) // the slot's alias names its audio too
				}
			}
			name = sprintf(tx.TileNameF, a.VideoSlot, name)
		}
		d.text(nx+sw/2, ny+18*s, name, 15*s, textOpt{col: cInk, bold: true, align: center, width: sw - 14*s})
		sub, ok := tx.AudioState[a.State]
		if !ok {
			sub = a.State
		}
		if a.State != "live" && a.DetailCode != "" { // why not, in the screen's language
			if t := u.detailText(a.DetailCode, a.DetailArgs, a.Detail); t != "" {
				sub = t
			}
		}
		if a.DelayMs != 0 {
			sub += sprintf(tx.DelaySuffixF, a.DelayMs)
		}
		d.text(nx+sw/2, ny+36*s, sub, 13*s, textOpt{col: cQuiet, align: center, width: sw - 14*s})
		key := sprintf("a_on_%d", i)
		if mix {
			label := tx.OutOfMix
			if sounding {
				label = tx.InTheMix
			}
			d.button(key, btn{label: label, active: sounding, col: colp(cGood)})
		} else {
			label := tx.PutOnAir
			if sounding {
				label = tx.Sounding
			}
			d.button(key, btn{label: label, active: sounding, col: colp(cGood)})
		}
		mr := stp.Meter
		d.meter(meterKey{id: a.ID}, float64(mr.X), float64(mr.Y), float64(mr.W), float64(mr.H), sounding)
		fr := stp.Fader
		fx, fy, fw, fh := float64(fr.X), float64(fr.Y), float64(fr.W), float64(fr.H)
		c.FillRR(RR{fx + fw/2 - 3*s, fy, 6 * s, fh, 3 * s}, Color{0.031, 0.043, 0.075, 0.9})
		z := FaderY(fr, 0) // the 0 dB mark
		c.FillRect(fx+4, z-0.5, fw-8, 1, cQuiet)
		d.text(fx+fw-2, z-3*s, tx.ZeroDb, 11*s, textOpt{col: cQuiet, align: right})
		g := 0.0
		if a.GainDb != nil {
			g = *a.GainDb
		}
		ky := FaderY(fr, g)
		c.FillRR(RR{fx + 2, ky - 9*s, fw - 4, 18 * s, 5 * s}, cBrand.WithAlpha(0.95))
		gt := strings.ReplaceAll(sprintf(tx.GainF, g), ".", tx.Decimal)
		gt = strings.Replace(gt, "+0"+tx.Decimal+"0", "0"+tx.Decimal+"0", 1)
		d.text(float64(stp.GainX)+sw/2, float64(stp.GainY), gt, 15*s, textOpt{col: cInk, bold: true, align: center})
		d.button(sprintf("a_minus_%d", i), btn{label: tx.Minus})
		d.button(sprintf("a_plus_%d", i), btn{label: tx.Plus})
	}
}

// drawKeyboard is the password keyboard, full screen (WIFI.md §6). Its base (background and the layer's
// keys) is drawn once per layer and pasted; on each drawing only the pressed key, «Conectar» (it depends on
// what is typed), the title and the field: ONLY dots (never the last character) and the counter.
func (u *UI) drawKeyboard(d *dc) {
	lay, tx, kb, c, s := u.Lay, u.Tx, u.kb, d.c, d.s
	m := float64(lay.M)
	layer := kb.layer
	r := &u.r
	if r.kbBase == nil || r.kbLayer != layer {
		if r.kbBase == nil {
			r.kbBase = newRGBA(lay.DW, lay.DH)
		} else {
			clear(r.kbBase.Pix)
		}
		k := NewCanvas(r.kbBase, u.Fonts)
		k.Paint(cDeep)
		for _, key := range lay.KB[layer] {
			if key.Name == "OK" {
				continue
			}
			active := key.Name == "SHIFT" && layer == "upper"
			q := rrOf(key.R, 8*s)
			if active {
				k.FillRR(q, cBrand.WithAlpha(0.24))
				k.StrokeRR(q, 2, cBrand)
			} else {
				k.FillRR(q, cPanel)
				k.StrokeRR(q, 1.25, cLine)
			}
			cx, cy := float64(key.R.X)+float64(key.R.W)/2, float64(key.R.Y)+float64(key.R.H)/2
			if key.Name == "SHIFT" || key.Name == "BKSP" {
				kind := "backspace"
				if key.Name == "SHIFT" {
					kind = "shift"
				}
				col := cInk
				if active {
					col = cBrand
				}
				u.icon(k, kind, cx, cy, 15*s, max(2.0, 2.4*s), col)
				continue
			}
			label, ok := tx.KeyLabel[key.Name]
			if !ok {
				label = key.Name
			}
			size, col := 20*s, cQuiet
			if len([]rune(label)) == 1 {
				size, col = 30*s, cInk
			}
			adv, _ := u.Fonts.Extents(label, size, Sans)
			k.Text(cx-adv/2, cy+size*0.36, label, size, Sans, col)
		}
		r.kbLayer = layer
	}
	c.CopyFrom(r.kbBase, r.kbBase.Rect)
	ok, okLabel := u.kbReady()
	okr, _ := lay.KeyRect(layer, "OK")
	q := rrOf(okr, 8*s)
	if ok {
		c.FillRR(q, cBrand)
		c.StrokeRR(q, 1.25, cBrand)
	} else {
		c.FillRR(q, cPanel)
		c.StrokeRR(q, 1.25, cLine)
	}
	oc := cQuiet
	if ok {
		oc = cInk
	}
	d.text(float64(okr.X)+float64(okr.W)/2, float64(okr.Y)+float64(okr.H)/2+8*s, okLabel, 22*s,
		textOpt{col: oc, bold: true, align: center, width: float64(okr.W) - 16*s})
	if d.pk != nil && d.pk.Kind == HitKey {
		if hr, found := lay.KeyRect(layer, d.pk.Key); found {
			d.lit(hr, 8*s, 0.22)
		}
	}
	d.button("k_cancel", btn{icon: "close"})
	d.text(m, m+float64(lay.HB)/2+8*s, u.kbTitle(), 22*s,
		textOpt{col: cInk, bold: true, width: float64(lay.Buttons["k_cancel"].X) - 2*m})
	f := lay.KBField
	fx, fy, fw, fh := float64(f.X), float64(f.Y), float64(f.W), float64(f.H)
	fq := RR{fx, fy, fw, fh, 10 * s}
	c.FillRR(fq, cPanel)
	c.StrokeRR(fq, 2, cBrand)
	pad, cw := 18*s, 96*s
	cy := fy + fh/2
	var count string
	var cc Color
	n := kb.n
	if kb.secret() {
		// ONLY dots, never a character of it
		sp := min(26*s, (fw-2*pad-cw)/float64(max(n, 1)))
		rad := min(7*s, sp*0.36)
		for i := range n {
			c.FillCircle(fx+pad+sp*float64(i)+rad, cy, rad, cInk)
		}
		cx := fx + pad + sp*float64(n)
		if n > 0 {
			cx += rad
		}
		c.FillRect(cx-1, cy-15*s, 2, 30*s, cBrand) // where the next one goes
		if n == 0 {
			d.text(cx+10*s, cy+7*s, tx.TypePassword, 19*s, textOpt{col: cQuiet})
		}
		count, cc = u.kbCount(), cWarn
		if ok || n == 0 {
			cc = cQuiet
		}
	} else {
		w := 0.0
		if len(kb.text) > 0 {
			w = d.text(fx+pad, cy+9*s, string(kb.text), 26*s, textOpt{col: cInk, width: fw - 2*pad - cw})
		}
		c.FillRect(fx+pad+w+2*s, cy-15*s, 2, 30*s, cBrand)
		count, cc = sprintf(tx.AliasCountF, len(kb.text)), cQuiet
		if kb.mode == "name" || kb.mode == "ssid" {
			count = sprintf("%d/%d", len(kb.text), kb.max)
		}
	}
	d.text(fx+fw-pad, cy+7*s, count, 19*s, textOpt{col: cc, align: right})
	note, tone := u.kbHelp(), cQuiet
	if kb.err != "" {
		note, tone = kb.err, cWarn
	}
	d.text(m+4*s, float64(lay.KBNote), note, 16*s, textOpt{col: tone, bold: kb.err != "", width: float64(lay.DW) - 2*m})
}

// kbReady: «OK» can be pressed, and its label.
func (u *UI) kbReady() (bool, string) {
	kb, tx := u.kb, u.Tx
	switch kb.mode {
	case "wifi":
		return PSKOK(kb.chars()), tx.ConnectButton
	case "alias":
		return true, tx.KeyDone
	case "name":
		return len(kb.text) > 0, tx.KeyDone
	case "ssid":
		return strings.TrimSpace(string(kb.text)) != "", tx.KeyDone
	}
	return kb.n >= WebPasswordMin, tx.KeyDone
}

// kbTitle is the keyboard's title.
func (u *UI) kbTitle() string {
	kb, tx := u.kb, u.Tx
	switch kb.mode {
	case "wifi":
		return sprintf(tx.PasswordForF, kb.ssid)
	case "alias":
		return sprintf(tx.AliasForF, kb.slot)
	case "ssid":
		return tx.SSIDKeyboard
	}
	return u.wizardKeyboardTitle()
}

// kbHelp is the keyboard's help line.
func (u *UI) kbHelp() string {
	kb, tx := u.kb, u.Tx
	switch kb.mode {
	case "wifi":
		return tx.KeyboardHelp
	case "alias":
		return tx.AliasHelp
	case "ssid":
		return tx.SSIDHelp
	}
	return u.wizardKeyboardHelp()
}

// kbCount is a secret's counter.
func (u *UI) kbCount() string {
	kb, tx := u.kb, u.Tx
	if kb.mode != "wifi" {
		return sprintf("%d", kb.n)
	}
	if kb.n > 63 {
		return tx.Count64
	}
	return sprintf(tx.CountF, kb.n)
}

func newRGBA(w, h int) *image.RGBA { return image.NewRGBA(image.Rect(0, 0, w, h)) }

// linkText is the header's link in the screen's language: «SRT», «SRTLA · 1 link», «SRTLA · 2 links».
func (u *UI) linkText(transport string, links int) string {
	tx := u.Tx
	switch {
	case transport != "srtla":
		return tx.LinkSRT
	case links == 1:
		return tx.LinkSRTLAOne
	}
	return sprintf(tx.LinkSRTLAF, links)
}
