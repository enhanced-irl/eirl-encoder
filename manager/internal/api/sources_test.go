package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/sources"
)

func TestSourcesCatalog(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	if w := e.do("GET", "/api/v1/sources", "", tok); w.Code != 503 {
		t.Errorf("no detector: %d", w.Code)
	}
	// A sysfs without devices: only the test pattern
	e.srv.Sources = &sources.Scanner{SysRoot: filepath.Join(e.dir, "sys"), DevRoot: filepath.Join(e.dir, "dev")}
	w := e.do("GET", "/api/v1/sources", "", tok)
	var got struct {
		Sources []struct{ ID, Kind string } `json:"sources"`
		Audio   []any                       `json:"audio"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Sources) != 1 || got.Sources[0].Kind != "test" || got.Audio == nil {
		t.Errorf("catalog: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/v1/sources", "", ""); w.Code != 401 {
		t.Errorf("no session: %d", w.Code)
	}
}

func TestInputUSB(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	for _, bad := range []string{
		`{"source":"usb","device":"/dev/video1"}`,
		`{"source":"usb","device":"/dev/v4l/by-id/x ! filesink location=/etc/x"}`,
		`{"source":"rtmp"}`,
		`{"audio":"hw:0"}`,
		`{"mode":"1080p"}`,
		`{"apply":true}`,
	} {
		if w := e.do("PUT", "/api/v1/input", bad, tok); w.Code != 400 || len(e.unit.actions) != 0 {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	dev := "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0005AB12-video-index0"
	w := e.do("PUT", "/api/v1/input", `{"source":"usb","device":"`+dev+`","mode":"1920x1080@60","audio":"alsa:C4K","apply":true}`, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"restarted":true`) {
		t.Fatalf("PUT input: %d %s", w.Code, w.Body)
	}
	env, _ := config.ReadEnv(filepath.Join(e.dir, "app.env"))
	if env["SOURCE"] != "usb" || env["VIDEO_DEVICE"] != dev || env["VIDEO_MODE"] != "1920x1080@60" || env["AUDIO_SOURCE"] != "alsa:C4K" || env["VIDEO_FORMAT"] != "auto" {
		t.Errorf("file: %v", env)
	}

	// Waiting for it to be plugged in
	state := filepath.Join(e.dir, "state")
	os.WriteFile(state, []byte("OUTPUT=on\nSOURCE=usb\nCONF_SHA=x\nINPUT=waiting_device\n"), 0o644)
	var st Status
	json.Unmarshal(e.do("GET", "/api/v1/status", "", tok).Body.Bytes(), &st)
	if st.Service.Phase != "waiting_device" || st.Signal.Kind != "usb" || st.Signal.Present {
		t.Errorf("waiting: phase %s, signal %+v", st.Service.Phase, st.Signal)
	}

	// Capturing, without an fps file (old launcher): assumed fine
	os.WriteFile(state, []byte("OUTPUT=on\nSOURCE=usb\nCONF_SHA=x\nINPUT=ready\nDEVICE="+dev+"\nDEVICE_NAME=Cam Link 4K\nFORMAT=NV12\nMODE=1920x1080@60\nRATE=60000/1001\nAUDIO=alsa:C4K\n"), 0o644)
	st = Status{}
	json.Unmarshal(e.do("GET", "/api/v1/status", "", tok).Body.Bytes(), &st)
	if st.Service.Phase != "streaming" || !st.Signal.Present || st.Signal.Width != 1920 || st.Signal.FPS != 59.94 || st.Audio.State != "usb" {
		t.Errorf("capturing: phase %s, signal %+v, audio %+v", st.Service.Phase, st.Signal, st.Audio)
	}
	if a := st.Input.Active; a == nil || a.Name != "Cam Link 4K" || a.Format != "NV12" || st.Input.Source != "usb" {
		t.Errorf("active input: %+v", st.Input)
	}

	// The capture delivers no frames (capture card without signal): no signal, with 0 fps measured
	e.srv.VideoPath = filepath.Join(e.dir, "video")
	os.WriteFile(e.srv.VideoPath, []byte("FPS=0.00\nFRAMES=10\n"), 0o644)
	st = Status{}
	json.Unmarshal(e.do("GET", "/api/v1/status", "", tok).Body.Bytes(), &st)
	if st.Signal.Present || st.Signal.MeasuredFPS == nil || *st.Signal.MeasuredFPS != 0 || st.Service.Phase != "waiting_signal" {
		t.Errorf("no frames: phase %s, signal %+v", st.Service.Phase, st.Signal)
	}
	os.WriteFile(e.srv.VideoPath, []byte("FPS=59.94\nFRAMES=600\n"), 0o644)
	st = Status{}
	json.Unmarshal(e.do("GET", "/api/v1/status", "", tok).Body.Bytes(), &st)
	if !st.Signal.Present || st.Signal.MeasuredFPS == nil || *st.Signal.MeasuredFPS != 59.94 {
		t.Errorf("with frames: signal %+v", st.Signal)
	}
}
