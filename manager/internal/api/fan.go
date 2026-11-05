package api

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/fan"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

// --- the fans in Settings (FAN.md §5): part of GET/PUT /device -------------------------------------------------

// FanFiles: what the app reads and writes for the second fan. State: the fan daemon's state (fan.StateFile; "" =
// never shown); Control: the app's request (fan.ControlFile); BootID: this boot's id (fan.BootIDFile).
type FanFiles struct {
	State, Control, BootID string
}

// fanView: the fans in GET /device, from the fan daemon's state (nil without it: the daemon is not running).
type fanView struct {
	TempC  float64   `json:"tempC"`            // the hottest of CPU and GPU, as the daemon read it
	PWM    int       `json:"pwm"`              // the main fan's speed (0-255)
	Second *fan2View `json:"second,omitempty"` // only on a board with the second fan
}

// fan2View: the second fan.
type fan2View struct {
	Mode   string  `json:"mode"`             // auto, or off: switched off here until the next boot
	On     bool    `json:"on"`               // what the daemon has it at now
	Reason string  `json:"reason,omitempty"` // why it is on: temp, hot, reading, start
	OnC    float64 `json:"onC"`              // automatic: on from this temperature
	HotC   float64 `json:"hotC"`             // off: still on from this temperature, or with a failed reading
}

// fanView: the fans, nil if the fan daemon is not running. The second fan's mode is what the app asked for in this
// boot (the daemon takes it within one reading, 2 s); the rest is the daemon's.
func (s *Server) fanView() *fanView {
	if s.Fan.State == "" {
		return nil
	}
	st, err := fan.ReadState(s.Fan.State)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.Log.Warn("read the fan's state", "err", err)
		}
		return nil
	}
	v := &fanView{TempC: st.TempC, PWM: st.PWM}
	if f := st.Fan2; f != nil {
		v.Second = &fan2View{Mode: s.fan2Mode(), On: f.On, Reason: f.Reason, OnC: f.OnC, HotC: f.HotC}
	}
	return v
}

// fan2Mode: the mode the app asked for in this boot (auto without a request, or with one from another boot).
func (s *Server) fan2Mode() string {
	b, err := safefile.ReadOwned(s.Fan.Control, fan.MaxControlSize)
	if err != nil {
		return fan.ModeAuto
	}
	c, err := fan.ParseControl(b)
	if err != nil {
		return fan.ModeAuto
	}
	id, err := fan.ReadBootID(s.Fan.BootID)
	if err != nil {
		return fan.ModeAuto
	}
	return c.Mode(id)
}

// setFan2Mode: the app's request for the second fan, for the daemon (0644: its root has no capabilities and reads
// it as "other"; there is nothing secret in it). Off carries this boot's id: the next boot starts automatic.
func (s *Server) setFan2Mode(mode string) error {
	c := fan.Control{Fan2: mode}
	if mode == fan.ModeOff {
		id, err := fan.ReadBootID(s.Fan.BootID)
		if err != nil {
			return err
		}
		c.BootID = id
	}
	b, _ := json.Marshal(c)
	return config.WriteAtomic(s.Fan.Control, append(b, '\n'), 0o644)
}
