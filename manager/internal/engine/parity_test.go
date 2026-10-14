package engine

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst/fake"
)

// LayoutTest.test_discover_in_thread: `probe -list` runs in a goroutine and its result is applied on
// the actor, once; while one runs no other starts.
func TestDiscoverInThread(t *testing.T) {
	e, _, r := startEngine(t, nil, catalogue)
	gate := make(chan struct{})
	r.mu.Lock()
	r.gate, r.listCalls = gate, 0
	r.mu.Unlock()
	e.disc.retryAt = time.Now().Add(-time.Second) // something changed
	e.discoverTick()
	if !e.disc.probing {
		t.Fatal("the probe runs in a goroutine")
	}
	e.disc.retryAt = time.Now().Add(-time.Second)
	e.discoverTick() // one is running: no other
	close(gate)
	var fn func()
	select {
	case fn = <-e.events:
	case <-time.After(5 * time.Second):
		t.Fatal("the result never reached the actor")
	}
	fn()
	r.mu.Lock()
	calls := r.listCalls
	r.mu.Unlock()
	if e.disc.probing || calls != 1 {
		t.Fatalf("probing %v, calls %d", e.disc.probing, calls)
	}
	select {
	case <-e.events:
		t.Fatal("applied once")
	default:
	}
}

// TimingTest.test_network_lag: a network camera with seconds of backlog is reopened (trap 16); the
// publisher changing is the camera gone.
func TestNetworkLag(t *testing.T) {
	e, m, _ := startEngine(t, nil, catalogue)
	pub := "p1"
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Write([]byte(`{"ready":true,"tracks":["H265","MPEG-4 Audio"],"source":{"id":"` + pub + `"}}`))
	}))
	defer srv.Close()
	e.net.BaseURL = srv.URL
	s := newSource(e, 3, "net:cam1", "network", "Móvil", "rtsp://127.0.0.1:8554/cam1")
	s.net.Publisher = "p1"
	b := m.Bridges[3]
	b.SetStats(gst.BridgeStats{Lag: 3100 * time.Millisecond})
	if got := s.stillValid(); got.Text != "va 3.1 s atrasada" || got.Code != "net_behind" || got.Args["seconds"] != "3.1" {
		t.Fatalf("lag: %q", got)
	}
	if got := s.stillValid(); got.Code != "" {
		t.Fatalf("the count starts again: %q", got)
	}
	b.SetStats(gst.BridgeStats{Lag: 50 * time.Millisecond})
	if got := s.stillValid(); got.Code != "" {
		t.Fatalf("one fresh frame is enough: %q", got)
	}
	mu.Lock()
	pub = "p2"
	mu.Unlock()
	if got := s.stillValid(); got.Text != "la cámara se ha desconectado" || got.Code != "net_disconnected" {
		t.Fatalf("publisher: %q", got)
	}
}

// JpegParseTest.test_needs_jpegparse
func TestNeedsJpegparse(t *testing.T) {
	e, _, _ := startEngine(t, nil, catalogue)
	s := newSource(e, 3, "/dev/v4l/by-id/x", "usb", "x", "")
	p := pipeline.Probe{Node: "/dev/video1", Format: "MJPG", Width: 1920, Height: 1080, FPSNum: 60, FPSDen: 1}
	plain, parsed := e.plat.USB(p, 1920, 1080, false), e.plat.USB(p, 1920, 1080, true)
	yuy := p
	yuy.Format = "YUY2"
	s.noImage = 1
	if s.needsJPEGParse(plain, "", false) {
		t.Error("one opening without image: no (the MS2109)")
	}
	s.noImage = 2
	cases := []struct {
		desc, src string
		had, want bool
	}{
		{plain, "", false, true},                 // two in a row: yes
		{plain, "nvv4l2decoder0", true, true},    // the decoder fails
		{plain, "cap", true, false},              // something else (unplugged…)
		{parsed, "nvv4l2decoder0", false, false}, // it has it already
		{e.plat.USB(yuy, 1920, 1080, false), "", false, false},
	}
	for i, c := range cases {
		if got := s.needsJPEGParse(c.desc, c.src, c.had); got != c.want {
			t.Errorf("case %d: %v", i, got)
		}
	}
}

func nvcamSource(t *testing.T) (*runner, *fake.Media, *Source, chan struct{}) {
	t.Helper()
	old := closeWait
	closeWait = 50 * time.Millisecond
	t.Cleanup(func() { closeWait = old })
	e, m, r := startEngine(t, nil, catalogue)
	s := newSource(e, 3, "hdmi", "hdmi-csi", "HDMI", "/dev/video0")
	s.nvcam = true
	release := make(chan struct{})
	m.BlockClose["source-stuck"] = release
	return r, m, s, release
}

// NvCamTest.test_close_unstuck_by_streamoff
func TestCloseUnstuckByStreamoff(t *testing.T) {
	_, m, s, release := nvcamSource(t)
	m.BlockClose["unblock-on-streamoff"] = release
	p, _ := m.Parse("source-stuck", "fakesrc name=snk")
	s.close(p, p.ByName("snk"))
	if s.stuckDone != nil || len(m.StreamOffs) != 1 || m.StreamOffs[0] != "/dev/video0" {
		t.Fatalf("stuck %v, streamoffs %v", s.stuckDone, m.StreamOffs)
	}
}

// NvCamTest.test_close_stuck: left behind; while it is still open the slot says so and does not try;
// once it closes, it comes back with v4l2src
func TestCloseStuck(t *testing.T) {
	r, m, s, release := nvcamSource(t)
	p, _ := m.Parse("source-stuck", "fakesrc name=snk")
	s.close(p, p.ByName("snk"))
	if s.stuckDone == nil {
		t.Fatal("left behind")
	}
	if _, err := s.prepareCSI(1920, 1080); err == nil || err.(*notReady).state != StateError {
		t.Fatalf("with the device still open it does not try: %v", err)
	}
	close(release)
	<-s.stuckDone
	r.mu.Lock()
	r.dv = "\tActive width: 1920\n\tActive height: 1080\n\tTotal width: 2200\n\tPixelclock: 74250000 Hz (30.00 frames per second)\n"
	r.mu.Unlock()
	d, err := s.prepareCSI(1920, 1080)
	if err != nil || !strings.HasPrefix(d, "v4l2src") || s.nvcam {
		t.Fatalf("back with v4l2src: %v %q nvcam=%v", err, d, s.nvcam)
	}
}
