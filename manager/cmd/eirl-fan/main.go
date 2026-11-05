// eirl-fan: the encoder's fan control (FAN.md), a root daemon run by eirl-fan.service.
//
// Every 2 s it reads the hottest of the chip's CPU and GPU sensors and sets the fan's speed from the curve of
// internal/fan, writing /sys/devices/pwm-fan/target_pwm. While it runs, the kernel's own control
// (temp_control) is off; it is given back when the daemon stops (and by the unit's ExecStopPost if it dies),
// so the fan is never left without a controller. A reading it cannot make runs the fan at full speed. It also gives
// the fan back and exits when it cannot set the speed 5 times in a row (systemd tries again), and when its own binary
// is replaced or removed (an update restarts it; a rollback to a release without it may leave no unit to do so).
//
// On a board with a second fan (FAN.md §5; EIRL_FAN2=on in /etc/default/eirl-fan), a fan that is only on or off, on
// pin 32 of the 40-pin header (gpio168): from the same reading it is switched on above one temperature and off below
// a lower one, never for less than a minimum time on. Switched off in Settings (the app's fan.ControlFile, until the
// next boot), only the safety switches it on. It is left as it is when the daemon stops or dies: the next start takes
// it from there, and the main fan, back in the kernel's control, is what keeps the chip safe.
//
//	eirl-fan            the daemon
//	eirl-fan -once      one reading: prints the temperatures and what it would set, and touches nothing
//	eirl-fan -claim     the second fan's pin as an output (the unit's ExecStartPre, outside its sandbox)
//
// What it is doing, for the app and for people: /run/eirl-fan/state.json.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/fan"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

var version = "dev"

// sensors: the thermal zones it reads (types in /sys/class/thermal/thermal_zone*/type). The chip's CPU and
// GPU, the parts that throttle; AO-therm reads some degrees above them and is not where the heat is made.
var sensors = []string{"CPU-therm", "GPU-therm"}

// fan2GPIO: the second fan's pin, pin 32 of the 40-pin header (PV0), IN1 of its DRV8833 (HARDWARE.md §6). Fixed:
// eirl-fan.service gives the daemon write access to this pin only.
const fan2GPIO = 168

// fan2Dir: the pin's sysfs directory once exported, under the sysfs root.
var fan2Dir = filepath.Join("class/gpio", "gpio"+strconv.Itoa(fan2GPIO))

type daemon struct {
	sys, run string // /sys and /run (other roots in tests)
	zones    map[string]string
	ctl      fan.Controller
	log      *slog.Logger
	now      func() time.Time
	written  int
	failures int // target_pwm writes failed in a row

	fan2    *fan.Switch // nil: no second fan
	fan2Cfg string      // what was wrong in its settings ("" nothing)
	fan2Set int         // the level last written to its pin, or found there: -1 unknown
	fan2Err string      // the last error setting its pin ("" none)
	fan2Why string      // why it is on

	ctlPath string // the app's request for the second fan (fan.ControlFile), reread at every reading
	appUID  uint32 // whose control file is trusted (besides root's): the app's user
	bootID  string // this boot (a ModeOff request only holds for the boot it was made in)
	ctlErr  string // why the control file could not be used ("" none), logged once
}

// maxFailures: target_pwm writes failed in a row before it gives the fan back to the kernel and exits.
const maxFailures = 5

func main() {
	once := flag.Bool("once", false, "one reading; print it and touch nothing")
	claim := flag.Bool("claim", false, "make the second fan's pin an output, at the level the temperature asks for")
	interval := flag.Duration("interval", 2*time.Second, "time between readings")
	sysRoot := flag.String("sys", "/sys", "sysfs root (tests)")
	runRoot := flag.String("run", "/run/eirl-fan", "state directory")
	ctlPath := flag.String("control", fan.ControlFile, "the app's request for the second fan")
	bootPath := flag.String("boot-id", fan.BootIDFile, "this boot's id")
	appUser := flag.String("app-user", "eirl-encoder", "the user whose control file is trusted (besides root)")
	flag.Parse()
	d := &daemon{sys: *sysRoot, run: *runRoot, ctl: fan.Controller{Curve: fan.Default, Hyst: 3},
		log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey { // the journal stamps every line
					return slog.Attr{}
				}
				return a
			}})), now: time.Now, written: -1, fan2Set: -1, ctlPath: *ctlPath}
	d.appUID, d.bootID = trustedUID(*appUser), readBootID(*bootPath)
	safefile.OnUntrusted = func(path, reason string) { d.controlProblem(path + ": " + reason) }
	if err := d.ctl.Curve.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "eirl-fan: the built-in curve is wrong:", err)
		os.Exit(1)
	}
	d.fan2, d.fan2Cfg = fan2Settings(os.Getenv)
	d.zones = d.findZones()
	switch {
	case *claim:
		if d.fan2Cfg != "" {
			d.log.Error("second fan settings", "err", d.fan2Cfg)
		}
		if err := d.claim(); err != nil {
			d.log.Error("cannot make the second fan's pin an output", "gpio", fan2GPIO, "err", err)
			os.Exit(1)
		}
	case *once:
		d.printOnce()
	default:
		os.Exit(d.loop(*interval))
	}
}

func (d *daemon) printOnce() {
	t, name, all, err := d.read()
	pwm := (&fan.Controller{Curve: d.ctl.Curve, Hyst: d.ctl.Hyst}).Next(t, err == nil)
	fmt.Printf("sensors %v · hottest %s %.1f °C · curve %s · would set %d (now %s)\n", all, name, t,
		d.ctl.Curve, pwm, strings.TrimSpace(d.readFile("devices/pwm-fan/cur_pwm")))
	if err != nil {
		fmt.Println("reading error:", err)
	}
	if d.fan2Cfg != "" {
		fmt.Println("second fan settings:", d.fan2Cfg)
	}
	if d.fan2 == nil {
		return
	}
	out, on, perr := d.fan2Pin()
	pin := fmt.Sprintf("an output, %s", onOff(on))
	switch {
	case perr != nil:
		pin = "not available: " + perr.Error()
	case !out:
		pin = "an input (wherever its level shifter left it)"
	}
	sw := *d.fan2
	sw.Start(out && on, d.now())
	fmt.Printf("second fan (gpio%d): %s · would be %s · its pin is %s\n", fan2GPIO, sw.String(),
		onOff(sw.Next(t, err == nil, d.now())), pin)
}

func (d *daemon) loop(interval time.Duration) int {
	if len(d.zones) == 0 {
		d.log.Error("no CPU or GPU thermal zone: the fan runs at full speed", "wanted", sensors)
	}
	if err := d.writeFile("devices/pwm-fan/temp_control", "0"); err != nil {
		d.log.Error("cannot take the fan from the kernel", "err", err)
		d.leaveFan2()
		return 1
	}
	d.log.Info("fan control started", "version", version, "curve", d.ctl.Curve.String(), "hyst", d.ctl.Hyst,
		"hot", fan.HotC, "zones", d.zones)
	d.startFan2()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var sum hourly
	giveBack := func(why string, args ...any) {
		err := d.writeFile("devices/pwm-fan/temp_control", "1")
		d.log.Info("fan control stopped: the kernel's control is back ("+why+")", append(args, "err", err)...)
		d.leaveFan2()
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
	now := d.now()
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
	var f2 *fan2Sample
	if d.fan2 != nil {
		f2 = d.stepFan2(t, name, err == nil, now)
	}
	if err == nil { // a failed reading is no temperature for the hour's summary
		if line, ok := sum.add(t, pwm, f2, now); ok {
			d.log.Info("fan, last hour", line...)
		}
	}
	st := fan.State{PWM: pwm, TempC: t, Sensor: name, Sensors: all, Curve: d.ctl.Curve.String(),
		At: now.UTC().Format(time.RFC3339), Fan2: d.fan2State()}
	if err != nil {
		st.Error = err.Error()
	}
	d.writeState(st)
	return d.failures < maxFailures
}

// fan2Settings: the second fan's switch from the environment (eirl-fan.service reads /etc/default/eirl-fan); nil
// unless EIRL_FAN2=on. With a value it cannot use, the defaults, and what was wrong.
func fan2Settings(getenv func(string) string) (*fan.Switch, string) {
	switch v := getenv("EIRL_FAN2"); v {
	case "on":
	case "", "off":
		return nil, ""
	default:
		return nil, fmt.Sprintf("EIRL_FAN2=%q is neither on nor off: no second fan", v)
	}
	sw := fan.DefaultSwitch()
	var bad []string
	for _, k := range []struct {
		name string
		dst  *float64
	}{{"EIRL_FAN2_ON_C", &sw.OnC}, {"EIRL_FAN2_OFF_C", &sw.OffC}} {
		if v := getenv(k.name); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err != nil {
				bad = append(bad, fmt.Sprintf("%s=%q is not a temperature", k.name, v))
			} else {
				*k.dst = f
			}
		}
	}
	if v := getenv("EIRL_FAN2_MIN_ON"); v != "" {
		if dur, err := time.ParseDuration(v); err != nil {
			bad = append(bad, fmt.Sprintf("EIRL_FAN2_MIN_ON=%q is not a duration (2m, 90s)", v))
		} else {
			sw.MinOn = dur
		}
	}
	if len(bad) == 0 {
		if err := sw.Validate(); err != nil {
			bad = append(bad, err.Error())
		}
	}
	if len(bad) > 0 {
		def := fan.DefaultSwitch()
		return &def, strings.Join(bad, "; ") + ": the defaults are used (" + def.String() + ")"
	}
	return &sw, ""
}

// claim: the second fan's pin as an output (eirl-fan -claim, the unit's ExecStartPre, run outside the sandbox
// because exporting is allowed for any pin; the daemon can then write this one only). Exported if it is not; made an
// output if it is not one yet, at the level the temperature asks for, by writing "high" or "low" to direction (level
// and direction at once, no glitch). An input pin of the header keeps whatever level it last had, through its
// TXB0108 level shifter, so at boot the fan may be on or off: this is what decides it. An output is left as it is
// (the daemon starts from it), and the pin is never given back (unexported), which would make it an input again.
func (d *daemon) claim() error {
	if d.fan2 == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(d.sys, fan2Dir, "value")); err != nil {
		if err := d.writeFile("class/gpio/export", strconv.Itoa(fan2GPIO)); err != nil {
			return fmt.Errorf("export: %w", err)
		}
	}
	if out, on, err := d.fan2Pin(); err == nil && out {
		d.log.Info("second fan's pin already an output", "gpio", fan2GPIO, "on", on)
		return nil
	}
	t, name, _, err := d.read()
	sw := *d.fan2
	sw.Start(false, d.now())
	on := sw.Next(t, err == nil, d.now())
	if werr := d.writeFile(filepath.Join(fan2Dir, "direction"), level(on)); werr != nil {
		return fmt.Errorf("direction: %w", werr)
	}
	d.log.Info("second fan's pin made an output", "gpio", fan2GPIO, "on", on, "tempC", t, "sensor", name,
		"readingErr", err)
	return nil
}

// startFan2: the daemon starts from the pin as it finds it (the claim made it an output at the level the temperature
// asked for, or a stop left it on), so a restart does not click the fan, and one found on stays on for MinOn.
func (d *daemon) startFan2() {
	if d.fan2Cfg != "" {
		d.log.Error("second fan settings", "err", d.fan2Cfg)
	}
	if d.fan2 == nil {
		return
	}
	out, on, err := d.fan2Pin()
	if err == nil && out {
		d.fan2Set = b2i(on)
	} else {
		on = false
	}
	d.fan2.Start(on, d.now())
	if on {
		d.fan2Why = "start"
	}
	d.log.Info("second fan", "gpio", fan2GPIO, "onC", d.fan2.OnC, "offC", d.fan2.OffC, "minOn", d.fan2.MinOn,
		"output", out, "on", on, "err", err)
}

// fan2Sample: the second fan in one reading, for the hour's summary.
type fan2Sample struct{ on, switched bool }

// stepFan2: the second fan for this reading, and its pin set to it.
func (d *daemon) stepFan2(t float64, name string, ok bool, now time.Time) *fan2Sample {
	was, since := d.fan2.On(), d.fan2.Since()
	if off := d.control() == fan.ModeOff; off != d.fan2.Off() {
		d.fan2.SetOff(off, now)
		if off {
			d.log.Info("second fan switched off in Settings, until the next boot", "tempC", t, "sensor", name,
				"stillOnAtC", fan.HotC)
		} else {
			d.log.Info("second fan back to automatic", "tempC", t, "sensor", name)
		}
	}
	on := d.fan2.Next(t, ok, now)
	switch {
	case on && !was:
		d.fan2Why = "temp"
		if !ok || math.IsNaN(t) {
			d.fan2Why = "reading"
		} else if t >= fan.HotC {
			d.fan2Why = "hot"
		}
		d.log.Info("second fan on", "tempC", t, "sensor", name, "reason", d.fan2Why)
	case !on && was:
		d.log.Info("second fan off", "tempC", t, "sensor", name, "onFor", now.Sub(since).Round(time.Second),
			"mode", d.fan2State().Mode)
	}
	d.setFan2(on)
	return &fan2Sample{on: on, switched: on != was}
}

// setFan2: the pin at the level asked for. Checked against what it is, not only written on a change: it is also put
// back if something else moved it or made it an input.
func (d *daemon) setFan2(on bool) {
	out, cur, err := d.fan2Pin()
	if err == nil && d.fan2Set >= 0 && (!out || b2i(cur) != d.fan2Set) {
		d.log.Warn("the second fan's pin was changed outside eirl-fan: set again", "output", out, "on", cur)
	}
	if err == nil && !(out && cur == on) {
		if out {
			err = d.writeFile(filepath.Join(fan2Dir, "value"), strconv.Itoa(b2i(on)))
		} else {
			err = d.writeFile(filepath.Join(fan2Dir, "direction"), level(on))
		}
	}
	if err != nil {
		if msg := err.Error(); msg != d.fan2Err { // once per error, not every 2 s
			d.log.Error("cannot set the second fan", "on", on, "err", err)
			d.fan2Err = msg
		}
		d.fan2Set = -1
		return
	}
	d.fan2Set = b2i(on)
	if d.fan2Err != "" {
		d.log.Info("the second fan can be set again", "on", on)
		d.fan2Err = ""
	}
}

// leaveFan2: the second fan is left as it is when the daemon stops. Switching it on there made every restart,
// update and shutdown loud for no need: a restart takes it from where it is (startFan2), and a daemon that does not
// come back leaves the main fan in the kernel's control, which keeps the chip safe on its own.
func (d *daemon) leaveFan2() {
	if d.fan2 == nil {
		return
	}
	_, on, err := d.fan2Pin()
	d.log.Info("second fan left as it is", "on", on, "err", err)
}

func (d *daemon) fan2State() *fan.Fan2State {
	if d.fan2 == nil {
		return nil
	}
	mode := fan.ModeAuto
	if d.fan2.Off() {
		mode = fan.ModeOff
	}
	f := &fan.Fan2State{On: d.fan2.On(), Since: d.fan2.Since().UTC().Format(time.RFC3339), Mode: mode,
		OnC: d.fan2.OnC, OffC: d.fan2.OffC, HotC: fan.HotC, MinOnS: int(d.fan2.MinOn / time.Second), GPIO: fan2GPIO,
		Error: d.fan2Err, ConfigError: d.fan2Cfg, ControlErr: d.ctlErr}
	if f.On {
		f.Reason = d.fan2Why
	}
	return f
}

// control: the second fan's mode the app asks for (fan.ControlFile), as data: only a regular file of the app's user or
// of root, ModeOff only for this boot. Missing, untrusted or unreadable: automatic (the last two logged once).
func (d *daemon) control() string {
	if d.ctlPath == "" {
		return fan.ModeAuto
	}
	b, err := safefile.ReadFrom(d.ctlPath, fan.MaxControlSize, d.appUID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			d.controlProblem(err.Error())
		} else if !errors.Is(err, safefile.ErrUntrusted) {
			d.ctlErr = "" // no file: nothing asked
		}
		return fan.ModeAuto
	}
	c, err := fan.ParseControl(b)
	if err != nil {
		d.controlProblem(d.ctlPath + ": " + err.Error())
		return fan.ModeAuto
	}
	d.ctlErr = ""
	if d.bootID == "" {
		return fan.ModeAuto // which boot the request is for cannot be told: automatic, the safe side
	}
	return c.Mode(d.bootID)
}

// controlProblem: the control file could not be used: logged once per problem, and in state.json.
func (d *daemon) controlProblem(why string) {
	if why != d.ctlErr {
		d.log.Warn("the second fan's control file is not used: automatic", "why", why)
		d.ctlErr = why
	}
}

// trustedUID: the uid of the app's user, whose control file is trusted; root's (0) if there is no such user.
func trustedUID(name string) uint32 {
	if u, err := user.Lookup(name); err == nil {
		if id, err := strconv.ParseUint(u.Uid, 10, 32); err == nil {
			return uint32(id)
		}
	}
	return 0
}

// readBootID: this boot's id, "" if it cannot be read (then the app's "off" is never applied).
func readBootID(path string) string {
	id, _ := fan.ReadBootID(path)
	return id
}

// fan2Pin: the second fan's pin as it is: whether it is an output, and its level (an output reads back the level it
// is set to).
func (d *daemon) fan2Pin() (out, on bool, err error) {
	dir, err := os.ReadFile(filepath.Join(d.sys, fan2Dir, "direction"))
	if err != nil {
		return false, false, err
	}
	v, err := os.ReadFile(filepath.Join(d.sys, fan2Dir, "value"))
	if err != nil {
		return false, false, err
	}
	return strings.TrimSpace(string(dir)) == "out", strings.TrimSpace(string(v)) == "1", nil
}

func level(on bool) string {
	if on {
		return "high"
	}
	return "low"
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
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
func (d *daemon) writeState(st fan.State) {
	b, _ := json.Marshal(st)
	tmp := filepath.Join(d.run, ".state.json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err == nil {
		_ = os.Rename(tmp, filepath.Join(d.run, "state.json"))
	}
}

// hourly: once an hour, one line with the temperatures and speeds of that hour (the journal keeps the history
// without a line every 2 s), and with a second fan, how much of the hour it was on and how many times it switched.
type hourly struct {
	start          time.Time
	n              int
	minT, maxT, sT float64
	sPWM, maxPWM   int
	f2n, f2on, f2s int
}

// add: one reading (f2: the second fan in it, nil without one).
func (h *hourly) add(t float64, pwm int, f2 *fan2Sample, now time.Time) ([]any, bool) {
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
	if f2 != nil {
		h.f2n++
		h.f2on += b2i(f2.on)
		h.f2s += b2i(f2.switched)
	}
	if now.Sub(h.start) < time.Hour {
		return nil, false
	}
	line := []any{"tempMinC", h.minT, "tempAvgC", round1(h.sT / float64(h.n)), "tempMaxC", h.maxT,
		"pwmAvg", h.sPWM / h.n, "pwmMax", h.maxPWM}
	if h.f2n > 0 {
		line = append(line, "fan2OnPct", h.f2on*100/h.f2n, "fan2Switches", h.f2s)
	}
	h.n = 0
	return line, true
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
