package cloud

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// Timing: channel intervals. The contract values are in DefaultTiming; tests shorten them.
type Timing struct {
	StatusIdle  time.Duration // status with nobody watching (60 s)
	StatusWatch time.Duration // status with watch (2 s)
	ThumbEvery  time.Duration // thumbnail with watch (5 s)
	WatchTTL    time.Duration // watch expires unless renewed (60 s)
	ReadTimeout time.Duration // nothing from the cloud in this time means the connection is dead (the cloud pings every 25 s)
	UplinkEvery time.Duration // how often the uplinks are checked (uplink_up/down)
	Debounce    time.Duration // after a journal line, when to check whether the phase changed
	BurstFor    time.Duration // after a command or a config.set, the phase is checked at watch pace for this long (30 s)
	MinBackoff  time.Duration // reconnection: 1 s → 60 s with ±30 % jitter
	MaxBackoff  time.Duration
	StableAfter time.Duration // connected this long, the wait starts again from MinBackoff
	TryLater    time.Duration // minimum wait after 1013 (flag off or maintenance)
	PollEvery   time.Duration // pairing poll; 0 = the `interval` the backend says
}

func DefaultTiming() Timing {
	return Timing{
		StatusIdle: 60 * time.Second, StatusWatch: 2 * time.Second, ThumbEvery: 5 * time.Second,
		WatchTTL: 60 * time.Second, ReadTimeout: 90 * time.Second, UplinkEvery: 5 * time.Second,
		Debounce: 1500 * time.Millisecond, MinBackoff: time.Second, MaxBackoff: 60 * time.Second,
		StableAfter: 60 * time.Second, TryLater: 30 * time.Second, BurstFor: 30 * time.Second,
	}
}

// Client: pairing and channel with the cloud. Init, then Run in a goroutine; Pair, Unlink and View
// are called by the local API.
type Client struct {
	APIBase string // -cloud-api: where to link (an existing link uses its own, State.APIBase)
	AppBase string // -cloud-app: for the pairing link
	State   *StateFile
	// Identity: the device's secret (§3.1). Created when the first code is requested and never deleted
	// on unlink.
	Identity  *IdentityFile
	Device    Device
	Log       *slog.Logger
	HTTP      *http.Client // nil = 15 s timeout
	UserAgent string
	// Lang: the device language for the messages of command results (nil = English). Codes never change
	Lang   func() string
	Timing Timing // zero = DefaultTiming

	linked atomic.Bool // config.Store reads it while holding its lock: without going through mu

	mu       sync.Mutex
	root     context.Context
	st       State
	gen      int // changes on every link/unlink: anything from an earlier generation touches nothing
	pairing  *pairing
	lastErr  string // why it is not linked (denied, expired, unlinked from the app…)
	online   bool
	halted   string // replaced | upgrade_required: no retry until restart
	stopConn context.CancelFunc
	sessDone chan struct{} // closed when the current session has fully finished (its work too)
	pauseTil time.Time
	kick     chan struct{}
}

type pairing struct {
	id, code, secret string
	expiresAt        time.Time
	interval         time.Duration
	cancel           context.CancelFunc
}

// lastErr reasons (besides the §3.1 denial ones, among them hardware_claimed: the device is still
// linked to another account and the request did not carry its identity).
const (
	ReasonExpired  = "expired"  // the code expired without being confirmed
	ReasonRevoked  = "revoked"  // unlinked from the app (4401)
	ReasonUnlinked = "unlinked" // the cloud sent unlink
	ReasonSave     = "save_failed"
)

// Init loads the stored link. ctx lives as long as the app. An unreadable file leaves the device
// unlinked (and says so in the log); it does not prevent startup.
func (c *Client) Init(ctx context.Context) error {
	if c.Timing == (Timing{}) {
		c.Timing = DefaultTiming()
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.root = ctx
	c.kick = make(chan struct{}, 1)
	st, ok, err := c.State.Load()
	if err != nil {
		// Neither deleted nor does the app stop: unlinked until it is fixed or linked again (which
		// overwrites it)
		c.Log.Error("cannot read the EnhancedIRL link: the device stays unlinked", "err", err)
		return nil
	}
	if ok {
		c.st = st
		c.linked.Store(true)
		if st.APIBase != c.APIBase {
			c.Log.Warn("the device is linked to a backend other than -cloud-api: using the link's", "link", st.APIBase, "flag", c.APIBase)
		}
	}
	return nil
}

// ManagedBy: "eirl" when linked, "local" otherwise (config.Store.ManagedBy).
func (c *Client) ManagedBy() string {
	if c.linked.Load() {
		return "eirl"
	}
	return "local"
}

// Linked: the device is linked now (CloudDevice checks it before writing a config.set).
func (c *Client) Linked() bool { return c.linked.Load() }

// CloudDestination: the destination on disk was set by the cloud (cloud.json). Only the migration to saved
// destinations reads it (config.Store.CloudOwned); after it, the cloud's destination is the one with id eirl.
func (c *Client) CloudDestination() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.linked.Load() && c.st.CloudDestination
}

// View: what the local web shows (GET /api/v1/cloud).
type View struct {
	State        string     `json:"state"` // unlinked | pairing | linked
	Code         string     `json:"code,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	LinkURL      string     `json:"linkUrl,omitempty"`
	AccountLabel string     `json:"accountLabel,omitempty"`
	Online       *bool      `json:"online,omitempty"`
	Halted       string     `json:"halted,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
}

func (c *Client) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.linked.Load():
		online := c.online
		v := View{State: "linked", AccountLabel: c.st.AccountLabel, Online: &online, Halted: c.halted}
		if c.lastErr == ReasonSave { // linked only in memory: the web has to say so
			v.LastError = c.lastErr
		}
		return v
	case c.pairing != nil:
		exp := c.pairing.expiresAt.UTC()
		return View{State: "pairing", Code: c.pairing.code, ExpiresAt: &exp, LinkURL: c.linkURL(c.pairing.code)}
	}
	return View{State: "unlinked", LastError: c.lastErr}
}

func (c *Client) linkURL(code string) string {
	if c.AppBase == "" {
		return ""
	}
	return joinBase(c.AppBase, "/encoders/link?pairing="+url.QueryEscape(code))
}

// Pair starts pairing or renews the code (§3.1) and polls in the background until it is confirmed,
// denied or expires.
func (c *Client) Pair(ctx context.Context) (View, error) {
	if c.linked.Load() {
		return c.View(), ErrAlreadyLinked
	}
	if c.Identity == nil {
		return c.View(), ErrNoIdentity
	}
	identity, err := c.Identity.LoadOrCreate()
	if err != nil {
		c.Log.Error("device identity", "err", err) // the error never carries the value
		return c.View(), ErrNoIdentity
	}
	resp, err := c.requestPairing(ctx, c.Device.Info(), identity)
	if err != nil {
		return c.View(), err
	}
	c.mu.Lock()
	if c.linked.Load() { // linked some other way in the meantime
		c.mu.Unlock()
		return c.View(), ErrAlreadyLinked
	}
	if c.pairing != nil {
		c.pairing.cancel()
	}
	exp := time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	pctx, cancel := context.WithDeadline(c.root, exp)
	p := &pairing{id: resp.PairingID, code: resp.Code, secret: resp.PollSecret, expiresAt: exp,
		interval: time.Duration(resp.Interval) * time.Second, cancel: cancel}
	if c.Timing.PollEvery > 0 {
		p.interval = c.Timing.PollEvery
	}
	c.pairing, c.lastErr = p, ""
	c.mu.Unlock()
	c.Log.Info("pairing: code requested", "expires", exp.UTC().Format(time.RFC3339)) // the code is not logged
	go c.poll(pctx, p)
	return c.View(), nil
}

func (c *Client) poll(ctx context.Context, p *pairing) {
	interval := p.interval
	for {
		select {
		case <-ctx.Done():
			c.endPairing(p, ReasonExpired)
			return
		case <-time.After(interval):
		}
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		r, err := c.pollToken(rctx, p.id, p.secret)
		cancel()
		switch {
		case errors.Is(err, ErrRateLimited):
			interval = min(interval*2, 30*time.Second)
		case err != nil: // no network or odd response: keep trying until it expires
			if ctx.Err() == nil {
				c.Log.Warn("pairing: poll failed", "err", err)
			}
		case r.Status == "pending":
		case r.Status == "approved":
			c.completePairing(p, r)
			return
		case r.Status == "denied":
			c.Log.Info("pairing denied", "reason", r.Reason)
			c.endPairing(p, r.Reason)
			return
		default: // gone
			c.endPairing(p, ReasonExpired)
			return
		}
	}
}

func (c *Client) endPairing(p *pairing, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pairing == p {
		p.cancel()
		c.pairing, c.lastErr = nil, reason
	}
}

func (c *Client) completePairing(p *pairing, r pollResult) {
	st := State{EncoderID: r.EncoderID, Token: r.Token, APIBase: c.APIBase, AccountLabel: r.AccountLabel, AccessPoint: AccessPointRescue}
	c.mu.Lock()
	if c.pairing != p { // cancelled in the meantime (DELETE /api/v1/cloud): the token is lost and expires in the cloud
		c.mu.Unlock()
		return
	}
	p.cancel()
	c.pairing = nil
	// The token is delivered only once: if it cannot be saved, stay linked in memory (until the app
	// restarts) instead of losing it
	c.lastErr = ""
	if err := c.State.Save(st); err != nil {
		c.Log.Error("cannot save the link: valid until the app restarts", "err", err)
		c.lastErr = ReasonSave
	}
	c.st, c.halted, c.online = st, "", false
	c.gen++
	c.linked.Store(true)
	c.mu.Unlock()
	c.Log.Info("linked to an EnhancedIRL account", "account", st.AccountLabel, "encoderId", st.EncoderID)
	c.wake()
}

// Unlink unlinks from the local web: cuts the channel, deletes the state (even if the cloud does not
// answer) and tells the cloud with DELETE /encoders/self. With a pairing in progress, it cancels it.
// cloudNotified: false if the cloud could not be told (the user should remove it in the app too).
func (c *Client) Unlink(ctx context.Context) (cloudNotified bool) {
	c.mu.Lock()
	if c.pairing != nil {
		c.pairing.cancel()
		c.pairing = nil
	}
	c.lastErr = ""
	if !c.linked.Load() {
		c.mu.Unlock()
		return true
	}
	st, gen := c.st, c.gen
	c.mu.Unlock()
	// Local state first (so the cloud does not close us with 4401 in the meantime)
	c.dropLink(gen, "")
	if err := c.deleteSelf(ctx, st); err != nil {
		c.Log.Warn("unlinked on the device, but the cloud has not confirmed it", "err", err)
		return false
	}
	c.Log.Info("unlinked")
	return true
}

// dropLink: stops being linked (from here, by 4401 or by the cloud's unlink). Only if gen is the
// current generation: a late close from an earlier link does not delete the new one.
// It cuts the session and WAITS for whatever it had in progress (a config.set being applied) before
// removing the destination: otherwise that config.set could write the key again afterwards. Never
// called from inside the session (it would wait for itself): Run handles a cloud unlink when it ends.
func (c *Client) dropLink(gen int, reason string) {
	c.mu.Lock()
	if gen != c.gen || !c.linked.Load() {
		c.mu.Unlock()
		return
	}
	st := c.st
	// The file first: if the app died right after, it would not link itself again on startup
	if err := c.State.Clear(); err != nil {
		c.Log.Error("cannot delete the stored link", "err", err)
	}
	c.st, c.online, c.halted, c.lastErr = State{}, false, "", reason
	c.gen++
	c.linked.Store(false)
	stop, done := c.stopConn, c.sessDone
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(45 * time.Second): // a config.set never takes this long (30 s cap)
			c.Log.Error("unlinking: the session does not end; carrying on")
		}
	}
	ctx, cancel := context.WithTimeout(c.root, 30*time.Second)
	defer cancel()
	if err := c.Device.Unlinked(ctx, st.CloudDestination); err != nil {
		c.Log.Error("after unlinking: could not remove the EnhancedIRL destination", "err", err)
	}
	c.wake()
}

// Disconnect cuts the current connection and does not reconnect until pause has passed (the
// simulator uses it to mimic a restart).
func (c *Client) Disconnect(pause time.Duration) {
	c.mu.Lock()
	c.pauseTil = time.Now().Add(pause)
	stop := c.stopConn
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (c *Client) wake() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// Run keeps the channel up while the device is linked: it connects and, when cut, retries with
// exponential backoff (1 s → 60 s, ±30 % jitter) except on a final close (4401, 4409, 4426).
func (c *Client) Run(ctx context.Context) {
	attempt, unauthorized := 0, 0
	for ctx.Err() == nil {
		c.mu.Lock()
		st, gen, halted, pause := c.st, c.gen, c.halted, time.Until(c.pauseTil)
		linked := c.linked.Load()
		c.mu.Unlock()
		if !linked || halted != "" {
			select {
			case <-ctx.Done():
				return
			case <-c.kick:
			}
			attempt, unauthorized = 0, 0
			continue
		}
		if pause > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pause):
			}
		}
		sctx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		if gen != c.gen { // the link changed in between
			c.mu.Unlock()
			cancel()
			continue
		}
		done := make(chan struct{})
		c.stopConn, c.sessDone = cancel, done
		c.mu.Unlock()
		res := c.session(sctx, st, gen)
		cancel()
		c.mu.Lock()
		c.stopConn, c.sessDone = nil, nil
		c.online = false
		stillOurs := gen == c.gen
		c.mu.Unlock()
		close(done)
		if ctx.Err() != nil {
			return
		}
		if !stillOurs { // unlinked or linked again during the session
			attempt, unauthorized = 0, 0
			continue
		}
		if res.unlinked { // the cloud sent unlink: the session has fully finished
			c.dropLink(gen, ReasonUnlinked)
			continue
		}
		if res.httpStatus == http.StatusUnauthorized {
			unauthorized++
		} else if res.connected {
			unauthorized = 0
		}
		wait := TryLaterFloor(res.code, c.Timing)
		switch {
		case res.code == CloseRevoked || unauthorized >= 2:
			// A single 401 on the upgrade may be a transient backend failure: with two in a row, the
			// token is no longer valid (unlinked while the device was off)
			c.Log.Info("the cloud revoked the link: the device goes back to unlinked", "code", res.code, "http", res.httpStatus)
			c.dropLink(gen, ReasonRevoked)
			continue
		case res.code == CloseReplaced:
			c.halt(gen, "replaced")
			continue
		case res.code == CloseUpgradeRequired || res.httpStatus == http.StatusUpgradeRequired:
			c.halt(gen, "upgrade_required")
			continue
		}
		if res.connectedFor >= c.Timing.StableAfter {
			attempt = 0
		}
		d := max(backoff(attempt, c.Timing.MinBackoff, c.Timing.MaxBackoff), wait)
		attempt++
		c.Log.Info("cloud channel closed: retrying", "code", res.code, "http", res.httpStatus, "err", errString(res.err), "in", d.Round(100*time.Millisecond).String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		case <-c.kick: // unlinked or linked again: re-evaluate now
		}
	}
}

// TryLaterFloor: after 1013 (flag off, maintenance) do not retry before Timing.TryLater.
func TryLaterFloor(code int, t Timing) time.Duration {
	if code == CloseTryLater {
		return t.TryLater
	}
	return 0
}

func (c *Client) halt(gen int, why string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	c.halted = why
	c.Log.Warn("cloud channel halted until the app restarts", "reason", why)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// lang: the device language (English without Lang).
func (c *Client) lang() string {
	if c.Lang != nil {
		return c.Lang()
	}
	return i18n.EN
}

// msg: a catalog message in the device language (the messages of command results; SETUP.md §3).
func (c *Client) msg(key string, args ...any) string { return i18n.Msg(c.lang(), key, args...) }
