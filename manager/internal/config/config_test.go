package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

const key = "AbCdEfGh0123456789_-AbCdEfGh0123" // real shape: base64url of 24 bytes

func valid() Settings {
	s := Defaults()
	s.URL, s.StreamID = "srt://eu1.relay.example.com:8890", "publish:"+key
	return s
}

func TestValidateRejectsInjection(t *testing.T) {
	cases := map[string]func(*Settings){
		"streamid with &":         func(s *Settings) { s.StreamID = "publish:x&mode=listener" },
		"streamid with space":     func(s *Settings) { s.StreamID = "publish:a b" },
		"streamid with $()":       func(s *Settings) { s.StreamID = "publish:$(id)" },
		"streamid with quote":     func(s *Settings) { s.StreamID = `publish:a'b` },
		"streamid with #!::":      func(s *Settings) { s.StreamID = "#!::r=x,m=publish" },
		"passphrase with &":       func(s *Settings) { s.Passphrase = "abcdefghij&x=1" },
		"short passphrase":        func(s *Settings) { s.Passphrase = "short" },
		"passphrase with newline": func(s *Settings) { s.Passphrase = "abcdefghij\nVDEV=x" },
		"url with query":          func(s *Settings) { s.URL = "srt://h:1?latency=1" },
		"url with ;":              func(s *Settings) { s.URL = "srt://h;id:1" },
		"url without port":        func(s *Settings) { s.URL = "srt://host" },
		"url port 0":              func(s *Settings) { s.URL = "srt://host:0" },
		"url other scheme":        func(s *Settings) { s.URL = "udp://host:1" },
		"listener with host":      func(s *Settings) { s.Mode = "listener" },
		"odd mode":                func(s *Settings) { s.Mode = "rendezvous" },
		"latency":                 func(s *Settings) { s.LatencyMs = 5 },
		"vbitrate":                func(s *Settings) { s.VideoKbps = 100000 },
		"abitrate":                func(s *Settings) { s.AudioKbps = 129 },
		"output":                  func(s *Settings) { s.Output = "maybe" },
		"on without url":          func(s *Settings) { s.URL = "" },
		"unknown edid":            func(s *Settings) { s.EDIDPreferred = "1080p60" },
		"edid with path":          func(s *Settings) { s.EDIDPreferred = "../../etc/x" },
		"empty edid":              func(s *Settings) { s.EDIDPreferred = "" },
	}
	for name, mut := range cases {
		s := valid()
		mut(&s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("the valid one fails: %v", err)
	}
	off := Defaults()
	off.Output = "off"
	if err := off.Validate(); err != nil {
		t.Errorf("rehearsal mode without URL should be valid: %v", err)
	}
}

// What the app writes must pass the engine's own validation (internal/engine/config).
func TestRenderPassesScriptRegexes(t *testing.T) {
	script := map[string]*regexp.Regexp{
		"SRT_URL":        regexp.MustCompile(`^srt://(\[[0-9A-Fa-f:.]{2,45}\]|[A-Za-z0-9.-]{1,253})?:[0-9]{1,5}$|^srtla://[A-Za-z0-9.-]{1,253}:[0-9]{1,5}$`),
		"SRT_STREAMID":   regexp.MustCompile(`^([A-Za-z0-9_:./-]{1,512})?$`),
		"SRT_PASSPHRASE": regexp.MustCompile(`^([A-Za-z0-9_.~-]{10,79})?$`),
		"SRT_MODE":       regexp.MustCompile(`^(caller|listener)$`),
		"OUTPUT":         regexp.MustCompile(`^(on|off)$`),
		"VBITRATE":       regexp.MustCompile(`^[0-9]{1,6}$`),
		"ABITRATE":       regexp.MustCompile(`^[0-9]{1,6}$`),
		"SRT_LATENCY":    regexp.MustCompile(`^[0-9]{1,6}$`),
		"ABR":            regexp.MustCompile(`^[01]$`),
		"VBITRATE_MIN":   regexp.MustCompile(`^[0-9]{1,6}$`),
		"EDID_PREFERRED": regexp.MustCompile(`^(1080p30|1080p50|720p60)$`),
	}
	for _, raw := range []string{"srt://eu1.relay.example.com:8890", "srt://[2001:db8::1]:9000", "srt://10.0.0.1:1", "srtla://eu1.relay.example.com:5001"} {
		s := valid()
		p, err := ParseURL(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		s.URL, s.Passphrase = p.URL, "abc.def_ghi~jk-l"
		dir := t.TempDir()
		path := filepath.Join(dir, "engine.env")
		if err := Save(path, s); err != nil {
			t.Fatal(err)
		}
		env, err := ReadEnv(path)
		if err != nil {
			t.Fatal(err)
		}
		for k, re := range script {
			if !re.MatchString(env[k]) {
				t.Errorf("%s=%q does not pass the script's validation", k, env[k])
			}
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
			t.Errorf("permissions %v, want 0640", fi.Mode().Perm())
		}
	}
}

func TestParseURLFromPanel(t *testing.T) {
	p, err := ParseURL("srt://eu1.relay.enhancedirl.com:5000?streamid=publish:" + key)
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "srt://eu1.relay.enhancedirl.com:5000" || p.StreamID != "publish:"+key {
		t.Errorf("parsed wrong: %+v", p)
	}
	for raw, want := range map[string]string{
		"srtla://h:5002?mode=listener":       "caller",
		"srtla://:5002":                      "host del relay",
		"srtla://[2001:db8::1]:5001":         "IPv4",
		"srt://h:5000?streamid=read:" + key:  "lectura",
		"srt://h:5000?streamid=a&streamid=b": "repetido",
		"srt://h:5000?foo=1":                 "no admitido",
		"srt://user:pw@h:5000":               "solo admite",
		"srt://h:5000/path":                  "solo admite",
		"http://h:5000":                      "srt://",
		"srt://h:99999":                      "puerto",
	} {
		// The Spanish catalog text (the API renders it in the request language)
		if _, err := ParseURL(raw); err == nil || !strings.Contains(i18n.Render(i18n.ES, err), want) {
			t.Errorf("%s: error %v, want it to mention %q", raw, err, want)
		}
	}
	// ffmpeg latency (µs) → ms
	if p, _ := ParseURL("srt://h:1?latency=600000"); p.LatencyMs != 600 {
		t.Errorf("latency µs → %d ms", p.LatencyMs)
	}
	if p, _ := ParseURL("srt://h:1?latency=300"); p.LatencyMs != 300 {
		t.Errorf("latency ms → %d", p.LatencyMs)
	}
}

func TestParseURLSRTLA(t *testing.T) {
	p, err := ParseURL("srtla://eu1.relay.enhancedirl.com:5001?streamid=publish:" + key)
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "srtla://eu1.relay.enhancedirl.com:5001" || p.StreamID != "publish:"+key || Transport(p.URL) != "srtla" {
		t.Errorf("parsed wrong: %+v", p)
	}
	if u, ok := SafeURL(p.URL); !ok || u != p.URL {
		t.Errorf("SafeURL(%q) = %q, %v", p.URL, u, ok)
	}
}

func patch(t *testing.T, cur Settings, body string) (Settings, error) {
	t.Helper()
	var p Patch
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	return p.ApplyTo(cur)
}

func TestPatchSRTLA(t *testing.T) {
	cur := valid() // srt to the same relay, latency 200
	cur.URL, cur.LatencyMs = "srt://eu1.relay.example.com:5000", 200
	// The form sends the previous latency: switching to SRTLA raises it to 2000
	s, err := patch(t, cur, `{"url": "srtla://eu1.relay.example.com:5001", "latencyMs": 200}`)
	if err != nil || s.LatencyMs != SRTLALatencyMs || s.Mode != "caller" || s.Public().Transport != "srtla" {
		t.Fatalf("switch to SRTLA: %+v %v", s, err)
	}
	// Same host: no need to type the key again
	if s.StreamID != cur.StreamID {
		t.Errorf("the key should be kept: %q", s.StreamID)
	}
	// A latency set on purpose in the same request wins
	if s, _ := patch(t, cur, `{"url": "srtla://eu1.relay.example.com:5001", "latencyMs": 800}`); s.LatencyMs != 800 {
		t.Errorf("explicit latency: %d", s.LatencyMs)
	}
	// Already on SRTLA, lowering the latency is respected
	if s2, _ := patch(t, s, `{"url": "srtla://eu1.relay.example.com:5001", "latencyMs": 1200}`); s2.LatencyMs != 1200 {
		t.Errorf("latency on SRTLA: %d", s2.LatencyMs)
	}
	if _, err := patch(t, s, `{"mode": "listener"}`); err == nil {
		t.Error("SRTLA in listener should fail")
	}
}

func TestPatchABR(t *testing.T) {
	cur := valid()
	s, err := patch(t, cur, `{"adaptiveBitrate": true, "videoMinBitrateKbps": 1500}`)
	if err != nil || !s.ABR || s.VideoMinKbps != 1500 {
		t.Fatalf("%+v %v", s, err)
	}
	env := map[string]string{}
	for _, l := range strings.Split(string(s.Render()), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			env[k] = v
		}
	}
	if env["ABR"] != "1" || env["VBITRATE_MIN"] != "1500" {
		t.Errorf("render: %v", env)
	}
	var back = Defaults()
	apply(&back, env)
	if !back.ABR || back.VideoMinKbps != 1500 {
		t.Errorf("round trip: %+v", back)
	}
	if _, err := patch(t, cur, `{"adaptiveBitrate": true, "videoMinBitrateKbps": 9000, "videoBitrateKbps": 6000}`); err == nil {
		t.Error("minimum above the maximum with ABR should fail")
	}
	if _, err := patch(t, cur, `{"videoMinBitrateKbps": 100}`); err == nil {
		t.Error("minimum < 200 should fail")
	}
}

func TestPatchKeepsAndClearsSecrets(t *testing.T) {
	cur := valid()
	cur.Passphrase = "abcdefghijk"
	var p Patch
	if err := json.Unmarshal([]byte(`{"latencyMs": 500}`), &p); err != nil {
		t.Fatal(err)
	}
	s, err := p.ApplyTo(cur)
	if err != nil || s.StreamID != cur.StreamID || s.Passphrase != cur.Passphrase || s.LatencyMs != 500 {
		t.Fatalf("absent must keep: %+v %v", s, err)
	}
	if err := json.Unmarshal([]byte(`{"passphrase": null}`), &p); err != nil {
		t.Fatal(err)
	}
	if s, _ = p.ApplyTo(cur); s.Passphrase != "" {
		t.Errorf("null must clear")
	}
	cur.Passphrase = "" // the URL cases below are not about the server-change rule
	var p2 Patch
	_ = json.Unmarshal([]byte(`{"url":"srt://relay.example:5000?streamid=publish:NEWKEY0123456789","streamId":"publish:OTHER"}`), &p2)
	if s, _ = p2.ApplyTo(cur); s.StreamID != "publish:NEWKEY0123456789" {
		t.Errorf("the URL query must win: %q", s.StreamID)
	}
	// pasting a URL with a host while in listener → caller; srt://:port → listener
	lis := cur
	lis.URL, lis.Mode = "srt://:9000", "listener"
	var p3 Patch
	_ = json.Unmarshal([]byte(`{"url":"srt://relay.example:5000?streamid=publish:NEWKEY0123456789","mode":"listener"}`), &p3)
	if s, err := p3.ApplyTo(lis); err != nil || s.Mode != "caller" {
		t.Errorf("URL with host must give caller: %+v %v", s, err)
	}
	var p4 Patch
	_ = json.Unmarshal([]byte(`{"url":"srt://:9001"}`), &p4)
	if s, err := p4.ApplyTo(cur); err != nil || s.Mode != "listener" {
		t.Errorf("srt://:port must give listener: %+v %v", s, err)
	}
	// changing server without giving the secrets again: error; giving or removing them: OK
	sec := valid()
	sec.Passphrase = "abcdefghijk"
	for body, wantErr := range map[string]string{
		`{"url":"srt://atacante.example:9000"}`:                                             "streamId",
		`{"url":"srt://atacante.example:9000","streamId":"publish:otraclave1234"}`:          "passphrase",
		`{"url":"srt://atacante.example:9000","streamId":null,"passphrase":null}`:           "",
		`{"url":"srt://eu1.relay.example.com:9999"}`:                                        "", // same host, other port
		`{"url":"srt://:9000"}`:                                                             "", // to listener: exposes nothing
		`{"url":"srt://otro.example:1?streamid=publish:nueva0123456789","passphrase":null}`: "",
	} {
		var p Patch
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		_, err := p.ApplyTo(sec)
		fe, _ := err.(FieldErrors)
		if wantErr == "" && err != nil || wantErr != "" && fe[wantErr] == "" {
			t.Errorf("%s → %v (want error on %q)", body, err, wantErr)
		}
	}
	var bad Patch
	_ = json.Unmarshal([]byte(`{"streamId":"publish:x&mode=listener"}`), &bad)
	if s, err := bad.ApplyTo(cur); err == nil || s.StreamID != cur.StreamID {
		t.Errorf("an invalid patch must not change anything")
	}
}

func TestLoadOverlayAndClearedKeys(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "default")
	app := filepath.Join(dir, "app.env")
	os.WriteFile(def, []byte("SOURCE=hdmi\nSRT_URL=srt://:9000\nSRT_MODE=listener\nSRT_LATENCY=500\nSRT_STREAMID=publish:old\nVBITRATE=6000\n"), 0o644)
	s, err := Load(def, app) // without the app file
	if err != nil || s.URL != "srt://:9000" || s.Mode != "listener" || s.LatencyMs != 500 {
		t.Fatalf("%+v %v", s, err)
	}
	s.StreamID = ""
	if err := Save(app, s); err != nil {
		t.Fatal(err)
	}
	s2, _ := Load(def, app)
	if s2.StreamID != "" {
		t.Errorf("a streamid cleared in the app must not come back from /etc/default: %q", s2.StreamID)
	}
}

func TestPublicNeverLeaksSecrets(t *testing.T) {
	s := valid()
	s.Passphrase = "supersecretpass"
	b, _ := json.Marshal(s.Public())
	if strings.Contains(string(b), key) || strings.Contains(string(b), "supersecretpass") {
		t.Fatalf("secrets leak: %s", b)
	}
	if got := MaskStreamID("publish:" + key); got != "publish:AbCd…0123" {
		t.Errorf("mask %q", got)
	}
	if got := MaskStreamID("publish:short"); got != "publish:••••" {
		t.Errorf("short mask %q", got)
	}
	if got := MaskStreamID("publish:ruta:usuario:" + key); got != "publish:••••" {
		t.Errorf("mask with ':' must hide everything: %q", got)
	}
	// URL saved by hand with a query: the API does not return it
	s.URL = "srt://h:1?streamid=publish:" + key
	if p := s.Public(); p.URL != "" || !p.URLInvalid {
		t.Errorf("URL with query must not be returned: %+v", p)
	}
}

func TestHashesWithoutOutput(t *testing.T) {
	dir := t.TempDir()
	st := &Store{DefaultPath: filepath.Join(dir, "def"), AppPath: filepath.Join(dir, "app.env")}
	os.WriteFile(st.AppPath, []byte("A=1\nOUTPUT=off\nB=2\n\n"), 0o640)
	h1 := st.Hashes()
	os.WriteFile(st.AppPath, []byte("A=1\nOUTPUT=on\nB=2\n"), 0o640)
	h2 := st.Hashes()
	if h1.Full == h2.Full || h1.NoOutput != h2.NoOutput {
		t.Errorf("OUTPUT changes Full and not NoOutput: %+v %+v", h1, h2)
	}
	// The same computation the engine does (in shell terms): printf '%s\n' "$conf" | grep -v '^OUTPUT=' | sha256sum
	sum := sha256.Sum256([]byte("A=1\nB=2\n"))
	if h2.NoOutput != hex.EncodeToString(sum[:]) || st.Hash() != h2.Full {
		t.Errorf("fingerprint without OUTPUT %s", h2.NoOutput)
	}
}
