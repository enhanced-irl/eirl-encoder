package netcam

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pathJSON = `{"name":"cam1","ready":true,"source":{"type":"srtConn","id":"d50c2c99-588f-4f26-ab8c-e5e28d6bcda1"},` +
	`"tracks":["H265","MPEG-4 Audio"],"bytesReceived":63225030}`

const decCaps = "video/x-raw(memory:NVMM), format=(string)NV12, width=(int)1080, height=(int)1920, " +
	"interlace-mode=(string)progressive, framerate=(fraction)30/1"

// NetTest.test_valid
func TestValid(t *testing.T) {
	if !Valid("net:cam1", "rtsp://127.0.0.1:8554/cam1") {
		t.Fatal("valid camera")
	}
	for _, c := range [][2]string{{"net:cam9", "rtsp://127.0.0.1:8554/cam9"}, {"net:cam1", "rtsp://127.0.0.1:8554/cam2"},
		{"net:cam1", "rtsp://10.42.0.1:8554/cam1"}, {"net:cam1/../x", "rtsp://127.0.0.1:8554/cam1/../x"},
		{"hdmi", "/dev/video0"}, {"", ""}} {
		if Valid(c[0], c[1]) {
			t.Errorf("%v", c)
		}
	}
	if PathOf("net:cam3") != "cam3" {
		t.Error("PathOf")
	}
}

// NetTest.test_clean_name
func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{"Móvil de Ana": "Móvil de Ana", "<b>x</b>": "bxb", "   ": "Cámara wifi"} {
		if got := CleanName(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	if n := len([]rune(CleanName(strings.Repeat("a", 80)))); n != 48 {
		t.Errorf("length %d", n)
	}
}

// NetTest.test_parse_path
func TestParsePath(t *testing.T) {
	p, ok := ParsePath([]byte(pathJSON))
	if !ok || !p.Ready || p.Video != "h265" || p.Audio != "aac" || p.Publisher != "d50c2c99-588f-4f26-ab8c-e5e28d6bcda1" {
		t.Fatalf("%+v", p)
	}
	off, _ := ParsePath([]byte(`{"name":"cam1","ready":false,"source":null,"tracks":[]}`))
	if off.Ready || off.Video != "" || off.Audio != "" {
		t.Errorf("off: %+v", off)
	}
	odd, _ := ParsePath([]byte(`{"ready":true,"tracks":["VP8","G711"],"source":{"id":"` + strings.Repeat("x", 100) + `"}}`))
	if odd.Video != "" || odd.Audio != "" || odd.Publisher != "" {
		t.Errorf("odd: %+v", odd)
	}
	if _, ok := ParsePath([]byte(`"no"`)); ok {
		t.Error("a string is not a path")
	}
}

// NetTest.test_path_state
func TestPathState(t *testing.T) {
	key := strings.Repeat("K", 32)
	dir := t.TempDir()
	os.WriteFile(KeyFile(dir), []byte(key+"\n"), 0o640)
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+" "+r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v3/paths/get/cam1":
			w.Write([]byte(pathJSON))
		case "/v3/paths/get/cam2":
			http.NotFound(w, r)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	c := &Client{KeyFile: KeyFile(dir), BaseURL: srv.URL}
	p, err := c.State("cam1")
	if err != nil || p.Video != "h265" {
		t.Fatalf("%+v %v", p, err)
	}
	if want := "/v3/paths/get/cam1 Basic " + base64.StdEncoding.EncodeToString([]byte("eirl-app:"+key)); got[0] != want {
		t.Errorf("request %q, want %q", got[0], want)
	}
	if _, err := c.State("../v3/config"); err == nil || len(got) != 1 {
		t.Error("a path outside cam1-8 is never asked")
	}
	if p, err := c.State("cam2"); err != nil || p.Ready {
		t.Errorf("404 = no publisher: %+v %v", p, err)
	}
	if _, err := c.State("cam3"); err == nil {
		t.Error("500 = unavailable")
	}
	down := &Client{KeyFile: KeyFile(dir), BaseURL: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: time.Second}}
	if _, err := down.State("cam1"); err == nil {
		t.Error("down = unavailable")
	}
	// A key that does not fit is not sent
	os.WriteFile(KeyFile(dir), []byte("short"), 0o640)
	os.Chtimes(KeyFile(dir), time.Now(), time.Now().Add(time.Second))
	c.State("cam1")
	if !strings.HasSuffix(got[len(got)-1], "cam1 ") {
		t.Errorf("bad key sent: %q", got[len(got)-1])
	}
}

// NetTest.test_streams_and_mode (decoded mode and its text; select-stream is in internal/gst)
func TestStreamsAndMode(t *testing.T) {
	w, h, fps, ok := DecodedMode(decCaps)
	if !ok || w != 1080 || h != 1920 || fps != 30 {
		t.Fatalf("%d %d %v %v", w, h, fps, ok)
	}
	if ModeText(1080, 1920, 30) != "1080x1920@30" || ModeText(1920, 1080, 29.97) != "1920x1080@29.97" || ModeText(640, 480, 0) != "640x480" {
		t.Error("ModeText")
	}
	if _, _, _, ok := DecodedMode("video/x-raw"); ok {
		t.Error("no size")
	}
}

// NetTest.test_cameras_stamp
func TestStamp(t *testing.T) {
	dir := t.TempDir()
	f := CamerasFile(dir)
	if Stamp(f) != "" {
		t.Fatal("missing file")
	}
	os.WriteFile(f, []byte("[]"), 0o640)
	a := Stamp(f)
	os.WriteFile(f, []byte("[1]"), 0o640)
	if a == "" || Stamp(f) == a || filepath.Base(f) != "cameras-public.json" {
		t.Error("stamp changes with the file")
	}
}
