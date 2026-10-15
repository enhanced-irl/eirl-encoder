package ui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/camera"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// The Image tab (Sources → Image, SOURCES.md §7, MIXER.md §21): a USB camera's image settings (brightness, exposure,
// white balance, focus…) as the engine reads them from the camera (sources[].controls). On top, the cameras that have
// settings (chips, with more than one) and «Factory settings» (a second tap); then the groups the camera has (chips)
// and that group's settings as rows: − value + (a tap moves ~2 % of its range, a long press ~10 %), a switch, or a
// menu's choices. They are the engine's own commands (camera_control, camera_reset), sent straight to it with origin
// "camera": a refusal shows on the page. A burst of taps steps from the value just sent while the state does not carry
// it yet (as the audio delays).

// CameraGroups are the groups of settings, in their chips' order (SOURCES.md §7.2).
var CameraGroups = []string{"image", "exposure", "white_balance", "framing"}

const (
	// camTapPct and camLongPct: what a tap and a long press move an integer setting, in % of its range (a whole
	// number of its steps, at least one)
	camTapPct  = 2
	camLongPct = 10
	// camSent: how long a value sent stands for the state's while the engine has not published it yet
	camSent = 2 * time.Second
	// camSegs: a menu's choices at most (the anti-flicker's four)
	camSegs = 4
	// camRowsMax: a group's rows on one page at most
	camRowsMax = 6
	// originCamera is the commands' origin: their refusals come back to the page (CommandDone)
	originCamera = "camera"
)

// cameraState is what the screen keeps of the Image tab while it is on view: the camera and group chosen, the
// group's page, and the values sent and not seen in the state yet.
type cameraState struct {
	src, group string
	page       int
	sent       map[string]sentValue // by camSentKey(source, control)
}

// sentValue is a burst of changes of one setting: the last value sent, the state's when the burst started and every
// value sent in it (the engine publishes them one by one).
type sentValue struct {
	v, base int
	vals    []int
	until   time.Time
}

// CameraLayout is where the Image tab's parts go (its buttons are in Layout.Buttons, keys s_cam_…).
type CameraLayout struct {
	Head  Rect   // the top row's left part: the cameras' chips, or the camera's name
	Rows  []Rect // the settings' rows (s_cam_row_i; inside, s_cam_m_i and s_cam_p_i, or s_cam_seg_i_k)
	Paged int    // the rows of a page with the pager (s_prev, s_next) under them
	Hint  Rect   // the bottom line: the hint, a refusal, or what the reset's second tap does
}

func init() {
	registerPage("camera", &pageDef{
		layout: (*Layout).cameraLayout,
		draw:   (*UI).drawCameraPage,
		keys:   (*UI).cameraKeys,
		touch:  (*UI).cameraTouch,
		leave:  func(u *UI) { u.cam = cameraState{} },
	})
}

// camChip is the key of camera k's chip when there are n (each number of cameras has its own, as wide as fits).
func camChip(n, k int) string { return "s_cam_c" + strconv.Itoa(n) + "_" + strconv.Itoa(k) }

// camSeg is the key of row i's menu choice j (of camSegs, right-aligned: a menu of n choices takes the last n).
func camSeg(i, j int) string { return "s_cam_seg_" + strconv.Itoa(i) + "_" + strconv.Itoa(j) }

func camSentKey(src, control string) string { return src + "\x00" + control }

// cameraLayout places the Image tab: the top row (the cameras' chips and «Factory settings» at its right), the
// groups' chips, then the rows, at least five (the Logi's exposure) of 48-56 px above the hint line. A group with more
// rows than fit is paged: the shared ‹ › (s_prev, s_next) at the bottom.
func (l *Layout) cameraLayout() {
	c, b, m := l.SettingsPage, l.Buttons, l.M
	gap := m / 2
	a := &l.Cam
	ch := max(48, l.HB)
	fw := max(190, c.W*28/100)
	b["s_cam_reset"] = Rect{c.X + c.W - fw, c.Y, fw, ch}
	a.Head = Rect{c.X, c.Y, c.W - fw - m, ch}
	for n := 2; n <= l.N; n++ {
		w := (a.Head.W - (n-1)*gap) / n
		for k := range n {
			b[camChip(n, k)] = Rect{a.Head.X + k*(w+gap), c.Y, w, ch}
		}
	}
	// The groups' chips, as tabs: four of the same width (a camera with fewer takes the first ones)
	gy := c.Y + ch + gap
	gn := len(CameraGroups)
	gw := (c.W - (gn-1)*gap) / gn
	for k := range gn {
		b["s_cam_g_"+strconv.Itoa(k)] = Rect{c.X + k*(gw+gap), gy, gw, ch}
	}
	top := gy + ch + m
	hh := max(24, l.HB/2)
	a.Hint = Rect{c.X, c.Y + c.H - hh, c.W, hh}
	avail := a.Hint.Y - gap - top
	rh := max(48, min(max(56, l.HB+l.HB/5), (avail-4*gap)/5))
	n := min(camRowsMax, max(1, (avail+gap)/(rh+gap)))
	pager := b["s_prev"].Y - gap
	// Inside a row, its buttons: 48 px high at least, half a margin from its right
	bh := max(48, rh-m)
	vp, hp := (rh-bh)/2, gap
	bw := max(48, min(64, bh+8))
	vw := max(110, c.W/6)
	sw := max(80, c.W/7)
	a.Rows, a.Paged = a.Rows[:0], 0
	for i := range n {
		r := Rect{c.X, top + i*(rh+gap), c.W, rh}
		a.Rows = append(a.Rows, r)
		if r.Y+r.H <= pager {
			a.Paged = i + 1
		}
		k := strconv.Itoa(i)
		b["s_cam_row_"+k] = r
		b["s_cam_p_"+k] = Rect{r.X + r.W - hp - bw, r.Y + vp, bw, bh}
		b["s_cam_m_"+k] = Rect{r.X + r.W - hp - 2*bw - vw, r.Y + vp, bw, bh}
		for j := range camSegs {
			b[camSeg(i, j)] = Rect{r.X + r.W - hp - (camSegs-j)*sw - (camSegs-1-j)*gap, r.Y + vp, sw, bh}
		}
	}
}

// --- the cameras and their settings ---

// cameras are the sources with image settings, by slot (a hidden or gone source carries none).
func (u *UI) cameras() []*SourceStatus {
	var out []*SourceStatus
	for i := range u.st.Sources {
		if x := &u.st.Sources[i]; len(x.Controls) > 0 {
			out = append(out, x)
		}
	}
	slices.SortStableFunc(out, func(a, b *SourceStatus) int { return a.Slot - b.Slot })
	return out[:min(len(out), max(1, u.Lay.N))]
}

// camNow is the camera on view (the one chosen, else the first), the groups it has and the group on view (the one
// chosen if the camera has it, else its first).
func (u *UI) camNow(cams []*SourceStatus) (cam *SourceStatus, groups []string, group string) {
	cam = cams[0]
	for _, x := range cams {
		if x.ID == u.cam.src {
			cam = x
		}
	}
	groups = camGroups(cam)
	if slices.Contains(groups, u.cam.group) {
		return cam, groups, u.cam.group
	}
	return cam, groups, groups[0]
}

// camGroups are the groups a camera has, in CameraGroups' order (one the screen does not know goes after them), as
// many as there are chips.
func camGroups(cam *SourceStatus) []string {
	var out []string
	for _, g := range CameraGroups {
		if slices.ContainsFunc(cam.Controls, func(c CameraControl) bool { return c.Group == g }) {
			out = append(out, g)
		}
	}
	for _, c := range cam.Controls {
		if !slices.Contains(out, c.Group) {
			out = append(out, c.Group)
		}
	}
	return out[:min(len(out), len(CameraGroups))]
}

// camRows are a group's settings the screen can change (int, bool, menu), in the engine's order.
func camRows(cam *SourceStatus, group string) []*CameraControl {
	var out []*CameraControl
	for i := range cam.Controls {
		c := &cam.Controls[i]
		if c.Group == group && (c.Type == "int" || c.Type == "bool" || c.Type == "menu") {
			out = append(out, c)
		}
	}
	return out
}

// camPage is the rows on the page on view, the page and how many there are (the pager takes the last row's room).
func (u *UI) camPage(rows []*CameraControl) (shown []*CameraControl, page, pages int) {
	a := &u.Lay.Cam
	per := len(a.Rows)
	if len(rows) > per {
		per = max(1, a.Paged)
	}
	pages = max(1, (len(rows)+per-1)/per)
	page = max(0, min(u.cam.page, pages-1))
	lo := page * per
	return rows[lo:min(len(rows), lo+per)], page, pages
}

// camMenu are a menu's choices, as many as fit (an engine without options: its range).
func camMenu(c *CameraControl) []int {
	opts := c.Options
	if len(opts) == 0 {
		for v := c.Min; v <= c.Max && len(opts) < camSegs; v++ {
			opts = append(opts, v)
		}
	}
	return opts[:min(len(opts), camSegs)]
}

// camValue is a setting's value as shown and stepped: the one just sent while the state does not have it yet. It
// stands while the state shows what it was or one of the values sent since; not once the last one is seen, another
// value comes (the web, a reset) or it is too old.
func (u *UI) camValue(src string, c *CameraControl) int {
	k := camSentKey(src, c.ID)
	if p, ok := u.cam.sent[k]; ok {
		if c.Value != p.v && u.now().Before(p.until) && (c.Value == p.base || slices.Contains(p.vals, c.Value)) {
			return p.v
		}
		delete(u.cam.sent, k)
	}
	return c.Value
}

// camTop is an integer setting's highest value on its grid (from its minimum).
func camTop(c *CameraControl) int {
	step := max(1, c.Step)
	return c.Min + max(0, c.Max-c.Min)/step*step
}

// camStep moves an integer setting from v in dir (−1, +1): ~camTapPct % of its range (camLongPct held), a whole
// number of its steps and at least one, kept on its grid and inside its range.
func camStep(c *CameraControl, v, dir int, long bool) int {
	step := max(1, c.Step)
	pct := camTapPct
	if long {
		pct = camLongPct
	}
	span := max(0, c.Max-c.Min)
	big := max(step, (span*pct/100+step/2)/step*step)
	off := v + dir*big - c.Min
	if off <= 0 {
		return c.Min
	}
	return min(camTop(c), c.Min+(off+step/2)/step*step)
}

// --- touches ---

func (u *UI) cameraKeys(_ *AppSnapshot) []string {
	cams := u.cameras()
	if len(cams) == 0 {
		return nil
	}
	cam, groups, group := u.camNow(cams)
	var keys []string
	if n := len(cams); n > 1 {
		for k := range n {
			keys = append(keys, camChip(n, k))
		}
	}
	keys = append(keys, "s_cam_reset")
	for k := range groups {
		keys = append(keys, "s_cam_g_"+strconv.Itoa(k))
	}
	shown, page, pages := u.camPage(camRows(cam, group))
	for i, c := range shown {
		k := strconv.Itoa(i)
		switch {
		case c.Inactive: // its automatic is on: nothing to change
		case c.Type == "int":
			keys = append(keys, "s_cam_m_"+k, "s_cam_p_"+k)
		case c.Type == "bool":
			keys = append(keys, "s_cam_row_"+k)
		case c.Type == "menu":
			opts := camMenu(c)
			for j := range opts {
				keys = append(keys, camSeg(i, camSegs-len(opts)+j))
			}
		}
	}
	if page > 0 {
		keys = append(keys, "s_prev")
	}
	if page < pages-1 {
		keys = append(keys, "s_next")
	}
	return keys
}

func (u *UI) cameraTouch(what string) bool {
	cams := u.cameras()
	if len(cams) == 0 {
		return false
	}
	cam, groups, group := u.camNow(cams)
	_, page, _ := u.camPage(camRows(cam, group))
	switch {
	case what == "s_cam_reset":
		if u.confirmTap("camera_reset") {
			for k := range u.cam.sent {
				if strings.HasPrefix(k, cam.ID+"\x00") {
					delete(u.cam.sent, k)
				}
			}
			u.send(originCamera, map[string]any{"cmd": "camera_reset", "source": cam.ID}, 0, 0)
		}
	case what == "s_prev":
		u.cam.page = max(0, page-1)
	case what == "s_next":
		u.cam.page = page + 1
	case strings.HasPrefix(what, "s_cam_g_"):
		if i, ok := keyIndex(what, "s_cam_g_"); ok && i < len(groups) {
			u.cam.group, u.cam.page = groups[i], 0
		}
	case strings.HasPrefix(what, "s_cam_c"): // another camera: its group, the same if it has it
		n, k, ok := camChipIndex(what)
		if ok && n == len(cams) && k < n && cams[k] != cam {
			u.cam.src, u.cam.page, u.confirm = cams[k].ID, 0, nil
		}
	default:
		return u.camRowTouch(cam, group, what)
	}
	return true
}

// camChipIndex is n and k of a camera chip's key (camChip).
func camChipIndex(key string) (n, k int, ok bool) {
	rest, ok := strings.CutPrefix(key, "s_cam_c")
	if !ok {
		return 0, 0, false
	}
	a, b, ok := strings.Cut(rest, "_")
	n, e1 := strconv.Atoi(a)
	k, e2 := strconv.Atoi(b)
	return n, k, ok && e1 == nil && e2 == nil && k >= 0
}

// camRowTouch is a tap on a row: − or + of an integer (held: a long step), a switch, or a menu's choice. An inactive
// setting sends nothing.
func (u *UI) camRowTouch(cam *SourceStatus, group, what string) bool {
	shown, _, _ := u.camPage(camRows(cam, group))
	row := func(i int) *CameraControl {
		if i < 0 || i >= len(shown) || shown[i].Inactive {
			return nil
		}
		return shown[i]
	}
	if s, ok := strings.CutPrefix(what, "s_cam_seg_"); ok {
		a, b, _ := strings.Cut(s, "_")
		i, e1 := strconv.Atoi(a)
		j, e2 := strconv.Atoi(b)
		if c := row(i); c != nil && e1 == nil && e2 == nil && c.Type == "menu" {
			opts := camMenu(c)
			if j -= camSegs - len(opts); j >= 0 && j < len(opts) {
				u.camSet(cam.ID, c, opts[j])
			}
		}
		return true
	}
	if i, ok := keyIndex(what, "s_cam_row_"); ok {
		if c := row(i); c != nil && c.Type == "bool" {
			v := 1
			if u.camValue(cam.ID, c) != 0 {
				v = 0
			}
			u.camSet(cam.ID, c, v)
		}
		return true
	}
	dir := 0
	i, ok := keyIndex(what, "s_cam_p_")
	if ok {
		dir = 1
	} else if i, ok = keyIndex(what, "s_cam_m_"); ok {
		dir = -1
	}
	if !ok {
		return false
	}
	if c := row(i); c != nil && c.Type == "int" {
		u.camSet(cam.ID, c, camStep(c, u.camValue(cam.ID, c), dir, u.tapHeld >= TapLong))
	}
	return true
}

// camSet sends a setting's new value (nothing if it has it already) and keeps it as the shown one until the state
// carries it.
func (u *UI) camSet(src string, c *CameraControl, v int) {
	if v == u.camValue(src, c) {
		return // at its end already, or that choice
	}
	if u.cam.sent == nil {
		u.cam.sent = map[string]sentValue{}
	}
	k := camSentKey(src, c.ID)
	p, ok := u.cam.sent[k] // still there: the value shown was its v (camValue), the same burst
	if !ok {
		p = sentValue{base: c.Value}
	}
	p.v, p.vals, p.until = v, append(p.vals, v), u.now().Add(camSent)
	u.cam.sent[k] = p
	u.send(originCamera, map[string]any{"cmd": "camera_control", "source": src, "control": c.ID, "value": v}, 0, 0)
}

// cameraRefused: the engine refused one of the tab's commands: the page says why, and the setting goes back to the
// state's value.
func (u *UI) cameraRefused(c Command, code string) {
	cmd, _ := c.Msg["cmd"].(string)
	elog.Event("mixer", "touch_command_failed", "x", c.X, "y", c.Y, "cmd", cmd, "error", code)
	src, _ := c.Msg["source"].(string)
	if ctl, ok := c.Msg["control"].(string); ok {
		delete(u.cam.sent, camSentKey(src, ctl))
	}
	why := u.commandError(code)
	if code == "unsupported" { // for these commands: the camera has no settings now, not an engine without them
		why = u.Tx.Camera.NoSettings
	}
	u.notePage(sprintf(u.Tx.CannotF, why), true)
}

// --- words ---

// camLabel is a setting's name (its id if the screen does not know it).
func (u *UI) camLabel(id string) string {
	if t, ok := u.Tx.Camera.Control[id]; ok {
		return t
	}
	return id
}

// camGroupLabel is a group's name (its id if the screen does not know it).
func (u *UI) camGroupLabel(id string) string {
	if t, ok := u.Tx.Camera.Group[id]; ok {
		return t
	}
	return id
}

// camChoice is a menu's choice in words: the anti-flicker's, else its number.
func (u *UI) camChoice(c *CameraControl, v int) string {
	if c.ID == "power_line" && v >= 0 && v < len(u.Tx.Camera.PowerLine) {
		return u.Tx.Camera.PowerLine[v]
	}
	return signed(v)
}

// camText is a setting's value in words: a switch on or off, a menu's choice, or a number with its unit (the exposure
// in ms with one decimal, the colour temperature in K, pan and tilt in degrees).
func (u *UI) camText(c *CameraControl, v int) string {
	tx := &u.Tx.Camera
	switch {
	case c.Type == "bool":
		if v != 0 {
			return tx.On
		}
		return tx.Off
	case c.Type == "menu":
		return u.camChoice(c, v)
	case c.ID == "exposure":
		return sprintf(tx.MsF, u.decimal(float64(v)/10))
	case c.ID == "wb_temperature":
		return sprintf(tx.KelvinF, v)
	case c.ID == "pan" || c.ID == "tilt":
		if v%3600 == 0 {
			return sprintf(tx.DegreesF, signed(v/3600))
		}
		return sprintf(tx.DegreesF, u.decimal(float64(v)/3600))
	}
	return signed(v)
}

// signed is an integer with a real minus sign.
func signed(v int) string {
	if v < 0 {
		return "−" + strconv.Itoa(-v)
	}
	return strconv.Itoa(v)
}

// decimal is a number with one decimal, in the language's way.
func (u *UI) decimal(x float64) string {
	t := strconv.FormatFloat(x, 'f', 1, 64)
	t = strings.Replace(t, ".", u.Tx.Decimal, 1)
	return strings.Replace(t, "-", "−", 1)
}

// --- drawing ---

func (u *UI) drawCameraPage(d *dc, _ *AppSnapshot) {
	cams := u.cameras()
	if len(cams) == 0 {
		u.cameraEmpty(d)
		return
	}
	lay, tx, s := u.Lay, &u.Tx.Camera, d.s
	a := &lay.Cam
	cam, groups, group := u.camNow(cams)
	// The top row: the cameras (chips), or the camera's name; and «Factory settings»
	if n := len(cams); n > 1 {
		for k, x := range cams {
			d.button(camChip(n, k), btn{label: sprintf(u.Tx.TileNameF, x.Slot, u.srcLabel(x)), active: x == cam,
				size: 17})
		}
	} else {
		h := a.Head
		mid := float64(h.Y) + float64(h.H)/2
		u.icon(d.c, "camera", float64(h.X)+14*s, mid, 12*s, max(2.0, 2.4*s), cQuiet)
		d.text(float64(h.X)+38*s, mid+7*s, sprintf(u.Tx.TileNameF, cam.Slot, u.srcLabel(cam)), 19*s,
			textOpt{col: cInk, bold: true, width: float64(h.W) - 38*s})
	}
	conf := u.confirming("camera_reset")
	rb := btn{label: tx.Reset, size: 17}
	if conf {
		rb.label, rb.active, rb.col = u.Tx.TapAgain, true, colp(cWarn)
	}
	d.button("s_cam_reset", rb)
	gs := u.camChipSize(d)
	for k, g := range groups {
		d.button("s_cam_g_"+strconv.Itoa(k), btn{label: u.camGroupLabel(g), active: g == group, size: gs})
	}
	shown, page, pages := u.camPage(camRows(cam, group))
	for i, c := range shown {
		u.camRow(d, i, cam.ID, c)
	}
	// Under the rows: the pager, or the hint (a refusal, or what the reset's second tap does, instead)
	note, warn := u.pageNoteText()
	if conf {
		note, warn = tx.ResetConfirm, true
	}
	if pages > 1 {
		if page > 0 {
			d.button("s_prev", btn{label: u.Tx.PrevPage})
		}
		if page < pages-1 {
			d.button("s_next", btn{label: u.Tx.NextPage})
		}
		if note == "" {
			note = sprintf(u.Tx.PageF, page+1, pages)
		}
		u.pagerNote(d, note, warn)
		return
	}
	col := cQuiet
	if note == "" {
		note = tx.Hint
	} else if warn {
		col = cWarn
	}
	h := a.Hint
	d.text(float64(h.X)+4*s, float64(h.Y+h.H)-7*s, note, 14*s, textOpt{col: col, bold: warn, width: float64(h.W) - 8*s})
}

// camChipSize is the groups' chips' label size (before scaling): the largest from 17 down to 13 at which every
// group's name fits its chip in bold (the chip on view), the same for all of them in a language.
func (u *UI) camChipSize(d *dc) float64 {
	w := float64(u.Lay.Buttons["s_cam_g_0"].W) - 16*d.s
	size := 17.0
	for ; size > 13; size-- {
		fits := true
		for _, g := range CameraGroups {
			if _, ink := d.c.Fonts.Extents(u.camGroupLabel(g), size*d.s, SansBold); ink > w {
				fits = false
			}
		}
		if fits {
			break
		}
	}
	return size
}

// camRow draws row i: its panel; the setting's name and a quiet line (its factory value, or which switch frees it)
// at the left; and at the right − value +, its switch, or a menu's choices. An inactive one is dimmed, «Auto».
func (u *UI) camRow(d *dc, i int, src string, c *CameraControl) {
	lay, tx, cv, s := u.Lay, &u.Tx.Camera, d.c, d.s
	m := float64(lay.M)
	k := strconv.Itoa(i)
	r := lay.Cam.Rows[i]
	q := rrOf(r, 10*s)
	cv.FillRR(q, cPanel)
	cv.StrokeRR(q, 1.25, cLine)
	v := u.camValue(src, c)
	mid := float64(r.Y) + float64(r.H)/2
	var right float64 // where the name ends
	switch c.Type {
	case "bool":
		key := "s_cam_row_" + k
		if !c.Inactive && d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
			cv.FillRR(q, cWhite.WithAlpha(0.12))
		}
		_, busy := u.cam.sent[camSentKey(src, c.ID)] // sent, not in the state yet
		right = d.knob(float64(r.X+r.W)-2*m, mid, knob(v != 0), busy || c.Inactive) - m
	case "menu":
		opts := camMenu(c)
		for j, o := range opts {
			d.button(camSeg(i, camSegs-len(opts)+j), btn{label: u.camChoice(c, o), active: o == v, dim: c.Inactive,
				size: 16})
		}
		right = float64(lay.Buttons[camSeg(i, camSegs-len(opts))].X) - m
	default:
		minus, plus := "s_cam_m_"+k, "s_cam_p_"+k
		d.button(minus, btn{label: u.Tx.Minus, dim: c.Inactive || v <= c.Min})
		d.button(plus, btn{label: u.Tx.Plus, dim: c.Inactive || v >= camTop(c)})
		val, col := u.camText(c, v), cInk
		if c.Inactive {
			val, col = tx.Auto, cQuiet
		}
		a, b := lay.Buttons[minus], lay.Buttons[plus]
		d.text(float64(a.X+a.W+b.X)/2, mid+7*s, val, 19*s, textOpt{col: col, bold: true, align: center,
			width: float64(b.X-a.X-a.W) - 4*s})
		right = float64(a.X) - m
	}
	sub, tc := sprintf(tx.DefaultF, u.camText(c, c.Default)), cInk
	if c.Inactive {
		tc = cQuiet
		if auto := camera.Master(c.ID); auto != "" {
			sub = sprintf(tx.InactiveF, u.camLabel(auto))
		}
	}
	x0 := float64(r.X) + 1.5*m
	d.text(x0, mid-3*s, u.camLabel(c.ID), 17*s, textOpt{col: tc, bold: true, width: right - x0})
	d.text(x0, mid+15*s, sub, 13*s, textOpt{col: cQuiet, width: right - x0})
}

// cameraEmpty is the tab without a camera to set: what it is for and which sources have no settings here.
func (u *UI) cameraEmpty(d *dc) {
	tx, s := &u.Tx.Camera, d.s
	pg := u.Lay.SettingsPage
	w := float64(pg.W) - 64*s
	cx := float64(pg.X) + float64(pg.W)/2
	lines := d.wrap(tx.EmptyHelp, 16*s, Sans, w)
	y := float64(pg.Y) + float64(pg.H)/2 - float64(len(lines))*11*s
	u.icon(d.c, "camera", cx, y-56*s, 20*s, max(2.0, 3*s), cQuiet)
	d.text(cx, y-12*s, tx.Empty, 20*s, textOpt{col: cInk, bold: true, align: center, width: w})
	for _, line := range lines {
		y += 23 * s
		d.text(cx, y, line, 16*s, textOpt{col: cQuiet, align: center, width: w})
	}
}

// --- previews (eirl-ui-render) ---

// CameraShow shows a camera's group on the Image tab: the camera by its source id ("" the first) and the group (""
// its first).
func (p Preview) CameraShow(src, group string) { p.u.cam.src, p.u.cam.group = src, group }
