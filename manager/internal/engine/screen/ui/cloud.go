package ui

import (
	"strings"
	"time"
)

// The EnhancedIRL page (MIXER.md §21): the web's «EnhancedIRL account» card. Unlinked: «Link» asks for a code
// (POST /cloud/pair) and shows it with its QR to confirm from a phone; pairing: a new code, or cancel; linked: to whom,
// whether it is connected, and «Unlink» (DELETE /cloud, with a second tap: what it does is said meanwhile).

func init() {
	registerPage("cloud", &pageDef{
		layout: func(l *Layout) {
			_, bottom := l.FormRows(1)
			bw := max(220, bottom.W*9/25)
			l.Buttons["s_cloud_pair"] = Rect{bottom.X + bottom.W - bw, bottom.Y, bw, bottom.H}
			l.Buttons["s_cloud_unlink"] = Rect{bottom.X + bottom.W - 2*bw - l.M, bottom.Y, bw, bottom.H}
			l.Buttons["s_cloud_leave"] = l.Buttons["s_cloud_pair"] // linked: «Unlink», alone at the right
		},
		draw:  (*UI).drawCloudPage,
		keys:  (*UI).cloudKeys,
		touch: (*UI).cloudTouch,
		watch: func(_ *UI, w *AppWatch) { w.Cloud = true },
	})
}

func (u *UI) cloudKeys(app *AppSnapshot) []string {
	cl := app.Cloud
	if app.CloudAPI != Yes || cl == nil {
		return nil
	}
	switch cl.State {
	case "unlinked":
		return []string{"s_cloud_pair"}
	case "pairing":
		return []string{"s_cloud_pair", "s_cloud_unlink"}
	case "linked":
		return []string{"s_cloud_leave"}
	}
	return nil
}

// cloudError is the link's last error in words (the app gives a code), else as it came.
func (u *UI) cloudError(code string) string {
	if t, ok := u.Tx.Cloud.Errors[code]; ok {
		return t
	}
	return code
}

func (u *UI) drawCloudPage(d *dc, app *AppSnapshot) {
	tx, ct, c, s := u.Tx, u.Tx.Cloud, d.c, d.s
	pg := u.Lay.SettingsPage
	x0 := float64(pg.X) + 4*s
	w0 := float64(pg.W) - 8*s
	cl := app.Cloud
	switch {
	case app.CloudAPI == No || (cl != nil && cl.State == "disabled"):
		u.pageMessage(d, tx.WizCloudOff, "", false)
		return
	case cl == nil && app.CloudAPI == Unknown:
		u.pageMessage(d, tx.AskingApp, "", false)
		return
	case cl == nil:
		u.pageMessage(d, tx.AppNotAnswering, "", true)
		return
	}
	busy := app.Pending == "cloud_pair" || app.Pending == "cloud_unlink"
	note, warn := "", true
	if t := u.resultNote(app, "cloud_pair", "cloud_unlink"); t != "" {
		note = t
	} else if cl.LastError != "" {
		note = u.cloudError(cl.LastError)
	}
	right := float64(u.Lay.Buttons["s_cloud_pair"].X)
	switch cl.State {
	case "linked":
		mid := float64(pg.Y) + 40*s
		u.icon(c, "check", x0+16*s, mid-8*s, 16*s, max(2.0, 3.2*s), cGood)
		t := ct.Linked
		if cl.AccountLabel != "" {
			t = sprintf(ct.LinkedToF, cl.AccountLabel)
		}
		d.text(x0+44*s, mid, t, 24*s, textOpt{col: cGood, bold: true, width: w0 - 44*s})
		on, col := ct.Offline, cWarn
		if cl.Online != nil && *cl.Online {
			on, col = ct.Online, cGood
		}
		c.FillCircle(x0+10*s, mid+38*s, 5*s, col)
		d.text(x0+24*s, mid+44*s, on, 16*s, textOpt{col: cInk, width: w0 - 24*s})
		y := mid + 90*s
		for _, line := range d.wrap(ct.LinkedHint, 15*s, Sans, w0) {
			d.text(x0, y, line, 15*s, textOpt{col: cQuiet, width: w0})
			y += 21 * s
		}
		conf := u.confirming("cloud_unlink")
		label := ct.Unlink
		if conf { // what unlinking does, in the page (it is long), while the second tap is awaited
			label = tx.TapAgain
			who := cl.AccountLabel
			if who == "" {
				who = ct.YourAccount
			}
			y += 14 * s
			for _, line := range d.wrap(sprintf(ct.UnlinkConfirmF, who), 16*s, SansBold, w0) {
				d.text(x0, y, line, 16*s, textOpt{col: cWarn, bold: true, width: w0})
				y += 22 * s
			}
		}
		d.button("s_cloud_leave", btn{label: label, active: conf, col: colp(cBad), dim: busy})
		right = float64(u.Lay.Buttons["s_cloud_leave"].X)
	case "pairing":
		_, bottom := u.Lay.FormRows(1)
		qr := 0
		if len(cl.LinkQR) > 0 {
			qr = u.drawQR(c, cl.LinkQR, pg.X, pg.Y, min(bottom.Y-pg.Y-u.Lay.M, pg.W/2))
		}
		tx0 := float64(pg.X+qr) + 24*s
		tw := float64(pg.X+pg.W) - tx0 - 8*s
		y := float64(pg.Y) + 26*s
		for _, line := range d.wrap(ct.PairingText, 15*s, Sans, tw) {
			d.text(tx0, y, line, 15*s, textOpt{col: cQuiet, width: tw})
			y += 21 * s
		}
		c.Text(tx0, y+40*s, cl.Code, 36*s, MonoBold, cInk)
		y += 70 * s
		if cl.LinkURL != "" {
			for _, line := range d.wrap(strings.TrimPrefix(cl.LinkURL, "https://"), 13*s, Sans, tw) {
				d.text(tx0, y, line, 13*s, textOpt{col: cInk, width: tw})
				y += 18 * s
			}
		}
		if cl.ExpiresAt != nil {
			t := cl.ExpiresAt.In(time.Local)
			if app.Device != nil && !app.Device.Now.IsZero() {
				t = cl.ExpiresAt.In(app.Device.Now.Location())
			}
			d.text(tx0, y+8*s, sprintf(tx.WizCloudExpiresF, sprintf("%02d:%02d", t.Hour(), t.Minute())), 14*s,
				textOpt{col: cQuiet, width: tw})
			y += 26 * s
		}
		d.text(tx0, y+16*s, ct.Waiting, 15*s, textOpt{col: cBrand, bold: true, width: tw})
		d.button("s_cloud_pair", btn{label: ct.Renew, dim: busy})
		d.button("s_cloud_unlink", btn{label: tx.Cancel, dim: busy})
		right = float64(u.Lay.Buttons["s_cloud_unlink"].X)
	default: // unlinked
		y := float64(pg.Y) + 30*s
		for _, line := range d.wrap(ct.Intro, 17*s, Sans, w0) {
			d.text(x0, y, line, 17*s, textOpt{col: cInk, width: w0})
			y += 24 * s
		}
		y += 8 * s
		for _, line := range d.wrap(ct.IntroHow, 15*s, Sans, w0) {
			d.text(x0, y, line, 15*s, textOpt{col: cQuiet, width: w0})
			y += 21 * s
		}
		d.button("s_cloud_pair", btn{label: ct.Pair, active: true, dim: busy})
	}
	u.bottomNote(d, note, warn, right)
}

func (u *UI) cloudTouch(what string) bool {
	app := u.app().AppSnapshot()
	cl := app.Cloud
	if cl == nil {
		return false
	}
	switch what {
	case "s_cloud_pair":
		if cl.State == "unlinked" || cl.State == "pairing" {
			u.appBusy(u.app().Do(AppAction{What: "cloud_pair"}))
		}
	case "s_cloud_unlink": // pairing: cancel (nothing to lose, no second tap)
		if cl.State == "pairing" {
			u.appBusy(u.app().Do(AppAction{What: "cloud_unlink"}))
		}
	case "s_cloud_leave":
		if cl.State == "linked" && u.confirmTap("cloud_unlink") {
			u.appBusy(u.app().Do(AppAction{What: "cloud_unlink"}))
		}
	default:
		return false
	}
	return true
}
