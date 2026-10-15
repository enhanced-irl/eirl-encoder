//go:build linux && cgo

package phones

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// With GStreamer 1.14 in the build image (base plugins, no sound card): the listening's description
// builds, the Tap carries the program's audio across, and an alsasink that cannot open ends in error.

// realProgram is the program's audio end, with the same phvalve → phtap branch as pipeline/program.go.
func realProgram(t *testing.T, g gst.Real) gst.Pipeline {
	t.Helper()
	pgm, err := g.Parse("program", "audiotestsrc is-live=true wave=silence samplesperbuffer=1920 ! "+ACaps+" ! tee name=at "+
		"at. ! queue ! fakesink sync=false "+
		"at. ! queue leaky=downstream max-size-buffers=12 ! valve name=phvalve drop=true "+
		"! appsink name=phtap emit-signals=false sync=false async=false max-buffers=12 drop=true enable-last-sample=false")
	if err != nil {
		t.Fatal(err)
	}
	pgm.FixBase(g.Now())
	if err := pgm.SetState(gst.StatePlaying); err != nil {
		t.Fatal(err)
	}
	return pgm
}

// recMedia keeps the listening pipelines it parses.
type recMedia struct {
	gst.Real
	mu    sync.Mutex
	pipes []gst.Pipeline
}

func (m *recMedia) Parse(label, desc string) (gst.Pipeline, error) {
	p, err := m.Real.Parse(label, desc)
	if err == nil && label == "headphones" {
		m.mu.Lock()
		m.pipes = append(m.pipes, p)
		m.mu.Unlock()
	}
	return p, err
}

func realPhones(t *testing.T, m gst.Media, pgm gst.Pipeline) *Phones {
	t.Helper()
	p, err := New(Options{Media: m, Program: pgm, CompositorLatency: 60 * ms})
	if err != nil {
		t.Fatal(err)
	}
	p.asound = t.TempDir()
	p.digital = func(int) {}
	p.measure = 200 * ms
	return p
}

func TestRealPlaysThroughTheTap(t *testing.T) {
	quiet(t)
	g := gst.New()
	pgm := realProgram(t, g)
	m := &recMedia{Real: g}
	p := realPhones(t, m, pgm)
	p.sink = "fakesink name=phsink sync=true"
	p.find = func() (Device, bool) { return Device{Name: "hw:99,0", Card: 99}, true }
	p.Start()
	for i := 0; i < 300 && p.Info().State != StateOn; i++ {
		time.Sleep(10 * ms)
	}
	if i := p.Info(); i.State != StateOn {
		t.Fatal(i)
	}
	m.mu.Lock()
	lis := m.pipes[0]
	m.mu.Unlock()
	cnt := g.NewCounter(lis.ByName("phsink"), "sink")
	time.Sleep(time.Second)
	if n, _ := cnt.Take(); n < 10 { // 40 ms chunks: ~25 a second
		t.Errorf("%d buffers reached the listening's sink in 1 s", n)
	}
	if lis.BaseTime() != pgm.BaseTime() {
		t.Error("base_time", lis.BaseTime(), pgm.BaseTime())
	}
	t0 := time.Now()
	p.Stop()
	select {
	case <-p.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("does not stop within the engine's budget")
	}
	t.Logf("stopped in %v", time.Since(t0))
	pgm.Close()
	p.Free()
}

func TestRealAlsaFailsToOpen(t *testing.T) {
	g := gst.New()
	if !g.HasElement("alsasink") {
		t.Skip("no alsasink in this image")
	}
	logs := quiet(t)
	pgm := realProgram(t, g)
	p := realPhones(t, g, pgm)
	p.find = func() (Device, bool) { return Device{Name: "hw:99,0", Card: 99}, true }
	p.retry = []time.Duration{time.Hour}
	p.Start()
	for i := 0; i < 300 && p.Info().State != StateError; i++ {
		time.Sleep(10 * ms)
	}
	i := p.Info()
	if i.State != StateError || i.Detail == "" || i.Detail == NoDevice || strings.HasPrefix(i.Detail, cannotBuild) {
		t.Error(i)
	}
	t.Logf("detail %q", i.Detail)
	p.Stop()
	select {
	case <-p.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("does not stop")
	}
	if !strings.Contains(logs.String(), "event=phones_retry") {
		t.Error(logs.String())
	}
	pgm.Close()
	p.Free()
}
