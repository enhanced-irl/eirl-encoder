package screen

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

// SplashUnit is the boot splash; the screen waits until it lets go of the display (SPLASH.md §3, variant b).
const SplashUnit = "eirl-splash.service"

// Sys is what the screen reads from the board; tests replace it.
type Sys interface {
	// Mode is the HDMI screen's current mode, ok false without a screen.
	Mode() (w, h int, ok bool)
	// Uptime is the board's uptime.
	Uptime() (time.Duration, bool)
	// SplashActive: the boot splash still has the screen.
	SplashActive() bool
}

// BoardSys reads the Jetson's sysfs (Root "" = /) and asks systemd through Run.
type BoardSys struct {
	Root string
	Run  sysx.Runner
}

func (b BoardSys) path(p string) string { return filepath.Join(b.Root+"/", p) }

func readText(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	bs, _ := io.ReadAll(io.LimitReader(f, 4096))
	return strings.TrimSpace(string(bs))
}

// Mode is screen_mode: without debugfs (root only), the hotplug switch and the framebuffer's size, which
// follow the mode (MIXER.md §3, trap 5). 640x480 (no EDID yet) and tiny sizes are no screen.
func (b BoardSys) Mode() (int, int, bool) {
	if readText(b.path("/sys/class/switch/hdmi/state")) != "1" {
		return 0, 0, false
	}
	ws, hs, ok := strings.Cut(readText(b.path("/sys/class/graphics/fb0/virtual_size")), ",")
	if !ok {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(ws))
	h, err2 := strconv.Atoi(strings.TrimSpace(hs))
	if err1 != nil || err2 != nil || (w == 640 && h == 480) || w < 320 || h < 200 {
		return 0, 0, false
	}
	return w, h, true
}

// Uptime is /proc/uptime.
func (b BoardSys) Uptime() (time.Duration, bool) {
	f := strings.Fields(readText(b.path("/proc/uptime")))
	if len(f) == 0 {
		return 0, false
	}
	s, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(s * float64(time.Second)), true
}

// SplashActive: `is-active` does not do: in L4T's systemd 237 it says inactive also while deactivating,
// when the splash still paints the logo.
func (b BoardSys) SplashActive() bool {
	run := b.Run
	if run == nil {
		run = sysx.Run
	}
	r := run(5*time.Second, "systemctl", "show", "-p", "ActiveState", "--value", SplashUnit)
	st := strings.TrimSpace(r.Out)
	return r.RC == 0 && st != "inactive" && st != "failed" && st != ""
}

// justBooted: within 90 s of the board's boot the screen has been stable since tegradc set its mode.
func justBooted(s Sys) bool {
	up, ok := s.Uptime()
	return ok && up < 90*time.Second
}

// Sensors reads the CPU temperature (°C) and the board's 5 V input (V, rail 0 of the ina3221). The paths are
// looked up once.
type Sensors struct {
	Root     string
	once     sync.Once
	temp, mv string
}

func (s *Sensors) find() {
	root := s.Root + "/"
	zones, _ := filepath.Glob(filepath.Join(root, "/sys/devices/virtual/thermal/thermal_zone*"))
	sort.Strings(zones)
	for _, z := range zones {
		if readText(filepath.Join(z, "type")) == "CPU-therm" {
			s.temp = filepath.Join(z, "temp")
			break
		}
	}
	volts, _ := filepath.Glob(filepath.Join(root, "/sys/bus/i2c/drivers/ina3221x/*/iio:device0/in_voltage0_input"))
	sort.Strings(volts)
	if len(volts) > 0 {
		s.mv = volts[0]
	}
}

// Read is board_sensors.
func (s *Sensors) Read() (temp float64, okT bool, volts float64, okV bool) {
	s.once.Do(s.find)
	read := func(p string) (float64, bool) {
		if p == "" {
			return 0, false
		}
		v, err := strconv.Atoi(readText(p))
		if err != nil {
			return 0, false
		}
		return float64(v) / 1000, true
	}
	temp, okT = read(s.temp)
	volts, okV = read(s.mv)
	return
}
