package appapi

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// MIXER.md §21's actions and reads against the fake app.

func destBody() map[string]any {
	return map[string]any{"managedBy": "local", "url": "srtla://eu1.relay.enhancedirl.com:5000", "mode": "caller",
		"latencyMs": 2000, "streamId": map[string]any{"set": true, "masked": "publish:a1…"},
		"passphrase": map[string]any{"set": false}, "videoBitrateKbps": 6000, "audioBitrateKbps": 128,
		"output": "on", "transport": "srtla", "adaptiveBitrate": true, "videoMinBitrateKbps": 1500,
		"edidPreferred": "1080p30", "pendingRestart": true}
}

// run queues an action and runs it, as the goroutine would; the result.
func run(t *testing.T, d *DeviceState, act ui.AppAction) *ui.AppResult {
	t.Helper()
	if !d.Do(act) {
		t.Fatalf("not queued: %+v", act)
	}
	q, _, _, _ := d.take()
	d.runAction(*q)
	return d.AppSnapshot().Last
}

func TestEveryParityActionHasARoute(t *testing.T) {
	for _, w := range ui.AppActions {
		if _, ok := actionRoutes[w]; !ok || !parityActions[w] {
			t.Errorf("%s: no route", w)
		}
	}
}

func TestDestSaveSecrets(t *testing.T) {
	a, d := newDevice(t)
	a.route("PUT /destination", 200, map[string]any{"managedBy": "local", "url": "srt://relay:5000", "mode": "caller",
		"latencyMs": 2100, "streamId": map[string]any{"set": true}, "passphrase": map[string]any{"set": true},
		"videoBitrateKbps": 6000, "audioBitrateKbps": 128, "output": "on", "transport": "srt",
		"adaptiveBitrate": false, "videoMinBitrateKbps": 1500, "restarted": true})
	sid, pass := []byte(`publish:k"ey\1`), []byte("Passphrase-0123")
	act := ui.AppAction{What: "dest_save", Body: map[string]any{"latencyMs": 2100, "apply": true},
		Secrets: map[string][]byte{"streamId": sid, "passphrase": pass}}
	if !d.Do(act) {
		t.Fatal("not queued")
	}
	clear(sid) // the caller wipes its own at once: the client has its copy
	clear(pass)
	q, _, _, _ := d.take()
	if strings.Contains(fmt.Sprintf("%+v %v %#v", d.AppSnapshot(), q, q), "Passphrase") || q.a.Secrets != nil {
		t.Fatal("a secret shows")
	}
	var kept [][]byte
	for _, s := range q.secrets {
		kept = append(kept, s.b)
	}
	d.runAction(*q)
	a.mu.Lock()
	raw := string(a.raws[len(a.raws)-1])
	a.mu.Unlock()
	if raw != `{"passphrase":"Passphrase-0123","streamId":"publish:k\"ey\\1","apply":true,"latencyMs":2100}` {
		t.Errorf("body %s", raw)
	}
	for _, b := range kept {
		for _, c := range b {
			if c != 0 {
				t.Fatal("a secret is still in memory")
			}
		}
	}
	s := d.AppSnapshot()
	if s.Last == nil || !s.Last.OK || s.Last.Restarted == nil || !*s.Last.Restarted || s.Dest == nil ||
		s.Dest.LatencyMs != 2100 || !s.Dest.PassphraseSet || s.Pending != "" {
		t.Fatalf("%+v %+v", s.Last, s.Dest)
	}
	// Clearing a secret is a null field; the app's field errors come back
	a.route("PUT /destination", 400, map[string]any{"error": "validation", "fields": map[string]any{"url": "falta el puerto"}})
	r := run(t, d, ui.AppAction{What: "dest_save", Body: map[string]any{"streamId": nil, "url": "srt://x", "apply": false}})
	if got := a.last().body; !reflect.DeepEqual(got, map[string]any{"streamId": nil, "url": "srt://x", "apply": false}) {
		t.Errorf("%v", got)
	}
	if r.OK || r.Status != 400 || r.Fields["url"].Message != "falta el puerto" {
		t.Errorf("%+v", r)
	}
}

func TestParityRoutes(t *testing.T) {
	a, d := newDevice(t)
	for _, k := range []string{"POST /service/restart", "PUT /uplinks/usb%200", "PUT /input", "DELETE /cloud",
		"PUT /password", "POST /cameras", "PATCH /cameras/cam2", "DELETE /cameras/cam2", "PUT /network/ap",
		"PUT /network/lan", "PUT /modems/m-5a1d0c3e9b21", "POST /modems/m-5a1d0c3e9b21/pin", "POST /modems/m-5a1d0c3e9b21/reset",
		"POST /modems/h-9b2c41d07e5a/subnet"} {
		a.route(k, 200, map[string]any{})
	}
	cases := []struct {
		act  ui.AppAction
		want seenReq
	}{
		{ui.AppAction{What: "restart"}, seenReq{"POST", "/api/v1/service/restart", map[string]any{}}},
		{ui.AppAction{What: "uplink", ID: "usb 0", Body: map[string]any{"disabled": true}},
			seenReq{"PUT", "/api/v1/uplinks/usb%200", map[string]any{"disabled": true}}},
		{ui.AppAction{What: "input_save", Body: map[string]any{"source": "hdmi", "audio": "auto", "apply": false}},
			seenReq{"PUT", "/api/v1/input", map[string]any{"source": "hdmi", "audio": "auto", "apply": false}}},
		{ui.AppAction{What: "cloud_unlink"}, seenReq{"DELETE", "/api/v1/cloud", nil}},
		{ui.AppAction{What: "cam_add", Body: map[string]any{"name": "Móvil"}},
			seenReq{"POST", "/api/v1/cameras", map[string]any{"name": "Móvil"}}},
		{ui.AppAction{What: "cam_rename", ID: "cam2", Body: map[string]any{"name": "Grúa"}},
			seenReq{"PATCH", "/api/v1/cameras/cam2", map[string]any{"name": "Grúa"}}},
		{ui.AppAction{What: "cam_rotate", ID: "cam2"}, seenReq{"PATCH", "/api/v1/cameras/cam2", map[string]any{"rotateKey": true}}},
		{ui.AppAction{What: "cam_remove", ID: "cam2"}, seenReq{"DELETE", "/api/v1/cameras/cam2", nil}},
		{ui.AppAction{What: "ap_enabled", Body: map[string]any{"enabled": false}},
			seenReq{"PUT", "/api/v1/network/ap", map[string]any{"enabled": false}}},
		{ui.AppAction{What: "ap_channel", Body: map[string]any{"channel": "6"}},
			seenReq{"PUT", "/api/v1/network/ap", map[string]any{"channel": "6"}}},
		{ui.AppAction{What: "ap_rotate"}, seenReq{"PUT", "/api/v1/network/ap", map[string]any{"rotatePassword": true}}},
		{ui.AppAction{What: "lan_enabled", Body: map[string]any{"enabled": true}},
			seenReq{"PUT", "/api/v1/network/lan", map[string]any{"enabled": true}}},
		{ui.AppAction{What: "password", Secrets: map[string][]byte{"current": []byte("vieja-0123"), "new": []byte("nueva-0123")}},
			seenReq{"PUT", "/api/v1/password", map[string]any{"current": "vieja-0123", "new": "nueva-0123"}}},
		{ui.AppAction{What: "modem_save", ID: "m-5a1d0c3e9b21", Body: map[string]any{"apn": "web.es", "force": true}},
			seenReq{"PUT", "/api/v1/modems/m-5a1d0c3e9b21", map[string]any{"apn": "web.es", "force": true}}},
		{ui.AppAction{What: "modem_pin", ID: "m-5a1d0c3e9b21", Body: map[string]any{"remove": false},
			Secrets: map[string][]byte{"pin": []byte("1234")}},
			seenReq{"POST", "/api/v1/modems/m-5a1d0c3e9b21/pin", map[string]any{"pin": "1234", "remove": false}}},
		{ui.AppAction{What: "modem_reset", ID: "m-5a1d0c3e9b21", Body: map[string]any{}},
			seenReq{"POST", "/api/v1/modems/m-5a1d0c3e9b21/reset", map[string]any{}}},
		{ui.AppAction{What: "modem_subnet", ID: "h-9b2c41d07e5a", Body: map[string]any{}},
			seenReq{"POST", "/api/v1/modems/h-9b2c41d07e5a/subnet", map[string]any{}}},
	}
	for _, c := range cases {
		if r := run(t, d, c.act); !r.OK {
			t.Errorf("%s: %+v", c.act.What, r)
		}
		if got := a.last(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.act.What, got, c.want)
		}
	}
	// An action with {id} and no id never reaches the app
	n := len(a.allSeen())
	if r := run(t, d, ui.AppAction{What: "cam_remove"}); r.OK || r.Status != 400 || len(a.allSeen()) != n {
		t.Errorf("%+v", r)
	}
}

func TestParityReads(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /destination", 200, destBody())
	d.PollDest()
	s := d.AppSnapshot()
	want := &ui.DestInfo{ManagedBy: "local", URL: "srtla://eu1.relay.enhancedirl.com:5000", Mode: "caller", LatencyMs: 2000,
		StreamIDSet: true, StreamIDMasked: "publish:a1…", VideoKbps: 6000, AudioKbps: 128, ABR: true, VideoMinKbps: 1500,
		Transport: "srtla", Output: "on", PendingRestart: true}
	if s.DestAPI != ui.Yes || !reflect.DeepEqual(s.Dest, want) {
		t.Fatalf("%+v", s.Dest)
	}
	a.route("GET /status", 200, map[string]any{"service": map[string]any{"state": "active"}, "transport": "srtla",
		"output": "on", "pendingRestart": false,
		"uplinks": []any{
			map[string]any{"name": "usb0", "ip": "192.168.8.100", "kind": "usb", "inUse": true, "disabled": false, "txKbps": 2480},
			map[string]any{"name": "bad name;", "ip": "x"},
			map[string]any{"name": "wlan0", "kind": "wifi", "noInternet": true, "conflict": true}},
		"input": map[string]any{"source": "usb", "device": "/dev/v4l/by-id/usb-Cam-video-index0", "format": "NV12",
			"mode": "1920x1080@30", "audio": "alsa:Cam", "edidPreferred": "1080p30",
			"active": map[string]any{"state": "ready", "source": "usb", "name": "Cam Link", "format": "NV12", "mode": "1920x1080@30"}}})
	d.PollStatus()
	st := d.AppSnapshot().Status
	if st == nil || !st.HasUplinks || len(st.Uplinks) != 2 || st.Uplinks[0].TxKbps != 2480 || !st.Uplinks[1].Conflict ||
		st.Input.Source != "usb" || st.Input.Format != "NV12" || st.Input.Audio != "alsa:Cam" || st.Input.ActiveState != "ready" || st.Input.ActiveSource != "usb" {
		t.Fatalf("%+v", st)
	}
	a.route("GET /sources", 200, map[string]any{
		"sources": []any{
			map[string]any{"id": "hdmi", "kind": "hdmi-csi", "name": "HDMI de la placa"},
			map[string]any{"id": "/dev/v4l/by-id/usb-Cam-video-index0", "kind": "usb", "name": "Cam Link",
				"usb":     map[string]any{"speedMbps": 5000},
				"auto":    map[string]any{"format": "NV12", "mode": "1920x1080@30", "fps": 30, "outWidth": 1920, "outHeight": 1080},
				"options": []any{map[string]any{"format": "MJPG", "mode": "1280x720@60", "fps": 59.94}, map[string]any{"format": "<b>"}},
				"audio":   map[string]any{"name": "Cam Link audio"}, "warnings": []any{"usb2"}},
			map[string]any{"id": "x", "kind": "toaster"}},
		"audio": []any{map[string]any{"id": "i2s", "kind": "i2s", "name": "HDMI"}, map[string]any{"id": "rm -rf", "kind": "usb"}}})
	d.PollSources()
	src := d.AppSnapshot().Sources
	if src == nil || len(src.Sources) != 2 || !src.Sources[1].USB3 || !src.Sources[1].USBKnown || src.Sources[0].USBKnown ||
		src.Sources[1].Auto == nil ||
		len(src.Sources[1].Options) != 1 || src.Sources[1].Options[0].FPS != 59.94 || len(src.Audio) != 1 {
		t.Fatalf("%+v", src)
	}
	// What the eyes ask for, and dropping it
	a.route("GET /destination/secrets", 200, map[string]any{"streamId": "publish:abc", "passphrase": ""})
	a.route("GET /network/credentials?cameras=1", 200, map[string]any{"cameras": []any{
		map[string]any{"id": "cam1", "srt": "srt://10.42.0.1:8890?streamid=publish:cam1:k", "rtmp": "rtmp://10.42.0.1/cam1/k",
			"rtmpServer": "rtmp://10.42.0.1/cam1", "rtmpKey": "k",
			"lan": map[string]any{"srt": "srt://192.168.1.20:8890?streamid=publish:cam1:k"}},
		map[string]any{"id": "../x", "srt": "srt://x"}}})
	run(t, d, ui.AppAction{What: "dest_reveal"})
	run(t, d, ui.AppAction{What: "cam_creds"})
	s = d.AppSnapshot()
	if s.DestSecrets == nil || s.DestSecrets.StreamID != "publish:abc" || len(s.CamCreds) != 1 || s.CamCreds[0].LAN == nil ||
		len(s.CamCreds[0].Wifi.SRTQR) < 21 {
		t.Fatalf("%+v %+v", s.DestSecrets, s.CamCreds)
	}
	d.DropSecrets()
	if s := d.AppSnapshot(); s.DestSecrets != nil || s.CamCreds != nil {
		t.Fatal("not dropped")
	}
	// An older app: the routes are not there
	a.drop("GET /destination")
	d.PollDest()
	if s := d.AppSnapshot(); s.DestAPI != ui.No || s.Dest != nil {
		t.Errorf("%+v", s.DestAPI)
	}
}

// GET /status's modems and links (MODEMS.md §4.2): every field validated, what does not fit dropped
func TestModemsRead(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /status", 200, map[string]any{"service": map[string]any{"state": "active"}, "transport": "srtla",
		"uplinks": []any{map[string]any{"name": "wwan0", "ip": "10.64.12.7", "kind": "modem", "modemId": "m-5a1d0c3e9b21",
			"label": "Movistar", "health": "good", "healthCode": "ok"},
			map[string]any{"name": "wwan1", "kind": "modem", "modemId": "../../etc"}},
		"modems": []any{
			map[string]any{"id": "m-5a1d0c3e9b21", "iface": "wwan0", "manufacturer": "Quectel", "model": "RM520N-GL",
				"state": "connected", "sim": "ready", "registration": "home", "operator": "Movistar", "tech": "5g-nsa",
				"signal":   map[string]any{"rsrp": -88.0, "rsrq": -9.0, "sinr": 14.5, "rssi": 900.0},
				"imeiTail": "4412", "iccidTail": "01934", "alias": "", "apn": "", "apnAuto": "telefonica.es", "roaming": false,
				"mode": "auto", "modes": []any{"auto", "4g", "5g", "6g"}, "can": []any{"apn", "pin", "format"}},
			map[string]any{"id": "m-7d3e2a1b9c04", "state": "locked", "sim": "pin", "mode": "ludicrous"},
			map[string]any{"id": "rm -rf /", "state": "connected"}},
		"links": []any{
			map[string]any{"name": "wwan0", "kind": "modem", "health": "good", "code": "ok", "available": true},
			map[string]any{"name": "m-7d3e2a1b9c04", "kind": "modem", "health": "bad", "code": "sim_pin", "available": false},
			map[string]any{"name": "x;y", "kind": "modem", "health": "bad"}}})
	d.PollStatus()
	st := d.AppSnapshot().Status
	if st == nil || len(st.Modems) != 2 || len(st.Uplinks) != 2 {
		t.Fatalf("%+v", st)
	}
	if st.Uplinks[0].ModemID != "m-5a1d0c3e9b21" || st.Uplinks[1].ModemID != "" {
		t.Errorf("modem ids %q %q", st.Uplinks[0].ModemID, st.Uplinks[1].ModemID)
	}
	m := st.Modems[0]
	if m.Iface != "wwan0" || m.Operator != "Movistar" || m.Tech != "5g-nsa" || m.RSRP == nil || *m.RSRP != -88 ||
		m.SINR == nil || *m.SINR != 14.5 || m.RSSI != nil || m.IMEITail != "4412" || m.ICCIDTail != "" ||
		m.APNAuto != "telefonica.es" || !reflect.DeepEqual(m.Modes, []string{"auto", "4g", "5g"}) ||
		!reflect.DeepEqual(m.Can, []string{"apn", "pin"}) || m.Label() != "Movistar" {
		t.Errorf("%+v", m)
	}
	if l := st.Modems[1]; l.SIM != "pin" || l.Mode != "" || l.Label() != "" {
		t.Errorf("%+v", l)
	}
	if len(st.Links) != 1 || st.Links[0].Name != "m-7d3e2a1b9c04" || st.Links[0].Code != "sim_pin" {
		t.Errorf("links %+v", st.Links)
	}
}

// A reveal queued or in flight when the page drops the secrets does not bring them back; a new key drops the
// cameras' URLs (a reveal in flight is the old ones).
func TestRevealAfterDrop(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /destination/secrets", 200, map[string]any{"streamId": "publish:abc", "passphrase": ""})
	a.route("GET /network/credentials?cameras=1", 200, map[string]any{"cameras": []any{
		map[string]any{"id": "cam1", "srt": "srt://10.42.0.1:8890?streamid=publish:cam1:k"}}})
	a.route("PATCH /cameras/cam1", 200, map[string]any{})
	for _, w := range []string{"dest_reveal", "cam_creds"} {
		if !d.Do(ui.AppAction{What: w}) {
			t.Fatal(w)
		}
		q, _, _, _ := d.take()
		d.DropSecrets() // the page went while it was in flight
		d.runAction(*q)
	}
	if s := d.AppSnapshot(); s.DestSecrets != nil || s.CamCreds != nil || !s.Last.OK {
		t.Fatalf("%+v %+v", s.DestSecrets, s.CamCreds)
	}
	run(t, d, ui.AppAction{What: "cam_creds"})
	if len(d.AppSnapshot().CamCreds) != 1 {
		t.Fatal("not revealed")
	}
	d.Do(ui.AppAction{What: "cam_creds"})
	stale, _, _, _ := d.take()
	run(t, d, ui.AppAction{What: "cam_rotate", ID: "cam1"})
	if d.AppSnapshot().CamCreds != nil {
		t.Fatal("the old key stays after a new one")
	}
	d.runAction(*stale)
	if d.AppSnapshot().CamCreds != nil {
		t.Fatal("a reveal from before the new key brought the old one back")
	}
}
