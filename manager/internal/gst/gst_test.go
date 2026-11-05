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

// The animator against a real GstVideoAggregator (the software compositor: nvcompositor on the board is
// the same base class, 1.14's libgstbadvideo): a fade table moves exactly one step per output frame,
// with no repeated or skipped steps, and the last step stays. Only where gst-plugins-bad is installed.
func TestAnimatorOneStepPerFrame(t *testing.T) {
	g := New()
	if !g.HasElement("compositor") {
		t.Skip("no compositor (gst-plugins-bad)")
	}
	caps := "video/x-raw,format=I420,width=64,height=36,framerate=30/1"
	p, err := g.Parse("anim", "compositor name=c background=black ! video/x-raw,format=I420,width=64,height=36 "+
		"! appsink name=out emit-signals=false sync=false max-buffers=200 drop=false "+
		"videotestsrc pattern=black num-buffers=120 ! "+caps+" ! c.sink_0 "+
		"videotestsrc pattern=white num-buffers=120 ! "+caps+" ! c.sink_1")
	if err != nil {
		t.Fatal(err)
	}
	a := g.NewAnimator(p.ByName("c"), 2, time.Second/30)
	defer func() { p.Close(); a.Free() }() // Free only once the pipeline is in NULL
	const n = 30
	steps := make([][]Layer, 0, n+1)
	for k := 0; k <= n; k++ {
		steps = append(steps, []Layer{{W: 64, H: 36, Alpha: 1, Z: 1}, {W: 64, H: 36, Alpha: float64(k) / n, Z: 2}})
	}
	a.Play(steps)
	if !a.Busy() {
		t.Fatal("not busy with a table")
	}
	if err := p.SetState(StatePlaying); err != nil {
		t.Fatal(err)
	}
	out := p.ByName("out")
	var luma []float64
	for len(luma) < 60 {
		b := out.PullBytes(2 * time.Second)
		if b == nil {
			break
		}
		sum := 0
		for _, v := range b[:64*36] {
			sum += int(v)
		}
		luma = append(luma, float64(sum)/(64*36))
	}
	if len(luma) < 50 {
		t.Fatalf("%d frames", len(luma))
	}
	// The first frame was composed before any step (both pads at their defaults: white on top); from the
	// second, step k on frame k+1: black (alpha 0) and then up by 1/n a frame until white
	ramp := luma[1 : n+2]
	step := (ramp[n] - ramp[0]) / n
	if step < 5 {
		t.Fatalf("no fade: %v", luma)
	}
	for k := 1; k <= n; k++ {
		if d := ramp[k] - ramp[k-1]; d < step*0.5 || d > step*1.5 {
			t.Errorf("frame %d: luma +%.1f, want +%.1f (one step a frame): %v", k+1, d, step, ramp)
		}
	}
	for _, v := range luma[n+2:] {
		if v != ramp[n] {
			t.Errorf("after the table: %.1f, want the last step (%.1f) to stay", v, ramp[n])
		}
	}
	if a.Busy() {
		t.Error("still busy after the last step")
	}
}

// The tiles' gate (MIXER.md §24): one frame per screen frame, by the pts on the screen's grid, whatever
// the arrival jitter; by arrival only without a pts.
func TestMvGateOnePerScreenFrame(t *testing.T) {
	ms := time.Millisecond
	off := 100*ms - time.Second/60 // TileDelay at 60 with MIXER_LATENCY 60
	slot := func(pts time.Duration) int64 { return int64((pts + off) * 30 / time.Second) }
	// passed runs the gate over n frames at fps with arrivals jittered by up to jit, and returns the slots
	// of those it let through
	passed := func(fps, n int, jit time.Duration) []int64 {
		g := newMvGate()
		var got []int64
		for k := range n {
			pts := time.Duration(k) * time.Second / time.Duration(fps)
			// arrivals: 19 ms after the capture plus a jitter that alternates sign and grows (like the Logi's)
			arr := time.Hour + pts + 19*ms + time.Duration((k*7919)%int(jit/ms+1))*ms
			if g.step(30, off, pts, arr) {
				got = append(got, slot(pts))
			}
		}
		return got
	}
	for _, c := range []struct {
		fps  int
		want int // frames through in 3 s
	}{{60, 90}, {50, 90}, {30, 90}} {
		got := passed(c.fps, 3*c.fps, 25*ms)
		if len(got) < c.want-1 || len(got) > c.want+1 {
			t.Errorf("%d fps: %d through in 3 s, want %d (30 a second)", c.fps, len(got), c.want)
		}
		for i := 1; i < len(got); i++ {
			if got[i] != got[i-1]+1 {
				t.Errorf("%d fps: slots %d then %d: a screen frame without a new picture or with two", c.fps, got[i-1], got[i])
				break
			}
		}
	}
	// A 25 one: every frame, one screen frame in six without a new one (no way around it at 30)
	if got := passed(25, 75, 25*ms); len(got) != 75 {
		t.Errorf("25 fps: %d of 75 through", len(got))
	}
	// A pts that jumps forward (a larger sync offset): the next one goes, the empty slots stay empty
	g := newMvGate()
	if !g.step(30, 0, 0, 0) || g.step(30, 0, 10*ms, 0) || !g.step(30, 0, 500*ms, 0) || g.step(30, 0, 510*ms, 0) || !g.step(30, 0, 534*ms, 0) {
		t.Error("forward jump")
	}
	// No screen: nothing; no pts: by arrival, one every 3/4 of a screen frame (25 ms at 30)
	if newMvGate().step(0, 0, 0, 0) {
		t.Error("fps 0 lets frames through")
	}
	g = newMvGate()
	var n int
	for k := range 60 {
		if g.step(30, 0, -1, time.Hour+time.Duration(k)*time.Second/60) {
			n++
		}
	}
	if n != 30 {
		t.Errorf("without pts, a 60 one by arrival: %d of 60, want 30", n)
	}
	// After a reset (another screen pipeline) the same slot goes again
	g = newMvGate()
	g.step(30, 0, 0, 0)
	g.g = newMvGate().g
	if !g.step(30, 0, 5*ms, 0) {
		t.Error("reset")
	}
	// A regrid (the same screen, its phase measured: 13 ms): the frame just sent keeps its screen frame on
	// the new grid, so the next one in it waits; the one after the new boundary goes
	g = newMvGate()
	if !g.step(30, 0, 20*ms, 0) {
		t.Fatal("first")
	}
	g.regrid(30, -13*ms) // 20 ms → 7 ms on the new grid: frame 0, which ends at 33.3 → pts 46.3
	if g.step(30, -13*ms, 40*ms, 0) || !g.step(30, -13*ms, 47*ms, 0) {
		t.Error("regrid: the slot of the last frame sent is kept on the new grid")
	}
	// A still takes its screen frame: a live frame in it waits for the next
	g = newMvGate()
	g.mark(30, 0, 10*ms)
	if g.step(30, 0, 20*ms, 0) || !g.step(30, 0, 34*ms, 0) {
		t.Error("still: its screen frame is taken")
	}
	g.mark(30, 0, 30*ms) // an older still does not move the gate back
	if g.step(30, 0, 40*ms, 0) {
		t.Error("still from the past moved the gate")
	}
}

// Real GStreamer: a 50 fps source gives its tile 30 frames a second (before Nov 5: every other frame, 25)
// and the program all 50.
func TestBridgeScreenTileAt30From50(t *testing.T) {
	g := New()
	pgm, err := g.Parse("program", "appsrc name=m0 is-live=true format=time do-timestamp=true min-latency=0 "+
		"max-latency=500000000 max-bytes=0 block=false caps=video/x-raw,format=I420,width=64,height=36,framerate=50/1 "+
		"! queue ! fakesink name=out sync=false")
	if err != nil {
		t.Fatal(err)
	}
	defer pgm.Close()
	scr, err := g.Parse("screen", "appsrc name=mvs0 is-live=true format=time do-timestamp=true min-latency=0 "+
		"max-latency=500000000 max-bytes=0 block=false caps=video/x-raw,format=I420,width=64,height=36,framerate=30/1 "+
		"! queue ! fakesink name=tile sync=false")
	if err != nil {
		t.Fatal(err)
	}
	defer scr.Close()
	src, err := g.Parse("source", "videotestsrc is-live=true pattern=ball ! video/x-raw,format=I420,width=64,height=36,framerate=50/1 "+
		"! appsink name=snk emit-signals=false sync=false max-buffers=2 drop=true enable-last-sample=false")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	pgm.FixBase(g.Now())
	scr.FixBase(pgm.BaseTime())
	b := g.NewBridge(BridgeSource, pgm, pgm.ByName("m0"))
	defer b.Free()
	b.SetParams(BridgeParams{Sync: 120 * time.Millisecond})
	b.SetScreen(scr.ByName("mvs0"), ScreenGrid{FPS: 30, Offset: 80 * time.Millisecond})
	out, tile := g.NewCounter(pgm.ByName("out"), "sink"), g.NewCounter(scr.ByName("tile"), "sink")
	for _, p := range []Pipeline{pgm, scr} {
		if err := p.SetState(StatePlaying); err != nil {
			t.Fatal(err)
		}
	}
	b.Attach(src.ByName("snk"))
	if err := src.SetState(StatePlaying); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // settle
	out.Take()
	tile.Take()
	time.Sleep(3 * time.Second)
	np, _ := out.Take()
	nt, _ := tile.Take()
	if np < 140 || np > 160 {
		t.Errorf("program: %d frames in 3 s, want ~150", np)
	}
	if nt < 85 || nt > 95 {
		t.Errorf("tile: %d frames in 3 s, want ~90 (30 a second)", nt)
	}
	// Off the screen: nothing more to the tile
	b.SetScreen(nil, ScreenGrid{})
	time.Sleep(200 * time.Millisecond)
	tile.Take()
	time.Sleep(300 * time.Millisecond)
	if n, _ := tile.Take(); n != 0 {
		t.Errorf("tile after SetScreen(nil): %d", n)
	}
	b.Detach() // before the deferred Free: the source's appsink would still call into it
}

// DropDuration: on a pad that exists, and the frames keep flowing.
func TestDropDuration(t *testing.T) {
	g := New()
	p, err := g.Parse("dur", "videotestsrc is-live=true ! video/x-raw,format=I420,width=64,height=36,framerate=30/1 "+
		"! queue name=q ! fakesink name=out sync=false")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	q := p.ByName("q")
	if !q.DropDuration("sink") || q.DropDuration("nope") {
		t.Fatal("DropDuration should say whether the pad exists")
	}
	cnt := g.NewCounter(p.ByName("out"), "sink")
	if err := p.SetState(StatePlaying); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if n, _ := cnt.Take(); n < 20 {
		t.Fatalf("frames through: %d", n)
	}
}
