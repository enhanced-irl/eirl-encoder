package sources

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeEnum map[string][]Format // by node name (video1); nil = does not capture

func (f fakeEnum) Formats(node string) ([]Format, error) {
	v, ok := f[filepath.Base(node)]
	if !ok || v == nil {
		return nil, ErrNotCapture
	}
	return v, nil
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

func modesOf(w, h int, rates ...Rate) []Mode { return []Mode{{Width: w, Height: h, Rates: rates}} }

var (
	r60   = Rate{60, 1}
	r5994 = Rate{60000, 1001}
	r30   = Rate{30, 1}
)

// A tree like the Jetson's: HDMI over CSI (video0), a Cam Link 4K on USB 3 with a serial number
// (video1 + card C4K) and an MS2109 on USB 2 without a serial number (video2 + card MS2109).
func fakeBoard(t *testing.T, camLinkSpeed string) (*Scanner, fakeEnum) {
	root := t.TempDir()
	sys, dev := filepath.Join(root, "sys"), filepath.Join(root, "dev")

	os.MkdirAll(filepath.Join(sys, "devices/platform/54080000.vi"), 0o755)
	write(t, filepath.Join(sys, "class/video4linux/video0/name"), "vi-output, tc358743 8-000f")
	write(t, filepath.Join(sys, "class/video4linux/video0/index"), "0")
	link(t, "../../../devices/platform/54080000.vi", filepath.Join(sys, "class/video4linux/video0/device"))
	write(t, filepath.Join(sys, "class/video4linux/v4l-subdev1/name"), "tc358743 8-000f")

	cl := filepath.Join(sys, "devices/usb2/2-1/2-1.1")
	for k, v := range map[string]string{"idVendor": "0fd9", "idProduct": "0066", "product": "Cam Link 4K", "speed": camLinkSpeed, "serial": "0005AB12", "busnum": "2", "devnum": "3"} {
		write(t, filepath.Join(cl, k), v)
	}
	os.MkdirAll(filepath.Join(cl, "2-1.1:1.0"), 0o755)
	os.MkdirAll(filepath.Join(cl, "2-1.1:1.3"), 0o755)
	write(t, filepath.Join(sys, "class/video4linux/video1/name"), "Cam Link 4K: Cam Link 4K")
	write(t, filepath.Join(sys, "class/video4linux/video1/index"), "0")
	link(t, "../../../devices/usb2/2-1/2-1.1/2-1.1:1.0", filepath.Join(sys, "class/video4linux/video1/device"))
	write(t, filepath.Join(sys, "class/sound/card2/id"), "C4K")
	link(t, "../../../devices/usb2/2-1/2-1.1/2-1.1:1.3", filepath.Join(sys, "class/sound/card2/device"))

	ms := filepath.Join(sys, "devices/usb1/1-2/1-2.3")
	for k, v := range map[string]string{"idVendor": "534d", "idProduct": "2109", "product": "USB Video\x1b[31m", "speed": "480", "busnum": "1", "devnum": "5"} {
		write(t, filepath.Join(ms, k), v)
	}
	os.MkdirAll(filepath.Join(ms, "1-2.3:1.0"), 0o755)
	os.MkdirAll(filepath.Join(ms, "1-2.3:1.2"), 0o755)
	write(t, filepath.Join(sys, "class/video4linux/video2/name"), "USB Video: USB Video")
	link(t, "../../../devices/usb1/1-2/1-2.3/1-2.3:1.0", filepath.Join(sys, "class/video4linux/video2/device"))
	write(t, filepath.Join(sys, "class/sound/card3/id"), "MS2109")
	link(t, "../../../devices/usb1/1-2/1-2.3/1-2.3:1.2", filepath.Join(sys, "class/sound/card3/device"))

	// The board's cards: left out
	os.MkdirAll(filepath.Join(sys, "devices/platform/sound"), 0o755)
	write(t, filepath.Join(sys, "class/sound/card1/id"), "tegrasndt210ref")
	link(t, "../../../devices/platform/sound", filepath.Join(sys, "class/sound/card1/device"))

	link(t, "../../video1", filepath.Join(dev, "v4l/by-id/usb-Elgato_Cam_Link_4K_0005AB12-video-index0"))
	link(t, "../../video1", filepath.Join(dev, "v4l/by-path/platform-70090000.xusb-usb-0:1.1:1.0-video-index0"))
	link(t, "../../video2", filepath.Join(dev, "v4l/by-id/usb-MACROSILICON_USB_Video-video-index0"))
	link(t, "../../video2", filepath.Join(dev, "v4l/by-path/platform-70090000.xusb-usb-0:2.3:1.0-video-index0"))
	link(t, "../../video0", filepath.Join(dev, "v4l/by-path/platform-54080000.vi-video-index0"))

	enum := fakeEnum{
		"video1": {
			newFormat("YUYV", modesOf(1920, 1080, r60, r5994)),
			newFormat("NV12", modesOf(1920, 1080, r60)),
			newFormat("YU12", modesOf(1920, 1080, r60)),
		},
		"video2": {
			newFormat("YUYV", []Mode{{1920, 1080, []Rate{{5, 1}}}, {1280, 720, []Rate{{10, 1}}}}),
			newFormat("MJPG", []Mode{{1920, 1080, []Rate{r30}}, {1280, 720, []Rate{r60, r30}}}),
			newFormat("H264", modesOf(1920, 1080, r30)),
		},
	}
	return &Scanner{SysRoot: sys, DevRoot: dev, Enum: enum}, enum
}

func TestScan(t *testing.T) {
	s, _ := fakeBoard(t, "5000")
	cat := s.Scan()
	if len(cat.Sources) != 3 {
		t.Fatalf("sources: %+v", cat.Sources)
	}
	hdmi, cl, ms := cat.Sources[0], cat.Sources[1], cat.Sources[2]
	if hdmi.ID != "hdmi" || hdmi.Kind != KindHDMI || hdmi.Node != "/dev/video0" {
		t.Errorf("HDMI: %+v", hdmi)
	}
	if cl.ID != "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0005AB12-video-index0" || cl.Name != "Cam Link 4K" || cl.USB.SpeedMbps != 5000 {
		t.Errorf("Cam Link: %+v", cl)
	}
	if cl.Audio == nil || cl.Audio.ID != "alsa:C4K" || len(cl.Warnings) != 0 {
		t.Errorf("Cam Link audio/warnings: %+v %v", cl.Audio, cl.Warnings)
	}
	// No serial number: by port, not by by-id (two identical ones would share the name)
	if ms.ID != "/dev/v4l/by-path/platform-70090000.xusb-usb-0:2.3:1.0-video-index0" {
		t.Errorf("MS2109 ID: %s", ms.ID)
	}
	if ms.Name != "USB Video 31m" {
		t.Errorf("the vendor name is not sanitized: %q", ms.Name)
	}
	if ms.Audio == nil || ms.Audio.Card != "MS2109" {
		t.Errorf("MS2109 audio: %+v", ms.Audio)
	}
	want := []string{"i2s", "alsa:C4K", "alsa:MS2109"}
	if len(cat.Audio) != len(want) {
		t.Fatalf("audio: %+v", cat.Audio)
	}
	for i, a := range cat.Audio {
		if a.ID != want[i] {
			t.Errorf("audio %d: %s, want %s", i, a.ID, want[i])
		}
	}
}

func TestScanSkipsNonCapture(t *testing.T) {
	s, enum := fakeBoard(t, "5000")
	enum["video2"] = nil
	if cat := s.Scan(); len(cat.Sources) != 2 {
		t.Errorf("a node that does not capture should be left out: %+v", cat.Sources)
	}
}

func TestCamLinkOnUSB2Warns(t *testing.T) {
	s, _ := fakeBoard(t, "480")
	cl := s.Scan().Sources[1]
	found := false
	for _, w := range cl.Warnings {
		found = found || w == WarnUSB2
	}
	if !found {
		t.Errorf("Cam Link on USB 2 without a warning: %v", cl.Warnings)
	}
}

func TestChoose(t *testing.T) {
	s, _ := fakeBoard(t, "5000")
	cat := s.Scan()
	cl, ms := cat.Sources[1], cat.Sources[2]
	for _, c := range []struct {
		name         string
		src          Source
		format, mode string
		want         Choice
		err          error
	}{
		{"Cam Link auto: NV12 at 1080p60", cl, "auto", "auto", Choice{"NV12", 1920, 1080, r60}, nil},
		{"Cam Link YUY2", cl, "YUY2", "auto", Choice{"YUY2", 1920, 1080, r60}, nil},
		{"Cam Link mode 60 covers 59.94", cl, "YUY2", "1920x1080@60", Choice{"YUY2", 1920, 1080, r60}, nil},
		{"Cam Link without MJPEG", cl, "MJPG", "auto", Choice{}, ErrNotOffered},
		{"MS2109 auto: MJPEG 1080p30 before YUY2 at 5 fps", ms, "auto", "auto", Choice{"MJPG", 1920, 1080, r30}, nil},
		{"MS2109 720p60", ms, "auto", "1280x720@60", Choice{"MJPG", 1280, 720, r60}, nil},
		{"MS2109 YUY2 requested by hand", ms, "YUY2", "auto", Choice{"YUY2", 1920, 1080, Rate{5, 1}}, nil},
		{"MS2109 does not do 1080p60", ms, "auto", "1920x1080@60", Choice{}, ErrNotOffered},
		{"H.264 is not captured", ms, "H264", "auto", Choice{}, ErrNotOffered},
		{"malformed mode", ms, "auto", "1080p", Choice{}, ErrBadModeSpec},
	} {
		got, err := Choose(c.src, c.format, c.mode)
		if err != c.err || got != c.want {
			t.Errorf("%s: %+v, %v; want %+v, %v", c.name, got, err, c.want, c.err)
		}
	}
}

func TestChooseBusBudget(t *testing.T) {
	// Cam Link with the camera at 4K: it only offers 2160p30, which uncompressed (373 MB/s) does not
	// fit in the Nano's USB → no mode, and a warning to set the camera to 1080p
	cl := Source{Kind: KindUSB, USB: &USBInfo{SpeedMbps: 5000}, Formats: []Format{newFormat("NV12", modesOf(3840, 2160, r30))}}
	if _, err := Choose(cl, "auto", "auto"); err != ErrNoMode {
		t.Errorf("4K30 NV12 should not fit: %v", err)
	}
	if !aboveOnly(cl.Formats) {
		t.Error("only modes above 1080p")
	}
	// 1440p30 does fit (166 MB/s) and is picked even though it must be scaled
	cl.Formats = []Format{newFormat("NV12", modesOf(2560, 1440, r30))}
	if c, err := Choose(cl, "auto", "auto"); err != nil || c.Height != 1440 {
		t.Errorf("1440p30: %+v %v", c, err)
	}
	// YUY2 1080p60 (249 MB/s) does not fit in USB 2
	usb2 := Source{Kind: KindUSB, USB: &USBInfo{SpeedMbps: 480}, Formats: []Format{newFormat("YUYV", modesOf(1920, 1080, r60))}}
	if _, err := Choose(usb2, "auto", "auto"); err != ErrNoMode {
		t.Errorf("YUY2 1080p60 over USB 2: %v", err)
	}
}

func TestOutputSize(t *testing.T) {
	for _, c := range [][4]int{{1920, 1080, 1920, 1080}, {1280, 720, 1280, 720}, {3840, 2160, 1920, 1080}, {2560, 1440, 1920, 1080}, {4096, 2160, 1920, 1012}, {640, 480, 640, 480}} {
		if w, h := OutputSize(c[0], c[1]); w != c[2] || h != c[3] {
			t.Errorf("%dx%d → %dx%d, want %dx%d", c[0], c[1], w, h, c[2], c[3])
		}
	}
}

func TestFind(t *testing.T) {
	s, _ := fakeBoard(t, "5000")
	cat := s.Scan()
	if src, ok := Find(cat, "auto", "auto", "auto", s.DevRoot); !ok || src.Name != "Cam Link 4K" {
		t.Errorf("auto: %+v %v", src, ok)
	}
	// Stored by port: it is found even though its ID is the by-id one
	if src, ok := Find(cat, "/dev/v4l/by-path/platform-70090000.xusb-usb-0:1.1:1.0-video-index0", "auto", "auto", s.DevRoot); !ok || src.Name != "Cam Link 4K" {
		t.Errorf("by by-path: %+v %v", src, ok)
	}
	if _, ok := Find(cat, "/dev/v4l/by-id/usb-Other-video-index0", "auto", "auto", s.DevRoot); ok {
		t.Error("a missing one should not be found")
	}
	if _, ok := Find(cat, "hdmi", "auto", "auto", s.DevRoot); ok {
		t.Error("Find only looks for USB sources")
	}
	// With a requested mode, the first one that offers it: only the MS2109 (the second) does MJPEG 720p60
	if src, ok := Find(cat, "auto", "MJPG", "1280x720@60", s.DevRoot); !ok || src.USB.ProductID != "2109" {
		t.Errorf("auto with a requested mode: %+v %v", src, ok)
	}
	// None can be captured (camera at 4K): auto still returns the USB one, to explain why
	only4k := Catalog{Sources: []Source{{ID: "x", Kind: KindUSB, Name: "Cam Link 4K", USB: &USBInfo{SpeedMbps: 5000},
		Formats: []Format{newFormat("NV12", modesOf(3840, 2160, r30))}}}}
	if src, ok := Find(only4k, "auto", "auto", "auto", s.DevRoot); !ok || src.ID != "x" {
		t.Errorf("auto without a capturable mode: %+v %v", src, ok)
	}
}

func TestDuplicateSerialsUsePort(t *testing.T) {
	s, enum := fakeBoard(t, "5000")
	// A second Cam Link with the SAME serial number (video3, another port)
	sys, dev := s.SysRoot, s.DevRoot
	cl2 := filepath.Join(sys, "devices/usb2/2-1/2-1.2")
	for k, v := range map[string]string{"idVendor": "0fd9", "idProduct": "0066", "product": "Cam Link 4K", "speed": "5000", "serial": "0005AB12", "busnum": "2", "devnum": "4"} {
		write(t, filepath.Join(cl2, k), v)
	}
	os.MkdirAll(filepath.Join(cl2, "2-1.2:1.0"), 0o755)
	write(t, filepath.Join(sys, "class/video4linux/video3/name"), "Cam Link 4K")
	link(t, "../../../devices/usb2/2-1/2-1.2/2-1.2:1.0", filepath.Join(sys, "class/video4linux/video3/device"))
	link(t, "../../video3", filepath.Join(dev, "v4l/by-path/platform-70090000.xusb-usb-0:1.2:1.0-video-index0"))
	enum["video3"] = enum["video1"]
	cat := s.Scan()
	ids := map[string]bool{}
	for _, src := range cat.Sources {
		if src.Kind == KindUSB && src.Name == "Cam Link 4K" {
			if !strings.Contains(src.ID, "/by-path/") {
				t.Errorf("repeated serial and not by port: %s", src.ID)
			}
			ids[src.ID] = true
		}
	}
	if len(ids) != 2 {
		t.Errorf("the two Cam Links should have different IDs: %v", ids)
	}
}

type errEnum struct{}

func (errEnum) Formats(string) ([]Format, error) { return nil, errors.New("device or resource busy") }

func TestUnreadableIsNotNoFormat(t *testing.T) {
	s, _ := fakeBoard(t, "5000")
	s.Enum = errEnum{}
	cl := s.Scan().Sources[1]
	if cl.Error != "device or resource busy" || len(cl.Warnings) != 1 || cl.Warnings[0] != WarnUnread {
		t.Errorf("read error: %q %v", cl.Error, cl.Warnings)
	}
}

func TestOptions(t *testing.T) {
	s, _ := fakeBoard(t, "5000")
	ms := s.Scan().Sources[2]
	opts := Options(ms)
	// MJPEG 1080p30, MJPEG 720p60, MJPEG 720p30 and, last, the YUY2 ones at 5 and 10 fps
	want := []string{"MJPG 1920x1080@30", "MJPG 1280x720@60", "MJPG 1280x720@30", "YUY2 1920x1080@5", "YUY2 1280x720@10"}
	if len(opts) != len(want) {
		t.Fatalf("options: %+v", opts)
	}
	for i, o := range opts {
		if got := o.Format + " " + o.Mode(); got != want[i] {
			t.Errorf("%d: %s, want %s", i, got, want[i])
		}
	}
	// 60/1 and 60000/1001 in YUY2 are a single "@60", the exact one first
	cl := s.Scan().Sources[1]
	n := 0
	for _, o := range Options(cl) {
		if o.Format == "YUY2" {
			n++
			if o.Rate != r60 {
				t.Errorf("YUY2: %+v", o)
			}
		}
	}
	if n != 1 {
		t.Errorf("YUY2 repeated: %d", n)
	}
	if c, _ := Choose(ms, "auto", "auto"); c != opts[0] {
		t.Errorf("auto (%+v) is not the first option (%+v)", c, opts[0])
	}
}

// Network cameras (F7): after the USB ones, with their audio, from the list WITHOUT keys
func TestScanNetworkCameras(t *testing.T) {
	s, _ := fakeBoard(t, "5000")
	s.Cameras = filepath.Join(t.TempDir(), "cameras-public.json")
	os.WriteFile(s.Cameras, []byte(`{"v":1,"cameras":[{"id":"cam1","name":"Zoë's phone"}]}`), 0o640)
	cat := s.Scan()
	if len(cat.Sources) != 4 {
		t.Fatalf("sources: %+v", cat.Sources)
	}
	n := cat.Sources[3]
	if n.ID != "net:cam1" || n.Kind != KindNetwork || n.Name != "Zoë's phone" || n.Node != "rtsp://127.0.0.1:8554/cam1" || n.Audio == nil || n.Audio.ID != "net:cam1" {
		t.Errorf("network camera: %+v", n)
	}
	if a := cat.Audio[len(cat.Audio)-1]; a.ID != "net:cam1" || a.Kind != "network" {
		t.Errorf("camera audio: %+v", a)
	}
	// An invalid list (e.g. with keys) adds nothing
	os.WriteFile(s.Cameras, []byte(`{"v":1,"cameras":[{"id":"cam1","name":"x","key":"y"}]}`), 0o640)
	if cat := s.Scan(); len(cat.Sources) != 3 {
		t.Errorf("invalid list: %+v", cat.Sources)
	}
}
