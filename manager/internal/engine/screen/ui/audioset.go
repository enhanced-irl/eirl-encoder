package ui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// The audio view's settings (MIXER.md §21: what the web has under «Audio settings», on the screen): each input's
// delay and each camera's audio. A sub-view of the audio view (view "audioset"), entered from its header's
// «Audio settings ›» and left with ←: two columns, the delays (− value +) and the cameras (a row that opens the
// choice of its audio, over both columns: Hit's view "audiopick"). They are the engine's own commands
// (audio_delay, audio_link), sent with origin "audioset": a refusal shows at the bottom of the view.

// Delay bounds and steps (the engine's audio.MaxDelayMs; a tap moves AudioDelayStep, a long press AudioDelayLong).
const (
	AudioDelayMax  = 500
	AudioDelayStep = 10
	AudioDelayLong = 100
	// audioSetSent: how long a delay sent stands for the state's while the engine has not published it yet
	audioSetSent = 2 * time.Second
	// audioSetInputs: the inputs with a delay row, as the strips (the first four)
	audioSetInputs = 4
	// audioSetOpts: the rows of a camera's choice: the inputs, «No audio of its own», and its audio if it is
	// another one (disconnected)
	audioSetOpts = audioSetInputs + 2
	// originAudioSet is the commands' origin: their refusals come back to this view (CommandDone)
	originAudioSet = "audioset"
)

// audioSetState is what the screen keeps of the audio view's settings (each input's delay, each camera's audio)
// (MIXER.md §21).
type audioSetState struct {
	pick *audioPick           // a camera's choice open ("audiopick"), else nil
	sent map[string]sentDelay // delays sent and not seen in the state yet, by input id: the next tap starts there
	note string               // the engine's last refusal, until noteUntil
	// noteUntil: when the refusal stops showing
	noteUntil time.Time
}

// sentDelay is a burst of taps on one input's delay: the last value sent, the state's when the burst started and
// every value sent in it (the engine publishes them one by one).
type sentDelay struct {
	ms, base int
	vals     []int
	until    time.Time
}

// audioPick is a camera's audio being chosen: the camera (its id and its label as shown when it opened), the
// choices as they were then, and the current one ("none" = no audio of its own).
type audioPick struct {
	video, label string
	cur          string
	opts         []pickOpt
}

// AudioSetLayout is where the audio settings' parts go (their buttons are in Layout.Buttons, keys a_s_…).
type AudioSetLayout struct {
	Title       Rect   // the title row, right of ←
	Delay       Rect   // the delays' column: its title and hints at the top (Head high), then its rows
	Links       Rect   // the cameras' column, the same
	Head        int    // the columns' title and hints
	DelayRows   []Rect // each input's row (its − and + inside: a_s_dm_i, a_s_dp_i)
	Pick        Rect   // the choice's hint (above its rows a_s_opt_i)
	Note        Rect   // the bottom line: a refusal
	LinkRowsLen int    // the cameras' rows (a_s_link_i): one per slot
}

// audioSetLayout places the audio settings: its button in the audio view's header (left of «Follows video», which
// keeps its place) and the sub-view (NewLayout: hy and hh are the audio view's header row).
func (l *Layout) audioSetLayout(hy, hh int) {
	b, m, dw, dh := l.Buttons, l.M, l.DW, l.DH
	fw := b["a_follow"]
	b["a_settings"] = Rect{fw.X - m - fw.W, hy, fw.W, hh}
	back := b["a_back"]
	b["a_s_back"] = back
	a := &l.ASet
	a.Title = Rect{back.X + back.W + 2*m, hy, dw - back.X - back.W - 3*m, hh}
	top := hy + hh + m
	noteH := max(28, dh/20)
	a.Note = Rect{m, dh - m - noteH, dw - 2*m, noteH}
	bottom := a.Note.Y
	cw := (dw - 3*m) / 2
	a.Head = max(70, dh*12/100)
	a.Delay = Rect{m, top, cw, bottom - top}
	a.Links = Rect{2*m + cw, top, dw - 3*m - cw, bottom - top}
	rows := max(audioSetInputs, l.N)
	gap := m * 2 / 3
	y0 := top + a.Head
	rh := min(max(56, dh/8), (bottom-y0-(rows-1)*gap)/rows)
	a.DelayRows = a.DelayRows[:0]
	for i := range rows {
		y := y0 + i*(rh+gap)
		s := strconv.Itoa(i)
		if i < audioSetInputs {
			r := Rect{a.Delay.X, y, a.Delay.W, rh}
			a.DelayRows = append(a.DelayRows, r)
			l.audioStepperKeys(r, "a_s_dm_"+s, "a_s_dp_"+s)
		}
		if i < l.N {
			b["a_s_link_"+s] = Rect{a.Links.X, y, a.Links.W, rh}
		}
	}
	a.LinkRowsLen = l.N
	// The choice: its hint, then one row per choice across the width
	ph := max(30, dh/18)
	a.Pick = Rect{m, top, dw - 2*m, ph}
	oy := top + ph
	oh := min(max(52, dh/11), (bottom-oy-(audioSetOpts-1)*gap)/audioSetOpts)
	for i := range audioSetOpts {
		b["a_s_opt_"+strconv.Itoa(i)] = Rect{m, oy + i*(oh+gap), dw - 2*m, oh}
	}
}

// audioStepperKeys places a delay row's − and + at its right, inside it, with room for the value between them.
func (l *Layout) audioStepperKeys(r Rect, minus, plus string) {
	pad := max(0, min(l.M/2, (r.H-48)/2))
	bw := max(48, min(r.H-2*pad, 64))
	vw := max(96, r.W/4)
	l.Buttons[plus] = Rect{r.X + r.W - pad - bw, r.Y + pad, bw, r.H - 2*pad}
	l.Buttons[minus] = Rect{r.X + r.W - pad - 2*bw - vw, r.Y + pad, bw, r.H - 2*pad}
}

// delayStep moves a delay one step in dir (−1, +1) inside 0…AudioDelayMax, snapping to the step's grid (a value
// off it, set on the web, goes to the grid in the direction asked).
func delayStep(v, step, dir int) int { return stepIn(v, step, 0, AudioDelayMax, dir) }

// hitView is the view whose buttons answer (Layout.Hit): the audio settings with a camera's choice open have only
// its rows and ←.
func (u *UI) hitView() string {
	if u.view == "audioset" && u.aset.pick != nil {
		return "audiopick"
	}
	return u.view
}

// openAudioSet enters the audio settings (from the audio view's header).
func (u *UI) openAudioSet() {
	u.view, u.aset.pick, u.aset.note = "audioset", nil, ""
	u.Dirty = true
}

// audioSetInputsNow are the inputs with a row, as the strips: the first four.
func (u *UI) audioSetInputsNow() []AudioInput {
	if u.st.Audio == nil {
		return nil
	}
	in := u.st.Audio.Sources
	return in[:min(len(in), audioSetInputs)]
}

// audioSetCameras are the video sources with a row: each slot's, in order, that the engine links an audio to
// (audio.links; an engine without them has none).
func (u *UI) audioSetCameras() []SourceStatus {
	au := u.st.Audio
	if au == nil || au.Links == nil {
		return nil
	}
	var out []SourceStatus
	for slot := 1; slot <= u.Lay.N; slot++ {
		for _, x := range u.st.Sources {
			if _, ok := au.Links[x.ID]; ok && x.Slot == slot {
				out = append(out, x)
				break
			}
		}
	}
	return out
}

// audioDelay is an input's delay as shown and stepped: the one just sent while the state does not have it yet. It
// stands while the state shows what it was or one of the values sent since; not once the last one is seen, another
// value comes (set on the web) or it is too old.
func (u *UI) audioDelay(a *AudioInput) int {
	if p, ok := u.aset.sent[a.ID]; ok {
		if a.DelayMs != p.ms && u.now().Before(p.until) && (a.DelayMs == p.base || slices.Contains(p.vals, a.DelayMs)) {
			return p.ms
		}
		delete(u.aset.sent, a.ID)
	}
	return a.DelayMs
}

// cameraLink is the id of a camera's audio ("none": no audio of its own).
func (u *UI) cameraLink(video string) string {
	if au := u.st.Audio; au != nil {
		if id := au.Links[video]; id != nil && *id != "" {
			return *id
		}
	}
	return "none"
}

// linkName is how a camera's audio is named: an input as its strip names it; none; or one that is not there.
func (u *UI) linkName(id string) (name string, gone bool) {
	ax := &u.Tx.AudioX
	if id == "none" {
		return ax.NoOwnAudio, false
	}
	if au := u.st.Audio; au != nil {
		for i := range au.Sources {
			if a := &au.Sources[i]; a.ID == id {
				return u.audioName(a), false
			}
		}
	}
	return sprintf(ax.GoneF, goneName(id)), true
}

// goneName is an input that is not there by its id (the card's name: all that is known), shortened so what follows
// it («disconnected») is always seen.
func goneName(id string) string {
	name := strings.TrimPrefix(id, "alsa:")
	if isBTInput(id) && len(id) > 7 { // a Bluetooth device: its address's end
		name = "Bluetooth …" + id[len(id)-4:]
	}
	if r := []rune(name); len(r) > goneNameMax {
		name = string(r[:goneNameMax-1]) + "…"
	}
	return name
}

// goneNameMax is the longest id of an input that is not there, as a camera's audio (runes).
const goneNameMax = 24

// audioStateText is an input's state in words, with why it is not live ("" when it is live).
func (u *UI) audioStateText(a *AudioInput) string {
	if a.State == "live" {
		return ""
	}
	if a.DetailCode != "" {
		if t := u.detailText(a.DetailCode, a.DetailArgs, a.Detail); t != "" {
			return t
		}
	}
	if t, ok := u.Tx.AudioState[a.State]; ok {
		return t
	}
	return a.State
}

// openAudioPick opens the choice of the audio of the camera in row i.
func (u *UI) openAudioPick(i int) {
	cams := u.audioSetCameras()
	if i < 0 || i >= len(cams) {
		return
	}
	cam := &cams[i]
	ax := &u.Tx.AudioX
	p := &audioPick{video: cam.ID, label: sprintf(u.Tx.TileNameF, cam.Slot, u.srcLabel(cam)), cur: u.cameraLink(cam.ID)}
	seen := false
	for _, a := range u.audioSetInputsNow() {
		p.opts = append(p.opts, pickOpt{ID: a.ID, Title: u.audioName(&a), Sub: u.audioStateText(&a), Warn: a.State != "live"})
		seen = seen || a.ID == p.cur
	}
	p.opts = append(p.opts, pickOpt{ID: "none", Title: ax.NoOwnAudio, Sub: ax.NoOwnAudioSub})
	if !seen && p.cur != "none" { // linked to an input that is not there (or past the fourth): it stays in view
		o := pickOpt{ID: p.cur}
		if name, gone := u.linkName(p.cur); gone {
			o.Title, o.Sub, o.Warn = goneName(p.cur), u.Tx.AudioState["gone"], true
		} else {
			o.Title = name
		}
		p.opts = append(p.opts, o)
	}
	u.aset.pick = p
	u.Dirty = true
}

// audioSetTouch is a tap in the audio settings (released on key what, held that long).
func (u *UI) audioSetTouch(what string, held time.Duration, x, y int) {
	u.Dirty = true
	if what == "a_s_back" {
		if u.aset.pick != nil {
			u.aset.pick = nil // back to the settings without choosing
			return
		}
		u.view, u.aset.note = "audio", ""
		return
	}
	if p := u.aset.pick; p != nil {
		i, ok := keyIndex(what, "a_s_opt_")
		if !ok || i >= len(p.opts) {
			return
		}
		u.aset.pick = nil
		if id := p.opts[i].ID; id != p.cur {
			u.send(originAudioSet, map[string]any{"cmd": "audio_link", "video": p.video, "source": id}, x, y)
		}
		return
	}
	if i, ok := keyIndex(what, "a_s_link_"); ok {
		u.openAudioPick(i)
		return
	}
	dir := 0
	i, ok := keyIndex(what, "a_s_dp_")
	if ok {
		dir = 1
	} else if i, ok = keyIndex(what, "a_s_dm_"); ok {
		dir = -1
	}
	ins := u.audioSetInputsNow()
	if dir == 0 || i >= len(ins) {
		return
	}
	a := &ins[i]
	step := AudioDelayStep
	if held >= TapLong {
		step = AudioDelayLong
	}
	cur := u.audioDelay(a)
	ms := delayStep(cur, step, dir)
	if ms == cur {
		return // at its end already
	}
	if u.aset.sent == nil {
		u.aset.sent = map[string]sentDelay{}
	}
	p, ok := u.aset.sent[a.ID] // still there: cur was its ms (audioDelay), the same burst
	if !ok {
		p = sentDelay{base: a.DelayMs}
	}
	p.ms, p.vals, p.until = ms, append(p.vals, ms), u.now().Add(audioSetSent)
	u.aset.sent[a.ID] = p
	u.send(originAudioSet, map[string]any{"cmd": "audio_delay", "source": a.ID, "ms": ms}, x, y)
}

// audioSetRefused: the engine refused one of the view's commands: it says why at its bottom, and a delay goes
// back to the state's.
func (u *UI) audioSetRefused(c Command, code string) {
	cmd, _ := c.Msg["cmd"].(string)
	elog.Event("mixer", "touch_command_failed", "x", c.X, "y", c.Y, "cmd", cmd, "error", code)
	if id, ok := c.Msg["source"].(string); ok && cmd == "audio_delay" {
		delete(u.aset.sent, id)
	}
	u.aset.note, u.aset.noteUntil = sprintf(u.Tx.CannotF, u.commandError(code)), u.now().Add(noteFor)
	u.Dirty = true
}

// keyIndex is i of a key prefix+i.
func keyIndex(key, prefix string) (int, bool) {
	s, ok := strings.CutPrefix(key, prefix)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(s)
	return i, err == nil && i >= 0
}

// --- drawing ---

// drawAudioSet is the audio settings: ← and its title, then the delays and the cameras (or a camera's choice), and a
// refusal at the bottom.
func (u *UI) drawAudioSet(d *dc) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	ax, al := &tx.AudioX, &lay.ASet
	sh := float64(lay.Status.H)
	c.FillRect(0, sh, float64(lay.DW), float64(lay.DH)-sh, cBg.WithAlpha(1))
	d.button("a_s_back", btn{icon: "back"})
	title := ax.Title
	if p := u.aset.pick; p != nil {
		title = sprintf(ax.PickTitleF, p.label)
	}
	tr := al.Title
	d.text(float64(tr.X), float64(tr.Y)+float64(tr.H)/2+7*s, title, 20*s, textOpt{col: cInk, bold: true,
		width: float64(tr.W)})
	if u.aset.pick != nil {
		u.drawAudioPick(d)
	} else {
		u.drawDelays(d)
		u.drawLinks(d)
	}
	if u.aset.note != "" && u.now().Before(u.aset.noteUntil) {
		n := al.Note
		d.text(float64(n.X)+4*s, float64(n.Y+n.H)-8*s, u.aset.note, 15*s, textOpt{col: cWarn, bold: true,
			width: float64(n.W) - 8*s})
	}
}

// audioSetHead is a column's title and its hint lines ("" none).
func (u *UI) audioSetHead(d *dc, r Rect, title string, hints ...string) {
	s := d.s
	x, w := float64(r.X)+4*s, float64(r.W)-8*s
	y := float64(r.Y) + 19*s
	d.text(x, y, title, 19*s, textOpt{col: cInk, bold: true, width: w})
	for _, h := range hints {
		y += 21 * s
		if h != "" {
			d.text(x, y, h, 15*s, textOpt{col: cQuiet, width: w})
		}
	}
}

func (u *UI) drawDelays(d *dc) {
	ax, al := &u.Tx.AudioX, &u.Lay.ASet
	u.audioSetHead(d, al.Delay, ax.Delay, ax.DelayHint, sprintf(ax.DelayRangeF, AudioDelayMax, AudioDelayLong))
	ins := u.audioSetInputsNow()
	if len(ins) == 0 {
		u.audioSetEmpty(d, al.Delay, ax.NoInputs)
		return
	}
	for i := range ins {
		a := &ins[i]
		k := strconv.Itoa(i)
		minus, plus := "a_s_dm_"+k, "a_s_dp_"+k
		ms := u.audioDelay(a)
		u.audioStepper(d, al.DelayRows[i], minus, plus, u.audioName(a), u.audioStateText(a), sprintf(ax.MsF, ms),
			ms <= 0, ms >= AudioDelayMax)
	}
}

// audioStepper is a delay row: its panel, the input's name (and its state below it) at the left, and − value + at
// the right.
func (u *UI) audioStepper(d *dc, r Rect, minus, plus, title, sub, value string, dimMinus, dimPlus bool) {
	lay, tx, c, s := u.Lay, u.Tx, d.c, d.s
	m := float64(lay.M)
	q := rrOf(r, 10*s)
	c.FillRR(q, cPanel)
	c.StrokeRR(q, 1.25, cLine)
	a, b := lay.Buttons[minus], lay.Buttons[plus]
	x0 := float64(r.X) + 1.5*m
	w := float64(a.X) - x0 - m
	mid := float64(r.Y) + float64(r.H)/2
	if sub == "" {
		d.text(x0, mid+6*s, title, 16*s, textOpt{col: cInk, bold: true, width: w})
	} else {
		d.text(x0, mid-3*s, title, 16*s, textOpt{col: cInk, bold: true, width: w})
		d.text(x0, mid+16*s, sub, 12*s, textOpt{col: cWarn, width: w})
	}
	d.button(minus, btn{label: tx.Minus, dim: dimMinus})
	d.button(plus, btn{label: tx.Plus, dim: dimPlus})
	cx := float64(a.X+a.W+b.X) / 2
	d.text(cx, mid+7*s, value, 19*s, textOpt{col: cInk, bold: true, align: center, width: float64(b.X-a.X-a.W) - 4*s})
}

func (u *UI) drawLinks(d *dc) {
	ax, al := &u.Tx.AudioX, &u.Lay.ASet
	hint2 := ""
	switch au := u.st.Audio; {
	case au != nil && au.Mode == "mix":
		hint2 = ax.LinksMix
	case au == nil || !au.Follow:
		hint2 = ax.LinksFollowOff
	}
	u.audioSetHead(d, al.Links, ax.Links, ax.LinksHint, hint2)
	cams := u.audioSetCameras()
	if len(cams) == 0 {
		u.audioSetEmpty(d, al.Links, ax.NoCameras)
		return
	}
	for i := range cams {
		if i >= al.LinkRowsLen {
			break
		}
		cam := &cams[i]
		name, gone := u.linkName(u.cameraLink(cam.ID))
		u.field(d, "a_s_link_"+strconv.Itoa(i), sprintf(u.Tx.TileNameF, cam.Slot, u.srcLabel(cam)), name,
			fieldOpt{chevron: true, warn: gone})
	}
}

// audioSetEmpty is a column without rows: a quiet line where its first row would go.
func (u *UI) audioSetEmpty(d *dc, col Rect, t string) {
	s := d.s
	r := u.Lay.ASet.DelayRows[0]
	d.text(float64(col.X)+4*s, float64(r.Y)+float64(r.H)/2+6*s, t, 16*s, textOpt{col: cQuiet,
		width: float64(col.W) - 8*s})
}

// drawAudioPick is a camera's choice: its hint, then one row per choice (the current one marked).
func (u *UI) drawAudioPick(d *dc) {
	lay, c, s := u.Lay, d.c, d.s
	m := float64(lay.M)
	p, ph := u.aset.pick, lay.ASet.Pick
	d.text(float64(ph.X)+4*s, float64(ph.Y)+float64(ph.H)/2+5*s, u.Tx.AudioX.LinksHint, 15*s, textOpt{col: cQuiet,
		width: float64(ph.W) - 8*s})
	for i, o := range p.opts {
		if i >= audioSetOpts {
			break
		}
		key := "a_s_opt_" + strconv.Itoa(i)
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
		x0, right := float64(r.X)+2*m, float64(r.X+r.W)-2*m
		if active {
			u.icon(c, "check", right-12*s, mid, 12*s, max(2.0, 2.8*s), cBrand)
			right -= 34 * s
		}
		if o.Sub == "" {
			d.text(x0, mid+7*s, o.Title, 19*s, textOpt{col: cInk, bold: active, width: right - x0})
			continue
		}
		sub := cQuiet
		if o.Warn {
			sub = cWarn
		}
		d.text(x0, mid-2*s, o.Title, 19*s, textOpt{col: cInk, bold: active, width: right - x0})
		d.text(x0, mid+17*s, o.Sub, 14*s, textOpt{col: sub, width: right - x0})
	}
}

// --- previews (eirl-ui-render) ---

// AudioSettings shows the audio view's settings; with link ≥ 0, the choice of that camera row's audio open.
func (p Preview) AudioSettings(link int) {
	p.u.openAudioSet()
	if link >= 0 {
		p.u.openAudioPick(link)
	}
}
