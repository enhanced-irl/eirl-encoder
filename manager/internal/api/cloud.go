package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sync/atomic"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/network"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

// CloudControl is cloud.Client as the local API sees it (an interface for the tests).
type CloudControl interface {
	View() cloud.View
	Pair(ctx context.Context) (cloud.View, error)
	Unlink(ctx context.Context) (cloudNotified bool)
}

// --- local API: /api/v1/cloud ----------------------------------------------------------------

// cloudView: GET /cloud and POST /cloud/pair. With ?qr=rows and a pairing code, linkQrRows is the QR of
// linkUrl in rows of 1 and 0 (for the touch screen, like the Wi-Fi QR). Nothing secret: the view never
// carries the token or the pollSecret.
type cloudView struct {
	cloud.View
	LinkQRRows []string `json:"linkQrRows,omitempty"`
}

func (s *Server) cloudView(r *http.Request, v cloud.View) cloudView {
	out := cloudView{View: v}
	if r.URL.Query().Get("qr") == "rows" && v.LinkURL != "" {
		if rows, err := network.QRRows(v.LinkURL); err == nil {
			out.LinkQRRows = rows
		}
	}
	return out
}

func (s *Server) getCloud(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		fail(w, r, 503, "cloud_disabled", "cloud.disabled")
		return
	}
	writeJSON(w, 200, s.cloudView(r, s.Cloud.View()))
}

// pairCloud starts linking or renews the code.
func (s *Server) pairCloud(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		fail(w, r, 503, "cloud_disabled", "cloud.disabled")
		return
	}
	var body struct{}
	if err := decode(r, &body); err != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	v, err := s.Cloud.Pair(ctx)
	switch {
	case err == nil:
		writeJSON(w, 200, s.cloudView(r, v))
	case errors.Is(err, cloud.ErrAlreadyLinked):
		fail(w, r, 409, "already_linked", "cloud.already_linked")
	case errors.Is(err, cloud.ErrRateLimited):
		fail(w, r, 429, "rate_limited", "cloud.rate_limited")
	case errors.Is(err, cloud.ErrUnreachable):
		fail(w, r, 502, "cloud_unreachable", "cloud.unreachable")
	case errors.Is(err, cloud.ErrNoHardwareID):
		fail(w, r, 500, "no_device_id", "cloud.no_device_id")
	case errors.Is(err, cloud.ErrNoIdentity):
		fail(w, r, 500, "no_identity", "cloud.no_identity")
	default:
		s.Log.Warn("cloud linking", "err", err)
		fail(w, r, 502, "cloud_error", "cloud.error")
	}
}

// unlinkCloud unlinks: removes the state even if the cloud does not answer (cloudNotified says whether
// it heard: if not, it should be removed in the app too).
func (s *Server) unlinkCloud(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		fail(w, r, 503, "cloud_disabled", "cloud.disabled")
		return
	}
	// Not cut if the caller hangs up: with a destination the cloud set, unlinking restarts the engine (Unlinked), and
	// over the socket the caller is the engine: the cloud would not hear about it
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	notified := s.Cloud.Unlink(ctx)
	writeJSON(w, 200, struct {
		cloud.View
		CloudNotified bool `json:"cloudNotified"`
	}{s.Cloud.View(), notified})
}

// --- The real device for the cloud client (cloud.Device) -------------------------------------

// CloudDevice: the Jetson as the cloud client sees it. Everything goes through the same as the local
// API (config.Store with its validation, the unit with its sudo commands, auth, and the same "apply"
// rule, applySaved). The cloud can do what the local web can PLUS two powers the local web does not
// have without a password (ENCODER_CLOUD.md §10): `reboot` and `set_local_password` (change the local
// password without knowing the current one). Counterweight planned: the setup code will ALWAYS work in
// the local portal as a rescue credential, so that a compromised account cannot lock out the owner who
// has the device in front of them (MANAGER.md §6; tonight the code only sets the FIRST password,
// SETUP.md §4). Messages to the cloud are in the device language.
type CloudDevice struct {
	Srv   *Server
	Ident cloud.DeviceInfo // Capabilities without thumbnail: Info adds it if the thumbnail is active
	// Thumbnail: Thumb = /run/eirl-engine/thumb.jpg ("" = none); ThumbEnabled = THUMB is not 0 in
	// /etc/default/eirl-engine; ThumbMaxAge = older than this, the stream is stopped (3 × THUMB_EVERY)
	Thumb        string
	ThumbEnabled bool
	ThumbMaxAge  time.Duration
	Port         int // port of the local web, for lanUrls
	Rebooter     Rebooter
	Uptime       func() time.Duration // nil = /proc/uptime (the tests replace it)
	// Linked: the device is still linked (cloud.Client.Linked); nil = yes. A config.set is not written once unlinked
	Linked func() bool

	thumbSeen atomic.Bool
}

// Rebooter: restart the board (service.Unit: sudo -n /bin/systemctl reboot).
type Rebooter interface {
	Reboot(ctx context.Context) error
	CanReboot(ctx context.Context) error // sudo -n -l: the permission exists
}

// Info: the thumbnail capability is only announced if the thumbnail is really active (THUMB not 0 and a
// recent JPEG at start or in some hello). Once seen, it stays (with the stream stopped there is no
// thumbnail, but the capability is still the device's).
func (d *CloudDevice) Info() cloud.DeviceInfo {
	info := d.Ident
	info.Capabilities = slices.Clone(d.Ident.Capabilities)
	if d.ThumbActive() {
		info.Capabilities = append(info.Capabilities, cloud.CapThumbnail)
	}
	return info
}

// ThumbActive: see Info. Cheap (one stat) until it sees it once.
func (d *CloudDevice) ThumbActive() bool {
	if d.thumbSeen.Load() {
		return true
	}
	if !d.ThumbEnabled || d.Thumb == "" {
		return false
	}
	if _, ok := d.Thumbnail(); ok {
		d.thumbSeen.Store(true)
		return true
	}
	return false
}

func (d *CloudDevice) LanURLs() []string {
	h, _ := os.Hostname()
	return cloud.LanURLs(h, d.Port)
}

func (d *CloudDevice) Status(ctx context.Context) cloud.EncoderStatus {
	return CloudStatus(d.Srv.collect(ctx), time.Now())
}

func (d *CloudDevice) Reported() (cloud.ReportedConfig, error) {
	ds, cfg, err := d.Srv.Config.Destinations()
	if err != nil {
		return cloud.ReportedConfig{}, err
	}
	rep := cloud.Reported(cfg, ds, d.disabled())
	// With eirl-netsetup, the real policy ("Create my Wi-Fi", with rule W4); without it, the client sets it
	if d.Srv.Network != nil {
		rep.AccessPoint = d.Srv.Network.APPolicy()
	}
	return rep, nil
}

func (d *CloudDevice) disabled() []string {
	if d.Srv.Uplinks == nil {
		return nil
	}
	names, _ := uplinks.ReadDisabled(d.Srv.Uplinks.Disabled)
	return names
}

// errUnlinked: the device was unlinked while a config.set arrived: nothing is written.
var errUnlinked = errors.New("unlinked: config.set dropped")

func (d *CloudDevice) ApplyConfig(ctx context.Context, w cloud.WireConfig, apply bool) (cloud.ApplyResult, error) {
	var res cloud.ApplyResult
	before := d.Srv.Config.Snapshot()
	var disabled []string
	// The cloud manages only its own destination (eirl) in the saved list (OUTPUTS.md §10)
	_, cfg, err := d.Srv.Config.UpdateDestinations(func(ds *config.Destinations, cur *config.Settings) error {
		// With the Store's lock: if it was unlinked (or the session was cut) meanwhile, nothing is
		// written. Unlinking waits for the session and then removes the destination.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.Linked != nil && !d.Linked() {
			return errUnlinked
		}
		nds, next, dis, fe := cloud.ApplyWireDestinations(*ds, *cur, w)
		if fe != nil {
			return fe
		}
		*ds, *cur, disabled = nds, next, dis
		return nil
	})
	if err != nil {
		return res, err
	}
	res.Saved = true
	d.Srv.logConfig("configuration saved (EnhancedIRL)", cfg)
	// Disabled uplinks: applied live (the stream rereads the file every 2 s)
	if d.Srv.Uplinks != nil {
		cur := d.disabled()
		slices.Sort(cur)
		want := slices.Clone(disabled)
		slices.Sort(want)
		if !slices.Equal(cur, want) {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			if err := config.WriteAtomic(d.Srv.Uplinks.Disabled, uplinks.Render(disabled), 0o640); err != nil {
				return res, err
			}
		}
	}
	if !apply || ctx.Err() != nil {
		return res, ctx.Err()
	}
	res.Restarted, err = d.Srv.applyAfterSave(ctx, before)
	return res, err
}

// minUptimeForCloudReboot: time switched on before another restart from the cloud is accepted.
const minUptimeForCloudReboot = 10 * time.Minute

// uptime: how long the board has been on (/proc/uptime); Uptime replaces it in the tests.
func (d *CloudDevice) uptime() (time.Duration, bool) {
	if d.Uptime != nil {
		return d.Uptime(), true
	}
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	var secs float64
	if _, err := fmt.Sscanf(string(b), "%f", &secs); err != nil {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// actionError: a cloud.ActionError with its message in the device language.
func (d *CloudDevice) actionError(code, key string, args ...any) *cloud.ActionError {
	return &cloud.ActionError{Code: code, Message: i18n.Msg(d.Srv.deviceLanguage(), key, args...)}
}

func (d *CloudDevice) CanReboot(ctx context.Context) error {
	if d.Rebooter == nil {
		return d.actionError("unsupported", "cloud.no_remote_reboot")
	}
	if err := d.Rebooter.CanReboot(ctx); err != nil {
		d.Srv.Log.Warn("remote restart without sudo permission", "err", err)
		return d.actionError("unsupported", "cloud.no_remote_reboot")
	}
	return nil
}

func (d *CloudDevice) Action(ctx context.Context, name, password string) error {
	switch name {
	case "start":
		return d.setOutput(ctx, "on")
	case "stop", "rehearsal":
		// No rehearsal mode (F3): stop streaming and the preview goes on, with the mixer (live) or without
		// it (restarts with OUTPUT=off). rehearsal is the old name; stopping eirl-engine is no longer done
		return d.setOutput(ctx, "off")
	case "restart":
		return d.Srv.Unit.Control(ctx, name)
	case "reboot":
		if err := d.CanReboot(ctx); err != nil {
			return err
		}
		// Brake: a compromised account (or backend) cannot keep the board in a restart loop, with no time
		// for the owner to reach the portal and unlink it
		if up, ok := d.uptime(); ok && up < minUptimeForCloudReboot {
			return d.actionError("too_soon", "cloud.reboot_too_soon", int(minUptimeForCloudReboot/time.Minute))
		}
		d.Srv.Log.Warn("restarting the board on EnhancedIRL's order")
		return d.Rebooter.Reboot(ctx)
	case "set_local_password":
		err := d.Srv.Auth.SetPassword(password)
		if ref, ok := passwordError(err); ok {
			return &cloud.ActionError{Code: "validation", Message: i18n.Text(d.Srv.deviceLanguage(), ref)}
		}
		if err == nil {
			d.Srv.Log.Info("local password changed by EnhancedIRL: local sessions closed")
		}
		return err
	}
	return d.actionError("unknown_action", "common.unknown_action")
}

// setOutput: Start (on) or Stop streaming (off), like the local web's buttons (PUT /output with apply
// and, if it did not restart, POST /service/start): saves the mode, applies with the common rule
// (applySaved) and, if the stream was stopped or failed, starts it.
func (d *CloudDevice) setOutput(ctx context.Context, output string) error {
	before := d.Srv.Config.Snapshot()
	_, err := d.Srv.Config.Update(func(cur config.Settings) (config.Settings, error) {
		cur.Output = output
		return cur, cur.Validate()
	})
	var fe config.FieldErrors
	if errors.As(err, &fe) {
		if _, ok := fe["url"]; ok {
			return d.actionError("no_destination", "cloud.no_destination")
		}
		return &cloud.ActionError{Code: "validation", Message: fe.Message(d.Srv.deviceLanguage())}
	}
	if err != nil {
		return err
	}
	restarted, err := d.Srv.applyAfterSave(ctx, before)
	if err != nil || restarted {
		return err
	}
	st, err := d.Srv.Unit.Status(ctx)
	if err != nil {
		return err
	}
	if st.State == "inactive" || st.State == "failed" {
		return d.Srv.Unit.Control(ctx, "start")
	}
	return nil // it already was
}

func (d *CloudDevice) Thumbnail() (cloud.Thumbnail, bool) {
	if d.Thumb == "" {
		return cloud.Thumbnail{}, false
	}
	age := d.ThumbMaxAge
	if age == 0 {
		age = 15 * time.Second
	}
	return cloud.ReadThumbnail(d.Thumb, age, time.Now())
}

func (d *CloudDevice) Follow(ctx context.Context, fn func(service.Entry)) error {
	return d.Srv.Unit.Follow(ctx, fn)
}

func (d *CloudDevice) Uplinks() []string {
	if d.Srv.Uplinks == nil {
		return nil
	}
	return d.Srv.Uplinks.AvailableNames()
}

// Unlinked: the cloud's destination (eirl) is removed (the key belongs to the account that no longer has the
// device); if it was the active one, nothing is active and streaming stops (the picture goes on), with the common
// "apply" rule. The device's own destinations stay. clearDestination (the cloud set the destination, cloud.json) is
// what the saved list already says: the cloud's destination exists only if the cloud set it.
func (d *CloudDevice) Unlinked(ctx context.Context, clearDestination bool) error {
	before := d.Srv.Config.Snapshot()
	removed := false
	cfg, err := d.updateDestinations(func(ds *config.Destinations, cur *config.Settings) error {
		var wasActive bool
		if removed, wasActive = ds.Remove(config.CloudID); wasActive {
			cur.Output = "off"
		}
		return nil
	})
	if err != nil || !removed {
		return err
	}
	d.Srv.logConfig("unlinked: EnhancedIRL destination removed", cfg)
	_, err = d.Srv.applyAfterSave(ctx, before)
	return err
}

func (d *CloudDevice) updateDestinations(fn func(*config.Destinations, *config.Settings) error) (config.Settings, error) {
	_, cfg, err := d.Srv.Config.UpdateDestinations(fn)
	return cfg, err
}

// jsonFields: the JSON fields of v that pass keep (nil if none). So the mapping does not have to know
// the fields other branches add to the local snapshot.
func jsonFields(v any, keep func(string) bool) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	var out map[string]any
	for k, val := range m {
		if keep(k) {
			if out == nil {
				out = map[string]any{}
			}
			out[k] = val
		}
	}
	return out
}

// CloudStatus: the GET /api/v1/status snapshot in the shape of EncoderStatus (§4.1), WITHOUT secrets:
// no destination, no key, no uplink IPs. (The mixer's sources, and so their aliases, are not sent.)
func CloudStatus(st Status, now time.Time) cloud.EncoderStatus {
	out := cloud.EncoderStatus{
		Phase:          st.Service.Phase,
		Service:        cloud.StatusService{State: st.Service.State, Restarts: st.Service.Restarts},
		Output:         st.Output,
		PendingRestart: st.PendingRestart,
		// input: everything the local API gives (multiple sources adds fields: passed unfiltered)
		Input: cloud.StatusInput{EDIDPreferred: st.Input.EDIDPreferred,
			Extra: jsonFields(st.Input, func(k string) bool { return k != "edidPreferred" && k != "edidActive" })},
		// signal: the contract's + kind and measuredFps (multiple sources); not the CSI diagnostics
		Signal: cloud.StatusSignal{Present: st.Signal.Present, Width: st.Signal.Width, Height: st.Signal.Height,
			FPS: st.Signal.FPS, Interlaced: st.Signal.Interlaced, Supported: st.Signal.Supported,
			Extra: jsonFields(st.Signal, func(k string) bool { return k == "kind" || k == "measuredFps" })},
		Audio:       cloud.StatusAudio{State: st.Audio.State},
		Transport:   st.Transport,
		OutputState: cloud.StatusOutput{State: st.OutputState.State, Reason: st.OutputState.Reason, Detail: st.OutputState.Detail},
		System: cloud.StatusSystem{CPUPct: st.System.CPUPct, Undervoltage: st.System.Undervoltage,
			TempC: cloud.StatusTemp{CPU: st.System.TempC["cpu"], GPU: st.System.TempC["gpu"]}},
		At: now.UTC(),
	}
	if !st.Service.Since.IsZero() {
		since := st.Service.Since.UTC()
		out.Service.Since = &since
	}
	if st.Input.EDIDActive != "" {
		a := st.Input.EDIDActive
		out.Input.EDIDActive = &a
	}
	if st.SRT != nil {
		out.SRT = &cloud.StatusSRT{Connected: st.SRT.Connected, RTTMs: st.SRT.RTTMs, SendMbps: st.SRT.SendMbps,
			LossPct: st.SRT.LossPct, PktRetrans: st.SRT.Retrans, PktDropped: st.SRT.Dropped}
	}
	// The board's access point: its policy ("Create my Wi-Fi", WIFI.md §7) and, when on, its name and the
	// connected devices; nothing else.
	// The Wi-Fi client: only the connected network and its signal (WIFI.md §5). Never the scan or the known
	// networks: the SSIDs around say where the board is
	if st.WiFi != nil && st.WiFi.Client != nil && st.WiFi.Client.State.State == "connected" && st.WiFi.Client.SSID != nil && st.WiFi.Client.Signal != nil {
		c := st.WiFi.Client
		out.Network.WiFi = &cloud.StatusWiFi{SSID: *c.SSID, Signal: *c.Signal}
	}
	if n := st.Network; n != nil {
		out.Network.AccessPoint.Policy = n.AccessPoint.Policy
		if n.AccessPoint.State == "on" {
			out.Network.AccessPoint.On, out.Network.AccessPoint.SSID, out.Network.AccessPoint.Clients = true, n.AccessPoint.SSID, n.AccessPoint.Clients
		}
	}
	for _, u := range st.Uplinks {
		kind := u.Kind
		if kind == "modem" {
			kind = "usb" // the cloud contract knows ethernet | usb | wifi; modems reach it in phase M6 (MODEMS.md)
		}
		out.Uplinks = append(out.Uplinks, cloud.StatusUplink{Name: u.Name, Kind: kind, InUse: u.InUse,
			Disabled: u.Disabled, Conflict: u.Conflict, TxKbps: u.TxKbps, NoInternet: u.NoInternet})
	}
	if st.Bitrate != nil {
		out.Bitrate = &cloud.StatusBitrate{Kbps: st.Bitrate.Kbps, MinKbps: st.Bitrate.MinKbps, MaxKbps: st.Bitrate.MaxKbps}
	}
	if p := st.System.Power; p != nil {
		out.System.Power = &cloud.StatusPower{InMw: p.InMw, InMv: p.InMv}
	}
	out.Normalize()
	return out
}
