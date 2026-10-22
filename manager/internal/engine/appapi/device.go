package appapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"rsc.io/qr"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The device API's cadence (CONTRACT §1-§5): with nothing open, only the device (its language) and the setup
// state (whether the wizard must show); the rest only while its page is on the screen.
const (
	DeviceSlow = 30 * time.Second // the device and the setup state, with nothing open
	DeviceFast = 2 * time.Second  // the device with Settings or the wizard open (the clock, the language)
	SetupFast  = 3 * time.Second  // the setup state with the wizard open (finished from the web?)
	UpdateSlow = 5 * time.Minute  // the «update available» dot
	UpdateFast = 2 * time.Second  // the Updates page
	SlotsEvery = 2 * time.Second  // the Sources page
	CloudEvery = 3 * time.Second  // the wizard's link step (like the web with a code pending)
	AboutEvery = 10 * time.Second // the About page: the IP addresses
	GoneEvery  = 5 * time.Minute  // a route an older app does not have: asked again rarely
	ZonesRetry = 10 * time.Second
	// ActionTimeout: PUT /device may wait for timedated/hostnamed; the POSTs answer at once
	ActionTimeout = 30 * time.Second
	queueMax      = 8
)

// SetupCodeFile is the setup code (CONTRACT §2.1): root:eirl-encoder 0640, so the engine (its group) can show it.
const SetupCodeFile = "/etc/eirl-encoder/setup-code"

// BuildFile is the installed build's stamp (CONTRACT §4): commit=<sha> date=<RFC3339>.
const BuildFile = "/usr/lib/eirl-encoder/BUILD"

var (
	reSetupCode = regexp.MustCompile(`^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{8}$`)
	reZone      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+){0,2}$`)
	reOffset    = regexp.MustCompile(`^[+-][0-9]{2}:[0-9]{2}$`)
	reHostname  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	reCommit    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	reSourceID  = regexp.MustCompile(`^(auto|off|hdmi|test|net:cam[1-8]|/dev/v4l/by-(id|path)/[A-Za-z0-9_:.-]{1,200})$`)
	reStepState = set("todo", "done", "skipped")
	setupSteps  = set("language", "time", "network", "password", "name", "cloud")
	updStates   = set("idle", "checking", "available", "downloading", "ready", "installing", "rolled_back", "error")
	updBlocked  = set("streaming", "not_ready", "busy")
	runStates   = set("installed", "rolled_back", "failed", "refused", "installing")
	sysStates   = set("ok", "failed", "skipped")
	sysAreas    = set("hdmi", "bluetooth", "uplinks", "system")
	updWaits    = set("metered")
	cloudStates = set("unlinked", "pairing", "linked")
	// the actions and their route; "device" ones are PUT /device with one field
	actionRoutes = map[string][2]string{
		"language": {"PUT", "/device"}, "name": {"PUT", "/device"}, "timezone": {"PUT", "/device"},
		"ntp": {"PUT", "/device"}, "time": {"PUT", "/device"},
		"setup_step": {"POST", "/setup/step"}, "setup_password": {"POST", "/setup/password"},
		"setup_complete": {"POST", "/setup/complete"}, "setup_restart": {"POST", "/setup/restart"},
		"update_check": {"POST", "/update/check"}, "update_install": {"POST", "/update/install"},
		"update_reboot": {"POST", "/update/reboot"},
		"slots":         {"PUT", "/sources/slots"}, "cloud_pair": {"POST", "/cloud/pair"},
	}
)

type queued struct {
	a       ui.AppAction // its Secrets nil: they are in secrets
	s       secret       // setup_password only
	secrets map[string]secret
	gen     uint64 // the secrets' generation when it was queued (a reveal after DropSecrets is not kept)
}

// wipe zeroes every secret of the action.
func (q *queued) wipe() {
	q.s.wipe()
	for k, s := range q.secrets {
		s.wipe()
		delete(q.secrets, k)
	}
}

// DeviceState is the app's device API for the screen (ui.App): the device (language, name, time zone, clock,
// versions), the first-boot setup, the updates, the source slots and the link to EnhancedIRL, over the same local
// socket as NetState. Its own goroutine asks only for what the screen shows (Watch) and runs the actions one at a
// time, in order (Do). The web password of the wizard (SetupPassword) is kept only until it is sent, apart from
// what the screen reads, wiped after, and never logged; so is the setup code it shows (only while needed).
type DeviceState struct {
	changed func()
	sock    string
	lang    *language

	getTimeout, actTimeout time.Duration
	readFile               func(string) ([]byte, error) // tests replace it
	addrs                  func() []string

	ctx     context.Context
	cancel  context.CancelFunc
	wake    chan struct{}
	done    chan struct{}
	started atomic.Bool

	mu    sync.Mutex
	snap  ui.AppSnapshot
	watch ui.AppWatch
	queue []queued
	kick  bool
	// secretGen moves on every DropSecrets (and when what was revealed stops being true: a new key): a reveal queued
	// before it does not bring its secrets back
	secretGen uint64
	// destsRetry: the Destination page was opened again, GET /destinations is asked again (an app updated since)
	destsRetry bool
}

var _ ui.App = (*DeviceState)(nil)

// NewDeviceState makes the device API's client for the app at sock ("" = Sock). changed may be nil; lang is the
// language it shares with a NetState (nil: its own). NetState makes one: an engine only needs NetState.
func NewDeviceState(changed func(), sock string, lang *language) *DeviceState {
	if sock == "" {
		sock = Sock
	}
	if changed == nil {
		changed = func() {}
	}
	if lang == nil {
		lang = &language{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &DeviceState{changed: changed, sock: sock, lang: lang, getTimeout: GetTimeout, actTimeout: ActionTimeout,
		readFile: os.ReadFile, addrs: boardAddrs, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
		done: make(chan struct{}), kick: true}
}

// Start runs the goroutine (once).
func (d *DeviceState) Start() {
	if d.started.CompareAndSwap(false, true) {
		go d.run()
	}
}

// Stop ends it (cancelling a request in flight) and waits; queued actions and their secrets are dropped.
func (d *DeviceState) Stop() {
	d.cancel()
	if d.started.Load() {
		<-d.done
	}
	d.mu.Lock()
	d.dropQueue()
	d.mu.Unlock()
}

func (d *DeviceState) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// AppSnapshot is the state for the screen.
func (d *DeviceState) AppSnapshot() ui.AppSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snap
}

// Watch: what the screen shows. Something new on it is asked at once; the setup code leaves memory when the wizard
// is not shown.
func (d *DeviceState) Watch(w ui.AppWatch) {
	d.mu.Lock()
	old := d.watch
	d.watch = w
	if !w.Setup && d.snap.Setup != nil && d.snap.Setup.Code != "" {
		s := *d.snap.Setup
		s.Code = ""
		d.snap.Setup = &s
	}
	if w.Dest && !old.Dest && d.snap.DestsAPI == ui.No {
		d.destsRetry = true
	}
	if (w.Device && !old.Device) || (w.Setup && !old.Setup) || (w.Update && !old.Update) || (w.Slots && !old.Slots) ||
		(w.Zones && !old.Zones) || (w.Cloud && !old.Cloud) || (w.About && !old.About) || (w.Dest && !old.Dest) ||
		(w.Status && !old.Status) || (w.Sources && !old.Sources) || (w.Bluetooth && !old.Bluetooth) {
		d.kick = true
	}
	d.mu.Unlock()
	d.poke()
}

// Do queues an action: false if the queue is full, the action is unknown, or there is no app at all. Its Secrets
// are copied here (the caller wipes its own right after).
func (d *DeviceState) Do(a ui.AppAction) bool {
	if _, ok := actionRoutes[a.What]; !ok || a.What == "setup_password" {
		return false
	}
	q := queued{a: a}
	if len(a.Secrets) > 0 {
		q.secrets = make(map[string]secret, len(a.Secrets))
		for k, v := range a.Secrets {
			q.secrets[k] = newSecret(v)
		}
	}
	q.a.Secrets = nil
	return d.enqueue(q)
}

// SetupPassword queues the web password (copied; the caller wipes its own).
func (d *DeviceState) SetupPassword(password []byte) bool {
	if len(password) == 0 {
		return false
	}
	return d.enqueue(queued{a: ui.AppAction{What: "setup_password"}, s: newSecret(password)})
}

func (d *DeviceState) enqueue(q queued) bool {
	d.mu.Lock()
	gone := d.snap.DeviceAPI == ui.No && d.snap.SetupAPI == ui.No && d.snap.Device == nil
	if len(d.queue) >= queueMax || gone {
		d.mu.Unlock()
		q.wipe()
		return false
	}
	q.gen = d.secretGen
	d.queue = append(d.queue, q)
	if d.snap.Pending == "" {
		d.snap.Pending = q.a.What
	}
	d.mu.Unlock()
	d.poke()
	return true
}

// dropQueue (with mu held) forgets the queued actions and wipes their secrets.
func (d *DeviceState) dropQueue() {
	for i := range d.queue {
		d.queue[i].wipe()
	}
	d.queue = nil
	d.snap.Pending = ""
}

func (d *DeviceState) call(lang, method, path string, body []byte, timeout time.Duration) (int, any, *APIError) {
	if lang == "" {
		lang = d.lang.get()
	}
	return call(d.ctx, d.sock, lang, method, path, body, timeout)
}

// gone: an error that means the app does not have this route (an older app) or there is no app at all.
func (d *DeviceState) gone(e *APIError) bool {
	if e.Status == 403 || e.Status == 404 || e.Status == 405 {
		return true
	}
	if e.Status == 0 {
		_, err := os.Stat(d.sock)
		return err != nil
	}
	return false
}

// --- the goroutine ---

func (d *DeviceState) run() {
	defer close(d.done)
	for d.ctx.Err() == nil {
		if !d.safeLoop() {
			return
		}
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// safeLoop runs loop; true if it panicked (only the panic's type is logged: its value could carry a request).
func (d *DeviceState) safeLoop() (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			elog.Event("screen", "device_api_panic", "type", fmt.Sprintf("%T", r))
			d.mu.Lock()
			d.dropQueue()
			d.mu.Unlock()
		}
	}()
	d.loop()
	return false
}

// take is the next action (out of the queue) and what the loop needs.
func (d *DeviceState) take() (q *queued, kick bool, w ui.AppWatch, snap ui.AppSnapshot) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.queue) > 0 {
		first := d.queue[0]
		d.queue = d.queue[1:]
		q = &first
		d.snap.Pending = first.a.What
	}
	kick, d.kick = d.kick, false
	return q, kick, d.watch, d.snap
}

func (d *DeviceState) loop() {
	var dev, setup, upd, slots, zones, cloud, about, dest, status, srcs, bt time.Time
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for d.ctx.Err() == nil {
		q, kick, w, snap := d.take()
		if kick {
			dev, setup, upd, slots, cloud, about = time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}
			dest, status, srcs, bt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
			if snap.Zones == nil {
				zones = time.Time{}
			}
		}
		if q != nil {
			d.runAction(*q)
			// What the action changed, asked again at once
			switch q.a.What {
			case "setup_step", "setup_password", "setup_complete", "setup_restart":
				setup = time.Time{}
			case "update_check", "update_install", "update_reboot":
				upd = time.Time{}
			case "slots":
				slots = time.Time{}
			case "dest_save", "restart", "input_save", "uplink", "modem_save", "modem_pin", "modem_reset", "modem_subnet",
				"dest_create", "dest_update", "dest_delete", "dest_activate":
				dest, status = time.Time{}, time.Time{}
			case "cloud_unlink":
				cloud = time.Time{}
			case "bt_power", "bt_scan", "bt_visible", "bt_device", "bt_use":
				bt = time.Time{}
			}
			d.changed()
			continue
		}
		every := func(api ui.Tri, fast bool, slow, quick time.Duration) time.Duration {
			switch {
			case api == ui.No:
				return GoneEvery
			case fast:
				return quick
			}
			return slow
		}
		now := time.Now()
		var waits []time.Time
		news := false
		due := func(last *time.Time, period time.Duration, on bool, fn func(ui.AppWatch)) {
			if !on {
				return
			}
			if now.Sub(*last) >= period {
				fn(w)
				*last, news = now, true
			}
			waits = append(waits, last.Add(period))
		}
		due(&dev, every(snap.DeviceAPI, w.Device || w.Setup, DeviceSlow, DeviceFast), true, d.pollDevice)
		due(&setup, every(snap.SetupAPI, w.Setup, DeviceSlow, SetupFast), true, d.pollSetup)
		upEvery := every(snap.UpdateAPI, w.Update, UpdateSlow, UpdateFast)
		due(&upd, upEvery, true, d.pollUpdate)
		due(&slots, every(snap.SlotsAPI, false, SlotsEvery, SlotsEvery), w.Slots, d.pollSlots)
		due(&zones, ZonesRetry, w.Zones && snap.Zones == nil, d.pollZones)
		due(&cloud, every(snap.CloudAPI, false, CloudEvery, CloudEvery), w.Cloud, d.pollCloud)
		due(&about, AboutEvery, w.About, d.pollAbout)
		due(&dest, every(snap.DestAPI, false, DestEvery, DestEvery), w.Dest, d.pollDest)
		due(&status, every(snap.StatusAPI, false, StatusEvery, StatusEvery), w.Status, d.pollStatus)
		due(&srcs, every(snap.SourcesAPI, false, SourcesEvery, SourcesEvery), w.Sources, d.pollSources)
		due(&bt, every(snap.BluetoothAPI, btFast(snap.Bluetooth), BluetoothEvery, BluetoothFast), w.Bluetooth,
			d.pollBluetooth)
		if news {
			d.changed()
		}
		next := now.Add(time.Hour)
		for _, t := range waits {
			if t.Before(next) {
				next = t
			}
		}
		timer.Reset(max(50*time.Millisecond, time.Until(next)))
		select {
		case <-d.wake:
		case <-d.ctx.Done():
		case <-timer.C:
		}
	}
}

// --- polls (blocking: only from the goroutine, and tests) ---

// pollDevice asks GET /device.
func (d *DeviceState) pollDevice(ui.AppWatch) {
	_, v, e := d.call("", "GET", "/device", nil, d.getTimeout)
	at := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case e == nil:
		if dev := deviceInfo(v, at); dev != nil {
			d.snap.Device, d.snap.DeviceAPI = dev, ui.Yes
			d.lang.set(dev.Language)
		}
	case d.gone(e):
		d.snap.Device, d.snap.DeviceAPI = nil, ui.No
	}
	// Passing (no answer, a 5xx): the last one stays, and its language with it
}

// PollDevice is pollDevice for tests and tools.
func (d *DeviceState) PollDevice() { d.pollDevice(ui.AppWatch{}) }

func (d *DeviceState) pollSetup(w ui.AppWatch) {
	_, v, e := d.call("", "GET", "/setup", nil, d.getTimeout)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case e == nil:
		s := setupInfo(v)
		if s == nil {
			return
		}
		// The setup code, only while the wizard is on the screen and a password is still needed
		if w.Setup && !s.Done && s.NeedsPassword {
			if s.Code == "" {
				if b, err := d.readFile(SetupCodeFile); err == nil {
					if c := strings.TrimSpace(string(b)); reSetupCode.MatchString(c) {
						s.Code = c
					}
					clear(b)
				}
			}
		} else {
			s.Code = ""
		}
		d.snap.Setup, d.snap.SetupAPI = s, ui.Yes
	case d.gone(e):
		d.snap.Setup, d.snap.SetupAPI = nil, ui.No
	}
}

// PollSetup is pollSetup as the wizard sees it, for tests.
func (d *DeviceState) PollSetup(w ui.AppWatch) { d.pollSetup(w) }

func (d *DeviceState) pollUpdate(ui.AppWatch) {
	_, v, e := d.call("", "GET", "/update", nil, d.getTimeout)
	lang := d.lang.get()
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case e == nil:
		if u := updateInfo(v, lang); u != nil {
			d.snap.Update, d.snap.UpdateAPI = u, ui.Yes
		}
	case d.gone(e):
		d.snap.Update, d.snap.UpdateAPI = nil, ui.No
	}
}

// PollUpdate is pollUpdate for tests.
func (d *DeviceState) PollUpdate() { d.pollUpdate(ui.AppWatch{}) }

func (d *DeviceState) pollSlots(ui.AppWatch) {
	_, v, e := d.call("", "GET", "/sources/slots", nil, d.getTimeout)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case e == nil:
		if s := slotsInfo(v); s != nil {
			d.snap.Slots, d.snap.SlotsAPI = s, ui.Yes
		}
	case d.gone(e):
		d.snap.Slots, d.snap.SlotsAPI = nil, ui.No
	}
}

// PollSlots is pollSlots for tests.
func (d *DeviceState) PollSlots() { d.pollSlots(ui.AppWatch{}) }

func (d *DeviceState) pollZones(ui.AppWatch) {
	_, v, e := d.call("", "GET", "/device/timezones", nil, d.getTimeout)
	d.mu.Lock()
	defer d.mu.Unlock()
	if e != nil {
		d.snap.ZonesErr = e.userText(d.lang.get())
		return
	}
	if z := zoneList(v); z != nil {
		d.snap.Zones, d.snap.ZonesErr = z, ""
	} else {
		d.snap.ZonesErr = text(d.lang.get(), "unexpected")
	}
}

// PollZones is pollZones for tests.
func (d *DeviceState) PollZones() { d.pollZones(ui.AppWatch{}) }

func (d *DeviceState) pollCloud(ui.AppWatch) {
	_, v, e := d.call("", "GET", "/cloud", nil, d.getTimeout)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case e == nil:
		if c := cloudInfo(v); c != nil {
			d.snap.Cloud, d.snap.CloudAPI = c, ui.Yes
		}
	case e.Kind == "cloud_disabled":
		d.snap.Cloud, d.snap.CloudAPI = &ui.CloudInfo{State: "disabled"}, ui.Yes
	case d.gone(e):
		d.snap.Cloud, d.snap.CloudAPI = nil, ui.No
	}
}

// PollCloud is pollCloud for tests.
func (d *DeviceState) PollCloud() { d.pollCloud(ui.AppWatch{}) }

// pollAbout reads what the engine knows itself: the build's commit and the board's addresses.
func (d *DeviceState) pollAbout(ui.AppWatch) {
	commit := ""
	if b, err := d.readFile(BuildFile); err == nil && len(b) < 4096 {
		for _, f := range strings.Fields(string(b)) {
			if c, ok := strings.CutPrefix(f, "commit="); ok && reCommit.MatchString(c) {
				commit = c
			}
		}
	}
	ips := d.addrs()
	d.mu.Lock()
	d.snap.Commit, d.snap.IPs = commit, ips
	d.mu.Unlock()
}

// boardAddrs: the board's IP addresses (up, not loopback, not link-local), IPv4 first, at most 6.
func boardAddrs() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var v4, v6 []string
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		as, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range as {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.IsLinkLocalUnicast() || n.IP.IsLoopback() {
				continue
			}
			if n.IP.To4() != nil {
				v4 = append(v4, n.IP.String())
			} else {
				v6 = append(v6, n.IP.String())
			}
		}
	}
	out := append(v4, v6...)
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

// --- actions ---

// runAction runs one action and keeps its result. The password's secret is wiped whatever happens.
func (d *DeviceState) runAction(q queued) {
	defer q.wipe()
	a := q.a
	route := actionRoutes[a.What]
	if parityActions[a.What] {
		d.runParity(q, route)
		return
	}
	lang := ""
	var body []byte
	switch a.What {
	case "language":
		body, lang = jsonBody(map[string]any{"language": a.Value}), a.Value // the answer already in the new one
	case "name":
		body = jsonBody(map[string]any{"name": a.Value})
	case "timezone":
		body = jsonBody(map[string]any{"timezone": a.Value})
	case "ntp":
		body = jsonBody(map[string]any{"ntp": a.On})
	case "time":
		body = jsonBody(map[string]any{"ntp": false, "time": a.Time.Format(time.RFC3339)})
	case "setup_step":
		body = jsonBody(map[string]any{"id": a.Value, "state": a.State})
	case "setup_password":
		body = passwordBody(q.s)
	case "slots":
		slots := a.Slots
		if slots == nil {
			slots = []ui.SlotAssign{}
		}
		body = jsonBody(map[string]any{"slots": slots})
	default:
		body = []byte("{}")
	}
	_, v, e := d.call(lang, route[0], route[1], body, d.actTimeout)
	res := &ui.AppResult{What: a.What, OK: e == nil, At: time.Now()}
	if e != nil {
		res.Status, res.Code, res.Message = e.Status, e.Kind, e.userText(d.lang.get())
		res.Fields = fieldErrors(v)
	}
	at := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.snap.Last = res
	d.snap.Pending = ""
	if len(d.queue) > 0 {
		d.snap.Pending = d.queue[0].a.What
	}
	if e != nil {
		return
	}
	switch route[1] {
	case "/device":
		if dev := deviceInfo(v, at); dev != nil {
			d.snap.Device, d.snap.DeviceAPI = dev, ui.Yes
			d.lang.set(dev.Language)
		} else if a.What == "language" {
			d.lang.set(a.Value)
		}
	case "/sources/slots":
		if s := slotsInfo(v); s != nil {
			d.snap.Slots, d.snap.SlotsAPI = s, ui.Yes
		}
		if a, ok := boolOf(obj(v)["applied"]); ok {
			res.Applied = &a
		}
	case "/cloud/pair":
		if c := cloudInfo(v); c != nil {
			d.snap.Cloud, d.snap.CloudAPI = c, ui.Yes
		}
	}
}

// passwordBody is {"password": …}, built without turning the password into a string (see connectBody). The
// caller's call() wipes it.
func passwordBody(s secret) []byte {
	b := make([]byte, 0, 2*len(s.b)+16)
	b = append(b, `{"password":"`...)
	b = appendJSONBytes(b, s.b)
	return append(b, `"}`...)
}

// appendJSONBytes escapes p as the inside of a JSON string.
func appendJSONBytes(b, p []byte) []byte {
	for _, c := range p {
		switch {
		case c == '"' || c == '\\':
			b = append(b, '\\', c)
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&15])
		default:
			b = append(b, c)
		}
	}
	return b
}

// --- what the app says, validated (whatever does not fit is left out) ---

func boolOf(v any) (bool, bool) {
	b, ok := v.(bool)
	return b, ok
}

func deviceInfo(v any, at time.Time) *ui.DeviceInfo {
	m := obj(v)
	if m == nil {
		return nil
	}
	dev := &ui.DeviceInfo{At: at}
	dev.Language = oneOf(m, "language", knownLanguages)
	if ls, ok := m["languages"].([]any); ok {
		for _, l := range ls {
			if s, ok := l.(string); ok && knownLanguages[s] {
				dev.Languages = append(dev.Languages, s)
			}
		}
	}
	if s := textField(m, "name", 160); utf8.RuneCountInString(s) <= 40 {
		dev.Name = s
	}
	if s, ok := m["hostname"].(string); ok && len(s) <= 63 && reHostname.MatchString(s) {
		dev.Hostname = s
	}
	if s, ok := m["timezone"].(string); ok && len(s) <= 64 && reZone.MatchString(s) {
		dev.Timezone = s
	}
	if t := obj(m["time"]); t != nil {
		if s, ok := t["now"].(string); ok {
			if now, err := time.Parse(time.RFC3339, s); err == nil {
				dev.Now = now
			}
		}
		dev.NTP, _ = boolOf(t["ntp"])
		dev.Synced, _ = boolOf(t["synced"])
		if r, ok := boolOf(t["rtc"]); ok {
			dev.RTC = &r
		}
	}
	if ver := obj(m["version"]); ver != nil {
		dev.Release, dev.AppVer, dev.EngineVer = textField(ver, "release", 64), textField(ver, "app", 64),
			textField(ver, "engine", 64)
	}
	if dev.Language == "" && dev.Name == "" && dev.Hostname == "" {
		return nil // not a device at all
	}
	return dev
}

func setupInfo(v any) *ui.SetupInfo {
	m := obj(v)
	done, ok := boolOf(m["done"])
	if m == nil || !ok {
		return nil
	}
	s := &ui.SetupInfo{Done: done}
	s.NeedsPassword, _ = boolOf(m["needsPassword"])
	s.CodeRequired, _ = boolOf(m["codeRequired"])
	if steps, ok := m["steps"].([]any); ok {
		for _, x := range steps {
			st := obj(x)
			id, state := oneOf(st, "id", setupSteps), oneOf(st, "state", reStepState)
			if id != "" && state != "" && len(s.Steps) < 12 {
				s.Steps = append(s.Steps, ui.SetupStep{ID: id, State: state})
			}
		}
	}
	if c, ok := m["code"].(string); ok && reSetupCode.MatchString(c) {
		s.Code = c // an app that gives it over the local socket
	}
	return s
}

// notesText is the release notes in lang: a string as is, or {"en": …, "es": …} by language (lang, then English,
// then any), with control characters out (new lines stay) and at most 2000 characters.
func notesText(v any, lang string) string {
	var t string
	switch x := v.(type) {
	case string:
		t = x
	case map[string]any:
		for _, k := range []string{lang, DefaultLanguage, "es", "fr"} {
			if s, ok := x[k].(string); ok && s != "" {
				t = s
				break
			}
		}
	}
	if !utf8.ValidString(t) {
		return ""
	}
	var b strings.Builder
	n := 0
	for _, r := range t {
		if r < 32 && r != '\n' {
			continue
		}
		if n++; n > 2000 {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func updateInfo(v any, lang string) *ui.UpdateInfo {
	m := obj(v)
	st := oneOf(m, "state", updStates)
	if m == nil || st == "" {
		return nil
	}
	u := &ui.UpdateInfo{Current: textField(m, "current", 64), State: st, Error: textField(m, "error", 200)}
	if p, ok := m["progress"].(json.Number); ok {
		if f, err := p.Float64(); err == nil && f >= 0 && f <= 1 {
			u.Progress = f
		}
	}
	if s, ok := m["lastCheck"].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			u.LastCheck = &t
		}
	}
	if a := obj(m["available"]); a != nil {
		if ver := textField(a, "version", 64); ver != "" {
			r := &ui.UpdateRelease{Version: ver, Notes: notesText(a["notes"], lang)}
			if n, ok := a["size"].(json.Number); ok {
				if i, err := n.Int64(); err == nil && i >= 0 {
					r.Size = i
				}
			}
			u.Available = r
		}
	}
	u.Message = textField(m, "message", 300)
	if c, ok := boolOf(m["canInstall"]); ok {
		u.CanInstall = &c
	}
	u.Blocked = oneOf(m, "blocked", updBlocked)
	u.BlockedMessage = textField(m, "blockedMessage", 300)
	u.TestBuild, _ = boolOf(m["testBuild"])
	if lr := obj(m["lastResult"]); lr != nil {
		if st := oneOf(lr, "state", runStates); st != "" {
			run := &ui.UpdateRun{State: st, From: textField(lr, "from", 64), To: textField(lr, "to", 64),
				Error: textField(lr, "error", 64), Message: textField(lr, "message", 300)}
			for _, k := range []string{"finishedAt", "at"} {
				if s, ok := lr[k].(string); ok {
					if t, err := time.Parse(time.RFC3339, s); err == nil {
						run.At = t
						break
					}
				}
			}
			u.LastResult = run
		}
	}
	if v := obj(m["versions"]); v != nil {
		u.Versions = ui.UpdateVersions{Release: textField(v, "release", 64), App: textField(v, "app", 64),
			Engine: textField(v, "engine", 64)}
		if c, ok := v["commit"].(string); ok && reCommit.MatchString(c) {
			u.Versions.Commit = c
		}
	}
	if w := oneOf(m, "downloadWaits", updWaits); w != "" {
		u.DownloadWaits, u.DownloadWaitsMessage = w, textField(m, "downloadWaitsMessage", 300)
	}
	u.System = updateSystem(obj(m["system"]))
	return u
}

// updateSystem is GET /update's "system" (the system layer, UPDATES.md §12), or nil without a known state.
func updateSystem(m map[string]any) *ui.UpdateSystem {
	st := oneOf(m, "state", sysStates)
	if m == nil || st == "" {
		return nil
	}
	s := &ui.UpdateSystem{State: st}
	if st != "ok" {
		s.Error, s.Message = textField(m, "error", 64), textField(m, "message", 300)
	}
	s.RestartRequired, _ = boolOf(m["restartRequired"])
	if !s.RestartRequired {
		return s
	}
	s.RestartMessage = textField(m, "restartMessage", 300)
	if ps, ok := m["pending"].([]any); ok {
		for _, x := range ps {
			if a, ok := x.(string); ok && sysAreas[a] && !slices.Contains(s.Pending, a) {
				s.Pending = append(s.Pending, a)
			}
		}
	}
	return s
}

// aliasOK: CONTRACT §3, 0-24 characters without control characters (the app validates the rest).
func aliasOK(s string) bool { return s == "" || (textOK(s, 128) && utf8.RuneCountInString(s) <= 24) }

func slotSource(v any) *ui.SlotSource {
	m := obj(v)
	id, ok := m["id"].(string)
	if m == nil || !ok || !textOK(id, 220) {
		return nil
	}
	return &ui.SlotSource{ID: id, Name: textField(m, "name", 120), Kind: textField(m, "kind", 32),
		State: textField(m, "state", 32)}
}

func slotsInfo(v any) *ui.SlotsInfo {
	m := obj(v)
	raw, ok := m["slots"].([]any)
	if m == nil || !ok {
		return nil
	}
	s := &ui.SlotsInfo{}
	s.EngineSupports, _ = boolOf(m["engineSupports"])
	seen := map[int]bool{}
	for _, x := range raw {
		sm := obj(x)
		n, ok := intIn(sm["slot"], 1, 16)
		assign, _ := sm["assign"].(string)
		alias, _ := sm["alias"].(string)
		if !ok || seen[n] || !reSourceID.MatchString(assign) || !aliasOK(alias) {
			continue
		}
		seen[n] = true
		s.Slots = append(s.Slots, ui.SlotInfo{Slot: n, Assign: assign, Alias: alias, Current: slotSource(sm["current"])})
	}
	if cs, ok := m["candidates"].([]any); ok {
		for _, x := range cs {
			cm := obj(x)
			id, _ := cm["id"].(string)
			if !reSourceID.MatchString(id) || id == "auto" || id == "off" || len(s.Candidates) >= 32 {
				continue
			}
			conn, _ := boolOf(cm["connected"])
			s.Candidates = append(s.Candidates, ui.SlotCandidate{ID: id, Name: textField(cm, "name", 120),
				Kind: textField(cm, "kind", 32), Connected: conn})
		}
	}
	return s
}

func zoneList(v any) []ui.TimeZone {
	m := obj(v)
	raw, ok := m["zones"].([]any)
	if m == nil || !ok {
		return nil
	}
	out := make([]ui.TimeZone, 0, len(raw))
	for _, x := range raw {
		zm := obj(x)
		id, _ := zm["id"].(string)
		if len(id) > 64 || !reZone.MatchString(id) || len(out) >= 800 {
			continue
		}
		off, _ := zm["offset"].(string)
		if !reOffset.MatchString(off) {
			off = ""
		}
		out = append(out, ui.TimeZone{ID: id, Offset: off})
	}
	return out
}

func cloudInfo(v any) *ui.CloudInfo {
	m := obj(v)
	st := oneOf(m, "state", cloudStates)
	if m == nil || st == "" {
		return nil
	}
	c := &ui.CloudInfo{State: st, AccountLabel: textField(m, "accountLabel", 120), LastError: textField(m, "lastError", 200)}
	if code := textField(m, "code", 16); code != "" {
		c.Code = code
	}
	if s, ok := m["expiresAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			c.ExpiresAt = &t
		}
	}
	if o, ok := boolOf(m["online"]); ok {
		c.Online = &o
	}
	if u := textField(m, "linkUrl", 300); strings.HasPrefix(u, "https://") && !strings.ContainsAny(u, " \"<>") {
		c.LinkURL = u
		c.LinkQR = qrRowsOf(u)
	}
	return c
}

// qrRowsOf is the QR of a text in rows of 0 and 1 (correction M, like the app's wifi QR), nil if it fails.
func qrRowsOf(text string) []string {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil
	}
	rows := make([]string, c.Size)
	b := make([]byte, c.Size)
	for y := range rows {
		for x := range b {
			b[x] = '0'
			if c.Black(x, y) {
				b[x] = '1'
			}
		}
		rows[y] = string(b)
	}
	return rows
}

// fieldErrors is the app's field errors: {"fields": {"slots.2.assign": "message"}, "codes": {"slots.2.assign":
// "duplicate"}} (the app's shape), or a field given as {"code", "message"}; at most 32.
func fieldErrors(v any) map[string]ui.AppFieldError {
	m := obj(v)
	fs, codes := obj(m["fields"]), obj(m["codes"])
	if len(fs) == 0 && len(codes) == 0 {
		return nil
	}
	out := map[string]ui.AppFieldError{}
	add := func(k string, f ui.AppFieldError) {
		if (len(out) < 32 || out[k] != (ui.AppFieldError{})) && textOK(k, 64) {
			cur := out[k]
			if f.Code != "" {
				cur.Code = f.Code
			}
			if f.Message != "" {
				cur.Message = f.Message
			}
			out[k] = cur
		}
	}
	for k, x := range fs {
		switch f := x.(type) {
		case string:
			if textOK(f, 400) {
				add(k, ui.AppFieldError{Message: f})
			}
		case map[string]any:
			add(k, ui.AppFieldError{Code: textField(f, "code", 64), Message: textField(f, "message", 400)})
		}
	}
	for k, x := range codes {
		if c, ok := x.(string); ok && textOK(c, 64) {
			add(k, ui.AppFieldError{Code: c})
		}
	}
	return out
}
