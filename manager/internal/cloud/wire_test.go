package cloud

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
)

const testKey = "AbCdEfGh0123456789_-AbCdEfGh0123"

func wireJSON(t *testing.T, mut func(m map[string]any)) json.RawMessage {
	t.Helper()
	m := map[string]any{
		"destination": map[string]any{"url": "srt://eu1.relay.enhancedirl.com:5000", "streamId": "publish:" + testKey, "latencyMs": 500},
		"video":       map[string]any{"bitrateKbps": 6000, "adaptive": true, "minBitrateKbps": 1500},
		"audio":       map[string]any{"bitrateKbps": 160},
		"input":       map[string]any{"edidPreferred": "1080p50"},
		"output":      "on",
		"uplinks":     map[string]any{"disabled": []string{"usb1", "usb0", "usb1"}},
		"accessPoint": "rescue",
	}
	if mut != nil {
		mut(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestApplyWireValid(t *testing.T) {
	w, fe := ParseWireConfig(wireJSON(t, nil))
	if fe != nil {
		t.Fatalf("valid config rejected: %v", fe)
	}
	cur := config.Defaults()
	cur.Passphrase = "secretpassphrase" // the cloud does not use a passphrase: it is removed
	s, disabled, fe := ApplyWire(cur, w)
	if fe != nil {
		t.Fatalf("ApplyWire: %v", fe)
	}
	want := config.Settings{ManagedBy: "eirl", URL: "srt://eu1.relay.enhancedirl.com:5000", Mode: "caller", LatencyMs: 500,
		StreamID: "publish:" + testKey, VideoKbps: 6000, AudioKbps: 160, Output: "on", ABR: true, VideoMinKbps: 1500, EDIDPreferred: "1080p50", Protocol: "srt",
		// The input (SOURCES.md) does not travel in wire v1: the device's is kept
		Source: "hdmi", VideoDevice: "auto", VideoFormat: "auto", VideoMode: "auto", AudioSource: "auto",
		// Nor does the local recording (RECORDING.md §6.1): the device's is kept
		RecMode: "off", RecQuality: "high"}
	if s != want {
		t.Errorf("ApplyWire:\n got %+v\nwant %+v", s, want)
	}
	if strings.Join(disabled, ",") != "usb1,usb0" {
		t.Errorf("normalised uplinks: %v", disabled)
	}
	if w.AccessPoint != "rescue" {
		t.Errorf("accessPoint: %q", w.AccessPoint)
	}
}

func TestApplyWireVariants(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(m map[string]any)
		check func(t *testing.T, s config.Settings, fe config.FieldErrors)
	}{
		{"dashboard publish URL (srtla with query)", func(m map[string]any) {
			m["destination"] = map[string]any{"url": "srtla://eu1.relay.enhancedirl.com:5002?streamid=publish:" + testKey, "streamId": "", "latencyMs": 2000}
		}, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe != nil || s.URL != "srtla://eu1.relay.enhancedirl.com:5002" || s.StreamID != "publish:"+testKey || config.Transport(s.URL) != "srtla" || s.LatencyMs != 2000 {
				t.Errorf("%+v %v", s, fe)
			}
		}},
		{"URL streamid differs from the field", func(m map[string]any) {
			m["destination"] = map[string]any{"url": "srt://eu1.relay.enhancedirl.com:5000?streamid=publish:other", "streamId": "publish:" + testKey, "latencyMs": 500}
		}, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["destination.streamId"] == "" {
				t.Errorf("expected a conflict: %v", fe)
			}
		}},
		// What the backend sends when the key is revoked, switched to RTMP or not chosen yet: accepted,
		// URL, key and passphrase are removed and it stays in REHEARSAL (rejecting it kept the revoked key).
		{"no destination while streaming", func(m map[string]any) { m["destination"] = nil }, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe != nil || s.URL != "" || s.StreamID != "" || s.Passphrase != "" || s.Output != "off" {
				t.Errorf("no destination with output=on: %+v %v", s, fe)
			}
		}},
		// Without the `destination` field (no key chosen in the app yet): the device's destination is
		// NOT touched (G1 finding: with null the Jetson's local destination was deleted).
		{"destination absent and device without destination: rehearsal", func(m map[string]any) { delete(m, "destination") }, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe != nil || s.URL != "" || s.Output != "off" {
				t.Errorf("%+v %v", s, fe)
			}
		}},
		{"no destination in rehearsal", func(m map[string]any) { m["destination"] = nil; m["output"] = "off" }, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe != nil || s.URL != "" || s.StreamID != "" || s.Output != "off" {
				t.Errorf("%+v %v", s, fe)
			}
		}},
		{"bitrate out of range", func(m map[string]any) {
			m["video"] = map[string]any{"bitrateKbps": 100, "adaptive": false, "minBitrateKbps": 1000}
		}, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["video.bitrateKbps"] == "" {
				t.Errorf("%v", fe)
			}
		}},
		{"minimum above maximum with ABR", func(m map[string]any) {
			m["video"] = map[string]any{"bitrateKbps": 3000, "adaptive": true, "minBitrateKbps": 4000}
		}, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["video.minBitrateKbps"] == "" {
				t.Errorf("%v", fe)
			}
		}},
		{"unsupported audio", func(m map[string]any) { m["audio"] = map[string]any{"bitrateKbps": 100} }, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["audio.bitrateKbps"] == "" {
				t.Errorf("%v", fe)
			}
		}},
		{"unknown EDID", func(m map[string]any) { m["input"] = map[string]any{"edidPreferred": "1080p60"} }, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["input.edidPreferred"] == "" {
				t.Errorf("%v", fe)
			}
		}},
		{"latency out of range", func(m map[string]any) {
			m["destination"] = map[string]any{"url": "srt://eu1.relay.enhancedirl.com:5000", "streamId": "publish:" + testKey, "latencyMs": 9000}
		}, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["destination.latencyMs"] == "" {
				t.Errorf("%v", fe)
			}
		}},
		{"injection in the streamid", func(m map[string]any) {
			m["destination"] = map[string]any{"url": "srt://eu1.relay.enhancedirl.com:5000", "streamId": "publish:x&mode=listener", "latencyMs": 500}
		}, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["destination.streamId"] == "" {
				t.Errorf("%v", fe)
			}
		}},
		{"uplink with an invalid name", func(m map[string]any) { m["uplinks"] = map[string]any{"disabled": []string{"eth0;rm"}} }, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["uplinks.disabled"] == "" {
				t.Errorf("%v", fe)
			}
		}},
		{"unknown field", func(m map[string]any) { m["foo"] = 1 }, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["foo"] == "" {
				t.Errorf("%v", fe)
			}
		}},
		{"unknown inner field", func(m map[string]any) {
			m["video"] = map[string]any{"bitrateKbps": 6000, "adaptive": false, "minBitrateKbps": 1000, "x": 1}
		}, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["video"] == "" {
				t.Errorf("%v", fe)
			}
		}},
		{"missing section", func(m map[string]any) { delete(m, "audio") }, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["audio"] != "missing" {
				t.Errorf("%v", fe)
			}
		}},
		{"missing field", func(m map[string]any) { m["video"] = map[string]any{"bitrateKbps": 6000, "adaptive": false} }, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["video.minBitrateKbps"] != "missing" {
				t.Errorf("%v", fe)
			}
		}},
		{"invalid accessPoint", func(m map[string]any) { m["accessPoint"] = "never" }, func(t *testing.T, s config.Settings, fe config.FieldErrors) {
			if fe["accessPoint"] == "" {
				t.Errorf("%v", fe)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, fe := ParseWireConfig(wireJSON(t, c.mut))
			var s config.Settings
			if fe == nil {
				s, _, fe = ApplyWire(config.Defaults(), w)
			}
			c.check(t, s, fe)
		})
	}
}

// D9: when linked, only EnhancedIRL's ingest relays: <zone>.relay.enhancedirl.com (production: the relays table of
// prod holds eu1, us1 and as1) and relay.dev.enhancedirl.com (development), plus future zones of either.
func TestDestinationAllowedD9(t *testing.T) {
	for host, want := range map[string]bool{
		"eu1.relay.enhancedirl.com": true, "us1.relay.enhancedirl.com": true, "as1.relay.enhancedirl.com": true,
		"EU1.Relay.EnhancedIRL.com": true, "relay.dev.enhancedirl.com": true, "sa1.relay.enhancedirl.com": true,
		"eu-2.relay.enhancedirl.com": true, "eu1.relay.dev.enhancedirl.com": true,
		// Other names under the domain are not relays: the API, the app, the docs, the landing, the old name
		"api.enhancedirl.com": false, "app.enhancedirl.com": false, "api.dev.enhancedirl.com": false,
		"docs.enhancedirl.com": false, "links.enhancedirl.com": false, "relay-eu.enhancedirl.com": false,
		"relay.enhancedirl.com": false, "dev.enhancedirl.com": false,
		// One zone label, and a valid one
		"a.eu1.relay.enhancedirl.com": false, ".relay.enhancedirl.com": false, "-eu.relay.enhancedirl.com": false,
		"eu-.relay.enhancedirl.com": false, "eu_1.relay.enhancedirl.com": false, "eu1.relay.enhancedirl.com.": false,
		strings.Repeat("a", 64) + ".relay.enhancedirl.com": false, strings.Repeat("a", 63) + ".relay.enhancedirl.com": true,
		"x.y.relay.dev.enhancedirl.com": false,
		"enhancedirl.com":               false, "evil-enhancedirl.com": false, "enhancedirl.com.evil.com": false,
		"eu1.relay.enhancedirl.com.evil.com": false, "evil.com": false, "203.0.113.5": false, "": false,
		"xenhancedirl.com": false, "eu1relay.enhancedirl.com": false, "::1": false,
	} {
		if got := DestinationAllowed(host); got != want {
			t.Errorf("DestinationAllowed(%q) = %v", host, got)
		}
	}
	for _, url := range []string{"srt://evil.example.com:5000", "srt://203.0.113.5:5000", "srt://:9000", "srtla://enhancedirl.com.evil.com:5002",
		"srt://api.enhancedirl.com:5000", "srtla://docs.enhancedirl.com:5002"} {
		w, fe := ParseWireConfig(wireJSON(t, func(m map[string]any) {
			m["destination"] = map[string]any{"url": url, "streamId": "publish:" + testKey, "latencyMs": 500}
		}))
		if fe != nil {
			t.Fatalf("%s: %v", url, fe)
		}
		if _, _, fe := ApplyWire(config.Defaults(), w); fe["destination"] != ErrDestinationNotAllowed {
			t.Errorf("%s: expected destination_not_allowed, %v", url, fe)
		}
	}
}

func TestReportedHasNoSecrets(t *testing.T) {
	s := config.Defaults()
	s.URL, s.StreamID, s.Passphrase, s.LatencyMs = "srtla://eu1.relay.enhancedirl.com:5002", "publish:"+testKey, "secretpassphrase", 2000
	ds := config.Migrate(s, true) // the cloud's, active
	r := Reported(ds.Flatten(s), ds, []string{"usb1", "eth0"})
	b, _ := json.Marshal(r)
	if bytes.Contains(b, []byte(testKey)) || bytes.Contains(b, []byte("secretpassphrase")) {
		t.Fatalf("secrets in the report: %s", b)
	}
	d := r.Destination
	if d == nil || d.Kind != "eirl" || d.Protocol != "srtla" || d.Codec != "h265" || d.Host != "eu1.relay.enhancedirl.com" ||
		d.Port != 5002 || d.Transport != "srtla" || d.LatencyMs != 2000 || !d.StreamIDSet {
		t.Errorf("reported destination: %+v", d)
	}
	if strings.Join(r.Uplinks.Disabled, ",") != "eth0,usb1" {
		t.Errorf("uplinks: %v", r.Uplinks.Disabled)
	}
	if Reported(config.Defaults(), config.Empty(), nil).Destination != nil {
		t.Error("without an active destination, destination must be null")
	}
	if b, _ := json.Marshal(Reported(config.Defaults(), config.Empty(), nil)); !bytes.Contains(b, []byte(`"disabled":[]`)) {
		t.Errorf("disabled must be [] and not null: %s", b)
	}
	if a := r.ActiveDestination; a == nil || a.Kind != "eirl" || a.Protocol != "srtla" || a.Codec != "h265" {
		t.Errorf("active destination: %+v", a)
	}
	// A local destination: destination null (an older cloud refuses the whole report for anything but srt/srtla
	// with host, port and latency) and activeDestination with only its kind, protocol and codec (OUTPUTS.md §10),
	// never where it goes
	local := config.Defaults()
	local.URL, local.StreamID = "srt://my.own.server.example:9000", "publish:"+testKey
	lds := config.Migrate(local, false)
	b, _ = json.Marshal(Reported(lds.Flatten(local), lds, nil))
	if !bytes.Contains(b, []byte(`"destination":null`)) ||
		!bytes.Contains(b, []byte(`"activeDestination":{"kind":"local","protocol":"srt","codec":"h264"}`)) ||
		bytes.Contains(b, []byte("my.own.server")) {
		t.Errorf("local destination: %s", b)
	}
}

// The cloud's destination in the saved list (OUTPUTS.md §9.1, §10): created and made active the first time, updated
// later without changing the active one, removed with null; its codec from the wire.
func TestApplyWireDestinations(t *testing.T) {
	cur := config.Defaults()
	local := cur
	local.URL = "srt://my.own.server.example:9000"
	ds := config.Migrate(local, false)
	localID := ds.Active
	cur = ds.Flatten(local)

	// The first time: created (H.265 by default) and active
	w, fe := ParseWireConfig(wireJSON(t, nil))
	if fe != nil {
		t.Fatal(fe)
	}
	ds2, s, dis, fe := ApplyWireDestinations(ds, cur, w)
	if fe != nil {
		t.Fatalf("first config.set: %v", fe)
	}
	e, ok := ds2.Get(config.CloudID)
	if !ok || ds2.Active != config.CloudID || !e.Managed || e.Template != "eirl" || e.VideoCodec != "h265" || e.Protocol != "srt" ||
		e.VideoKbps != 6000 || e.AudioKbps != 160 || e.VideoMinKbps != 1500 || len(ds2.Destinations) != 2 {
		t.Fatalf("created: %+v active=%s", e, ds2.Active)
	}
	if s.URL != "srt://eu1.relay.enhancedirl.com:5000" || s.ManagedBy != "eirl" || s.Output != "on" || s.VideoCodec != "h265" ||
		s.EDIDPreferred != "1080p50" || strings.Join(dis, ",") != "usb1,usb0" {
		t.Errorf("flattened: %+v", s)
	}
	// The owner activates their own one; an update from the cloud does not take it back, and streams to the active one
	ds2.Active = localID
	s = ds2.Flatten(s)
	w, _ = ParseWireConfig(wireJSON(t, func(m map[string]any) {
		m["destination"] = map[string]any{"url": "srtla://eu1.relay.enhancedirl.com:5001", "streamId": "publish:" + testKey,
			"latencyMs": 2000, "videoCodec": "h264"}
		m["video"] = map[string]any{"bitrateKbps": 4000, "adaptive": true, "minBitrateKbps": 1000}
	}))
	ds3, s3, _, fe := ApplyWireDestinations(ds2, s, w)
	e, _ = ds3.Get(config.CloudID)
	if fe != nil || ds3.Active != localID || e.Protocol != "srtla" || e.VideoCodec != "h264" || e.VideoKbps != 4000 ||
		s3.URL != "srt://my.own.server.example:9000" || s3.ManagedBy != "local" || s3.Output != "on" || s3.VideoKbps != 6000 {
		t.Errorf("update: %v %+v %+v", fe, e, s3)
	}
	// null: removed; the active local one stays
	w, _ = ParseWireConfig(wireJSON(t, func(m map[string]any) { m["destination"] = nil }))
	ds4, s4, _, fe := ApplyWireDestinations(ds3, s3, w)
	if _, ok := ds4.Get(config.CloudID); fe != nil || ok || ds4.Active != localID || s4.URL != s3.URL || s4.Output != "on" {
		t.Errorf("null with a local one active: %v %+v", fe, ds4)
	}
	// null with the cloud's active: nothing active and not streaming
	ds3.Active = config.CloudID
	ds5, s5, _, fe := ApplyWireDestinations(ds3, ds3.Flatten(s3), w)
	if fe != nil || ds5.Active != "" || s5.URL != "" || s5.Output != "off" || s5.ManagedBy != "local" {
		t.Errorf("null with the cloud's active: %v %+v %+v", fe, ds5, s5)
	}
	// Absent: nothing changes but the bitrates of the cloud's
	w, _ = ParseWireConfig(wireJSON(t, func(m map[string]any) {
		delete(m, "destination")
		m["video"] = map[string]any{"bitrateKbps": 3000, "adaptive": false, "minBitrateKbps": 1000}
	}))
	ds6, _, _, fe := ApplyWireDestinations(ds3, ds3.Flatten(s3), w)
	if e, _ := ds6.Get(config.CloudID); fe != nil || e.VideoKbps != 3000 || e.URL != "srtla://eu1.relay.enhancedirl.com:5001" {
		t.Errorf("absent: %v %+v", fe, e)
	}
	// A codec the device does not know
	if _, fe := ParseWireConfig(wireJSON(t, func(m map[string]any) {
		m["destination"] = map[string]any{"url": "srt://eu1.relay.enhancedirl.com:5000", "streamId": "", "latencyMs": 500, "videoCodec": "av1"}
	})); fe["destination.videoCodec"] == "" {
		t.Errorf("videoCodec av1: %v", fe)
	}
	// D9 still applies
	w, _ = ParseWireConfig(wireJSON(t, func(m map[string]any) {
		m["destination"] = map[string]any{"url": "srt://evil.example.com:5000", "streamId": "", "latencyMs": 500}
	}))
	if _, _, _, fe := ApplyWireDestinations(ds, cur, w); fe["destination"] != ErrDestinationNotAllowed {
		t.Errorf("D9: %v", fe)
	}
	// No destination at all and output on: stays off
	w, _ = ParseWireConfig(wireJSON(t, func(m map[string]any) { delete(m, "destination") }))
	if ds7, s7, _, fe := ApplyWireDestinations(config.Empty(), config.Defaults(), w); fe != nil || s7.Output != "off" || ds7.Active != "" {
		t.Errorf("no destination: %v %+v", fe, s7)
	}
}

// What other branches add to input (multiple sources) passes through unfiltered; to signal, only kind
// and measuredFps; new phases and audio states, as they are.
func TestStatusExtrasPassThrough(t *testing.T) {
	st := EncoderStatus{
		Phase: PhaseWaitingDevice,
		Input: StatusInput{EDIDPreferred: "1080p30", Extra: map[string]any{"source": "usb", "device": "auto", "edidPreferred": "overwritten"}},
		Signal: StatusSignal{Present: true, Width: 1920, Height: 1080, FPS: 59.94, Supported: true,
			Extra: map[string]any{"kind": "usb", "measuredFps": 59.9}},
		Audio: StatusAudio{State: "usb"},
	}
	st.Normalize()
	b, _ := json.Marshal(st)
	for _, want := range []string{`"phase":"waiting_device"`, `"source":"usb"`, `"device":"auto"`, `"edidPreferred":"1080p30"`,
		`"edidActive":null`, `"kind":"usb"`, `"measuredFps":59.9`, `"fps":59.94`, `"state":"usb"`, `"uplinks":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	st.Audio.State = "something-odd"
	st.Normalize()
	if st.Audio.State != "unknown" {
		t.Errorf("unknown audio: %q", st.Audio.State)
	}
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, 320, 180)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReadThumbnail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "thumb.jpg")
	now := time.Now()
	j := testJPEG(t)
	os.WriteFile(p, j, 0o644)
	th, ok := ReadThumbnail(p, 15*time.Second, now)
	if !ok || th.Width != 320 || th.Height != 180 || !bytes.Equal(th.JPEG, j) {
		t.Fatalf("valid thumbnail: ok=%v %dx%d", ok, th.Width, th.Height)
	}
	if _, ok := ReadThumbnail(p, 15*time.Second, now.Add(time.Minute)); ok {
		t.Error("a stale thumbnail is not sent")
	}
	os.WriteFile(p, j[:len(j)/2], 0o644) // multifilesink half-way through writing
	if _, ok := ReadThumbnail(p, 15*time.Second, now); ok {
		t.Error("a half-written JPEG is not sent")
	}
	os.WriteFile(p, append(append([]byte{0xff, 0xd8}, make([]byte, MaxThumbnail)...), 0xff, 0xd9), 0o644)
	if _, ok := ReadThumbnail(p, 15*time.Second, now); ok {
		t.Error("a thumbnail over 64 KB is not sent")
	}
	if _, ok := ReadThumbnail(filepath.Join(dir, "no"), 15*time.Second, now); ok {
		t.Error("no file")
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{"abcd2345": "ABCD-2345", "ABCD-2345": "ABCD-2345", "ABC": "", "ABCD-234!": "", "abcd23456": ""} {
		if got := FormatCode(in); got != want {
			t.Errorf("FormatCode(%q) = %q", in, got)
		}
	}
	for raw, ok := range map[string]bool{
		"https://api.enhancedirl.com": true, "https://api.dev.enhancedirl.com/": true, "http://127.0.0.1:3000": true,
		"http://localhost:3000": true, "http://api.enhancedirl.com": false, "ftp://x": false, "https://u:p@x.com": false,
		"https://x.com?a=1": false, "": false,
	} {
		if err := ValidateBase(raw, false); (err == nil) != ok {
			t.Errorf("ValidateBase(%q) = %v", raw, err)
		}
	}
	if ValidateBase("http://192.168.1.10:3000", true) != nil {
		t.Error("the simulator accepts http")
	}
	if wsURL("https://api.dev.enhancedirl.com/") != "wss://api.dev.enhancedirl.com/encoders/connect" || wsURL("http://127.0.0.1:1") != "ws://127.0.0.1:1/encoders/connect" {
		t.Error("wsURL")
	}
	for i := 0; i < 200; i++ {
		for attempt, base := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
			if d := backoff(attempt, time.Second, time.Minute); d < base*7/10 || d > base*13/10 {
				t.Fatalf("backoff(%d) = %v outside ±30 %% of %v", attempt, d, base)
			}
		}
		if d := backoff(20, time.Second, time.Minute); d < 42*time.Second || d > 78*time.Second {
			t.Fatalf("capped backoff: %v", d)
		}
	}
	if got := cleanText("a\x00b\nc\u0085d", 64); got != "abcd" {
		t.Errorf("cleanText: %q", got)
	}
	if got := cleanText(strings.Repeat("é", 40), 63); len(got) > 63 || !strings.HasPrefix(got, "éé") {
		t.Errorf("cleanText cuts without breaking UTF-8: %q (%d)", got, len(got))
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "machine-id"), []byte("0123456789abcdef0123456789abcdef\n"), 0o644)
	if id := DeviceID(filepath.Join(dir, "machine-id")); !reHardwareID.MatchString(id) || len(id) != 16 {
		t.Errorf("DeviceID: %q", id)
	}
}

func TestStateFile(t *testing.T) {
	f := &StateFile{Path: filepath.Join(t.TempDir(), "cloud.json")}
	if _, ok, err := f.Load(); ok || err != nil {
		t.Fatalf("no file: ok=%v err=%v", ok, err)
	}
	st := State{EncoderID: "enc-1", Token: "tok_0123456789abcdef", APIBase: "https://api.enhancedirl.com", AccountLabel: "u•••@example.com", AppliedVersion: 3}
	if err := f.Save(st); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(f.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("permissions %v, must be 0600 (it holds the token)", fi.Mode().Perm())
	}
	got, ok, err := f.Load()
	if !ok || err != nil || got != st {
		t.Errorf("Load: %+v %v %v", got, ok, err)
	}
	os.WriteFile(f.Path, []byte(`{"token":"x"}`), 0o600)
	if _, ok, err := f.Load(); ok || err == nil {
		t.Error("incomplete state: not linked and an error")
	}
	if err := f.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := f.Clear(); err != nil {
		t.Error("deleting twice is not an error")
	}
}

// G1 finding (12-oct): a config.set without a key chosen in the app must NOT delete the destination the
// owner configured locally (even if it is not EnhancedIRL's: it is theirs, D9 applies to what the cloud sends).
func TestApplyWireKeepsDeviceDestination(t *testing.T) {
	cur := config.Defaults()
	cur.URL, cur.StreamID, cur.Passphrase, cur.LatencyMs, cur.Output = "srtla://eu1.relay.enhancedirl.com:5001", "publish:"+testKey, "", 2000, "on"
	w, fe := ParseWireConfig(wireJSON(t, func(m map[string]any) {
		delete(m, "destination")
		m["video"] = map[string]any{"bitrateKbps": 5000, "adaptive": false, "minBitrateKbps": 1000}
	}))
	if fe != nil || !w.KeepDestination {
		t.Fatalf("parse: %+v %v", w, fe)
	}
	s, _, fe := ApplyWire(cur, w)
	if fe != nil || s.URL != cur.URL || s.StreamID != cur.StreamID || s.LatencyMs != 2000 || s.Output != "on" || s.VideoKbps != 5000 || s.ManagedBy != "eirl" {
		t.Errorf("%+v %v", s, fe)
	}
}

// The reasons of RTMPS (OUTPUTS.md §8) pass; unknown ones are internal
func TestNormalizeOutputReasons(t *testing.T) {
	for in, want := range map[string]string{"tls_failed": "tls_failed", "clock_not_synced": "clock_not_synced", "brand_new": "internal"} {
		st := EncoderStatus{OutputState: StatusOutput{State: "error", Reason: in}}
		st.Normalize()
		if st.OutputState.Reason != want {
			t.Errorf("%s → %s", in, st.OutputState.Reason)
		}
	}
}
