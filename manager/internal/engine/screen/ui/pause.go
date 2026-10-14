package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The Pause page (Stream → Pause, MIXER.md §21): the card's text and whether the pause silences the program. They
// are the engine's own settings: they go straight to it (the pause command, like the web's «Pause settings»); the
// pause itself is put and taken off with the mixer's Pause button.

// PauseTextMax is the card's longest text (runes): the engine's (scene.PauseText).
const PauseTextMax = 60

// pausePunct is the punctuation the card takes besides letters, digits and spaces (the engine's, scene.pausePunct).
const pausePunct = ".,:;!?¡¿'\"()-–—…·/+#@%€$*"

// PauseTextOK is the engine's rule for the card's text: 1-60 runes once trimmed; letters, digits, spaces and
// pausePunct (no < > &: the overlay's markup).
func PauseTextOK(t string) bool {
	t = strings.TrimSpace(t)
	if n := utf8.RuneCountInString(t); n < 1 || n > PauseTextMax {
		return false
	}
	for _, r := range t {
		if !(unicode.IsLetter(r) || unicode.Is(unicode.Nd, r) || r == ' ' || strings.ContainsRune(pausePunct, r)) {
			return false
		}
	}
	return true
}

func init() {
	registerPage("pause", &pageDef{
		layout: func(l *Layout) {
			rows, _ := l.FormRows(4)
			l.Buttons["s_pause_text"] = rows[0]
			l.Buttons["s_pause_mute"] = rows[1]
		},
		draw: (*UI).drawPausePage,
		keys: func(u *UI, _ *AppSnapshot) []string {
			if !u.PauseSupported {
				return nil
			}
			return []string{"s_pause_text", "s_pause_mute"}
		},
		touch: (*UI).pauseTouch,
	})
}

// pauseNow is the program's pause as the engine publishes it (zero if it has none yet).
func (u *UI) pauseNow() (on bool, text string, mute bool) {
	if p := u.st.Program.Pause; p != nil {
		return p.On, p.Text, p.Mute
	}
	return false, "", false
}

func (u *UI) drawPausePage(d *dc, _ *AppSnapshot) {
	tx, s := u.Tx.Stream, d.s
	if !u.PauseSupported {
		u.pageMessage(d, tx.PauseUnsupported, "", false)
		return
	}
	on, text, mute := u.pauseNow()
	val, dim := text, false
	if strings.TrimSpace(val) == "" {
		val, dim = tx.PauseTextDefault, true
	}
	// None set: the engine's own card says the default words; they show quiet
	u.field(d, "s_pause_text", tx.PauseText, val, fieldOpt{chevron: true, placeholder: dim})
	sub := tx.PauseMuteOff
	if mute {
		sub = tx.PauseMuteOn
	}
	u.row(d, "s_pause_mute", "sliders", tx.PauseMute, RowState{knob(mute), false, sub, "quiet"}, false, 0)
	// What the card does, and whether it is on now
	pg := u.Lay.SettingsPage
	y := float64(u.Lay.Buttons["s_pause_mute"].Y+u.Lay.Buttons["s_pause_mute"].H) + 30*s
	help := tx.PauseHelpOff
	if on {
		help = tx.PauseHelpOn
	}
	for _, line := range d.wrap(help, 15*s, Sans, float64(pg.W)-8*s) {
		d.text(float64(pg.X)+4*s, y, line, 15*s, textOpt{col: cQuiet, width: float64(pg.W) - 8*s})
		y += 21 * s
	}
	if t, warn := u.pageNoteText(); t != "" {
		u.noteLine(d, t, warn)
	}
}

func (u *UI) pauseTouch(what string) bool {
	switch what {
	case "s_pause_text":
		_, text, _ := u.pauseNow()
		tx := u.Tx.Stream
		u.openSpecKeyboard(&kbSpec{title: tx.PauseText, help: tx.PauseTextHelp, max: PauseTextMax, min: 1,
			done: func(u *UI, t string, _ []byte) string {
				if !PauseTextOK(t) {
					return u.Tx.Stream.PauseTextBad
				}
				u.send("page", map[string]any{"cmd": "pause", "text": t}, 0, 0)
				return ""
			}}, text)
	case "s_pause_mute":
		_, _, mute := u.pauseNow()
		u.send("page", map[string]any{"cmd": "pause", "mute": !mute}, 0, 0)
	default:
		return false
	}
	return true
}
