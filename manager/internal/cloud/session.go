package cloud

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
)

type sessionResult struct {
	code         int // the cloud's close code (-1 if none)
	httpStatus   int // HTTP response to the upgrade, if it failed
	connected    bool
	connectedFor time.Duration
	err          error
	unlinked     bool // the cloud sent unlink (Run unlinks once the session has fully finished)
}

// wsHTTP: the upgrade client. No Timeout (the dial context sets it: with Timeout, the established
// connection would inherit the deadline) and HTTP/1.1 only (the WebSocket upgrade does not go over HTTP/2).
var wsHTTP = &http.Client{CheckRedirect: noRedirects, Transport: func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ForceAttemptHTTP2 = false
	t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	return t
}()}

// session: one channel connection, from start to end.
func (c *Client) session(ctx context.Context, st State, gen int) (res sessionResult) {
	res.code = -1
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, resp, err := websocket.Dial(dctx, wsURL(st.APIBase), &websocket.DialOptions{
		HTTPClient:   c.wsClient(),
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + st.Token}, "User-Agent": {c.userAgent()}},
		Subprotocols: []string{Subprotocol},
	})
	cancel()
	if err != nil {
		if resp != nil {
			res.httpStatus = resp.StatusCode
		}
		res.err = errors.New("could not open the channel")
		if res.httpStatus == 0 {
			res.err = err
		}
		return res
	}
	defer conn.CloseNow()
	if p := conn.Subprotocol(); p != Subprotocol {
		if p != "" { // another protocol: this device does not speak it
			conn.Close(websocket.StatusPolicyViolation, "subprotocol")
			res.code = CloseUpgradeRequired
			return res
		}
		c.Log.Warn("the cloud did not confirm the subprotocol; carrying on")
	}
	conn.SetReadLimit(MaxMessage)
	start := time.Now()
	res.connected = true
	defer func() { res.connectedFor = time.Since(start) }()

	s := &session{c: c, conn: conn, ctx: ctx, gen: gen, limiter: newBucket(10, 20)}
	c.mu.Lock()
	if gen == c.gen {
		c.online = true
	}
	c.mu.Unlock()
	c.Log.Info("cloud channel open")
	code, err := s.run()
	res.code, res.err, res.unlinked = code, err, s.unlinked
	return res
}

func (c *Client) wsClient() *http.Client {
	if c.HTTP != nil && c.HTTP.Transport != nil {
		return &http.Client{Transport: c.HTTP.Transport, CheckRedirect: noRedirects}
	}
	return wsHTTP
}

type session struct {
	c       *Client
	conn    *websocket.Conn
	ctx     context.Context
	gen     int
	wmu     sync.Mutex
	limiter *bucket

	unlinked bool // set by run before returning

	// Only touched by the main goroutine (run)
	lastPhase   string
	lastOutput  string
	lastUplinks map[string]bool
	watchUntil  time.Time
	lastThumbAt time.Time
}

const writeTimeout = 10 * time.Second

// send writes a message. Critical ones (hello, result, config.applied, pong) always go through;
// status, event and thumbnail are dropped above 10/s (the backend's maximum: 20/s).
func (s *session) send(v any, critical bool) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxMessage {
		return errors.New("message too large")
	}
	if !critical && !s.limiter.take(time.Now()) {
		return nil
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	wctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
	defer cancel()
	return s.conn.Write(wctx, websocket.MessageText, b)
}

func (s *session) run() (int, error) {
	c, t := s.c, s.c.Timing
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()

	if err := s.send(s.hello(), true); err != nil {
		return -1, err
	}

	// Reading: one message at a time. With nothing from the cloud in ReadTimeout (it pings every 25 s)
	// the connection is dead: the expired context closes the socket.
	in := make(chan inbound, 16)
	readErr := make(chan error, 1)
	// Incoming traffic is capped too (the contract's 20/s): a backend bursting watch or ping does not
	// burn the device's CPU or mobile data. The excess is dropped, the connection is not closed.
	inLimit := newBucket(20, 40)
	go func() {
		for {
			rctx, rcancel := context.WithTimeout(ctx, t.ReadTimeout)
			typ, data, err := s.conn.Read(rctx)
			rcancel()
			if err != nil {
				readErr <- err
				return
			}
			if typ != websocket.MessageText || !inLimit.take(time.Now()) {
				continue
			}
			var m inbound
			if json.Unmarshal(data, &m) != nil || m.Type == "" {
				continue // whatever does not fit is dropped
			}
			select {
			case in <- m:
			case <-ctx.Done():
				return
			}
		}
	}()

	// config.set and action, in order and outside the loop (a restart takes seconds and ping does not wait)
	work := make(chan inbound, 8)
	check := make(chan struct{}, 1)
	requestCheck := func() {
		select {
		case check <- struct{}{}:
		default:
		}
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-work:
				s.handleWork(ctx, m)
				requestCheck()
			}
		}
	}()

	// Streaming journal: events and a hint that the phase may have changed
	journal := make(chan service.Entry, 32)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			c.Device.Follow(ctx, func(e service.Entry) {
				select {
				case journal <- e:
				default: // burst: the line is lost, reading is not slowed down
				}
			})
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second): // journalctl died: retry without hammering
			}
		}
	}()

	statusT := time.NewTimer(0) // the first snapshot, right after hello
	defer statusT.Stop()
	// Check the phase after a hint: the FIRST hint sets the deadline (later ones do not extend it;
	// otherwise a burst of lines would postpone it forever)
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	defer debounce.Stop()
	checkPending := false
	scheduleCheck := func(d time.Duration) {
		if !checkPending {
			checkPending = true
			debounce.Reset(d)
		}
	}
	uplinkT := time.NewTicker(t.UplinkEvery)
	defer uplinkT.Stop()
	var thumbT *time.Ticker
	var thumbC <-chan time.Time
	watchT := time.NewTimer(time.Hour)
	watchT.Stop()
	defer watchT.Stop()
	stopWatch := func() {
		s.watchUntil = time.Time{}
		if thumbT != nil {
			thumbT.Stop()
			thumbT, thumbC = nil, nil
		}
		watchT.Stop()
	}
	defer stopWatch()
	// Burst after a command or a config.set: with nobody watching, the snapshot is every 60 s, and a
	// start takes a while to get a signal without writing anything to the journal (Stopped → Rehearsal
	// showed up ~30 s late in the list). During BurstFor the phase is checked at watch pace and sent
	// only if it changes.
	var burstUntil time.Time
	inBurst := func() bool { return !s.watching() && time.Now().Before(burstUntil) }
	nextStatus := func() time.Duration {
		if s.watching() || inBurst() {
			return t.StatusWatch
		}
		return t.StatusIdle
	}
	s.observeUplinks()

	for {
		select {
		case <-ctx.Done():
			s.conn.Close(websocket.StatusNormalClosure, "")
			return -1, ctx.Err()
		case err := <-readErr:
			return int(websocket.CloseStatus(err)), err
		case m := <-in:
			switch m.Type {
			case "ping":
				if err := s.send(pongMsg{Type: "pong"}, true); err != nil {
					return -1, err
				}
			case "watch":
				if m.On == nil {
					continue
				}
				if !*m.On {
					if s.watching() {
						c.Log.Info("cloud: watch off")
					}
					stopWatch()
					statusT.Reset(t.StatusIdle)
					continue
				}
				was := s.watching()
				s.watchUntil = time.Now().Add(t.WatchTTL)
				watchT.Reset(t.WatchTTL)
				if !was {
					c.Log.Info("cloud: watch on")
					thumbT = time.NewTicker(t.ThumbEvery)
					thumbC = thumbT.C
					s.sendStatus(true)
					s.sendThumbnail()
					statusT.Reset(t.StatusWatch)
				}
			case "config.set", "action":
				select {
				case work <- m:
				default:
					s.busy(m)
				}
			case "unlink":
				c.Log.Info("the cloud unlinked the device")
				s.conn.Close(websocket.StatusNormalClosure, "")
				s.unlinked = true
				return -1, nil // the defers wait for work in progress; Run unlinks afterwards
			}
		case <-watchT.C: // expired without renewal: the app stopped watching (or the message was lost)
			c.Log.Info("cloud: watch expired")
			stopWatch()
			statusT.Reset(t.StatusIdle)
		case <-statusT.C:
			if inBurst() {
				s.sendStatus(false) // only if the phase changed
			} else {
				s.sendStatus(true)
			}
			statusT.Reset(nextStatus())
		case <-thumbC:
			s.sendThumbnail()
		case <-uplinkT.C:
			s.observeUplinks()
		case e := <-journal:
			if journalEvents[e.Kind] {
				s.sendEvent(e.Kind, e.TS, journalDetail(e))
			}
			if phaseKinds[e.Kind] {
				scheduleCheck(t.Debounce)
			}
		case <-check: // a command or a config.set finished
			scheduleCheck(t.Debounce)
			if t.BurstFor > 0 {
				burstUntil = time.Now().Add(t.BurstFor)
				if !s.watching() {
					statusT.Reset(t.StatusWatch)
				}
			}
		case <-debounce.C:
			checkPending = false
			// Did the phase change? If so, snapshot right away. A restart in progress is checked again
			phase, sent := s.sendStatus(false)
			if phase == PhaseRestarting {
				scheduleCheck(3 * t.Debounce)
			}
			if sent && !s.watching() && !inBurst() {
				statusT.Reset(t.StatusIdle)
			}
		}
	}
}

// phaseKinds: journal lines after which the phase may have changed (start, stop, signal, failure).
// Frequent ones that do not change it (adaptive bitrate, SRTLA uplinks, audio) do not trigger a
// snapshot: every snapshot runs `systemctl show`.
var phaseKinds = map[string]bool{
	"start": true, "stop": true, "failed": true, "restart": true, "config_invalid": true,
	"signal_lost": true, "waiting_signal": true, "signal": true, "mode_changed": true,
	"signal_unsupported": true, "pipeline_exit": true, "pipeline_error": true, "rehearsal": true,
	"srt_target": true, "device_missing": true, "device_lost": true, "device": true,
}

func (s *session) watching() bool { return !s.watchUntil.IsZero() && time.Now().Before(s.watchUntil) }

func (s *session) hello() helloMsg {
	c := s.c
	info := c.Device.Info()
	rep, err := c.Device.Reported()
	if err != nil {
		c.Log.Error("cannot read the configuration for hello", "err", err)
	}
	c.mu.Lock()
	applied := c.st.AppliedVersion
	c.mu.Unlock()
	// The board's real policy wins over the one stored from the cloud: on linking, the cloud adopts
	// what hello says, and with cloud.json's (rescue by default) the next config.set would switch off
	// the cameras' AP (safe order, WIFI.md §7)
	if rep.AccessPoint == "" {
		rep.AccessPoint = s.accessPoint()
	}
	if rep.Uplinks.Disabled == nil {
		rep.Uplinks.Disabled = []string{}
	}
	caps := append([]string{}, info.Capabilities...)
	return helloMsg{
		Type: "hello", Protocol: ProtocolVersion, Model: cleanText(info.Model, 64),
		FirmwareVersion: cleanText(info.FirmwareVersion, 64), Capabilities: caps,
		AppliedVersion: applied, Config: rep, LanURLs: c.Device.LanURLs(),
	}
}

// sendStatus takes a snapshot; it sends it if force or if the phase changed (and then also the
// stream_started/stream_stopped events). Returns the phase and whether it was sent.
func (s *session) sendStatus(force bool) (string, bool) {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	st := s.c.Device.Status(ctx)
	cancel()
	st.Normalize()
	if st.At.IsZero() {
		st.At = time.Now().UTC()
	}
	prev := s.lastPhase
	// Also a change in how the output is doing (connecting → streaming → failure): the app shows it right away
	changed := prev != st.Phase || s.lastOutput != st.OutputState.State
	if !force && !changed {
		return st.Phase, false
	}
	// A phase change (and stream_started/stream_stopped) is critical: it bypasses the periodic rate
	// limit. If it could not be sent, lastPhase does not change and it is retried on the next snapshot.
	if err := s.send(statusMsg{Type: "status", Status: st}, changed); err != nil {
		return st.Phase, false
	}
	s.lastPhase = st.Phase
	s.lastOutput = st.OutputState.State
	if prev != "" && prev != st.Phase {
		switch {
		case st.Phase == PhaseStreaming:
			s.sendEventCritical(EventStreamStarted, st.At, map[string]any{"transport": st.Transport})
		case prev == PhaseStreaming:
			s.sendEventCritical(EventStreamStopped, st.At, map[string]any{"phase": st.Phase})
		}
	}
	return st.Phase, true
}

func (s *session) sendThumbnail() {
	th, ok := s.c.Device.Thumbnail()
	if !ok || !th.At.After(s.lastThumbAt) { // no new thumbnail: the previous one is not repeated
		return
	}
	s.lastThumbAt = th.At
	s.send(thumbnailMsg{Type: "thumbnail", JPEG: base64.StdEncoding.EncodeToString(th.JPEG), Width: th.Width, Height: th.Height, At: th.At}, false)
}

func (s *session) sendEvent(name string, at time.Time, detail map[string]any) {
	s.event(name, at, detail, false)
}

func (s *session) sendEventCritical(name string, at time.Time, detail map[string]any) {
	s.event(name, at, detail, true)
}

func (s *session) event(name string, at time.Time, detail map[string]any, critical bool) {
	if at.IsZero() {
		at = time.Now()
	}
	s.send(eventMsg{Type: "event", Name: name, At: at.UTC(), Detail: detail}, critical)
}

// observeUplinks: uplink_up/uplink_down when an uplink appears or disappears (not on the first look).
func (s *session) observeUplinks() {
	now := map[string]bool{}
	for _, n := range s.c.Device.Uplinks() {
		now[n] = true
	}
	if s.lastUplinks != nil {
		var up, down []string
		for n := range now {
			if !s.lastUplinks[n] {
				up = append(up, n)
			}
		}
		for n := range s.lastUplinks {
			if !now[n] {
				down = append(down, n)
			}
		}
		sort.Strings(up)
		sort.Strings(down)
		for _, n := range up {
			s.sendEvent(EventUplinkUp, time.Now(), map[string]any{"uplink": n})
		}
		for _, n := range down {
			s.sendEvent(EventUplinkDown, time.Now(), map[string]any{"uplink": n})
		}
	}
	s.lastUplinks = now
}

func journalDetail(e service.Entry) map[string]any {
	d := map[string]any{}
	if e.Msg != "" { // already redacted by service.ParseJournal
		d["message"] = cleanText(e.Msg, 256)
	}
	if e.Step != "" {
		d["step"] = e.Step
	}
	if e.OK != nil {
		d["ok"] = *e.OK
	}
	if len(d) == 0 {
		return nil
	}
	return d
}

func (s *session) busy(m inbound) {
	if m.Type == "action" {
		s.send(resultMsg{Type: "result", ID: m.ID, OK: false, Error: "busy", Message: s.c.msg("cloud.busy")}, true)
	} else {
		s.send(appliedMsg{Type: "config.applied", Version: m.Version, Errors: map[string]string{"config": CodeApplyFailed}}, true)
	}
}

// --- config.set and action ----------------------------------------------------------------------

func (s *session) handleWork(ctx context.Context, m inbound) {
	if m.Type == "config.set" {
		s.applyConfig(ctx, m)
	} else {
		s.action(ctx, m)
	}
}

func (s *session) applyConfig(ctx context.Context, m inbound) {
	c := s.c
	reply := appliedMsg{Type: "config.applied", Version: m.Version}
	defer func() { s.send(reply, true) }()
	w, ferrs := ParseWireConfig(m.Config)
	if ferrs != nil {
		reply.Errors = errorCodes(ferrs)
		c.Log.Warn("cloud: invalid config.set", "version", m.Version, "fields", fieldNames(ferrs))
		return
	}
	c.mu.Lock()
	applied := c.st.AppliedVersion
	c.mu.Unlock()
	if m.Version < 1 || m.Version < applied {
		reply.Errors = map[string]string{"version": CodeInvalid}
		return
	}
	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	res, err := c.Device.ApplyConfig(actx, w, m.Apply)
	cancel()
	// As soon as the cloud's destination is on disk it counts as the cloud's (deleted on unlink), even
	// if the restart or the uplinks fail afterwards
	if res.Saved {
		s.saveState(func(st *State) {
			// Without `destination` on the wire the destination on disk does not change: neither does
			// the flag (if the cloud set it, it is still deleted on unlink)
			if !w.KeepDestination {
				st.CloudDestination = w.Destination != nil
			}
			if w.AccessPoint != "" {
				st.AccessPoint = w.AccessPoint
			}
		})
	}
	var fe config.FieldErrors
	switch {
	case errors.As(err, &fe):
		reply.Errors = errorCodes(fe)
		c.Log.Warn("cloud: config.set rejected", "version", m.Version, "fields", fieldNames(fe))
		return
	case err != nil:
		// Saved (or half saved) but not applied: it does not count as applied; the cloud resends it
		reply.Errors = map[string]string{"config": CodeApplyFailed}
		c.Log.Error("cloud: config.set not applied", "version", m.Version, "saved", res.Saved, "err", err)
		return
	}
	reply.OK, reply.Restarted = true, res.Restarted
	// What actually resulted (no destination → rehearsal): the cloud does not have to infer it
	if rep, err := c.Device.Reported(); err == nil {
		if rep.AccessPoint == "" {
			rep.AccessPoint = s.accessPoint()
		}
		reply.Config = &rep
	}
	s.saveState(func(st *State) { st.AppliedVersion = m.Version })
	c.Log.Info("cloud: configuration applied", "version", m.Version, "apply", m.Apply, "restarted", res.Restarted)
}

// saveState changes the stored state only if it is still the same link.
func (s *session) saveState(fn func(st *State)) {
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.gen != c.gen || !c.linked.Load() {
		return
	}
	fn(&c.st)
	if err := c.State.Save(c.st); err != nil {
		c.Log.Error("cannot save the link state", "err", err)
	}
}

func (s *session) accessPoint() string {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if s.c.st.AccessPoint == "" {
		return AccessPointRescue
	}
	return s.c.st.AccessPoint
}

// config.applied.errors codes (§4.1): the app translates them per field.
const (
	CodeInvalid     = "invalid"
	CodeApplyFailed = "apply_failed"
)

// errorCodes: validation messages (local only) → contract codes.
func errorCodes(fe map[string]string) map[string]string {
	out := make(map[string]string, len(fe))
	for k, v := range fe {
		if v == ErrDestinationNotAllowed {
			out[k] = v
		} else {
			out[k] = CodeInvalid
		}
	}
	return out
}

func fieldNames(fe map[string]string) []string {
	out := make([]string, 0, len(fe))
	for k := range fe {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var actions = map[string]bool{"start": true, "stop": true, "rehearsal": true, "restart": true, "reboot": true, "set_local_password": true}

func (s *session) action(ctx context.Context, m inbound) {
	c := s.c
	reply := resultMsg{Type: "result", ID: m.ID}
	if len(m.ID) > 128 {
		reply.ID = ""
		reply.Error, reply.Message = "bad_request", c.msg("cloud.id_too_long")
		s.send(reply, true)
		return
	}
	if !actions[m.Name] {
		reply.Error, reply.Message = "unknown_action", c.msg("common.unknown_action")
		s.send(reply, true)
		return
	}
	c.Log.Info("cloud: action", "action", m.Name) // never the password
	if m.Name == "reboot" {
		// ok only if it really can (the means and the sudo permission exist); answered BEFORE
		// rebooting: afterwards there is nothing left to answer with
		cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
		err := c.Device.CanReboot(cctx)
		ccancel()
		if err != nil {
			c.Log.Warn("cloud: reboot not available", "err", err)
			reply.Error, reply.Message = "unsupported", c.msg("cloud.no_remote_reboot")
			s.send(reply, true)
			return
		}
		reply.OK = true
		s.send(reply, true)
		go func() {
			time.Sleep(time.Second)
			rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.Device.Action(rctx, "reboot", ""); err != nil {
				c.Log.Error("reboot requested by the cloud", "err", err)
			}
		}()
		return
	}
	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := c.Device.Action(actx, m.Name, m.Password)
	cancel()
	var ae *ActionError
	var fe config.FieldErrors
	switch {
	case err == nil:
		reply.OK = true
	case errors.As(err, &ae):
		reply.Error, reply.Message = ae.Code, ae.Message
	case errors.As(err, &fe):
		reply.Error, reply.Message = "validation", fe.Message(c.lang())
	default:
		c.Log.Error("cloud: action failed", "action", m.Name, "err", err)
		reply.Error, reply.Message = "failed", c.msg("cloud.action_failed")
	}
	s.send(reply, true)
}

// --- send rate limit -----------------------------------------------------------------------------

type bucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(rate, burst float64) *bucket { return &bucket{rate: rate, burst: burst, tokens: burst} }

func (b *bucket) take(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.last.IsZero() {
		b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
