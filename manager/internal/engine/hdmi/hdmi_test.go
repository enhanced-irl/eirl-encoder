package hdmi

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

const dv1080p60 = "\tActive width: 1920\n\tActive height: 1080\n\tTotal width: 2200\n\tTotal height: 1125\n" +
	"\tFrame format: progressive\n\tPolarities: +vsync +hsync\n\tPixelclock: 148500000 Hz (60.00 frames per second)\n"

// ParseTest.test_dv
func TestParseDV(t *testing.T) {
	tm, ok := ParseDV(dv1080p60)
	if !ok || tm.W != 1920 || tm.H != 1080 || tm.FPS != 60 || tm.Interlaced {
		t.Fatalf("1080p60: %+v %v", tm, ok)
	}
	if pipeline.CSILoad(tm) <= 95 {
		t.Error("1080p60 must not fit in 2 lanes")
	}
	p50, _ := ParseDV(strings.NewReplacer("2200", "2640", "60.00 frames", "50.00 frames").Replace(dv1080p60))
	if pipeline.CSILoad(p50) > 95 {
		t.Error("1080p50 fits")
	}
	il, _ := ParseDV(strings.NewReplacer("progressive", "interlaced", "60.00 frames", "30.00 frames").Replace(dv1080p60))
	if !il.Interlaced {
		t.Error("interlaced")
	}
	if _, ok := ParseDV("VIDIOC_QUERY_DV_TIMINGS: failed: No link has been detected"); ok {
		t.Error("no signal")
	}
}

func bt(w, h uint32, pclk uint64, hfp, hs, hbp, vfp, vs, vbp uint32, il uint32) []byte {
	raw := make([]byte, 132)
	le := binary.LittleEndian
	le.PutUint32(raw[4:], w)
	le.PutUint32(raw[8:], h)
	le.PutUint32(raw[12:], il)
	le.PutUint32(raw[16:], 3)
	le.PutUint64(raw[20:], pclk)
	for i, v := range []uint32{hfp, hs, hbp, vfp, vs, vbp} {
		le.PutUint32(raw[28+4*i:], v)
	}
	return raw
}

// DvTimingsTest.test_like_v4l2_ctl
func TestDvTimingsLikeV4l2Ctl(t *testing.T) {
	got, _ := FromBT(bt(1920, 1080, 148500000, 88, 44, 148, 4, 5, 36, 0))
	if (got != pipeline.Timings{W: 1920, H: 1080, FPS: 60, HTotal: 2200, PClk: 148500000}) {
		t.Errorf("1080p60: %+v", got)
	}
	p50, _ := FromBT(bt(1920, 1080, 148500000, 528, 44, 148, 4, 5, 36, 0))
	if p50.FPS != 50 || pipeline.CSILoad(p50) != 88 {
		t.Errorf("1080p50: %+v load %d", p50, pipeline.CSILoad(p50))
	}
	p720, _ := FromBT(bt(1280, 720, 74250000, 110, 40, 220, 5, 5, 20, 0))
	if p720.W != 1280 || p720.H != 720 || p720.FPS != 60 || p720.HTotal != 1650 {
		t.Errorf("720p60: %+v", p720)
	}
	if i, _ := FromBT(bt(1920, 1080, 74250000, 88, 44, 148, 2, 5, 15, 1)); !i.Interlaced {
		t.Error("interlaced")
	}
	if _, ok := FromBT(make([]byte, 132)); ok {
		t.Error("all zero is no mode")
	}
}

// DvTimingsTest.test_ioctl_number and test_no_device_falls_back
func TestDvIoctlNumberAndFallback(t *testing.T) {
	if (2<<30)|(132<<16)|('V'<<8)|99 != 0x80845663 {
		t.Fatal("ioctl number")
	}
	if _, _, err := queryIoctl("/dev/no-existe"); err != ErrNotImplemented {
		t.Fatalf("missing device: %v", err)
	}
	calls := 0
	tm, ok := QueryDV("/dev/no-existe", func(_ time.Duration, name string, args ...string) sysx.Result {
		calls++
		return sysx.Result{Out: dv1080p60}
	})
	if !ok || tm.FPS != 60 || calls != 1 {
		t.Fatalf("v4l2-ctl fallback: %+v %v %d", tm, ok, calls)
	}
}

func TestPrepare(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sys/class/video4linux/v4l-subdev2")
	os.MkdirAll(filepath.Join(sub, "device/of_node"), 0o755)
	os.WriteFile(filepath.Join(sub, "name"), []byte("tc358743 8-000f\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "sys/class/video4linux/video0"), 0o755)
	os.WriteFile(filepath.Join(root, "sys/class/video4linux/video0/name"), []byte("vi-output, tc358743 8-000f\n"), 0o644)
	state := t.TempDir()
	os.WriteFile(filepath.Join(state, "hw-reset.request"), nil, 0o644)
	edid := filepath.Join(t.TempDir(), "edid.bin")
	os.WriteFile(edid, []byte{0x00, 0xff, 0xAB}, 0o644)

	var log []string
	chipEDID := "00 ff ab\n"
	queries := 0
	run := func(_ time.Duration, name string, args ...string) sysx.Result {
		log = append(log, strings.Join(args, " "))
		for _, a := range args {
			if f, ok := strings.CutPrefix(a, "--get-edid=pad=0,file="); ok {
				os.WriteFile(f, []byte(chipEDID), 0o644)
			}
			if a == "--query-dv-timings" {
				queries++
				if queries > 2 {
					return sysx.Result{RC: 1}
				}
			}
		}
		return sysx.Result{}
	}
	s := Sys{Root: root, Run: run, Sleep: func(time.Duration) {}}
	if s.WiredSubdev("") != "/dev/v4l-subdev2" || s.HasResetPin("/dev/v4l-subdev2") {
		t.Fatal("subdev / reset pin")
	}
	// Same EDID: only the reset (asked for) and the read
	if !s.Prepare(edid, state, "") {
		t.Fatal("no TC358743")
	}
	if sysx.Exists(filepath.Join(state, "hw-reset.request")) || !sysx.Exists(filepath.Join(state, "hw-reset.last")) {
		t.Error("the reset request is consumed and the time written")
	}
	if strings.Contains(strings.Join(log, "|"), "--set-edid") {
		t.Errorf("same EDID rewritten: %v", log)
	}
	// Another EDID on the chip: rewritten, then wait for the signal to drop
	log, chipEDID = nil, "00 00 00"
	s.Prepare(edid, state, "")
	all := strings.Join(log, "|")
	if !strings.Contains(all, "--set-edid=pad=0,file="+edid+",format=raw") || queries != 3 {
		t.Errorf("EDID not loaded or no wait: %v (queries %d)", log, queries)
	}
	if (Sys{Root: t.TempDir(), Run: run, Sleep: func(time.Duration) {}}).Prepare(edid, state, "") {
		t.Error("a board without TC358743")
	}
	// Replug: the same EDID written again, whatever the chip has (the driver drops the hotplug)
	log, chipEDID = nil, "00 ff ab\n"
	if !s.Replug(edid, "") || strings.Join(log, "|") != "-d /dev/v4l-subdev2 --set-edid=pad=0,file="+edid+",format=raw" {
		t.Errorf("replug: %v", log)
	}
	if s.Replug(filepath.Join(t.TempDir(), "none.bin"), "") || (Sys{Root: t.TempDir(), Run: run}).Replug(edid, "") {
		t.Error("replug without an EDID file or a TC358743")
	}
}

// Two chips (CAM0 and CAM1): every chip gets the EDID; the reset, the replug and the reset pin are the
// wired chip's (HDMI_WIRED), and only a chip with the driver's control has a hardware reset.
func TestTwoChips(t *testing.T) {
	root := t.TempDir()
	w := func(p, v string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(v+"\n"), 0o644)
	}
	w("sys/class/video4linux/v4l-subdev2/name", "tc358743 8-000f")
	w("sys/class/video4linux/v4l-subdev3/name", "tc358743 7-000f")
	w("sys/class/video4linux/video0/name", "vi-output, tc358743 8-000f")
	w("sys/class/video4linux/video1/name", "vi-output, tc358743 7-000f")
	w("sys/bus/i2c/devices/i2c-7/name", "i2c-6-mux (chan_id 0)")
	w("sys/bus/i2c/devices/i2c-8/name", "i2c-6-mux (chan_id 1)")
	// Both nodes declare the pin; only CAM0's chip got it (and the control)
	w("sys/class/video4linux/v4l-subdev2/device/of_node/reset-gpios", "")
	w("sys/class/video4linux/v4l-subdev3/device/of_node/reset-gpios", "")
	state := t.TempDir()
	os.WriteFile(filepath.Join(state, "hw-reset.request"), nil, 0o644)
	edid := filepath.Join(t.TempDir(), "edid.bin")
	os.WriteFile(edid, []byte{0x00, 0xff, 0xAB}, 0o644)
	var log []string
	run := func(_ time.Duration, name string, args ...string) sysx.Result {
		log = append(log, strings.Join(args, " "))
		if len(args) == 3 && args[2] == "--list-ctrls" {
			if args[1] == "/dev/v4l-subdev3" {
				return sysx.Result{Out: "                 hardware_reset 0x00981982 (button) : flags=write-only, execute-on-write\n"}
			}
			return sysx.Result{Out: "             audio_sampling_rate 0x00981980 (int)    : min=0 max=768000 step=1 default=0 value=0 flags=read-only\n"}
		}
		if len(args) > 1 && args[len(args)-1] == "--query-dv-timings" {
			return sysx.Result{RC: 1}
		}
		return sysx.Result{}
	}
	s := Sys{Root: root, Run: run, Sleep: func(time.Duration) {}}
	if got := s.Chips(); len(got) != 2 || got[0].Subdev != "/dev/v4l-subdev3" || got[1].Video != "/dev/video0" {
		t.Fatalf("chips: %+v", got)
	}
	if s.WiredSubdev("") != "/dev/v4l-subdev3" || s.WiredSubdev("cam1") != "/dev/v4l-subdev2" {
		t.Error("wired subdev: CAM0 by default, CAM1 when set")
	}
	if !s.HasResetPin("/dev/v4l-subdev3") || s.HasResetPin("/dev/v4l-subdev2") {
		t.Error("only the chip with the control has a hardware reset, although both nodes declare the pin")
	}
	log = nil
	if !s.Prepare(edid, state, "cam1") {
		t.Fatal("no TC358743")
	}
	all := strings.Join(log, "|")
	for _, want := range []string{
		"-d /dev/v4l-subdev2 -c hardware_reset=1",
		"-d /dev/v4l-subdev3 --set-edid=pad=0,file=" + edid + ",format=raw",
		"-d /dev/v4l-subdev2 --set-edid=pad=0,file=" + edid + ",format=raw",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("Prepare: no %q in %v", want, log)
		}
	}
	if strings.Contains(all, "/dev/v4l-subdev3 -c hardware_reset") {
		t.Errorf("reset on the chip that is not wired: %v", log)
	}
	log = nil
	if !s.Replug(edid, "cam1") || strings.Join(log, "|") != "-d /dev/v4l-subdev2 --set-edid=pad=0,file="+edid+",format=raw" {
		t.Errorf("replug of the wired chip: %v", log)
	}
}
