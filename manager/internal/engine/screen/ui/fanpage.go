package ui

import (
	"strconv"
	"strings"
)

// The Fan page (System › Fan, FAN.md §5): the temperature the fan control reads and the main fan's speed and, on a
// board with the second fan, its switch: automatic, or off until the next restart. Off is the user's risk to take:
// it still switches on when the board gets too hot (HotC) or the temperature cannot be read. Switching it off takes
// a second tap (and says what it risks); back to automatic, one. The same as the web's card.

func init() {
	registerPage("fan", &pageDef{
		layout: func(l *Layout) {
			rows, _ := l.FormRows(4)
			l.Buttons["s_fan2"] = rows[1]
		},
		draw: (*UI).drawFanPage,
		keys: func(_ *UI, app *AppSnapshot) []string {
			if f := fanOf(app); f != nil && f.Second != nil {
				return []string{"s_fan2"}
			}
			return nil
		},
		touch: (*UI).fanTouch,
	})
}

// fanOf is the fans as the app reports them (nil: not yet, or not at all).
func fanOf(app *AppSnapshot) *FanInfo {
	if app == nil || app.Device == nil {
		return nil
	}
	return app.Device.Fan
}

// degrees is a threshold as people read it: 55, 80, 52,5.
func (u *UI) degrees(x float64) string {
	return strings.Replace(strconv.FormatFloat(x, 'f', -1, 64), ".", u.Tx.Decimal, 1)
}

func (u *UI) drawFanPage(d *dc, app *AppSnapshot) {
	tx, s := u.Tx.Fan, d.s
	if !u.devicePageReady(d, app.DeviceAPI, app.Device != nil) {
		return
	}
	f := app.Device.Fan
	if f == nil {
		u.pageMessage(d, tx.NoData, "", false)
		return
	}
	rows, _ := u.Lay.FormRows(4)
	top := rows[0]
	w := float64(top.W) - 8*s
	d.text(float64(top.X)+4*s, float64(top.Y)+float64(top.H)/2+7*s, sprintf(tx.TempF, u.decimal(f.TempC),
		(f.PWM*100+127)/255), 18*s, textOpt{col: cInk, bold: true, width: w})
	help, warn := tx.None, false
	if sf := f.Second; sf != nil {
		auto, conf := sf.Mode == "auto", u.confirming("fan2_off")
		var sub, tone string
		switch {
		case conf:
			sub, tone = tx.Confirm, "warn"
		case auto && sf.On:
			sub, tone = sprintf(tx.AutoOnF, u.degrees(sf.OnC)), "good"
		case auto:
			sub, tone = sprintf(tx.AutoF, u.degrees(sf.OnC)), "quiet"
		case sf.On:
			sub, tone = sprintf(tx.OffOnF, u.degrees(sf.HotC)), "warn"
		default:
			sub, tone = sprintf(tx.OffF, u.degrees(sf.HotC)), "warn"
		}
		u.row(d, "s_fan2", "fan", tx.Second, RowState{knob(auto), app.Pending == "fan2", sub, tone}, sf.On, 0)
		help = sprintf(tx.HelpF, u.degrees(sf.OnC))
		if !auto || conf {
			help, warn = tx.HelpOff, true
		}
	}
	col := cQuiet
	if warn {
		col = cWarn
	}
	y := float64(rows[1].Y+rows[1].H) + 30*s
	if f.Second == nil {
		y = float64(rows[1].Y) + 24*s
	}
	for _, line := range d.wrap(help, 15*s, Sans, w) {
		d.text(float64(top.X)+4*s, y, line, 15*s, textOpt{col: col, width: w})
		y += 21 * s
	}
	u.noteLine(d, u.resultNote(app, "fan2"), true)
}

func (u *UI) fanTouch(what string) bool {
	if what != "s_fan2" {
		return false
	}
	snap := u.app().AppSnapshot()
	f := fanOf(&snap)
	if f == nil || f.Second == nil {
		return true
	}
	if f.Second.Mode == "auto" {
		if u.confirmTap("fan2_off") {
			u.appBusy(u.app().Do(AppAction{What: "fan2", Value: "off"}))
		}
		return true
	}
	u.appBusy(u.app().Do(AppAction{What: "fan2", Value: "auto"}))
	return true
}
