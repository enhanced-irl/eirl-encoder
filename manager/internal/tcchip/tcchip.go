// Package tcchip finds the board's TC358743 chips (HDMI → CSI) in sysfs: one on CAM0, one on CAM1, or one
// on each (DEVELOPMENT.md, "HDMI over CSI on CAM0 and CAM1"). Nothing is fixed: the I2C bus, the v4l2 subdev
// and the VI's capture node of every chip come from what the driver registered, and its connector from the
// I2C mux channel the bus hangs from. The engine and the app both use it, so they agree on which chip is
// which.
package tcchip

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Chip is one TC358743 the driver found (a connector without a chip has none: its probe fails).
type Chip struct {
	Port   int    // camera connector: 0 = CAM0, 1 = CAM1 (the cam_i2cmux channel); -1 if unknown
	Bus    int    // I2C bus in Linux (7 and 8 with NVIDIA's DT)
	Name   string // the subdev's name: "tc358743 7-000f"
	Subdev string // /dev/v4l-subdevN
	Video  string // the VI's capture node, /dev/videoN ("vi-output, tc358743 7-000f"); "" if it has none
}

// Connector is the chip's connector as the board prints it: "CAM0", "CAM1", or "" if unknown.
func (c Chip) Connector() string {
	if c.Port < 0 {
		return ""
	}
	return "CAM" + strconv.Itoa(c.Port)
}

var (
	reClient = regexp.MustCompile(`^tc358743 ([0-9]{1,3})-00[0-9a-f]{2}$`)
	reChanID = regexp.MustCompile(`\(chan_id ([0-9])\)$`)
)

// Find lists the chips under root ("" = /), in connector order (unknown connectors last, by bus).
func Find(root string) []Chip {
	p := func(s string) string { return filepath.Join("/", root, s) }
	videos := map[string]string{} // subdev name → capture node
	nodes, _ := filepath.Glob(p("/sys/class/video4linux/video*/name"))
	for _, n := range nodes {
		if name := readTrim(n); strings.HasPrefix(name, "vi-output, ") {
			videos[strings.TrimPrefix(name, "vi-output, ")] = "/dev/" + filepath.Base(filepath.Dir(n))
		}
	}
	var chips []Chip
	subs, _ := filepath.Glob(p("/sys/class/video4linux/v4l-subdev*/name"))
	for _, n := range subs {
		name := readTrim(n)
		m := reClient.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		bus, _ := strconv.Atoi(m[1])
		c := Chip{Port: -1, Bus: bus, Name: name, Subdev: "/dev/" + filepath.Base(filepath.Dir(n)), Video: videos[name]}
		// "i2c-6-mux (chan_id 1)": the mux channel is the connector
		if mm := reChanID.FindStringSubmatch(readTrim(p("/sys/bus/i2c/devices/i2c-" + m[1] + "/name"))); mm != nil {
			c.Port, _ = strconv.Atoi(mm[1])
		}
		chips = append(chips, c)
	}
	sort.Slice(chips, func(i, j int) bool {
		a, b := chips[i], chips[j]
		if (a.Port < 0) != (b.Port < 0) {
			return b.Port < 0
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.Bus < b.Bus
	})
	return chips
}

// Wired values: HDMI_WIRED, the connector whose chip has the board's HDMI audio (the I2S4 of the 40-pin
// header) and reset pin wired to it. Auto ("") is enough with one chip, and means CAM0 with two.
const (
	WiredAuto = ""
	WiredCAM0 = "cam0"
	WiredCAM1 = "cam1"
)

// The files HDMI_WIRED is read from, in the engine's order: the board settings, then the app's file (it wins).
const (
	BoardSettings = "/etc/default/eirl-engine"
	AppSettings   = "/etc/eirl-encoder/engine.env"
)

// ReadWired is HDMI_WIRED from files (the last file that sets it to a valid value wins; within a file, its
// last valid line), auto if none does. An empty value is valid: it puts back auto.
func ReadWired(files ...string) string {
	w := WiredAuto
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "HDMI_WIRED="); ok && ValidWired(v) {
				w = v
			}
		}
		f.Close()
	}
	return w
}

// ValidWired reports whether s is a value of HDMI_WIRED.
func ValidWired(s string) bool { return s == WiredAuto || s == WiredCAM0 || s == WiredCAM1 }

// Wired is the chip wired to the I2S and the reset pin: the one on the connector the setting names, or
// with auto the only chip, or the first (CAM0) with two. ok=false without one: no chip, or the named
// connector is empty (then no chip has the board's HDMI audio).
func Wired(chips []Chip, setting string) (Chip, bool) {
	switch setting {
	case WiredCAM0, WiredCAM1:
		port := 0
		if setting == WiredCAM1 {
			port = 1
		}
		for _, c := range chips {
			if c.Port == port {
				return c, true
			}
		}
		return Chip{}, false
	}
	if len(chips) == 0 {
		return Chip{}, false
	}
	return chips[0], true
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
