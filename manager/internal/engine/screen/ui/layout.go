// Package ui is the encoder's touch screen without GStreamer or devices (ENGINE_GO.md §3, M3): the layout,
// the drawing of every view into an *image.RGBA (pure Go, DejaVu fonts, the same pixels as the Python
// engine's Cairo screen), and the touch logic. The runtime around it (pipeline, display, evdev) is the
// parent package screen.
//
// The Python engine (scripts/engine_mixer.py: Layout, Screen.on_touch/tick/draw…) is the reference; names
// keep its terms so a reader can follow both side by side.
package ui

import (
	"sort"
	"strconv"
	"strings"
)

// Rect is a rectangle in screen (and touch) pixels.
type Rect struct{ X, Y, W, H int }

// Inside is Layout.inside: (x, y) in r, widened by margin on every side.
func (r Rect) Inside(x, y, margin int) bool {
	return r.X-margin <= x && x < r.X+r.W+margin && r.Y-margin <= y && y < r.Y+r.H+margin
}

func (r Rect) overlaps(b Rect) bool {
	return r.X < b.X+b.W && b.X < r.X+r.W && r.Y < b.Y+b.H && b.Y < r.Y+r.H
}

// Key is one key of the password keyboard: a printable character, or SHIFT, BKSP, SYM, SYM2, ABC, OK.
type Key struct {
	Name string
	R    Rect
}

type kbKey struct {
	name  string
	units float64
}

func kbRow(chars string, w float64) []kbKey {
	out := make([]kbKey, 0, len(chars))
	for _, ch := range chars {
		out = append(out, kbKey{string(ch), w})
	}
	return out
}

func joinRows(parts ...[]kbKey) []kbKey {
	var out []kbKey
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// KBLayers is the wifi password keyboard by layer: rows of (key, width in keys). Between all layers, the
// whole printable ASCII (a test checks it). KBLayerNames is their order.
var (
	kbBottom     = []kbKey{{"SYM", 1.5}, {" ", 5}, {"OK", 3.5}}
	kbBottomSym  = []kbKey{{"ABC", 1.5}, {" ", 5}, {"OK", 3.5}}
	KBLayerNames = []string{"lower", "upper", "sym1", "sym2"}
	KBLayers     = map[string][][]kbKey{
		"lower": {kbRow("qwertyuiop", 1), kbRow("asdfghjkl", 1),
			joinRows([]kbKey{{"SHIFT", 1.5}}, kbRow("zxcvbnm", 1), []kbKey{{"BKSP", 1.5}}), kbBottom},
		"upper": {kbRow("QWERTYUIOP", 1), kbRow("ASDFGHJKL", 1),
			joinRows([]kbKey{{"SHIFT", 1.5}}, kbRow("ZXCVBNM", 1), []kbKey{{"BKSP", 1.5}}), kbBottom},
		"sym1": {kbRow("1234567890", 1), kbRow(`-/:;()$&@"`, 1),
			joinRows([]kbKey{{"SYM2", 1.5}}, kbRow(".,?!'", 1.4), []kbKey{{"BKSP", 1.5}}), kbBottomSym},
		"sym2": {kbRow("[]{}#%^*+=", 1), kbRow("_\\|~<>`", 10/7.0),
			joinRows([]kbKey{{"SYM", 1.5}}, kbRow(".,?!'", 1.4), []kbKey{{"BKSP", 1.5}}), kbBottomSym},
	}
)

// Strip is one column of the audio view.
type Strip struct {
	Name, On, Meter, Fader Rect
	GainX, GainY           int
}

// Layout is the board's screen: the status bar on top (broadcast, network, board and the icon buttons), the
// program large on the left, Composition, Transition and Emit at its right, and below one tile per slot.
// Also the Power menu, the audio view, the Settings panel with the network list, and the keyboard.
type Layout struct {
	DW, DH, N int
	M         int     // margin
	HB        int     // header button height
	IB        int     // header icon button width
	Scale     float64 // dw / 1280: font sizes and radii

	Status   Rect
	PGM      Rect
	PGMMeter Rect
	Tiles    []Rect
	// TileMeters: each source's VU meter inside its tile
	TileMeters []Rect
	Info       Rect // the audio button and the hint
	Menu       Rect
	Labels     map[string][2]int
	Buttons    map[string]Rect
	keys       []string // Buttons' keys sorted, for the hit test (Python iterates sorted(items))

	// Settings: the panel below the header, the list of pages on the left (its title row on top, then one
	// entry per page), and the page on the right (its title row, then its content)
	SettingsPanel  Rect
	SettingsHead   Rect // the list's title row («Ajustes»)
	SettingsList   Rect // where the entries go
	SettingsEntryH int
	SettingsTitle  Rect // the page's title row (its name; ← in a sub-view; the X)
	SettingsPage   Rect // the page's content
	SettingsQR     Rect
	SettingsCams   Rect
	settingsClient int // -1 unset, 0 no client row, 1 with it
	btWarn         int // the Bluetooth page's warning band: -1 unset, 0 none, 1 on top of its controls
	ListRows       int // network list rows per page
	// The Date & time page: the clock's block; the zone picker: its grid
	TimeClock          Rect
	ZoneCols, ZoneRows int
	// The Updates page: the notes' box and the row of the restart that finishes an update; About: where its lines go
	UpdateNotes, UpdateRestart Rect
	AboutLines                 Rect
	// The wizard's list of steps (left column, above «Later»)
	WizardList Rect

	KBField Rect
	KBNote  int
	KB      map[string][]Key

	Strips []Strip
	// ASet is the audio view's settings (audioset.go)
	ASet AudioSetLayout
	// Cam is the Image tab (camera.go)
	Cam CameraLayout
}

// NewLayout is Layout(dw, dh, nslots).
func NewLayout(dw, dh, nslots int) *Layout {
	l := &Layout{DW: dw, DH: dh, N: nslots, Buttons: map[string]Rect{}, settingsClient: -1}
	m := max(8, dw/80)
	l.M = m
	// Header: logo and status on the left, icon buttons (≥ 44 for a finger) on the right, with the same margin m
	// above and below them as at the sides: the separating line never touches them
	hb := max(44, dh/13)
	l.HB = hb
	sb := hb + 2*m
	l.Status = Rect{0, 0, dw, sb}
	k := max(3, nslots)
	tw := (dw - (k+1)*m) / k / 2 * 2
	py := sb + m
	// The program (16:9, at most 5/8 of the width) and the tiles share the height left, with the margin m also
	// below; the tiles keep what is left (their video is letterboxed)
	avail := dh - py - 2*m
	ph := min(dw*5/8*9/16, avail-min(tw*9/16, max(90, dh/6))) / 2 * 2
	pw := ph * 16 / 9 / 2 * 2
	l.PGM = Rect{m, py, pw, ph}
	ty := py + ph + m
	for i := range nslots {
		l.Tiles = append(l.Tiles, Rect{m + i*(tw+m), ty, tw, dh - m - ty})
	}
	// The program's VU meter, vertical between its preview and the buttons
	mw := max(26, dw/40)
	l.PGMMeter = Rect{m + pw + m/2, py, mw, ph}
	x0 := m + pw + m/2 + mw + m
	cw := dw - x0 - m
	bw := (cw - m) / 2
	lab := max(18, dh/30)
	bh := max(44, ph*2/13)
	th2 := max(44, ph/8)
	eh := max(56, ph/5)
	y1 := py + lab
	y2 := y1 + bh + m/2
	y3 := y2 + bh + lab + m/2
	// The header's icon buttons, right to left: Power, Settings (gear) and Lock (padlock)
	ib := max(56, hb*5/4)
	l.IB = ib
	gap := max(6, m*2/3)
	l.Labels = map[string][2]int{"layout": {x0, y1 - lab/4}, "transition": {x0, y3 - lab/4}}
	b := l.Buttons
	b["full"] = Rect{x0, y1, bw, bh}
	b["pip"] = Rect{x0 + bw + m, y1, bw, bh}
	b["side"] = Rect{x0, y2, bw, bh}
	b["pause"] = Rect{x0 + bw + m, y2, bw, bh}
	b["cut"] = Rect{x0, y3, bw, th2}
	b["fade"] = Rect{x0 + bw + m, y3, bw, th2}
	b["emit"] = Rect{x0, py + ph - eh, cw, eh}
	b["power"] = Rect{dw - m - ib, m, ib, hb}
	b["lock"] = Rect{dw - m - 3*ib - 2*gap, m, ib, hb}
	b["settings"] = Rect{dw - m - 2*ib - gap, m, ib, hb}
	// Settings (and the wizard, which shares its pages): a list on the left and the selected page on the right
	l.settingsLayout()
	// The password keyboard (WIFI.md §6): full screen (inside Settings: screen.jpg stays frozen), QWERTY with
	// upper case and numbers/symbols layers: all of printable ASCII
	b["k_cancel"] = Rect{dw - m - ib, m, ib, hb}
	b["k_eye"] = Rect{dw - 2*m - 2*ib, m, ib, hb} // a secret's eye: its characters shown or dots
	fh := max(52, dh/11)
	l.KBField = Rect{m, 2*m + hb, dw - 2*m, fh}
	l.KBNote = 2*m + hb + fh + max(22, dh/26)
	ktop := l.KBNote + m
	kh := (dh - m - ktop - 3*gap) / 4
	u := float64(dw-2*m-9*gap) / 10.0
	l.KB = map[string][]Key{}
	for _, layer := range KBLayerNames {
		var keys []Key
		for j, row := range KBLayers[layer] {
			units := 0.0
			for _, kk := range row {
				units += kk.units
			}
			kx := float64(m) + (10-units)*(u+float64(gap))/2.0
			for _, kk := range row {
				kw := kk.units*u + (kk.units-1)*float64(gap)
				keys = append(keys, Key{kk.name, Rect{int(kx), ktop + j*(kh+gap), int(kw), kh}})
				kx += kw + float64(gap)
			}
		}
		l.KB[layer] = keys
	}
	// Power menu: a panel in the middle with Reboot and Power off (held) and Cancel
	mw2, mh := dw*9/20, dh*11/20
	mx0, my0 := (dw-mw2)/2, (dh-mh)/2
	mb := (mh - 2*m - 54 - 3*m) / 3
	l.Menu = Rect{mx0, my0, mw2, mh}
	b["m_reboot"] = Rect{mx0 + m, my0 + 54 + m, mw2 - 2*m, mb}
	b["m_poweroff"] = Rect{mx0 + m, my0 + 54 + 2*m + mb, mw2 - 2*m, mb}
	b["m_cancel"] = Rect{mx0 + m, my0 + 54 + 3*m + 2*mb, mw2 - 2*m, mb}
	// Audio view (the second): header (← back to video, just the arrow; Una fuente and Mezcla, apart; Sigue al
	// vídeo, only with «Una fuente») and one strip per input (up to 4) plus the program's: big button, meter and
	// fader, gain and − / +
	hh := max(48, dh/12)
	hy := sb + m
	segw := max(150, dw/7)
	bkw := max(ib, hh)
	b["a_back"] = Rect{m, hy, bkw, hh}
	b["a_single"] = Rect{3*m + bkw, hy, segw, hh}
	b["a_mix"] = Rect{4*m + bkw + segw, hy, segw, hh}
	b["a_follow"] = Rect{dw - m - int(float64(segw)*1.4), hy, int(float64(segw) * 1.4), hh}
	cols := 5
	sw := (dw - (cols+1)*m) / cols
	y0 := hy + hh + m
	bh2 := max(48, dh/12)
	for i := range cols {
		x := m + i*(sw+m)
		pad := 8 // the controls' margin inside their strip
		name := Rect{x, y0, sw, 44}
		on := Rect{x + pad, y0 + 48, sw - 2*pad, bh2}
		by2 := dh - m - pad - bh2
		fy := y0 + 48 + bh2 + m
		fb := by2 - 30 // the fader ends above the gain
		meter := Rect{x + pad + 4, fy, max(10, sw/10), fb - fy}
		fader := Rect{x + 2*pad + 4 + meter.W, fy, sw - 3*pad - 4 - meter.W, fb - fy}
		mb2 := (sw - 2*pad - m) / 2
		minus, plus := Rect{x + pad, by2, mb2, bh2}, Rect{x + pad + mb2 + m, by2, mb2, bh2}
		l.Strips = append(l.Strips, Strip{Name: name, On: on, Meter: meter, Fader: fader, GainX: x, GainY: fb + 22})
		if i < cols-1 {
			s := strconv.Itoa(i)
			b["a_on_"+s], b["a_fader_"+s], b["a_minus_"+s], b["a_plus_"+s] = on, fader, minus, plus
		} else {
			// The program's strip: the screen's headphones (HEADPHONES.md), on/off and their volume
			b["a_phones"], b["a_ph_minus"], b["a_ph_plus"] = on, minus, plus
		}
	}
	// Its settings (each input's delay, each camera's audio): their button in this header and their sub-view
	l.audioSetLayout(hy, hh)
	// Each source's VU meter, inside its tile (the program's goes in the column)
	vw := max(8, dw/110)
	for _, r := range l.Tiles {
		l.TileMeters = append(l.TileMeters, Rect{r.X + r.W - vw - 8, r.Y + 8, vw, r.H - 56})
	}
	l.Info = Rect{x0, y3 + th2 + m, cw, py + ph - eh - m - (y3 + th2 + m)} // audio and help
	b["audio"] = l.Info                                                    // «Mezclador de audio ›»
	l.Scale = float64(dw) / 1280.0
	l.sortKeys()
	return l
}

func (l *Layout) sortKeys() {
	l.keys = l.keys[:0]
	for k := range l.Buttons {
		l.keys = append(l.keys, k)
	}
	sort.Strings(l.keys)
}

// KBHit is the keyboard key under (x, y) in that layer (with half a gap of margin), k_cancel, or "".
func (l *Layout) KBHit(x, y int, layer string) string {
	if l.Buttons["k_cancel"].Inside(x, y, 8) {
		return "k_cancel"
	}
	if l.Buttons["k_eye"].Inside(x, y, 8) { // only a secret's keyboard has it (kbTouch)
		return "k_eye"
	}
	for _, k := range l.KB[layer] {
		if k.R.Inside(x, y, 3) {
			return k.Name
		}
	}
	return ""
}

// KeyRect is the rectangle of a key in a layer.
func (l *Layout) KeyRect(layer, key string) (Rect, bool) {
	for _, k := range l.KB[layer] {
		if k.Name == key {
			return k.R, true
		}
	}
	return Rect{}, false
}

// Hit is what a touch falls on: a tile (Slot), the program, a button (Key), a keyboard key (Key), or nothing.
type Hit struct {
	Kind string
	Key  string
	Slot int
}

// Hit kinds.
const (
	HitNone    = ""
	HitTile    = "tile"
	HitProgram = "program"
	HitButton  = "button"
	HitKey     = "key" // a keyboard key (press highlight only)
)

// Hit is Layout.hit: (tile, slot) | (program) | (button, key) | none. With the Power menu open, only its
// buttons (m_*); with the Settings panel, the given keys (settings: those visible, in order: the first that
// fits wins; nil and settingsOpen = the X and the cameras' switch). In the audio views (audio, audioset,
// audiopick), their buttons (audioKeyIn) and the header's.
func (l *Layout) Hit(x, y int, menu bool, view string, settingsOpen bool, settings []string) Hit {
	if menu || settingsOpen {
		keys := settings
		if menu {
			keys = []string{"m_reboot", "m_poweroff", "m_cancel"}
		} else if keys == nil {
			keys = []string{"s_close", "s_net"}
		}
		for _, margin := range []int{0, 8} { // first the exact one: the tabs touch each other
			for _, k := range keys {
				if l.Buttons[k].Inside(x, y, margin) {
					return Hit{Kind: HitButton, Key: k}
				}
			}
		}
		return Hit{}
	}
	if view == "audio" || view == "audioset" || view == "audiopick" {
		for _, margin := range []int{0, 12} {
			for _, k := range l.keys {
				if (audioKeyIn(k, view) || k == "power" || k == "lock" || k == "settings") &&
					l.Buttons[k].Inside(x, y, margin) {
					return Hit{Kind: HitButton, Key: k}
				}
			}
		}
		return Hit{}
	}
	for i, r := range l.Tiles {
		if r.Inside(x, y, 0) {
			return Hit{Kind: HitTile, Slot: i}
		}
	}
	if l.PGM.Inside(x, y, 0) {
		return Hit{Kind: HitProgram}
	}
	// First the exact button; else with a margin (a finger misses by a little), without one's margin stepping
	// on its neighbour
	for _, margin := range []int{0, 16} {
		for _, k := range l.keys {
			if hasAnyPrefix(k, "m_", "a_", "s_", "k_", "w_") {
				continue
			}
			if l.Buttons[k].Inside(x, y, margin) {
				return Hit{Kind: HitButton, Key: k}
			}
		}
	}
	return Hit{}
}

// audioKeyIn: an audio view's button (a_*) answers in that view: the strips' and their header's in "audio"; the
// audio settings' (a_s_*) in "audioset", and with a camera's choice open ("audiopick") only its rows and ←.
func audioKeyIn(k, view string) bool {
	switch {
	case !strings.HasPrefix(k, "a_"):
		return false
	case k == "a_s_back":
		return view == "audioset" || view == "audiopick"
	case strings.HasPrefix(k, "a_s_opt_"):
		return view == "audiopick"
	case strings.HasPrefix(k, "a_s_"):
		return view == "audioset"
	}
	return view == "audio"
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
