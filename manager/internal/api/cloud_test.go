package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/network"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/srtstats"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

type fakeCloud struct {
	mu      sync.Mutex
	view    cloud.View
	pairErr error
	unlinks int
}

func (f *fakeCloud) View() cloud.View { f.mu.Lock(); defer f.mu.Unlock(); return f.view }
func (f *fakeCloud) Pair(context.Context) (cloud.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pairErr != nil {
		return f.view, f.pairErr
	}
	exp := time.Now().Add(10 * time.Minute)
	f.view = cloud.View{State: "pairing", Code: "ABCD-2346", ExpiresAt: &exp, LinkURL: "https://app.example/encoders/link?pairing=ABCD-2346"}
	return f.view, nil
}
func (f *fakeCloud) Unlink(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlinks++
	f.view = cloud.View{State: "unlinked"}
	return false
}

func TestCloudEndpoints(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	if w := e.do("GET", "/api/v1/cloud", "", tok); w.Code != 503 {
		t.Errorf("no cloud client: %d", w.Code)
	}
	fc := &fakeCloud{view: cloud.View{State: "unlinked"}}
	e.srv.Cloud = fc
	if w := e.do("GET", "/api/v1/cloud", "", ""); w.Code != 401 {
		t.Errorf("no session: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/cloud", "", tok); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"unlinked"`) {
		t.Errorf("GET: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/cloud/pair", `{}`, tok, "Origin", "http://evil.example"); w.Code != 403 {
		t.Errorf("CSRF on pair: %d", w.Code)
	}
	w := e.do("POST", "/api/v1/cloud/pair", `{}`, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"ABCD-2346"`) || !strings.Contains(w.Body.String(), `"linkUrl":"https://app.example/encoders/link?pairing=ABCD-2346"`) {
		t.Errorf("pair: %d %s", w.Code, w.Body)
	}
	for err, code := range map[error]int{cloud.ErrAlreadyLinked: 409, cloud.ErrRateLimited: 429, cloud.ErrUnreachable: 502, cloud.ErrNoIdentity: 500, errors.New("x"): 502} {
		fc.pairErr = err
		if w := e.do("POST", "/api/v1/cloud/pair", `{}`, tok); w.Code != code {
			t.Errorf("pair with %v: %d, want %d", err, w.Code, code)
		}
	}
	w = e.do("DELETE", "/api/v1/cloud", `{}`, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cloudNotified":false`) || fc.unlinks != 1 {
		t.Errorf("DELETE: %d %s", w.Code, w.Body)
	}
}

// Linked (managedBy = eirl): the destination is read-only in the local web; the HDMI input and
// rehearsal mode are not.
func TestManagedDestinationIsReadOnly(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	e.srv.Config.ManagedBy = func() string { return "eirl" }
	if w := e.do("PUT", "/api/v1/destination", `{"latencyMs":300}`, tok); w.Code != 409 {
		t.Errorf("PUT /destination while linked: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/v1/destination", "", tok); !strings.Contains(w.Body.String(), `"managedBy":"eirl"`) {
		t.Errorf("GET /destination: %s", w.Body)
	}
	if w := e.do("PUT", "/api/v1/input", `{"edidPreferred":"1080p50"}`, tok); w.Code != 200 {
		t.Errorf("PUT /input while linked: %d", w.Code)
	}
	if w := e.do("PUT", "/api/v1/output", `{"output":"off"}`, tok); w.Code != 200 {
		t.Errorf("PUT /output while linked: %d", w.Code)
	}
	// Linking does not rewrite the file: no “unapplied changes” just because of linking
	e.do("PUT", "/api/v1/output", `{"output":"on"}`, tok)
	h := e.srv.Config.Hash()
	e.srv.Config.ManagedBy = func() string { return "local" }
	if e.srv.Config.Hash() != h {
		t.Error("the fingerprint does not depend on who manages the destination")
	}
	if w := e.do("PUT", "/api/v1/destination", `{"latencyMs":300}`, tok); w.Code != 200 {
		t.Errorf("unlinked, PUT /destination: %d", w.Code)
	}
}

func cloudWire(t *testing.T, url string) cloud.WireConfig {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"destination": map[string]any{"url": url, "streamId": "publish:" + key, "latencyMs": 500},
		"video":       map[string]any{"bitrateKbps": 5000, "adaptive": false, "minBitrateKbps": 1000},
		"audio":       map[string]any{"bitrateKbps": 128},
		"input":       map[string]any{"edidPreferred": "1080p30"},
		"output":      "on",
		"uplinks":     map[string]any{"disabled": []string{"usb0"}},
	})
	w, fe := cloud.ParseWireConfig(raw)
	if fe != nil {
		t.Fatal(fe)
	}
	return w
}

// Reported carries the real access point policy (“Create my Wi-Fi”, WIFI.md §7) when eirl-netsetup is there;
// without it, it is empty and the cloud client fills it in.
func TestCloudDeviceReportedAPPolicy(t *testing.T) {
	e := setup(t, true)
	d := &CloudDevice{Srv: e.srv}
	if rep, err := d.Reported(); err != nil || rep.AccessPoint != "" {
		t.Fatalf("without eirl-netsetup: %q %v", rep.AccessPoint, err)
	}
	e.srv.Network = &network.Manager{Dir: e.dir, StatePath: filepath.Join(e.dir, "ap-state.json"), HardwareID: "abcdef0123",
		Apply: func(context.Context) error { return nil },
		Write: func(p string, b []byte, m os.FileMode) error { return config.WriteAtomic(p, b, m) }}
	if rep, _ := d.Reported(); rep.AccessPoint != "rescue" {
		t.Errorf("not chosen and no cameras (W4): %q", rep.AccessPoint)
	}
	always := "always"
	if _, err := e.srv.Network.PutWiFi(context.Background(), network.WiFiChange{AP: &always}, nil); err != nil {
		t.Fatal(err)
	}
	if rep, _ := d.Reported(); rep.AccessPoint != "always" {
		t.Errorf("with “Create my Wi-Fi”: %q", rep.AccessPoint)
	}
}

func TestCloudDeviceApplyConfig(t *testing.T) {
	e := setup(t, true)
	e.srv.Config.ManagedBy = func() string { return "eirl" }
	e.srv.Uplinks = &uplinks.Reader{Available: filepath.Join(e.dir, "available"), Disabled: filepath.Join(e.dir, "uplinks-disabled"), SysNet: e.dir}
	d := &CloudDevice{Srv: e.srv}
	ctx := context.Background()

	res, err := d.ApplyConfig(ctx, cloudWire(t, "srt://eu1.relay.enhancedirl.com:5000"), true)
	if err != nil || !res.Restarted || !res.Saved {
		t.Fatalf("valid config: %+v err=%v", res, err)
	}
	b, _ := os.ReadFile(e.srv.Config.AppPath)
	for _, want := range []string{"EIRL_MANAGED_BY=eirl", "SRT_URL=srt://eu1.relay.enhancedirl.com:5000", "SRT_STREAMID=publish:" + key, "SRT_PASSPHRASE=\n", "VBITRATE=5000"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %q in the .env:\n%s", want, b)
		}
	}
	if b, _ := os.ReadFile(e.srv.Uplinks.Disabled); !strings.HasSuffix(string(b), "\nusb0\n") {
		t.Errorf("disabled links: %q", b)
	}
	// The same config again (the cloud resends it on reconnect): does not cut the broadcast
	e.unit.actions = nil
	if res, err := d.ApplyConfig(ctx, cloudWire(t, "srt://eu1.relay.enhancedirl.com:5000"), true); err != nil || res.Restarted || len(e.unit.actions) != 0 {
		t.Errorf("same config: %+v err=%v actions=%v", res, err, e.unit.actions)
	}
	// D9
	before, _ := os.ReadFile(e.srv.Config.AppPath)
	_, err = d.ApplyConfig(ctx, cloudWire(t, "srt://attacker.example.com:5000"), true)
	var fe config.FieldErrors
	if !errors.As(err, &fe) || fe["destination"] != cloud.ErrDestinationNotAllowed {
		t.Errorf("D9: %v", err)
	}
	if after, _ := os.ReadFile(e.srv.Config.AppPath); string(after) != string(before) {
		t.Error("a rejected config must not touch the .env")
	}
	// Deliberately stopped: saved without starting
	e.unit.state, e.unit.actions = "inactive", nil
	if res, _ := d.ApplyConfig(ctx, cloudWire(t, "srt://eu2.relay.enhancedirl.com:5000"), true); res.Restarted || len(e.unit.actions) != 0 {
		t.Errorf("stopped: %+v %v", res, e.unit.actions)
	}
	rep, _ := d.Reported()
	if rep.Destination == nil || rep.Destination.Host != "eu2.relay.enhancedirl.com" || !rep.Destination.StreamIDSet || rep.Uplinks.Disabled[0] != "usb0" {
		t.Errorf("Reported: %+v", rep)
	}
}

func TestCloudDeviceActions(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	d := &CloudDevice{Srv: e.srv}
	ctx := context.Background()
	// The app file already exists (otherwise writing it for the first time changes the fingerprint)
	e.srv.Config.Update(func(cur config.Settings) (config.Settings, error) { return cur, nil })
	// Already streaming with the saved config: start cuts nothing
	if err := d.Action(ctx, "start", ""); err != nil || len(e.unit.actions) != 0 {
		t.Errorf("start without changes: %v %v", err, e.unit.actions)
	}
	if err := d.Action(ctx, "rehearsal", ""); err != nil || strings.Join(e.unit.actions, ",") != "restart" {
		t.Errorf("rehearsal: %v %v", err, e.unit.actions)
	}
	if cfg, _ := e.srv.Config.Load(); cfg.Output != "off" {
		t.Errorf("rehearsal saves OUTPUT=off: %q", cfg.Output)
	}
	e.unit.state, e.unit.actions = "inactive", nil
	if err := d.Action(ctx, "start", ""); err != nil || strings.Join(e.unit.actions, ",") != "start" {
		t.Errorf("start while stopped: %v %v", err, e.unit.actions)
	}
	e.unit.actions = nil
	for _, a := range []string{"stop", "restart"} {
		if err := d.Action(ctx, a, ""); err != nil {
			t.Error(a, err)
		}
	}
	// stop no longer stops eirl-engine (F3, no rehearsal mode): it stops streaming and the preview
	// stays; with the service stopped it starts it in “ready”, like start
	if strings.Join(e.unit.actions, ",") != "start,restart" {
		t.Errorf("stop/restart: %v", e.unit.actions)
	}
	if cfg, _ := e.srv.Config.Load(); cfg.Output != "off" {
		t.Errorf("stop saves OUTPUT=off: %q", cfg.Output)
	}
	var ae *cloud.ActionError
	if err := d.Action(ctx, "reboot", ""); !errors.As(err, &ae) || ae.Code != "unsupported" {
		t.Errorf("reboot without Rebooter: %v", err)
	}
	// Reboot loop brake: right after power-on it is not rebooted from the cloud
	rb := &countRebooter{}
	d.Rebooter = rb
	d.Uptime = func() time.Duration { return 3 * time.Minute }
	if err := d.Action(ctx, "reboot", ""); !errors.As(err, &ae) || ae.Code != "too_soon" || rb.n != 0 {
		t.Errorf("reboot right after power-on: %v (%d)", err, rb.n)
	}
	d.Uptime = func() time.Duration { return 11 * time.Minute }
	if err := d.Action(ctx, "reboot", ""); err != nil || rb.n != 1 {
		t.Errorf("reboot: %v (%d)", err, rb.n)
	}
	d.Rebooter = nil
	if err := d.Action(ctx, "set_local_password", "corta"); !errors.As(err, &ae) || ae.Code != "validation" {
		t.Errorf("short password: %v", err)
	}
	if err := d.Action(ctx, "set_local_password", strings.Repeat("x", 73)); !errors.As(err, &ae) || ae.Code != "validation" {
		t.Errorf("password over 72 bytes: %v", err)
	}
	if err := d.Action(ctx, "set_local_password", "nueva-contraseña-1"); err != nil {
		t.Fatal(err)
	}
	if w := e.do("GET", "/api/v1/status", "", tok); w.Code != 401 {
		t.Errorf("set_local_password closes the sessions: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/login", `{"password":"nueva-contraseña-1"}`, ""); w.Code != 204 {
		t.Errorf("login with the new one: %d", w.Code)
	}
	// Without a destination, start fails with a clear reason
	e.srv.Config.Update(func(cur config.Settings) (config.Settings, error) {
		cur.URL, cur.StreamID, cur.Passphrase, cur.Output = "", "", "", "off"
		return cur, nil
	})
	if err := d.Action(ctx, "start", ""); !errors.As(err, &ae) || ae.Code != "no_destination" {
		t.Errorf("start without destination: %v", err)
	}
}

func TestCloudDeviceUnlinked(t *testing.T) {
	e := setup(t, true)
	d := &CloudDevice{Srv: e.srv}
	ctx := context.Background()
	if err := d.Unlinked(ctx, false); err != nil || len(e.unit.actions) != 0 {
		t.Errorf("local destination: left alone: %v %v", err, e.unit.actions)
	}
	if cfg, _ := e.srv.Config.Load(); cfg.URL == "" {
		t.Error("the local destination is kept")
	}
	if err := d.Unlinked(ctx, true); err != nil {
		t.Fatal(err)
	}
	cfg, _ := e.srv.Config.Load()
	if cfg.URL != "" || cfg.StreamID != "" || cfg.Passphrase != "" || cfg.Output != "off" || strings.Join(e.unit.actions, ",") != "restart" {
		t.Errorf("cloud destination cleared and back to rehearsal: %+v %v", cfg, e.unit.actions)
	}
}

func TestCloudStatusHasNoSecrets(t *testing.T) {
	e := setup(t, true)
	st := e.srv.collect(context.Background())
	st.SRT = &srtstats.Stats{Connected: true, RTTMs: 12, SendMbps: 6.1, Retrans: 2}
	st.Uplinks = []uplinks.Uplink{{Name: "eth0", IP: "192.168.1.232", Kind: "ethernet", InUse: true, TxKbps: 6000}}
	st.Input.EDIDActive = "1080p30"
	cs := CloudStatus(st, time.Now())
	b, _ := json.Marshal(cs)
	for _, secret := range []string{key, "secretpassphrase", "relay.example", "192.168.1.232"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("%q in EncoderStatus: %s", secret, b)
		}
	}
	if cs.Phase != cloud.PhaseStreaming || cs.SRT == nil || cs.SRT.PktRetrans != 2 || len(cs.Uplinks) != 1 || cs.Input.EDIDActive == nil || cs.Audio.State != "playing" {
		t.Errorf("CloudStatus: %+v", cs)
	}
	for _, k := range []string{`"bitrate":null`, `"cpuPct":[]`, `"network":{"accessPoint":{"on":false,"ssid":null,"clients":0},"wifi":null}`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("missing %s in %s", k, b)
		}
	}
}

// The mapping does not filter what other branches add to input (multiple sources), and from signal
// it only sends kind and measuredFps, not the CSI diagnostics.
func TestCloudStatusPassesInputExtras(t *testing.T) {
	// The input fields of USB sources (SOURCES.md) pass through as is
	in := jsonFields(InputView{EDIDPreferred: "1080p30", Source: "usb", Device: "auto", Format: "auto", Mode: "auto", Audio: "auto"},
		func(k string) bool { return k != "edidPreferred" && k != "edidActive" })
	if len(in) != 5 || in["source"] != "usb" || in["device"] != "auto" {
		t.Errorf("input extra: %v", in)
	}
	st := Status{Service: ServiceView{State: "active", Phase: "waiting_device"}}
	st.Signal.PixelClockHz, st.Signal.CSILoad = 148500000, 0.89
	b, _ := json.Marshal(CloudStatus(st, time.Now()))
	if strings.Contains(string(b), "pixelClockHz") || strings.Contains(string(b), "csiLoad") || !strings.Contains(string(b), `"phase":"waiting_device"`) {
		t.Errorf("signal/phase: %s", b)
	}
}

type countRebooter struct{ n int }

func (r *countRebooter) CanReboot(context.Context) error { return nil }
func (r *countRebooter) Reboot(context.Context) error    { r.n++; return nil }
