package appapi

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// MIXER.md §21: the screen's pages of everything the web configures (destination, uplinks, input, cameras, the
// link to EnhancedIRL, the web password). Their reads are polled only while their page is on the screen; their
// actions go through the same queue as the device's, one at a time. The secrets they carry (a stream key, a
// password) are copied when queued, built into the body without becoming a string and wiped after; what the app
// reveals for an eye (the stream key, a camera's URLs) is dropped when the page goes (DropSecrets).

const (
	// StatusTimeout: GET /status may wait for the network and the Wi-Fi (two 3 s timeouts in the app)
	StatusTimeout = 8 * time.Second
	DestEvery     = 2 * time.Second  // the Destination page
	StatusEvery   = 2 * time.Second  // the Uplinks and Input pages (GET /status)
	SourcesEvery  = 10 * time.Second // the Input page: a camera just plugged in shows up (like the web)
)

// parityActions are ui.AppActions: run by runParity.
var parityActions = map[string]bool{}

func init() {
	routes := map[string][2]string{
		"dest_save": {"PUT", "/destination"}, "dest_reveal": {"GET", "/destination/secrets"},
		// The saved destinations (OUTPUTS.md §9.3); dest_create's and dest_update's secrets go as secret fields
		"dest_create": {"POST", "/destinations"}, "dest_update": {"PUT", "/destinations/{id}"},
		"dest_delete": {"DELETE", "/destinations/{id}"}, "dest_activate": {"POST", "/destinations/{id}/activate"},
		"dest_secrets": {"GET", "/destinations/{id}/secrets"},
		"restart":      {"POST", "/service/restart"}, "uplink": {"PUT", "/uplinks/{id}"},
		"input_save": {"PUT", "/input"}, "sources_scan": {"GET", "/sources"},
		"cloud_unlink": {"DELETE", "/cloud"}, "password": {"PUT", "/password"},
		"cam_add": {"POST", "/cameras"}, "cam_rename": {"PATCH", "/cameras/{id}"},
		"cam_rotate": {"PATCH", "/cameras/{id}"}, "cam_remove": {"DELETE", "/cameras/{id}"},
		"cam_creds":  {"GET", "/network/credentials?cameras=1"},
		"ap_enabled": {"PUT", "/network/ap"}, "ap_channel": {"PUT", "/network/ap"}, "ap_rotate": {"PUT", "/network/ap"},
		"lan_enabled": {"PUT", "/network/lan"},
		// Cellular modems (MODEMS.md §7); modem_pin's PIN goes as a secret field
		"modem_save": {"PUT", "/modems/{id}"}, "modem_pin": {"POST", "/modems/{id}/pin"},
		"modem_reset": {"POST", "/modems/{id}/reset"}, "modem_subnet": {"POST", "/modems/{id}/subnet"},
		// Bluetooth (BLUETOOTH.md §3.4): checked and built by btRequest; bt_device's {action} is its Value
		"bt_power": {"PUT", "/bluetooth"}, "bt_scan": {"POST", "/bluetooth/scan"},
		"bt_visible": {"POST", "/bluetooth/visible"}, "bt_device": {"POST", "/bluetooth/devices/{id}/{action}"},
		"bt_use": {"PUT", "/bluetooth/devices/{id}"},
		// Local recording (RECORDING.md §6.2): checked and built by recRequest
		"rec_settings": {"PUT", "/recording"}, "rec_eject": {"POST", "/storage/eject"},
		"rec_format": {"POST", "/storage/format"},
	}
	for _, w := range ui.AppActions {
		r, ok := routes[w]
		if !ok {
			panic("appapi: no route for " + w)
		}
		actionRoutes[w] = r
		parityActions[w] = true
	}
}

// parityBody is an action's JSON body: its fields, plus its secrets written straight from their bytes (never a
// string). nil for a request without a body (GET, DELETE). The caller's call() wipes it.
func parityBody(a ui.AppAction, method string, secrets map[string]secret) []byte {
	if method == "GET" || method == "DELETE" {
		return nil
	}
	fields := map[string]any{}
	for k, v := range a.Body {
		if _, s := secrets[k]; !s {
			fields[k] = v
		}
	}
	switch a.What {
	case "cam_rotate":
		fields["rotateKey"] = true
	case "ap_rotate":
		fields["rotatePassword"] = true
	}
	rest, _ := json.Marshal(fields) // {…}: the secrets go before its first field
	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	n := len(rest)
	for _, k := range keys {
		n += len(k) + 2*len(secrets[k].b) + 8
	}
	b := make([]byte, 0, n)
	b = append(b, '{')
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		kq, _ := json.Marshal(k)
		b = append(b, kq...)
		b = append(b, ':', '"')
		b = appendJSONBytes(b, secrets[k].b)
		b = append(b, '"')
	}
	if len(rest) > 2 {
		if len(keys) > 0 {
			b = append(b, ',')
		}
		b = append(b, rest[1:len(rest)-1]...)
	}
	return append(b, '}')
}

// runParity runs one of MIXER.md §21's actions and keeps its result (and what its answer brings).
func (d *DeviceState) runParity(q queued, route [2]string) {
	a := q.a
	path := route[1]
	res := &ui.AppResult{What: a.What}
	var body []byte
	if isBT(a.What) { // only its route's fields, each of the right shape (btRequest), or it is not sent
		b, ok := btRequest(a)
		if !ok || len(q.secrets) > 0 {
			res.Status, res.Code, res.At = 400, "bad_request", time.Now()
			d.finish(res)
			return
		}
		body = b
		path = strings.Replace(path, "{action}", a.Value, 1) // one of btOrders
	}
	if isRec(a.What) { // the same: only its route's fields (recRequest), and no secret
		b, ok := recRequest(a)
		if !ok || len(q.secrets) > 0 {
			res.Status, res.Code, res.At = 400, "bad_request", time.Now()
			d.finish(res)
			return
		}
		body = b
	}
	if strings.Contains(path, "{id}") {
		if a.ID == "" || len(a.ID) > 200 {
			res.Status, res.Code, res.At = 400, "bad_request", time.Now()
			d.finish(res)
			return
		}
		path = strings.Replace(path, "{id}", url.PathEscape(a.ID), 1)
	}
	if body == nil {
		body = parityBody(a, route[0], q.secrets)
	}
	_, v, e := d.call("", route[0], path, body, recTimeout(a.What, d.actTimeout))
	res.OK, res.At = e == nil, time.Now()
	if e != nil {
		res.Status, res.Code, res.Message = e.Status, e.Kind, e.userText(d.lang.get())
		res.Fields = fieldErrors(v)
		d.finish(res)
		return
	}
	if r, ok := boolOf(obj(v)["restarted"]); ok {
		res.Restarted = &r
	}
	d.mu.Lock()
	current := q.gen == d.secretGen // no DropSecrets since it was queued
	switch a.What {
	case "dest_save", "input_save":
		if dst := destInfo(v); dst != nil {
			d.snap.Dest, d.snap.DestAPI = dst, ui.Yes
		}
		if a.What == "dest_save" { // what an eye showed may be the old key: asked again
			d.snap.DestSecrets = nil
			d.secretGen++
		}
	case "dest_reveal":
		if current {
			d.snap.DestSecrets = destSecrets(v)
		}
	case "dest_create", "dest_update":
		if dst := destInfo(v); dst != nil && dst.ID != "" {
			res.ID = dst.ID
			d.snap.Dests = withDest(d.snap.Dests, *dst)
		}
		d.snap.DestSecrets = nil // what an eye showed may be the old key: asked again
		d.secretGen++
	case "dest_delete":
		d.snap.Dests = withoutDest(d.snap.Dests, a.ID)
		d.snap.DestSecrets = nil
		d.secretGen++
	case "dest_activate":
		if l := destList(v); l != nil {
			d.snap.Dests, d.snap.DestsAPI = l, ui.Yes
		}
	case "dest_secrets":
		if s := destSecrets(v); current && s != nil {
			s.ID = a.ID
			d.snap.DestSecrets = s
		}
	case "sources_scan":
		if s := sourcesInfo(v); s != nil {
			d.snap.Sources, d.snap.SourcesAPI = s, ui.Yes
		}
	case "cam_creds":
		if current {
			d.snap.CamCreds = camCreds(v)
		}
	case "cam_add", "cam_rotate", "cam_remove": // the cameras' URLs changed: a reveal in flight is the old ones
		d.snap.CamCreds = nil
		d.secretGen++
	case "cloud_unlink":
		if c := cloudInfo(v); c != nil {
			d.snap.Cloud, d.snap.CloudAPI = c, ui.Yes
		}
	case "bt_power", "bt_scan", "bt_visible", "bt_device", "bt_use": // {"ok": true, "bluetooth": the state now}
		if b := btState(obj(v)["bluetooth"]); b != nil {
			d.snap.Bluetooth, d.snap.BluetoothAPI = b, ui.Yes
		}
	case "rec_settings": // the recording view
		if r := recordingInfo(v); r != nil {
			d.snap.Recording, d.snap.RecordingAPI = r, ui.Yes
		}
	case "rec_eject", "rec_format": // the storage object
		if st, ok := storageInfo(v); ok {
			d.snap.Recording = withStorage(d.snap.Recording, st)
		}
	}
	d.mu.Unlock()
	d.finish(res)
}

// finish keeps an action's result and moves Pending to the next queued one.
func (d *DeviceState) finish(res *ui.AppResult) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.snap.Last = res
	d.snap.Pending = ""
	if len(d.queue) > 0 {
		d.snap.Pending = d.queue[0].a.What
	}
}

// DropSecrets forgets what an eye revealed (the stream key and passphrase, the cameras' URLs): the page that showed
// them is gone. Go cannot zero a string; they are let go and never written anywhere.
func (d *DeviceState) DropSecrets() {
	d.mu.Lock()
	d.snap.DestSecrets, d.snap.CamCreds = nil, nil
	d.secretGen++ // a reveal queued or in flight is not kept when it answers
	d.mu.Unlock()
}

var _ ui.SecretsDropper = (*DeviceState)(nil)

// --- polls (blocking: only from the goroutine, and tests) ---

// pollDest asks for the saved destinations (GET /destinations); an older app without them (403, 404), for its single
// destination (GET /destination), and the list is asked again only when the page is opened again.
func (d *DeviceState) pollDest(ui.AppWatch) {
	d.mu.Lock()
	list := d.snap.DestsAPI != ui.No || d.destsRetry
	d.destsRetry = false
	d.mu.Unlock()
	if list {
		_, v, e := d.call("", "GET", "/destinations", nil, d.getTimeout)
		d.mu.Lock()
		l := destList(v)
		switch {
		case e == nil && l != nil:
			d.snap.Dests, d.snap.DestsAPI = l, ui.Yes
			d.mu.Unlock()
			return
		case e == nil || d.gone(e): // not there (or not a list): the single destination
			d.snap.Dests, d.snap.DestsAPI = nil, ui.No
		default: // passing (no answer, a 5xx): the last one stays
			d.mu.Unlock()
			return
		}
		d.mu.Unlock()
	}
	_, v, e := d.call("", "GET", "/destination", nil, d.getTimeout)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case e == nil:
		if dst := destInfo(v); dst != nil {
			d.snap.Dest, d.snap.DestAPI = dst, ui.Yes
		}
	case d.gone(e):
		d.snap.Dest, d.snap.DestAPI = nil, ui.No
	}
}

func (d *DeviceState) pollStatus(ui.AppWatch) {
	_, v, e := d.call("", "GET", "/status", nil, max(d.getTimeout, StatusTimeout))
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case e == nil:
		if st := statusInfo(v); st != nil {
			d.snap.Status, d.snap.StatusAPI = st, ui.Yes
		}
	case d.gone(e):
		d.snap.Status, d.snap.StatusAPI = nil, ui.No
	}
}

func (d *DeviceState) pollSources(ui.AppWatch) {
	_, v, e := d.call("", "GET", "/sources", nil, d.getTimeout)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case e == nil:
		if s := sourcesInfo(v); s != nil {
			d.snap.Sources, d.snap.SourcesAPI = s, ui.Yes
		}
	case d.gone(e) || e.Kind == "sources_disabled":
		d.snap.Sources, d.snap.SourcesAPI = nil, ui.No
	}
}

// PollDest, PollStatus and PollSources are the polls for tests and tools.
func (d *DeviceState) PollDest()    { d.pollDest(ui.AppWatch{}) }
func (d *DeviceState) PollStatus()  { d.pollStatus(ui.AppWatch{}) }
func (d *DeviceState) PollSources() { d.pollSources(ui.AppWatch{}) }

// --- what the app says, validated (whatever does not fit is left out) ---

var (
	destModes  = set("caller", "listener")
	transports = set("srt", "srtla", "rtmp", "rtmps", "rist", "udp") // also the destinations' protocols
	// The saved destinations (OUTPUTS.md §7, §9): closed lists, anything else is left out
	destTemplates = set("eirl", "twitch", "youtube", "kick", "facebook", "custom")
	destCodecs    = set("h264", "h265")
	destSizes     = set("1280x720", "960x540") // "" is the program's
	destFps       = set("full", "half")
	ristProfiles  = set("simple", "main")
	reDestID      = regexp.MustCompile(`^(eirl|d-[0-9a-f]{8})$`)
	reRistUser    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	onOff         = set("on", "off")
	inputKinds    = set("hdmi", "usb", "test")
	activeState   = set("starting", "waiting_device", "ready")
	srcKinds      = set("hdmi-csi", "usb", "test")
	audioKinds    = set("i2s", "usb", "network")
	reMode        = regexp.MustCompile(`^(auto|[0-9]{2,5}x[0-9]{2,5}@[0-9]{1,6}(/[0-9]{1,6})?)$`)
	reFormat      = regexp.MustCompile(`^(auto|[A-Z0-9]{2,8})$`)
	reAudioID     = regexp.MustCompile(`^(auto|none|i2s|silence|test|alsa:[A-Za-z0-9_.-]{1,64}|net:cam[1-8]|bt:[0-9A-F]{12})$`)
	reEDID        = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,32}$`)
	reUplink      = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,32}$`)
	reModemID     = regexp.MustCompile(`^[mh]-[0-9a-f]{12}$`)
	modemStates   = set("failed", "locked", "disabled", "enabling", "searching", "registered", "connecting", "connected",
		"disconnected", "unknown")
	simStates     = set("ready", "missing", "pin", "puk", "error", "unknown")
	regStates     = set("home", "roaming", "searching", "denied", "idle", "unknown")
	modemModes    = set("auto", "4g", "5g")
	modemBackends = set("mm", "hilink")
	modemCans     = set("apn", "roaming", "mode", "pin", "reset", "subnet")
	reTail        = regexp.MustCompile(`^[0-9A-Fa-f]{4}$`)
	// MODEMS.md §5: a level and its code (closed lists: anything else is dropped and the row shows no dot)
	healthLevels = set("good", "weak", "bad", "off")
	bonds        = set("active", "stalled", "weak", "down")
	healthCodes  = set("ok", "off", "ip_conflict", "disconnected", "searching", "sim_missing", "sim_pin", "sim_puk",
		"sim_error", "modem_failed", "denied", "roaming_off", "no_internet", "signal_minimal", "signal_weak", "slow_tech",
		"connecting", "link_weak")
	reCamID = regexp.MustCompile(`^cam[1-8]$`)
)

// numberOf is v as a float (a JSON number).
func numberOf(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// destInfo is GET /destination (or what a PUT /destination or /input returns); nil if it is not one.
func destInfo(v any) *ui.DestInfo {
	m := obj(v)
	if m == nil {
		return nil
	}
	sid, pass := obj(m["streamId"]), obj(m["passphrase"])
	if sid == nil || pass == nil {
		return nil
	}
	d := &ui.DestInfo{ManagedBy: textField(m, "managedBy", 32), URL: textField(m, "url", 512),
		Mode: oneOf(m, "mode", destModes), Transport: oneOf(m, "transport", transports), Output: oneOf(m, "output", onOff)}
	d.URLInvalid, _ = boolOf(m["urlInvalid"])
	d.LatencyMs, _ = intIn(m["latencyMs"], 0, 100000)
	d.StreamIDSet, _ = boolOf(sid["set"])
	d.StreamIDMasked = textField(sid, "masked", 200)
	d.PassphraseSet, _ = boolOf(pass["set"])
	d.VideoKbps, _ = intIn(m["videoBitrateKbps"], 0, 1000000)
	d.AudioKbps, _ = intIn(m["audioBitrateKbps"], 0, 100000)
	d.ABR, _ = boolOf(m["adaptiveBitrate"])
	d.VideoMinKbps, _ = intIn(m["videoMinBitrateKbps"], 0, 1000000)
	d.PendingRestart, _ = boolOf(m["pendingRestart"])
	// A saved destination's view (GET /destinations); absent from an older app
	if id, _ := m["id"].(string); reDestID.MatchString(id) {
		d.ID = id
	}
	if n := textField(m, "name", 128); utf8.RuneCountInString(n) <= 32 {
		d.Name = n
	}
	d.Template = oneOf(m, "template", destTemplates)
	d.Managed, _ = boolOf(m["managed"])
	d.Active, _ = boolOf(m["active"])
	d.Protocol = oneOf(m, "protocol", transports)
	if k := obj(m["key"]); k != nil {
		d.KeySet, _ = boolOf(k["set"])
		d.KeyMasked = textField(k, "masked", 200)
	}
	if p := obj(m["password"]); p != nil {
		d.PasswordSet, _ = boolOf(p["set"])
	}
	d.RistProfile = oneOf(m, "ristProfile", ristProfiles)
	if u, _ := m["username"].(string); reRistUser.MatchString(u) {
		d.Username = u
	}
	d.VideoCodec = oneOf(m, "videoCodec", destCodecs)
	d.OutputSize = oneOf(m, "outputSize", destSizes)
	d.OutputFps = oneOf(m, "outputFps", destFps)
	return d
}

// MaxDests is the most saved destinations the app keeps (OUTPUTS.md §9.1).
const MaxDests = 16

// destList is GET /destinations (or what POST …/activate returns); nil if it is not one. A destination without a
// valid id, or one more than MaxDests, is left out; the active one is the list's (and only if it is in it).
func destList(v any) *ui.DestList {
	m := obj(v)
	raw, ok := m["destinations"].([]any)
	if m == nil || !ok {
		return nil
	}
	l := &ui.DestList{ManagedBy: textField(m, "managedBy", 32)}
	l.PendingRestart, _ = boolOf(m["pendingRestart"])
	active, _ := m["active"].(string)
	seen := map[string]bool{}
	for _, x := range raw {
		dst := destInfo(x)
		if dst == nil || dst.ID == "" || seen[dst.ID] || len(l.Destinations) >= MaxDests {
			continue
		}
		seen[dst.ID] = true
		dst.Active = dst.ID == active
		if dst.Active {
			l.Active = active
		}
		l.Destinations = append(l.Destinations, *dst)
	}
	if ts, ok := m["templates"].([]any); ok {
		for _, x := range ts {
			if t, ok := destTemplate(obj(x)); ok && l.Template(t.ID) == nil {
				l.Templates = append(l.Templates, t)
			}
		}
	}
	return l
}

// destTemplate is one of GET /destinations' templates; ok false if it does not fit (an unknown id, no protocol).
func destTemplate(m map[string]any) (ui.DestTemplate, bool) {
	id := oneOf(m, "id", destTemplates)
	if m == nil || id == "" {
		return ui.DestTemplate{}, false
	}
	t := ui.DestTemplate{ID: id, URL: textField(m, "url", 512), Protocols: closedList(m["protocols"], transports),
		Codecs: closedList(m["codecs"], destCodecs)}
	t.MaxVideoKbps, _ = intIn(m["maxVideoKbps"], 500, 1000000)
	t.KeyRequired, _ = boolOf(m["keyRequired"])
	if us := obj(m["urls"]); us != nil {
		for _, p := range t.Protocols {
			if u := textField(us, p, 512); u != "" {
				if t.URLs == nil {
					t.URLs = map[string]string{}
				}
				t.URLs[p] = u
			}
		}
	}
	return t, len(t.Protocols) > 0
}

// closedList is a JSON list of strings kept to the allowed ones, each once, in their order.
func closedList(v any, allowed map[string]bool) []string {
	xs, _ := v.([]any)
	var out []string
	for _, x := range xs {
		if s, ok := x.(string); ok && allowed[s] && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// withDest is the list with a destination added or replaced (a copy: the screen may be reading the old one).
func withDest(l *ui.DestList, dst ui.DestInfo) *ui.DestList {
	if l == nil {
		return nil
	}
	n := *l
	n.Destinations = slices.Clone(l.Destinations)
	dst.Active = dst.ID == n.Active
	for i := range n.Destinations {
		if n.Destinations[i].ID == dst.ID {
			n.Destinations[i] = dst
			return &n
		}
	}
	if len(n.Destinations) < MaxDests {
		n.Destinations = append(n.Destinations, dst)
	}
	return &n
}

// withoutDest is the list without a destination (a copy).
func withoutDest(l *ui.DestList, id string) *ui.DestList {
	if l == nil {
		return nil
	}
	n := *l
	n.Destinations = slices.DeleteFunc(slices.Clone(l.Destinations), func(x ui.DestInfo) bool { return x.ID == id })
	return &n
}

// destSecrets is GET /destination/secrets (the stream ID and the passphrase as they are saved) or GET
// /destinations/{id}/secrets (also the RTMP key and the RIST password); at most 512 bytes each.
func destSecrets(v any) *ui.DestSecrets {
	m := obj(v)
	if m == nil {
		return nil
	}
	s := &ui.DestSecrets{}
	for k, dst := range map[string]*string{"streamId": &s.StreamID, "passphrase": &s.Passphrase, "key": &s.Key,
		"password": &s.Password} {
		if x, ok := m[k].(string); ok && (x == "" || textOK(x, 512)) {
			*dst = x
		}
	}
	return s
}

// statusInfo is what the pages use of GET /status; nil if it is not a status.
func statusInfo(v any) *ui.StatusInfo {
	m := obj(v)
	if m == nil || obj(m["service"]) == nil {
		return nil
	}
	st := &ui.StatusInfo{Transport: oneOf(m, "transport", transports), Output: oneOf(m, "output", onOff)}
	st.PendingRestart, _ = boolOf(m["pendingRestart"])
	if ups, ok := m["uplinks"].([]any); ok {
		st.HasUplinks = true
		for _, x := range ups {
			um := obj(x)
			name, _ := um["name"].(string)
			if !reUplink.MatchString(name) || len(st.Uplinks) >= 16 {
				continue
			}
			u := ui.UplinkInfo{Name: name, IP: textField(um, "ip", 64), Kind: textField(um, "kind", 16)}
			u.InUse, _ = boolOf(um["inUse"])
			u.Disabled, _ = boolOf(um["disabled"])
			u.Conflict, _ = boolOf(um["conflict"])
			u.NoInternet, _ = boolOf(um["noInternet"])
			u.TxKbps, _ = intIn(um["txKbps"], 0, 10000000)
			u.Label = textField(um, "label", 32)
			if id, _ := um["modemId"].(string); reModemID.MatchString(id) {
				u.ModemID = id
			}
			u.Health = oneOf(um, "health", healthLevels)
			if u.Health != "" {
				u.HealthCode = oneOf(um, "healthCode", healthCodes)
			}
			// The Rust SRTLA sender's figures (SRTLA.md §9.4): all three or none
			rtt, ok1 := intIn(um["rttMs"], 0, 60000)
			kbps, ok2 := intIn(um["sendKbps"], 0, 1000000)
			nak, ok3 := floatIn(um["nakPerSec"], 0, 100000)
			if ok1 && ok2 && ok3 {
				u.HasStats, u.RTTMs, u.SendKbps, u.NakPerSec = true, rtt, kbps, nak
				u.Bond = oneOf(um, "bond", bonds)
			}
			st.Uplinks = append(st.Uplinks, u)
		}
	}
	if mods, ok := m["modems"].([]any); ok {
		for _, x := range mods {
			if md, ok := modemInfo(obj(x)); ok && len(st.Modems) < 8 {
				st.Modems = append(st.Modems, md)
			}
		}
	}
	if links, ok := m["links"].([]any); ok {
		for _, x := range links {
			lm := obj(x)
			name, _ := lm["name"].(string)
			if lm == nil || !reUplink.MatchString(name) || len(st.Links) >= MaxLinks {
				continue
			}
			l := ui.LinkDot{Name: name, Kind: oneOf(lm, "kind", linkKinds), Label: textField(lm, "label", 32),
				Tech: oneOf(lm, "tech", linkTechs), Health: oneOf(lm, "health", healthLevels), Code: oneOf(lm, "code", healthCodes)}
			l.Disabled, _ = boolOf(lm["disabled"])
			if av, _ := boolOf(lm["available"]); !av && l.Kind != "" && l.Health != "" {
				st.Links = append(st.Links, l) // only the ones without a link: the others are uplinks[]
			}
		}
	}
	if in := obj(m["input"]); in != nil {
		i := &st.Input
		i.Source = oneOf(in, "source", inputKinds)
		i.Device = textField(in, "device", 256)
		if f, _ := in["format"].(string); reFormat.MatchString(f) {
			i.Format = f
		}
		if md, _ := in["mode"].(string); reMode.MatchString(md) {
			i.Mode = md
		}
		if au, _ := in["audio"].(string); reAudioID.MatchString(au) {
			i.Audio = au
		}
		if e, _ := in["edidPreferred"].(string); reEDID.MatchString(e) {
			i.EDIDPreferred = e
		}
		if e, _ := in["edidActive"].(string); reEDID.MatchString(e) {
			i.EDIDActive = e
		}
		if a := obj(in["active"]); a != nil {
			i.ActiveState = oneOf(a, "state", activeState)
			i.ActiveSource = oneOf(a, "source", inputKinds)
			i.ActiveName = textField(a, "name", 120)
			if f, _ := a["format"].(string); reFormat.MatchString(f) {
				i.ActiveFormat = f
			}
			if md, _ := a["mode"].(string); reMode.MatchString(md) {
				i.ActiveMode = md
			}
		}
	}
	return st
}

// captureOption is one of a USB source's options (or its «auto»); ok false if it does not fit.
func captureOption(v any) (ui.CaptureOption, bool) {
	m := obj(v)
	f, _ := m["format"].(string)
	md, _ := m["mode"].(string)
	if m == nil || !reFormat.MatchString(f) || f == "auto" || !reMode.MatchString(md) || md == "auto" {
		return ui.CaptureOption{}, false
	}
	o := ui.CaptureOption{Format: f, Mode: md}
	if x, ok := numberOf(m["fps"]); ok && x > 0 && x < 1000 {
		o.FPS = x
	}
	o.OutWidth, _ = intIn(m["outWidth"], 0, 10000)
	o.OutHeight, _ = intIn(m["outHeight"], 0, 10000)
	if x, ok := numberOf(m["busShare"]); ok && x >= 0 && x < 100 {
		o.BusShare = x
	}
	return o, true
}

// sourcesInfo is GET /sources; nil if it is not one.
func sourcesInfo(v any) *ui.SourcesInfo {
	m := obj(v)
	raw, ok := m["sources"].([]any)
	if m == nil || !ok {
		return nil
	}
	out := &ui.SourcesInfo{}
	for _, x := range raw {
		sm := obj(x)
		id, _ := sm["id"].(string)
		kind := oneOf(sm, "kind", srcKinds)
		if kind == "" || !textOK(id, 256) || len(out.Sources) >= 16 {
			continue
		}
		s := ui.SourceChoice{ID: id, Kind: kind, Name: textField(sm, "name", 120)}
		if u := obj(sm["usb"]); u != nil {
			if sp, ok := intIn(u["speedMbps"], 1, 1000000); ok {
				s.USB3, s.USBKnown = sp >= 5000, true
			}
		}
		if o, ok := captureOption(sm["auto"]); ok {
			s.Auto = &o
		}
		if opts, ok := sm["options"].([]any); ok {
			for _, ox := range opts {
				if o, ok := captureOption(ox); ok && len(s.Options) < 64 {
					s.Options = append(s.Options, o)
				}
			}
		}
		if a := obj(sm["audio"]); a != nil {
			s.AudioName = textField(a, "name", 120)
		}
		if ws, ok := sm["warnings"].([]any); ok {
			for _, w := range ws {
				if t, ok := w.(string); ok && textOK(t, 32) && len(s.Warnings) < 8 {
					s.Warnings = append(s.Warnings, t)
				}
			}
		}
		out.Sources = append(out.Sources, s)
	}
	if au, ok := m["audio"].([]any); ok {
		for _, x := range au {
			am := obj(x)
			id, _ := am["id"].(string)
			kind := oneOf(am, "kind", audioKinds)
			if kind == "" || !reAudioID.MatchString(id) || len(out.Audio) >= 16 {
				continue
			}
			out.Audio = append(out.Audio, ui.AudioChoice{ID: id, Kind: kind, Name: textField(am, "name", 120)})
		}
	}
	return out
}

// camURLs is a camera's URLs over one way in, with the SRT one's QR; ok false if they do not fit.
func camURLs(m map[string]any) (ui.CamURLs, bool) {
	u := ui.CamURLs{SRT: textField(m, "srt", 512), RTMP: textField(m, "rtmp", 512),
		RTMPServer: textField(m, "rtmpServer", 512), RTMPKey: textField(m, "rtmpKey", 256)}
	if !strings.HasPrefix(u.SRT, "srt://") || (u.RTMP != "" && !strings.HasPrefix(u.RTMP, "rtmp://")) {
		return ui.CamURLs{}, false
	}
	u.SRTQR = qrRowsOf(u.SRT)
	return u, true
}

// camCreds is the cameras of GET /network/credentials?cameras=1 (their URLs with their keys).
func camCreds(v any) []ui.CamConnect {
	raw, _ := obj(v)["cameras"].([]any)
	out := []ui.CamConnect{}
	for _, x := range raw {
		cm := obj(x)
		id, _ := cm["id"].(string)
		if !reCamID.MatchString(id) || len(out) >= 8 {
			continue
		}
		w, ok := camURLs(cm)
		if !ok {
			continue
		}
		c := ui.CamConnect{ID: id, Wifi: w}
		if lm := obj(cm["lan"]); lm != nil {
			if l, ok := camURLs(lm); ok {
				c.LAN = &l
			}
		}
		out = append(out, c)
	}
	return out
}

// modemInfo: one modem of GET /status, every field validated (the app runs as another user).
func modemInfo(m map[string]any) (ui.ModemInfo, bool) {
	id, _ := m["id"].(string)
	if m == nil || !reModemID.MatchString(id) {
		return ui.ModemInfo{}, false
	}
	md := ui.ModemInfo{ID: id, Backend: oneOf(m, "backend", modemBackends), Manufacturer: textField(m, "manufacturer", 32),
		Model: textField(m, "model", 32),
		State: oneOf(m, "state", modemStates), SIM: oneOf(m, "sim", simStates), Registration: oneOf(m, "registration", regStates),
		Operator: textField(m, "operator", 32), Tech: oneOf(m, "tech", linkTechs), Alias: textField(m, "alias", 48),
		APN: textField(m, "apn", 63), APNAuto: textField(m, "apnAuto", 63), Mode: oneOf(m, "mode", modemModes)}
	if v, _ := m["iface"].(string); reUplink.MatchString(v) {
		md.Iface = v
	}
	for k, dst := range map[string]*string{"imeiTail": &md.IMEITail, "iccidTail": &md.ICCIDTail} {
		if v, _ := m[k].(string); reTail.MatchString(v) {
			*dst = v
		}
	}
	md.Roaming, _ = boolOf(m["roaming"])
	if sig := obj(m["signal"]); sig != nil {
		for k, dst := range map[string]**float64{"rsrp": &md.RSRP, "rsrq": &md.RSRQ, "sinr": &md.SINR, "rssi": &md.RSSI} {
			if f, ok := numberOf(sig[k]); ok && f > -200 && f < 100 {
				v := f
				*dst = &v
			}
		}
	}
	for _, list := range []struct {
		key     string
		allowed map[string]bool
		dst     *[]string
	}{{"modes", modemModes, &md.Modes}, {"can", modemCans, &md.Can}} {
		if xs, ok := m[list.key].([]any); ok {
			for _, x := range xs {
				if s, ok := x.(string); ok && list.allowed[s] && len(*list.dst) < 8 {
					*list.dst = append(*list.dst, s)
				}
			}
		}
	}
	return md, true
}
