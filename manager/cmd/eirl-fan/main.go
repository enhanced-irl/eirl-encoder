// eirl-fan: the encoder's fan control (FAN.md), a root daemon run by eirl-fan.service.
//
// Every 2 s it reads the hottest of the chip's CPU and GPU sensors and sets the fan's speed from the curve of
// internal/fan, writing /sys/devices/pwm-fan/target_pwm. While it runs, the kernel's own control
// (temp_control) is off; it is given back when the daemon stops (and by the unit's ExecStopPost if it dies),
// so the fan is never left without a controller. A reading it cannot make runs the fan at full speed. It also gives
// the fan back and exits when it cannot set the speed 5 times in a row (systemd tries again), and when its own binary
// is replaced or removed (an update restarts it; a rollback to a release without it may leave no unit to do so).
//
//	eirl-fan            the daemon
//	eirl-fan -once      one reading: prints the temperatures and the speed it would set, and touches nothing
//
// What it is doing, for the app and for people: /run/eirl-fan/state.json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/fan"
)

var version = "dev"

// sensors: the thermal zones it reads (types in /sys/class/thermal/thermal_zone*/type). The chip's CPU and
// GPU, the parts that throttle; AO-therm reads some degrees above them and is not where the heat is made.
var sensors = []string{"CPU-therm", "GPU-therm"}

// State: /run/eirl-fan/state.json.
type State struct {
	PWM     int                `json:"pwm"`
	TempC   float64            `json:"tempC"`
	Sensor  string             `json:"sensor"`
	Sensors map[string]float64 `json:"sensors"`
	Curve   string             `json:"curve"`
	Error   string             `json:"error,omitempty"`
	At      string             `json:"at"`
}

type daemon struct {
	sys, run string // /sys and /run (other roots in tests)
	zones    map[string]string
	ctl      fan.Controller
	log      *slog.Logger
	written  int
	failures int // target_pwm writes failed in a row
}

// maxFailures: target_pwm writes failed in a row before it gives the fan back to the kernel and exits.
const maxFailures = 5

func main() {
	once := flag.Bool("once", false, "one reading; print it and touch nothing")
	interval := flag.Duration("interval", 2*time.Second, "time between readings")
	sysRoot := flag.String("sys", "/sys", "sysfs root (tests)")
	runRoot := flag.String("run", "/run/eirl-fan", "state directory")
	flag.Parse()
	d := &daemon{sys: *sysRoot, run: *runRoot, ctl: fan.Controller{Curve: fan.Default, Hyst: 3},
		log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey { // the journal stamps every line
					return slog.Attr{}
				}
				return a
			}})), written: -1}
	if err := d.ctl.Curve.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "eirl-fan: the built-in curve is wrong:", err)
		os.Exit(1)
	}
	d.zones = d.findZones()
	if *once {
		t, name, all, err := d.read()
		pwm := (&fan.Controller{Curve: d.ctl.Curve, Hyst: d.ctl.Hyst}).Next(t, err == nil)
		fmt.Printf("sensors %v · hottest %s %.1f °C · curve %s · would set %d (now %s)\n", all, name, t,
			d.ctl.Curve, pwm, strings.TrimSpace(d.readFile("devices/pwm-fan/cur_pwm")))
		if err != nil {
			fmt.Println("reading error:", err)
		}
		return
	}
	os.Exit(d.loop(*interval))
}

func (d *daemon) loop(interval time.Duration) int {
	if len(d.zones) == 0 {
		d.log.Error("no CPU or GPU thermal zone: the fan runs at full speed", "wanted", sensors)
	}
	if err := d.writeFile("devices/pwm-fan/temp_control", "0"); err != nil {
		d.log.Error("cannot take the fan from the kernel", "err", err)
		return 1
	}
	d.log.Info("fan control started", "version", version, "curve", d.ctl.Curve.String(), "hyst", d.ctl.Hyst,
		"hot", fan.HotC, "zones", d.zones)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var sum hourly
	giveBack := func(why string, args ...any) {
		err := d.writeFile("devices/pwm-fan/temp_control", "1")
		d.log.Info("fan control stopped: the kernel's control is back ("+why+")", append(args, "err", err)...)
	}
	for {
		if !d.step(&sum) {
			giveBack("cannot set the speed", "failures", d.failures)
			return 1
		}
		if exe, err := os.Readlink("/proc/self/exe"); err == nil && strings.HasSuffix(exe, " (deleted)") {
			giveBack("its binary was replaced or removed", "exe", exe)
			return 0
		}
		select {
		case s := <-stop:
			giveBack("signal " + s.String())
			return 0
		case <-tick.C:
		}
	}
}

// step: one reading, the speed and the state file. False: the speed could not be set maxFailures times in a row.
func (d *daemon) step(sum *hourly) bool {
	if len(d.zones) == 0 { // not there yet at boot, or never: look again (cheap)
		d.zones = d.findZones()
	}
	t, name, all, err := d.read()
	before := d.ctl.PWM()
	pwm := d.ctl.Next(t, err == nil)
	if pwm != d.written {
		if werr := d.writeFile("devices/pwm-fan/target_pwm", strconv.Itoa(pwm)); werr != nil {
			d.failures++
			d.log.Error("cannot set the fan's speed", "pwm", pwm, "err", werr, "failures", d.failures)
		} else {
			d.written, d.failures = pwm, 0
		}
	}
	switch {
	case err != nil && before != fan.MaxPWM:
		d.log.Warn("cannot read the temperature: fan at full speed", "err", err)
	case before == 0 && pwm > 0, before > 0 && pwm == 0, pwm == fan.MaxPWM && before != fan.MaxPWM:
		d.log.Info("fan", "pwm", pwm, "tempC", t, "sensor", name)
	}
	if err == nil { // a failed reading is no temperature for the hour's summary
		if line, ok := sum.add(t, pwm, time.Now()); ok {
			d.log.Info("fan, last hour", line...)
		}
	}
	st := State{PWM: pwm, TempC: t, Sensor: name, Sensors: all, Curve: d.ctl.Curve.String(),
		At: time.Now().UTC().Format(time.RFC3339)}
	if err != nil {
		st.Error = err.Error()
	}
	d.writeState(st)
	return d.failures < maxFailures
}

// findZones: thermal zone type → its temp file.
func (d *daemon) findZones() map[string]string {
	z := map[string]string{}
	dirs, _ := filepath.Glob(filepath.Join(d.sys, "class/thermal/thermal_zone*"))
	for _, dir := range dirs {
		typ := strings.TrimSpace(d.readPath(filepath.Join(dir, "type")))
		for _, s := range sensors {
			if typ == s {
				z[s] = filepath.Join(dir, "temp")
			}
		}
	}
	return z
}

// read: the hottest sensor (°C), its name, every reading, or an error if none could be read.
func (d *daemon) read() (float64, string, map[string]float64, error) {
	all := map[string]float64{}
	best, name := 0.0, ""
	var lastErr error
	for _, s := range sensors {
		p, ok := d.zones[s]
		if !ok {
			continue
		}
		raw := strings.TrimSpace(d.readPath(p))
		milli, err := strconv.Atoi(raw)
		if err != nil || milli < -40000 || milli > 150000 {
			lastErr = fmt.Errorf("%s: %q", s, raw)
			continue
		}
		c := float64(milli) / 1000
		all[s] = c
		if name == "" || c > best {
			best, name = c, s
		}
	}
	if name == "" {
		if lastErr == nil {
			lastErr = fmt.Errorf("no thermal zone among %v", sensors)
		}
		return 0, "", all, lastErr
	}
	return best, name, all, nil
}

func (d *daemon) readPath(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func (d *daemon) readFile(rel string) string { return d.readPath(filepath.Join(d.sys, rel)) }

func (d *daemon) writeFile(rel, v string) error {
	return os.WriteFile(filepath.Join(d.sys, rel), []byte(v), 0o644)
}

// writeState: the state file, replaced whole (a reader never sees half of it).
func (d *daemon) writeState(st State) {
	b, _ := json.Marshal(st)
	tmp := filepath.Join(d.run, ".state.json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err == nil {
		_ = os.Rename(tmp, filepath.Join(d.run, "state.json"))
	}
}

// hourly: once an hour, one line with the temperatures and speeds of that hour (the journal keeps the history
// without a line every 2 s).
type hourly struct {
	start          time.Time
	n              int
	minT, maxT, sT float64
	sPWM, maxPWM   int
}

func (h *hourly) add(t float64, pwm int, now time.Time) ([]any, bool) {
	if h.n == 0 {
		*h = hourly{start: now, minT: t, maxT: t}
	}
	h.n++
	h.sT += t
	h.sPWM += pwm
	if t < h.minT {
		h.minT = t
	}
	if t > h.maxT {
		h.maxT = t
	}
	if pwm > h.maxPWM {
		h.maxPWM = pwm
	}
	if now.Sub(h.start) < time.Hour {
		return nil, false
	}
	line := []any{"tempMinC", h.minT, "tempAvgC", round1(h.sT / float64(h.n)), "tempMaxC", h.maxT,
		"pwmAvg", h.sPWM / h.n, "pwmMax", h.maxPWM}
	h.n = 0
	return line, true
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
