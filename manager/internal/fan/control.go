package fan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// What eirl-fan and the app share (FAN.md §5): the daemon's state file, which the app reads to show the second fan
// in Settings, and the control file, which the app writes when someone switches the second fan off there.

// StateFile: the daemon's state (eirl-fan.service's RuntimeDirectory, root, 0644).
const StateFile = "/run/eirl-fan/state.json"

// State: StateFile.
type State struct {
	PWM     int                `json:"pwm"`
	TempC   float64            `json:"tempC"`
	Sensor  string             `json:"sensor"`
	Sensors map[string]float64 `json:"sensors"`
	Curve   string             `json:"curve"`
	Error   string             `json:"error,omitempty"`
	At      string             `json:"at"`
	Fan2    *Fan2State         `json:"fan2,omitempty"` // only on a board with the second fan
}

// Fan2State: the second fan in State.
type Fan2State struct {
	On          bool    `json:"on"`
	Since       string  `json:"since"`            // when it was last switched (or the daemon started)
	Reason      string  `json:"reason,omitempty"` // why it is on: temp (OnC), hot (HotC), reading (it failed), start (found on)
	Mode        string  `json:"mode"`             // ModeAuto, or ModeOff: switched off in Settings until the next boot
	OnC         float64 `json:"onC"`
	OffC        float64 `json:"offC"`
	HotC        float64 `json:"hotC"` // on at this temperature even when switched off
	MinOnS      int     `json:"minOnS"`
	GPIO        int     `json:"gpio"`
	Error       string  `json:"error,omitempty"`        // setting its pin failed
	ConfigError string  `json:"configError,omitempty"`  // what was wrong in its settings (the defaults are used)
	ControlErr  string  `json:"controlError,omitempty"` // the control file could not be used (automatic)
}

// MaxStateSize: the most a reader takes of StateFile (it is a few hundred bytes).
const MaxStateSize = 16 << 10

// ReadState: StateFile as the daemon left it. A missing file (no daemon, or not yet) is an error that matches
// os.ErrNotExist.
func ReadState(path string) (State, error) {
	var st State
	f, err := os.Open(path)
	if err != nil {
		return st, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxStateSize+1))
	if err != nil {
		return st, err
	}
	if len(b) > MaxStateSize {
		return st, fmt.Errorf("%s: more than %d bytes", path, MaxStateSize)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("%s: %w", path, err)
	}
	return st, nil
}

// The second fan's modes.
const (
	ModeAuto = "auto" // by temperature (Switch)
	ModeOff  = "off"  // switched off in Settings: on only at HotC or with a failed reading, until the next boot
)

// ControlFile: the app's request for the second fan, in the app's configuration directory (FAN.md §5). Written by
// the app (its user, 0644: the daemon's root has no capabilities and reads it as "other"; nothing in it is secret)
// and read by eirl-fan as data at every reading, only if it is a regular file of the app's user or of root.
const ControlFile = "/etc/eirl-encoder/fan.json"

// BootIDFile: the kernel's id of this boot. A ModeOff request carries it, and only holds for that boot.
const BootIDFile = "/proc/sys/kernel/random/boot_id"

// MaxControlSize: the most the daemon reads of ControlFile.
const MaxControlSize = 4 << 10

// Control: ControlFile.
type Control struct {
	Fan2   string `json:"fan2"`             // ModeAuto or ModeOff
	BootID string `json:"bootId,omitempty"` // the boot a ModeOff was asked in
}

// ParseControl: ControlFile's content, checked: a known mode, and a boot id with ModeOff.
func ParseControl(b []byte) (Control, error) {
	var c Control
	if err := json.Unmarshal(b, &c); err != nil {
		return Control{}, err
	}
	switch c.Fan2 {
	case ModeAuto:
	case ModeOff:
		if c.BootID == "" {
			return Control{}, errors.New(`"off" without the boot it was asked in`)
		}
	default:
		return Control{}, fmt.Errorf("unknown mode %q", c.Fan2)
	}
	return c, nil
}

// Mode: the second fan's mode in the boot bootID: ModeOff only if it was asked in this boot.
func (c Control) Mode(bootID string) string {
	if c.Fan2 == ModeOff && c.BootID == bootID {
		return ModeOff
	}
	return ModeAuto
}

// ReadBootID: this boot's id (BootIDFile, one line).
func ReadBootID(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if id == "" || len(id) > 64 {
		return "", fmt.Errorf("%s: not a boot id", path)
	}
	return id, nil
}
