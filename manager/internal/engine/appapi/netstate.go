package appapi

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// Polling cadence: GET /network (and GET /network/wifi, the client) with Settings open / closed (only the gear's
// dot and the header's antenna when closed: emitting, the engine is short of CPU); the wifi to join with the panel
// open; and the scan with the list of networks open (the app answers at once with the latest and scans behind).
const (
	Fast      = 2 * time.Second
	Slow      = 15 * time.Second
	WiFiEvery = 10 * time.Second
	ScanEvery = 2500 * time.Millisecond
	// MessageFor: how long the notice of the last change is shown.
	MessageFor = 6 * time.Second
)

// The changes the screen asks the app for: what → (route, body field). One at a time, by NetState's goroutine.
var puts = map[string][2]string{
	"net":    {"/network", "enabled"},     // the RTMP/SRT reception (NETWORK_SOURCES.md §6.12)
	"ap":     {"/network/wifi", "ap"},     // «Crear mi wifi»: always | rescue (WIFI.md §5)
	"client": {"/network/wifi", "client"}, // «Conectarse a una wifi»: bool
	"ssid":   {"/network/ap", "ssid"},     // the name of «Crear mi wifi» (the AP's SSID; the app validates it)
}

// Connect and forget, with {"ssid"} (and the password).
var posts = map[string]string{"connect": "/network/wifi/connect", "forget": "/network/wifi/forget"}

// APIState says whether the app answers (Python NetState.api: None, True, False). It also holds whether the app has
// GET /network/wifi (Python wifi_api).
type APIState int8

const (
	APIUnknown APIState = iota // not asked yet (None)
	APIUp                      // the app answers, or its socket is there but it hangs (True)
	APIGone                    // no socket: an older app, the screen uses wifi.json (False)
)

// Change is the change in progress (Python pending = (what, value)).
type Change struct {
	What  string // net | ap | client | ssid | connect | forget
	On    bool   // net, client: the value asked; ap: Value == always
	Value string // ap: always | rescue; ssid: the new name; connect, forget: the SSID
}

// Message is the notice of the last change, shown until Until.
type Message struct {
	Text  string
	Until time.Time
}

// Active: there is a notice to show at now.
func (m Message) Active(now time.Time) bool { return m.Text != "" && now.Before(m.Until) }

// PostResult is the last connect or forget (Python last_post): the screen knows whether connecting failed.
type PostResult struct {
	What string // connect | forget
	SSID string
	OK   bool
	At   time.Time
}

// Snapshot is what the screen reads (Python snapshot()). Pointers and slices are shared and read-only. It never
// holds the typed wifi password.
type Snapshot struct {
	API     APIState
	Status  *Status // the last GET /network (or the Status of a PUT); nil: None
	Error   string  // why there is no Status (the app's message)
	WiFi    *WiFi   // the wifi to join, only while the panel is open
	Client  *Client // nil if the app does not have it or there is no wlan0
	Warning string  // two_channels: the client and the AP on different channels
	// Scan is the networks around with the list open: nil is Python's None (list closed, not asked yet) or 'busy'
	// (ScanBusy: no scan yet, «Buscando redes…»); non-nil is the list, maybe empty («No se ve ninguna red wifi»).
	Scan      []Network
	ScanBusy  bool
	ScanError string
	Pending   *Change
	Message   Message
	LastPost  *PostResult
}

// Net is what the screen uses of NetState (so it can be faked in its tests).
type Net interface {
	Snapshot() Snapshot
	SetPanel(on bool)
	SetScan(on bool)
	SetEnabled(enabled bool) bool
	SetAP(on bool) bool
	SetClient(on bool) bool
	SetSSID(name string) bool
	Connect(ssid string, password []byte) bool
	Forget(ssid string) bool
	Say(text string)
}

var _ Net = (*NetState)(nil)

// NetState is the network according to the app (the board's wifi and the network cameras), for the Settings panel,
// the gear's dot and the header's antenna. It asks GET /network and GET /network/wifi (the client) every Fast with
// the panel open and every Slow closed; only with the panel open, the wifi to join (without the cameras' keys: they
// do not leave through the socket), and with the list of networks open, the scan. The changes (PUTs) and connect or
// forget (POSTs) go through this same goroutine, one at a time. The screen only reads Snapshot(); changed is called
// (from this goroutine, without locks held: it must not block) when there is something new to draw.
//
// The typed password (Connect) is kept only until it is sent, apart from what the screen reads (Snapshot does not
// carry it), wiped after, and never logged: neither it nor the request.
type NetState struct {
	changed func()
	sock    string
	lang    *language    // the device language (DeviceState sets it): Accept-Language and the texts
	dev     *DeviceState // the device API (the same socket and callback), started and stopped with this

	getTimeout, putTimeout, postTimeout time.Duration
	putFn, postFn                       func(Change) // put and post; tests replace them

	ctx     context.Context
	cancel  context.CancelFunc
	wake    chan struct{}
	done    chan struct{}
	started atomic.Bool

	mu       sync.Mutex
	api      APIState
	status   *Status
	err      string
	wifi     *WiFi
	client   *Client
	warning  string
	wifiAPI  APIState // APIGone: the app has no GET /network/wifi (403/404): not asked again until reopening
	scan     []Network
	scanBusy bool
	scanErr  string
	scanning bool // the list of networks is open (SetScan)
	pending  *Change
	lastPost *PostResult
	message  Message
	panel    bool
	want     *Change
	secret   secret // the password of a queued connect, only until it is sent (not in Snapshot)
	kick     bool
}

// New makes a NetState for the app at sock ("" = Sock). changed may be nil. Start runs it.
func New(changed func(), sock string) *NetState {
	if sock == "" {
		sock = Sock
	}
	if changed == nil {
		changed = func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	lang := &language{}
	n := &NetState{
		changed: changed, sock: sock, lang: lang, dev: NewDeviceState(changed, sock, lang),
		getTimeout: GetTimeout, putTimeout: PutTimeout, postTimeout: PostTimeout,
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}),
		kick: true,
	}
	n.putFn, n.postFn = n.put, n.post
	return n
}

// Start runs the polling goroutine (once), and the device API's.
func (n *NetState) Start() {
	if n.started.CompareAndSwap(false, true) {
		go n.run()
		n.dev.Start()
	}
}

// App is the device API of the same client (ui.AppProvider: the screen finds it through its Net).
func (n *NetState) App() ui.App { return n.dev }

// Device is the device API's client itself.
func (n *NetState) Device() *DeviceState { return n.dev }

// Stop ends the goroutine (cancelling a request in flight) and waits for it. A NetState does not start again.
func (n *NetState) Stop() {
	n.cancel()
	n.dev.Stop()
	if n.started.Load() {
		<-n.done
	}
	n.mu.Lock()
	n.secret.wipe()
	n.mu.Unlock()
}

func (n *NetState) poke() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// Snapshot is the state for the screen.
func (n *NetState) Snapshot() Snapshot {
	n.mu.Lock()
	defer n.mu.Unlock()
	return Snapshot{API: n.api, Status: n.status, Error: n.err, WiFi: n.wifi, Client: n.client, Warning: n.warning,
		Scan: n.scan, ScanBusy: n.scanBusy, ScanError: n.scanErr, Pending: n.pending, Message: n.message,
		LastPost: n.lastPost}
}

// SetPanel: the Settings panel opens (asks at once, the wifi too) or closes (the wifi's password out of memory, and
// the list of networks too; a connect still queued is dropped with its password).
func (n *NetState) SetPanel(on bool) {
	n.mu.Lock()
	n.panel = on
	if !on {
		n.wifi = nil
		n.scanning, n.scan, n.scanBusy, n.scanErr = false, nil, false, ""
		// Closing Settings (or locking, which closes it) with a connect still queued: it is not sent, and its
		// password goes (one already on its way took it from here when it started: post)
		if n.want != nil && n.want.What == "connect" {
			n.want, n.pending = nil, nil
		}
		n.secret.wipe()
	} else {
		n.wifiAPI = APIUnknown
	}
	n.kick = true
	n.mu.Unlock()
	n.poke()
}

// SetScan: the list of networks opens (scans at once and every ScanEvery) or closes (stops asking; the latest
// stays until Settings closes, so as not to show «Buscando redes…» when coming back from the keyboard).
func (n *NetState) SetScan(on bool) {
	n.mu.Lock()
	n.scanning = on && n.panel
	n.kick = true
	n.mu.Unlock()
	n.poke()
}

// queue takes a change: false if one is already in progress or the network's state is unknown (then the secret is
// wiped).
func (n *NetState) queue(c Change, s secret) bool {
	n.mu.Lock()
	if n.pending != nil || n.status == nil {
		n.mu.Unlock()
		s.wipe()
		return false
	}
	n.pending, n.want = &c, &c
	n.secret.wipe()
	n.secret = s
	n.mu.Unlock()
	n.poke()
	return true
}

// SetEnabled turns the RTMP/SRT reception on or off (NETWORK_SOURCES.md §6.12).
func (n *NetState) SetEnabled(enabled bool) bool {
	return n.queue(Change{What: "net", On: enabled}, secret{})
}

// SetAP is «Crear mi wifi» (WIFI.md W7): always (on) or rescue (off except to rescue the board).
func (n *NetState) SetAP(on bool) bool {
	v := "rescue"
	if on {
		v = "always"
	}
	return n.queue(Change{What: "ap", On: on, Value: v}, secret{})
}

// SetClient is «Conectarse a una wifi» (WIFI.md §4).
func (n *NetState) SetClient(on bool) bool {
	return n.queue(Change{What: "client", On: on}, secret{})
}

// SetSSID renames «Crear mi wifi» (PUT /network/ap {"ssid"}): the AP restarts with it and whoever is on it has to
// join the new name; with a camera on air over it, the app refuses (409 on_air) and its message is shown.
func (n *NetState) SetSSID(name string) bool {
	return n.queue(Change{What: "ssid", Value: name}, secret{})
}

// Connect connects the client to ssid: with the typed password, or without it (empty: an open or known network,
// the saved profile). The password is copied (the caller can wipe its own right after) and kept only until sent.
// False if another change is in progress.
func (n *NetState) Connect(ssid string, password []byte) bool {
	return n.queue(Change{What: "connect", Value: ssid}, newSecret(password))
}

// Forget forgets a known network.
func (n *NetState) Forget(ssid string) bool {
	return n.queue(Change{What: "forget", Value: ssid}, secret{})
}

// Say shows a notice for MessageFor.
func (n *NetState) Say(text string) {
	n.mu.Lock()
	n.message = Message{Text: text, Until: time.Now().Add(MessageFor)}
	n.mu.Unlock()
}

func (n *NetState) call(method, path string, body []byte, timeout time.Duration) (int, any, *APIError) {
	return call(n.ctx, n.sock, n.lang.get(), method, path, body, timeout)
}

// PollStatus asks GET /network. Blocking (up to GetTimeout): only from NetState's goroutine (and tests).
func (n *NetState) PollStatus() {
	_, d, e := n.call("GET", "/network", nil, n.getTimeout)
	if e == nil {
		st := networkStatus(d)
		n.mu.Lock()
		n.api, n.status = APIUp, st
		n.err = ""
		if st == nil {
			n.err = text(n.lang.get(), "no_network_state", text(n.lang.get(), "unexpected"))
		}
		n.mu.Unlock()
		return
	}
	// No socket = an older app (the screen uses wifi.json); with a socket and no answer, it hangs
	_, serr := os.Stat(n.sock)
	gone := e.Status == 0 && serr != nil
	n.mu.Lock()
	defer n.mu.Unlock()
	n.status = nil
	switch {
	case gone:
		n.api, n.err = APIGone, ""
		n.client = nil // no app, nothing to say of the client (the antenna cannot keep the latest)
	case e.Status == 0:
		n.api, n.err = APIUp, text(n.lang.get(), "encoder_app_not_responding")
	default:
		msg := e.Message
		if msg == "" {
			msg = e.Kind
		}
		n.api, n.err = APIUp, text(n.lang.get(), "no_network_state", msg)
	}
}

// PollWiFi asks the wifi to join (only kept with the panel open). Blocking.
func (n *NetState) PollWiFi() {
	var w *WiFi
	if _, d, e := n.call("GET", "/network/credentials?qr=rows", nil, n.getTimeout); e == nil {
		w = wifiCreds(d)
	}
	n.mu.Lock()
	n.wifi = nil
	if n.panel {
		n.wifi = w
	}
	n.mu.Unlock()
}

// setWiFiStatus takes GET /network/wifi (or a PUT or connect, which return the same): the client and the warning;
// and the access point, into accessPoint at once (without waiting for the next GET /network).
func (n *NetState) setWiFiStatus(v any) {
	d := obj(v)
	apm := obj(d["ap"])
	if apm == nil {
		return
	}
	ap := apFields(apm)
	cl := clientState(d)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.wifiAPI = APIUp
	n.client = cl
	n.warning = ""
	if d["warning"] == "two_channels" {
		n.warning = "two_channels"
	}
	if n.status != nil {
		st := *n.status
		cur := st.AccessPoint
		if ap.Policy != "" {
			cur.Policy = ap.Policy
		}
		if ap.State != "" {
			cur.State = ap.State
		}
		if ap.SSID != "" {
			cur.SSID = ap.SSID
		}
		if ap.Channel != nil {
			cur.Channel = ap.Channel
		}
		if ap.Clients != nil {
			cur.Clients = ap.Clients
		}
		cur.Error = ap.Error // the old error never stays: the new one, if any
		st.AccessPoint = cur
		n.status = &st
	}
}

// PollWiFiStatus asks GET /network/wifi. Blocking.
func (n *NetState) PollWiFiStatus() {
	_, d, e := n.call("GET", "/network/wifi", nil, n.getTimeout)
	if e == nil {
		n.setWiFiStatus(d)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case e.Status == 403 || e.Status == 404: // an app without the board's wifi: not asked again
		n.wifiAPI, n.client = APIGone, nil
	case e.Kind == "wifi_unavailable": // the board without wlan0
		n.client = nil
	}
	// The rest (no answer, a 5xx) is passing: the latest stays, so the rows of Settings do not move under the finger
}

// PollScan asks GET /network/wifi/scan. Blocking.
func (n *NetState) PollScan() {
	_, d, e := n.call("GET", "/network/wifi/scan", nil, n.getTimeout)
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.scanning {
		return
	}
	switch {
	case e == nil:
		if nets := scanList(d); nets != nil {
			n.scan, n.scanBusy, n.scanErr = nets, false, ""
		} else {
			n.scanErr = text(n.lang.get(), "no_scan")
		}
	case e.Kind == "scan_busy": // no scan yet: «Buscando redes…», not an error
		if len(n.scan) == 0 {
			n.scan, n.scanBusy = nil, true
		}
		n.scanErr = ""
	default:
		n.scanErr = e.userText(n.lang.get())
	}
}

// put makes a PUT change (PUTS).
func (n *NetState) put(c Change) {
	defer func() {
		n.mu.Lock()
		n.pending = nil
		n.mu.Unlock()
	}()
	route := puts[c.What]
	var value any = c.On
	if c.What == "ap" || c.What == "ssid" {
		value = c.Value
	}
	code, d, e := n.call("PUT", route[0], jsonBody(map[string]any{route[1]: value}), n.putTimeout)
	if e != nil {
		n.Say(e.userText(n.lang.get()))
		return
	}
	why := ""
	var st *Status
	if c.What == "net" || c.What == "ssid" { // both answer like GET /network
		st = networkStatus(d)
	}
	m := obj(d)
	switch {
	case st != nil:
		n.mu.Lock()
		n.status = st
		n.mu.Unlock()
		if code == 202 { // saved but not applied: the reason, in the wifi or the cable
			why = stringOf(obj(m["accessPoint"])["error"])
			if why == "" {
				why = stringOf(obj(m["lan"])["error"])
			}
		}
	case (c.What == "ap" || c.What == "client") && obj(m["ap"]) != nil:
		n.setWiFiStatus(d)
		if code == 202 && c.What == "ap" {
			why = apFields(m["ap"]).Error
		}
	}
	if code == 202 {
		msg := text(n.lang.get(), "saved_not_applied")
		if textOK(why, 200) {
			msg += ": " + why
		}
		n.Say(msg)
		return
	}
	if c.What == "ssid" { // what to do now: with the AP up, join the devices to the new name
		key := "renamed_off"
		if st != nil && st.AccessPoint.State == "on" {
			key = "renamed"
		}
		n.Say(text(n.lang.get(), key, c.Value))
	}
}

// post connects or forgets. The password comes out of the secret and is kept nowhere else: not in the state, not
// in the notice, not in the error (APIError only has the code and the app's message); the body is wiped after.
func (n *NetState) post(c Change) {
	n.mu.Lock()
	s := n.secret
	n.secret = secret{}
	n.mu.Unlock()
	ok := false
	defer func() {
		s.wipe()
		n.mu.Lock()
		n.lastPost = &PostResult{What: c.What, SSID: c.Value, OK: ok, At: time.Now()}
		n.pending = nil
		n.mu.Unlock()
	}()
	var body []byte
	if c.What == "connect" {
		body = connectBody(c.Value, s)
	} else {
		body = jsonBody(map[string]string{"ssid": c.Value})
	}
	_, d, e := n.call("POST", posts[c.What], body, n.postTimeout)
	if e != nil {
		n.Say(e.userText(n.lang.get()))
		return
	}
	ok = true
	n.setWiFiStatus(d)
	if c.What == "forget" {
		n.Say(text(n.lang.get(), "forgotten", c.Value))
	}
}

// run keeps loop alive: an unexpected failure cannot leave the goroutine dead (no polling, a change «in progress»
// forever). What is pending is dropped, the password too, and it goes on. Only the panic's type is logged: its
// value could carry something of a request.
func (n *NetState) run() {
	defer close(n.done)
	for n.ctx.Err() == nil {
		if !n.safeLoop() {
			return
		}
		select {
		case <-n.ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// safeLoop runs loop; true if it panicked.
func (n *NetState) safeLoop() (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			elog.Event("screen", "app_api_panic", "type", fmt.Sprintf("%T", r))
			n.mu.Lock()
			n.pending, n.want = nil, nil
			n.secret.wipe()
			n.mu.Unlock()
		}
	}()
	n.loop()
	return false
}

// take is what loop needs from the state, taking the queued change.
func (n *NetState) take() (want *Change, kick, panel, scanning bool, wifiAPI APIState) {
	n.mu.Lock()
	defer n.mu.Unlock()
	want, n.want = n.want, nil
	kick, n.kick = n.kick, false
	return want, kick, n.panel, n.scanning, n.wifiAPI
}

func (n *NetState) apiUp() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.api == APIUp
}

func (n *NetState) loop() {
	var last, lastWiFi, lastScan time.Time
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for n.ctx.Err() == nil {
		want, kick, panel, scanning, wifiAPI := n.take()
		if kick {
			last, lastWiFi, lastScan = time.Time{}, time.Time{}, time.Time{}
		}
		if want != nil {
			if _, ok := posts[want.What]; ok {
				n.postFn(*want)
			} else {
				n.putFn(*want)
			}
			last, lastWiFi = time.Time{}, time.Time{} // the new state now (turning «Crear mi wifi» on the first time creates the network)
			n.changed()
			continue
		}
		every := Slow
		if panel {
			every = Fast
		}
		now := time.Now()
		news := false
		if now.Sub(last) >= every {
			n.PollStatus()
			if wifiAPI != APIGone && n.apiUp() { // the client: the header's antenna, also closed
				n.PollWiFiStatus()
			}
			last, news = now, true
		}
		if panel && now.Sub(lastWiFi) >= WiFiEvery {
			n.PollWiFi()
			lastWiFi, news = now, true
		}
		if scanning && now.Sub(lastScan) >= ScanEvery {
			n.PollScan()
			lastScan, news = now, true
		}
		if news {
			n.changed()
		}
		due := last.Add(every)
		if panel && lastWiFi.Add(WiFiEvery).Before(due) {
			due = lastWiFi.Add(WiFiEvery)
		}
		if scanning && lastScan.Add(ScanEvery).Before(due) {
			due = lastScan.Add(ScanEvery)
		}
		timer.Reset(max(50*time.Millisecond, time.Until(due)))
		select { // Go ≥1.23 timers: Reset needs no drain
		case <-n.wake:
		case <-n.ctx.Done():
		case <-timer.C:
		}
	}
}
