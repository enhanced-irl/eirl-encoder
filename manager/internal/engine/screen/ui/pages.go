package ui

import (
	"time"
)

// The device pages' actions (CONTRACT §1, §2, §4, §5): each one goes to the app (App.Do) and its result comes
// back in AppSnapshot.Last, which the page shows for a few seconds if it failed.

// appBusy: an action the app could not take now (its queue full, or no app).
func (u *UI) appBusy(ok bool) {
	if !ok {
		u.say(u.Tx.BusyChange, 3*time.Second)
	}
}

// chooseLanguage: the screen changes at once (the app's answer confirms it; a failure goes back to the
// device's), and the device language is the app's (the web follows it).
func (u *UI) chooseLanguage(code string) {
	if !KnownLanguage(code) {
		return
	}
	if !u.app().Do(AppAction{What: "language", Value: code}) {
		u.appBusy(false)
		return
	}
	u.SetLanguage(code)
}

// languageResult: a failed language change goes back to the device's (once per result).
func (u *UI) languageResult(snap *AppSnapshot) {
	l := snap.Last
	if l == nil || !l.At.After(u.lastResult) {
		return
	}
	u.lastResult = l.At
	if l.What == "language" && !l.OK && KnownLanguage(u.lastAppLang) {
		u.SetLanguage(u.lastAppLang)
	}
}

func (u *UI) toggleNTP() {
	snap := u.app().AppSnapshot()
	if snap.Device == nil {
		return
	}
	u.timeDraftSet = false
	u.appBusy(u.app().Do(AppAction{What: "ntp", On: !snap.Device.NTP}))
}

// openZones opens the zone picker on the current zone's region and page.
func (u *UI) openZones() {
	snap := u.app().AppSnapshot()
	cur := ""
	if snap.Device != nil {
		cur = snap.Device.Timezone
	}
	u.settingsView, u.zoneRegion, u.zonePage = "zones", regionOf(cur), 0
	if cur == "" {
		u.zoneRegion = "Europe"
	}
	if zs := zonesIn(snap.Zones, u.zoneRegion); len(zs) > 0 {
		for i, z := range zs {
			if z.ID == cur {
				u.zonePage = i / max(1, u.Lay.ZoneCols*u.Lay.ZoneRows)
			}
		}
	}
	u.Dirty = true
}

// chooseZone sets the time zone and goes back to the page.
func (u *UI) chooseZone(zone string) {
	if u.app().Do(AppAction{What: "timezone", Value: zone}) {
		u.settingsView = "main"
	} else {
		u.appBusy(false)
	}
}

// stepTime moves the manual date or time one step (s_t_<field>_m / _p), from the device's clock the first time.
func (u *UI) stepTime(key string) {
	snap := u.app().AppSnapshot()
	if snap.Device == nil || len(key) < 7 {
		return
	}
	if !u.timeDraftSet {
		u.timeDraft, u.timeDraftSet = u.deviceNow(snap.Device).Truncate(time.Minute), true
	}
	n := 1
	if key[len(key)-1] == 'm' {
		n = -1
	}
	t := u.timeDraft
	switch key[4 : len(key)-2] {
	case "day":
		t = t.AddDate(0, 0, n)
	case "mon":
		t = addMonthsClamped(t, n)
	case "year":
		t = addMonthsClamped(t, 12*n)
	case "hour":
		t = t.Add(time.Duration(n) * time.Hour)
	case "min":
		t = t.Add(time.Duration(n) * time.Minute)
	}
	if t.Year() >= 2020 && t.Year() <= 2099 {
		u.timeDraft = t
	}
}

// addMonthsClamped adds months keeping the day inside the new month (31 Jan + 1 month = 28/29 Feb).
func addMonthsClamped(t time.Time, n int) time.Time {
	first := time.Date(t.Year(), t.Month(), 1, t.Hour(), t.Minute(), 0, 0, t.Location()).AddDate(0, n, 0)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(t.Day(), last), t.Hour(), t.Minute(), 0, 0, t.Location())
}

// setTime sends the manual date and time (only with the automatic time off).
func (u *UI) setTime() {
	snap := u.app().AppSnapshot()
	if snap.Device == nil || snap.Device.NTP {
		return
	}
	t := u.timeDraft
	if !u.timeDraftSet {
		t = u.deviceNow(snap.Device)
	}
	if u.app().Do(AppAction{What: "time", Time: t}) {
		u.timeDraftSet = false
	} else {
		u.appBusy(false)
	}
}

func (u *UI) updateCheck() {
	snap := u.app().AppSnapshot()
	if up := snap.Update; up == nil || up.State == "checking" || up.State == "downloading" || up.State == "installing" {
		return
	}
	u.appBusy(u.app().Do(AppAction{What: "update_check"}))
}

// updateInstall: never while streaming; a second tap within ConfirmUpdate confirms it.
func (u *UI) updateInstall() {
	snap := u.app().AppSnapshot()
	up := snap.Update
	if up == nil || up.Available == nil {
		return
	}
	if ok, _ := u.installEnabled(&snap); !ok {
		return
	}
	now := u.now()
	if u.confirm != nil && u.confirm.what == "update" && now.Before(u.confirm.until) {
		u.confirm = nil
		u.appBusy(u.app().Do(AppAction{What: "update_install"}))
		return
	}
	u.confirm = &confirm{what: "update", until: now.Add(ConfirmUpdate)}
}

// setupAgain: «Run the setup again» (POST /setup/restart), with a second tap; with the setup not done yet,
// «Continue the setup» shows the wizard at once.
func (u *UI) setupAgain() {
	snap := u.app().AppSnapshot()
	if snap.Setup != nil && !snap.Setup.Done {
		u.wizardResume()
		return
	}
	now := u.now()
	if u.confirm != nil && u.confirm.what == "setup_again" && now.Before(u.confirm.until) {
		u.confirm = nil
		if u.app().Do(AppAction{What: "setup_restart"}) {
			u.wizardPostponed = false
		} else {
			u.appBusy(false)
		}
		return
	}
	u.confirm = &confirm{what: "setup_again", until: now.Add(ConfirmUpdate)}
}

// wizardResume shows the setup again after «Later» (Settings → About → «Continue the setup»).
func (u *UI) wizardResume() {
	u.wizardPostponed = false
	u.openSettings(false)
}

// WebPasswordMin is the web password's shortest length (CONTRACT §2: ≥ 10).
const WebPasswordMin = 10
