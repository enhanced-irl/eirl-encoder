package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/api"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/fan"
)

// simFan is the fan daemon, fake (FAN.md §5): a board with the second fan, idle at 48.5 °C. Every 2 s it takes the
// app's request for the second fan the way eirl-fan does (the control file in -data, only for this boot: each start
// of the simulator is a new boot), so System › Fan and the web's card can be tried without a board.
func simFan(ctx context.Context, dataDir string) api.FanFiles {
	f := api.FanFiles{State: filepath.Join(dataDir, "fan-state.json"), Control: filepath.Join(dataDir, "fan.json"),
		BootID: filepath.Join(dataDir, "boot_id")}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	boot := hex.EncodeToString(b)
	_ = os.WriteFile(f.BootID, []byte(boot+"\n"), 0o644)
	since := time.Now().UTC().Format(time.RFC3339)
	write := func() {
		mode := fan.ModeAuto
		if b, err := os.ReadFile(f.Control); err == nil {
			if c, err := fan.ParseControl(b); err == nil {
				mode = c.Mode(boot)
			}
		}
		st := fan.State{PWM: 133, TempC: 48.5, Sensor: "CPU-therm", Sensors: map[string]float64{"CPU-therm": 48.5,
			"GPU-therm": 46}, Curve: fan.Default.String(), At: time.Now().UTC().Format(time.RFC3339),
			Fan2: &fan.Fan2State{Since: since, Mode: mode, OnC: 55, OffC: 50, HotC: fan.HotC, MinOnS: 120, GPIO: 168}}
		b, _ := json.Marshal(st)
		_ = os.WriteFile(f.State, b, 0o644)
	}
	write()
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				write()
			}
		}
	}()
	return f
}
