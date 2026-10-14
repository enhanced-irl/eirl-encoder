// Package wifi: the board's client wifi (wlan0) through NetworkManager (WIFI.md §4, phase W-B): scan, connect to
// a network, forget it, and switch «Join a wifi» on or off.
//
// The password only travels in the settings handed to NetworkManager over D-Bus, which stores it in its profile
// (/etc/NetworkManager/system-connections, root, 0600). Never on a process command line, in logs, in the state or
// in any file of the app (W6). The app's profiles are named eirl-wifi-<n>: the others are neither touched nor
// shown.
package wifi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid     = errors.New("wifi: invalid data")
	ErrNotFound    = errors.New("wifi: unknown network")
	ErrUnsupported = errors.New("wifi: unsupported security")
	ErrScanBusy    = errors.New("wifi: scanning for networks")
)

// ProfilePrefix: the NetworkManager profiles the app creates
const ProfilePrefix = "eirl-wifi-"

// A network's security (WIFI.md §4). Only open, wpa and wpa2 can be joined in v1: enterprise (802.1X), wpa3
// (SAE only: the board's wpa_supplicant 2.6 lacks it) and wep (obsolete) show up in the list and are rejected.
const (
	SecOpen       = "open"
	SecWEP        = "wep"
	SecWPA        = "wpa"
	SecWPA2       = "wpa2"
	SecWPA3       = "wpa3"
	SecEnterprise = "enterprise"
)

// Network: a network from the scan, without secrets.
type Network struct {
	SSID      string `json:"ssid"`
	Signal    int    `json:"signal"`   // 0-100
	Security  string `json:"security"` // open | wep | wpa | wpa2 | wpa3 | enterprise
	Band      string `json:"band"`     // 2.4 | 5
	Channel   int    `json:"channel"`
	Known     bool   `json:"known"`
	Connected bool   `json:"connected"`
}

// State: how the client is doing, without secrets. Off is decided by whoever knows whether «Join a wifi» is on
// (network.json): here, disconnected | connecting | connected | error.
type State struct {
	State    string  `json:"state"`
	SSID     *string `json:"ssid"`
	Signal   *int    `json:"signal"`
	Band     *string `json:"band"`
	Channel  *int    `json:"channel"`
	IP       *string `json:"ip"`
	Internet *bool   `json:"internet"` // null: not checked yet (or nowhere to check it against)
	Error    string  `json:"error,omitempty"`
}

// --- what is needed from NetworkManager (backend: D-Bus on the board, a fake one in tests) ----------------------

type apInfo struct {
	Path            string
	SSID            []byte
	Strength        uint8
	Freq            uint32
	Flags, WPA, RSN uint32
}

type devState struct {
	State, Reason uint32
	Active        *apInfo // the network it is connected to (or trying to)
	IP            string  // its IPv4, or ""
}

type profile struct {
	Path, ID string
	SSID     []byte
}

// newProfile: what is handed to NetworkManager to create or change a profile. Empty PSK = open network.
type newProfile struct {
	ID, UUID, Iface string
	SSID            []byte
	PSK             string
}

type backend interface {
	device(ctx context.Context) (devState, error)
	requestScan(ctx context.Context) error
	accessPoints(ctx context.Context) ([]apInfo, error)
	profiles(ctx context.Context) ([]profile, error)
	addAndActivate(ctx context.Context, p newProfile, ap string) error
	update(ctx context.Context, path string, p newProfile) error
	activate(ctx context.Context, path, ap string) error
	remove(ctx context.Context, path string) error
	disconnect(ctx context.Context) error
	setAutoconnect(ctx context.Context, on bool) error
	// stateChanges: the device's state changes with their reason (the StateChanged signal), until ctx ends.
	// Polling the state is not enough: FAILED, which carries the reason (NO_SECRETS), lasts less than an interval
	stateChanges(ctx context.Context) (<-chan stateChange, error)
}

type stateChange struct{ State, Reason uint32 }

// NetworkManager device states (NMDeviceState) and failure reasons (NMDeviceStateReason) in use
const (
	devDisconnected = 30
	devPrepare      = 40
	devSecondaries  = 90
	devActivated    = 100
	devDeactivating = 110
	devFailed       = 120

	reasonNoSecrets   = 7
	reasonSSIDMissing = 53
)

// Client: the client wifi.
type Client struct {
	Iface string // wlan0
	// OwnSSID: our AP's name, which is left out of the list (wlan0 sees it). nil = no AP
	OwnSSID func() string
	// InternetURL: where internet access through the client wifi is checked (HTTPS: a captive portal cannot fake
	// the certificate): the /ping of the relay being streamed to, or the cloud API. nil or "" = not checked
	// (internet: null)
	InternetURL func() string
	Now         func() time.Time

	b backend

	opMu    sync.Mutex // connect, forget and switch on or off, one at a time (otherwise two adds at once duplicate the profile)
	mu      sync.Mutex
	scanAt  time.Time // last rescan requested
	attempt struct {
		ssid string
		at   time.Time
		err  string // wrong_password | not_found | connect_failed | ""
	}
	inetMu      sync.Mutex
	inetIP      string
	inetOK      *bool
	inetStreak  int       // consecutive checks that say the opposite of inetOK
	inetChanged time.Time // last change of inetOK
}

// New: the client wifi of that interface, through NetworkManager on the system bus.
func New(iface string) *Client {
	return &Client{Iface: iface, b: &dbusNM{iface: iface}}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// --- validation ---------------------------------------------------------------------------------------------

// ValidSSID: 1-32 bytes of UTF-8 without control characters (those that cannot be shown or typed on the screen
// are not accepted).
func ValidSSID(s string) bool {
	if len(s) < 1 || len(s) > 32 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ValidPassword: WPA/WPA2 personal, 8-63 printable ASCII characters or 64 hex digits.
func ValidPassword(p string) bool {
	if len(p) == 64 {
		for _, r := range p {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
		return true
	}
	if len(p) < 8 || len(p) > 63 {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] > 0x7e {
			return false
		}
	}
	return true
}

// security: a network's security, from its flags (NM80211ApFlags / NM80211ApSecurityFlags).
func security(a apInfo) string {
	const privacy, psk, eap, sae = 0x1, 0x100, 0x200, 0x400
	switch {
	case (a.WPA|a.RSN)&eap != 0:
		return SecEnterprise
	case a.RSN&psk != 0: // mixed WPA2/WPA3 ones too: they join as WPA2
		return SecWPA2
	case a.WPA&psk != 0:
		return SecWPA
	case a.RSN&sae != 0, a.RSN != 0 || a.WPA != 0: // RSN with neither PSK nor 802.1X: SAE an old NetworkManager does not name
		return SecWPA3
	case a.Flags&privacy != 0:
		return SecWEP
	}
	return SecOpen
}

func connectable(sec string) bool { return sec == SecOpen || sec == SecWPA || sec == SecWPA2 }

// band: 2.4 or 5 and the channel, from the frequency in MHz.
func band(freq uint32) (string, int) {
	switch {
	case freq >= 2412 && freq <= 2472:
		return "2.4", int(freq-2407) / 5
	case freq == 2484:
		return "2.4", 14
	case freq >= 5160 && freq <= 5885:
		return "5", int(freq-5000) / 5
	}
	return "", 0
}

// --- state ----------------------------------------------------------------------------------------------------

// State: how the client is doing (on/off: the caller knows).
func (c *Client) State(ctx context.Context) (State, error) {
	d, err := c.b.device(ctx)
	if err != nil {
		return State{}, err
	}
	st := State{State: "disconnected"}
	switch {
	case d.State == devActivated:
		st.State = "connected"
	case d.State >= devPrepare && d.State <= devSecondaries:
		st.State = "connecting"
	}
	if d.Active != nil && st.State != "disconnected" && utf8.Valid(d.Active.SSID) {
		ssid := string(d.Active.SSID)
		sig := int(d.Active.Strength)
		st.SSID, st.Signal = &ssid, &sig
		if b, ch := band(d.Active.Freq); b != "" {
			st.Band, st.Channel = &b, &ch
		}
	}
	c.mu.Lock()
	if st.State == "connected" && st.SSID != nil && *st.SSID == c.attempt.ssid {
		c.attempt.err = ""
	}
	if st.State != "connected" && st.State != "connecting" && c.attempt.err != "" {
		// The network that failed, for «Wrong password for X» and to ask for it again
		ssid := c.attempt.ssid
		st.State, st.Error, st.SSID = "error", c.attempt.err, &ssid
	}
	c.mu.Unlock()
	if st.State == "connected" && d.IP != "" {
		ip := d.IP
		st.IP = &ip
		st.Internet = c.internet(ip)
	}
	return st, nil
}

// --- scan -------------------------------------------------------------------------------------------------------

// scanEvery: the most often NetworkManager is asked for a scan (it also rejects two in a row)
const scanEvery = 10 * time.Second

// Scan: the networks wlan0 sees, right away (NetworkManager's latest), and requests a new scan in the background if
// the last one is older than 10 s: whoever shows the list asks for it again every few seconds. Without hidden
// SSIDs, our AP's, or names that cannot be shown; one entry per SSID (the strongest). ErrScanBusy if there is
// nothing yet.
func (c *Client) Scan(ctx context.Context) ([]Network, time.Time, error) {
	c.mu.Lock()
	at := c.scanAt
	if c.now().Sub(at) > scanEvery {
		c.scanAt = c.now()
		// A rejection («Scanning not allowed immediately») does not matter: what there is gets served and it is asked again
		go func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = c.b.requestScan(sctx)
		}()
	}
	c.mu.Unlock()
	aps, err := c.b.accessPoints(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	profs, err := c.b.profiles(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	d, _ := c.b.device(ctx)
	own := ""
	if c.OwnSSID != nil {
		own = c.OwnSSID()
	}
	best := map[string]Network{}
	for _, a := range aps {
		ssid := string(a.SSID)
		if !ValidSSID(ssid) || ssid == own {
			continue
		}
		b, ch := band(a.Freq)
		if b == "" {
			continue
		}
		n := Network{SSID: ssid, Signal: int(a.Strength), Security: security(a), Band: b, Channel: ch}
		for _, p := range profs {
			if bytes.Equal(p.SSID, a.SSID) {
				n.Known = true
			}
		}
		if d.State == devActivated && d.Active != nil && bytes.Equal(d.Active.SSID, a.SSID) {
			n.Connected = true
		}
		if old, ok := best[ssid]; !ok || n.Signal > old.Signal || n.Connected {
			n.Connected = n.Connected || old.Connected
			best[ssid] = n
		}
	}
	out := make([]Network, 0, len(best))
	for _, n := range best {
		out = append(out, n)
	}
	// The connected one first, then the known ones, then by signal
	sort.Slice(out, func(i, j int) bool {
		if out[i].Connected != out[j].Connected {
			return out[i].Connected
		}
		if out[i].Known != out[j].Known {
			return out[i].Known
		}
		if out[i].Signal != out[j].Signal {
			return out[i].Signal > out[j].Signal
		}
		return out[i].SSID < out[j].SSID
	})
	if len(out) == 0 && c.now().Sub(at) > scanEvery {
		return out, c.now(), ErrScanBusy
	}
	return out, c.now(), nil
}

// --- connect and forget -----------------------------------------------------------------------------------------

// connectWatch: how long a connection attempt is watched (NetworkManager gives up on a wrong password in ~25 s)
var connectWatch = 45 * time.Second

// Connect: to a network from the scan. Known (an eirl-wifi-* profile with that SSID) and without a password, with
// the saved profile; with a password, the profile's password is changed. Unknown: with its password if it is
// secured (otherwise, ErrInvalid). Returns as soon as connecting starts: the outcome shows up in State (connected,
// or error with wrong_password, not_found or connect_failed; the profile stays so it can be corrected).
func (c *Client) Connect(ctx context.Context, ssid string, password *string) error {
	if !ValidSSID(ssid) || (password != nil && !ValidPassword(*password)) {
		return ErrInvalid
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	aps, err := c.b.accessPoints(ctx)
	if err != nil {
		return err
	}
	var ap *apInfo
	for i := range aps {
		if string(aps[i].SSID) == ssid && (ap == nil || aps[i].Strength > ap.Strength) {
			ap = &aps[i]
		}
	}
	if ap == nil {
		return ErrNotFound // hidden ones too: they do not show up in the scan (a v1 limitation)
	}
	sec := security(*ap)
	if !connectable(sec) {
		return ErrUnsupported
	}
	psk := ""
	if password != nil && sec != SecOpen {
		psk = *password
	}
	profs, err := c.b.profiles(ctx)
	if err != nil {
		return err
	}
	var known *profile
	for i := range profs {
		if bytes.Equal(profs[i].SSID, []byte(ssid)) {
			known = &profs[i]
		}
	}
	// State changes, subscribed to BEFORE activating (otherwise the first ones are lost). Without the signal, polling
	wctx, wcancel := context.WithTimeout(context.Background(), connectWatch)
	changes, serr := c.b.stateChanges(wctx)
	if serr != nil {
		changes = nil
	}
	switch {
	case known != nil && psk == "" && sec != SecOpen:
		err = c.b.activate(ctx, known.Path, ap.Path)
	case known != nil:
		if err = c.b.update(ctx, known.Path, newProfile{ID: known.ID, UUID: "", Iface: c.Iface, SSID: []byte(ssid), PSK: psk}); err == nil {
			err = c.b.activate(ctx, known.Path, ap.Path)
		}
	case psk == "" && sec != SecOpen:
		wcancel()
		return ErrInvalid // an unknown secured network needs its password
	default:
		u, uerr := uuid4()
		if uerr != nil {
			wcancel()
			return uerr
		}
		err = c.b.addAndActivate(ctx, newProfile{ID: nextID(profs), UUID: u, Iface: c.Iface, SSID: []byte(ssid), PSK: psk}, ap.Path)
	}
	if err != nil {
		wcancel()
		return err
	}
	c.mu.Lock()
	c.attempt.ssid, c.attempt.at, c.attempt.err = ssid, c.now(), ""
	c.mu.Unlock()
	go func() {
		defer wcancel()
		if changes != nil {
			c.watchSignals(wctx, ssid, changes)
		} else {
			c.watch(wctx, ssid)
		}
	}()
	return nil
}

// failReason: an attempt's error code, from NetworkManager's reason.
func failReason(reason uint32) string {
	switch reason {
	case reasonNoSecrets:
		return "wrong_password"
	case reasonSSIDMissing:
		return "not_found"
	}
	return "connect_failed"
}

// fail: records the reason of a failed attempt (if it is still the latest).
func (c *Client) fail(ssid, msg string) {
	c.mu.Lock()
	if c.attempt.ssid == ssid {
		c.attempt.err = msg
	}
	c.mu.Unlock()
}

// activeIs: is the active network that one? (when switching networks, the previous one stays activated for a moment).
func (c *Client) activeIs(ssid string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d, err := c.b.device(ctx)
	return err == nil && d.State == devActivated && d.Active != nil && string(d.Active.SSID) == ssid
}

// watchSignals: follows an attempt through the StateChanged signal: connected (to THAT network), or the failure
// reason. FAILED carries the reason (NO_SECRETS = wrong password) and right away moves to DISCONNECTED with another
// one: FAILED's is kept.
func (c *Client) watchSignals(ctx context.Context, ssid string, changes <-chan stateChange) {
	seen := false
	var failed uint32
	for {
		select {
		case <-ctx.Done():
			if !c.activeIs(ssid) {
				c.fail(ssid, "connect_failed")
			}
			return
		case ch, ok := <-changes:
			if !ok {
				c.watch(ctx, ssid) // the signal was cut: poll for the rest of the attempt
				return
			}
			switch {
			case ch.State == devActivated:
				if c.activeIs(ssid) {
					return
				}
			case ch.State >= devPrepare && ch.State <= devSecondaries:
				seen = true
			case ch.State == devFailed:
				failed = ch.Reason
			case seen && (ch.State == devDisconnected || ch.State == devDeactivating):
				r := ch.Reason
				if failed != 0 {
					r = failed
				}
				c.fail(ssid, failReason(r))
				return
			}
		}
	}
}

// watch: follows an attempt by polling (without the StateChanged signal) until it connects to that network, fails
// or ctx ends (Connect gives it connectWatch, read there: never from this goroutine), and records a failure's
// reason. FAILED's reason may be missed (it is short-lived): connect_failed.
func (c *Client) watch(ctx context.Context, ssid string) {
	seen := false // it has already gone through «connecting» (otherwise an initial disconnected is not a failure)
	for ctx.Err() == nil {
		time.Sleep(500 * time.Millisecond)
		dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		d, err := c.b.device(dctx)
		cancel()
		if err != nil {
			continue
		}
		c.mu.Lock()
		stale := c.attempt.ssid != ssid
		c.mu.Unlock()
		if stale {
			return // another attempt after this one
		}
		switch {
		case d.State == devActivated && d.Active != nil && string(d.Active.SSID) == ssid:
			return
		case d.State >= devPrepare && d.State <= devSecondaries:
			seen = true
			continue
		case d.State == devFailed, seen && (d.State == devDisconnected || d.State == devDeactivating):
			c.fail(ssid, failReason(d.Reason))
			return
		}
	}
}

// Forget: deletes that network's profile (and disconnects if it was the connected one).
func (c *Client) Forget(ctx context.Context, ssid string) error {
	if !ValidSSID(ssid) {
		return ErrInvalid
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	profs, err := c.b.profiles(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, p := range profs {
		if bytes.Equal(p.SSID, []byte(ssid)) {
			if err := c.b.remove(ctx, p.Path); err != nil {
				return err
			}
			found = true
		}
	}
	if !found {
		return ErrNotFound
	}
	c.mu.Lock()
	if c.attempt.ssid == ssid {
		c.attempt.err = ""
	}
	c.mu.Unlock()
	return nil
}

// SetEnabled: «Join a wifi». Off: wlan0 disconnects and does not reconnect on its own to ANYTHING (the profiles
// stay; a hand-made one too: the switch rules over all of the board's client wifi). On: NetworkManager joins the
// known ones on its own. It is runtime only: at start, the app sets it again from network.json.
func (c *Client) SetEnabled(ctx context.Context, on bool) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if on {
		return c.b.setAutoconnect(ctx, true)
	}
	c.mu.Lock()
	c.attempt.err = ""
	c.mu.Unlock()
	if err := c.b.disconnect(ctx); err != nil {
		return err
	}
	return c.b.setAutoconnect(ctx, false)
}

// --- internet -------------------------------------------------------------------------------------------------

// Hysteresis of «no internet» in both directions (mixer session): each change takes the client wifi out of or puts
// it into the SRTLA split (a SIGHUP to srtla_send and a new split), and a hotel wifi that comes and goes must not
// shake the stream every 30 s. It takes internetStreak consecutive checks saying the opposite and, except the first
// time, internetDwell since the last change.
const internetStreak = 2

var internetDwell = 60 * time.Second

// internet: the latest check for that IP (null if none yet). Monitor does the checking.
func (c *Client) internet(ip string) *bool {
	c.inetMu.Lock()
	defer c.inetMu.Unlock()
	if c.inetIP != ip || c.inetOK == nil {
		return nil
	}
	v := *c.inetOK
	return &v
}

// checkNow: one check of internet access through the client wifi (if connected), and whether with it the wifi is
// «no internet» (with the hysteresis above; a first good check is accepted right away, since it does not change the
// split). Disconnected, it is not (and what was known is forgotten). Transient things (NetworkManager not answering,
// nowhere to check against, a new DHCP IP on the same network) change nothing: otherwise the hysteresis would be
// skipped.
func (c *Client) checkNow(ctx context.Context) bool {
	d, err := c.b.device(ctx)
	url := ""
	if c.InternetURL != nil {
		url = c.InternetURL()
	}
	c.inetMu.Lock()
	off := c.inetOK != nil && !*c.inetOK
	switch {
	case err != nil || url == "":
		c.inetMu.Unlock()
		return off
	case d.State != devActivated || d.IP == "":
		c.inetIP, c.inetOK, c.inetStreak = "", nil, 0
		c.inetMu.Unlock()
		return false
	}
	c.inetIP = d.IP // a new IP (DHCP renewal) is still the same network: what was known is kept
	c.inetMu.Unlock()
	ok := checkInternet(ctx, url, d.IP) // HTTPS from the client wifi's IP (outside the lock: it is slow)
	if ctx.Err() != nil {
		return off // the app is stopping: it does not count
	}
	c.inetMu.Lock()
	defer c.inetMu.Unlock()
	if c.inetIP != d.IP {
		return c.inetOK != nil && !*c.inetOK // it changed meanwhile: next round
	}
	now := c.now()
	switch {
	case c.inetOK == nil && ok: // the first good one: it does not change the split
		c.inetOK, c.inetStreak, c.inetChanged = &ok, 0, now
	case c.inetOK != nil && *c.inetOK == ok:
		c.inetStreak = 0
	default:
		c.inetStreak++
		if c.inetStreak >= internetStreak && (c.inetOK == nil || now.Sub(c.inetChanged) >= internetDwell) {
			c.inetOK, c.inetStreak, c.inetChanged = &ok, 0, now
		}
	}
	return c.inetOK != nil && !*c.inetOK
}

// Monitor: checks the client wifi's internet access every `every` (also with nobody watching) and calls onChange at
// the start and every time whether it is «no internet» changes. The app uses it to take it out of the SRTLA split
// (WIFI.md §13): a captive portal gives a connection and a gateway, but no internet.
//
// If onChange fails (it could not write), it is retried on the next round.
func (c *Client) Monitor(ctx context.Context, every time.Duration, onChange func(noInternet bool) error) {
	first, last := true, false
	for {
		off := c.checkNow(ctx)
		if (first || off != last) && ctx.Err() == nil && onChange(off) == nil {
			first, last = false, off
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// checkInternet: HEAD over HTTPS to url from ip, with the certificate verified (a portal that intercepts HTTPS is
// «no internet» too). With ctx: when the app stops it does not wait for the timeout.
var checkInternet = func(ctx context.Context, url, ip string) bool {
	d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}, Timeout: 5 * time.Second}
	cl := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{DialContext: d.DialContext,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false
	}
	resp, err := cl.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// --- helpers --------------------------------------------------------------------------------------------------

// nextID: eirl-wifi-<n>, the first free number.
func nextID(profs []profile) string {
	used := map[string]bool{}
	for _, p := range profs {
		used[p.ID] = true
	}
	for i := 1; ; i++ {
		id := ProfilePrefix + strconv.Itoa(i)
		if !used[id] {
			return id
		}
	}
}

func uuid4() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
