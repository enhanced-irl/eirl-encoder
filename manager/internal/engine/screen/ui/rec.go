package ui

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The local recording on the main screen (RECORDING.md §1, §5.2): a line in the header while it records («● REC
// 01:12:34 · 3 h 20 min libres») or when it stopped on its own (why, for RecNoticeFor), the REC button at Go live's
// right (held like Go live: the engine's record command, by=screen), and what Go live says with REC_MODE=with_output.
// Everything comes from the engine's state (record); the Recording page (recpage.go) is the app's.

// RecNoticeFor is how long the header says why a recording stopped on its own (the Recording page keeps saying it).
const RecNoticeFor = 5 * time.Minute

// PlugWarnFor is how long the warning of a disk plugged in while live stays over the program.
const PlugWarnFor = 15 * time.Second

// originRec is the REC button's command: a refusal is said in words.
const originRec = "rec"

// recOn: recording or asked to (the web's recOn): REC stops, the quality and the disk wait.
func recOn(r *RecordStatus) bool {
	return r != nil && (r.Mode == "on" || r.State == "starting" || r.State == "on")
}

// RecButton is what the REC button is now.
type RecButton struct {
	Shown bool   // the recording is not off (or still records: it can be stopped)
	On    bool   // recording or asked to: holding it stops
	Ready bool   // holding it acts
	Why   string // why it cannot start: none | ejected | checking | full | bad ("" it can)
}

// RecButtonState is REC's state from the engine's record object (nil: an engine without a recorder, no REC).
func RecButtonState(r *RecordStatus) RecButton {
	if r == nil {
		return RecButton{}
	}
	on := recOn(r)
	if !on && (r.Setting == "off" || r.Setting == "") {
		return RecButton{}
	}
	b := RecButton{Shown: true, On: on, Ready: on}
	if !on {
		b.Why = recDiskWhy(&r.Disk)
		b.Ready = b.Why == ""
	}
	return b
}

// recDiskWhy is why a recording cannot start on this disk ("" it can): none | ejected | checking | full | bad.
func recDiskWhy(d *RecordDisk) string {
	switch d.State {
	case "ready":
		if d.MinutesLeft != nil && *d.MinutesLeft < 1 {
			return "full"
		}
		return ""
	case "none", "":
		return "none"
	case "ejected", "checking":
		return d.State
	}
	return "bad"
}

// EmitRecText is what Go live says about the recording with REC_MODE=with_output while it is not live: «Go live +
// record» (rec) with a disk ready, else why it will not record (noRec: none | full | bad), or nothing to add.
func EmitRecText(r *RecordStatus) (rec bool, noRec string) {
	if r == nil || r.Setting != "with_output" || recOn(r) {
		return false, ""
	}
	switch why := recDiskWhy(&r.Disk); why {
	case "":
		return true, ""
	case "checking":
		return false, "" // in a moment it is ready, or it says why
	case "ejected":
		return false, "none"
	default:
		return false, why
	}
}

// recSpan is minutes as the web says them: «45 min», «3 h 20 min», «12 h».
func recSpan(minutes int, tx *RecStrings) string {
	m := max(0, minutes)
	h := m / 60
	switch {
	case h == 0:
		return sprintf(tx.MinF, m)
	case m%60 == 0:
		return sprintf(tx.HF, h)
	}
	return sprintf(tx.HMF, h, m%60)
}

// diskSize is bytes as disks are sold (decimal): «62 GB», «1,2 TB», «740 MB».
func diskSize(b int64, tx *Strings) string {
	num := func(v float64, dec int) string {
		return strings.ReplaceAll(strconv.FormatFloat(v, 'f', dec, 64), ".", tx.Decimal)
	}
	switch {
	case b <= 0:
		return ""
	case b >= 1e12:
		return sprintf(tx.Rec.TBF, num(float64(b)/1e12, 1))
	case b >= 1e10:
		return sprintf(tx.Rec.GBF, num(float64(b)/1e9, 0))
	case b >= 1e9:
		return sprintf(tx.Rec.GBF, num(float64(b)/1e9, 1))
	}
	return sprintf(tx.Rec.MBF, num(float64(b)/1e6, 0))
}

// inSentence lowers a name's first letter for the middle of a sentence («en calidad alta»), as the web does.
func inSentence(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || !unicode.IsUpper(r) {
		return s
	}
	if r2, _ := utf8.DecodeRuneInString(s[n:]); !unicode.IsLower(r2) {
		return s // an acronym stays
	}
	return string(unicode.ToLower(r)) + s[n:]
}

// --- following the engine: the clock, the notice, a disk plugged in while live ---

// recView is what the screen keeps of the recording between states: its clock counted on between them (the
// engine publishes elapsedS every few seconds), the notice of a stop, and the warning of a disk plugged in live.
type recView struct {
	seq           int
	base, shown   int
	at            time.Time
	running       bool
	notice        string // the reason last seen on a stopped recording ("" none) and since when
	noticeSince   time.Time
	diskState     string
	plugWarnUntil time.Time
}

// recFollow takes a new state (SetStatus).
func (u *UI) recFollow(st *Status) {
	v := &u.rec
	now := u.now()
	r := st.Record
	if r == nil {
		*v = recView{plugWarnUntil: v.plugWarnUntil}
		return
	}
	// The clock: the engine's figure, never backwards within one recording (a retry in a new file starts again)
	if r.State == "on" && r.ElapsedS != nil {
		s := *r.ElapsedS
		if !v.running || r.Seq != v.seq || s+2 < v.base {
			v.seq, v.shown, v.base = r.Seq, 0, -1
		}
		if s != v.base {
			v.base, v.at = s, now
		}
		v.running = true
	} else {
		v.running = false
	}
	// Why a recording stopped on its own: said for RecNoticeFor from when it was first seen
	key := ""
	if r.State != "on" && r.State != "starting" && r.Reason != "" {
		key = r.Reason + "/" + strconv.Itoa(r.Seq) + "/" + r.Mode
	}
	if key != v.notice {
		v.notice, v.noticeSince = key, now
	}
	// A disk plugged in while live: a hard disk without its own supply can cut the board's power (RECORDING.md §3)
	had := v.diskState != "" && v.diskState != "none" && v.diskState != "ejected"
	has := r.Disk.State != "" && r.Disk.State != "none" && r.Disk.State != "ejected"
	if v.diskState != "" && !had && has && st.Output.Mode == "on" {
		v.plugWarnUntil = now.Add(PlugWarnFor)
	}
	v.diskState = r.Disk.State
	if v.diskState == "" {
		v.diskState = "none"
	}
}

// recElapsed is the time recorded now: the engine's figure counted on, at most 10 s past it (a frozen engine stops it).
func (u *UI) recElapsed(now time.Time) int {
	v := &u.rec
	if !v.running {
		return 0
	}
	v.shown = max(v.shown, v.base+min(10, int(now.Sub(v.at)/time.Second)))
	return v.shown
}

// recLine is the header's second line about the recording: its dot, what it says and the colours.
type recLine struct {
	main, tail          string
	dot, col, tailColor Color
}

// recHeadLine is the header's second line ("" main: none). Recording: «REC 01:12:34» and the time left (amber under
// 10 min, or that the disk cannot keep up); starting; still asked to record but stopped (why, red if the disk went);
// or why it stopped on its own, for RecNoticeFor.
func (u *UI) recHeadLine(now time.Time) recLine {
	r := u.st.Record
	tx := &u.Tx.Rec
	if r == nil {
		return recLine{}
	}
	switch r.State {
	case "on":
		el := u.recElapsed(now)
		l := recLine{main: sprintf(tx.HeadOnF, el/3600, el/60%60, el%60), dot: cBad, col: cInk, tailColor: cQuiet}
		switch {
		case r.Reason == "disk_slow":
			l.tail, l.tailColor = tx.HeadSlow, cWarn
		case r.Disk.MinutesLeft != nil:
			l.tail = sprintf(tx.LeftF, recSpan(*r.Disk.MinutesLeft, tx))
			if *r.Disk.MinutesLeft < 10 {
				l.tailColor = cWarn
			}
		}
		return l
	case "starting":
		return recLine{main: tx.HeadStarting, dot: cBad, col: cInk}
	}
	why := tx.HeadStopped[r.Reason]
	if why == "" && r.State == "error" {
		why = tx.HeadStopped["internal"]
	}
	if why == "" || (r.Mode != "on" && now.Sub(u.rec.noticeSince) >= RecNoticeFor) {
		return recLine{}
	}
	col := cWarn
	if r.Reason == "disk_removed" {
		col = cBad
	}
	return recLine{main: why, dot: col, col: col}
}

// recShown: REC is on Go live's row now (the layout follows it).
func (u *UI) recShown() bool { return RecButtonState(u.st.Record).Shown }

// --- REC and Go live ---

// recTap is a tap (not a hold) on REC: a REC that cannot start says why.
func (u *UI) recTap() {
	b := RecButtonState(u.st.Record)
	if b.Ready || !b.Shown {
		u.say(sprintf(u.Tx.HoldForF, int(Hold/time.Second)), 3*time.Second)
		return
	}
	u.say(u.Tx.Rec.WhyTap[b.Why], 4*time.Second)
}

// recRefused: the engine said no to REC: why, in words (RECORDING.md §5.2: disabled, no_disk, disk_full).
func (u *UI) recRefused(code string) {
	why, ok := u.Tx.Rec.Refused[code]
	if !ok {
		why = u.commandError(code)
	}
	u.say(sprintf(u.Tx.CannotF, why), 4*time.Second)
}

// drawRecButton is REC: a red dot and «REC», what holding it does or why it cannot start; recording, in red like Go
// live on air; its progress while held.
func (u *UI) drawRecButton(d *dc, b RecButton, progress float64, held bool) {
	lay, c, s := u.Lay, d.c, d.s
	tx := &u.Tx.Rec
	r := lay.Buttons["rec"]
	x, y, w, h := float64(r.X), float64(r.Y), float64(r.W), float64(r.H)
	q := RR{x, y, w, h, 8 * s}
	switch {
	case b.On:
		c.FillRR(q, cBad.WithAlpha(0.24))
		c.StrokeRR(q, 2, cBad)
	case b.Ready:
		c.FillRR(q, cPanel)
		c.StrokeRR(q, 1.5, cBad.WithAlpha(0.7))
	default:
		c.FillRR(q, cPanel)
		c.StrokeRR(q, 1.25, cLine)
	}
	if d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == "rec" {
		d.lit(r, 8*s, 0.16)
	}
	if held {
		c.FillRectInRR(x, y, w*min(1, progress), h, q, cWhite.WithAlpha(0.22))
	}
	sub, subCol := tx.Hold, cQuiet
	switch {
	case b.On && u.st.Record.State == "starting":
		sub = tx.Starting
	case b.On:
		sub = tx.HoldStop
	case !b.Ready:
		sub, subCol = tx.Why[b.Why], cWarn
	}
	size := 21 * s
	lines := d.wrap(sub, 14*s, Sans, w-14*s)
	if len(lines) > 2 {
		lines = []string{sub}
	}
	cy := y + h/2 - 2*s - float64(len(lines)-1)*8*s
	// «● REC»: the dot red while it can record, grey when it cannot
	dot, ink := cBad, cInk
	if !b.Ready {
		dot, ink = cQuiet, cQuiet
	}
	style := Sans
	if b.On {
		style = SansBold
	}
	tw, _ := c.Fonts.Extents(tx.Rec, size, style)
	rad := 6 * s
	x0 := x + w/2 - (2*rad+6*s+tw)/2
	c.FillCircle(x0+rad, cy-size*0.36, rad, dot)
	c.Text(x0+2*rad+6*s, cy, tx.Rec, size, style, ink)
	ly := cy + 19*s
	for _, l := range lines {
		d.text(x+w/2, ly, l, 14*s, textOpt{col: subCol, align: center, width: w - 14*s})
		ly += 16 * s
	}
}

// drawPlugWarn is the warning of a disk plugged in while live: a band over the program, under its tag (the name of
// what is on air stays at its foot), for PlugWarnFor.
func (u *UI) drawPlugWarn(d *dc) {
	if !d.now.Before(u.rec.plugWarnUntil) {
		return
	}
	lay, c, s := u.Lay, d.c, d.s
	p := lay.PGM
	m := float64(lay.M)
	w := float64(p.W) - 2*m
	lines := d.wrap(u.Tx.Rec.PlugWhileLive, 14*s, SansBold, w-2*m)
	bh := float64(len(lines))*19*s + 14*s
	y := float64(p.Y) + 48*s
	c.FillRR(RR{float64(p.X) + m, y, w, bh, 8 * s}, cWarn.WithAlpha(0.94))
	ly := y + 7*s + 14*s
	for _, l := range lines {
		d.text(float64(p.X)+2*m, ly, l, 14*s, textOpt{col: cDeep, bold: true, width: w - 2*m})
		ly += 19 * s
	}
}
