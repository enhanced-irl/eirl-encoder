package probe

import (
	"strings"
	"testing"
)

// ParseTest.test_probe_whitelist
func TestProbeWhitelist(t *testing.T) {
	out := "NODE=/dev/video1\nDEVICE=/dev/v4l/by-id/x\nNAME=Cam Link 4K; rm -rf /\nFORMAT=NV12\nWIDTH=1920\n" +
		"HEIGHT=1080\nFPS_NUM=60\nFPS_DEN=1\nALSA_CARD=C4K\n"
	p, ok := Parse(out)
	if !ok || p.Name != "Cam Link 4K rm -rf " || p.Format != "NV12" || p.Width != 1920 || p.FPSNum != 60 || p.ALSACard != "C4K" {
		t.Fatalf("parse: %+v %v", p, ok)
	}
	if _, ok := Parse(strings.Replace(out, "/dev/video1", "/tmp/x", 1)); ok {
		t.Error("a node outside /dev/video* must not parse")
	}
}

// ParseTest.test_probe_all
func TestProbeAll(t *testing.T) {
	out := "COUNT=2\nSRC0_ID=/dev/v4l/by-id/usb-Elgato-video-index0\nSRC0_NODE=/dev/video1\nSRC0_NAME=Cam Link 4K\n" +
		"SRC0_FORMAT=NV12\nSRC0_WIDTH=1920\nSRC0_HEIGHT=1080\nSRC0_FPS_NUM=60\nSRC0_FPS_DEN=1\nSRC0_BUS_SHARE=62\n" +
		"SRC1_ID=/dev/v4l/by-id/usb-Logi-video-index0\nSRC1_NODE=/dev/video2\nSRC1_NAME=Logi\nSRC1_FORMAT=\n" +
		"SRC1_WIDTH=\nSRC1_HEIGHT=\nSRC1_FPS_NUM=\nSRC1_FPS_DEN=\nSRC1_WARNINGS=no_bus\nSRC1_BUS_SHARE=0\n" +
		"SRC2_ID=../../etc/passwd\n"
	r := ParseAll(out)
	if len(r) != 2 {
		t.Fatalf("ids: %v", r)
	}
	if e := r["/dev/v4l/by-id/usb-Elgato-video-index0"]; !e.Complete || e.Format != "NV12" {
		t.Errorf("elgato: %+v", e)
	}
	if l := r["/dev/v4l/by-id/usb-Logi-video-index0"]; l.Complete || l.Warnings != "no_bus" || !l.HasWarning("no_bus") || l.HasWarning("bus") {
		t.Errorf("logi: %+v", l)
	}
}

func TestKeepArg(t *testing.T) {
	p, _ := Parse("NODE=/dev/video1\nFORMAT=MJPG\nWIDTH=1920\nHEIGHT=1080\nFPS_NUM=60000\nFPS_DEN=1001\n")
	if got := KeepArg("/dev/v4l/by-id/x", p.Probe); got != "/dev/v4l/by-id/x=MJPG:1920x1080@60000/1001" {
		t.Fatal(got)
	}
}

func TestParseList(t *testing.T) {
	valid := func(id, node string) bool { return id == "net:cam1" && node == "rtsp://127.0.0.1:8554/cam1" }
	name := func(n string) string { return "[" + n + "]" }
	out := `{"sources":[
		{"id":"/dev/v4l/by-id/usb-Logi-video-index0","kind":"usb","name":"Logi 4K; x"},
		{"id":"tc","kind":"hdmi-csi","node":"/dev/video0"},
		{"id":"net:cam1","kind":"network","node":"rtsp://127.0.0.1:8554/cam1","name":"Móvil"},
		{"id":"net:cam2","kind":"network","node":"rtsp://evil/cam2"},
		{"id":"../x","kind":"usb"},
		{"kind":"usb"},
		{"id":"/dev/v4l/by-id/usb-NoName-video-index0","kind":"usb","name":";;"}],
		"audio":[{"id":"i2s","kind":"i2s","name":"HDMI de la placa"}]}`
	c := ParseList(out, true, valid, name)
	if !c.OK {
		t.Fatal("not OK")
	}
	var ids []string
	for _, e := range c.Sources {
		ids = append(ids, e.ID+"/"+e.Name)
	}
	want := "hdmi/HDMI de la placa,test/Barras,/dev/v4l/by-id/usb-Logi-video-index0/Logi 4K x,net:cam1/[Móvil],/dev/v4l/by-id/usb-NoName-video-index0/USB"
	if strings.Join(ids, ",") != want {
		t.Errorf("order/names:\n got %s\nwant %s", strings.Join(ids, ","), want)
	}
	if c.Sources[0].Node != "/dev/video0" || len(c.Audio) != 1 {
		t.Errorf("hdmi node / audio: %+v %+v", c.Sources[0], c.Audio)
	}
	for _, bad := range []string{"", "nope", `{"sources":"x"}`, `[]`} {
		if c := ParseList(bad, false, valid, name); c.OK || len(c.Sources) != 0 {
			t.Errorf("%q: %+v", bad, c)
		}
	}
}
