package appapi

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The device API's client (CONTRACT §1-§5) against the fake app.

func deviceBody(lang string) map[string]any {
	return map[string]any{"language": lang, "languages": []any{"es", "en", "fr"}, "name": "Estudio 2",
		"hostname": "estudio-2", "timezone": "Europe/Madrid",
		"time":    map[string]any{"now": "2026-10-14T03:04:05+02:00", "ntp": true, "synced": true},
		"version": map[string]any{"release": "0.1.0", "app": "0.1.0", "engine": "0.1.0"}}
}

func newDevice(t *testing.T) (*fakeApp, *DeviceState) {
	t.Helper()
	a := newFakeApp(t)
	d := NewDeviceState(nil, a.sock, nil)
	d.readFile = func(string) ([]byte, error) { return nil, errors.New("no file") }
	d.addrs = func() []string { return []string{"192.168.1.20"} }
	return a, d
}

func TestDevicePoll(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /device", 200, deviceBody("fr"))
	if d.lang.get() != "en" {
		t.Fatal("not English before the app answers")
	}
	d.PollDevice()
	s := d.AppSnapshot()
	dev := s.Device
	if s.DeviceAPI != ui.Yes || dev == nil || dev.Language != "fr" || dev.Name != "Estudio 2" || dev.Hostname != "estudio-2" ||
		dev.Timezone != "Europe/Madrid" || !dev.NTP || !dev.Synced || dev.Release != "0.1.0" ||
		!reflect.DeepEqual(dev.Languages, []string{"es", "en", "fr"}) {
		t.Fatalf("%+v %+v", s, dev)
	}
	if want := time.Date(2026, 10, 14, 1, 4, 5, 0, time.UTC); !dev.Now.Equal(want) {
		t.Errorf("now %v", dev.Now)
	}
	// The device language goes in Accept-Language from now on (the app's messages come back in it)
	if d.lang.get() != "fr" {
		t.Error("lang", d.lang.get())
	}
	d.PollDevice()
	if a.lastLang() != "fr" {
		t.Errorf("Accept-Language %q", a.lastLang())
	}
	// Odd data is left out: an unknown language, a hostname with capitals, a bad zone
	b := deviceBody("de")
	b["hostname"], b["timezone"] = "Estudio", "../../etc"
	a.route("GET /device", 200, b)
	d.PollDevice()
	if dev := d.AppSnapshot().Device; dev == nil || dev.Language != "" || dev.Hostname != "" || dev.Timezone != "" || dev.Name != "Estudio 2" {
		t.Errorf("%+v", dev)
	}
	b["name"] = strings.Repeat("x", 41)
	d.PollDevice()
	if dev := d.AppSnapshot().Device; dev.Name != "Estudio 2" { // nothing of a device left: the last one stays
		t.Errorf("%+v", dev)
	}
	if d.lang.get() != "fr" { // an unknown language does not replace the last one
		t.Error("lang", d.lang.get())
	}
	// A passing failure keeps the last one; an older app (403/404) is No
	a.route("GET /device", 500, map[string]any{"error": "internal"})
	d.PollDevice()
	if s := d.AppSnapshot(); s.Device == nil || s.DeviceAPI != ui.Yes {
		t.Errorf("after a 500: %+v", s)
	}
	a.drop("GET /device") // the fake answers 403 local_forbidden, like an app without the route
	d.PollDevice()
	if s := d.AppSnapshot(); s.Device != nil || s.DeviceAPI != ui.No {
		t.Errorf("an older app: %+v", s)
	}
	// No socket at all: No too
	g := NewDeviceState(nil, filepath.Join(a.dir, "no"), nil)
	g.PollDevice()
	g.PollSetup(ui.AppWatch{})
	if s := g.AppSnapshot(); s.DeviceAPI != ui.No || s.SetupAPI != ui.No {
		t.Errorf("no socket: %+v", s)
	}
	if g.Do(ui.AppAction{What: "language", Value: "es"}) || g.SetupPassword([]byte("x")) {
		t.Error("an action with no app")
	}
}

func TestSetupStateAndCode(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /setup", 200, map[string]any{"done": false, "needsPassword": true, "codeRequired": true,
		"steps": []any{map[string]any{"id": "language", "state": "done"}, map[string]any{"id": "time", "state": "todo"},
			map[string]any{"id": "bogus", "state": "done"}, map[string]any{"id": "name", "state": "maybe"}}})
	reads := 0
	d.readFile = func(p string) ([]byte, error) {
		if p != SetupCodeFile {
			return nil, errors.New("no")
		}
		reads++
		return []byte("ABCD2345\n"), nil
	}
	d.PollSetup(ui.AppWatch{})
	s := d.AppSnapshot().Setup
	if s == nil || s.Done || !s.NeedsPassword || !s.CodeRequired || s.Code != "" || reads != 0 {
		t.Fatalf("%+v (reads %d)", s, reads)
	}
	if !reflect.DeepEqual(s.Steps, []ui.SetupStep{{ID: "language", State: "done"}, {ID: "time", State: "todo"}}) {
		t.Errorf("steps %+v", s.Steps)
	}
	if s.StepState("language") != "done" || s.StepState("cloud") != "" {
		t.Error("StepState")
	}
	// With the wizard on the screen, the code (to finish from the web) is read; it leaves when the wizard does
	d.Watch(ui.AppWatch{Setup: true})
	d.PollSetup(ui.AppWatch{Setup: true})
	if c := d.AppSnapshot().Setup.Code; c != "ABCD2345" || reads != 1 {
		t.Fatalf("code %q, reads %d", c, reads)
	}
	d.Watch(ui.AppWatch{})
	if c := d.AppSnapshot().Setup.Code; c != "" {
		t.Errorf("the code stays: %q", c)
	}
	// A code that is not one, or a password already set: none
	d.readFile = func(string) ([]byte, error) { return []byte("abcd2345"), nil }
	d.PollSetup(ui.AppWatch{Setup: true})
	if c := d.AppSnapshot().Setup.Code; c != "" {
		t.Errorf("bad code %q", c)
	}
	a.route("GET /setup", 200, map[string]any{"done": false, "needsPassword": false, "code": "ZZZZ9999"})
	d.PollSetup(ui.AppWatch{Setup: true})
	if c := d.AppSnapshot().Setup.Code; c != "" {
		t.Errorf("code without a password to set: %q", c)
	}
	// An app that gives the code itself over the socket
	a.route("GET /setup", 200, map[string]any{"done": false, "needsPassword": true, "code": "ZZZZ9999"})
	d.PollSetup(ui.AppWatch{Setup: true})
	if c := d.AppSnapshot().Setup.Code; c != "ZZZZ9999" {
		t.Errorf("the app's code: %q", c)
	}
	a.route("GET /setup", 200, map[string]any{"steps": []any{}}) // no "done": not a setup state
	d.PollSetup(ui.AppWatch{})
	if d.AppSnapshot().Setup == nil {
		t.Error("an odd answer replaced the last one")
	}
	a.drop("GET /setup")
	d.PollSetup(ui.AppWatch{})
	if s := d.AppSnapshot(); s.Setup != nil || s.SetupAPI != ui.No {
		t.Errorf("an older app: %+v", s)
	}
}

func TestSetupPasswordSecret(t *testing.T) {
	a, d := newDevice(t)
	a.route("POST /setup/password", 204, nil)
	pw := []byte(`Secreto"10\chars`)
	if !d.SetupPassword(pw) {
		t.Fatal("queued")
	}
	clear(pw) // the caller wipes its own at once: the client has its copy
	q, _, _, snap := d.take()
	if q == nil || q.a.What != "setup_password" || snap.Pending != "setup_password" {
		t.Fatalf("%+v %+v", q, snap)
	}
	if strings.Contains(fmt.Sprintf("%+v %v %#v", d.AppSnapshot(), q, q), "Secreto") {
		t.Fatal("the password shows")
	}
	kept := q.s.b
	d.runAction(*q)
	a.mu.Lock()
	raw := string(a.raws[len(a.raws)-1])
	a.mu.Unlock()
	if raw != `{"password":"Secreto\"10\\chars"}` {
		t.Errorf("body %s", raw)
	}
	for _, c := range kept {
		if c != 0 {
			t.Fatal("the password is still in memory")
		}
	}
	s := d.AppSnapshot()
	if s.Last == nil || !s.Last.OK || s.Last.What != "setup_password" || s.Pending != "" {
		t.Errorf("%+v", s.Last)
	}
	// The app refuses (a password exists): the result says so, the password goes anyway
	a.route("POST /setup/password", 409, map[string]any{"error": "password_exists", "message": "ya hay contraseña"})
	d.SetupPassword([]byte("Otra-0123456"))
	q, _, _, _ = d.take()
	kept = q.s.b
	d.runAction(*q)
	if l := d.AppSnapshot().Last; l.OK || l.Status != 409 || l.Code != "password_exists" || l.Message != "ya hay contraseña" {
		t.Errorf("%+v", l)
	}
	for _, c := range kept {
		if c != 0 {
			t.Fatal("the refused password is still in memory")
		}
	}
	// Stopping drops what is queued, and wipes it
	d.SetupPassword([]byte("Tercera-123456"))
	d.mu.Lock()
	kept = d.queue[0].s.b
	d.mu.Unlock()
	d.Stop()
	for _, c := range kept {
		if c != 0 {
			t.Fatal("a queued password stays after Stop")
		}
	}
}

func TestDeviceActions(t *testing.T) {
	a, d := newDevice(t)
	a.route("PUT /device", 200, deviceBody("es"))
	a.route("POST /setup/step", 204, nil)
	run := func(act ui.AppAction) *ui.AppResult {
		t.Helper()
		if !d.Do(act) {
			t.Fatalf("not queued: %+v", act)
		}
		q, _, _, _ := d.take()
		d.runAction(*q)
		return d.AppSnapshot().Last
	}
	// The language: the PUT already asks for its answer in the new language
	if r := run(ui.AppAction{What: "language", Value: "es"}); !r.OK {
		t.Fatalf("%+v", r)
	}
	if got := a.last(); !reflect.DeepEqual(got, seenReq{"PUT", "/api/v1/device", map[string]any{"language": "es"}}) ||
		a.lastLang() != "es" {
		t.Errorf("%+v %q", got, a.lastLang())
	}
	if d.AppSnapshot().Device.Language != "es" || d.lang.get() != "es" {
		t.Error("the answer is not the device")
	}
	when := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	for _, c := range []struct {
		act  ui.AppAction
		want map[string]any
	}{
		{ui.AppAction{What: "name", Value: "Estudio 2"}, map[string]any{"name": "Estudio 2"}},
		{ui.AppAction{What: "timezone", Value: "America/Bogota"}, map[string]any{"timezone": "America/Bogota"}},
		{ui.AppAction{What: "ntp", On: false}, map[string]any{"ntp": false}},
		{ui.AppAction{What: "time", Time: when}, map[string]any{"ntp": false, "time": "2026-10-14T09:30:00Z"}},
	} {
		run(c.act)
		if got := a.last(); got.method != "PUT" || !reflect.DeepEqual(got.body, c.want) {
			t.Errorf("%+v: %+v", c.act, got)
		}
	}
	run(ui.AppAction{What: "setup_step", Value: "network", State: "skipped"})
	if got := a.last(); !reflect.DeepEqual(got, seenReq{"POST", "/api/v1/setup/step",
		map[string]any{"id": "network", "state": "skipped"}}) {
		t.Errorf("%+v", got)
	}
	// Validation: the field errors, as strings or with their code
	a.route("PUT /device", 400, map[string]any{"error": "validation", "message": "revisa los campos",
		"fields": map[string]any{"name": "demasiado largo", "x": map[string]any{"code": "invalid", "message": "mal"}}})
	r := run(ui.AppAction{What: "name", Value: "x"})
	if r.OK || r.Status != 400 || r.Code != "validation" || r.Fields["name"].Message != "demasiado largo" ||
		r.Fields["x"] != (ui.AppFieldError{Code: "invalid", Message: "mal"}) {
		t.Errorf("%+v", r)
	}
	// Unknown actions and a full queue
	if d.Do(ui.AppAction{What: "reboot"}) || d.Do(ui.AppAction{What: "setup_password"}) {
		t.Error("an unknown action")
	}
	for range queueMax {
		d.Do(ui.AppAction{What: "update_check"})
	}
	if d.Do(ui.AppAction{What: "update_check"}) {
		t.Error("the queue has no end")
	}
}

func TestUpdateInfo(t *testing.T) {
	a, d := newDevice(t)
	d.lang.set("fr")
	a.route("GET /update", 200, map[string]any{"current": "0.1.0", "state": "available", "progress": 0,
		"lastCheck": "2026-10-14T02:00:00Z", "available": map[string]any{"version": "0.2.0", "size": 12345678,
			"notes": map[string]any{"en": "Better Wi-Fi", "fr": "Meilleur Wi-Fi\nEt plus\x07", "es": "Mejor wifi"}}})
	d.PollUpdate()
	u := d.AppSnapshot().Update
	if u == nil || u.State != "available" || u.Current != "0.1.0" || u.Available == nil || u.Available.Version != "0.2.0" ||
		u.Available.Size != 12345678 || u.Available.Notes != "Meilleur Wi-Fi\nEt plus" || u.LastCheck == nil {
		t.Fatalf("%+v %+v", u, u.Available)
	}
	if got := notesText(map[string]any{"en": "Hi"}, "es"); got != "Hi" {
		t.Errorf("fallback to English: %q", got)
	}
	if got := notesText("Plain", "es"); got != "Plain" {
		t.Errorf("a string: %q", got)
	}
	// The app's additive fields: the message of the error, whether «Update» can be pressed and why not, the
	// installer's last run, the versions and a test build
	a.route("GET /update", 200, map[string]any{"current": "0.1.0", "state": "error", "error": "network",
		"message": "no se puede contactar con el servidor de actualizaciones", "canInstall": false, "blocked": "streaming",
		"blockedMessage": "deja de emitir para actualizar el codificador", "testBuild": true,
		"lastResult": map[string]any{"state": "rolled_back", "from": "0.1.0", "to": "0.2.0", "error": "unhealthy",
			"message": "la versión nueva no arrancó bien", "at": "2026-10-14T05:00:00Z"},
		"versions": map[string]any{"release": "0.1.0", "app": "0.1.0", "engine": "0.1.0", "commit": "0123abcd"}})
	d.PollUpdate()
	u = d.AppSnapshot().Update
	if u.Message != "no se puede contactar con el servidor de actualizaciones" || u.CanInstall == nil || *u.CanInstall ||
		u.Blocked != "streaming" || u.BlockedMessage == "" || !u.TestBuild || u.Versions.Commit != "0123abcd" ||
		u.LastResult == nil || u.LastResult.State != "rolled_back" || u.LastResult.To != "0.2.0" ||
		u.LastResult.Message != "la versión nueva no arrancó bien" || u.LastResult.At.IsZero() {
		t.Fatalf("%+v %+v", u, u.LastResult)
	}
	a.route("GET /update", 200, map[string]any{"state": "idle", "blocked": "whatever",
		"lastResult": map[string]any{"state": "exploded"}, "versions": map[string]any{"commit": "../x"}})
	d.PollUpdate()
	if u := d.AppSnapshot().Update; u.CanInstall != nil || u.Blocked != "" || u.LastResult != nil || u.Versions.Commit != "" {
		t.Errorf("odd fields kept: %+v", u)
	}
	a.route("GET /update", 200, map[string]any{"state": "installing", "progress": 0.5})
	d.PollUpdate()
	if u := d.AppSnapshot().Update; u.State != "installing" || u.Progress != 0.5 || u.Available != nil {
		t.Errorf("%+v", u)
	}
	a.route("GET /update", 200, map[string]any{"state": "exploded"})
	d.PollUpdate()
	if u := d.AppSnapshot().Update; u.State != "installing" {
		t.Errorf("an unknown state replaced it: %+v", u)
	}
	a.route("POST /update/install", 409, map[string]any{"error": "streaming", "message": "Deja de emitir antes"})
	d.Do(ui.AppAction{What: "update_install"})
	q, _, _, _ := d.take()
	d.runAction(*q)
	if l := d.AppSnapshot().Last; l.OK || l.Code != "streaming" || l.Message != "Deja de emitir antes" {
		t.Errorf("%+v", l)
	}
	a.drop("GET /update")
	d.PollUpdate()
	if s := d.AppSnapshot(); s.UpdateAPI != ui.No || s.Update != nil {
		t.Errorf("older app %+v", s)
	}
}

func TestSlots(t *testing.T) {
	a, d := newDevice(t)
	cam := "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0001-video-index0"
	body := map[string]any{"engineSupports": true, "slots": []any{
		map[string]any{"slot": 1, "assign": "auto", "alias": "Cámara principal",
			"current": map[string]any{"id": "hdmi", "name": "HDMI", "kind": "hdmi-csi", "state": "live"}},
		map[string]any{"slot": 2, "assign": cam, "alias": "", "current": nil},
		map[string]any{"slot": 3, "assign": "net:cam1", "alias": strings.Repeat("a", 25)}, // too long: out
		map[string]any{"slot": 2, "assign": "off", "alias": ""},                           // repeated: out
		map[string]any{"slot": 4, "assign": "usb:x", "alias": ""},                         // not an id: out
		map[string]any{"slot": 4, "assign": "off", "alias": "Fuera"}},
		"candidates": []any{map[string]any{"id": "hdmi", "name": "HDMI", "kind": "hdmi-csi", "connected": true},
			map[string]any{"id": cam, "name": "Cam Link 4K", "kind": "usb", "connected": false},
			map[string]any{"id": "auto", "name": "x"}}}
	a.route("GET /sources/slots", 200, body)
	d.PollSlots()
	s := d.AppSnapshot().Slots
	if s == nil || !s.EngineSupports || len(s.Slots) != 3 || len(s.Candidates) != 2 {
		t.Fatalf("%+v", s)
	}
	if s.Slots[0].Alias != "Cámara principal" || s.Slots[0].Current == nil || s.Slots[0].Current.ID != "hdmi" ||
		s.Slots[1].Assign != cam || s.Slots[1].Current != nil || s.Slots[2].Slot != 4 || s.Candidates[1].Connected {
		t.Errorf("%+v", s.Slots)
	}
	// Save: all of them; the app's answer is the new state, with whether the engine took it now
	put := map[string]any{"applied": false}
	for k, v := range body {
		put[k] = v
	}
	a.route("PUT /sources/slots", 200, put)
	want := []ui.SlotAssign{{Slot: 1, Assign: "auto", Alias: "Principal"}, {Slot: 2, Assign: "off"},
		{Slot: 3, Assign: "auto"}, {Slot: 4, Assign: "auto"}}
	d.Do(ui.AppAction{What: "slots", Slots: want})
	q, _, _, _ := d.take()
	d.runAction(*q)
	if l := d.AppSnapshot().Last; !l.OK || l.Applied == nil || *l.Applied {
		t.Errorf("applied: %+v", l)
	}
	got := a.last()
	if got.method != "PUT" || got.uri != "/api/v1/sources/slots" ||
		fmt.Sprint(got.body) != "map[slots:[map[alias:Principal assign:auto slot:1] map[alias: assign:off slot:2] map[alias: assign:auto slot:3] map[alias: assign:auto slot:4]]]" {
		t.Errorf("%+v", got)
	}
	// On air: the app's message, in the device language
	a.route("PUT /sources/slots", 409, map[string]any{"error": "on_air", "message": "Stop streaming to move the source on air"})
	d.Do(ui.AppAction{What: "slots", Slots: want})
	q, _, _, _ = d.take()
	d.runAction(*q)
	if l := d.AppSnapshot().Last; l.OK || l.Code != "on_air" || !strings.Contains(l.Message, "Stop streaming") {
		t.Errorf("%+v", l)
	}
	a.route("PUT /sources/slots", 400, map[string]any{"error": "validation",
		"fields": map[string]any{"slots.2.assign": map[string]any{"code": "duplicate", "message": "ya está en la fuente 1"}}})
	d.Do(ui.AppAction{What: "slots", Slots: want})
	q, _, _, _ = d.take()
	d.runAction(*q)
	if l := d.AppSnapshot().Last; l.Fields["slots.2.assign"].Code != "duplicate" {
		t.Errorf("%+v", l)
	}
	// The app's own shape: messages in fields, codes apart
	a.route("PUT /sources/slots", 400, map[string]any{"error": "validation",
		"fields": map[string]any{"slots.4.assign": "that source is already pinned to source 2"},
		"codes":  map[string]any{"slots.4.assign": "duplicate", "slots.1.alias": "invalid"}})
	d.Do(ui.AppAction{What: "slots", Slots: want})
	q, _, _, _ = d.take()
	d.runAction(*q)
	l := d.AppSnapshot().Last
	if l.Fields["slots.4.assign"] != (ui.AppFieldError{Code: "duplicate", Message: "that source is already pinned to source 2"}) ||
		l.Fields["slots.1.alias"].Code != "invalid" {
		t.Errorf("%+v", l.Fields)
	}
}

func TestZonesAndCloud(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /device/timezones", 200, map[string]any{"zones": []any{map[string]any{"id": "Europe/Madrid", "offset": "+02:00"},
		map[string]any{"id": "UTC", "offset": "+00:00"}, map[string]any{"id": "../etc/passwd"},
		map[string]any{"id": "America/Argentina/Buenos_Aires", "offset": "x"}}})
	d.PollZones()
	if z := d.AppSnapshot().Zones; !reflect.DeepEqual(z, []ui.TimeZone{{ID: "Europe/Madrid", Offset: "+02:00"},
		{ID: "UTC", Offset: "+00:00"}, {ID: "America/Argentina/Buenos_Aires"}}) {
		t.Errorf("%+v", z)
	}
	exp := "2026-10-14T03:20:00Z"
	a.route("GET /cloud", 200, map[string]any{"state": "pairing", "code": "ABCD-EFGH", "expiresAt": exp,
		"linkUrl": "https://app.enhancedirl.com/encoders/link?pairing=ABCD-EFGH"})
	d.PollCloud()
	c := d.AppSnapshot().Cloud
	if c == nil || c.State != "pairing" || c.Code != "ABCD-EFGH" || c.ExpiresAt == nil || len(c.LinkQR) < 21 ||
		len(c.LinkQR[0]) != len(c.LinkQR) {
		t.Fatalf("%+v", c)
	}
	a.route("GET /cloud", 200, map[string]any{"state": "pairing", "code": "ABCD-EFGH", "linkUrl": "javascript:alert(1)"})
	d.PollCloud()
	if c := d.AppSnapshot().Cloud; c.LinkURL != "" || c.LinkQR != nil {
		t.Errorf("a link that is not https: %+v", c)
	}
	a.route("GET /cloud", 503, map[string]any{"error": "cloud_disabled", "message": "desactivada"})
	d.PollCloud()
	if c := d.AppSnapshot().Cloud; c == nil || c.State != "disabled" {
		t.Errorf("%+v", c)
	}
	a.route("POST /cloud/pair", 200, map[string]any{"state": "pairing", "code": "WXYZ-2345",
		"linkUrl": "https://app.enhancedirl.com/encoders/link?pairing=WXYZ-2345"})
	d.Do(ui.AppAction{What: "cloud_pair"})
	q, _, _, _ := d.take()
	d.runAction(*q)
	if c := d.AppSnapshot().Cloud; c.Code != "WXYZ-2345" || a.last().uri != "/api/v1/cloud/pair" {
		t.Errorf("%+v %+v", c, a.last())
	}
	// About: the commit of the build stamp and the addresses
	d.readFile = func(p string) ([]byte, error) {
		if p == BuildFile {
			return []byte("commit=0123abcd date=2026-10-14T00:00:00Z\n"), nil
		}
		return nil, errors.New("no")
	}
	d.pollAbout(ui.AppWatch{})
	if s := d.AppSnapshot(); s.Commit != "0123abcd" || !reflect.DeepEqual(s.IPs, []string{"192.168.1.20"}) {
		t.Errorf("%+v %+v", s.Commit, s.IPs)
	}
}

// The goroutine: it asks by itself, follows Watch, runs what is queued in order and tells.
func TestDeviceGoroutine(t *testing.T) {
	a, d := newDevice(t)
	var hits atomic.Int32
	d.changed = func() { hits.Add(1) }
	a.route("GET /device", 200, deviceBody("es"))
	a.route("GET /setup", 200, map[string]any{"done": true, "needsPassword": false})
	a.route("PUT /device", 200, deviceBody("fr"))
	a.route("GET /update", 200, map[string]any{"state": "idle", "current": "0.1.0"})
	d.Start()
	defer d.Stop()
	wait := func(what string, ok func(ui.AppSnapshot) bool) {
		t.Helper()
		for t0 := time.Now(); time.Since(t0) < 3*time.Second; time.Sleep(10 * time.Millisecond) {
			if ok(d.AppSnapshot()) {
				return
			}
		}
		t.Fatalf("%s: %+v", what, d.AppSnapshot())
	}
	wait("the first answers", func(s ui.AppSnapshot) bool {
		return s.Device != nil && s.Setup != nil && s.Setup.Done && s.Update != nil
	})
	d.Do(ui.AppAction{What: "language", Value: "fr"})
	wait("the language", func(s ui.AppSnapshot) bool { return s.Device.Language == "fr" && s.Pending == "" })
	if hits.Load() == 0 {
		t.Error("changed never called")
	}
	// Slots only while the page is open
	a.route("GET /sources/slots", 200, map[string]any{"slots": []any{}, "engineSupports": false})
	for _, s := range a.allSeen() {
		if strings.HasSuffix(s.uri, "/sources/slots") {
			t.Fatal("slots asked without the page")
		}
	}
	d.Watch(ui.AppWatch{Slots: true})
	wait("the slots", func(s ui.AppSnapshot) bool { return s.Slots != nil && !s.Slots.EngineSupports })
}
