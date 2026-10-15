package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func custom(protocol string) Destination {
	t, _ := TemplateByID(TemplateCustom)
	d := NewDestination(t, protocol)
	d.ID = "d-0000abcd"
	return d
}

// Every protocol with its URL forms, its secrets and the fields it does not use (OUTPUTS.md §3).
func TestDestinationValidatePerProtocol(t *testing.T) {
	ok := map[string]func(*Destination){
		"srt caller":   func(d *Destination) { d.URL, d.StreamID = "srt://relay.example:5000", "publish:"+key },
		"srt listener": func(d *Destination) { d.Protocol, d.URL, d.Mode = "srt", "srt://:9000", "listener" },
		"srtla":        func(d *Destination) { d.Protocol, d.URL, d.LatencyMs = "srtla", "srtla://relay.example:5001", 2000 },
		"rtmp": func(d *Destination) {
			d.Protocol, d.URL, d.LatencyMs, d.Key = "rtmp", "rtmp://live.twitch.tv/app", 0, "live_1234/x?bandwidthtest=true"
		},
		"rtmps ipv6 port":     func(d *Destination) { d.Protocol, d.URL, d.LatencyMs = "rtmps", "rtmps://[2001:db8::1]:443/rtmp/", 0 },
		"rtmp key ends in =":  func(d *Destination) { d.Protocol, d.URL, d.LatencyMs, d.Key = "rtmp", "rtmp://h/app", 0, "abc==" },
		"rist main":           func(d *Destination) { setRIST(d, "rist://10.0.0.2:5001", "main", "abcdefghij", "user.1", "pw~x") },
		"rist simple even":    func(d *Destination) { setRIST(d, "rist://10.0.0.2:5000", "simple", "", "", "") },
		"udp":                 func(d *Destination) { d.Protocol, d.URL, d.LatencyMs = "udp", "udp://239.0.0.1:1234", 0 },
		"rtp":                 func(d *Destination) { d.Protocol, d.URL, d.LatencyMs = "udp", "rtp://192.168.1.20:5004", 0 },
		"h265, 720p and half": func(d *Destination) { d.VideoCodec, d.OutputSize, d.OutputFPS = "h265", "1280x720", "half" },
	}
	for name, mut := range ok {
		d := custom("srt")
		d.URL = "srt://relay.example:5000"
		mut(&d)
		if err := d.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]struct {
		mut   func(*Destination)
		field string
	}{
		"no server":              {func(d *Destination) { d.URL = "" }, "url"},
		"scheme ≠ protocol":      {func(d *Destination) { d.Protocol = "srtla" }, "url"},
		"rtmp in an srt dest":    {func(d *Destination) { d.URL = "rtmp://h/app" }, "url"},
		"srtla listener":         {func(d *Destination) { d.Protocol, d.URL, d.Mode = "srtla", "srtla://h:1", "listener" }, "mode"},
		"rtmp listener":          {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs, d.Mode = "rtmp", "rtmp://h/app", 0, "listener" }, "mode"},
		"rtmp without app":       {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs = "rtmp", "rtmp://h", 0 }, "url"},
		"rtmp with query":        {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs = "rtmp", "rtmp://h/app?k=1", 0 }, "url"},
		"rtmp bare ipv6":         {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs = "rtmp", "rtmp://2001:db8::1/app", 0 }, "url"},
		"rtmp port 0":            {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs = "rtmp", "rtmp://h:0/app", 0 }, "url"},
		"rtmp space":             {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs = "rtmp", "rtmp://h/a pp", 0 }, "url"},
		"rtmp key with space":    {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs, d.Key = "rtmp", "rtmp://h/app", 0, "a b" }, "key"},
		"rtmp key with #":        {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs, d.Key = "rtmp", "rtmp://h/app", 0, "a#b" }, "key"},
		"rtmp key with quote":    {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs, d.Key = "rtmp", "rtmp://h/app", 0, `a"b` }, "key"},
		"rtmp latency":           {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs = "rtmp", "rtmp://h/app", 200 }, "latencyMs"},
		"stream ID on rtmp":      {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs, d.StreamID = "rtmp", "rtmp://h/app", 0, "x" }, "streamId"},
		"key on srt":             {func(d *Destination) { d.Key = "live_x" }, "key"},
		"rist without port":      {func(d *Destination) { setRIST(d, "rist://h", "main", "", "", "") }, "url"},
		"rist with path":         {func(d *Destination) { setRIST(d, "rist://h:5000/x", "main", "", "", "") }, "url"},
		"rist latency 20":        {func(d *Destination) { setRIST(d, "rist://h:5000", "main", "", "", ""); d.LatencyMs = 20 }, "latencyMs"},
		"rist user alone":        {func(d *Destination) { setRIST(d, "rist://h:5000", "main", "", "u", "") }, "username"},
		"rist passphrase with &": {func(d *Destination) { setRIST(d, "rist://h:5000", "main", "abcdefghij&x=1", "", "") }, "passphrase"},
		"rist password with ,":   {func(d *Destination) { setRIST(d, "rist://h:5000", "main", "", "u", "a,b") }, "password"},
		"rist user with =":       {func(d *Destination) { setRIST(d, "rist://h:5000", "main", "", "u=1", "p") }, "username"},
		"rist simple psk":        {func(d *Destination) { setRIST(d, "rist://h:5000", "simple", "abcdefghij", "", "") }, "passphrase"},
		"rist simple srp":        {func(d *Destination) { setRIST(d, "rist://h:5000", "simple", "", "u", "p") }, "username"},
		"rist simple odd port":   {func(d *Destination) { setRIST(d, "rist://h:5001", "simple", "", "", "") }, "url"},
		"rist profile":           {func(d *Destination) { setRIST(d, "rist://h:5000", "advanced", "", "", "") }, "ristProfile"},
		"udp without port":       {func(d *Destination) { d.Protocol, d.URL, d.LatencyMs = "udp", "udp://h", 0 }, "url"},
		"codec":                  {func(d *Destination) { d.VideoCodec = "av1" }, "videoCodec"},
		"size":                   {func(d *Destination) { d.OutputSize = "1920x1080" }, "outputSize"},
		"fps":                    {func(d *Destination) { d.OutputFPS = "" }, "outputFps"},
		"name empty":             {func(d *Destination) { d.Name = "" }, "name"},
		"name 33":                {func(d *Destination) { d.Name = strings.Repeat("é", 33) }, "name"},
		"name newline":           {func(d *Destination) { d.Name = "a\nb" }, "name"},
		"template":               {func(d *Destination) { d.Template = "vimeo" }, "template"},
		"protocol":               {func(d *Destination) { d.Protocol = "whip" }, "protocol"},
		"video kbps":             {func(d *Destination) { d.VideoKbps = 20001 }, "videoBitrateKbps"},
		"audio kbps":             {func(d *Destination) { d.AudioKbps = 100 }, "audioBitrateKbps"},
	}
	for name, c := range bad {
		d := custom("srt")
		d.URL = "srt://relay.example:5000"
		c.mut(&d)
		fe, _ := d.Validate().(FieldErrors)
		if fe[c.field] == "" {
			t.Errorf("%s: want an error on %s, got %v", name, c.field, fe)
		}
	}
}

func setRIST(d *Destination, url, profile, psk, user, pass string) {
	d.Protocol, d.URL, d.LatencyMs, d.RISTProfile, d.Passphrase, d.Username, d.Password = "rist", url, 1000, profile, psk, user, pass
}

// A template that lists codecs refuses the others, and its maximum bounds the video bitrate (OUTPUTS.md §9.2).
func TestTemplateLimits(t *testing.T) {
	for _, c := range []struct {
		tmpl, proto string
		mut         func(*Destination)
		field       string
	}{
		{"twitch", "rtmp", func(d *Destination) { d.VideoCodec = "h265" }, "videoCodec"},
		{"twitch", "rtmp", func(d *Destination) { d.VideoKbps = 6500 }, "videoBitrateKbps"},
		{"twitch", "srt", func(d *Destination) {}, "protocol"},
		{"kick", "rtmps", func(d *Destination) { d.URL = "rtmps://x.live-video.net:443/app"; d.VideoKbps = 8001 }, "videoBitrateKbps"},
		{"facebook", "rtmp", func(d *Destination) { d.URL = "rtmp://h/rtmp/" }, "protocol"},
		{"eirl", "rtmp", func(d *Destination) { d.URL = "rtmp://h/app" }, "protocol"},
	} {
		tp, _ := TemplateByID(c.tmpl)
		d := NewDestination(tp, c.proto)
		d.ID = "d-0000abcd"
		c.mut(&d)
		fe, _ := d.Validate().(FieldErrors)
		if fe[c.field] == "" {
			t.Errorf("%s/%s: want an error on %s, got %v", c.tmpl, c.proto, c.field, fe)
		}
		if fe[c.field] != "" && c.field == "videoCodec" && !strings.Contains(fe[c.field], "codec_unsupported") {
			t.Errorf("codec_unsupported: %q", fe[c.field])
		}
	}
	// Defaults: ABR on, minimum 1000, min(6000, max); H.265 towards EnhancedIRL, H.264 elsewhere; YouTube takes H.265
	for id, want := range map[string]struct {
		codec string
		kbps  int
	}{"eirl": {"h265", 6000}, "twitch": {"h264", 6000}, "youtube": {"h264", 6000}, "kick": {"h264", 6000},
		"facebook": {"h264", 6000}, "custom": {"h264", 6000}} {
		tp, _ := TemplateByID(id)
		d := NewDestination(tp, tp.Protocols[0])
		if d.VideoCodec != want.codec || d.VideoKbps != want.kbps || !d.ABR || d.VideoMinKbps != 1000 || d.OutputFPS != "full" {
			t.Errorf("%s defaults: %+v", id, d)
		}
	}
	yt, _ := TemplateByID("youtube")
	d := NewDestination(yt, "rtmps")
	d.ID, d.VideoCodec, d.Key = "d-0000abcd", "h265", "abcd-efgh-ijkl-mnop"
	if err := d.Validate(); err != nil {
		t.Errorf("YouTube with H.265: %v", err)
	}
	// Every template server passes the URL rules and agrees with its protocol
	for _, tp := range Templates {
		for proto, u := range tp.URLs {
			if _, err := checkOutputURL(u); err != nil || ProtocolOf(u) != proto {
				t.Errorf("%s %s: %q %v", tp.ID, proto, u, err)
			}
		}
		if tp.URL != tp.URLs[tp.Protocols[0]] {
			t.Errorf("%s: url is not the default protocol's", tp.ID)
		}
	}
}

// What the app writes for every protocol passes the engine's regexes (OUTPUTS.md §3) and reads back the same.
func TestRenderOutputKeysRoundTrip(t *testing.T) {
	engine := map[string]*regexp.Regexp{
		"OUTPUT_PROTOCOL":   regexp.MustCompile(`^(|srt|srtla|rtmp|rtmps|rist|udp)$`),
		"OUTPUT_KEY":        regexp.MustCompile(`^([A-Za-z0-9._~!$()*+,;=:@%/?&-]{1,512})?$`),
		"OUTPUT_PASSPHRASE": regexp.MustCompile(`^([A-Za-z0-9_.~-]{10,79})?$`),
		"OUTPUT_USER":       regexp.MustCompile(`^([A-Za-z0-9_.-]{1,64})?$`),
		"OUTPUT_PASSWORD":   regexp.MustCompile(`^([A-Za-z0-9_.~-]{1,128})?$`),
		"RIST_PROFILE":      regexp.MustCompile(`^(|simple|main)$`),
		"OUTPUT_LATENCY":    regexp.MustCompile(`^[0-9]{0,5}$`),
		"OUTPUT_VCODEC":     regexp.MustCompile(`^(|h264|h265)$`),
		"OUTPUT_SIZE":       regexp.MustCompile(`^(|1280x720|960x540)$`),
		"OUTPUT_FPS":        regexp.MustCompile(`^(|full|half)$`),
	}
	dests := []Destination{custom("srt"), custom("rtmp"), custom("rist"), custom("udp")}
	dests[0].URL, dests[0].StreamID = "srt://relay.example:5000", "publish:"+key
	dests[1].URL, dests[1].Key, dests[1].VideoCodec, dests[1].OutputSize, dests[1].OutputFPS = "rtmps://h:443/app", "live_x==", "h265", "960x540", "half"
	dests[1].Protocol = "rtmps"
	dests[2].URL, dests[2].Passphrase, dests[2].Username, dests[2].Password = "rist://10.0.0.2:5000", "abcdefghij", "u", "p~1"
	dests[3].URL = "rtp://239.1.1.1:5004"
	for _, d := range dests {
		if err := d.Validate(); err != nil {
			t.Fatalf("%s: %v", d.Protocol, err)
		}
		ds := Destinations{V: 1, Active: d.ID, Destinations: []Destination{d}}
		s := ds.Flatten(Defaults())
		path := filepath.Join(t.TempDir(), "engine.env")
		if err := Save(path, s); err != nil {
			t.Fatalf("%s: %v", d.Protocol, err)
		}
		env, _ := ReadEnv(path)
		for k, re := range engine {
			if v, ok := env[k]; !ok || !re.MatchString(v) {
				t.Errorf("%s: %s=%q (written: %v)", d.Protocol, k, v, ok)
			}
		}
		if usesSRT(d.Protocol) != (env["SRT_URL"] != "") || usesSRT(d.Protocol) == (env["OUTPUT_URL"] != "") {
			t.Errorf("%s: SRT_URL=%q OUTPUT_URL=%q", d.Protocol, env["SRT_URL"], env["OUTPUT_URL"])
		}
		back, err := Load(filepath.Join(t.TempDir(), "none"), path)
		if err != nil || back != s {
			t.Errorf("%s: round trip\n got %+v\nwant %+v", d.Protocol, back, s)
		}
		if got := fromSettings(back); got.URL != d.URL || got.Protocol != d.Protocol || got.Passphrase != d.Passphrase ||
			got.Key != d.Key || got.VideoCodec != d.VideoCodec || got.LatencyMs != d.LatencyMs {
			t.Errorf("%s: back to a destination: %+v", d.Protocol, got)
		}
	}
}

// engine.env's agreement rules: protocol ↔ scheme, SRT_URL empty for the other protocols.
func TestSettingsOutputAgreement(t *testing.T) {
	for name, mut := range map[string]func(*Settings){
		"protocol srtla with srt://":   func(s *Settings) { s.Protocol = "srtla" },
		"rtmp with SRT_URL":            func(s *Settings) { s.Protocol, s.OutputURL = "rtmp", "rtmp://h/app" },
		"srt with OUTPUT_URL":          func(s *Settings) { s.Protocol, s.OutputURL = "srt", "rtmp://h/app" },
		"rtmps with rtmp://":           func(s *Settings) { s.URL, s.Protocol, s.OutputURL = "", "rtmps", "rtmp://h/app" },
		"unknown protocol":             func(s *Settings) { s.Protocol = "whip" },
		"output on with no OUTPUT_URL": func(s *Settings) { s.URL, s.Protocol = "", "rist" },
		"RIST latency":                 func(s *Settings) { s.OutputLatencyMs = 10 },
		"user without password":        func(s *Settings) { s.OutputUser = "u" },
		"simple with a psk": func(s *Settings) {
			s.URL, s.Protocol, s.OutputURL, s.RISTProfile, s.OutputPassphrase = "", "rist", "rist://h:5000", "simple", "abcdefghij"
		},
		"simple odd port": func(s *Settings) {
			s.URL, s.Protocol, s.OutputURL, s.RISTProfile = "", "rist", "rist://h:5001", "simple"
		},
	} {
		s := valid()
		mut(&s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s := valid()
	s.URL, s.StreamID, s.Protocol, s.OutputURL = "", "", "udp", "udp://239.0.0.1:1234"
	if err := s.Validate(); err != nil || !s.HasDestination() {
		t.Errorf("udp: %v", err)
	}
}

// The migration the first time (OUTPUTS.md §9.1).
func TestMigrate(t *testing.T) {
	linked := valid()
	ds := Migrate(linked, true)
	if d, ok := ds.ActiveDestination(); !ok || d.ID != CloudID || !d.Managed || d.Template != "eirl" || d.VideoCodec != "h265" ||
		d.StreamID != linked.StreamID || ds.ManagedBy() != "eirl" {
		t.Errorf("linked: %+v", ds)
	}
	srtla := valid()
	srtla.URL, srtla.LatencyMs = "srtla://relay.example:5001", 2000
	ds = Migrate(srtla, false)
	if d, ok := ds.ActiveDestination(); !ok || !ValidID(d.ID) || d.ID == CloudID || d.Template != "custom" || d.Name != "SRTLA" ||
		d.Protocol != "srtla" || d.VideoCodec != "h264" || d.LatencyMs != 2000 || ds.ManagedBy() != "local" {
		t.Errorf("local srtla: %+v", ds)
	}
	ds = Migrate(valid(), false)
	if d, _ := ds.ActiveDestination(); d.Name != "SRT" || d.Protocol != "srt" {
		t.Errorf("local srt: %+v", d)
	}
	// What engine.env holds, flattened again, is what it was (nothing to apply after migrating)
	if got := ds.Flatten(valid()); got.URL != valid().URL || got.StreamID != valid().StreamID || got.LatencyMs != valid().LatencyMs {
		t.Errorf("flatten after migrating: %+v", got)
	}
	if ds := Migrate(Defaults(), true); len(ds.Destinations) != 0 || ds.Active != "" {
		t.Errorf("empty: %+v", ds)
	}
	bad := valid()
	bad.URL = "srt://h:1?streamid=x" // saved by hand: not a destination it could stream to
	if ds := Migrate(bad, false); len(ds.Destinations) != 0 {
		t.Errorf("invalid: %+v", ds)
	}
}

func TestDestinationsStore(t *testing.T) {
	dir := t.TempDir()
	st := &Store{DefaultPath: filepath.Join(dir, "default"), AppPath: filepath.Join(dir, "engine.env")}
	os.WriteFile(st.DefaultPath, []byte("SRT_URL=srt://relay.example:5000\nSRT_STREAMID=publish:"+key+"\n"), 0o644)
	// Load does not write the migration; Destinations does, 0600
	if s, err := st.Load(); err != nil || s.ManagedBy != "local" {
		t.Fatalf("load: %+v %v", s, err)
	}
	if _, err := os.Stat(st.DestPath()); !os.IsNotExist(err) {
		t.Fatal("Load wrote destinations.json")
	}
	ds, _, err := st.Destinations()
	if err != nil || len(ds.Destinations) != 1 || ds.Active == "" {
		t.Fatalf("migration: %+v %v", ds, err)
	}
	if fi, _ := os.Stat(st.DestPath()); fi.Mode().Perm() != 0o600 {
		t.Errorf("destinations.json %v, want 0600", fi.Mode().Perm())
	}
	again, _, _ := st.Destinations()
	if again.Active != ds.Active {
		t.Errorf("ids must be stable once saved: %s %s", again.Active, ds.Active)
	}
	// Activating another one renders engine.env with it
	tw, _ := TemplateByID("twitch")
	rt := NewDestination(tw, "rtmp")
	rt.ID, rt.Key = ds.NewID(), "live_123456789_abc"
	_, s, err := st.UpdateDestinations(func(ds *Destinations, _ *Settings) error {
		ds.Put(rt)
		ds.Active = rt.ID
		return nil
	})
	if err != nil || s.URL != "" || s.OutputURL != "rtmp://live.twitch.tv/app" || s.Key != rt.Key || s.Protocol != "rtmp" {
		t.Fatalf("activate rtmp: %+v %v", s, err)
	}
	env, _ := ReadEnv(st.AppPath)
	if env["SRT_URL"] != "" || env["SRT_STREAMID"] != "" || env["OUTPUT_KEY"] != rt.Key || env["EIRL_MANAGED_BY"] != "local" {
		t.Errorf("engine.env: %v", env)
	}
	// Store.Update (the global keys) cannot change the destination's
	if s, _ := st.Update(func(s Settings) (Settings, error) { s.Output, s.OutputURL = "off", "rtmp://evil/app"; return s, nil }); s.OutputURL != rt.URL || s.Output != "off" {
		t.Errorf("Update must keep the destination: %+v", s)
	}
	// An invalid list is not saved and nothing changes
	before, _ := os.ReadFile(st.AppPath)
	_, _, err = st.UpdateDestinations(func(ds *Destinations, _ *Settings) error {
		for i := 0; i < MaxDestinations; i++ {
			d := rt
			d.ID = ds.NewID()
			ds.Put(d)
		}
		return nil
	})
	if after, _ := os.ReadFile(st.AppPath); err == nil || string(after) != string(before) {
		t.Errorf("more than %d: %v", MaxDestinations, err)
	}
	// Output on with nothing active fails
	if _, _, err := st.UpdateDestinations(func(ds *Destinations, s *Settings) error { ds.Active, s.Output = "", "on"; return nil }); err == nil {
		t.Error("output on without a destination")
	}
	// A corrupt or foreign file is an error (never silently replaced)
	os.WriteFile(st.DestPath(), []byte(`{"v":1,"destinations":[{"id":"x"}]}`), 0o600)
	if _, _, err := st.Destinations(); err == nil {
		t.Error("invalid destinations.json accepted")
	}
	// The file's shape
	var raw map[string]any
	b, _ := json.Marshal(Destinations{V: 1, Active: rt.ID, Destinations: []Destination{rt}})
	json.Unmarshal(b, &raw)
	if raw["v"] != 1.0 || raw["active"] != rt.ID {
		t.Errorf("shape: %s", b)
	}
}

func TestIDs(t *testing.T) {
	ds := Empty()
	for i := 0; i < 50; i++ {
		id := ds.NewID()
		if !ValidID(id) || id == CloudID {
			t.Fatalf("id %q", id)
		}
		ds.Put(Destination{ID: id})
	}
	for _, bad := range []string{"", "d-123", "d-0000ABCD", "d-0000abcd0", "eirl2", "../x"} {
		if ValidID(bad) {
			t.Errorf("%q valid", bad)
		}
	}
	// Only the cloud's is eirl and managed
	d := custom("srt")
	d.URL = "srt://h:1"
	d.Managed = true
	if (Destinations{V: 1, Destinations: []Destination{d}}).Validate() == nil {
		t.Error("a local managed destination")
	}
}

// The patch: protocol from the scheme, fields cleared on a protocol change, the new-server rule for every secret,
// a template's server per protocol.
func TestDestinationPatch(t *testing.T) {
	cur := custom("srt")
	cur.URL, cur.StreamID, cur.Passphrase = "srt://relay.example:5000", "publish:"+key, "abcdefghijk"
	// To RTMP on another server: the SRT secrets go (unused), so nothing to type again
	d, err := patch(t, cur, `{"url":"rtmp://live.example/app","key":"live_xyz"}`)
	if err != nil || d.Protocol != "rtmp" || d.StreamID != "" || d.Passphrase != "" || d.LatencyMs != 0 || d.Key != "live_xyz" {
		t.Fatalf("srt → rtmp: %+v %v", d, err)
	}
	// A value for a field the protocol does not use is an error
	if _, err := patch(t, cur, `{"url":"rtmp://live.example/app","streamId":"publish:x"}`); err == nil {
		t.Error("streamId with rtmp")
	}
	// Protocol and scheme must agree
	if _, err := patch(t, cur, `{"protocol":"rist","url":"rtmp://h/app"}`); err == nil {
		t.Error("protocol rist with rtmp://")
	}
	// The key is a secret too: changing server asks for it again
	if _, err := patch(t, d, `{"url":"rtmp://evil.example/app"}`); err == nil || err.(FieldErrors)["key"] == "" {
		t.Errorf("new server must ask for the key: %v", err)
	}
	if d2, err := patch(t, d, `{"url":"rtmps://live.example:443/app"}`); err != nil || d2.Key != "live_xyz" {
		t.Errorf("same host over RTMPS keeps the key: %+v %v", d2, err)
	}
	// To RIST: buffer 1000 by default and the main profile
	r, err := patch(t, d, `{"url":"rist://10.0.0.2:5000","key":null}`)
	if err != nil || r.LatencyMs != 1000 || r.RISTProfile != "main" || r.Key != "" {
		t.Errorf("→ rist: %+v %v", r, err)
	}
	// Twitch over RTMPS: the template's server for the new protocol
	tw, _ := TemplateByID("twitch")
	td := NewDestination(tw, "rtmp")
	td.ID = "d-0000abcd"
	if d, err := patch(t, td, `{"protocol":"rtmps"}`); err != nil || d.URL != "rtmps://live.twitch.tv:443/app" {
		t.Errorf("twitch rtmps: %+v %v", d, err)
	}
	if _, err := patch(t, td, `{"videoCodec":"h265"}`); err == nil {
		t.Error("twitch with H.265")
	}
	if _, err := patch(t, td, `{"name":"   "}`); err == nil {
		t.Error("blank name")
	}
}

func TestDiffEnv(t *testing.T) {
	base := map[string]string{"SRT_URL": "srt://h:1", "OUTPUT": "on", "EDID_PREFERRED": "1080p30", "OUTPUT_VCODEC": "h264"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range base {
			m[a] = b
		}
		m[k] = v
		return m
	}
	for _, c := range []struct {
		name                string
		after               map[string]string
		changed, onlyOutput bool
	}{
		{"same", with("SRT_URL", "srt://h:1"), false, false},
		{"only OUTPUT", with("OUTPUT", "off"), false, false},
		{"codec", with("OUTPUT_VCODEC", "h265"), true, true},
		{"new key", with("OUTPUT_KEY", "x"), true, true},
		{"managed by", with("EIRL_MANAGED_BY", "eirl"), true, true},
		{"edid", with("EDID_PREFERRED", "720p60"), true, false},
	} {
		if ch, oo := DiffEnv(base, c.after); ch != c.changed || oo != c.onlyOutput {
			t.Errorf("%s: %v %v", c.name, ch, oo)
		}
	}
	if _, oo := DiffEnv(nil, base); oo {
		t.Error("without a reading before, never hot")
	}
}
