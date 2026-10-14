package config

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"testing"
)

func TestInputValidate(t *testing.T) {
	bad := map[string]func(*Settings){
		"odd source":       func(s *Settings) { s.Source = "rtmp" },
		"device outside":   func(s *Settings) { s.VideoDevice = "/dev/video1" },
		"device /etc":      func(s *Settings) { s.VideoDevice = "/etc/passwd" },
		"device ..":        func(s *Settings) { s.VideoDevice = "/dev/v4l/by-id/.." },
		"device with /":    func(s *Settings) { s.VideoDevice = "/dev/v4l/by-id/a/../../x" },
		"device with $()":  func(s *Settings) { s.VideoDevice = "/dev/v4l/by-id/$(id)" },
		"device with '":    func(s *Settings) { s.VideoDevice = "/dev/v4l/by-id/a'b" },
		"device with ' !'": func(s *Settings) { s.VideoDevice = "/dev/v4l/by-id/a ! filesink" },
		"format H264":      func(s *Settings) { s.VideoFormat = "H264" },
		"mode 1080p":       func(s *Settings) { s.VideoMode = "1080p" },
		"mode with !":      func(s *Settings) { s.VideoMode = "1920x1080@60 ! x" },
		"audio hw:0":       func(s *Settings) { s.AudioSource = "hw:0" },
		"audio with ,":     func(s *Settings) { s.AudioSource = "alsa:C4K,DEV=1" },
		"empty audio":      func(s *Settings) { s.AudioSource = "" },
	}
	for name, mut := range bad {
		s := valid()
		mut(&s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := []func(*Settings){
		func(s *Settings) {
			s.Source, s.VideoDevice = "usb", "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0005AB12-video-index0"
		},
		func(s *Settings) {
			s.Source, s.VideoDevice = "usb", "/dev/v4l/by-path/platform-70090000.xusb-usb-0:2.3:1.0-video-index0"
		},
		func(s *Settings) {
			s.Source, s.VideoFormat, s.VideoMode, s.AudioSource = "usb", "MJPG", "1280x720@60", "alsa:MS2109"
		},
		func(s *Settings) { s.Source, s.AudioSource = "test", "none" },
	}
	for i, mut := range good {
		s := valid()
		mut(&s)
		if err := s.Validate(); err != nil {
			t.Errorf("valid %d: %v", i, err)
		}
	}
}

// What the app writes must pass the engine's own validation (internal/engine/config)
func TestInputRenderPassesScriptRegexes(t *testing.T) {
	script := map[string]*regexp.Regexp{
		"SOURCE":       regexp.MustCompile(`^(hdmi|usb|test)$`),
		"VIDEO_DEVICE": regexp.MustCompile(`^(auto|/dev/v4l/by-(id|path)/[A-Za-z0-9_:-][A-Za-z0-9_.:-]{0,199})$`),
		"VIDEO_FORMAT": regexp.MustCompile(`^(auto|NV12|YUY2|I420|UYVY|MJPG)$`),
		"VIDEO_MODE":   regexp.MustCompile(`^(auto|[0-9]{2,5}x[0-9]{2,5}@[0-9]{1,3})$`),
		"AUDIO_SOURCE": regexp.MustCompile(`^(auto|none|i2s|alsa:[A-Za-z0-9_-]{1,32})$`),
	}
	for _, s := range []Settings{valid(), func() Settings {
		s := valid()
		s.Source, s.VideoDevice, s.VideoFormat, s.VideoMode, s.AudioSource = "usb", "/dev/v4l/by-path/platform-70090000.xusb-usb-0:1.1:1.0-video-index0", "NV12", "1920x1080@60", "alsa:C4K"
		return s
	}()} {
		path := filepath.Join(t.TempDir(), "engine.env")
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
		back, _ := Load("/nonexistent", path)
		if back.Source != s.Source || back.VideoDevice != s.VideoDevice || back.AudioSource != s.AudioSource {
			t.Errorf("round trip: %+v", back)
		}
	}
}

func TestInputPatch(t *testing.T) {
	cur := valid()
	cur.EDIDPreferred = "1080p50"
	// The old API: only the EDID
	var p InputPatch
	json.Unmarshal([]byte(`{"edidPreferred":"720p60","apply":true}`), &p)
	s, err := p.ApplyTo(cur)
	if err != nil || s.EDIDPreferred != "720p60" || s.Source != "hdmi" || !p.Apply {
		t.Fatalf("EDID only: %+v %v", s, err)
	}
	var p2 InputPatch
	json.Unmarshal([]byte(`{"source":"usb","device":"/dev/v4l/by-id/usb-X-video-index0","audio":"alsa:C4K"}`), &p2)
	s, err = p2.ApplyTo(cur)
	if err != nil || s.Source != "usb" || s.VideoDevice != "/dev/v4l/by-id/usb-X-video-index0" || s.EDIDPreferred != "1080p50" {
		t.Fatalf("USB: %+v %v", s, err)
	}
	var bad InputPatch
	json.Unmarshal([]byte(`{"source":"usb","device":"/dev/video1"}`), &bad)
	if _, err := bad.ApplyTo(cur); err == nil {
		t.Error("device outside /dev/v4l accepted")
	} else if fe, ok := err.(FieldErrors); !ok || fe["device"] == "" {
		t.Errorf("error without device field: %v", err)
	}
}
