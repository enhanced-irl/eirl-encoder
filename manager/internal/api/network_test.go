package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/network"
)

func TestNetworkAPI(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	origin := []string{"Origin", "http://example.com", "Host", "example.com"}
	// Without eirl-netsetup: 503 and /status without network
	if w := e.do("GET", "/api/v1/network", "", tok); w.Code != 503 {
		t.Fatalf("no network: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/status", "", tok); strings.Contains(w.Body.String(), `"network"`) {
		t.Fatal("status with network without eirl-netsetup")
	}
	applies := 0
	e.srv.Network = &network.Manager{Dir: e.dir, StatePath: filepath.Join(e.dir, "ap-state.json"), HardwareID: "abcdef0123",
		Apply:   func(context.Context) error { applies++; return nil },
		Write:   func(p string, b []byte, m os.FileMode) error { return config.WriteAtomic(p, b, m) },
		LANAddr: func() string { return "192.168.1.232" }}

	if w := e.do("POST", "/api/v1/cameras", `{"name":"Móvil"}`, "", origin...); w.Code != 401 {
		t.Errorf("no session: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/cameras", `{"name":"Móvil"}`, tok, "Origin", "http://evil.example"); w.Code != 403 {
		t.Errorf("CSRF: %d", w.Code)
	}
	for _, b := range []string{`{}`, `{"name":"<script>"}`, `{"name":"Móvil","key":"mía"}`} {
		if w := e.do("POST", "/api/v1/cameras", b, tok, origin...); w.Code != 400 {
			t.Errorf("%s: %d", b, w.Code)
		}
	}
	w := e.do("POST", "/api/v1/cameras", `{"name":"Móvil de Ana"}`, tok, origin...)
	if w.Code != 201 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var cr network.Created
	json.Unmarshal(w.Body.Bytes(), &cr)
	key := strings.TrimPrefix(cr.Connect.RTMPKey, "cam1?user=cam1&pass=")
	if len(key) != netcfg.KeyLen || applies != 1 {
		t.Fatalf("create: %+v (%d)", cr, applies)
	}
	if w := e.do("POST", "/api/v1/cameras", `{"name":"Otra"}`, tok, origin...); w.Code != 409 {
		t.Errorf("limit: %d", w.Code)
	}
	// Neither /network nor /status carry the key or the Wi-Fi password
	creds := e.do("GET", "/api/v1/network/credentials", "", tok)
	var c network.Credentials
	json.Unmarshal(creds.Body.Bytes(), &c)
	if c.WiFi == nil || creds.Header().Get("Cache-Control") != "no-store" || c.Cameras[0].SRT != cr.Connect.SRT {
		t.Fatalf("credentials: %s", creds.Body)
	}
	for _, p := range []string{"/api/v1/network", "/api/v1/status"} {
		body := e.do("GET", p, "", tok).Body.String()
		if strings.Contains(body, key) || strings.Contains(body, c.WiFi.Password) {
			t.Errorf("%s leaks a secret: %s", p, body)
		}
		if !strings.Contains(body, `"id":"cam1"`) {
			t.Errorf("%s without the camera: %s", p, body)
		}
	}
	for _, p := range []string{"/api/v1/network/qr/wifi.svg", "/api/v1/cameras/cam1/qr.svg", "/api/v1/cameras/cam1/qr.svg?proto=rtmp"} {
		if w := e.do("GET", p, "", tok); w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(w.Body.String(), "<svg") {
			t.Errorf("%s: %d %s", p, w.Code, w.Header())
		}
	}
	if w := e.do("GET", "/api/v1/cameras/cam9/qr.svg", "", tok); w.Code != 404 {
		t.Errorf("QR of an unknown camera: %d", w.Code)
	}
	if w := e.do("PATCH", "/api/v1/cameras/cam1", `{"rotateKey":true}`, tok, origin...); w.Code != 200 || strings.Contains(w.Body.String(), key) {
		t.Errorf("new key: %d", w.Code)
	}
	if w := e.do("PUT", "/api/v1/network/ap", `{"ssid":"a;b"}`, tok, origin...); w.Code != 400 {
		t.Errorf("invalid SSID: %d", w.Code)
	}
	if w := e.do("PUT", "/api/v1/network/ap", `{"channel":"6"}`, tok, origin...); w.Code != 200 {
		t.Errorf("channel: %d %s", w.Code, w.Body)
	}
	// The name: the generated one in accessPoint.name (also with the AP off), and a new one, there and in the credentials
	if body := e.do("GET", "/api/v1/network", "", tok).Body.String(); !strings.Contains(body, `"name":"EnhancedIRL-0123"`) {
		t.Errorf("generated name: %s", body)
	}
	for _, b := range []string{`{"ssid":""}`, `{"ssid":" Studio"}`, `{"ssid":"Estudio ñ"}`, `{"ssid":"123456789012345678901234567890123"}`} {
		if w := e.do("PUT", "/api/v1/network/ap", b, tok, origin...); w.Code != 400 || !strings.Contains(w.Body.String(), `"ssid"`) {
			t.Errorf("invalid name %s: %d %s", b, w.Code, w.Body)
		}
	}
	if w := e.do("PUT", "/api/v1/network/ap", `{"ssid":"Studio A"}`, tok, origin...); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Studio A"`) {
		t.Errorf("rename: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/v1/network/credentials", "", tok); !strings.Contains(w.Body.String(), `"ssid":"Studio A"`) {
		t.Errorf("credentials after the rename: %s", w.Body)
	}
	// Cameras over cable (§6.11)
	for _, b := range []string{`{}`, `{"enabled":"sí"}`, `{"enabled":true,"iface":"usb0"}`} {
		if w := e.do("PUT", "/api/v1/network/lan", b, tok, origin...); w.Code != 400 {
			t.Errorf("cable %s: %d", b, w.Code)
		}
	}
	if w := e.do("PUT", "/api/v1/network/lan", `{"enabled":true}`, "", origin...); w.Code != 401 {
		t.Errorf("cable without session: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/cameras/cam1/qr.svg?via=lan", "", tok); w.Code != 404 {
		t.Errorf("cable QR with cable off: %d", w.Code)
	}
	if w := e.do("PUT", "/api/v1/network/lan", `{"enabled":true}`, tok, origin...); w.Code != 200 || !strings.Contains(w.Body.String(), `"lan":{"enabled":true`) {
		t.Errorf("turn cable on: %d %s", w.Code, w.Body)
	}
	creds = e.do("GET", "/api/v1/network/credentials", "", tok)
	if !strings.Contains(creds.Body.String(), `"lan":{"srt":"srt://192.168.1.232:8890?streamid=publish:cam1:cam1:`) {
		t.Errorf("cable credentials: %s", creds.Body)
	}
	for _, p := range []string{"/api/v1/cameras/cam1/qr.svg?via=lan", "/api/v1/cameras/cam1/qr.svg?via=lan&proto=rtmp"} {
		if w := e.do("GET", p, "", tok); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "<svg") {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if w := e.do("GET", "/api/v1/cameras/cam1/qr.svg?via=usb0", "", tok); w.Code != 400 {
		t.Errorf("unknown via: %d", w.Code)
	}
	if w := e.do("PUT", "/api/v1/network/ap", `{"enabled":false}`, tok, origin...); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Errorf("turn Wi-Fi off: %d %s", w.Code, w.Body)
	}
	if w := e.do("PUT", "/api/v1/network/lan", `{"enabled":false}`, tok, origin...); w.Code != 409 || !strings.Contains(w.Body.String(), "no_input") {
		t.Errorf("turn both off: %d %s", w.Code, w.Body)
	}
	body := e.do("GET", "/api/v1/network", "", tok).Body.String()
	if !strings.Contains(body, `"lan":{"enabled":true,"state":"off","iface":"eth0","ip":"192.168.1.232"}`) || strings.Contains(body, key) {
		t.Errorf("/network with cable: %s", body)
	}
	if w := e.do("PUT", "/api/v1/network/ap", `{"enabled":true}`, tok, origin...); w.Code != 200 {
		t.Errorf("turn Wi-Fi on: %d", w.Code)
	}
	// Main switch (§6.12)
	for _, b := range []string{`{}`, `{"enabled":"no"}`, `{"enabled":false,"ap":false}`} {
		if w := e.do("PUT", "/api/v1/network", b, tok, origin...); w.Code != 400 {
			t.Errorf("switch %s: %d", b, w.Code)
		}
	}
	if w := e.do("PUT", "/api/v1/network", `{"enabled":false}`, "", origin...); w.Code != 401 {
		t.Errorf("switch without session: %d", w.Code)
	}
	// Streaming with the camera on air, it does not turn off: the program would go black
	runDir := t.TempDir()
	e.srv.Mixer = &mixer.Client{StatePath: filepath.Join(runDir, "mixer.json")}
	mixerState := func(js string) { os.WriteFile(filepath.Join(runDir, "mixer.json"), []byte(js), 0o644) }
	mixerState(`{"v":1,"program":{"main":"net:cam1","layout":"full"},"sources":[{"id":"net:cam1","name":"Móvil de Ana"}],"output":{"mode":"on","state":"on"}}`)
	if w := e.do("PUT", "/api/v1/network", `{"enabled":false}`, tok, origin...); w.Code != 409 || !strings.Contains(w.Body.String(), "on_air") || !strings.Contains(w.Body.String(), "Móvil de Ana") {
		t.Errorf("on air: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(e.do("GET", "/api/v1/network", "", tok).Body.String(), `{"enabled":true`) {
		t.Error("a 409 turned it off anyway")
	}
	mixerState(`{"v":1,"program":{"main":"hdmi","secondary":"net:cam1","layout":"pip"},"output":{"mode":"on","state":"connecting"}}`)
	if w := e.do("PUT", "/api/v1/network", `{"enabled":false}`, tok, origin...); w.Code != 409 {
		t.Errorf("in PiP: %d", w.Code)
	}
	mixerState(`{"v":1,"program":{"main":"hdmi","layout":"full"},"audio":{"main":"net:cam1","mode":"single","sources":[]},"output":{"mode":"on","state":"on"}}`)
	if w := e.do("PUT", "/api/v1/network", `{"enabled":false}`, tok, origin...); w.Code != 409 {
		t.Errorf("its audio playing: %d", w.Code)
	}
	// The secondary hidden (full screen), or not streaming: it turns off
	mixerState(`{"v":1,"program":{"main":"hdmi","secondary":"net:cam1","layout":"full"},"output":{"mode":"on","state":"on"}}`)
	w = e.do("PUT", "/api/v1/network", `{"enabled":false}`, tok, origin...)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `{"enabled":false`) || strings.Contains(w.Body.String(), key) {
		t.Errorf("turn off: %d %s", w.Code, w.Body)
	}
	if body := e.do("GET", "/api/v1/status", "", tok).Body.String(); !strings.Contains(body, `"network":{"enabled":false`) {
		t.Errorf("/status without the switch: %s", body)
	}
	if w := e.do("POST", "/api/v1/cameras", `{"name":"Otra"}`, tok, origin...); w.Code != 409 || !strings.Contains(w.Body.String(), `"disabled"`) {
		t.Errorf("create with everything off: %d %s", w.Code, w.Body)
	}
	mixerState(`{"v":1,"program":{"main":"net:cam1","layout":"full"},"output":{"mode":"off","state":"off"}}`)
	// With reception off, Wi-Fi and cable can both be turned off; turning it on like that cannot (no_input)
	if w := e.do("PUT", "/api/v1/network/lan", `{"enabled":false}`, tok, origin...); w.Code != 200 {
		t.Errorf("turn cable off with reception off: %d %s", w.Code, w.Body)
	}
	if w := e.do("PUT", "/api/v1/network/ap", `{"enabled":false}`, tok, origin...); w.Code != 200 {
		t.Errorf("turn Wi-Fi off with reception off: %d %s", w.Code, w.Body)
	}
	if w := e.do("PUT", "/api/v1/network", `{"enabled":true}`, tok, origin...); w.Code != 409 || !strings.Contains(w.Body.String(), "no_input") {
		t.Errorf("turn on without Wi-Fi or cable: %d %s", w.Code, w.Body)
	}
	e.do("PUT", "/api/v1/network/ap", `{"enabled":true}`, tok, origin...)
	if w := e.do("PUT", "/api/v1/network", `{"enabled":true}`, tok, origin...); w.Code != 200 || !strings.Contains(w.Body.String(), `{"enabled":true`) {
		t.Errorf("turn on: %d %s", w.Code, w.Body)
	}
	// Without a readable mixer state it is unknown what is on air: it does not turn off
	mixerState(`{"v":1,"program":`)
	if w := e.do("PUT", "/api/v1/network", `{"enabled":false}`, tok, origin...); w.Code != 503 || !strings.Contains(w.Body.String(), "mixer_unreadable") {
		t.Errorf("unreadable mixer: %d %s", w.Code, w.Body)
	}
	// With the engine stopped (no mixer.json) nothing is streamed: it turns off
	os.Remove(filepath.Join(runDir, "mixer.json"))
	if w := e.do("PUT", "/api/v1/network", `{"enabled":false}`, tok, origin...); w.Code != 200 {
		t.Errorf("not streaming: %d", w.Code)
	}
	e.do("PUT", "/api/v1/network", `{"enabled":true}`, tok, origin...)
	if w := e.do("DELETE", "/api/v1/cameras/cam1", "", tok, origin...); w.Code != 204 {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if w := e.do("DELETE", "/api/v1/cameras/cam1", "", tok, origin...); w.Code != 404 {
		t.Errorf("repeated delete: %d", w.Code)
	}
}
