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
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
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
	must(t, os.MkdirAll(filepath.Join(root, "class/gpio"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "class/gpio/export"), nil, 0o644))
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
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now, written: -1, fan2Set: -1}
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
	var st fan.State
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
	var st fan.State
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
	if _, ok := h.add(40, 100, nil, t0); ok {
		t.Fatal("a line before the hour")
	}
	h.add(50, 200, nil, t0.Add(30*time.Minute))
	line, ok := h.add(45, 150, nil, t0.Add(time.Hour))
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
	if _, ok := h.add(45, 150, nil, t0.Add(time.Hour+time.Second)); ok {
		t.Fatal("it should start a new hour")
	}
	if _, ok := got["fan2OnPct"]; ok {
		t.Fatal("no second fan: no fan2 fields")
	}
}

func TestHourlyFan2(t *testing.T) {
	var h hourly
	t0 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	h.add(50, 150, &fan2Sample{}, t0)
	h.add(56, 190, &fan2Sample{on: true, switched: true}, t0.Add(20*time.Minute))
	h.add(52, 170, &fan2Sample{on: true}, t0.Add(40*time.Minute))
	line, ok := h.add(49, 140, &fan2Sample{switched: true}, t0.Add(time.Hour))
	if !ok {
		t.Fatal("no line after an hour")
	}
	got := map[string]any{}
	for i := 0; i+1 < len(line); i += 2 {
		got[line[i].(string)] = line[i+1]
	}
	if got["fan2OnPct"] != 50 || got["fan2Switches"] != 2 {
		t.Fatalf("line %v", got)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// fakeFan2: the second fan's pin as sysfs shows it once exported.
func fakeFan2(t *testing.T, sys, direction, value string) {
	t.Helper()
	dir := filepath.Join(sys, fan2Dir)
	must(t, os.MkdirAll(dir, 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "direction"), []byte(direction+"\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "value"), []byte(value+"\n"), 0o644))
}

// setTemp: CPU-therm and GPU-therm (thermal_zone1 and 2 of fakeSys) at the same reading.
func setTemp(t *testing.T, sys, milli string) {
	t.Helper()
	for _, z := range []string{"thermal_zone1", "thermal_zone2"} {
		must(t, os.WriteFile(filepath.Join(sys, "class/thermal", z, "temp"), []byte(milli+"\n"), 0o644))
	}
}

func withFan2(d *daemon) *daemon {
	sw := fan.DefaultSwitch()
	d.fan2 = &sw
	return d
}

func pin(t *testing.T, sys, f string) string { return readTrim(t, filepath.Join(sys, fan2Dir, f)) }

func readState(t *testing.T, run string) fan.State {
	t.Helper()
	var st fan.State
	must(t, json.Unmarshal([]byte(readTrim(t, filepath.Join(run, "state.json"))), &st))
	return st
}

func TestFan2Settings(t *testing.T) {
	def := fan.DefaultSwitch()
	cases := []struct {
		env     map[string]string
		want    *fan.Switch
		problem bool
	}{
		{nil, nil, false},
		{map[string]string{"EIRL_FAN2": "off", "EIRL_FAN2_ON_C": "40"}, nil, false},
		{map[string]string{"EIRL_FAN2": "yes"}, nil, true},
		{map[string]string{"EIRL_FAN2": "on"}, &def, false},
		{map[string]string{"EIRL_FAN2": "on", "EIRL_FAN2_ON_C": "47.5", "EIRL_FAN2_OFF_C": "44", "EIRL_FAN2_MIN_ON": "30s"},
			&fan.Switch{OnC: 47.5, OffC: 44, MinOn: 30 * time.Second}, false},
		{map[string]string{"EIRL_FAN2": "on", "EIRL_FAN2_ON_C": "hot"}, &def, true},
		{map[string]string{"EIRL_FAN2": "on", "EIRL_FAN2_MIN_ON": "2"}, &def, true},
		{map[string]string{"EIRL_FAN2": "on", "EIRL_FAN2_ON_C": "45"}, &def, true}, // off (50) not below on
		{map[string]string{"EIRL_FAN2": "on", "EIRL_FAN2_OFF_C": "40", "EIRL_FAN2_MIN_ON": "1h"}, &def, true},
	}
	for i, k := range cases {
		got, problem := fan2Settings(func(n string) string { return k.env[n] })
		if (got == nil) != (k.want == nil) || got != nil && *got != *k.want || (problem != "") != k.problem {
			t.Errorf("case %d %v: got %v %q, want %v (problem %v)", i, k.env, got, problem, k.want, k.problem)
		}
	}
}

func TestClaimExports(t *testing.T) {
	sys, run := fakeSys(t, nil)
	d := withFan2(newDaemon(sys, run))
	err := d.claim() // the fake kernel makes no gpio168 directory: the direction fails after the export
	if got := readTrim(t, filepath.Join(sys, "class/gpio/export")); got != "168" {
		t.Fatalf("export %q, want 168", got)
	}
	if err == nil {
		t.Fatal("no gpio168 after the export: want an error")
	}
}

func TestClaimLevelFromTemperature(t *testing.T) {
	for _, k := range []struct {
		milli, want string
	}{{"45000", "low"}, {"52000", "low"}, {"55000", "high"}, {"garbage", "high"}} {
		sys, run := fakeSys(t, map[string]string{"CPU-therm": k.milli, "GPU-therm": k.milli})
		fakeFan2(t, sys, "in", "1") // an input stuck high by its level shifter
		d := withFan2(newDaemon(sys, run))
		must(t, d.claim())
		if got := pin(t, sys, "direction"); got != k.want {
			t.Errorf("%s m°C: direction %q, want %q", k.milli, got, k.want)
		}
		if got := readTrim(t, filepath.Join(sys, "class/gpio/export")); got != "" {
			t.Errorf("exported again an exported pin: %q", got)
		}
	}
}

func TestClaimLeavesAnOutput(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"CPU-therm": "40000", "GPU-therm": "40000"})
	fakeFan2(t, sys, "out", "1")
	d := withFan2(newDaemon(sys, run))
	must(t, d.claim())
	if pin(t, sys, "direction") != "out" || pin(t, sys, "value") != "1" {
		t.Fatal("an output must be left as it is")
	}
}

func TestNoSecondFanTouchesNothing(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"CPU-therm": "70000", "GPU-therm": "70000"})
	fakeFan2(t, sys, "in", "0")
	d := newDaemon(sys, run)
	must(t, d.claim())
	d.startFan2()
	d.step(&hourly{})
	d.leaveFan2()
	if readTrim(t, filepath.Join(sys, "class/gpio/export")) != "" || pin(t, sys, "direction") != "in" || pin(t, sys, "value") != "0" {
		t.Fatal("without EIRL_FAN2=on the pin must not be touched")
	}
	if strings.Contains(readTrim(t, filepath.Join(run, "state.json")), "fan2") {
		t.Fatal("no second fan: no fan2 in the state")
	}
}

func TestFan2FollowsTemperature(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"CPU-therm": "45000", "GPU-therm": "44000"})
	fakeFan2(t, sys, "out", "0")
	d := withFan2(newDaemon(sys, run))
	t0 := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	now := t0
	d.now = func() time.Time { return now }
	d.startFan2()
	steps := []struct {
		at    time.Duration
		milli string
		want  string
	}{
		{0, "45000", "0"},
		{2 * time.Second, "55000", "1"},
		{time.Minute, "49000", "1"}, // below OffC, but not on for 2 min yet
		{2*time.Minute + 2*time.Second, "49000", "0"},
		{3 * time.Minute, "54500", "0"}, // between the thresholds
	}
	for i, k := range steps {
		now = t0.Add(k.at)
		setTemp(t, sys, k.milli)
		d.step(&hourly{})
		if got := pin(t, sys, "value"); got != k.want {
			t.Fatalf("step %d (%s m°C at +%s): value %s, want %s", i, k.milli, k.at, got, k.want)
		}
		st := readState(t, run)
		if st.Fan2 == nil || st.Fan2.On != (k.want == "1") || st.Fan2.Error != "" || st.Fan2.GPIO != 168 {
			t.Fatalf("step %d: state %+v", i, st.Fan2)
		}
		if k.want == "1" && st.Fan2.Reason != "temp" {
			t.Fatalf("step %d: reason %q, want temp", i, st.Fan2.Reason)
		}
	}
	if st := readState(t, run); st.PWM != fan.Default.At(54.5) {
		t.Fatalf("the main fan must keep its curve: pwm %d", st.PWM)
	}
}

func TestFan2SafetyAndStart(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"CPU-therm": "30000", "GPU-therm": "30000"})
	fakeFan2(t, sys, "out", "1") // left on by a stop while hot
	d := withFan2(newDaemon(sys, run))
	t0 := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	now := t0
	d.now = func() time.Time { return now }
	d.startFan2()
	d.step(&hourly{})
	if pin(t, sys, "value") != "1" || readState(t, run).Fan2.Reason != "start" {
		t.Fatal("found on: on for its minimum time")
	}
	now = t0.Add(2 * time.Minute)
	d.step(&hourly{})
	if pin(t, sys, "value") != "0" {
		t.Fatal("found on, cool, after the minimum time: off")
	}
	now = now.Add(2 * time.Second)
	setTemp(t, sys, "garbage")
	d.step(&hourly{})
	if pin(t, sys, "value") != "1" || readState(t, run).Fan2.Reason != "reading" {
		t.Fatal("a failed reading must switch it on at once")
	}
	sys2, run2 := fakeSys(t, map[string]string{"CPU-therm": "81000", "GPU-therm": "40000"})
	fakeFan2(t, sys2, "out", "0")
	d2 := withFan2(newDaemon(sys2, run2))
	d2.fan2.OnC, d2.fan2.OffC = 79, 70
	d2.startFan2()
	d2.step(&hourly{})
	if pin(t, sys2, "value") != "1" || readState(t, run2).Fan2.Reason != "hot" {
		t.Fatal("at HotC: on, reason hot")
	}
}

func TestFan2PutBack(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"CPU-therm": "60000", "GPU-therm": "60000"})
	fakeFan2(t, sys, "out", "0")
	d := withFan2(newDaemon(sys, run))
	d.startFan2()
	d.step(&hourly{})
	if pin(t, sys, "value") != "1" {
		t.Fatal("hot: on")
	}
	must(t, os.WriteFile(filepath.Join(sys, fan2Dir, "value"), []byte("0\n"), 0o644))
	d.step(&hourly{})
	if pin(t, sys, "value") != "1" {
		t.Fatal("moved outside eirl-fan: not put back")
	}
	must(t, os.WriteFile(filepath.Join(sys, fan2Dir, "direction"), []byte("in\n"), 0o644))
	d.step(&hourly{})
	if pin(t, sys, "direction") != "high" {
		t.Fatalf("made an input: direction %q, want high", pin(t, sys, "direction"))
	}
}

func TestFan2LeftAsItIsWhenStopping(t *testing.T) {
	for _, c := range []struct {
		temp, want string
	}{{"40000", "0"}, {"60000", "1"}} {
		sys, run := fakeSys(t, map[string]string{"CPU-therm": c.temp, "GPU-therm": c.temp})
		fakeFan2(t, sys, "out", "0")
		d := withFan2(newDaemon(sys, run))
		d.startFan2()
		d.step(&hourly{})
		if pin(t, sys, "value") != c.want {
			t.Fatalf("%s m°C: the second fan should be %s", c.temp, c.want)
		}
		d.leaveFan2()
		if got := pin(t, sys, "value"); got != c.want {
			t.Fatalf("%s m°C: stopping changed the second fan from %s to %s", c.temp, c.want, got)
		}
	}
}

func TestFan2PinMissing(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"CPU-therm": "60000", "GPU-therm": "60000"})
	d := withFan2(newDaemon(sys, run)) // the claim failed: no gpio168
	d.startFan2()
	if !d.step(&hourly{}) {
		t.Fatal("a second fan it cannot set must not stop the main fan's control")
	}
	st := readState(t, run)
	if st.Fan2 == nil || st.Fan2.Error == "" || st.PWM != fan.Default.At(60) {
		t.Fatalf("state %+v %+v", st, st.Fan2)
	}
	if got := readTrim(t, filepath.Join(sys, "devices/pwm-fan/target_pwm")); got != itoa(fan.Default.At(60)) {
		t.Fatalf("target_pwm %s", got)
	}
}

// withControl: the app's control file for the second fan, in a temporary directory, trusted as the test's user's.
func withControl(t *testing.T, d *daemon, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fan.json")
	if content != "" {
		must(t, os.WriteFile(p, []byte(content), 0o644))
	}
	d.ctlPath, d.appUID, d.bootID = p, uint32(os.Geteuid()), "boot-1"
	return p
}

func TestFan2SwitchedOffInSettings(t *testing.T) {
	sys, run := fakeSys(t, map[string]string{"CPU-therm": "60000", "GPU-therm": "60000"})
	fakeFan2(t, sys, "out", "0")
	d := withFan2(newDaemon(sys, run))
	p := withControl(t, d, "")
	d.startFan2()
	d.step(&hourly{})
	if pin(t, sys, "value") != "1" || readState(t, run).Fan2.Mode != fan.ModeAuto {
		t.Fatal("no control file: automatic, on at 60 °C")
	}
	must(t, os.WriteFile(p, []byte(`{"fan2":"off","bootId":"boot-1"}`), 0o644))
	d.step(&hourly{})
	st := readState(t, run)
	if pin(t, sys, "value") != "0" || st.Fan2.Mode != fan.ModeOff || st.Fan2.On || st.Fan2.HotC != fan.HotC {
		t.Fatalf("switched off in Settings: off at once at 60 °C (%+v)", st.Fan2)
	}
	setTemp(t, sys, "81000")
	d.step(&hourly{})
	if st := readState(t, run); pin(t, sys, "value") != "1" || st.Fan2.Reason != "hot" {
		t.Fatalf("switched off, but at 81 °C: on (%+v)", st.Fan2)
	}
	setTemp(t, sys, "60000")
	must(t, os.WriteFile(p, []byte(`{"fan2":"auto"}`), 0o644))
	d.step(&hourly{})
	if pin(t, sys, "value") != "1" || readState(t, run).Fan2.Mode != fan.ModeAuto {
		t.Fatal("back to automatic: on at 60 °C")
	}
}

func TestFan2ControlOnlyThisBootAndTrusted(t *testing.T) {
	for _, c := range []struct {
		name, content string
		untrusted     bool
		wantErr       bool
	}{
		{"another boot", `{"fan2":"off","bootId":"boot-0"}`, false, false},
		{"not json", `off`, false, true},
		{"another user's file", `{"fan2":"off","bootId":"boot-1"}`, true, true},
	} {
		sys, run := fakeSys(t, map[string]string{"CPU-therm": "60000", "GPU-therm": "60000"})
		fakeFan2(t, sys, "out", "0")
		d := withFan2(newDaemon(sys, run))
		withControl(t, d, c.content)
		if c.untrusted {
			if os.Geteuid() == 0 {
				continue // root's own files are always trusted
			}
			d.appUID++
			old := safefile.OnUntrusted
			safefile.OnUntrusted = func(path, reason string) { d.controlProblem(path + ": " + reason) }
			defer func() { safefile.OnUntrusted = old }()
		}
		d.startFan2()
		d.step(&hourly{})
		st := readState(t, run)
		if pin(t, sys, "value") != "1" || st.Fan2.Mode != fan.ModeAuto || (st.Fan2.ControlErr != "") != c.wantErr {
			t.Fatalf("%s: automatic, on at 60 °C (%+v)", c.name, st.Fan2)
		}
	}
}
