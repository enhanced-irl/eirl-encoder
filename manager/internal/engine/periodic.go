package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

const (
	timingsEvery = 60 * time.Second
	memoryEvery  = 10 * time.Minute
)

type periodicState struct {
	last       time.Time
	frames     map[string]uint64 // per source id: bridge frames at the last periodic
	cadTick    int               // new frames are counted 2 s of every 10: open while 0
	cadFrom    string            // the source on air and live when the window opened
	cadWarned  time.Time
	timingsAt  time.Time
	memoryAt   time.Time
	meterAt    time.Time
	meterTotal uint64
}

// periodic, every 2 s: each source's fps, the program's new frames, the timings log, and a publish.
func (e *Engine) periodic() {
	now := time.Now()
	p := &e.per
	if p.last.IsZero() {
		p.last, p.timingsAt, p.memoryAt = now, now, now.Add(time.Minute-memoryEvery) // the first one a minute in
		p.frames = map[string]uint64{}
	}
	dt := now.Sub(p.last).Seconds()
	p.last = now
	var onAir *Source
	for i, s := range e.sources {
		if s == nil {
			continue
		}
		n := e.bridges[i].Stats(false, false).Frames
		prev, ok := p.frames[s.id]
		if !ok {
			prev = n
		}
		p.frames[s.id] = n
		state, _, _ := s.snapshot()
		fps := 0.0
		if dt > 0 && state == StateLive {
			fps = float64(n-prev) / dt
		}
		s.mu.Lock()
		s.fps = fps
		s.mu.Unlock()
		if s.id == e.scene.Main && state == StateLive {
			onAir = s
		}
	}
	e.cadenceWindow(dt, now, onAir)
	if now.Sub(p.timingsAt) >= timingsEvery {
		p.timingsAt = now
		e.logTimings()
	}
	if now.Sub(p.memoryAt) >= memoryEvery {
		p.memoryAt = now
		go logMemory() // malloc_trim walks every arena: not on the actor
	}
	e.measured = readFPS(e.runDir)
	e.publish()
}

// cadenceWindow: new (not repeated) program frames per second, measured 2 s of every 10 with the
// cadvalve open (out of the window it drops before converting). A low count with the source on air and
// live since the window opened means the compositor drops its frames (≤ one warning a minute).
func (e *Engine) cadenceWindow(dt float64, now time.Time, onAir *Source) {
	if e.cadence == nil {
		return
	}
	p := &e.per
	all, fresh := e.cadence.Take()
	if p.cadTick == 0 && dt > 0 {
		v := round1(float64(fresh) / dt)
		e.newFPS = &v
		if onAir != nil && all >= 20 && float64(fresh) < float64(all)*0.3 && p.cadFrom == onAir.id && now.Sub(p.cadWarned) >= time.Minute {
			p.cadWarned = now
			elog.Event("mixer", "program_cadence_low", "new_fps", fmt.Sprintf("%.1f", float64(fresh)/dt),
				"fps", fmt.Sprintf("%.0f", float64(all)/dt), "source", onAir.name)
		}
	}
	p.cadFrom = ""
	if onAir != nil {
		p.cadFrom = onAir.id
	}
	p.cadTick = (p.cadTick + 1) % 5
	if e.cadValve != nil {
		e.cadValve.SetBool("drop", p.cadTick != 0)
	}
}

// logTimings, every 60 s: per live source, the age of its frames on arrival (capture → program) and the
// longest gap between two arrivals. The window restarts each time.
func (e *Engine) logTimings() {
	kv := []any{"clock", "system"}
	for i, s := range e.sources {
		if s == nil {
			continue
		}
		st := e.bridges[i].Stats(true, false)
		if state, _, _ := s.snapshot(); state != StateLive || st.N == 0 {
			continue
		}
		kv = append(kv, fmt.Sprintf("slot%d", i+1), fmt.Sprintf("age=%s/%s/%sms gap=%sms",
			fmtMs(st.AgeMin), fmtMs(st.AgeAvg), fmtMs(st.AgeMax), fmtMs(st.MaxGap)))
	}
	if e.audio != nil {
		kv = append(kv, e.audio.TimingsKV()...)
	}
	if e.scr != nil {
		if d := e.scr.TakeDrawMax(); d > 0 {
			kv = append(kv, "draw_max_ms", fmtMs(d))
		}
		if d := e.scr.TakeTouchMax(); d > 0 {
			kv = append(kv, "touch_max_ms", fmtMs(d))
		}
	}
	e.scrMu.Lock()
	if e.mvBridge != nil {
		if st := e.mvBridge.Stats(true, false); st.N > 0 {
			kv = append(kv, "program_to_screen", fmt.Sprintf("age=%s/%s/%sms gap=%sms", fmtMs(st.AgeMin), fmtMs(st.AgeAvg), fmtMs(st.AgeMax), fmtMs(st.MaxGap)))
		}
	}
	e.scrMu.Unlock()
	elog.Event("mixer", "timings", kv...)
}

// videoMeter writes RUN_DIR/video every second (FPS and FRAMES of the program, after the compositor),
// like engine-run.py's VideoMeter: the engine's measuredFps and the app read it.
func (e *Engine) videoMeter() {
	if e.vsrc == nil || e.runDir == "" {
		return
	}
	now := time.Now()
	n, _ := e.vsrc.Take()
	p := &e.per
	p.meterTotal += n
	if !p.meterAt.IsZero() {
		if dt := now.Sub(p.meterAt).Seconds(); dt > 0 {
			body := fmt.Sprintf("FPS=%.2f\nFRAMES=%d\n", float64(n)/dt, p.meterTotal)
			_ = sysx.WriteAtomic(filepath.Join(e.runDir, "video"), []byte(body), 0o644)
		}
	}
	p.meterAt = now
}

// logMemory, every 10 min: what glibc keeps free in its arenas goes back to the system (malloc_trim: the
// pipelines that closed since), and the RSS against the Go heap, to tell a leak in Go from one in C
// (GStreamer, the NVIDIA libraries) over a long run (ENGINE_GO.md §14). ReadMemStats stops the world for
// a few microseconds.
func logMemory() {
	before, _ := procStatus()
	t0 := time.Now()
	gst.TrimMalloc()
	trimMs := time.Since(t0).Milliseconds()
	rss, threads := procStatus()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	mb := func(b uint64) string { return strconv.FormatFloat(float64(b)/(1<<20), 'f', 1, 64) }
	elog.Event("engine", "memory", "rss_mb", mb(rss), "trimmed_mb", mb(before-min(before, rss)), "trim_ms", trimMs,
		"go_heap_mb", mb(m.HeapInuse), "go_sys_mb", mb(m.Sys), "goroutines", runtime.NumGoroutine(), "threads", threads)
}

// procStatus is the process' RSS (bytes) and threads, from /proc/self/status.
func procStatus() (rss uint64, threads string) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) >= 2 && f[0] == "VmRSS:":
			if kb, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				rss = kb << 10
			}
		case len(f) >= 2 && f[0] == "Threads:":
			threads = f[1]
		}
	}
	return rss, threads
}
