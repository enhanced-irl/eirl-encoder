package netcfg

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func cam(id string) Camera {
	return Camera{ID: id, Name: "Zoë's phone", Key: "AbCdEfGhIjKlMnOpQrSt", CreatedAt: time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)}
}

func TestParseCameras(t *testing.T) {
	c := Cameras{Cameras: []Camera{cam("cam2"), cam("cam1")}}
	got, err := ParseCameras(c.Render())
	if err != nil || len(got.Cameras) != 2 || got.Cameras[0].ID != "cam1" {
		t.Fatalf("round trip: %v %+v", err, got)
	}
	if got, err := ParseCameras(nil); err != nil || len(got.Cameras) != 0 {
		t.Fatalf("empty: %v %+v", err, got)
	}
	bad := []string{
		`{"v":1,"cameras":[{"id":"cam9","name":"x","key":"AbCdEfGhIjKlMnOpQrSt"}]}`,                   // id out of range
		`{"v":1,"cameras":[{"id":"cam1","name":"x","key":"short"}]}`,                                  // key
		`{"v":1,"cameras":[{"id":"cam1","name":"<b>","key":"AbCdEfGhIjKlMnOpQrSt"}]}`,                 // name
		`{"v":1,"cameras":[{"id":"cam1","name":" x","key":"AbCdEfGhIjKlMnOpQrSt"}]}`,                  // surrounding spaces
		`{"v":1,"cameras":[{"id":"cam1","name":"x","key":"AbCdEfGhIjKlMnOpQrSt","runOnReady":"sh"}]}`, // unknown field
		`{"v":2,"cameras":[]}`,
		`{"v":1,"cameras":[]} {"v":1}`,
	}
	for _, b := range bad {
		if _, err := ParseCameras([]byte(b)); err == nil {
			t.Errorf("should fail: %s", b)
		}
	}
	dup := Cameras{Cameras: []Camera{cam("cam1"), cam("cam1")}}
	if _, err := ParseCameras(dup.Render()); err == nil {
		t.Error("repeated ids")
	}
}

func TestNextID(t *testing.T) {
	c := Cameras{}
	if id, err := c.NextID(1); err != nil || id != "cam1" {
		t.Fatalf("first: %q %v", id, err)
	}
	c.Cameras = []Camera{cam("cam1")}
	if _, err := c.NextID(1); err != ErrLimit {
		t.Fatalf("limit: %v", err)
	}
	c.Cameras = []Camera{cam("cam2")}
	if id, _ := c.NextID(3); id != "cam1" {
		t.Fatalf("gap: %q", id)
	}
}

func TestValidators(t *testing.T) {
	for _, s := range []string{"EnhancedIRL-1A2B", "My network", "a", "x.y_z-1"} {
		if !ValidSSID(s) {
			t.Errorf("valid SSID rejected: %q", s)
		}
	}
	for _, s := range []string{"", " x", "x ", "a;b", "a\"b", strings.Repeat("a", 33), "café"} {
		if ValidSSID(s) {
			t.Errorf("invalid SSID accepted: %q", s)
		}
	}
	for _, c := range []string{"auto", "1", "6", "11"} {
		if !ValidChannel(c) {
			t.Errorf("channel %q", c)
		}
	}
	for _, c := range []string{"0", "12", "36", "01", "", "6 "} {
		if ValidChannel(c) {
			t.Errorf("invalid channel accepted %q", c)
		}
	}
	if DefaultSSID("ab12cd34ef") != "EnhancedIRL-34EF" || DefaultSSID("") != "EnhancedIRL-Cam" {
		t.Error("default SSID")
	}
}

// LooseSSID: the name of a network.json ParseNetwork rejects, only if it is valid
func TestLooseSSID(t *testing.T) {
	for in, want := range map[string]string{
		`{"v":1,"ap":{"ssid":"Studio A","channel":"auto"},"future":true}`: "Studio A",
		`{"v":2,"ap":{"ssid":"EnhancedIRL-A1B2"}}`:                        "EnhancedIRL-A1B2",
		`{"v":1,"ap":{"ssid":"a;b","channel":"auto"},"future":true}`:      "",
		`{"v":1,"ap":{"ssid":" lead","channel":"auto"}}`:                  "",
		`{"v":1,"ap":`: "",
		``:             "",
	} {
		if got := LooseSSID([]byte(in)); got != want {
			t.Errorf("%s: %q, expected %q", in, got, want)
		}
	}
}

func TestSecret(t *testing.T) {
	a, _ := Secret(KeyLen)
	b, _ := Secret(KeyLen)
	if !reKey.MatchString(a) || a == b {
		t.Fatalf("secret: %q %q", a, b)
	}
}

func TestConnectAndQR(t *testing.T) {
	c := cam("cam1").Connect("")
	if c.SRT != "srt://10.42.0.1:8890?streamid=publish:cam1:cam1:AbCdEfGhIjKlMnOpQrSt" {
		t.Errorf("SRT: %s", c.SRT)
	}
	if c.RTMP != "rtmp://10.42.0.1:1935/cam1?user=cam1&pass=AbCdEfGhIjKlMnOpQrSt" || c.RTMPServer+c.RTMPKey != c.RTMP {
		t.Errorf("RTMP: %s · %s + %s", c.RTMP, c.RTMPServer, c.RTMPKey)
	}
	if c.LAN != nil {
		t.Error("without an eth0 IP there is no wired URL")
	}
	b, _ := json.Marshal(c)
	if !strings.Contains(string(b), `"srt":"srt://10.42.0.1:8890?`) || !strings.Contains(string(b), `"lan":null`) {
		t.Errorf("JSON: %s", b)
	}
	// Over the cable, eth0's IP; anything that is not an IPv4 is rejected
	l := cam("cam1").Connect("192.168.1.50").LAN
	if l == nil || l.SRT != "srt://192.168.1.50:8890?streamid=publish:cam1:cam1:AbCdEfGhIjKlMnOpQrSt" ||
		l.RTMPServer != "rtmp://192.168.1.50:1935/" || l.RTMPServer+l.RTMPKey != l.RTMP {
		t.Errorf("wired: %+v", l)
	}
	for _, bad := range []string{"fe80::1", "192.168.1.50/24", "jetson.local", "1.2.3.4:80"} {
		if cam("cam1").Connect(bad).LAN != nil {
			t.Errorf("eth0 IP %q accepted", bad)
		}
	}
	if q := WifiQR(`a;b:c`, "AbCdEfGhIjKlMnOpQrSt"); q != `WIFI:T:WPA;S:a\;b\:c;P:AbCdEfGhIjKlMnOpQrSt;;` {
		t.Errorf("QR: %s", q)
	}
}

func TestMediaMTXConfig(t *testing.T) {
	const apiKey = "ApiKeyApiKeyApiKeyApiKeyApiKey12"
	y, err := MediaMTXConfig([]Camera{cam("cam1")}, apiKey, APAddr)
	if err != nil {
		t.Fatal(err)
	}
	s := string(y)
	// The API, only with the key (hashed); without credentials from localhost, only reading the video
	any := s[strings.Index(s, "- user: any"):strings.Index(s, "- user: "+APIUser)]
	if strings.Contains(any, "action: api") || !strings.Contains(s, "pass: "+KeyHash(apiKey)) || strings.Contains(s, apiKey) {
		t.Errorf("API poorly protected:\n%s", s)
	}
	if y, _ := MediaMTXConfig([]Camera{cam("cam1")}, "", APAddr); strings.Contains(string(y), "action: api") {
		t.Error("without a key, the API is open to nobody")
	}
	if _, err := MediaMTXConfig(nil, "short", APAddr); err == nil {
		t.Error("invalid API key")
	}
	for _, l := range []string{"", "192.168.1.50", "0.0.0.0\nrunOnReady: sh", "::"} {
		if _, err := MediaMTXConfig(nil, "", l); err == nil {
			t.Errorf("listen address %q accepted", l)
		}
	}
	for _, want := range []string{
		"rtmpAddress: 10.42.0.1:1935", "srtAddress: 10.42.0.1:8890", "rtspAddress: 127.0.0.1:8554",
		"apiAddress: 127.0.0.1:9997", "moq: false", "hls: false", "webrtc: false", "  cam1: {}",
		"pass: " + KeyHash("AbCdEfGhIjKlMnOpQrSt"), `ips: ["10.42.0.0/24"]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(s, "AbCdEfGhIjKlMnOpQrSt") || strings.Contains(s, "runOn") {
		t.Error("the configuration carries the key in clear text or a runOn")
	}
	bad := cam("cam1")
	bad.Key = "x"
	if _, err := MediaMTXConfig([]Camera{bad}, "", APAddr); err == nil {
		t.Error("invalid camera")
	}
	if y, _ := MediaMTXConfig(nil, "", APAddr); !strings.Contains(string(y), "paths:\n  {}\n") {
		t.Error("no cameras")
	}
	// With the cable (§6.11): RTMP and SRT on every IPv4 and publishing without a network limit (the firewall
	// filters); reading and the API, same as before (localhost only)
	y, _ = MediaMTXConfig([]Camera{cam("cam1")}, apiKey, AnyAddr)
	s = string(y)
	for _, want := range []string{"rtmpAddress: 0.0.0.0:1935", "srtAddress: 0.0.0.0:8890", "rtspAddress: 127.0.0.1:8554",
		"apiAddress: 127.0.0.1:9997", "pass: " + KeyHash("AbCdEfGhIjKlMnOpQrSt") + "\n    permissions:\n      - action: publish\n        path: cam1\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("with the cable, missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, APNet) || strings.Count(s, `ips: ["127.0.0.1"]`) != 2 {
		t.Errorf("with the cable, the ips:\n%s", s)
	}
}

func TestNetworkSwitches(t *testing.T) {
	// network.json from before §6.11: Wi-Fi on and cable off
	n, ok, err := ParseNetwork([]byte(`{"v":1,"ap":{"ssid":"EnhancedIRL-1A2B","channel":"auto"}}`))
	if err != nil || !ok || !n.AP.On() || n.LANOn() {
		t.Fatalf("old file: %+v %v %v", n, ok, err)
	}
	r := string(n.Render())
	if !strings.Contains(r, `"enabled": true`) || !strings.Contains(r, `"lan": {`) {
		t.Errorf("Render without the switches:\n%s", r)
	}
	off := false
	n.AP.Enabled = &off
	n.LAN = &LAN{Enabled: true}
	n2, ok, err := ParseNetwork(n.Render())
	if err != nil || !ok || n2.AP.On() || !n2.LANOn() {
		t.Fatalf("round trip: %+v %v %v", n2, ok, err)
	}
	if _, _, err := ParseNetwork([]byte(`{"v":1,"ap":{"ssid":"a","channel":"auto"},"lan":{"enabled":true,"iface":"usb0"}}`)); err == nil {
		t.Error("extra field in lan accepted")
	}
}

// The master switch (§6.12): without the field, on; off, a round trip without touching the other two
func TestNetworkMasterSwitch(t *testing.T) {
	n, ok, err := ParseNetwork([]byte(`{"v":1,"ap":{"ssid":"EnhancedIRL-1A2B","channel":"auto","enabled":true},"lan":{"enabled":true}}`))
	if err != nil || !ok || !n.On() {
		t.Fatalf("file from before §6.12: %+v %v %v", n, ok, err)
	}
	// On is not written: the file is also valid for an older app (strict reading)
	on := true
	n.Enabled = &on
	if r := string(n.Render()); strings.Contains(r, "{\n  \"v\": 1,\n  \"enabled\"") {
		t.Errorf("on, Render writes the master switch:\n%s", r)
	}
	off := false
	n.Enabled = &off
	r := string(n.Render())
	if !strings.Contains(r, "{\n  \"v\": 1,\n  \"enabled\": false,") {
		t.Errorf("off, Render without the master switch:\n%s", r)
	}
	n2, ok, err := ParseNetwork([]byte(r))
	if err != nil || !ok || n2.On() || !n2.AP.On() || !n2.LANOn() {
		t.Fatalf("round trip: %+v %v %v", n2, ok, err)
	}
}

func TestParseMediaKey(t *testing.T) {
	if k, ok, err := ParseMediaKey([]byte("ApiKeyApiKeyApiKeyApiKeyApiKey12\n")); !ok || err != nil || k != "ApiKeyApiKeyApiKeyApiKeyApiKey12" {
		t.Fatal(k, ok, err)
	}
	if _, ok, err := ParseMediaKey(nil); ok || err != nil {
		t.Fatal("empty")
	}
	if _, _, err := ParseMediaKey([]byte("with spaces and other junk")); err == nil {
		t.Fatal("invalid")
	}
}

func TestAPKeyfile(t *testing.T) {
	k, err := APKeyfile("EnhancedIRL-1A2B", "AbCdEfGhIjKlMnOpQrSt", 6)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mode=ap", "band=bg", "channel=6", "interface-name=ap0", "method=manual",
		"address1=10.42.0.1/24", "never-default=true", "autoconnect=false", "proto=rsn;"} {
		if !strings.Contains(string(k), want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(string(k), "shared") {
		t.Error("the AP must not be in shared mode")
	}
	if _, err := APKeyfile("a\nb", "AbCdEfGhIjKlMnOpQrSt", 6); err == nil {
		t.Error("SSID with a line break")
	}
	if _, err := APKeyfile("x", "AbCdEfGhIjKlMnOpQrSt", 36); err == nil {
		t.Error("5 GHz channel")
	}
}

func TestPublic(t *testing.T) {
	c := Cameras{Cameras: []Camera{cam("cam1")}}
	b := c.Public().Render()
	if strings.Contains(string(b), "AbCdEfGhIjKlMnOpQrSt") || strings.Contains(string(b), "key") {
		t.Fatal("the public list carries the key")
	}
	p, err := ParsePublic(b)
	if err != nil || len(p.Cameras) != 1 || p.Cameras[0].Name != "Zoë's phone" {
		t.Fatalf("round trip: %v %+v", err, p)
	}
	if _, err := ParsePublic([]byte(`{"v":1,"cameras":[{"id":"cam1","name":"x","key":"y"}]}`)); err == nil {
		t.Error("extra field")
	}
}

// The AP policy (WIFI.md W4 and W7): the chosen one, or the historical one (with cameras and «por wifi», up)
func TestAPPolicy(t *testing.T) {
	n, _, err := ParseNetwork([]byte(`{"v":1,"ap":{"ssid":"EnhancedIRL-1A2B","channel":"auto"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if n.APPolicy(true) != APAlways || n.APPolicy(false) != APRescue || !n.ClientOn() {
		t.Errorf("not chosen: %s %s %v", n.APPolicy(true), n.APPolicy(false), n.ClientOn())
	}
	off := false
	n.AP.Enabled = &off
	if n.APPolicy(true) != APRescue {
		t.Error("without «por wifi», the historical one is rescue")
	}
	n.WiFi = &WiFi{AP: APAlways}
	if n.APPolicy(false) != APAlways {
		t.Error("always chosen, no cameras")
	}
	n.WiFi = &WiFi{AP: APRescue}
	n.AP.Enabled = nil
	if n.APPolicy(true) != APRescue {
		t.Error("rescue chosen, with cameras: cameras do not force the AP (W3)")
	}
	r := string(n.Render())
	if !strings.Contains(r, `"wifi": {`) || !strings.Contains(r, `"ap": "rescue"`) {
		t.Errorf("Render without the policy:\n%s", r)
	}
	if n2, ok, err := ParseNetwork([]byte(r)); err != nil || !ok || n2.APPolicy(true) != APRescue {
		t.Fatalf("round trip: %+v %v", n2, err)
	}
	// With nothing chosen, the block is not written (the file is valid for an older app)
	n.WiFi = &WiFi{}
	if strings.Contains(string(n.Render()), `"wifi"`) {
		t.Error("empty wifi block written")
	}
	for _, bad := range []string{`{"v":1,"wifi":{"ap":"sometimes"},"ap":{"ssid":"a","channel":"auto"}}`,
		`{"v":1,"wifi":{"ap":"always","mode":"x"},"ap":{"ssid":"a","channel":"auto"}}`} {
		if _, _, err := ParseNetwork([]byte(bad)); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
}
