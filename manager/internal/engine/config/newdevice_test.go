package config

import (
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/enhanced-irl/eirl-encoder/manager/internal/config"
)

// newDeviceEnv is the engine.env install.sh writes on a NEW device (step 7, nothing to migrate): no
// destination and the output off.
const newDeviceEnv = `# Written by eirl-encoderd (the encoder's management app). Do not edit by hand: it is overwritten.
# Created by install.sh on 20261014-120000 (a new device).
EIRL_MANAGED_BY=local
SRT_URL=
SRT_MODE=caller
SRT_LATENCY=200
SRT_STREAMID=
SRT_PASSPHRASE=
VBITRATE=6000
ABITRATE=128
OUTPUT=off
`

// A new device runs with the board settings install.sh creates (deploy/eirl-engine.default) and that
// engine.env. The engine must load them, start on the board's HDMI, not emit, and have no destination
// at all: nothing is sent anywhere, and the state file says OUTPUT=off, so the OTA helper does not take
// the device for one that is streaming.
func TestNewDeviceConfig(t *testing.T) {
	def, err := appconfig.ReadEnv(filepath.Join("..", "..", "..", "deploy", "eirl-engine.default"))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"EIRL_CONF": confPath}
	for k, v := range def {
		env[k] = v
	}
	conf := newDeviceEnv
	c := mustLoad(t, fake{env: env, conf: &conf})
	if c.OutputAtBoot {
		t.Error("a new device emits at boot")
	}
	if !c.SRTTarget().IsZero() || c.MaskedTarget() != "" || c.Transport != "srt" {
		t.Errorf("a new device has a destination: %q (%s)", c.MaskedTarget(), c.Transport)
	}
	if c.Source != "hdmi" || c.MixerTest() || c.MixerMain("") != "hdmi" || c.AudioMain("") != "i2s" {
		t.Errorf("a new device starts on %q (audio %q, bars %v), want the HDMI", c.MixerMain(""), c.AudioMain(""), c.MixerTest())
	}
	state := string(c.StateFile(c.ReadyState(c.Program(30), "", "", c.AudioMain(""))))
	if !strings.Contains(state, "OUTPUT=off\n") {
		t.Errorf("state file without OUTPUT=off:\n%s", state)
	}
}

// Without /etc/default/eirl-engine (the unit's EnvironmentFile is optional) the engine still loads, on
// its built-in defaults.
func TestNoBoardSettings(t *testing.T) {
	conf := newDeviceEnv
	c := mustLoad(t, fake{env: map[string]string{"EIRL_CONF": confPath}, conf: &conf})
	if c.OutputAtBoot || !c.SRTTarget().IsZero() {
		t.Errorf("output at boot %v, target %q", c.OutputAtBoot, c.MaskedTarget())
	}
}
