package ui

import (
	"bytes"
	"time"
)

// The Name & password page (Device → Name & password, MIXER.md §21): the encoder's name (PUT /device, as in the
// wizard) and the web's password (PUT /password: the current one, then the new one, each on the secret keyboard with
// its eye; changing it closes every web session). With no password yet (the setup's), only the new one
// (POST /setup/password, as in the wizard). With one, the rescue (MANAGER.md §6), the counterweight to the cloud's
// set_local_password: the setup code behind its eye (GET /setup/code: the web's «Forgot the password?» asks for it)
// and «Forgot the web password?», a new one without the current one (POST /password/reset: the screen is physical
// access).

// accountState keeps the current password and the new one between their keyboards (the new one is typed twice,
// like in the wizard: a mistyped one would leave nobody knowing it, with every web session closed): wiped when they
// are sent, a keyboard is cancelled or the page goes.
type accountState struct {
	cur, next []byte
	sentAt    time.Time
	reset     bool // the new password is a reset (POST /password/reset), not the setup's first one
}

func (a *accountState) wipe() {
	clear(a.cur)
	clear(a.next)
	a.cur, a.next, a.reset = nil, nil, false
}

// codeEye is the setup code's eye.
const codeEye = "s_acct_code_eye"

func init() {
	registerPage("account", &pageDef{
		layout: func(l *Layout) {
			rows, _ := l.FormRows(5)
			l.Buttons["s_acct_name"] = rows[0]
			l.Buttons["s_acct_pw"] = rows[1]
			l.Buttons["s_acct_code"] = l.eyeKey(rows[2], codeEye)
			l.Buttons["s_acct_reset"] = rows[3]
		},
		draw: (*UI).drawAccountPage,
		keys: func(u *UI, app *AppSnapshot) []string {
			if app.DeviceAPI != Yes || app.Device == nil {
				return nil
			}
			if needsWebPassword(app) {
				return []string{"s_acct_name", "s_acct_pw"}
			}
			return []string{"s_acct_name", "s_acct_pw", codeEye, "s_acct_reset"}
		},
		touch: (*UI).accountTouch,
		leave: func(u *UI) {
			u.acct.wipe()
			u.hideSetupCode()
		},
	})
}

// needsWebPassword: the web has no password yet (the setup's step): the page sets the first one.
func needsWebPassword(app *AppSnapshot) bool { return app.Setup != nil && app.Setup.NeedsPassword }

func (u *UI) drawAccountPage(d *dc, app *AppSnapshot) {
	tx, at, s := u.Tx, u.Tx.Account, d.s
	dev := app.Device
	if !u.devicePageReady(d, app.DeviceAPI, dev != nil) {
		return
	}
	host := ""
	if dev.Hostname != "" {
		host = sprintf(tx.WizNameHostF, dev.Hostname+".local")
	}
	busyName := app.Pending == "name"
	u.field(d, "s_acct_name", at.Name, dev.Name, fieldOpt{chevron: true, sub: host, dim: busyName})
	pwVal, pwSub := "", at.PasswordSub
	set := !needsWebPassword(app)
	if !set {
		pwVal, pwSub = tx.NotSet, at.PasswordNone
	}
	u.field(d, "s_acct_pw", at.Password, pwVal, fieldOpt{chevron: true, secret: true, set: set, sub: pwSub})
	last := "s_acct_pw"
	if set {
		// The setup code: eight dots, or the code (XXXX-XXXX, as on the label) with its eye open
		shown, code := u.revealed(codeEye), ""
		if shown {
			code = "…" // the app has not answered yet
			if c := app.SetupCode; len(c) == 8 {
				code = c[:4] + "-" + c[4:]
			}
		}
		u.field(d, "s_acct_code", at.SetupCode, code, fieldOpt{secret: true, shown: shown, set: true, sub: at.SetupCodeSub})
		u.eye(d, codeEye, shown)
		u.field(d, "s_acct_reset", at.Forgot, at.ForgotValue, fieldOpt{chevron: true, sub: at.ForgotSub,
			dim: app.Pending == "password_reset"})
		last = "s_acct_reset"
	}
	pg := u.Lay.SettingsPage
	y := float64(u.Lay.Buttons[last].Y+u.Lay.Buttons[last].H) + 30*s
	for _, line := range d.wrap(at.Help, 15*s, Sans, float64(pg.W)-8*s) {
		d.text(float64(pg.X)+4*s, y, line, 15*s, textOpt{col: cQuiet, width: float64(pg.W) - 8*s})
		y += 21 * s
	}
	note, warn := u.accountNote(app)
	u.noteLine(d, note, warn)
}

// accountNote is the page's bottom line: the last change's result (8 s).
func (u *UI) accountNote(app *AppSnapshot) (string, bool) {
	if t, warn := u.pageNoteText(); t != "" {
		return t, warn
	}
	if t := u.resultNote(app, "name", "password", "setup_password", "password_reset", "setup_code"); t != "" {
		return t, true
	}
	l := app.Last
	if l != nil && l.OK && (l.What == "password" || l.What == "setup_password" || l.What == "password_reset") &&
		l.At.After(u.acct.sentAt) &&
		u.now().Sub(l.At) < 8*time.Second {
		return u.Tx.Account.PasswordChanged, false
	}
	return "", false
}

func (u *UI) accountTouch(what string) bool {
	app := u.app().AppSnapshot()
	at := u.Tx.Account
	switch what {
	case "s_acct_name":
		if app.Device == nil {
			return true
		}
		u.openSpecKeyboard(&kbSpec{title: u.Tx.WizNameKeyboard, help: u.Tx.WizNameHelp, max: NameMax, min: 1,
			done: func(u *UI, t string, _ []byte) string {
				if !nameOK(t) {
					return u.Tx.WizNameBad
				}
				if d := u.app().AppSnapshot().Device; d != nil && d.Name == t {
					return ""
				}
				u.appBusy(u.app().Do(AppAction{What: "name", Value: t}))
				return ""
			}}, app.Device.Name)
	case codeEye:
		// Opened: asked of the app (GET /setup/code); closed: forgotten (it is a credential: not kept on view)
		if !u.toggleReveal(what) {
			u.dropSecrets()
		} else if app.SetupCode == "" && app.Pending != "setup_code" {
			u.appBusy(u.app().Do(AppAction{What: "setup_code"}))
		}
	case "s_acct_reset":
		if app.Pending == "password_reset" {
			return true
		}
		u.acct.wipe()
		u.acct.reset = true
		u.openNewPassword(nil)
	case "s_acct_pw":
		u.acct.wipe()
		if needsWebPassword(&app) {
			u.openNewPassword(nil)
			return true
		}
		u.openSpecKeyboard(&kbSpec{title: at.Current, help: at.CurrentHelp, secret: true, max: 72, min: 1,
			done: func(u *UI, _ string, b []byte) string {
				u.acct.cur = append([]byte(nil), b...)
				u.openNewPassword(u.acct.cur)
				return ""
			}, cancel: func(u *UI) { u.acct.wipe() }}, "")
	default:
		return false
	}
	return true
}

// openNewPassword is the new password's keyboard, then its repetition's: with the current one (cur), PUT /password;
// without it (no password yet), the setup's. errText: why it is asked again (the two did not match).
func (u *UI) openNewPassword(cur []byte, errText ...string) {
	at := u.Tx.Account
	u.openSpecKeyboard(&kbSpec{title: at.New, help: at.NewHelp, secret: true, max: 72, min: WebPasswordMin,
		done: func(u *UI, _ string, b []byte) string {
			if len(bytes.TrimSpace(b)) == 0 { // checked on the bytes: the password never becomes a string
				return sprintf(u.Tx.Account.NewShortF, WebPasswordMin)
			}
			clear(u.acct.next)
			u.acct.next = append([]byte(nil), b...)
			u.openRepeatPassword(cur)
			return ""
		}, cancel: func(u *UI) { u.acct.wipe() }}, "")
	if len(errText) > 0 && u.kb != nil {
		u.kb.err = errText[0]
	}
}

// openRepeatPassword asks for the new password again; the same: it is sent; else back to the new one's keyboard.
func (u *UI) openRepeatPassword(cur []byte) {
	tx := u.Tx
	u.openSpecKeyboard(&kbSpec{title: tx.WizPwAgain, help: tx.Account.NewHelp, secret: true, max: 72, min: 1,
		done: func(u *UI, _ string, b []byte) string {
			next := u.acct.next
			if !bytes.Equal(b, next) {
				clear(u.acct.next)
				u.acct.next = nil
				u.openNewPassword(cur, u.Tx.WizPwMismatch)
				return ""
			}
			u.acct.sentAt = u.now().Add(-time.Millisecond)
			var ok bool
			switch {
			case cur == nil && u.acct.reset:
				ok = u.app().Do(AppAction{What: "password_reset", Secrets: map[string][]byte{"password": next}})
			case cur == nil:
				ok = u.app().SetupPassword(next)
			default:
				ok = u.app().Do(AppAction{What: "password", Secrets: map[string][]byte{"current": cur, "new": next}})
			}
			u.acct.wipe() // the app's client has its own copies until they are sent
			u.appBusy(ok)
			return ""
		}, cancel: func(u *UI) { u.acct.wipe() }}, "")
}

// hideSetupCode closes the setup code's eye and forgets the code the app gave.
func (u *UI) hideSetupCode() {
	if u.reveal[codeEye] {
		delete(u.reveal, codeEye)
		u.dropSecrets()
	}
}
