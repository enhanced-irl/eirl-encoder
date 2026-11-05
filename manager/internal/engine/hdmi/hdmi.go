// Package hdmi is the board's HDMI input (TC358743 on CSI): its mode (DV timings), fixing that mode
// before capturing, and the EDID and hardware reset the launcher did before the pipeline started.
package hdmi

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/tcchip"
)

// ErrNotImplemented: the driver does not answer VIDIOC_QUERY_DV_TIMINGS; use v4l2-ctl.
var ErrNotImplemented = errors.New("dv timings ioctl not available")

var (
	reActiveW = regexp.MustCompile(`Active width:\s*(\d+)`)
	reActiveH = regexp.MustCompile(`Active height:\s*(\d+)`)
	reFPS     = regexp.MustCompile(`\(([0-9.]+) frames per second\)`)
	reIL      = regexp.MustCompile(`Frame format:\s*interlaced`)
	reTotalW  = regexp.MustCompile(`Total width:\s*(\d+)`)
	rePClk    = regexp.MustCompile(`Pixelclock:\s*(\d+) Hz`)
)

func num(re *regexp.Regexp, s string) int64 {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	return n
}

// round is Python's round(): half to even.
func round(f float64) int { return int(math.RoundToEven(f)) }

// ParseDV reads `v4l2-ctl --query-dv-timings`; ok=false without a signal.
func ParseDV(text string) (pipeline.Timings, bool) {
	w, h := num(reActiveW, text), num(reActiveH, text)
	m := reFPS.FindStringSubmatch(text)
	if w == 0 || h == 0 || m == nil {
		return pipeline.Timings{}, false
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	return pipeline.Timings{W: int(w), H: int(h), FPS: round(f), Interlaced: reIL.MatchString(text),
		HTotal: int(num(reTotalW, text)), PClk: num(rePClk, text)}, true
}

// FromBT reads a struct v4l2_dv_timings (132 bytes: type, then the packed v4l2_bt_timings) like
// v4l2-ctl does: fps = pixelclock / (total width × total height). ok=false if it is empty.
func FromBT(raw []byte) (pipeline.Timings, bool) {
	if len(raw) < 4+4*4+8+9*4 {
		return pipeline.Timings{}, false
	}
	le := binary.LittleEndian
	u := func(off int) int64 { return int64(le.Uint32(raw[off:])) }
	w, h, il := u(4), u(8), u(12)
	pclk := int64(le.Uint64(raw[20:]))
	hfp, hs, hbp, vfp, vs, vbp, ivfp, ivs, ivbp := u(28), u(32), u(36), u(40), u(44), u(48), u(52), u(56), u(60)
	htotal := w + hfp + hs + hbp
	vtotal := h + vfp + vs + vbp
	if il != 0 {
		vtotal += ivfp + ivs + ivbp
	}
	if w == 0 || h == 0 || htotal == 0 || vtotal == 0 || pclk == 0 {
		return pipeline.Timings{}, false
	}
	return pipeline.Timings{W: int(w), H: int(h), FPS: round(float64(pclk) / float64(htotal*vtotal)), Interlaced: il != 0,
		HTotal: int(htotal), PClk: pclk}, true
}

// QueryDV is the current mode: the ioctl, or v4l2-ctl when the driver does not answer it. ok=false
// without a signal.
func QueryDV(node string, run sysx.Runner) (pipeline.Timings, bool) {
	t, ok, err := queryIoctl(node)
	if !errors.Is(err, ErrNotImplemented) {
		return t, ok
	}
	r := run(5*time.Second, "v4l2-ctl", "-d", node, "--query-dv-timings")
	if r.RC != 0 {
		return pipeline.Timings{}, false
	}
	return ParseDV(r.Out)
}

// SetMode fixes the capture to the signal's mode before opening it. "" on success, else stderr (≤120).
func SetMode(node string, t pipeline.Timings, run sysx.Runner) string {
	r1 := run(5*time.Second, "v4l2-ctl", "-d", node, "--set-dv-bt-timings", "query")
	r2 := run(5*time.Second, "v4l2-ctl", "-d", node,
		"--set-fmt-video=width="+strconv.Itoa(t.W)+",height="+strconv.Itoa(t.H)+",pixelformat=UYVY")
	if r1.RC == 0 && r2.RC == 0 {
		return ""
	}
	e := r1.Err
	if e == "" {
		e = r2.Err
	}
	if len(e) > 120 {
		e = e[:120]
	}
	return e
}

// Sys is where the board exposes things; tests point it at a temporary tree.
type Sys struct {
	Root string // "" = /
	Run  sysx.Runner
	// Sleep is time.Sleep (tests make it instant)
	Sleep func(time.Duration)
}

func (s Sys) path(p string) string { return filepath.Join(s.Root, p) }

// Chips are the board's TC358743s, in connector order (tcchip): none, CAM0's, CAM1's or both.
func (s Sys) Chips() []tcchip.Chip { return tcchip.Find(s.Root) }

// WiredSubdev is the v4l-subdev node of the chip wired to the I2S audio and the reset pin (wired is
// HDMI_WIRED, tcchip.Wired), "" if the board has none.
func (s Sys) WiredSubdev(wired string) string {
	if c, ok := tcchip.Wired(s.Chips(), wired); ok {
		return c.Subdev
	}
	return ""
}

// HasResetPin: the chip has a hardware reset. The device tree must declare the pin, and the driver must
// have given the chip its "Hardware Reset" control: with two chips both nodes may declare the one pin,
// and only the chip that holds it gets the control (tc358743-jetson.patch). A driver from before that
// change gives every chip the control, so the device tree is checked as well.
func (s Sys) HasResetPin(sub string) bool {
	if sub == "" || !sysx.Exists(s.path("/sys/class/video4linux/"+filepath.Base(sub)+"/device/of_node/reset-gpios")) {
		return false
	}
	r := s.Run(5*time.Second, "v4l2-ctl", "-d", sub, "--list-ctrls")
	return r.RC == 0 && strings.Contains(r.Out, "hardware_reset")
}

// Replug rewrites the EDID (edidFile, the one Prepare loaded) of the chip wired to the I2S (wired is
// HDMI_WIRED): the driver drops the hotplug and raises it again, and the source renegotiates HDMI from
// scratch, audio included. It cuts that HDMI's video for a moment: only with nothing of it on air. False
// if it could not be done.
func (s Sys) Replug(edidFile, wired string) bool {
	sub := s.WiredSubdev(wired)
	if sub == "" || !sysx.Exists(edidFile) {
		return false
	}
	elog.Event("engine", "edid_replug", "file", filepath.Base(edidFile))
	return s.Run(10*time.Second, "v4l2-ctl", "-d", sub, "--set-edid=pad=0,file="+edidFile+",format=raw").RC == 0
}

// Prepare does what the launcher did before the pipeline: a hardware reset asked for by the previous
// run (STATE_DIR/hw-reset.request, with the pipeline stopped) on the chip wired to the reset pin (wired is
// HDMI_WIRED), and the EDID of every chip, rewritten only where it changed (writing it drops the hotplug
// and the source renegotiates). It returns whether the board has a TC358743.
func (s Sys) Prepare(edidFile, stateDir, wired string) bool {
	chips := s.Chips()
	if len(chips) == 0 {
		return false
	}
	if stateDir != "" {
		req := filepath.Join(stateDir, "hw-reset.request")
		if sysx.Exists(req) {
			os.Remove(req)
			sub := s.WiredSubdev(wired)
			if sub != "" && s.Run(10*time.Second, "v4l2-ctl", "-d", sub, "-c", "hardware_reset=1").RC == 0 {
				_ = os.WriteFile(filepath.Join(stateDir, "hw-reset.last"), []byte(strconv.FormatInt(time.Now().Unix(), 10)+"\n"), 0o644)
				elog.Event("engine", "tc358743_hw_reset_done")
				s.Sleep(3 * time.Second) // the driver drops and raises the hotplug: the source takes a while
			} else {
				elog.Event("engine", "tc358743_hw_reset_failed")
			}
		}
	}
	want, err := os.ReadFile(edidFile)
	if err != nil {
		elog.Event("engine", "edid_missing", "file", edidFile)
		return true
	}
	for _, c := range chips {
		s.loadEDID(c, edidFile, want)
	}
	return true
}

// loadEDID writes edidFile into the chip unless it already has it, and waits for the signal to drop.
func (s Sys) loadEDID(c tcchip.Chip, edidFile string, want []byte) {
	tmp, err := os.CreateTemp("", "eirl-edid-")
	if err == nil {
		tmp.Close()
		defer os.Remove(tmp.Name())
		s.Run(10*time.Second, "v4l2-ctl", "-d", c.Subdev, "--get-edid=pad=0,file="+tmp.Name())
	}
	var cur []byte
	if tmp != nil {
		cur, _ = os.ReadFile(tmp.Name())
	}
	// v4l2-ctl writes the EDID as hex text; compare like the launcher (no spaces, lower case)
	clean := strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(strings.ToLower(string(cur)))
	if clean == hex.EncodeToString(want) {
		return
	}
	elog.Event("engine", "edid_loaded", "file", filepath.Base(edidFile), "chip", c.Name)
	s.Run(10*time.Second, "v4l2-ctl", "-d", c.Subdev, "--set-edid=pad=0,file="+edidFile+",format=raw")
	if c.Video == "" {
		return
	}
	// Until the signal drops, the timings read are those of the old EDID: wait for it to drop (≤5 s; a
	// source that ignores the hotplug does not drop it) and let the source pick it up renegotiated
	for range 10 {
		if r := s.Run(5*time.Second, "v4l2-ctl", "-d", c.Video, "--query-dv-timings"); r.RC != 0 {
			break
		}
		s.Sleep(500 * time.Millisecond)
	}
}
