package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// State is what changes between the two writes of /run/eirl-engine/state.
type State struct {
	Input      string // starting | ready
	Device     string // probed device of a usb main source (DEVICE)
	DeviceName string // its name, through SanitizeDeviceName (DEVICE_NAME)
	Rate       string // program FPS_FRAC (RATE); "" while starting
	Audio      string // AUDIO
}

// StartingState is the first write (sh:248): nothing probed yet, AUDIO as the environment had it.
func (c *Config) StartingState() State {
	return State{Input: "starting", Audio: c.AudioEnv}
}

// ReadyState is the write once the pipeline is built (sh:624): the program rate and
// AUDIO=mezclador:<audioMain> (audioMain from AudioMain).
func (c *Config) ReadyState(p Program, device, deviceName, audioMain string) State {
	return State{Input: "ready", Device: device, DeviceName: deviceName, Rate: p.FPSFrac, Audio: "mezclador:" + audioMain}
}

// StateFile renders the state file in the launcher's order (sh:244-247). FORMAT and MODE are
// single-source fields and stay empty; MIXER is always 1, which tells the app to compare CONF_SHA
// with its hash without OUTPUT.
func (c *Config) StateFile(s State) []byte {
	output := "off"
	if c.OutputAtBoot {
		output = "on"
	}
	abr := "0"
	if c.ABR {
		abr = "1"
	}
	lines := [][2]string{
		{"OUTPUT", output}, {"SOURCE", c.Source}, {"CONF_SHA", c.ConfSHA}, {"EDID", EDIDStem(c.EDID)},
		{"TRANSPORT", c.Transport}, {"ABR", abr}, {"INPUT", s.Input}, {"DEVICE", s.Device},
		{"DEVICE_NAME", s.DeviceName}, {"FORMAT", ""}, {"MODE", ""}, {"RATE", s.Rate}, {"AUDIO", s.Audio},
		{"MIXER", "1"},
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l[0] + "=" + l[1] + "\n")
	}
	return []byte(b.String())
}

// WriteState writes <RUN_DIR>/state atomically (state.tmp + rename). Like the launcher it does
// nothing when RUN_DIR is not a writable directory.
func (c *Config) WriteState(s State) error {
	if !c.RunDirUsable() {
		return nil
	}
	tmp := filepath.Join(c.RunDir, "state.tmp")
	if err := os.WriteFile(tmp, c.StateFile(s), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(c.RunDir, "state"))
}

// EDIDStem is the EDID value of the state file: the file name without "-lpcm48k.bin" and without a
// leading "edid-" (sh:246, basename "$EDID" -lpcm48k.bin | sed 's/^edid-//').
func EDIDStem(path string) string {
	return strings.TrimPrefix(shBasename(path, "-lpcm48k.bin"), "edid-")
}

// SanitizeDeviceName is how the launcher keeps a probed device name (sh:449): only
// [A-Za-z0-9 ._()+-], at most 48 bytes.
func SanitizeDeviceName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < 48; i++ {
		ch := s[i]
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || strings.IndexByte(" ._()+-", ch) >= 0 {
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// RunDirUsable is the launcher's `[ -d RUN_DIR ] && [ -w RUN_DIR ]`: the condition for the state
// file, --run-dir (socket, mixer.json) and the video stats.
func (c *Config) RunDirUsable() bool { return isDir(c.RunDir) && access(c.RunDir, accessW) }

// PrepareRunDir is sh:228-235: with SRT_STATS set, create RUN_DIR if needed and, when it is not
// writable, log event=run_dir_unwritable and turn the SRT statistics off.
func (c *Config) PrepareRunDir() {
	if c.SRTStats == "" {
		return
	}
	_ = os.MkdirAll(c.RunDir, 0o755)
	if !access(c.RunDir, accessW) {
		elog.Event("engine", "run_dir_unwritable", "dir", c.RunDir)
		c.SRTStats = ""
	}
}

// PrepareStateDir is the launcher's `mkdir -p STATE_DIR && [ -w STATE_DIR ]` (sh:954): whether
// --state-dir (scene persistence, HW reset marks) is usable.
func (c *Config) PrepareStateDir() bool {
	return os.MkdirAll(c.StateDir, 0o755) == nil && access(c.StateDir, accessW)
}

// PrepareThumb is sh:695-698: the thumbnail goes off when THUMB_FILE is empty or its directory is
// not a writable directory; event=thumb_disabled is logged only when THUMB_FILE was set.
func (c *Config) PrepareThumb() {
	if !c.Thumb {
		return
	}
	dir := shDirname(c.ThumbFile)
	if c.ThumbFile != "" && isDir(dir) && access(dir, accessW) {
		return
	}
	if c.ThumbFile != "" {
		elog.Event("engine", "thumb_disabled", "dir", dir)
	}
	c.Thumb = false
}
