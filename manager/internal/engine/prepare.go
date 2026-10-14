package engine

import (
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/hdmi"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

// Prepared is what the launcher's mixer_prepare decided before the program existed.
type Prepared struct {
	TC358743   bool   // the board has the HDMI input
	FPS        int    // program rate: 50 if every known source is a multiple of 25, else 60 (or FPS)
	MixerMain  string // hdmi | test | usb:auto | a by-id path
	Device     string // SOURCE=usb: the main USB device and its name and audio card
	DeviceName string
	ALSA       string
	Rates      []int // fps of the sources known at start
}

var (
	reRateKey  = regexp.MustCompile(`^SRC[0-9]_(FPS_NUM|FPS_DEN)$`)
	reRateVal  = regexp.MustCompile(`^[1-9][0-9]{0,5}$`)
	reDeviceID = regexp.MustCompile(`^/dev/v4l/by-(id|path)/[A-Za-z0-9_:-][A-Za-z0-9_.:-]{0,199}$`)
	reALSACard = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
)

// Prepare does the launcher's work before the pipeline: the HDMI's pending reset and EDID, then the
// program rate from the sources it can see now (the HDMI's current mode and `probe -all`), and the main
// source. A source without signal now does not count: the EDID does not say what the camera will send.
func Prepare(c *config.Config, run sysx.Runner, hs hdmi.Sys) Prepared {
	var p Prepared
	p.TC358743 = hs.Prepare(c.EDID, c.StateDir, c.VDEV)
	if p.TC358743 {
		if t, ok := hdmi.QueryDV(c.VDEV, run); ok {
			p.Rates = append(p.Rates, t.FPS)
		}
	}
	if fileExecutable(c.ProbeBin) {
		if r := run(15*time.Second, c.ProbeBin, "probe", "-all", "-max", strconv.Itoa(c.MixerSlots)); r.RC == 0 {
			p.Rates = append(p.Rates, ratesOfProbeAll(r.Out)...)
		} else {
			// A binary without -all: each USB on its own (what the engine will do as well)
			for _, dev := range sysx.Glob("/dev/v4l/by-id/*-video-index0") {
				if r := run(15*time.Second, c.ProbeBin, "probe", "-device", dev, "-format", "auto", "-mode", "auto"); r.RC == 0 {
					p.Rates = append(p.Rates, ratesOfProbe(r.Out)...)
				}
			}
		}
		// The main USB source's audio (AUDIO_SOURCE=auto): its card, without waiting for it
		if c.Source == "usb" {
			if r := run(15*time.Second, c.ProbeBin, "probe", "-device", c.VideoDevice, "-format", "auto", "-mode", "auto"); r.RC == 0 {
				p.Device, p.DeviceName, p.ALSA = inputOfProbe(r.Out)
			}
		}
	}
	p.FPS = chooseFPS(p.Rates, c)
	p.MixerMain = c.MixerMain(p.Device)
	rates := "none"
	if len(p.Rates) > 0 {
		var s []string
		for _, r := range p.Rates {
			s = append(s, strconv.Itoa(r))
		}
		rates = strings.Join(s, ",")
	}
	elog.Event("engine", "mixer_config", "slots", c.MixerSlots, "fps", p.FPS, "known_rates", rates, "on_air", p.MixerMain)
	return p
}

// chooseFPS is D10: 50 if every known source is a multiple of 25 (PAL), else 60 (the 30 fps ones go
// out twice); FPS in the config wins.
func chooseFPS(rates []int, c *config.Config) int {
	if c.FPSSet {
		return c.FPS
	}
	pal, ntsc := false, false
	for _, f := range rates {
		if f%25 == 0 {
			pal = true
		} else {
			ntsc = true
		}
	}
	if pal && !ntsc {
		return 50
	}
	return 60
}

// ratesOfProbeAll: the rounded fps of each source of `probe -all` (SRCn_FPS_NUM / SRCn_FPS_DEN).
func ratesOfProbeAll(out string) []int {
	kv := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, "=")
		if reRateKey.MatchString(k) && reRateVal.MatchString(v) {
			kv[k], _ = strconv.Atoi(v)
		}
	}
	var rates []int
	for i := range 4 {
		n, d := kv["SRC"+strconv.Itoa(i)+"_FPS_NUM"], kv["SRC"+strconv.Itoa(i)+"_FPS_DEN"]
		if n > 0 && d > 0 {
			rates = append(rates, (n+d/2)/d)
		}
	}
	return rates
}

func ratesOfProbe(out string) []int {
	var n, d int
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, "=")
		if !reRateVal.MatchString(v) {
			continue
		}
		switch k {
		case "FPS_NUM":
			n, _ = strconv.Atoi(v)
		case "FPS_DEN":
			d, _ = strconv.Atoi(v)
		}
	}
	if n > 0 && d > 0 {
		return []int{(n + d/2) / d}
	}
	return nil
}

func inputOfProbe(out string) (dev, name, alsa string) {
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "DEVICE":
			if reDeviceID.MatchString(v) {
				dev = v
			}
		case "NAME":
			name = config.SanitizeDeviceName(v)
		case "ALSA_CARD":
			if reALSACard.MatchString(v) {
				alsa = v
			}
		}
	}
	return dev, name, alsa
}

// fileExecutable is the launcher's [ -x ].
func fileExecutable(p string) bool { return syscall.Access(p, 0x1) == nil }
