package api

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

type listBody struct {
	Active       *string                  `json:"active"`
	ManagedBy    string                   `json:"managedBy"`
	Destinations []config.DestinationView `json:"destinations"`
	Templates    []config.Template        `json:"templates"`
	Restarted    bool                     `json:"restarted"`
}

func list(t *testing.T, e *env, tok string) listBody {
	t.Helper()
	w := e.do("GET", "/api/v1/destinations", "", tok)
	var l listBody
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatalf("GET /destinations: %d %s", w.Code, w.Body)
	}
	return l
}

func TestDestinationsCRUD(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	// The migration: the destination in /etc/default becomes a custom one, active
	l := list(t, e, tok)
	if len(l.Destinations) != 1 || l.Active == nil || l.Destinations[0].Template != "custom" || l.Destinations[0].Name != "SRT" ||
		!l.Destinations[0].Active || l.ManagedBy != "local" || len(l.Templates) != 6 {
		t.Fatalf("migrated list: %+v", l)
	}
	migrated := *l.Active
	// Create: 201, masked, not active
	w := e.do("POST", "/api/v1/destinations", `{"template":"twitch","protocol":"rtmps","key":"live_123456789_abcdefghij"}`, tok)
	var v config.DestinationView
	json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != 201 || v.Active || v.URL != "rtmps://live.twitch.tv:443/app" || !v.Key.Set || v.Key.Masked != "live…ghij" ||
		strings.Contains(w.Body.String(), "123456789_abcdefghij") || v.VideoCodec != "h264" || v.Name != "Twitch" {
		t.Fatalf("POST: %d %s", w.Code, w.Body)
	}
	for body, code := range map[string]int{
		`{"protocol":"rtmp"}`:                           400, // no template
		`{"template":"twitch"}`:                         400, // no protocol
		`{"template":"vimeo","protocol":"rtmp"}`:        400,
		`{"template":"twitch","protocol":"srt"}`:        400,
		`{"template":"eirl","protocol":"srtla"}`:        409, // only the cloud creates it
		`{"template":"custom","protocol":"rtmp"}`:       400, // no server
		`{"template":"twitch","protocol":"rtmp","x":1}`: 400,
	} {
		if w := e.do("POST", "/api/v1/destinations", body, tok); w.Code != code {
			t.Errorf("POST %s: %d %s", body, w.Code, w.Body)
		}
	}
	// Update a non-active one: no restart, nothing in engine.env
	before, _ := os.ReadFile(e.srv.Config.AppPath)
	w = e.do("PUT", "/api/v1/destinations/"+v.ID, `{"name":"My Twitch","videoBitrateKbps":5000,"apply":true}`, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"My Twitch"`) || len(e.unit.actions) != 0 {
		t.Errorf("PUT non-active: %d %s %v", w.Code, w.Body, e.unit.actions)
	}
	if after, _ := os.ReadFile(e.srv.Config.AppPath); string(after) != string(before) {
		t.Error("a non-active destination must not change engine.env")
	}
	if w := e.do("PUT", "/api/v1/destinations/"+v.ID, `{"videoBitrateKbps":7000}`, tok); w.Code != 400 || !strings.Contains(w.Body.String(), "6000") {
		t.Errorf("twitch over its maximum: %d %s", w.Code, w.Body)
	}
	if w := e.do("PUT", "/api/v1/destinations/d-00000000", `{"name":"x"}`, tok); w.Code != 404 || !strings.Contains(w.Body.String(), `"not_found"`) {
		t.Errorf("PUT unknown: %d %s", w.Code, w.Body)
	}
	// Activate with apply: engine.env with it and a restart (no mixer)
	w = e.do("POST", "/api/v1/destinations/"+v.ID+"/activate", `{"apply":true}`, tok)
	var l2 listBody
	json.Unmarshal(w.Body.Bytes(), &l2)
	if w.Code != 200 || l2.Active == nil || *l2.Active != v.ID || !l2.Restarted || strings.Join(e.unit.actions, ",") != "restart" {
		t.Errorf("activate: %d %s %v", w.Code, w.Body, e.unit.actions)
	}
	env, _ := config.ReadEnv(e.srv.Config.AppPath)
	if env["OUTPUT_PROTOCOL"] != "rtmps" || env["OUTPUT_URL"] != "rtmps://live.twitch.tv:443/app" || env["SRT_URL"] != "" ||
		env["OUTPUT_KEY"] != "live_123456789_abcdefghij" || env["VBITRATE"] != "5000" {
		t.Errorf("engine.env after activating: %v", env)
	}
	if w := e.do("POST", "/api/v1/destinations/d-00000000/activate", `{"apply":true}`, tok); w.Code != 404 {
		t.Errorf("activate unknown: %d", w.Code)
	}
	// The legacy route works on the active one, with the new fields
	w = e.do("GET", "/api/v1/destination", "", tok)
	if !strings.Contains(w.Body.String(), `"protocol":"rtmps"`) || !strings.Contains(w.Body.String(), `"transport":"rtmps"`) ||
		!strings.Contains(w.Body.String(), `"id":"`+v.ID+`"`) || strings.Contains(w.Body.String(), "123456789_abcdefghij") {
		t.Errorf("GET /destination: %s", w.Body)
	}
	if w := e.do("PUT", "/api/v1/destination", `{"outputSize":"1280x720","outputFps":"half"}`, tok); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"outputSize":"1280x720"`) {
		t.Errorf("PUT /destination: %d %s", w.Code, w.Body)
	}
	// Delete: the active one and an unknown one cannot; another one can
	if w := e.do("DELETE", "/api/v1/destinations/"+v.ID, "{}", tok); w.Code != 409 || !strings.Contains(w.Body.String(), `"error":"active"`) {
		t.Errorf("DELETE active: %d %s", w.Code, w.Body)
	}
	if w := e.do("DELETE", "/api/v1/destinations/d-00000000", "{}", tok); w.Code != 404 {
		t.Errorf("DELETE unknown: %d", w.Code)
	}
	if w := e.do("DELETE", "/api/v1/destinations/"+migrated, "{}", tok); w.Code != 204 {
		t.Errorf("DELETE: %d %s", w.Code, w.Body)
	}
	if l := list(t, e, tok); len(l.Destinations) != 1 {
		t.Errorf("after delete: %+v", l)
	}
	// CSRF and session as every mutation
	if w := e.do("POST", "/api/v1/destinations", `{"template":"twitch","protocol":"rtmp"}`, tok, "Origin", "http://evil.example"); w.Code != 403 {
		t.Errorf("CSRF: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/destinations", "", ""); w.Code != 401 {
		t.Errorf("no session: %d", w.Code)
	}
	// Secrets only over the socket
	if w := e.do("GET", "/api/v1/destinations/"+v.ID+"/secrets", "", tok); w.Code != 404 || strings.Contains(w.Body.String(), "abcdefghij") {
		t.Errorf("secrets on the web: %d %s", w.Code, w.Body)
	}
}

func TestDestinationsLimitAndManaged(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	for i := 1; i < config.MaxLocalDestinations; i++ { // the migrated one is the first
		if w := e.do("POST", "/api/v1/destinations", `{"template":"custom","protocol":"udp","url":"udp://239.0.0.1:1234"}`, tok); w.Code != 201 {
			t.Fatalf("POST %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := e.do("POST", "/api/v1/destinations", `{"template":"custom","protocol":"udp","url":"udp://239.0.0.1:1234"}`, tok); w.Code != 409 ||
		!strings.Contains(w.Body.String(), `"error":"too_many"`) {
		t.Errorf("over the limit: %d %s", w.Code, w.Body)
	}
	// The cloud's: can be activated, not edited nor deleted, and nothing turns into it
	e.setManaged(t, true)
	if w := e.do("DELETE", "/api/v1/destinations/eirl", "{}", tok); w.Code != 409 || !strings.Contains(w.Body.String(), `"error":"managed"`) {
		t.Errorf("DELETE eirl: %d %s", w.Code, w.Body)
	}
	l := list(t, e, tok)
	other := l.Destinations[0].ID
	if w := e.do("PUT", "/api/v1/destinations/"+other, `{"template":"eirl"}`, tok); w.Code != 400 {
		t.Errorf("to template eirl: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/destinations/eirl/activate", `{"apply":false}`, tok); w.Code != 200 || !strings.Contains(w.Body.String(), `"managedBy":"eirl"`) {
		t.Errorf("activate eirl: %d %s", w.Code, w.Body)
	}
}

// The local socket: the new routes, and the secrets in clear only there
func TestDestinationsLocalSocket(t *testing.T) {
	e := setup(t, true)
	do := localClient(t, e.srv)
	if code, body := do("POST", "/api/v1/destinations", `{"template":"custom","protocol":"rist","url":"rist://10.0.0.2:5000","passphrase":"abcdefghijkl","username":"u","password":"pw.1"}`); code != 201 ||
		strings.Contains(body, "abcdefghijkl") || strings.Contains(body, "pw.1") {
		t.Fatalf("POST over the socket: %d %s", code, body)
	}
	code, body := do("GET", "/api/v1/destinations", "")
	var l listBody
	json.Unmarshal([]byte(body), &l)
	if code != 200 || len(l.Destinations) != 2 {
		t.Fatalf("GET over the socket: %d %s", code, body)
	}
	id := l.Destinations[1].ID
	w := httptest.NewRecorder()
	e.srv.LocalHandler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/destinations/"+id+"/secrets", nil))
	var sec config.Secrets
	json.Unmarshal(w.Body.Bytes(), &sec)
	if w.Code != 200 || sec.Passphrase != "abcdefghijkl" || sec.Password != "pw.1" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("secrets over the socket: %d %s", w.Code, w.Body)
	}
	for _, r := range []struct{ m, p, b string }{{"PUT", "/api/v1/destinations/" + id, `{"name":"LAN"}`},
		{"POST", "/api/v1/destinations/" + id + "/activate", `{"apply":false}`}, {"DELETE", "/api/v1/destinations/" + l.Destinations[0].ID, `{}`}} {
		if code, body := do(r.m, r.p, r.b); code >= 400 {
			t.Errorf("%s %s over the socket: %d %s", r.m, r.p, code, body)
		}
	}
}

// fakeOutputEngine: a mixer socket that answers output_config with reply ("" = ok) and records the commands.
func fakeOutputEngine(t *testing.T, e *env, reply string) *[]string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mxo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	cmds := &[]string{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				var req map[string]any
				json.Unmarshal(sc.Bytes(), &req)
				mu.Lock()
				*cmds = append(*cmds, req["cmd"].(string))
				mu.Unlock()
				if req["cmd"] == "output_config" && reply != "" {
					c.Write([]byte(`{"id":1,"ok":false,"error":"` + reply + `"}` + "\n"))
				} else {
					c.Write([]byte(`{"id":1,"ok":true}` + "\n"))
				}
			}
			c.Close()
		}
	}()
	e.srv.Mixer = &mixer.Client{Sock: filepath.Join(dir, "s"), StatePath: filepath.Join(e.dir, "mixer.json"), RunDir: e.dir}
	// Running with the mixer and nothing pending
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=on\nSOURCE=hdmi\nMIXER=1\nTRANSPORT=srt\nCONF_SHA="+e.srv.Config.Hashes().NoOutput+"\n"), 0o644)
	return cmds
}

// The apply rule (OUTPUTS.md §9.4): hot with output_config when only output keys changed; the restart of today when
// the engine cannot (an older one, or something else changed) or the mixer is not running.
func TestApplyOutputConfig(t *testing.T) {
	for _, c := range []struct {
		name, reply, body string
		wantCmd           bool
		restarted         bool
	}{
		{"hot", "", `{"videoCodec":"h265","apply":true}`, true, false},
		{"older engine", "unknown_command", `{"videoCodec":"h265","apply":true}`, true, true},
		{"restart_required", "restart_required", `{"latencyMs":900,"apply":true}`, true, true},
		{"nothing changed", "", `{"latencyMs":2000,"apply":true}`, false, false},
		{"without apply", "", `{"videoCodec":"h265"}`, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t, true)
			tok := e.login(t)
			// engine.env already rendered from the saved destinations (the first save after migrating writes the new keys)
			e.srv.Config.UpdateDestinations(func(*config.Destinations, *config.Settings) error { return nil })
			cmds := fakeOutputEngine(t, e, c.reply)
			w := e.do("PUT", "/api/v1/destination", c.body, tok)
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			sent := strings.Contains(strings.Join(*cmds, ","), "output_config")
			restarted := strings.Contains(w.Body.String(), `"restarted":true`)
			if sent != c.wantCmd || restarted != c.restarted || (len(e.unit.actions) > 0) != c.restarted {
				t.Errorf("output_config sent %v, restarted %v (actions %v): %s", sent, restarted, e.unit.actions, w.Body)
			}
		})
	}
	// Something besides the output changed (the input): straight to the restart, without asking the engine
	e := setup(t, true)
	tok := e.login(t)
	e.srv.Config.Update(func(s config.Settings) (config.Settings, error) { return s, nil })
	cmds := fakeOutputEngine(t, e, "")
	if w := e.do("PUT", "/api/v1/input", `{"edidPreferred":"1080p50","apply":true}`, tok); w.Code != 200 ||
		strings.Contains(strings.Join(*cmds, ","), "output_config") || strings.Join(e.unit.actions, ",") != "restart" {
		t.Errorf("input change: %d %v %v", w.Code, *cmds, e.unit.actions)
	}
	// Go live stays the output command, not output_config
	e.unit.actions, *cmds = nil, nil
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=off\nSOURCE=hdmi\nMIXER=1\nCONF_SHA="+e.srv.Config.Hashes().NoOutput+"\n"), 0o644)
	if w := e.do("PUT", "/api/v1/output", `{"output":"off","apply":true}`, tok); w.Code != 200 || strings.Join(*cmds, ",") != "output" ||
		len(e.unit.actions) != 0 {
		t.Errorf("output off: %d %v %v", w.Code, *cmds, e.unit.actions)
	}
}

// A destination switch taken hot never starts or stops the stream: on air from the screen while the saved OUTPUT
// still says off (the screen's go-live not saved yet), it stays on air.
func TestOutputConfigLeavesTheStreamAlone(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	e.srv.Config.UpdateDestinations(func(_ *config.Destinations, s *config.Settings) error { s.Output = "off"; return nil })
	cmds := fakeOutputEngine(t, e, "")
	os.WriteFile(filepath.Join(e.dir, "mixer.json"), []byte(`{"v":1,"output":{"mode":"on","state":"on"}}`), 0o644)
	if w := e.do("PUT", "/api/v1/destination", `{"videoCodec":"h265","apply":true}`, tok); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := strings.Join(*cmds, ","); got != "output_config" {
		t.Errorf("commands %q: only output_config", got)
	}
}

// /status: protocol and codec, transport carrying the protocol (from the run state, or the engine's output).
func TestStatusProtocolAndCodec(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	get := func() map[string]any {
		w := e.do("GET", "/api/v1/status", "", tok)
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	if m := get(); m["protocol"] != "srt" || m["transport"] != "srt" || m["codec"] != "h264" {
		t.Errorf("saved: %v %v %v", m["protocol"], m["transport"], m["codec"])
	}
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=on\nSOURCE=hdmi\nMIXER=1\nTRANSPORT=rtmps\n"), 0o644)
	if m := get(); m["protocol"] != "rtmps" || m["transport"] != "rtmps" {
		t.Errorf("run state: %v %v", m["protocol"], m["transport"])
	}
	e.srv.Mixer = &mixer.Client{Sock: filepath.Join(e.dir, "none"), StatePath: filepath.Join(e.dir, "mixer.json"), RunDir: e.dir}
	os.WriteFile(filepath.Join(e.dir, "mixer.json"), []byte(`{"v":1,"output":{"mode":"on","state":"error","reason":"tls_failed","protocol":"rist","codec":"h265","size":"960x540","fps":25}}`), 0o644)
	m := get()
	out, _ := m["outputState"].(map[string]any)
	if m["protocol"] != "rist" || m["codec"] != "h265" || out["reason"] != "tls_failed" {
		t.Errorf("engine's output: %v %v %v", m["protocol"], m["codec"], out)
	}
}
