package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	appconfig "github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

const srtlaBin = "/usr/local/bin/srtla_send"

func TestTransportAndTarget(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		exec    bool
		errKey  string
		trans   string
		target  string
		masked  string
		srtlaHP string
	}{
		{name: "no destination", env: map[string]string{}, trans: "srt"},
		{name: "no destination with OUTPUT=on is fine with the mixer", env: map[string]string{"OUTPUT": "on"}, trans: "srt"},
		{name: "caller", env: map[string]string{"SRT_URL": "srt://relay.example.com:9000"}, trans: "srt",
			target: "srt://relay.example.com:9000?mode=caller&latency=2000&payloadsize=1128",
			masked: "srt://relay.example.com:9000?mode=caller&latency=2000&payloadsize=1128"},
		{name: "listener without host", env: map[string]string{"SRT_URL": "srt://:9000", "SRT_MODE": "listener", "SRT_LATENCY": "500"},
			trans: "srt", target: "srt://:9000?mode=listener&latency=500&payloadsize=1128", masked: "srt://:9000?mode=listener&latency=500&payloadsize=1128"},
		{name: "listener with host is not checked by the launcher", env: map[string]string{"SRT_URL": "srt://h:9000", "SRT_MODE": "listener"},
			trans: "srt", target: "srt://h:9000?mode=listener&latency=2000&payloadsize=1128", masked: "srt://h:9000?mode=listener&latency=2000&payloadsize=1128"},
		{name: "caller without host is not checked either", env: map[string]string{"SRT_URL": "srt://:9000"},
			trans: "srt", target: "srt://:9000?mode=caller&latency=2000&payloadsize=1128", masked: "srt://:9000?mode=caller&latency=2000&payloadsize=1128"},
		{name: "ipv6", env: map[string]string{"SRT_URL": "srt://[fe80::1]:9000"}, trans: "srt",
			target: "srt://[fe80::1]:9000?mode=caller&latency=2000&payloadsize=1128", masked: "srt://[fe80::1]:9000?mode=caller&latency=2000&payloadsize=1128"},
		{name: "port is not range-checked", env: map[string]string{"SRT_URL": "srt://h:99999"}, trans: "srt",
			target: "srt://h:99999?mode=caller&latency=2000&payloadsize=1128", masked: "srt://h:99999?mode=caller&latency=2000&payloadsize=1128"},
		{name: "secrets", env: map[string]string{"SRT_URL": "srt://h:9000", "SRT_PASSPHRASE": "s3cretpass", "SRT_STREAMID": "publish:live:key"},
			trans:  "srt",
			target: "srt://h:9000?mode=caller&latency=2000&payloadsize=1128&passphrase=s3cretpass&pbkeylen=16&streamid=publish:live:key",
			masked: "srt://h:9000?mode=caller&latency=2000&payloadsize=1128&passphrase=***&pbkeylen=16&streamid=***"},
		{name: "srtla", env: map[string]string{"SRT_URL": "srtla://relay.example.com:5000", "SRT_LATENCY": "2000", "SRT_STREAMID": "publish:k"},
			exec: true, trans: "srtla",
			target:  "srt://127.0.0.1:6000?mode=caller&latency=2000&payloadsize=1128&streamid=publish:k&conntimeo=10000",
			masked:  "srt://127.0.0.1:6000?mode=caller&latency=2000&payloadsize=1128&streamid=***&conntimeo=10000",
			srtlaHP: "relay.example.com:5000"},
		{name: "srtla local port", env: map[string]string{"SRT_URL": "srtla://1.2.3.4:5000", "SRTLA_LOCAL_PORT": "6100"},
			exec: true, trans: "srtla",
			target:  "srt://127.0.0.1:6100?mode=caller&latency=2000&payloadsize=1128&conntimeo=10000",
			masked:  "srt://127.0.0.1:6100?mode=caller&latency=2000&payloadsize=1128&conntimeo=10000",
			srtlaHP: "1.2.3.4:5000"},
		{name: "srtla requires caller", env: map[string]string{"SRT_URL": "srtla://h:5000", "SRT_MODE": "listener"}, exec: true, errKey: "SRT_MODE"},
		{name: "srtla requires srtla_send", env: map[string]string{"SRT_URL": "srtla://h:5000"}, errKey: "SRTLA_BIN"},
		{name: "srtla without host", env: map[string]string{"SRT_URL": "srtla://:5000"}, exec: true, errKey: "SRT_URL"},
		{name: "srtla ipv6", env: map[string]string{"SRT_URL": "srtla://[::1]:5000"}, exec: true, errKey: "SRT_URL"},
		{name: "srtla with query", env: map[string]string{"SRT_URL": "srtla://h:5000?streamid=x"}, exec: true, errKey: "SRT_URL"},
		{name: "srt with query", env: map[string]string{"SRT_URL": "srt://h:9000?latency=1"}, errKey: "SRT_URL"},
		{name: "srt without port", env: map[string]string{"SRT_URL": "srt://h"}, errKey: "SRT_URL"},
		{name: "other scheme", env: map[string]string{"SRT_URL": "SRT://h:9000"}, errKey: "SRT_URL"},
		{name: "srt url checked even with OUTPUT=off", env: map[string]string{"SRT_URL": "srt://h h:9000", "OUTPUT": "off"}, errKey: "SRT_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fake{env: tc.env, exec: map[string]bool{srtlaBin: tc.exec}}
			c, err := load(t, f)
			if tc.errKey != "" {
				wantInvalid(t, err, tc.errKey)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Transport != tc.trans {
				t.Errorf("transport %s, want %s", c.Transport, tc.trans)
			}
			if got := c.SRTTarget().Reveal(); got != tc.target {
				t.Errorf("target\n got %s\nwant %s", got, tc.target)
			}
			if got := c.MaskedTarget(); got != tc.masked {
				t.Errorf("masked\n got %s\nwant %s", got, tc.masked)
			}
			hp := ""
			if c.SRTLAHost() != "" {
				hp = c.SRTLAHost() + ":" + strconv.Itoa(c.SRTLAPort())
			}
			if hp != tc.srtlaHP {
				t.Errorf("srtla relay %q, want %q", hp, tc.srtlaHP)
			}
			if tc.trans == "srt" && c.SRTLAPort() != 0 {
				t.Errorf("srtla port %d without srtla", c.SRTLAPort())
			}
		})
	}
}

func TestProgram(t *testing.T) {
	cases := []struct {
		env    map[string]string
		chosen int
		want   Program
	}{
		{map[string]string{}, 60, Program{1920, 1080, "1920x1080", 60, "60/1", 120}},
		{map[string]string{}, 50, Program{1920, 1080, "1920x1080", 50, "50/1", 100}},
		{map[string]string{}, 0, Program{1920, 1080, "1920x1080", 30, "30/1", 60}},
		{map[string]string{"FPS": "25", "RES": "1280x720"}, 60, Program{1280, 720, "1280x720", 25, "25/1", 50}},
		{map[string]string{"GOP": "30"}, 50, Program{1920, 1080, "1920x1080", 50, "50/1", 30}},
		{map[string]string{"FPS": "0"}, 60, Program{1920, 1080, "1920x1080", 0, "0/1", 0}},
	}
	for _, tc := range cases {
		c := mustLoad(t, fake{env: tc.env})
		if got := c.Program(tc.chosen); got != tc.want {
			t.Errorf("%v chosen %d: %+v, want %+v", tc.env, tc.chosen, got, tc.want)
		}
	}
}

func TestMainSources(t *testing.T) {
	cases := []struct {
		env              map[string]string
		device, alsa     string
		wantMain, wantAu string
	}{
		{map[string]string{"SOURCE": "hdmi"}, "", "", "hdmi", "i2s"},
		{map[string]string{"SOURCE": "test"}, "", "", "test", "none"},
		{map[string]string{"SOURCE": "usb"}, "", "", "usb:auto", "none"},
		{map[string]string{"SOURCE": "usb"}, "/dev/v4l/by-id/cam", "C4K", "/dev/v4l/by-id/cam", "alsa:C4K"},
		{map[string]string{"SOURCE": "usb", "VIDEO_DEVICE": "/dev/v4l/by-id/x"}, "", "", "/dev/v4l/by-id/x", "none"},
		{map[string]string{"SOURCE": "hdmi", "AUDIO_SOURCE": "none"}, "", "", "hdmi", "none"},
		{map[string]string{"SOURCE": "test", "AUDIO_SOURCE": "i2s"}, "", "", "test", "i2s"},
		{map[string]string{"SOURCE": "hdmi", "AUDIO_SOURCE": "alsa:USB"}, "", "X", "hdmi", "alsa:USB"},
	}
	for _, tc := range cases {
		c := mustLoad(t, fake{env: tc.env})
		if got := c.MixerMain(tc.device); got != tc.wantMain {
			t.Errorf("%v: main %s, want %s", tc.env, got, tc.wantMain)
		}
		if got := c.AudioMain(tc.alsa); got != tc.wantAu {
			t.Errorf("%v: audio %s, want %s", tc.env, got, tc.wantAu)
		}
	}
	c := mustLoad(t, fake{env: map[string]string{"ALSA_ADMAIF": "3", "ALSA_CARD": "card"}})
	if c.I2SDevice() != "hw:card,2" {
		t.Fatalf("I2SDevice %s", c.I2SDevice())
	}
}

// CONF_SHA must be what the app computes (config.Store.Hashes().NoOutput), or the web would show
// «cambios sin aplicar» forever.
func TestConfSHAMatchesApp(t *testing.T) {
	contents := map[string]string{
		"app file":                  string(appFile(t)),
		"no trailing newline":       "SRT_URL=srt://h:9000\nOUTPUT=on",
		"several trailing newlines": "SRT_URL=srt://h:9000\nOUTPUT=off\n\n\n\n",
		"CRLF":                      "SRT_URL=srt://h:9000\r\nOUTPUT=on\r\nVBITRATE=5000\r\n",
		"comments and unknown keys": "# comment\nFOO=bar\n\nEIRL_MANAGED_BY=local\nOUTPUT=on\n  OUTPUT=off\n",
		"empty":                     "",
		"only newlines":             "\n\n",
		"leading newlines":          "\n\nVBITRATE=5000\n",
		"only OUTPUT":               "OUTPUT=on\n", // bash would hash "" here; see the report
		"OUTPUT not at line start":  "#OUTPUT=on\nXOUTPUT=on\n",
		"utf-8 and spaces":          "SRT_STREAMID=añ ü \nOUTPUT=on\n",
	}
	dir := t.TempDir()
	for name, content := range contents {
		p := filepath.Join(dir, "engine.env")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		want := (&appconfig.Store{AppPath: p}).Hashes().NoOutput
		if got := ConfHash([]byte(content)); got != want {
			t.Errorf("%s: ConfHash %s, app %s", name, got, want)
		}
		c, err := Load(Options{
			LookupEnv: func(k string) (string, bool) {
				if k == "EIRL_CONF" {
					return p, true
				}
				return "", false
			},
			FindVDEV:   func() string { return "/dev/video0" },
			Executable: func(string) bool { return true },
		})
		if err != nil {
			// some of these contents are invalid for the engine; the hash is still compared above
			continue
		}
		if c.ConfSHA != want {
			t.Errorf("%s: Load CONF_SHA %s, app %s", name, c.ConfSHA, want)
		}
	}
	// OUTPUT does not change the hash; anything else does
	if ConfHash([]byte("A=1\nOUTPUT=on\n")) != ConfHash([]byte("A=1\nOUTPUT=off")) {
		t.Error("OUTPUT changed the hash")
	}
	if ConfHash([]byte("A=1\n")) == ConfHash([]byte("A=2\n")) {
		t.Error("content did not change the hash")
	}
	// Known value: sha256("A=1\n"), as `printf 'A=1\n' | sha256sum`
	if got := ConfHash([]byte("A=1")); got != "91d6a3d55e9fea7911c537afae6607c77fa8bc0f3a76c375e104ac5a8cfa84db" {
		t.Errorf("ConfHash(A=1) = %s", got)
	}
}

// appFile is what the app writes (config.Save), with secrets and ABR.
func appFile(t *testing.T) []byte {
	t.Helper()
	s := appconfig.Defaults()
	s.URL, s.StreamID, s.Passphrase, s.ABR, s.VideoMinKbps, s.Output = "srt://relay.example.com:9000", "publish:live:k", "0123456789ab", true, 1500, "off"
	p := filepath.Join(t.TempDir(), "engine.env")
	if err := appconfig.Save(p, s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// What the app saves is what the engine loads.
func TestAppRoundTrip(t *testing.T) {
	content := string(appFile(t))
	c := mustLoad(t, fake{env: map[string]string{"EIRL_CONF": confPath, "SOURCE": "test"}, conf: &content})
	if c.SRTURL != "srt://relay.example.com:9000" || c.SRTStreamID.Reveal() != "publish:live:k" ||
		c.SRTPassphrase.Reveal() != "0123456789ab" || !c.ABR || c.VBitrateMin != 1500 || c.OutputAtBoot ||
		c.Source != "hdmi" || c.VBitrate != 6000 || c.ABitrate != 128 || c.SRTLatencyMs != 2000 || c.SRTMode != "caller" ||
		c.EDIDPreferred != "1080p30" || c.VideoDevice != "auto" || c.AudioSource != "auto" {
		t.Fatalf("%+v", *c)
	}
}

func TestStateFile(t *testing.T) {
	content := "SRT_URL=srtla://relay.example.com:5000\nABR=1\nOUTPUT=off\nSOURCE=usb\n"
	c := mustLoad(t, fake{env: map[string]string{"EIRL_CONF": confPath, "EDID": "/usr/lib/eirl-encoder/engine/edid-1080p50-lpcm48k.bin"},
		conf: &content, exec: map[string]bool{srtlaBin: true}})
	starting := string(c.StateFile(c.StartingState()))
	wantStarting := "OUTPUT=off\nSOURCE=usb\nCONF_SHA=" + ConfHash([]byte(content)) + "\nEDID=1080p50\nTRANSPORT=srtla\nABR=1\n" +
		"INPUT=starting\nDEVICE=\nDEVICE_NAME=\nFORMAT=\nMODE=\nRATE=\nAUDIO=\nMIXER=1\n"
	if starting != wantStarting {
		t.Fatalf("starting:\n%s\nwant:\n%s", starting, wantStarting)
	}
	p := c.Program(50)
	ready := c.ReadyState(p, "/dev/v4l/by-id/usb-cam", SanitizeDeviceName("Cam Link 4K\n$(x)"), c.AudioMain("C4K"))
	b := c.StateFile(ready)

	// Through the app's parser
	rs := parseRunState(b)
	want := appRunState{Output: "off", Source: "usb", ConfSHA: c.ConfSHA, EDID: "1080p50", Transport: "srtla", ABR: "1", Mixer: "1",
		Input: "ready", Device: "/dev/v4l/by-id/usb-cam", DeviceName: "Cam Link 4K(x)", Rate: "50/1", Audio: "mezclador:alsa:C4K"}
	if rs != want {
		t.Fatalf("app parse:\n got %+v\nwant %+v", rs, want)
	}

	// Written atomically, nothing left behind
	dir := t.TempDir()
	c.RunDir = dir
	if err := c.WriteState(ready); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "state"))
	if err != nil || !bytes.Equal(got, b) {
		t.Fatalf("state file %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.tmp")); !os.IsNotExist(err) {
		t.Fatal("state.tmp left behind")
	}
	// No RUN_DIR: nothing, no error (like the launcher)
	c.RunDir = filepath.Join(dir, "missing")
	if err := c.WriteState(ready); err != nil {
		t.Fatal(err)
	}
}

// The keys we write are exactly those the app's parser knows (manager/internal/api/status.go).
func TestStateKeysMatchAppParser(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join("..", "..", "api", "status.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var appKeys []string
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "runState" || fd.Recv == nil {
			return true
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if cc, ok := n.(*ast.CaseClause); ok {
				for _, e := range cc.List {
					if bl, ok := e.(*ast.BasicLit); ok {
						s, _ := strconv.Unquote(bl.Value)
						appKeys = append(appKeys, s)
					}
				}
			}
			return true
		})
		return false
	})
	c := mustLoad(t, fake{env: map[string]string{}})
	var ours []string
	for _, l := range strings.Split(strings.TrimSuffix(string(c.StateFile(c.StartingState())), "\n"), "\n") {
		k, _, _ := strings.Cut(l, "=")
		ours = append(ours, k)
	}
	if len(appKeys) == 0 {
		t.Fatal("runState not found in status.go")
	}
	if !sameSet(appKeys, ours) {
		t.Fatalf("app parser keys %v, state keys %v", appKeys, ours)
	}
	wantOrder := []string{"OUTPUT", "SOURCE", "CONF_SHA", "EDID", "TRANSPORT", "ABR", "INPUT", "DEVICE", "DEVICE_NAME", "FORMAT", "MODE", "RATE", "AUDIO", "MIXER"}
	if !reflect.DeepEqual(ours, wantOrder) {
		t.Fatalf("order %v", ours)
	}
}

func sameSet(a, b []string) bool {
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

// appRunState and parseRunState are a verbatim copy of runState in manager/internal/api/status.go
// (unexported there); TestStateKeysMatchAppParser catches a drift in its keys.
type appRunState struct {
	Output, Source, ConfSHA, EDID, Transport, ABR, Mixer string
	Input, Device, DeviceName, Format, Mode, Rate, Audio string
}

func parseRunState(b []byte) appRunState {
	var rs appRunState
	for _, line := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "OUTPUT":
			rs.Output = v
		case "SOURCE":
			rs.Source = v
		case "CONF_SHA":
			rs.ConfSHA = v
		case "TRANSPORT":
			rs.Transport = v
		case "ABR":
			rs.ABR = v
		case "EDID":
			rs.EDID = v
		case "INPUT":
			rs.Input = v
		case "DEVICE":
			rs.Device = v
		case "DEVICE_NAME":
			rs.DeviceName = v
		case "FORMAT":
			rs.Format = v
		case "MODE":
			rs.Mode = v
		case "RATE":
			rs.Rate = v
		case "AUDIO":
			rs.Audio = v
		case "MIXER":
			rs.Mixer = v
		}
	}
	return rs
}

func TestEDIDStem(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/lib/eirl-encoder/engine/edid-1080p30-lpcm48k.bin": "1080p30",
		"/srv/custom.bin":         "custom.bin",
		"/srv/edid-x.bin":         "x.bin",
		"/srv/-lpcm48k.bin":       "-lpcm48k.bin", // basename keeps a suffix that is the whole name
		"edid-720p60-lpcm48k.bin": "720p60",
	} {
		if got := EDIDStem(in); got != want {
			t.Errorf("EDIDStem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeDeviceName(t *testing.T) {
	if got := SanitizeDeviceName("Elgato Cam Link 4K (USB3) + ä;\n"); got != "Elgato Cam Link 4K (USB3) + " {
		t.Fatalf("%q", got)
	}
	if got := SanitizeDeviceName(strings.Repeat("ab", 40)); len(got) != 48 {
		t.Fatalf("len %d", len(got))
	}
}

func TestPrepareDirs(t *testing.T) {
	var log bytes.Buffer
	elog.SetOutput(&log)
	defer elog.SetOutput(nil)

	dir := t.TempDir()
	c := mustLoad(t, fake{env: map[string]string{"RUN_DIR": dir + "/run", "STATE_DIR": dir + "/state"}})
	c.PrepareRunDir()
	if c.SRTStats != dir+"/run/srt-stats.json" || !c.RunDirUsable() {
		t.Fatalf("run dir not prepared: %+v", *c)
	}
	if !c.PrepareStateDir() {
		t.Fatal("state dir not prepared")
	}
	c.PrepareThumb()
	if !c.Thumb {
		t.Fatal("thumb disabled with a writable dir")
	}
	if log.Len() != 0 {
		t.Fatalf("unexpected log %q", log.String())
	}

	if os.Geteuid() != 0 {
		ro := filepath.Join(dir, "ro")
		if err := os.Mkdir(ro, 0o555); err != nil {
			t.Fatal(err)
		}
		c := mustLoad(t, fake{env: map[string]string{"RUN_DIR": ro}})
		c.PrepareRunDir()
		c.PrepareThumb()
		if c.SRTStats != "" || c.Thumb || c.RunDirUsable() {
			t.Fatalf("read-only run dir: %+v", *c)
		}
		want := "[engine] event=run_dir_unwritable dir=" + ro + "\n[engine] event=thumb_disabled dir=" + ro + "\n"
		if log.String() != want {
			t.Fatalf("log %q, want %q", log.String(), want)
		}
	}

	// THUMB_FILE empty: off without a warning
	log.Reset()
	c = mustLoad(t, fake{env: map[string]string{"THUMB_FILE": ""}})
	c.PrepareThumb()
	if c.Thumb || log.Len() != 0 {
		t.Fatalf("thumb %v log %q", c.Thumb, log.String())
	}
}

// No exported formatting of the configuration shows the stream id or the passphrase.
func TestSecretsNeverLeak(t *testing.T) {
	const sid, pass = "publish:live:SECRETSTREAMKEY", "SECRETPASSPHRASE42"
	c := mustLoad(t, fake{env: map[string]string{"SRT_URL": "srt://h:9000", "SRT_STREAMID": sid, "SRT_PASSPHRASE": pass}})
	leaks := func(where, s string) {
		t.Helper()
		if strings.Contains(s, "SECRET") {
			t.Errorf("%s leaks a secret: %s", where, s)
		}
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"} {
		leaks("Config "+verb, fmt.Sprintf(verb, *c))
		leaks("*Config "+verb, fmt.Sprintf(verb, c))
		leaks("SRTTarget "+verb, fmt.Sprintf(verb, c.SRTTarget()))
		leaks("SRTQuery "+verb, fmt.Sprintf(verb, c.SRTQuery()))
		leaks("in a struct "+verb, fmt.Sprintf(verb, struct{ C *Config }{c}))
	}
	j, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	leaks("json", string(j))
	leaks("elog", elog.Format("engine", "srt_target", "target", c.SRTTarget(), "sid", c.SRTStreamID, "cfg", c))
	leaks("MaskedTarget", c.MaskedTarget())
	if c.MaskedTarget() != "srt://h:9000?mode=caller&latency=2000&payloadsize=1128&passphrase=***&pbkeylen=16&streamid=***" {
		t.Errorf("masked %s", c.MaskedTarget())
	}

	// Every exported method without arguments, whatever it returns, formatted with %v
	v := reflect.ValueOf(c)
	for i := 0; i < v.NumMethod(); i++ {
		m := v.Type().Method(i)
		if m.Type.NumIn() != 1 || strings.HasPrefix(m.Name, "Prepare") { // Prepare* touch the filesystem
			continue
		}
		for _, out := range v.Method(i).Call(nil) {
			leaks(m.Name, fmt.Sprintf("%v", out.Interface()))
		}
	}
	// Methods with arguments that render text
	leaks("StateFile", string(c.StateFile(c.ReadyState(c.Program(60), "", "", "none"))))

	// Errors never carry the value
	for _, env := range []map[string]string{
		{"SRT_STREAMID": "SECRET with space"}, {"SRT_PASSPHRASE": "SECRET&x=1"}, {"SRT_URL": "srt://SECRET:1?streamid=x"},
	} {
		_, err := load(t, fake{env: env})
		if err == nil {
			t.Fatalf("%v accepted", env)
		}
		leaks("error", err.Error())
		leaks("error %+v", fmt.Sprintf("%+v", err))
	}
	// Reveal is the only way out
	if c.SRTStreamID.Reveal() != sid || c.SRTPassphrase.Reveal() != pass ||
		!strings.Contains(c.SRTTarget().Reveal(), sid) {
		t.Fatal("Reveal lost the value")
	}
}

// SRTLA.md §9.2: either sender makes srtla valid (the output falls back to the one that is installed);
// with neither, SRTLA_BIN is reported, as before.
func TestSRTLAEitherSender(t *testing.T) {
	env := map[string]string{"SRT_URL": "srtla://h:5000"}
	for _, bins := range [][]string{{srtlaBin}, {"/usr/local/bin/srtla_send_rs"}} {
		f := fake{env: env, exec: map[string]bool{}}
		for _, b := range bins {
			f.exec[b] = true
		}
		if _, err := load(t, f); err != nil {
			t.Errorf("only %v installed: %v", bins, err)
		}
	}
	_, err := load(t, fake{env: env, exec: map[string]bool{}})
	wantInvalid(t, err, "SRTLA_BIN")
}

// SRTLA.md §9.5 and §9.7: the payload and the mux alignment, and the relay's host for each kind of target.
func TestPayloadAndRelayHost(t *testing.T) {
	exec := map[string]bool{srtlaBin: true}
	cases := []struct {
		env      map[string]string
		host     string
		tsPer    int
		targetAt string // SRTTargetAt("192.0.2.7"), without the query
	}{
		{map[string]string{"SRT_URL": "srt://relay.example.com:5000"}, "relay.example.com", 6, "srt://192.0.2.7:5000"},
		{map[string]string{"SRT_URL": "srt://relay.example.com:5000", "SRT_PAYLOAD": "1316"}, "relay.example.com", 7, "srt://192.0.2.7:5000"},
		{map[string]string{"SRT_URL": "srt://h:9000", "SRT_MODE": "listener"}, "", 6, "srt://h:9000"},
		{map[string]string{"SRT_URL": "srt://:9000"}, "", 6, "srt://:9000"},
		{map[string]string{"SRT_URL": "srt://[fe80::1]:9000"}, "", 6, "srt://[fe80::1]:9000"},
		{map[string]string{"SRT_URL": "srtla://relay.example.com:5001"}, "relay.example.com", 6, "srt://127.0.0.1:6000"},
		{map[string]string{"OUTPUT_PROTOCOL": "udp", "OUTPUT_URL": "udp://h:5000"}, "", 7, ""},
	}
	for _, tc := range cases {
		c, err := load(t, fake{env: tc.env, exec: exec})
		if err != nil {
			t.Fatal(tc.env, err)
		}
		if got := c.RelayHost(); got != tc.host {
			t.Errorf("%v: host %q, want %q", tc.env, got, tc.host)
		}
		if got := c.TSPerPacket(); got != tc.tsPer {
			t.Errorf("%v: TS per packet %d, want %d", tc.env, got, tc.tsPer)
		}
		got, _, _ := strings.Cut(c.SRTTargetAt("192.0.2.7").Reveal(), "?")
		if got != tc.targetAt {
			t.Errorf("%v: target %q, want %q", tc.env, got, tc.targetAt)
		}
	}
}

// 🔴 srt-live-transmit's file://con sends a whole -chunk even after a short read (the tail of the previous packet
// goes out as TS): every write of the mux must be exactly one chunk. So for srt and srtla the mux's alignment and
// the chunk come from the same SRT_PAYLOAD, always a whole number of TS packets.
func TestChunkIsWholeMuxWrites(t *testing.T) {
	for _, payload := range []string{"1128", "1316"} {
		for _, url := range []string{"srt://relay.example.com:5000", "srtla://relay.example.com:5001"} {
			c, err := load(t, fake{env: map[string]string{"SRT_URL": url, "SRT_PAYLOAD": payload}, exec: map[string]bool{srtlaBin: true}})
			if err != nil {
				t.Fatal(err)
			}
			if c.TSPerPacket()*tsPacket != c.SRTPayloadBytes() || !strings.Contains(c.SRTQuery().Reveal(), "payloadsize="+payload) {
				t.Errorf("%s %s: %d TS per write, chunk %d, query %s", url, payload, c.TSPerPacket(), c.SRTPayloadBytes(), c.SRTQuery().Reveal())
			}
		}
	}
}
