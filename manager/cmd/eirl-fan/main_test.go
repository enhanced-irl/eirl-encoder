package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/fan"
)

// fakeSys: a sysfs with the Nano's thermal zones and pwm-fan.
func fakeSys(t *testing.T, temps map[string]string) (string, string) {
	t.Helper()
	root, run := t.TempDir(), t.TempDir()
	i := 0
	for _, typ := range []string{"AO-therm", "CPU-therm", "GPU-therm", "thermal-fan-est"} {
		dir := filepath.Join(root, "class/thermal", "thermal_zone"+string(rune('0'+i)))
		i++
		must(t, os.MkdirAll(dir, 0o755))
		must(t, os.WriteFile(filepath.Join(dir, "type"), []byte(typ+"\n"), 0o644))
		v := temps[typ]
		if v == "" {
			v = "45000"
		}
		must(t, os.WriteFile(filepath.Join(dir, "temp"), []byte(v+"\n"), 0o644))
	}
	f := filepath.Join(root, "devices/pwm-fan")
	must(t, os.MkdirAll(f, 0o755))
	for n, v := range map[string]string{"target_pwm": "0", "cur_pwm": "0", "temp_control": "1"} {
		must(t, os.WriteFile(filepath.Join(f, n), []byte(v), 0o644))
	}
	return root, run
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newDaemon(sys, run string) *daemon {
	d := &daemon{sys: sys, run: run, ctl: fan.Controller{Curve: fan.Default, Hyst: 3},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), written: -1}
	d.zones = d.findZones()
	return d
}

func readTrim(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	must(t, err)
	return strings.TrimSpace(string(b))
}

func TestHottestSensorDrivesTheFan(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"AO-therm": "70000", "CPU-therm": "50000", "GPU-therm": "52500"})
	d := newDaemon(sys, run)
	if len(d.zones) != 2 {
		t.Fatalf("zones %v: want CPU-therm and GPU-therm only", d.zones)
	}
	d.step(&hourly{})
	want := fan.Default.At(52.5)
	if got := readTrim(t, filepath.Join(sys, "devices/pwm-fan/target_pwm")); got != itoa(want) {
		t.Fatalf("target_pwm %s, want %d (GPU-therm 52.5; AO-therm is not read)", got, want)
	}
	var st State
	must(t, json.Unmarshal([]byte(readTrim(t, filepath.Join(run, "state.json"))), &st))
	if st.PWM != want || st.Sensor != "GPU-therm" || st.TempC != 52.5 || st.Error != "" {
		t.Fatalf("state %+v", st)
	}
}

func TestUnreadableSensorsRunFull(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"CPU-therm": "garbage", "GPU-therm": "999999"})
	d := newDaemon(sys, run)
	d.step(&hourly{})
	if got := readTrim(t, filepath.Join(sys, "devices/pwm-fan/target_pwm")); got != "255" {
		t.Fatalf("target_pwm %s, want 255", got)
	}
	var st State
	must(t, json.Unmarshal([]byte(readTrim(t, filepath.Join(run, "state.json"))), &st))
	if st.Error == "" {
		t.Fatal("the state should carry the reading error")
	}
}

func TestNoZonesRunFull(t *testing.T) {
	root, run := t.TempDir(), t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "devices/pwm-fan"), 0o755))
	d := newDaemon(root, run)
	d.step(&hourly{})
	if got := readTrim(t, filepath.Join(root, "devices/pwm-fan/target_pwm")); got != "255" {
		t.Fatalf("target_pwm %s, want 255", got)
	}
}

func TestWritesOnlyOnChange(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"CPU-therm": "45000", "GPU-therm": "45000"})
	d := newDaemon(sys, run)
	d.step(&hourly{})
	p := filepath.Join(sys, "devices/pwm-fan/target_pwm")
	must(t, os.WriteFile(p, []byte("marker"), 0o644))
	d.step(&hourly{})
	if got := readTrim(t, p); got != "marker" {
		t.Fatalf("rewrote an unchanged speed: %s", got)
	}
}

func TestGivesUpAfterFailedWrites(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"CPU-therm": "45000", "GPU-therm": "45000"})
	p := filepath.Join(sys, "devices/pwm-fan/target_pwm")
	must(t, os.Remove(p))
	must(t, os.Mkdir(p, 0o755)) // writing it fails
	d := newDaemon(sys, run)
	for i := 1; i < maxFailures; i++ {
		if !d.step(&hourly{}) {
			t.Fatalf("gave up after %d failures", i)
		}
	}
	if d.step(&hourly{}) {
		t.Fatalf("still going after %d failures", maxFailures)
	}
}

func TestZonesFoundLater(t *testing.T) {
	sys, run := fakeSys(t, nil)
	hidden := filepath.Join(t.TempDir(), "thermal")
	must(t, os.Rename(filepath.Join(sys, "class/thermal"), hidden))
	d := newDaemon(sys, run)
	d.step(&hourly{})
	if got := readTrim(t, filepath.Join(sys, "devices/pwm-fan/target_pwm")); got != "255" {
		t.Fatalf("without zones target_pwm %s, want 255", got)
	}
	must(t, os.Rename(hidden, filepath.Join(sys, "class/thermal")))
	d.step(&hourly{})
	if len(d.zones) != 2 {
		t.Fatalf("zones not found again: %v", d.zones)
	}
}

func TestFailedReadingsLeaveTheHour(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"CPU-therm": "garbage", "GPU-therm": "garbage"})
	d := newDaemon(sys, run)
	var h hourly
	d.step(&h)
	if h.n != 0 {
		t.Fatalf("a failed reading counted in the hour: %+v", h)
	}
}

func TestHourly(t *testing.T) {
	var h hourly
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if _, ok := h.add(40, 100, t0); ok {
		t.Fatal("a line before the hour")
	}
	h.add(50, 200, t0.Add(30*time.Minute))
	line, ok := h.add(45, 150, t0.Add(time.Hour))
	if !ok {
		t.Fatal("no line after an hour")
	}
	got := map[string]any{}
	for i := 0; i+1 < len(line); i += 2 {
		got[line[i].(string)] = line[i+1]
	}
	if got["tempMinC"] != 40.0 || got["tempMaxC"] != 50.0 || got["tempAvgC"] != 45.0 || got["pwmAvg"] != 150 || got["pwmMax"] != 200 {
		t.Fatalf("line %v", got)
	}
	if _, ok := h.add(45, 150, t0.Add(time.Hour+time.Second)); ok {
		t.Fatal("it should start a new hour")
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
