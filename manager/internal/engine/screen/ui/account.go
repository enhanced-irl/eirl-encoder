package ui

import (
	"bytes"
	"time"
)

// The Name & password page (Device → Name & password, MIXER.md §21): the encoder's name (PUT /device, as in the
// wizard) and the web's password (PUT /password: the current one, then the new one, each on the secret keyboard with
// its eye; changing it closes every web session). With no password yet (the setup's), only the new one
// (POST /setup/password, as in the wizard).

// accountState keeps the current password and the new one between their keyboards (the new one is typed twice,
// like in the wizard: a mistyped one would leave nobody knowing it, with every web session closed): wiped when they
// are sent, a keyboard is cancelled or the page goes.
type accountState struct {
	cur, next []byte
	sentAt    time.Time
}

func (a *accountState) wipe() {
	clear(a.cur)
	clear(a.next)
	a.cur, a.next = nil, nil
}

func init() {
	registerPage("account", &pageDef{
		layout: func(l *Layout) {
			rows, _ := l.FormRows(5)
			l.Buttons["s_acct_name"] = rows[0]
			l.Buttons["s_acct_pw"] = rows[1]
		},
		draw: (*UI).drawAccountPage,
		keys: func(u *UI, app *AppSnapshot) []string {
			if app.DeviceAPI != Yes || app.Device == nil {
				return nil
			}
			return []string{"s_acct_name", "s_acct_pw"}
		},
		touch: (*UI).accountTouch,
		leave: func(u *UI) { u.acct.wipe() },
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
	pg := u.Lay.SettingsPage
	y := float64(u.Lay.Buttons["s_acct_pw"].Y+u.Lay.Buttons["s_acct_pw"].H) + 30*s
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
	if t := u.resultNote(app, "name", "password", "setup_password"); t != "" {
		return t, true
	}
	l := app.Last
	if l != nil && l.OK && (l.What == "password" || l.What == "setup_password") && l.At.After(u.acct.sentAt) &&
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
			if cur == nil {
				ok = u.app().SetupPassword(next)
			} else {
				ok = u.app().Do(AppAction{What: "password", Secrets: map[string][]byte{"current": cur, "new": next}})
			}
			u.acct.wipe() // the app's client has its own copies until they are sent
			u.appBusy(ok)
			return ""
		}, cancel: func(u *UI) { u.acct.wipe() }}, "")
}
