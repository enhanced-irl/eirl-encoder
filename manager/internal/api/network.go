package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/network"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/wifi"
)

// --- network cameras and their access point (F7, NETWORK_SOURCES.md §6.6) -------------------------

func (s *Server) networkOff(w http.ResponseWriter, r *http.Request) bool {
	if s.Network == nil {
		fail(w, r, 503, "network_unavailable", "network.unavailable")
		return true
	}
	return false
}

func (s *Server) getNetwork(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	writeJSON(w, 200, netStatus(reqLang(r), s.Network.Status(r.Context())))
}

// getNetworkCredentials: the Wi-Fi password and the URL (with key) of each camera. Only when "Show" is
// pressed in the web; never in /status or to the cloud.
func (s *Server) getNetworkCredentials(w http.ResponseWriter, r *http.Request) {
	if cr, ok := s.credentials(w, r); ok {
		writeJSON(w, 200, cr)
	}
}

// getWiFiCredentials: only the Wi-Fi, without the cameras (local socket: the cameras' keys are not shown
// on the screen).
func (s *Server) getWiFiCredentials(w http.ResponseWriter, r *http.Request) {
	if cr, ok := s.credentials(w, r); ok {
		writeJSON(w, 200, map[string]any{"wifi": cr.WiFi})
	}
}

// credentials: the credentials (with the QR in rows with ?qr=rows), no-store. false if it already answered.
func (s *Server) credentials(w http.ResponseWriter, r *http.Request) (network.Credentials, bool) {
	if s.networkOff(w, r) {
		return network.Credentials{}, false
	}
	switch r.URL.Query().Get("qr") {
	case "", "rows":
	default:
		fail(w, r, 400, "bad_request", "network.bad_qr")
		return network.Credentials{}, false
	}
	cr, err := s.Network.Credentials()
	if err == nil && cr.WiFi != nil && r.URL.Query().Get("qr") == "rows" {
		cr.WiFi.QRRows, err = network.QRRows(cr.WiFi.QR)
	}
	if err != nil {
		s.Log.Error("read the network credentials", "err", err)
		fail(w, r, 500, "internal", "common.read_failed")
		return network.Credentials{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	return cr, true
}

func (s *Server) networkQR(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	var text string
	switch r.PathValue("what") {
	case "wifi.svg":
		cr, err := s.Network.Credentials()
		if err != nil || cr.WiFi == nil {
			fail(w, r, 404, "not_found", "network.not_configured")
			return
		}
		text = cr.WiFi.QR
	default:
		fail(w, r, 404, "not_found", "common.unknown_route")
		return
	}
	s.writeQR(w, r, text)
}

func (s *Server) cameraQR(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	cn, err := s.Network.CameraConnect(r.PathValue("id"))
	if err != nil {
		fail(w, r, 404, "not_found", "network.unknown_camera")
		return
	}
	u := cn.URLs
	switch r.URL.Query().Get("via") {
	case "", "wifi":
	case "lan":
		if cn.LAN == nil {
			fail(w, r, 404, "not_found", "network.lan_off")
			return
		}
		u = *cn.LAN
	default:
		fail(w, r, 400, "bad_request", "network.bad_via")
		return
	}
	text := u.SRT
	switch r.URL.Query().Get("proto") {
	case "", "srt":
	case "rtmp":
		text = u.RTMP
	default:
		fail(w, r, 400, "bad_request", "network.bad_proto")
		return
	}
	s.writeQR(w, r, text)
}

func (s *Server) writeQR(w http.ResponseWriter, r *http.Request, text string) {
	svg, err := network.QRSVG(text)
	if err != nil {
		fail(w, r, 500, "internal", "network.qr_failed")
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(svg)
}

func (s *Server) postCamera(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	var body struct {
		Name *string `json:"name"`
	}
	if err := decode(r, &body); err != nil || body.Name == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"name": "…"}`)
		return
	}
	ctx, cancel := s.applyCtx(r)
	defer cancel()
	c, err := s.Network.CreateCamera(ctx, *body.Name)
	if s.cameraError(w, r, err, "camera add") {
		return
	}
	s.Log.Info("network camera added", "id", c.Camera.ID)
	w.Header().Set("Cache-Control", "no-store")
	c.AP.Error = netError(reqLang(r), c.AP.Error)
	writeJSON(w, applied(err, 201), c)
}

func (s *Server) patchCamera(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	var body struct {
		Name      *string `json:"name"`
		RotateKey bool    `json:"rotateKey"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"name"?: "…", "rotateKey"?: true}`)
		return
	}
	ctx, cancel := s.applyCtx(r)
	defer cancel()
	c, err := s.Network.UpdateCamera(ctx, r.PathValue("id"), body.Name, body.RotateKey)
	if s.cameraError(w, r, err, "camera change") {
		return
	}
	s.Log.Info("network camera changed", "id", c.Camera.ID, "newKey", body.RotateKey)
	w.Header().Set("Cache-Control", "no-store")
	c.AP.Error = netError(reqLang(r), c.AP.Error)
	writeJSON(w, applied(err, 200), c)
}

func (s *Server) deleteCamera(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	id := r.PathValue("id")
	ctx, cancel := s.applyCtx(r)
	defer cancel()
	err := s.Network.DeleteCamera(ctx, id)
	if s.cameraError(w, r, err, "camera removal") {
		return
	}
	s.Log.Info("network camera removed", "id", id)
	w.WriteHeader(204)
}

func (s *Server) putNetworkAP(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	var body struct {
		SSID           *string `json:"ssid"`
		Channel        *string `json:"channel"`
		Enabled        *bool   `json:"enabled"`
		RotatePassword bool    `json:"rotatePassword"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"ssid"?, "channel"?, "enabled"?, "rotatePassword"?}`)
		return
	}
	ctx, cancel := s.applyCtx(r)
	defer cancel()
	// A new name restarts the AP and everything on it reconnects (to the new name): not with a camera on air coming in
	// over it, the same rule as turning "Create my Wi-Fi" off (checked with the network lock held)
	_, err := s.Network.PutAP(ctx, body.SSID, body.Channel, body.Enabled, body.RotatePassword, s.APOffCheck(ctx))
	var oa *onAirError
	if errors.As(err, &oa) {
		fail(w, r, 409, "on_air", "network.ap_rename_on_air", oa.name)
		return
	}
	if errors.Is(err, errMixerUnreadable) {
		s.Log.Warn("rename the Wi-Fi", "err", err)
		fail(w, r, 503, "mixer_unreadable", "network.mixer_unreadable")
		return
	}
	if s.noInput(w, r, err) {
		return
	}
	if errors.Is(err, network.ErrInvalid) {
		fieldErrors(w, r, config.FieldErrors{"ssid": i18n.Ref("network.bad_ssid"), "channel": i18n.Ref("network.bad_channel")})
		return
	}
	if err != nil && !errors.Is(err, network.ErrApply) {
		s.Log.Error("change the cameras' network", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	s.Log.Info("cameras' network changed", "ssid", body.SSID != nil, "channel", body.Channel != nil, "enabled", body.Enabled, "newPassword", body.RotatePassword)
	// The same as GET /network (it carries accessPoint, as before): whoever changes something draws the new state without another GET
	writeJSON(w, applied(err, 200), netStatus(reqLang(r), s.Network.Status(ctx)))
}

// putNetwork: the RTMP/SRT reception of network cameras (NETWORK_SOURCES.md §6.12). Off: neither the receiver nor
// the cameras in the mixer; the board's Wi-Fi stays as it is and everything stays saved. Streaming with a network
// camera on air, it does not turn off (409 on_air): the program would go black or silent, and streaming is also
// started from the screen and the cloud, so a warning in the web is not enough.
func (s *Server) putNetwork(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decode(r, &body); err != nil || body.Enabled == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"enabled": true|false}`)
		return
	}
	ctx, cancel := s.applyCtx(r)
	defer cancel()
	// Checked with the network lock held, right before turning off (PutEnabled).
	// Without being able to read the mixer state, it does not turn off (unless the engine is stopped: then nothing streams)
	check := func() error {
		if s.Mixer == nil {
			return nil
		}
		st, err := s.Mixer.Read()
		if errors.Is(err, mixer.ErrOff) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %v", errMixerUnreadable, err)
		}
		if _, name := netOnAir(st); name != "" {
			return &onAirError{name}
		}
		return nil
	}
	st, err := s.Network.PutEnabled(ctx, *body.Enabled, check)
	var oa *onAirError
	if errors.As(err, &oa) {
		fail(w, r, 409, "on_air", "network.on_air", oa.name)
		return
	}
	if errors.Is(err, network.ErrNoInput) {
		fail(w, r, 409, "no_input", "network.no_input_on")
		return
	}
	if errors.Is(err, errMixerUnreadable) {
		s.Log.Warn("network cameras switch", "err", err)
		fail(w, r, 503, "mixer_unreadable", "network.mixer_unreadable")
		return
	}
	if err != nil && !errors.Is(err, network.ErrApply) {
		s.Log.Error("network cameras switch", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	if err != nil {
		s.Log.Warn("network cameras: the network did not end up as asked", "err", err)
	}
	s.Log.Info("network cameras", "enabled", *body.Enabled)
	writeJSON(w, applied(err, 200), netStatus(reqLang(r), st))
}

// errMixerUnreadable: the mixer state could not be read when turning off (503: without it, what is on air is unknown)
var errMixerUnreadable = errors.New("mixer state unreadable")

// onAirError: a network camera is in what is streamed (409 on_air)
type onAirError struct{ name string }

func (e *onAirError) Error() string { return "network camera on air: " + e.name }

// netOnAir: the id (net:camN) and name of the network camera in what is streamed (the main source, the secondary
// in PiP or side by side, or its audio playing alone), or "" if none or not streaming. Removed from the catalogue, the
// engine leaves its place black and its audio silent (it does not draw the no-signal card: it no longer has the source).
func netOnAir(st *mixer.State) (id, name string) {
	if st.Output == nil || st.Output.Mode != "on" {
		return "", ""
	}
	isNet := func(id *string) bool { return id != nil && strings.HasPrefix(*id, "net:") }
	nameOf := func(id string) string {
		for _, x := range st.Sources {
			if x.ID == id {
				return x.Name
			}
		}
		if st.Audio != nil {
			for _, x := range st.Audio.Sources {
				if x.ID == id {
					return x.Name
				}
			}
		}
		return id
	}
	p := st.Program
	switch {
	case isNet(p.Main):
		return *p.Main, nameOf(*p.Main)
	case isNet(p.Secondary) && (p.Layout == "pip" || p.Layout == "side"):
		return *p.Secondary, nameOf(*p.Secondary)
	case st.Audio != nil && st.Audio.Mode != "mix" && isNet(st.Audio.Main):
		return *st.Audio.Main, nameOf(*st.Audio.Main)
	}
	return "", ""
}

// getNetworkWiFi: the board's Wi-Fi (WIFI.md §5). No secrets.
func (s *Server) getNetworkWiFi(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	writeJSON(w, 200, wifiStatus(reqLang(r), s.Network.WiFi(r.Context())))
}

// putNetworkWiFi: "Create my Wi-Fi" (WIFI.md W3 and W7) and/or "Connect to a Wi-Fi" (W-B):
// {"ap"?: "always"|"rescue", "client"?: bool}. Works also with the board linked: these are local settings the
// cloud adopts (§7), not managed from it like the destination.
func (s *Server) putNetworkWiFi(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	var body struct {
		AP     *string `json:"ap"`
		Client *bool   `json:"client"`
	}
	if err := decode(r, &body); err != nil || (body.AP == nil && body.Client == nil) {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"ap"?: "always"|"rescue", "client"?: true|false}`)
		return
	}
	ctx, cancel := s.applyCtx(r)
	defer cancel()
	if body.Client != nil && !*body.Client && s.uplinkBusy(w, r) {
		return
	}
	st, err := s.Network.PutWiFi(ctx, network.WiFiChange{AP: body.AP, Client: body.Client}, s.APOffCheck(ctx))
	var oa *onAirError
	if errors.As(err, &oa) {
		fail(w, r, 409, "on_air", "network.ap_on_air", oa.name)
		return
	}
	if errors.Is(err, errMixerUnreadable) {
		s.Log.Warn("create my Wi-Fi", "err", err)
		fail(w, r, 503, "mixer_unreadable", "network.mixer_unreadable")
		return
	}
	if errors.Is(err, network.ErrInvalid) {
		fieldError(w, r, "ap", "network.bad_ap_policy")
		return
	}
	if s.noClient(w, r, err) {
		return
	}
	if err != nil && !errors.Is(err, network.ErrApply) {
		s.Log.Error("board Wi-Fi", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	if err != nil {
		s.Log.Warn("board Wi-Fi: did not end up as asked", "err", err)
	}
	var attrs []any
	if body.AP != nil {
		attrs = append(attrs, "createMyWifi", *body.AP)
	}
	if body.Client != nil {
		attrs = append(attrs, "connectToWifi", *body.Client)
	}
	s.Log.Info("board Wi-Fi", attrs...)
	writeJSON(w, applied(err, 200), wifiStatus(reqLang(r), st))
}

// APOffCheck: the check to turn "Create my Wi-Fi" off or to rename it, which restarts it (network.Manager.PutWiFi and
// PutAP call it with the network lock held): streaming with a camera on air that comes in OVER THAT WI-FI, *onAirError
// (409 on_air); without being able to read the mixer, errMixerUnreadable (503). If it comes in by cable, it allows.
// Used by PUT /network/wifi, PUT /network/ap {"ssid"} and the cloud (WIFI.md §7).
func (s *Server) APOffCheck(ctx context.Context) func() error {
	return func() error {
		if s.Mixer == nil {
			return nil
		}
		st, err := s.Mixer.Read()
		if errors.Is(err, mixer.ErrOff) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %v", errMixerUnreadable, err)
		}
		id, name := netOnAir(st)
		if id == "" {
			return nil
		}
		for _, c := range s.Network.Status(ctx).Cameras.Items {
			if "net:"+c.ID == id && c.Via != nil && *c.Via == "wifi" {
				return &onAirError{name}
			}
		}
		return nil
	}
}

// uplinkBusy: true (and answers 409 uplink_in_use) if the Wi-Fi client carries the stream now: streaming over SRTLA
// with it among the uplinks in use. Turning it off, changing its network or forgetting the connected one would cut
// that uplink live.
func (s *Server) uplinkBusy(w http.ResponseWriter, r *http.Request) bool {
	if s.Uplinks == nil || s.Network.Client == nil {
		return false
	}
	rs, ok := s.runState()
	if !ok || rs.Transport != "srtla" {
		return false
	}
	on := rs.Output == "on"
	if s.Mixer != nil {
		if m, err := s.Mixer.Read(); err == nil && m.Output != nil {
			on = m.Output.Mode == "on"
		}
	}
	if !on {
		return false
	}
	for _, u := range s.Uplinks.Read(time.Now(), true) {
		if u.Name == s.Network.Client.Iface && u.InUse {
			fail(w, r, 409, "uplink_in_use", "network.uplink_in_use")
			return true
		}
	}
	return false
}

// noClient: true if it already answered (no Wi-Fi client on this device).
func (s *Server) noClient(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, network.ErrNoClient) {
		return false
	}
	fail(w, r, 503, "wifi_unavailable", "network.no_wifi_client")
	return true
}

// getNetworkWiFiScan: the networks the Wi-Fi client sees (WIFI.md §4-§5), right away: the latest NetworkManager
// knows, with a new scan behind.
func (s *Server) getNetworkWiFiScan(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	nets, at, err := s.Network.ScanWiFi(r.Context())
	if s.noClient(w, r, err) {
		return
	}
	if errors.Is(err, wifi.ErrScanBusy) {
		fail(w, r, 503, "scan_busy", "network.scan_busy")
		return
	}
	if err != nil {
		s.Log.Warn("Wi-Fi client: scan", "err", err)
		fail(w, r, 503, "wifi_unavailable", "network.wifi_unresponsive")
		return
	}
	writeJSON(w, 200, map[string]any{"networks": nets, "at": at.UTC().Format(time.RFC3339)})
}

// postNetworkWiFiConnect: connect the Wi-Fi client (WIFI.md §5). {"ssid", "password"?}: a known network without a
// password uses its profile; with a password, it changes it. 🔴 The body is NEVER logged (nor the SSID: it says
// where the board is): warnings carry only the error code. Answers when connecting starts (202): the result shows
// in GET /network/wifi.
func (s *Server) postNetworkWiFiConnect(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	var body struct {
		SSID     *string `json:"ssid"`
		Password *string `json:"password"`
	}
	if err := decode(r, &body); err != nil || body.SSID == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"ssid": "…", "password"?: "…"}`)
		return
	}
	// Changing network while the current one carries the stream would cut it
	if cl := s.Network.WiFi(r.Context()).Client; cl != nil && cl.State.State == "connected" && cl.SSID != nil && *cl.SSID != *body.SSID && s.uplinkBusy(w, r) {
		return
	}
	err := s.Network.ConnectWiFi(r.Context(), *body.SSID, body.Password)
	body.Password = nil
	switch {
	case s.noClient(w, r, err):
	case errors.Is(err, network.ErrClientOff):
		fail(w, r, 409, "client_off", "network.client_off")
	case errors.Is(err, wifi.ErrInvalid):
		fieldErrors(w, r, config.FieldErrors{"ssid": i18n.Ref("network.bad_wifi_ssid"), "password": i18n.Ref("network.bad_wifi_password")})
	case errors.Is(err, wifi.ErrNotFound):
		fail(w, r, 404, "not_found", "network.wifi_not_found")
	case errors.Is(err, wifi.ErrUnsupported):
		fail(w, r, 409, "unsupported", "network.wifi_unsupported")
	case err != nil:
		s.Log.Warn("Wi-Fi client: could not connect") // without the error: it may carry the SSID
		fail(w, r, 503, "wifi_unavailable", "network.wifi_unresponsive")
	default:
		s.Log.Info("Wi-Fi client: connecting")
		writeJSON(w, 202, wifiStatus(reqLang(r), s.Network.WiFi(r.Context())))
	}
}

// postNetworkWiFiForget: forget a known network (WIFI.md §5). {"ssid"}.
func (s *Server) postNetworkWiFiForget(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	var body struct {
		SSID *string `json:"ssid"`
	}
	if err := decode(r, &body); err != nil || body.SSID == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"ssid": "…"}`)
		return
	}
	// Forgetting the connected one disconnects it: not while it carries the stream
	if cl := s.Network.WiFi(r.Context()).Client; cl != nil && cl.State.State == "connected" && cl.SSID != nil && *cl.SSID == *body.SSID && s.uplinkBusy(w, r) {
		return
	}
	err := s.Network.ForgetWiFi(r.Context(), *body.SSID)
	switch {
	case s.noClient(w, r, err):
	case errors.Is(err, wifi.ErrInvalid):
		fail(w, r, 400, "bad_request", "network.bad_ssid_short")
	case errors.Is(err, wifi.ErrNotFound):
		fail(w, r, 404, "not_found", "network.wifi_unknown")
	case err != nil:
		s.Log.Warn("Wi-Fi client: could not forget")
		fail(w, r, 503, "wifi_unavailable", "network.wifi_unresponsive")
	default:
		s.Log.Info("Wi-Fi client: network forgotten")
		w.WriteHeader(204)
	}
}

// putNetworkLAN: wired cameras (over the eth0 network, NETWORK_SOURCES.md §6.11).
func (s *Server) putNetworkLAN(w http.ResponseWriter, r *http.Request) {
	if s.networkOff(w, r) {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decode(r, &body); err != nil || body.Enabled == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"enabled": true|false}`)
		return
	}
	ctx, cancel := s.applyCtx(r)
	defer cancel()
	_, err := s.Network.PutLAN(ctx, *body.Enabled)
	if s.noInput(w, r, err) {
		return
	}
	if err != nil && !errors.Is(err, network.ErrApply) {
		s.Log.Error("wired cameras", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	if err != nil {
		s.Log.Warn("wired cameras: the network did not end up as asked", "err", err)
	}
	s.Log.Info("wired cameras", "enabled", *body.Enabled)
	writeJSON(w, applied(err, 200), netStatus(reqLang(r), s.Network.Status(ctx))) // like GET /network (it carries lan, as before)
}

// noInput: true if it already answered (turning Wi-Fi and cable off at once).
func (s *Server) noInput(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, network.ErrNoInput) {
		return false
	}
	fail(w, r, 409, "no_input", "network.no_input_off")
	return true
}

// applied: 202 if saved but the network did not end up as asked (ErrApply): the reason is in accessPoint.
func applied(err error, ok int) int {
	if err != nil {
		return 202
	}
	return ok
}

// cameraError: true if it already answered. ErrApply does not stop: the configuration was saved and the
// access point state says why it did not come up.
func (s *Server) cameraError(w http.ResponseWriter, r *http.Request, err error, what string) bool {
	switch {
	case err == nil, errors.Is(err, network.ErrApply):
		if err != nil {
			s.Log.Warn(what+": the access point did not come up", "err", err)
		}
		return false
	case errors.Is(err, network.ErrInvalid):
		fieldError(w, r, "name", "network.bad_camera_name")
	case errors.Is(err, network.ErrLimit):
		fail(w, r, 409, "limit", "network.camera_limit")
	case errors.Is(err, network.ErrDisabled):
		fail(w, r, 409, "disabled", "network.cameras_off")
	case errors.Is(err, network.ErrUnknown):
		fail(w, r, 404, "not_found", "network.unknown_camera")
	default:
		s.Log.Error(what, "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
	}
	return true
}

// applyCtx: applying the network takes time (creating ap0 ~10 s); not cut if the browser hangs up halfway.
func (s *Server) applyCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
}
