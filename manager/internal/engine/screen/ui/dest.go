package ui

import (
	"strconv"
	"strings"
	"time"
)

// The Destination page (Stream → Destination, MIXER.md §21): the web's «Destination» card. Edits stay on the screen
// (a draft) until «Save» or «Save and apply» (PUT /destination); the stream key and the passphrase are typed on the
// keyboard (dots, with its eye) and kept only in the draft until they are sent; the saved ones show as dots and their
// eye asks the app for them (GET /destination/secrets, local socket only), dropped when the page goes. Set by the
// EnhancedIRL account (managedBy eirl): read-only, as on the web.

// The bounds of the app's validation (config.Validate).
const (
	LatencyMin, LatencyMax, LatencyStep = 20, 8000, 100
	VideoMin, VideoMax, VideoStep       = 500, 20000, 500
	VideoMinMin, VideoMinStep           = 200, 100
	StreamIDMax                         = 512
	PassphraseMin, PassphraseMax        = 10, 79
	DestURLMax                          = 512
)

// AudioRates are the audio bitrates the app takes (kbps).
var AudioRates = []int{64, 96, 128, 160, 192, 256}

// destState is the page's draft: nil fields are unchanged. The typed secrets live here until they are sent (Do
// copies them) or the draft goes, and are wiped then.
type destState struct {
	url                 *string
	latency, video, aud *int
	vmin                *int
	abr                 *bool
	sid, pass           []byte // typed; nil = as saved
	clearSID, clearPass bool   // typed empty: remove it
	savedAt             time.Time
	synced              time.Time // the save result already taken (destSync)
	sentAt              time.Time // when the last save was sent (an edit after it keeps the draft)
	sentApply           bool
	dirtyAt             time.Time
}

func (d *destState) wipe() {
	clear(d.sid)
	clear(d.pass)
	*d = destState{savedAt: d.savedAt, sentApply: d.sentApply, synced: d.synced, sentAt: d.sentAt}
}

func (d *destState) dirty() bool {
	return d.url != nil || d.latency != nil || d.video != nil || d.aud != nil || d.vmin != nil || d.abr != nil ||
		d.sid != nil || d.pass != nil || d.clearSID || d.clearPass
}

func init() {
	registerPage("dest", &pageDef{
		layout: func(l *Layout) {
			destsLayout(l) // with saved destinations (dests.go)
			rows, bottom := l.FormRows(5)
			b := l.Buttons
			b["s_dest_url"] = rows[0]
			sid, pass := l.halves(rows[1])
			b["s_dest_sid"] = l.eyeKey(sid, "s_dest_sid_eye")
			b["s_dest_pass"] = l.eyeKey(pass, "s_dest_pass_eye")
			lat, aud := l.halves(rows[2])
			b["s_dest_lat"], b["s_dest_aud"] = lat, aud
			l.stepperKeys(lat, "s_dest_lat_m", "s_dest_lat_p")
			l.stepperKeys(aud, "s_dest_aud_m", "s_dest_aud_p")
			vid, vmin := l.halves(rows[3])
			b["s_dest_vid"], b["s_dest_vmin"] = vid, vmin
			l.stepperKeys(vid, "s_dest_vid_m", "s_dest_vid_p")
			l.stepperKeys(vmin, "s_dest_vmin_m", "s_dest_vmin_p")
			b["s_dest_abr"] = rows[4]
			aw, sw := max(240, bottom.W*2/5), max(160, bottom.W/4) // «Save and apply» is the long one
			b["s_dest_apply"] = Rect{bottom.X + bottom.W - aw, bottom.Y, aw, bottom.H}
			b["s_dest_save"] = Rect{bottom.X + bottom.W - aw - l.M - sw, bottom.Y, sw, bottom.H}
		},
		// With saved destinations (an app with GET /destinations), the list and a destination's page (dests.go); else
		// the single destination of an older app
		draw: func(u *UI, d *dc, app *AppSnapshot) {
			if destsOn(app) {
				u.drawDests(d, app)
				return
			}
			u.drawDestPage(d, app)
		},
		title: (*UI).destsTitle,
		keys: func(u *UI, app *AppSnapshot) []string {
			if destsOn(app) {
				return u.destsKeys(app)
			}
			return u.destKeys(app)
		},
		touch: func(u *UI, what string) bool {
			app := u.app().AppSnapshot()
			if destsOn(&app) {
				return u.destsTouch(what)
			}
			return u.destTouch(what)
		},
		watch: func(_ *UI, w *AppWatch) { w.Dest = true },
		leave: func(u *UI) {
			u.dst.wipe()
			u.destsLeave()
			u.dropSecrets()
		},
	})
}

// destNow is what the page shows: the draft over the app's.
type destNow struct {
	url                     string
	latency, video, aud, vm int
	abr                     bool
	sidSet, passSet         bool
}

func (u *UI) destNow(d *DestInfo) destNow {
	n := destNow{url: d.URL, latency: d.LatencyMs, video: d.VideoKbps, aud: d.AudioKbps, vm: d.VideoMinKbps,
		abr: d.ABR, sidSet: d.StreamIDSet, passSet: d.PassphraseSet}
	dr := &u.dst
	if dr.url != nil {
		n.url = *dr.url
	}
	for _, p := range []struct {
		dst *int
		src *int
	}{{&n.latency, dr.latency}, {&n.video, dr.video}, {&n.aud, dr.aud}, {&n.vm, dr.vmin}} {
		if p.src != nil {
			*p.dst = *p.src
		}
	}
	if dr.abr != nil {
		n.abr = *dr.abr
	}
	switch {
	case dr.clearSID:
		n.sidSet = false
	case dr.sid != nil:
		n.sidSet = true
	}
	switch {
	case dr.clearPass:
		n.passSet = false
	case dr.pass != nil:
		n.passSet = true
	}
	return n
}

// destManaged: EnhancedIRL sets the destination (read-only here).
func destManaged(d *DestInfo) bool { return d != nil && d.ManagedBy == "eirl" }

func (u *UI) destKeys(app *AppSnapshot) []string {
	d := app.Dest
	if app.DestAPI != Yes || d == nil {
		return nil
	}
	keys := []string{"s_dest_sid_eye", "s_dest_pass_eye"}
	if destManaged(d) {
		return keys
	}
	keys = append(keys, "s_dest_url", "s_dest_sid", "s_dest_pass", "s_dest_lat_m", "s_dest_lat_p", "s_dest_aud_m",
		"s_dest_aud_p", "s_dest_vid_m", "s_dest_vid_p", "s_dest_abr")
	if u.destNow(d).abr {
		keys = append(keys, "s_dest_vmin_m", "s_dest_vmin_p")
	}
	if u.dst.dirty() {
		keys = append(keys, "s_dest_save", "s_dest_apply")
	} else if d.PendingRestart {
		keys = append(keys, "s_dest_apply")
	}
	return keys
}

// destSecretLen is how many dots a secret field draws with its eye closed: the typed one's length, the revealed
// one's, else 0 (secretText draws eight).
func (u *UI) destSecretLen(app *AppSnapshot, which string) int {
	dr := &u.dst
	typed := dr.sid
	if which == "pass" {
		typed = dr.pass
	}
	if typed != nil {
		return len(typed)
	}
	if s := app.DestSecrets; s != nil {
		if which == "pass" {
			return len(s.Passphrase)
		}
		return len(s.StreamID)
	}
	return 0
}

// destSecretText is what a secret field's eye shows: the typed one (not saved yet), else the saved one the app
// revealed ("" while it has not answered).
func (u *UI) destSecretText(app *AppSnapshot, which string) string {
	dr := &u.dst
	typed, saved := dr.sid, ""
	if which == "pass" {
		typed = dr.pass
	}
	if typed != nil {
		return string(typed)
	}
	if s := app.DestSecrets; s != nil {
		saved = s.StreamID
		if which == "pass" {
			saved = s.Passphrase
		}
	}
	return saved
}

func (u *UI) drawDestPage(d *dc, app *AppSnapshot) {
	tx, st := u.Tx, u.Tx.Stream
	dest := app.Dest
	if !u.devicePageReady(d, app.DestAPI, dest != nil) {
		return
	}
	u.destSync(app)
	n := u.destNow(dest)
	dr := &u.dst
	ro := destManaged(dest)
	fe := u.destFieldErrors(app)
	url := n.url
	if url == "" {
		url = st.DestNone
		if dest.URLInvalid && dr.url == nil {
			url = st.DestURLInvalid
		}
	}
	urlSub := ""
	switch {
	case strings.HasPrefix(n.url, "srtla://"):
		urlSub = st.DestModeSrtla
	case dest.Mode == "listener":
		urlSub = st.DestModeListener
	case n.url != "":
		urlSub = st.DestModeCaller
	}
	u.field(d, "s_dest_url", st.DestURL, url, fieldOpt{chevron: !ro, dim: ro, changed: dr.url != nil, mono: n.url != "",
		tail: true, placeholder: n.url == "", warn: fe["url"] != "", sub: urlSub})
	for _, f := range []struct {
		key, eye, which, title string
		set, changed           bool
	}{{"s_dest_sid", "s_dest_sid_eye", "sid", st.DestStreamID, n.sidSet, dr.sid != nil || dr.clearSID},
		{"s_dest_pass", "s_dest_pass_eye", "pass", st.DestPassphrase, n.passSet, dr.pass != nil || dr.clearPass}} {
		shown := u.revealed(f.eye)
		val := tx.NotSet
		switch {
		case f.set && shown:
			val = u.destSecretText(app, f.which)
			if val == "" { // the app has not answered yet
				val = "…"
			}
		case f.set: // dots: only its length counts (the secret itself never becomes a string with the eye closed)
			val = strings.Repeat("·", u.destSecretLen(app, f.which))
		}
		u.field(d, f.key, f.title, val, fieldOpt{chevron: !ro, dim: ro, changed: f.changed, secret: true, shown: shown,
			set: f.set, warn: fe[map[string]string{"sid": "streamId", "pass": "passphrase"}[f.which]] != ""})
		u.eye(d, f.eye, shown)
	}
	steppers := []struct {
		box, m, p, title, val, field string
		changed, off                 bool
		lo, hi                       bool
	}{
		{"s_dest_lat", "s_dest_lat_m", "s_dest_lat_p", st.DestLatency, itoa(n.latency), "latencyMs",
			dr.latency != nil, false, n.latency <= LatencyMin, n.latency >= LatencyMax},
		{"s_dest_aud", "s_dest_aud_m", "s_dest_aud_p", st.DestAudio, itoa(n.aud), "audioBitrateKbps",
			dr.aud != nil, false, n.aud <= AudioRates[0], n.aud >= AudioRates[len(AudioRates)-1]},
		{"s_dest_vid", "s_dest_vid_m", "s_dest_vid_p", st.DestVideo, itoa(n.video), "videoBitrateKbps",
			dr.video != nil, false, n.video <= VideoMin, n.video >= VideoMax},
		{"s_dest_vmin", "s_dest_vmin_m", "s_dest_vmin_p", st.DestVideoMin, itoa(n.vm), "videoMinBitrateKbps",
			dr.vmin != nil, !n.abr, n.vm <= VideoMinMin, n.vm >= n.video},
	}
	for _, sp := range steppers {
		sub := fe[sp.field] // an error of the app's (the minimum's «only with adaptive» is the switch's line)
		u.stepperTitle(d, u.Lay.Buttons[sp.box], sp.m, sp.title, sub)
		if sp.changed {
			d.frameR(u.Lay.Buttons[sp.box], cBrand, 2, 10*d.s)
		}
		u.stepper(d, sp.m, sp.p, sp.val, ro || sp.off || sp.lo, ro || sp.off || sp.hi)
	}
	abrSub := st.DestABROff
	if n.abr {
		abrSub = st.DestABROn
	}
	kn := knob(n.abr)
	if ro {
		kn = KnobNone
	}
	u.row(d, "s_dest_abr", "stream", st.DestABR, RowState{kn, false, abrSub, "quiet"}, false, 0)
	if dr.abr != nil {
		d.frameR(u.Lay.Buttons["s_dest_abr"], cBrand, 2, 10*d.s)
	}
	// The bottom: the note, and the buttons
	busy := app.Pending == "dest_save" || app.Pending == "restart"
	note, warn := u.destNote(app, dest, fe)
	right := float64(u.Lay.SettingsPage.X + u.Lay.SettingsPage.W)
	switch {
	case ro:
	case dr.dirty():
		d.button("s_dest_save", btn{label: tx.Save, dim: busy})
		label := tx.SaveApply
		if u.confirming("dest_apply") {
			label = tx.TapAgain
		}
		d.button("s_dest_apply", btn{label: label, active: true, dim: busy, col: colp(map[bool]Color{true: cWarn,
			false: cBrand}[u.confirming("dest_apply")])})
		right = float64(u.Lay.Buttons["s_dest_save"].X)
	case dest.PendingRestart:
		label := tx.Apply
		if u.confirming("dest_restart") {
			label = tx.TapAgain
		}
		d.button("s_dest_apply", btn{label: label, active: true, dim: busy, col: colp(cWarn)})
		right = float64(u.Lay.Buttons["s_dest_apply"].X)
	}
	u.bottomNote(d, note, warn, right)
}

// bottomNote is the note of a form page, at the left of its buttons (wrapped in up to two lines).
func (u *UI) bottomNote(d *dc, t string, warn bool, right float64) {
	u.bottomNoteIn(d, t, warn, float64(u.Lay.SettingsPage.X), right)
}

// bottomNoteIn is bottomNote between left and right (a button on each side).
func (u *UI) bottomNoteIn(d *dc, t string, warn bool, left, right float64) {
	if t == "" {
		return
	}
	s := d.s
	_, bottom := u.Lay.FormRows(1)
	col := cQuiet
	style := Sans
	if warn {
		col, style = cWarn, SansBold
	}
	x0 := left + 4*s
	if left > float64(u.Lay.SettingsPage.X) {
		x0 = left + float64(u.Lay.M)
	}
	w := right - x0 - float64(u.Lay.M)
	lines := d.wrap(t, 14*s, style, w)
	if len(lines) > 2 {
		lines = append(lines[:1], strings.Join(lines[1:], " "))
	}
	y := float64(bottom.Y) + float64(bottom.H)/2 + 5*s - float64(len(lines)-1)*9*s
	for _, l := range lines {
		d.text(x0, y, l, 14*s, textOpt{col: col, bold: warn, width: w})
		y += 18 * s
	}
}

// destFieldErrors are the app's field errors of the last save (field → message), while it is fresh.
func (u *UI) destFieldErrors(app *AppSnapshot) map[string]string {
	out := map[string]string{}
	l := app.Last
	if l == nil || l.OK || l.What != "dest_save" || !l.At.After(u.dst.savedAt) || l.At.Before(u.pageSince) {
		return out
	}
	for k, f := range l.Fields {
		out[k] = f.Message
		if out[k] == "" {
			out[k] = sprintf(u.Tx.AppMessageF, f.Code)
		}
	}
	return out
}

// destNote is the page's bottom line: a refusal, what the last save did, the draft, a saved change not applied.
func (u *UI) destNote(app *AppSnapshot, dest *DestInfo, fe map[string]string) (string, bool) {
	tx := u.Tx
	if destManaged(dest) {
		return tx.ManagedByCloud, false
	}
	if t, warn := u.pageNoteText(); t != "" {
		return t, warn
	}
	if u.confirming("dest_apply") || u.confirming("dest_restart") {
		return tx.ApplyCuts, true
	}
	if t := u.resultNote(app, "dest_reveal"); t != "" { // an eye that could not be opened
		return t, true
	}
	l := app.Last
	fresh := l != nil && (l.What == "dest_save" || l.What == "restart") && l.At.After(u.dst.savedAt) &&
		!l.At.Before(u.pageSince) && u.now().Sub(l.At) < 8*time.Second
	switch {
	case len(fe) > 0:
		for _, k := range []string{"url", "streamId", "passphrase", "latencyMs", "audioBitrateKbps", "videoBitrateKbps",
			"videoMinBitrateKbps", "mode"} {
			if fe[k] != "" {
				return fe[k], true
			}
		}
		for _, v := range fe {
			return v, true
		}
	case fresh && !l.OK:
		return u.resultText(l), true
	case fresh && l.What == "restart":
		return tx.SavedRestarting, false
	case fresh && l.Restarted != nil && *l.Restarted:
		return tx.SavedRestarting, false
	case fresh && u.dst.sentApply:
		return tx.SavedStopped, false
	case fresh:
		return tx.SavedLater, false
	case u.dst.dirty():
		return tx.Unsaved, false
	case dest.PendingRestart:
		return tx.PendingApply, false
	}
	return "", false
}

// destSync: after a save the app answered OK, the draft goes (the app's is the saved one).
func (u *UI) destSync(app *AppSnapshot) {
	l := app.Last
	if l == nil || l.What != "dest_save" || !l.OK || !l.At.After(u.dst.savedAt) || u.dst.synced.Equal(l.At) {
		return
	}
	u.dst.synced = l.At
	if !u.dst.dirtyAt.After(u.dst.sentAt) { // nothing edited since that save
		u.dst.wipe()
	}
	// What an eye showed is the old one (the client dropped it): asked again if an eye is open
	if (u.revealed("s_dest_sid_eye") || u.revealed("s_dest_pass_eye")) && app.Pending != "dest_reveal" {
		u.app().Do(AppAction{What: "dest_reveal"})
	}
}

func (u *UI) destEdit() *destState {
	u.dst.dirtyAt = u.now()
	return &u.dst
}

func intp(v int) *int    { return &v }
func boolp(v bool) *bool { return &v }

func (u *UI) destTouch(what string) bool {
	app := u.app().AppSnapshot()
	dest := app.Dest
	if dest == nil {
		return false
	}
	n := u.destNow(dest)
	st := u.Tx.Stream
	switch what {
	case "s_dest_sid_eye", "s_dest_pass_eye":
		// Opened: the saved ones, asked if the app has not given them (or a reveal failed, or they were dropped)
		if u.toggleReveal(what) && app.DestSecrets == nil && app.Pending != "dest_reveal" {
			u.appBusy(u.app().Do(AppAction{What: "dest_reveal"}))
		}
	case "s_dest_url":
		u.openSpecKeyboard(&kbSpec{title: st.DestURL, help: st.DestURLHelp, max: DestURLMax,
			done: func(u *UI, t string, _ []byte) string {
				u.destEdit().url = &t
				return ""
			}}, n.url)
	case "s_dest_sid", "s_dest_pass":
		pass := what == "s_dest_pass"
		sp := &kbSpec{title: st.DestStreamID, help: st.DestStreamIDHelp, secret: true, max: StreamIDMax,
			done: func(u *UI, _ string, b []byte) string {
				dr := u.destEdit()
				if pass {
					if len(b) > 0 && (len(b) < PassphraseMin || len(b) > PassphraseMax) {
						return sprintf(st.DestPassphraseBadF, PassphraseMin, PassphraseMax)
					}
					clear(dr.pass)
					dr.pass, dr.clearPass = append([]byte(nil), b...), false
					if len(b) == 0 {
						dr.pass, dr.clearPass = nil, dest.PassphraseSet
					}
					return ""
				}
				clear(dr.sid)
				dr.sid, dr.clearSID = append([]byte(nil), b...), false
				if len(b) == 0 {
					dr.sid, dr.clearSID = nil, dest.StreamIDSet
				}
				return ""
			}}
		if pass {
			sp.title, sp.help, sp.max = st.DestPassphrase, st.DestPassphraseHelp, PassphraseMax
		}
		u.openSpecKeyboard(sp, "")
	case "s_dest_lat_m", "s_dest_lat_p":
		u.destEdit().latency = intp(stepIn(n.latency, LatencyStep, LatencyMin, LatencyMax, dir(what)))
	case "s_dest_aud_m", "s_dest_aud_p":
		u.destEdit().aud = intp(stepList(n.aud, AudioRates, dir(what)))
	case "s_dest_vid_m", "s_dest_vid_p":
		v := stepIn(n.video, VideoStep, VideoMin, VideoMax, dir(what))
		dr := u.destEdit()
		dr.video = intp(v)
		if n.abr && n.vm > v { // the minimum never above the maximum
			dr.vmin = intp(v)
		}
	case "s_dest_vmin_m", "s_dest_vmin_p":
		u.destEdit().vmin = intp(stepIn(n.vm, VideoMinStep, VideoMinMin, n.video, dir(what)))
	case "s_dest_abr":
		u.destEdit().abr = boolp(!n.abr)
	case "s_dest_save":
		u.destSave(false)
	case "s_dest_apply":
		switch {
		case u.dst.dirty():
			if u.streaming() && !u.confirmTap("dest_apply") {
				return true
			}
			u.destSave(true)
		case dest.PendingRestart:
			if u.streaming() && !u.confirmTap("dest_restart") {
				return true
			}
			u.dst.savedAt = u.now().Add(-time.Millisecond)
			u.appBusy(u.app().Do(AppAction{What: "restart"}))
		}
	default:
		return false
	}
	return true
}

// dir is −1 for a key ending in _m (minus), +1 for _p.
func dir(key string) int {
	if strings.HasSuffix(key, "_m") {
		return -1
	}
	return 1
}

// destSave sends the draft: only what changed; the typed secrets go as secrets (Do copies them) and are wiped here.
func (u *UI) destSave(apply bool) {
	app := u.app().AppSnapshot()
	dr := &u.dst
	if !dr.dirty() || app.Pending == "dest_save" {
		return
	}
	body := map[string]any{"apply": apply}
	if dr.url != nil {
		body["url"] = strings.TrimSpace(*dr.url)
	}
	for k, v := range map[string]*int{"latencyMs": dr.latency, "videoBitrateKbps": dr.video,
		"audioBitrateKbps": dr.aud, "videoMinBitrateKbps": dr.vmin} {
		if v != nil {
			body[k] = *v
		}
	}
	if dr.abr != nil {
		body["adaptiveBitrate"] = *dr.abr
	}
	sec := map[string][]byte{}
	if dr.clearSID {
		body["streamId"] = nil
	} else if dr.sid != nil {
		sec["streamId"] = dr.sid
	}
	if dr.clearPass {
		body["passphrase"] = nil
	} else if dr.pass != nil {
		sec["passphrase"] = dr.pass
	}
	dr.savedAt, dr.sentAt, dr.sentApply = u.now().Add(-time.Millisecond), u.now(), apply
	// The client copies the secrets; the draft keeps its own until the app saves them (destSync wipes it then), so a
	// refused save (a wrong URL…) does not make the user type a long key again. The page going wipes them too.
	u.appBusy(u.app().Do(AppAction{What: "dest_save", Body: body, Secrets: sec}))
}

var itoa = strconv.Itoa
