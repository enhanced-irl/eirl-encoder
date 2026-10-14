//go:build linux && cgo

package gst

import (
	"strings"
	"testing"
	"time"
)

func TestABI(t *testing.T) {
	New()
	for _, b := range abiCheck() {
		t.Error(b)
	}
	if !strings.HasPrefix(Version(), "1.") {
		t.Errorf("version %q", Version())
	}
}

// Real GStreamer (the build image has the base plugins): a live source in its own pipeline crosses a
// bridge into a "program" that shares nothing with it, like the engine's sources.
func TestBridgeMovesFramesAcrossPipelines(t *testing.T) {
	g := New()
	pgm, err := g.Parse("program", "appsrc name=m0 is-live=true format=time do-timestamp=true min-latency=0 "+
		"max-latency=500000000 max-bytes=0 block=false caps=video/x-raw,format=I420,width=64,height=36,framerate=30/1 "+
		"! queue ! fakesink name=out sync=false")
	if err != nil {
		t.Fatal(err)
	}
	defer pgm.Close()
	src, err := g.Parse("source", "videotestsrc is-live=true pattern=ball ! video/x-raw,format=I420,width=64,height=36,framerate=30/1 "+
		"! appsink name=snk emit-signals=false sync=false max-buffers=2 drop=true enable-last-sample=false")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	pgm.FixBase(g.Now())
	m0, out, snk := pgm.ByName("m0"), pgm.ByName("out"), src.ByName("snk")
	if m0 == nil || out == nil || snk == nil || pgm.ByName("nope") != nil {
		t.Fatal("ByName")
	}
	b := g.NewBridge(BridgeSource, pgm, m0)
	defer b.Free()
	b.SetParams(BridgeParams{Sync: 120 * time.Millisecond})
	cnt := g.NewCounter(out, "sink")
	if cnt == nil || g.NewCounter(out, "nope") != nil {
		t.Fatal("NewCounter")
	}
	b.Attach(snk)
	if err := pgm.SetState(StatePlaying); err != nil {
		t.Fatal(err)
	}
	if err := src.SetState(StatePlaying); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	st := b.Stats(true, true)
	n, _ := cnt.Take()
	if st.Frames < 60 || n < 50 {
		t.Fatalf("frames: bridge %d, program %d", st.Frames, n)
	}
	if st.Judge != 1 {
		t.Errorf("capture times of a live videotestsrc should be usable, judge %d (min %v)", st.Judge, st.JudgeMin)
	}
	if st.AgeMax > 100*time.Millisecond || st.MaxGap > 200*time.Millisecond || st.Lag < 0 {
		t.Errorf("age max %v, gap %v, lag %v", st.AgeMax, st.MaxGap, st.Lag)
	}
	if g.Monotonic()-st.LastFrame > 200*time.Millisecond {
		t.Errorf("last frame %v ago", g.Monotonic()-st.LastFrame)
	}
	if s := snk.CapsString("sink"); !strings.Contains(s, "width=(int)64") {
		t.Errorf("caps: %q", s)
	}
	// Detached: the frames of that appsink no longer count
	b.Detach()
	before := b.Stats(false, false).Frames
	time.Sleep(300 * time.Millisecond)
	if after := b.Stats(false, false).Frames; after != before {
		t.Errorf("frames after detach: %d -> %d", before, after)
	}
	if m := pgm.Pop(10*time.Millisecond, MessageError); m != nil {
		t.Fatalf("program error: %s: %s", m.Src, m.Text)
	}
}

func TestStillsAndErrors(t *testing.T) {
	g := New()
	if _, err := g.Parse("bad", "nosuchelement ! fakesink"); err == nil {
		t.Fatal("parse of an unknown element should fail")
	}
	p, err := g.Parse("still", "videotestsrc num-buffers=1 pattern=black ! video/x-raw,format=I420,width=64,height=36 ! appsink name=snk sync=false")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetState(StatePlaying); err != nil {
		t.Fatal(err)
	}
	buf := p.ByName("snk").PullBuffer(2 * time.Second)
	p.Close()
	if buf == nil {
		t.Fatal("no still")
	}
	defer buf.Unref()
	if w, h := buf.Size(); w != 64 || h != 36 {
		t.Fatalf("still %dx%d", w, h)
	}

	pgm, err := g.Parse("program", "appsrc name=m0 is-live=true format=time do-timestamp=true caps=video/x-raw,format=I420,width=64,height=36,framerate=30/1 "+
		"! fakesink name=out sync=false")
	if err != nil {
		t.Fatal(err)
	}
	defer pgm.Close()
	pgm.FixBase(g.Now())
	m0 := pgm.ByName("m0")
	cnt := g.NewCounter(pgm.ByName("out"), "sink")
	b := g.NewBridge(BridgeSource, pgm, m0)
	defer b.Free()
	b.SetParams(BridgeParams{Sync: 120 * time.Millisecond})
	if err := pgm.SetState(StatePlaying); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		b.PushStill(buf)
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if n, _ := cnt.Take(); n != 10 {
		t.Fatalf("stills through: %d", n)
	}
	if !m0.Set("max-latency", "400000000") || m0.Set("no-such-property", "1") {
		t.Fatal("Set should report whether the property exists")
	}
	// Errors reach the bus as messages
	e, err := g.Parse("err", "filesrc location=/nonexistent/eirl ! fakesink")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	_ = e.SetState(StatePlaying)
	if m := e.Pop(2*time.Second, MessageError|MessageEOS); m == nil || m.Type != MessageError || m.Text == "" {
		t.Fatalf("expected an error message, got %+v", m)
	}
}

func TestSelectStreamNeedsTheSignal(t *testing.T) {
	g := New()
	p, err := g.Parse("x", "fakesrc name=s ! fakesink")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.ByName("s").SelectStream("video") {
		t.Fatal("fakesrc has no select-stream")
	}
}

func TestFeeder(t *testing.T) {
	g := New()
	p, err := g.Parse("ui", "appsrc name=ui is-live=true format=time do-timestamp=true caps=video/x-raw,format=RGBA,width=8,height=4,framerate=20/1 "+
		"! fakesink name=out sync=false")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cnt := g.NewCounter(p.ByName("out"), "sink")
	f := g.NewFeeder(p.ByName("ui"), 8*4*4, 3)
	defer f.Free()
	if err := p.SetState(StatePlaying); err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 8*4*4)
	pushed := 0
	for i := range 20 {
		frame[0] = byte(i)
		if f.Push(frame) {
			pushed++
		}
		time.Sleep(10 * time.Millisecond)
	}
	if f.Push(make([]byte, 5)) {
		t.Error("a frame of the wrong size must be refused")
	}
	time.Sleep(200 * time.Millisecond)
	if n, b := cnt.Take(); pushed < 15 || int(n) != pushed || b != uint64(pushed*8*4*4) {
		t.Fatalf("pushed %d, through %d (%d bytes)", pushed, n, b)
	}
}

func TestTapPassesAndMeasures(t *testing.T) {
	g := New()
	pgm, err := g.Parse("program", "audiotestsrc is-live=true ! audio/x-raw,format=S16LE,rate=48000,channels=2 "+
		"! appsink name=phtap emit-signals=false sync=false async=false max-buffers=12 drop=true")
	if err != nil {
		t.Fatal(err)
	}
	defer pgm.Close()
	pgm.FixBase(g.Now())
	lis, err := g.Parse("listen", "appsrc name=src is-live=true format=time do-timestamp=false caps=audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved "+
		"! fakesink name=out sync=false")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	lis.FixBase(pgm.BaseTime())
	lis.SetLatency(200 * time.Millisecond)
	cnt := g.NewCounter(lis.ByName("out"), "sink")
	tp := g.NewTap(pgm.ByName("phtap"))
	if err := lis.SetState(StatePlaying); err != nil {
		t.Fatal(err)
	}
	if err := pgm.SetState(StatePlaying); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if n, _ := cnt.Take(); n != 0 {
		t.Fatalf("not connected: %d buffers went through", n)
	}
	if l := tp.TakeLate(); l <= 0 || l > 200*time.Millisecond {
		t.Errorf("late without destination: %v", l)
	}
	tp.Connect(lis.ByName("src"))
	time.Sleep(time.Second)
	if n, _ := cnt.Take(); n < 40 { // audiotestsrc: 1024 samples at 48 kHz = 47 buffers/s
		t.Fatalf("connected: only %d buffers", n)
	}
	tp.Connect(nil)
	pgm.Close()
	tp.Free()
}

// TimingTest.test_capture_judge
func TestCaptureJudge(t *testing.T) {
	ms := time.Millisecond
	limit := 150 * ms
	j := newJudge()
	for range 59 {
		if j.add(limit, 20*ms) {
			t.Fatal("undecided: arrival time")
		}
	}
	if !j.add(limit, 20*ms) {
		t.Fatal("a window of 20 ms ages is usable")
	}
	for range 59 {
		j.add(limit, 400*ms)
	}
	if j.add(limit, 47000*ms) {
		t.Error("a drifting clock (the Logi half hung) is not usable")
	}
	var ok bool
	for range 59 {
		ok = j.add(limit, 80*ms)
	}
	if ok = j.add(limit, 3*ms); !ok {
		t.Error("usable again: the window's minimum decides")
	}
	j2 := newJudge()
	for range 60 {
		ok = j2.add(limit, -50*ms)
	}
	if ok {
		t.Error("stamps in the future are not usable")
	}
	// Network cameras: their stamps come from RTP, believable with more margin
	j3 := newJudge()
	for range 60 {
		ok = j3.add(400*ms, 300*ms)
	}
	if !ok {
		t.Error("300 ms is fine with a 400 ms limit")
	}
}

// NvCamTest.test_capture_time_early: the capture is the stamp minus how far it is behind
func TestCaptureTimeEarly(t *testing.T) {
	s := time.Second
	at := captureTime(8*s, 1*s, 40*time.Millisecond, 0)
	if at != 9*s-40*time.Millisecond {
		t.Fatalf("capture %v", at)
	}
	if age := 10*s - at; age != s+40*time.Millisecond {
		t.Fatalf("age %v", age)
	}
	if captureTime(8*s, 1*s, 0, 419*time.Millisecond) != 9*s+419*time.Millisecond {
		t.Fatal("the CSI's raw clock offset is added (trap 24)")
	}
}
