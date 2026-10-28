package hw

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type System struct {
	CPUPct       []int              `json:"cpuPct"`
	GPUPct       int                `json:"gpuPct"`
	TempC        map[string]float64 `json:"tempC"`
	Power        *Power             `json:"power,omitempty"`
	Undervoltage bool               `json:"undervoltage"`
}

// Power: channels of the Jetson Nano's ina3221 (6-0040): 0 = POM_5V_IN (the 5 V input),
// 1 = POM_5V_GPU, 2 = POM_5V_CPU. By default only root can read them: the install's tmpfiles.d
// opens them for reading.
type Power struct {
	InMw  int64 `json:"inMw"`
	InMv  int64 `json:"inMv"`
	GPUMw int64 `json:"gpuMw"`
	CPUMw int64 `json:"cpuMw"`
}

// undervoltageMv: with the 2 A supply the input drops to 4.68-4.76 V while streaming (see DEVELOPMENT.md).
const undervoltageMv = 4800

// Sysfs reads the telemetry. It keeps the previous /proc/stat sample to give CPU % between calls
// (the first call gives 0).
type Sysfs struct {
	mu   sync.Mutex
	prev [][2]uint64 // per core: busy, total
}

var thermalNames = map[string]string{"CPU-therm": "cpu", "GPU-therm": "gpu", "PMIC-Die": "pmic", "AO-therm": "ao", "PLL-therm": "pll"}

func (s *Sysfs) Read() System {
	sys := System{TempC: map[string]float64{}, CPUPct: s.cpu()}
	zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*")
	for _, z := range zones {
		t, err := os.ReadFile(filepath.Join(z, "type"))
		if err != nil {
			continue
		}
		name, ok := thermalNames[strings.TrimSpace(string(t))]
		if !ok {
			continue
		}
		if v, err := readInt(filepath.Join(z, "temp")); err == nil {
			sys.TempC[name] = float64(v) / 1000
		}
	}
	if v, err := readInt("/sys/devices/gpu.0/load"); err == nil {
		sys.GPUPct = int(v / 10) // in tenths of a percent
	}
	if devs, _ := filepath.Glob("/sys/bus/i2c/drivers/ina3221x/*-0040/iio:device*"); len(devs) > 0 {
		d := devs[0]
		in, e1 := readInt(filepath.Join(d, "in_power0_input"))
		mv, e2 := readInt(filepath.Join(d, "in_voltage0_input"))
		if e1 == nil && e2 == nil {
			p := &Power{InMw: in, InMv: mv}
			p.GPUMw, _ = readInt(filepath.Join(d, "in_power1_input"))
			p.CPUMw, _ = readInt(filepath.Join(d, "in_power2_input"))
			sys.Power = p
			sys.Undervoltage = mv > 0 && mv < undervoltageMv
		}
	}
	return sys
}

func (s *Sysfs) cpu() []int {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil
	}
	defer f.Close()
	var cur [][2]uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") || fields[0] == "cpu" {
			continue
		}
		var total, idle uint64
		for i, v := range fields[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			total += n
			if i == 3 || i == 4 { // idle, iowait
				idle += n
			}
		}
		cur = append(cur, [2]uint64{total - idle, total})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pct := make([]int, len(cur))
	if len(s.prev) == len(cur) {
		for i := range cur {
			dt := cur[i][1] - s.prev[i][1]
			if dt > 0 {
				pct[i] = int((cur[i][0] - s.prev[i][0]) * 100 / dt)
			}
		}
	}
	s.prev = cur
	return pct
}
