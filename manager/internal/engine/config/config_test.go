package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

const confPath = "/etc/eirl-encoder/engine.env"

// fake is an injectable outside world: environment, EIRL_CONF content and executables.
type fake struct {
	env     map[string]string
	conf    *string // nil = file missing
	readErr error
	exec    map[string]bool
}

func (f fake) options() Options {
	return Options{
		LookupEnv: func(k string) (string, bool) { v, ok := f.env[k]; return v, ok },
		ReadFile: func(p string) ([]byte, error) {
			if p != confPath {
				return nil, fs.ErrNotExist
			}
			if f.readErr != nil {
				return nil, f.readErr
			}
			if f.conf == nil {
				return nil, fs.ErrNotExist
			}
			return []byte(*f.conf), nil
		},
		Executable: func(p string) bool { return f.exec[p] },
		FindVDEV:   func() string { return "/dev/video1" },
	}
}

func load(t *testing.T, f fake) (*Config, error) {
	t.Helper()
	return Load(f.options())
}

func mustLoad(t *testing.T, f fake) *Config {
	t.Helper()
	c, err := load(t, f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func ptr(s string) *string { return &s }

// wantInvalid checks that err is an *InvalidError naming key.
func wantInvalid(t *testing.T, err error, key string) {
	t.Helper()
	var ie *InvalidError
	if !errors.As(err, &ie) {
		t.Fatalf("err = %v, want InvalidError(%s)", err, key)
	}
	if ie.Key() != key {
		t.Fatalf("key = %s, want %s (%v)", ie.Key(), key, err)
	}
}

func TestDefaults(t *testing.T) {
	c := mustLoad(t, fake{env: map[string]string{}})
	want := Config{
		ConfSHA: "none", NetConfDir: "/etc/eirl-encoder",
		Source: "test", VDEV: "/dev/video1", VideoDevice: "auto", VideoFormat: "auto", VideoMode: "auto", AudioSource: "auto",
		MixerSlots: 4, MixerLatencyMs: 60, MixerSyncMs: 120, MixerScreen: true, MixerHDMICapture: "nvcam", MixerAudioSlots: 4,
		MixerNetSyncMs: 150, ProbeBin: "/usr/local/bin/eirl-encoderd",
		EDIDPreferred: "1080p30", EDID: "/usr/lib/eirl-encoder/engine/edid-1080p30-lpcm48k.bin",
		ALSACard: "tegrasndt210ref", ALSAAdmaif: 2, SignalTimeout: 60, DeviceTimeout: 60, WatchdogMs: 3000, Overlay: true,
		TCI2C: 8, AudioLockTimeout: 10, HWResetInterval: 300, StateDir: "/var/lib/eirl-engine", RunDir: "/run/eirl-engine",
		VBitrate: 6000, VBitrateMin: 1000, ABitrate: 128, AENC: "voaacenc", Profile: 4, H264Preset: 2, H264TwoPass: true,
		OutputAtBoot: true, SRTMode: "caller", SRTLatencyMs: 200, Transport: "srt", SRTLABin: "/usr/local/bin/srtla_send",
		SRTLALocalPort: 6000, UplinksFile: "/run/eirl-uplinks/available", UplinkWait: 30,
		SRTStats: "/run/eirl-engine/srt-stats.json",
		Levels:   true, LevelsPort: 5005, Live: true, LiveSize: "640x360", LiveVBitrate: 800, LiveABitrate: 96,
		LiveVPort: 5006, LiveAPort: 5008,
		Thumb: true, ThumbSize: "320x180", ThumbEvery: 5, ThumbFile: "/run/eirl-engine/thumb.jpg",
		PulseServer: "unix:/nonexistent",
	}
	if !reflect.DeepEqual(*c, want) {
		t.Fatalf("defaults:\n got %+v\nwant %+v", *c, want)
	}
}

// Every key: a valid value lands in its field; invalid values are rejected naming the key.
func TestKeys(t *testing.T) {
	cases := []struct {
		key     string
		valid   string
		check   func(*Config) bool
		invalid []string
	}{
		{"SOURCE", "usb", func(c *Config) bool { return c.Source == "usb" }, []string{"HDMI", "hdmi ", "file"}},
		{"VDEV", "/dev/video3", func(c *Config) bool { return c.VDEV == "/dev/video3" }, []string{"/dev/video 3", "/dev/$(x)"}},
		{"VIDEO_DEVICE", "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0004-video-index0", func(c *Config) bool {
			return c.VideoDevice == "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0004-video-index0"
		}, []string{"/dev/video0", "/dev/v4l/by-id/.hidden", "/dev/v4l/by-id/a b", "/dev/v4l/by-id/" + strings.Repeat("a", 201)}},
		{"VIDEO_FORMAT", "MJPG", func(c *Config) bool { return c.VideoFormat == "MJPG" }, []string{"mjpg", "H264"}},
		{"VIDEO_MODE", "1920x1080@60", func(c *Config) bool { return c.VideoMode == "1920x1080@60" }, []string{"1920x1080", "1920x1080@6000"}},
		{"AUDIO_SOURCE", "alsa:C4K", func(c *Config) bool { return c.AudioSource == "alsa:C4K" }, []string{"alsa:", "alsa:a b", "pulse"}},
		{"MIXER_SLOTS", "2", func(c *Config) bool { return c.MixerSlots == 2 }, []string{"0", "5", "02"}},
		{"MIXER_LATENCY", "9999", func(c *Config) bool { return c.MixerLatencyMs == 9999 }, []string{"10000", "-1"}},
		{"MIXER_SYNC", "0", func(c *Config) bool { return c.MixerSyncMs == 0 }, []string{"1000", "x"}},
		{"MIXER_SCREEN", "0", func(c *Config) bool { return !c.MixerScreen }, []string{"2", "on"}},
		{"MIXER_HDMI_CAPTURE", "v4l2", func(c *Config) bool { return c.MixerHDMICapture == "v4l2" }, []string{"NVCAM", "v4l"}},
		{"MIXER_AUDIO_SLOTS", "1", func(c *Config) bool { return c.MixerAudioSlots == 1 }, []string{"0", "9"}},
		{"PROBE_BIN", "/opt/probe", func(c *Config) bool { return c.ProbeBin == "/opt/probe" }, []string{"/opt/pro be", "/opt/p;rm"}},
		{"EDID_PREFERRED", "720p60", func(c *Config) bool {
			return c.EDIDPreferred == "720p60" && c.EDID == "/usr/lib/eirl-encoder/engine/edid-720p60-lpcm48k.bin"
		}, []string{"1080p60", "720p"}},
		{"EDID", "/srv/x.bin", func(c *Config) bool { return c.EDID == "/srv/x.bin" }, []string{"/srv/x y.bin"}},
		{"ALSA_CARD", "C4K", func(c *Config) bool { return c.ALSACard == "C4K" }, []string{"C 4K"}},
		{"ALSA_ADMAIF", "3", func(c *Config) bool { return c.ALSAAdmaif == 3 }, []string{"1234567", "-1"}},
		{"SIGNAL_TIMEOUT", "0", func(c *Config) bool { return c.SignalTimeout == 0 && c.DeviceTimeout == 0 }, []string{"1s"}},
		{"DEVICE_TIMEOUT", "7", func(c *Config) bool { return c.DeviceTimeout == 7 }, []string{"x"}},
		{"WATCHDOG", "0", func(c *Config) bool { return c.WatchdogMs == 0 }, []string{"3e3"}},
		{"TC_I2C", "7", func(c *Config) bool { return c.TCI2C == 7 }, []string{"i2c-7"}},
		{"AUDIO_LOCK_TIMEOUT", "0", func(c *Config) bool { return c.AudioLockTimeout == 0 }, []string{"1.5"}},
		{"HW_RESET_INTERVAL", "600", func(c *Config) bool { return c.HWResetInterval == 600 }, []string{"1000000"}},
		{"STATE_DIR", "/tmp/s", func(c *Config) bool { return c.StateDir == "/tmp/s" }, []string{"/tmp/s s"}},
		{"RUN_DIR", "/tmp/r", func(c *Config) bool {
			return c.RunDir == "/tmp/r" && c.SRTStats == "/tmp/r/srt-stats.json" && c.ThumbFile == "/tmp/r/thumb.jpg"
		}, []string{"/tmp/r r"}},
		{"RES", "1280x720", func(c *Config) bool { return c.Res == "1280x720" }, []string{"1280x", "1x1", "1280X720"}},
		{"FPS", "25", func(c *Config) bool { return c.FPSSet && c.FPS == 25 }, []string{"12345", "29.97"}},
		{"GOP", "60", func(c *Config) bool { return c.GOPSet && c.GOP == 60 }, []string{"12345"}},
		{"VBITRATE", "999999", func(c *Config) bool { return c.VBitrate == 999999 }, []string{"1000000", "6000k", "a[$(id)]"}},
		{"VBITRATE_MIN", "300", func(c *Config) bool { return c.VBitrateMin == 300 }, []string{"1e3"}},
		{"ABR", "1", func(c *Config) bool { return c.ABR }, []string{"yes"}},
		{"ABITRATE", "96", func(c *Config) bool { return c.ABitrate == 96 }, []string{"96k"}},
		{"AENC", "avenc_aac", func(c *Config) bool { return c.AENC == "avenc_aac" }, []string{"fdkaacenc", "voaacenc "}},
		{"PROFILE", "2", func(c *Config) bool { return c.Profile == 2 }, []string{"high"}},
		{"H264_PRESET", "4", func(c *Config) bool { return c.H264Preset == 4 }, []string{"0", "5"}},
		{"H264_QP_RANGE", "10,30:10,30:10,30", func(c *Config) bool { return c.H264QPRange == "10,30:10,30:10,30" }, []string{"10,30", "100,30:10,30:10,30"}},
		{"H264_TWOPASS", "0", func(c *Config) bool { return !c.H264TwoPass }, []string{"true"}},
		{"OUTPUT", "off", func(c *Config) bool { return !c.OutputAtBoot }, []string{"ON", "1"}},
		{"RECORD", "/tmp/rec.ts", func(c *Config) bool { return c.Record == "/tmp/rec.ts" }, []string{"/tmp/rec .ts"}},
		{"SRT_URL", "srt://relay.example.com:9000", func(c *Config) bool { return c.SRTURL == "srt://relay.example.com:9000" }, []string{"srt://h:9000?streamid=x", "rtmp://h:1"}},
		{"SRT_MODE", "listener", func(c *Config) bool { return c.SRTMode == "listener" }, []string{"rendezvous"}},
		{"SRT_LATENCY", "2000", func(c *Config) bool { return c.SRTLatencyMs == 2000 }, []string{"2000ms"}},
		{"SRT_PASSPHRASE", "0123456789", func(c *Config) bool { return c.SRTPassphrase.Reveal() == "0123456789" }, []string{"short", "0123456789&mode=listener", strings.Repeat("a", 80)}},
		{"SRT_STREAMID", "publish:live/key", func(c *Config) bool { return c.SRTStreamID.Reveal() == "publish:live/key" }, []string{"a&latency=1", "a b", strings.Repeat("a", 513)}},
		{"SRTLA_BIN", "/opt/srtla_send", func(c *Config) bool { return c.SRTLABin == "/opt/srtla_send" }, []string{"/opt/srtla send"}},
		{"SRTLA_LOCAL_PORT", "65535", func(c *Config) bool { return c.SRTLALocalPort == 65535 }, []string{"0", "65536", "08"}},
		{"UPLINKS_FILE", "/tmp/up", func(c *Config) bool { return c.UplinksFile == "/tmp/up" }, []string{"/tmp/u p"}},
		{"UPLINK_WAIT", "0", func(c *Config) bool { return c.UplinkWait == 0 }, []string{"-"}},
		{"UPLINKS_DISABLED", "/tmp/d", func(c *Config) bool { return c.UplinksDisabled == "/tmp/d" }, []string{"/tmp/d d"}},
		{"UPLINKS_NOINTERNET", "/tmp/n", func(c *Config) bool { return c.UplinksNoInternet == "/tmp/n" }, []string{"/tmp/n n"}},
		{"SRT_STATS", "/tmp/s.json", func(c *Config) bool { return c.SRTStats == "/tmp/s.json" }, []string{"/tmp/s s.json"}},
		{"OVERLAY", "0", func(c *Config) bool { return !c.Overlay }, []string{"no"}},
		{"MIXER", "0", func(c *Config) bool { return c.SingleSource }, []string{"2", "on"}},
		{"LEVELS", "0", func(c *Config) bool { return !c.Levels }, []string{"off"}},
		{"LEVELS_PORT", "1", func(c *Config) bool { return c.LevelsPort == 1 }, []string{"0", "70000", "09"}},
		{"LIVE", "0", func(c *Config) bool { return !c.Live }, []string{"off"}},
		{"LIVE_SIZE", "1280x720", func(c *Config) bool { return c.LiveSize == "1280x720" }, []string{"640x", "720p"}},
		{"LIVE_VBITRATE", "1500", func(c *Config) bool { return c.LiveVBitrate == 1500 }, []string{"x"}},
		{"LIVE_ABITRATE", "64", func(c *Config) bool { return c.LiveABitrate == 64 }, []string{"x"}},
		{"LIVE_VPORT", "7000", func(c *Config) bool { return c.LiveVPort == 7000 }, []string{"0", "99999"}},
		{"LIVE_APORT", "7002", func(c *Config) bool { return c.LiveAPort == 7002 }, []string{"0", "99999"}},
		{"THUMB", "0", func(c *Config) bool { return !c.Thumb }, []string{"yes"}},
		{"THUMB_SIZE", "160x90", func(c *Config) bool { return c.ThumbSize == "160x90" }, []string{"16000x90", "160x90x"}},
		{"THUMB_EVERY", "1", func(c *Config) bool { return c.ThumbEvery == 1 }, []string{"0", "00", "10000", "09"}},
		{"THUMB_FILE", "/tmp/t.jpg", func(c *Config) bool { return c.ThumbFile == "/tmp/t.jpg" }, []string{"/tmp/t t.jpg"}},
		{"PULSE_SERVER", "unix:/run/pulse", func(c *Config) bool { return c.PulseServer == "unix:/run/pulse" }, nil},
		{"AUDIO", "anything goes", func(c *Config) bool { return c.AudioEnv == "anything goes" }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			c := mustLoad(t, fake{env: map[string]string{tc.key: tc.valid}})
			if !tc.check(c) {
				t.Fatalf("%s=%q not applied: %+v", tc.key, tc.valid, *c)
			}
			for _, bad := range tc.invalid {
				_, err := load(t, fake{env: map[string]string{tc.key: bad}})
				wantInvalid(t, err, tc.key)
			}
		})
	}
}

// Keys whose value is checked with bash arithmetic: a leading 0 is octal there.
func TestBashArithmetic(t *testing.T) {
	c := mustLoad(t, fake{env: map[string]string{"LEVELS_PORT": "010", "THUMB_EVERY": "007"}})
	if c.LevelsPort != 10 || c.ThumbEvery != 7 {
		t.Fatalf("decimal values: port %d every %d", c.LevelsPort, c.ThumbEvery)
	}
	// 0300 is 192 for bash: below the ABR floor of 200
	_, err := load(t, fake{env: map[string]string{"ABR": "1", "VBITRATE_MIN": "0300"}})
	wantInvalid(t, err, "VBITRATE_MIN")
	// 09000 is not a number for bash: the comparison fails and names VBITRATE_MIN
	_, err = load(t, fake{env: map[string]string{"ABR": "1", "VBITRATE": "09000"}})
	wantInvalid(t, err, "VBITRATE_MIN")
	// without ABR the same values are fine
	mustLoad(t, fake{env: map[string]string{"ABR": "0", "VBITRATE_MIN": "0300", "VBITRATE": "09000"}})
}

func TestABRFloor(t *testing.T) {
	for _, tc := range []struct {
		min, max string
		ok       bool
	}{{"200", "6000", true}, {"6000", "6000", true}, {"199", "6000", false}, {"6001", "6000", false}} {
		_, err := load(t, fake{env: map[string]string{"ABR": "1", "VBITRATE_MIN": tc.min, "VBITRATE": tc.max}})
		if tc.ok && err != nil {
			t.Errorf("%s..%s: %v", tc.min, tc.max, err)
		}
		if !tc.ok {
			wantInvalid(t, err, "VBITRATE_MIN")
		}
	}
}

// The first invalid key in the launcher's order is the one reported.
func TestValidationOrder(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"THUMB": "x", "VBITRATE": "x"}, "VBITRATE"},
		{map[string]string{"DEVICE_TIMEOUT": "x", "FPS": "x"}, "DEVICE_TIMEOUT"},
		{map[string]string{"SRT_URL": "x", "SRT_STREAMID": "a b"}, "SRT_STREAMID"},
		{map[string]string{"SRT_URL": "x", "AENC": "x"}, "SRT_URL"},
		{map[string]string{"AENC": "x", "THUMB_EVERY": "0"}, "AENC"},
		{map[string]string{"EDID_PREFERRED": "x", "EDID": "a b"}, "EDID_PREFERRED"},
		{map[string]string{"LIVE_APORT": "0", "RES": "x"}, "LIVE_APORT"},
		{map[string]string{"SIGNAL_TIMEOUT": "x"}, "SIGNAL_TIMEOUT"}, // DEVICE_TIMEOUT inherits it but comes later
	}
	for _, tc := range cases {
		_, err := load(t, fake{env: tc.env})
		wantInvalid(t, err, tc.want)
	}
}

func TestWhitelist(t *testing.T) {
	conf := strings.Join([]string{
		"BASH_ENV=/tmp/evil", "LD_PRELOAD=/tmp/evil.so", "GST_PLUGIN_PATH=/tmp", "RECORD=/tmp/leak.ts",
		"RUN_DIR=/tmp", "MIXER=0", "SRTLA_BIN=/tmp/x", "PROBE_BIN=/tmp/x", "EIRL_MANAGED_BY=eirl", "UNKNOWN=1",
		"MIXER_NET_SYNC=5", "EIRL_CONF=/tmp/other", "PULSE_SERVER=tcp:evil", "VBITRATE=5000",
	}, "\n") + "\n"
	c := mustLoad(t, fake{env: map[string]string{"EIRL_CONF": confPath}, conf: &conf})
	if c.Record != "" || c.RunDir != "/run/eirl-engine" || c.SingleSource || c.SRTLABin != "/usr/local/bin/srtla_send" ||
		c.ProbeBin != "/usr/local/bin/eirl-encoderd" || c.MixerNetSyncMs != 150 || c.ConfPath != confPath ||
		c.PulseServer != "unix:/nonexistent" {
		t.Fatalf("non-whitelisted key applied: %+v", *c)
	}
	if c.VBitrate != 5000 {
		t.Fatalf("whitelisted VBITRATE not applied: %d", c.VBitrate)
	}
	got := ParseConfFile([]byte(conf))
	if len(got) != 1 || got["VBITRATE"] != "5000" {
		t.Fatalf("ParseConfFile kept %v", got)
	}
	if len(Whitelist) != 16 {
		t.Fatalf("whitelist has %d keys, the launcher has 16", len(Whitelist))
	}
}

func TestPrecedence(t *testing.T) {
	envVB := map[string]string{"EIRL_CONF": confPath, "VBITRATE": "4000", "SOURCE": "hdmi"}
	cases := []struct {
		name string
		conf *string
		want int
	}{
		{"env only (file missing)", nil, 4000},
		{"file without the key", ptr("SRT_MODE=caller\n"), 4000},
		{"file wins", ptr("VBITRATE=5000\n"), 5000},
		{"last occurrence wins", ptr("VBITRATE=5000\nVBITRATE=7000\n"), 7000},
		{"empty in file → default, not env", ptr("VBITRATE=\n"), 6000},
		{"bare key in file → default, not env", ptr("VBITRATE"), 6000},
	}
	for _, tc := range cases {
		c := mustLoad(t, fake{env: envVB, conf: tc.conf})
		if c.VBitrate != tc.want {
			t.Errorf("%s: VBITRATE %d, want %d", tc.name, c.VBitrate, tc.want)
		}
		if c.Source != "hdmi" {
			t.Errorf("%s: SOURCE from env lost: %s", tc.name, c.Source)
		}
	}
	// Without EIRL_CONF the file is never read
	c := mustLoad(t, fake{env: map[string]string{"VBITRATE": "4000"}, conf: ptr("VBITRATE=5000\n")})
	if c.VBitrate != 4000 || c.ConfSHA != NoConfSHA {
		t.Fatalf("file read without EIRL_CONF: %d %s", c.VBitrate, c.ConfSHA)
	}
}

func TestParseConfFileQuirks(t *testing.T) {
	cases := []struct {
		name, content, key string
		want               string
		present            bool
	}{
		{"value with '=' inside", "SRT_URL=a=b\n", "SRT_URL", "a=b", true},
		{"single trailing '=' dropped (bash read)", "SRT_STREAMID=publish:abc=\n", "SRT_STREAMID", "publish:abc", true},
		{"lone '=' value", "SRT_STREAMID==\n", "SRT_STREAMID", "", true},
		{"two trailing '=' kept", "SRT_STREAMID=abc==\n", "SRT_STREAMID", "abc==", true},
		{"'=' inside and at the end kept", "SRT_STREAMID=a=b=\n", "SRT_STREAMID", "a=b=", true},
		{"spaces kept", "SRT_MODE= caller \n", "SRT_MODE", " caller ", true},
		{"quotes kept", `SRT_MODE="caller"` + "\n", "SRT_MODE", `"caller"`, true},
		{"CR kept", "SRT_MODE=caller\r\n", "SRT_MODE", "caller\r", true},
		{"leading space: different key", " SRT_MODE=listener\n", "SRT_MODE", "", false},
		{"comment: different key", "#SRT_MODE=listener\n", "SRT_MODE", "", false},
		{"no trailing newline", "SRT_MODE=listener", "SRT_MODE", "listener", true},
		{"bare key", "SRT_MODE\n", "SRT_MODE", "", true},
		{"NUL bytes dropped", "SRT_\x00MODE=list\x00ener\n", "SRT_MODE", "listener", true},
		{"lowercase key ignored", "srt_mode=listener\n", "SRT_MODE", "", false},
	}
	for _, tc := range cases {
		got, ok := ParseConfFile([]byte(tc.content))[tc.key]
		if ok != tc.present || got != tc.want {
			t.Errorf("%s: %q present=%v, want %q present=%v", tc.name, got, ok, tc.want, tc.present)
		}
	}
}

// Values with the quirks above, through Load: kept as they are and rejected by the regex.
func TestQuirksRejected(t *testing.T) {
	cases := []struct{ conf, key string }{
		{"SRT_MODE= caller\n", "SRT_MODE"},
		{"VBITRATE=5000\r\n", "VBITRATE"},
		{`VBITRATE="5000"` + "\n", "VBITRATE"},
		{"SRT_STREAMID=a=b\n", "SRT_STREAMID"},
		{"SRT_URL=srt://h:9000 \n", "SRT_URL"},
		{"OUTPUT=on # comment\n", "OUTPUT"},
	}
	for _, tc := range cases {
		_, err := load(t, fake{env: map[string]string{"EIRL_CONF": confPath}, conf: ptr(tc.conf)})
		wantInvalid(t, err, tc.key)
	}
	c := mustLoad(t, fake{env: map[string]string{"EIRL_CONF": confPath}, conf: ptr("SRT_STREAMID=publish:abc=\n")})
	if c.SRTStreamID.Reveal() != "publish:abc" {
		t.Fatalf("streamid %q", c.SRTStreamID.Reveal())
	}
}

func TestConfFileAccess(t *testing.T) {
	// Unreadable but existing: config error
	_, err := load(t, fake{env: map[string]string{"EIRL_CONF": confPath}, readErr: fs.ErrPermission})
	wantInvalid(t, err, "EIRL_CONF")
	// Missing (also through a non-directory component, like [ -e ]): ignored
	for _, e := range []error{fs.ErrNotExist, syscall.ENOTDIR} {
		c := mustLoad(t, fake{env: map[string]string{"EIRL_CONF": confPath}, readErr: e})
		if c.ConfSHA != NoConfSHA {
			t.Fatalf("%v: CONF_SHA %s", e, c.ConfSHA)
		}
	}
	// A real file without read permission
	if os.Geteuid() != 0 {
		p := filepath.Join(t.TempDir(), "engine.env")
		if err := os.WriteFile(p, []byte("VBITRATE=5000\n"), 0o000); err != nil {
			t.Fatal(err)
		}
		_, err := Load(Options{LookupEnv: func(k string) (string, bool) {
			if k == "EIRL_CONF" {
				return p, true
			}
			return "", false
		}, FindVDEV: func() string { return "/dev/video0" }})
		wantInvalid(t, err, "EIRL_CONF")
	}
}

func TestEIRLConfDerived(t *testing.T) {
	c := mustLoad(t, fake{env: map[string]string{"EIRL_CONF": "/srv/enc/engine.env"}})
	if c.UplinksDisabled != "/srv/enc/uplinks-disabled" || c.UplinksNoInternet != "/run/eirl-encoder/uplinks-nointernet" ||
		c.NetConfDir != "/srv/enc" || c.ConfSHA != NoConfSHA {
		t.Fatalf("%+v", *c)
	}
	// Without EIRL_CONF: no uplink filters, engine_net's default directory
	c = mustLoad(t, fake{env: map[string]string{}})
	if c.UplinksDisabled != "" || c.UplinksNoInternet != "" || c.NetConfDir != "/etc/eirl-encoder" {
		t.Fatalf("%+v", *c)
	}
	// ${VAR-…}: set but empty disables; ${VAR:-…} would not
	c = mustLoad(t, fake{env: map[string]string{"EIRL_CONF": confPath, "UPLINKS_DISABLED": "", "UPLINKS_NOINTERNET": "",
		"SRT_STATS": "", "THUMB_FILE": "", "VBITRATE": ""}})
	if c.UplinksDisabled != "" || c.UplinksNoInternet != "" || c.SRTStats != "" || c.ThumbFile != "" || c.VBitrate != 6000 {
		t.Fatalf("%+v", *c)
	}
	// EIRL_CONF set but empty: like unset for the launcher, "" directory for engine_net
	c = mustLoad(t, fake{env: map[string]string{"EIRL_CONF": ""}})
	if c.UplinksDisabled != "" || c.NetConfDir != "" {
		t.Fatalf("%+v", *c)
	}
	// An EIRL_CONF the path regex refuses makes UPLINKS_DISABLED invalid, as in bash
	_, err := load(t, fake{env: map[string]string{"EIRL_CONF": "/etc/eirl encoder/engine.env"}})
	wantInvalid(t, err, "UPLINKS_DISABLED")
}

func TestDirnames(t *testing.T) {
	for _, tc := range []struct{ in, sh, py string }{
		{"/etc/eirl-encoder/engine.env", "/etc/eirl-encoder", "/etc/eirl-encoder"},
		{"engine.env", ".", ""},
		{"/engine.env", "/", "/"},
		{"/a/b/", "/a", "/a/b"},
		{"/", "/", "/"},
		{"a//b", "a", "a"},
	} {
		if got := shDirname(tc.in); got != tc.sh {
			t.Errorf("shDirname(%q) = %q, want %q", tc.in, got, tc.sh)
		}
		if got := pyDirname(tc.in); got != tc.py {
			t.Errorf("pyDirname(%q) = %q, want %q", tc.in, got, tc.py)
		}
	}
}

func TestNetSyncMs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 150}, {"0", 0}, {"300", 300}, {"1000", 1000}, {"1001", 150}, {"0100", 100}, {"-5", 150}, {" 300", 150},
		{"300ms", 150}, {"99999999999999999999999", 150}, {"٣٠٠", 150}, {"1e3", 150},
	} {
		if got := NetSyncMs(tc.in); got != tc.want {
			t.Errorf("NetSyncMs(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	c := mustLoad(t, fake{env: map[string]string{"MIXER_NET_SYNC": "400"}})
	if c.MixerNetSyncMs != 400 {
		t.Fatalf("MixerNetSyncMs %d", c.MixerNetSyncMs)
	}
}

func TestSingleSourceAccepted(t *testing.T) {
	// MIXER=0 with OUTPUT=on and no destination was exit 64 in bash; the engine runs the mixer,
	// where no destination is allowed.
	c := mustLoad(t, fake{env: map[string]string{"MIXER": "0", "OUTPUT": "on"}})
	if !c.SingleSource || !c.SRTTarget().IsZero() {
		t.Fatalf("%+v", *c)
	}
	if !strings.Contains(string(c.StateFile(c.StartingState())), "\nMIXER=1\n") {
		t.Fatal("state must say MIXER=1: the engine runs the mixer")
	}
}

func TestVDEV(t *testing.T) {
	c := mustLoad(t, fake{env: map[string]string{}})
	if c.VDEV != "/dev/video1" {
		t.Fatalf("FindVDEV not used: %s", c.VDEV)
	}
	c = mustLoad(t, fake{env: map[string]string{"VDEV": "/dev/video4"}})
	if c.VDEV != "/dev/video4" {
		t.Fatalf("explicit VDEV lost: %s", c.VDEV)
	}
}

func TestInvalidErrorMessage(t *testing.T) {
	_, err := load(t, fake{env: map[string]string{"ABR": "1", "VBITRATE_MIN": "10"}})
	if err.Error() != "invalid configuration: VBITRATE_MIN (between 200 and VBITRATE)" {
		t.Fatalf("%q", err.Error())
	}
	_, err = load(t, fake{env: map[string]string{"SOURCE": "x"}})
	if err.Error() != "invalid configuration: SOURCE" {
		t.Fatalf("%q", err.Error())
	}
}
