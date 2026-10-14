package ui

import (
	"bytes"
	_ "embed"
	"image"
	"image/png"
	"strconv"
	"time"
)

//go:embed assets/eirl-logo.png
var logoPNG []byte

// DefaultLogo is EnhancedIRL's logo for the header (embedded: one file less to install).
func DefaultLogo() image.Image {
	img, err := png.Decode(bytes.NewReader(logoPNG))
	if err != nil {
		return nil
	}
	return img
}

// Preview puts the UI in a given situation without touches: eirl-ui-render (tools/ui_render.py's variants)
// and the tests. Never used by the running screen.
type Preview struct{ u *UI }

// Preview is the UI's preview handle.
func (u *UI) Preview() Preview { return Preview{u} }

func (p Preview) AudioView()        { p.u.view = "audio" }
func (p Preview) OpenMenu()         { p.u.menuOpen = true }
func (p Preview) Powering(w string) { p.u.powering, p.u.poweringAt = w, p.u.now() }

// Holding: key held for d (its progress).
func (p Preview) Holding(key string, d time.Duration) {
	p.u.hold = &hold{key: key, since: p.u.now().Add(-d)}
}

// OpenSettings opens the panel like ui_render.py does (without telling Net).
func (p Preview) OpenSettings()            { p.u.settingsOpen = true }
func (p Preview) SettingsPage(page string) { p.u.settingsPage = page }
func (p Preview) OpenZones()               { p.u.openZones() }
func (p Preview) ConfirmUpdate() {
	p.u.confirm = &confirm{what: "update", until: p.u.now().Add(ConfirmUpdate)}
}
func (p Preview) ConfirmSetupAgain() {
	p.u.confirm = &confirm{what: "setup_again", until: p.u.now().Add(ConfirmUpdate)}
}

// TimeDraft shows the manual date and time steppers at t.
func (p Preview) TimeDraft(t time.Time) { p.u.timeDraft, p.u.timeDraftSet = t, true }
func (p Preview) OpenNetworks()         { p.u.openNetworks() }
func (p Preview) ListPage(n int)        { p.u.listPage = n }
func (p Preview) ConfirmForget(ssid string) {
	p.u.confirm = &confirm{what: "forget", ssid: ssid, until: p.u.now().Add(ConfirmFor)}
}

// OpenKeyboard opens the password keyboard with typed characters (a fake password: only dots are drawn).
func (p Preview) OpenKeyboard(ssid, errText, typed, layer string) {
	p.u.openKeyboard(ssid, errText)
	for i := 0; i < len(typed) && p.u.kb.n < PSKMax; i++ {
		p.u.kb.buf[p.u.kb.n] = typed[i]
		p.u.kb.n++
	}
	if layer != "" {
		p.u.kb.layer = layer
	}
}

// Press shows a finger on a button, a tile (key "" and slot) or a keyboard key.
func (p Preview) Press(kind, key string, slot int) {
	p.u.press = &press{hit: Hit{Kind: kind, Key: key, Slot: slot}, since: p.u.now()}
}

// EditSlot changes the Sources page's draft: slot n to assign ("" = unchanged) and alias ("" = unchanged).
func (p Preview) EditSlot(n int, assign, alias string) {
	slots := p.u.srcEdit()
	if n < 1 || n > len(slots) {
		return
	}
	if assign != "" {
		slots[n-1].Assign = assign
	}
	if alias != "" {
		slots[n-1].Alias = alias
	}
}

// PickSlot opens the picker of what slot n is.
func (p Preview) PickSlot(n int) { p.u.srcTouch("s_src_"+strconv.Itoa(n), "", false) }

// AliasKeyboard opens the alias keyboard of slot n with a text.
func (p Preview) AliasKeyboard(n int, text string) {
	p.u.settingsOpen = true
	p.u.openTextKeyboard("alias", n, text, AliasMax)
}

// SSIDKeyboard opens the keyboard of the AP's name with a text and, if not "", its error line.
func (p Preview) SSIDKeyboard(text, errText string) {
	p.u.openTextKeyboard("ssid", 0, text, SSIDMax)
	p.u.kb.err = errText
}

// WizardStep shows the wizard at step i (as if the app said the setup is not done).
func (p Preview) WizardStep(i int) {
	app := p.u.app().AppSnapshot()
	p.u.wizardSync(&app)
	p.u.wizardGo(i)
}

// WizardKeyboard opens one of the wizard's keyboards (webpw, webpw2, name) with what is typed (a fake password:
// only dots are drawn).
func (p Preview) WizardKeyboard(mode, typed string) {
	if mode == "name" {
		p.u.openTextKeyboard("name", 0, typed, NameMax)
		return
	}
	p.u.kbClear()
	p.u.kb = &keyboard{mode: mode, layer: "lower"}
	p.u.kb.n = copy(p.u.kb.buf[:], typed)
	p.u.settingsView = "keyboard"
}
