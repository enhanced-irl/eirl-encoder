package main

import (
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/sources"
)

func TestParseKeep(t *testing.T) {
	k, err := parseKeep("/dev/v4l/by-path/platform-70090000.xusb-usb-0:1.1:1.0-video-index0=NV12:1920x1080@60000/1001")
	want := sources.Choice{Format: "NV12", Width: 1920, Height: 1080, Rate: sources.Rate{Num: 60000, Den: 1001}}
	if err != nil || k.ID != "/dev/v4l/by-path/platform-70090000.xusb-usb-0:1.1:1.0-video-index0" || k.Choice != want {
		t.Errorf("%+v %v", k, err)
	}
	for _, bad := range []string{
		"", "auto=NV12:1920x1080@60/1", "/dev/video1=NV12:1920x1080@60/1",
		"/dev/v4l/by-id/x=H264:1920x1080@60/1", "/dev/v4l/by-id/x=NV12:1920x1080@60", "/dev/v4l/by-id/x=NV12:1920x1080@60/0",
		"/dev/v4l/by-id/x;reboot=NV12:1920x1080@60/1", "/dev/v4l/by-id/x",
	} {
		if _, err := parseKeep(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestProbeArgs(t *testing.T) {
	for _, args := range [][]string{
		{"-all", "-device", "auto"}, {"-all", "-list"}, {"-max", "2"}, {"-all", "-max", "0"}, {"-all", "-max", "5"},
		{"-keep", "/dev/v4l/by-id/x=NV12:1920x1080@60/1"}, {"-all", "-keep", "nope"}, {"-all", "extra"},
		{"-want", "/dev/v4l/by-id/x"}, {"-all", "-want", "auto"}, {"-all", "-want", "/dev/video1"},
	} {
		if got := probe(args); got != probeUsage {
			t.Errorf("%v: %d, want %d", args, got, probeUsage)
		}
	}
}

func TestProbeAllOutput(t *testing.T) {
	cl := sources.Source{ID: "/dev/v4l/by-id/cl", Kind: sources.KindUSB, Name: "Cam Link 4K", Node: "/dev/video1",
		USB:   &sources.USBInfo{SpeedMbps: 5000, Bus: 2},
		Audio: &sources.AudioInput{ID: "alsa:C4K", Kind: "usb", Card: "C4K"},
		Formats: []sources.Format{{FourCC: "NV12", Name: "NV12", Supported: true,
			Modes: []sources.Mode{{Width: 1920, Height: 1080, Rates: []sources.Rate{{Num: 60, Den: 1}}}}}}}
	cl2 := cl
	cl2.ID, cl2.Node, cl2.Audio = "/dev/v4l/by-id/cl2", "/dev/video3", nil
	var b strings.Builder
	probeAll(&b, sources.Catalog{Sources: []sources.Source{{ID: "hdmi", Kind: sources.KindHDMI}, cl, cl2}}, nil, 3)
	want := `COUNT=2
SRC0_ID=/dev/v4l/by-id/cl
SRC0_NODE=/dev/video1
SRC0_NAME=Cam Link 4K
SRC0_FORMAT=NV12
SRC0_WIDTH=1920
SRC0_HEIGHT=1080
SRC0_FPS_NUM=60
SRC0_FPS_DEN=1
SRC0_OUT_WIDTH=1920
SRC0_OUT_HEIGHT=1080
SRC0_ALSA_CARD=C4K
SRC0_USB_SPEED=5000
SRC0_WARNINGS=
SRC0_BUS_SHARE=62
SRC1_ID=/dev/v4l/by-id/cl2
SRC1_NODE=/dev/video3
SRC1_NAME=Cam Link 4K
SRC1_FORMAT=
SRC1_WIDTH=
SRC1_HEIGHT=
SRC1_FPS_NUM=
SRC1_FPS_DEN=
SRC1_OUT_WIDTH=
SRC1_OUT_HEIGHT=
SRC1_ALSA_CARD=
SRC1_USB_SPEED=5000
SRC1_WARNINGS=no_bus
SRC1_BUS_SHARE=0
`
	if b.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", b.String(), want)
	}
	b.Reset()
	probeAll(&b, sources.Catalog{}, nil, 3)
	if b.String() != "COUNT=0\n" {
		t.Errorf("sin fuentes: %q", b.String())
	}
}
