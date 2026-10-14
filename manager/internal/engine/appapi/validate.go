package appapi

import (
	"regexp"
	"unicode/utf8"
)

// What the screen draws, validated: whatever does not fit is left out, so drawing never fails on odd data from the
// app. Values are never modified once published in a Snapshot (every update builds new ones): callers must not
// modify them either.

// AccessPoint is «Crear mi wifi»: accessPoint of GET /network, updated with ap of GET /network/wifi. An empty
// string or a nil pointer is a field the app did not give (or gave in a shape that does not fit).
type AccessPoint struct {
	Enabled bool   // only from GET /network; false when absent
	Policy  string // always | rescue
	State   string // off | starting | on | error
	SSID    string // 1-32 bytes
	Channel *int   // 0-1000
	Clients *int   // 0-1000
	Error   string // 1-200 bytes
}

// LAN is the cameras by cable (lan of GET /network). Python kept it raw; only what the screen reads is kept.
type LAN struct {
	Enabled bool
	State   string // off | starting | on | error
	Error   string
}

// Camera is one network camera of GET /network (the screen only counts them: their live state comes from the
// engine's own sources).
type Camera struct {
	ID        string
	Name      string
	Connected bool
	Via       string // wifi | lan | ""
}

// Cameras of GET /network.
type Cameras struct {
	Max   int
	Items []Camera
}

// Status is GET /network (or what PUT /network returns): the reception switch and how the cameras get in.
type Status struct {
	Enabled     bool // the RTMP/SRT reception (NETWORK_SOURCES.md §6.12)
	AccessPoint AccessPoint
	LAN         LAN
	Cameras     Cameras
}

// WiFi is the board's wifi to join (GET /network/credentials?qr=rows, only with the panel open): the SSID, its
// password and the QR in rows of 0 and 1.
type WiFi struct {
	SSID     string
	Password string
	QR       []string
}

// Client is «Conectarse a una wifi» (client of GET /network/wifi).
type Client struct {
	Enabled  bool
	State    string // off | disconnected | connecting | connected | error
	SSID     string
	Signal   *int   // 0-100
	Band     string // 2.4 | 5
	Internet *bool
	Error    string // wrong_password | not_found | connect_failed | unavailable
}

// Network is one network of GET /network/wifi/scan.
type Network struct {
	SSID      string
	Security  string // open | wep | wpa | wpa2 | wpa3 | enterprise
	Signal    int    // 0-100 (0 if the app gave something else)
	Band      string // 2.4 | 5 | "" (None)
	Known     bool
	Connected bool
}

var (
	clientStates  = set("off", "disconnected", "connecting", "connected", "error")
	clientErrors  = set("wrong_password", "not_found", "connect_failed", "unavailable")
	securities    = set("open", "wep", "wpa", "wpa2", "wpa3", "enterprise")
	apStates      = set("off", "starting", "on", "error")
	apPolicies    = set("always", "rescue")
	bands         = set("2.4", "5")
	reSSID        = regexp.MustCompile(`^[A-Za-z0-9 _.-]{1,32}$`) // netcfg's (the app validates them on write)
	rePSK         = regexp.MustCompile(`^[A-Za-z0-9]{8,63}$`)
	reQRRow       = regexp.MustCompile(`^[01]{21,177}$`)
	maxNetworks   = 40
	maxQRFileRead = 16384
)

func set(vs ...string) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// textOK: a text of the app that can be drawn: 1-n bytes of valid UTF-8 (Python: no lone surrogates, on which
// Cairo fails) and no control characters (< 32).
func textOK(s string, n int) bool {
	if len(s) == 0 || len(s) > n || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 32 {
			return false
		}
	}
	return true
}

// textField is m[k] if it is a drawable text of at most n bytes.
func textField(m map[string]any, k string, n int) string {
	if s, ok := m[k].(string); ok && textOK(s, n) {
		return s
	}
	return ""
}

// oneOf is m[k] if it is a string in allowed.
func oneOf(m map[string]any, k string, allowed map[string]bool) string {
	if s, ok := m[k].(string); ok && allowed[s] {
		return s
	}
	return ""
}

// apFields is the access point of GET /network (accessPoint) or GET /network/wifi (ap), with only what fits.
func apFields(v any) AccessPoint {
	m := obj(v)
	var ap AccessPoint
	if m == nil {
		return ap
	}
	ap.Enabled, _ = m["enabled"].(bool)
	ap.Policy = oneOf(m, "policy", apPolicies)
	ap.State = oneOf(m, "state", apStates)
	ap.SSID = textField(m, "ssid", 32)
	if n, ok := intIn(m["channel"], 0, 1000); ok {
		ap.Channel = &n
	}
	if n, ok := intIn(m["clients"], 0, 1000); ok {
		ap.Clients = &n
	}
	ap.Error = textField(m, "error", 200)
	return ap
}

// networkStatus is GET /network (or what a network PUT returns) in the shape the screen draws; nil if it is not a
// state (no boolean enabled).
func networkStatus(v any) *Status {
	d := obj(v)
	en, ok := d["enabled"].(bool)
	if !ok {
		return nil
	}
	st := &Status{Enabled: en, AccessPoint: apFields(d["accessPoint"])}
	if lan := obj(d["lan"]); lan != nil {
		st.LAN.Enabled, _ = lan["enabled"].(bool)
		st.LAN.State = oneOf(lan, "state", apStates)
		st.LAN.Error = textField(lan, "error", 200)
	}
	cams := obj(d["cameras"])
	st.Cameras.Max, _ = intIn(cams["max"], 0, 1000)
	st.Cameras.Items = []Camera{}
	items, _ := cams["items"].([]any)
	for _, it := range items {
		c := obj(it)
		if c == nil {
			continue
		}
		cam := Camera{ID: textField(c, "id", 200), Name: textField(c, "name", 200), Via: oneOf(c, "via", set("wifi", "lan"))}
		cam.Connected, _ = c["connected"].(bool)
		st.Cameras.Items = append(st.Cameras.Items, cam)
	}
	return st
}

// clientState is the client of GET /network/wifi, validated; nil without a wifi client (an older app, or no
// wlan0).
func clientState(v any) *Client {
	cl := obj(obj(v)["client"])
	if cl == nil || !clientStates[stringOf(cl["state"])] {
		return nil
	}
	out := &Client{State: cl["state"].(string)}
	out.Enabled, _ = cl["enabled"].(bool)
	out.SSID = textField(cl, "ssid", 32)
	if n, ok := intIn(cl["signal"], 0, 100); ok {
		out.Signal = &n
	}
	out.Band = oneOf(cl, "band", bands)
	if b, ok := cl["internet"].(bool); ok {
		out.Internet = &b
	}
	out.Error = oneOf(cl, "error", clientErrors)
	return out
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// scanList is the networks of GET /network/wifi/scan, validated, in the app's order (the connected one, the known
// ones, then by signal), from the first maxNetworks it gave; nil if it does not fit, non-nil (maybe empty)
// otherwise.
func scanList(v any) []Network {
	nets, ok := obj(v)["networks"].([]any)
	if !ok {
		return nil
	}
	if len(nets) > maxNetworks {
		nets = nets[:maxNetworks]
	}
	out := []Network{}
	for _, it := range nets {
		n := obj(it)
		if n == nil {
			continue
		}
		ssid, sec := textField(n, "ssid", 32), oneOf(n, "security", securities)
		if ssid == "" || sec == "" {
			continue
		}
		net := Network{SSID: ssid, Security: sec, Band: oneOf(n, "band", bands)}
		net.Signal, _ = intIn(n["signal"], 0, 100)
		net.Known, _ = n["known"].(bool)
		net.Connected, _ = n["connected"].(bool)
		out = append(out, net)
	}
	return out
}

// validWiFi checks a wifi to join as wifi.json does: netcfg's SSID and password, and a square QR of 21-177 rows.
func validWiFi(ssid, password, rows any) (WiFi, bool) {
	s, ok1 := ssid.(string)
	p, ok2 := password.(string)
	rs, ok3 := rows.([]any)
	if !ok1 || !ok2 || !ok3 || !reSSID.MatchString(s) || !rePSK.MatchString(p) || len(rs) < 21 {
		return WiFi{}, false
	}
	qr := make([]string, 0, len(rs))
	for _, r := range rs {
		row, ok := r.(string)
		if !ok || !reQRRow.MatchString(row) || len(row) != len(rs) {
			return WiFi{}, false
		}
		qr = append(qr, row)
	}
	return WiFi{SSID: s, Password: p, QR: qr}, true
}

// wifiCreds is the wifi of GET /network/credentials?qr=rows ({"wifi": {"ssid", "password", "qrRows"}}) validated
// like wifi.json's, or nil (no wifi, or something that does not fit).
func wifiCreds(v any) *WiFi {
	w := obj(obj(v)["wifi"])
	if w == nil {
		return nil
	}
	if c, ok := validWiFi(w["ssid"], w["password"], w["qrRows"]); ok {
		return &c
	}
	return nil
}
