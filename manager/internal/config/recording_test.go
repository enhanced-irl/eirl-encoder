package config

import (
	"path/filepath"
	"regexp"
	"testing"

	engine "github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
)

// REC_MODE and REC_QUALITY (RECORDING.md §6.1): off and high by default, written with every save, read back, and
// only from their closed lists: what the engine accepts (its reccfg.go)
func TestRecordingSettings(t *testing.T) {
	if d := Defaults(); d.RecMode != "off" || d.RecQuality != "high" {
		t.Errorf("defaults %q %q", d.RecMode, d.RecQuality)
	}
	engineRe := map[string]*regexp.Regexp{
		"REC_MODE":    regexp.MustCompile(`^(off|manual|with_output)$`),
		"REC_QUALITY": regexp.MustCompile(`^(standard|high|max|efficient)$`),
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "engine.env")
	for _, mode := range RecModes {
		for _, q := range RecQualities {
			s := valid()
			s.RecMode, s.RecQuality = mode, q.ID
			if err := Save(path, s); err != nil {
				t.Fatalf("%s %s: %v", mode, q.ID, err)
			}
			env, _ := ReadEnv(path)
			for k, re := range engineRe {
				if !re.MatchString(env[k]) {
					t.Errorf("%s=%q does not pass the engine's validation", k, env[k])
				}
			}
			if got, _ := Load(filepath.Join(dir, "none"), path); got.RecMode != mode || got.RecQuality != q.ID {
				t.Errorf("read back %q %q, want %q %q", got.RecMode, got.RecQuality, mode, q.ID)
			}
		}
	}
	for _, c := range []struct{ mode, quality, field string }{
		{"on", "high", "recordingMode"}, {"", "high", "recordingMode"}, {"manual;reboot", "high", "recordingMode"},
		{"manual", "ultra", "recordingQuality"}, {"manual", "", "recordingQuality"}, {"manual", "High", "recordingQuality"},
	} {
		s := valid()
		s.RecMode, s.RecQuality = c.mode, c.quality
		fe, ok := s.Validate().(FieldErrors)
		if !ok || fe[c.field] == "" {
			t.Errorf("%q %q: %v", c.mode, c.quality, s.Validate())
		}
	}
}

// The qualities the API lists are what the engine encodes (D2)
func TestRecQualitiesAreTheEngines(t *testing.T) {
	if len(RecQualities) != 4 {
		t.Fatalf("%d qualities", len(RecQualities))
	}
	for _, q := range RecQualities {
		e := (&engine.Config{RecQuality: q.ID}).Recording()
		if e.Codec != q.Codec || e.VKbps != q.VideoKbps || e.AKbps != q.AudioKbps {
			t.Errorf("%s: app %+v, engine %+v", q.ID, q, e)
		}
	}
	if DefaultRecQuality != "high" || !ValidRecQuality(DefaultRecQuality) {
		t.Errorf("default %q", DefaultRecQuality)
	}
}

// The recording's keys are taken live with output_config, alone or with the destination's; a device key still restarts
func TestDiffEnvRecording(t *testing.T) {
	base := map[string]string{"SRT_URL": "srt://h:1", "OUTPUT": "on", "EDID_PREFERRED": "1080p30", "REC_MODE": "off",
		"REC_QUALITY": "high"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for a, b := range base {
			m[a] = b
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for _, c := range []struct {
		name                string
		after               map[string]string
		changed, onlyOutput bool
	}{
		{"mode", with("REC_MODE", "manual"), true, true},
		{"quality", with("REC_QUALITY", "max"), true, true},
		{"with the destination", with("REC_MODE", "with_output", "SRT_URL", "srt://h:2"), true, true},
		{"with the EDID", with("REC_MODE", "manual", "EDID_PREFERRED", "720p60"), true, false},
	} {
		if ch, oo := DiffEnv(base, c.after); ch != c.changed || oo != c.onlyOutput {
			t.Errorf("%s: %v %v", c.name, ch, oo)
		}
	}
	// An engine.env from before the recording: its keys appearing with the next save are taken live too
	old := map[string]string{"SRT_URL": "srt://h:1", "OUTPUT": "on"}
	if ch, oo := DiffEnv(old, map[string]string{"SRT_URL": "srt://h:1", "OUTPUT": "on", "REC_MODE": "off", "REC_QUALITY": "high"}); !ch || !oo {
		t.Errorf("keys appearing: %v %v", ch, oo)
	}
}
