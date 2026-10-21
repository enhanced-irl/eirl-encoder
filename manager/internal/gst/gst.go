//go:build linux && cgo

// Package gst is the engine's own, minimal GStreamer layer (ENGINE_GO.md §3, decision E1): the calls
// the engine needs, written for the board's GStreamer 1.14 and GLib 2.56.
//
// Rules:
//   - No frame ever enters Go. The per-frame work (bridges, cadence, counters) is C, in gstc.c.
//   - No callbacks from C into Go. Go reads counters and pops bus messages when it wants.
//   - Elements belong to their pipeline: ByName caches a reference that Close releases.
//
// The engine uses it through the interfaces of types.go; this file only builds on linux with cgo.
package gst

/*
#cgo pkg-config: gstreamer-1.0 gstreamer-app-1.0
#include <stdlib.h>
#include "gstc.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"
)

var initOnce sync.Once

// Real is GStreamer itself.
type Real struct{}

// New initialises GStreamer (once) and returns it.
func New() Real {
	initOnce.Do(func() { C.gst_init(nil, nil) })
	return Real{}
}

// Version is the runtime GStreamer version, e.g. "1.14.5".
func Version() string {
	var maj, min, mic, nano C.guint
	C.gst_version(&maj, &min, &mic, &nano)
	return fmt.Sprintf("%d.%d.%d", maj, min, mic)
}

func (Real) HasElement(factory string) bool {
	cs := C.CString(factory)
	defer C.free(unsafe.Pointer(cs))
	return C.eg_has_factory(cs) != 0
}

func (Real) Now() uint64 { return uint64(C.eg_now()) }

func (Real) Monotonic() time.Duration { return time.Duration(int64(C.g_get_monotonic_time()) * 1000) }

func (Real) StreamOff(node string) int {
	cs := C.CString(node)
	defer C.free(unsafe.Pointer(cs))
	return int(C.eg_streamoff(cs))
}

// --- pipelines and elements ---

// A pipeline and its elements are used from several goroutines (the engine, the sources, the screen,
// the headphones) while another may Close it. Every call takes the pipeline's read lock and checks it
// is still open; Close takes the write lock, so it waits for the calls in flight (a Pop or a PullBytes
// for at most their timeout) and nothing touches a freed GstElement afterwards.
type pipeline struct {
	p     *C.GstElement
	label string
	mu    sync.RWMutex
	elems map[string]*element
}

type element struct {
	p  *C.GstElement
	pl *pipeline
}

func (Real) Parse(label, desc string) (Pipeline, error) {
	cs := C.CString(desc)
	defer C.free(unsafe.Pointer(cs))
	var cerr *C.char
	p := C.eg_parse(cs, &cerr)
	if p == nil {
		msg := C.GoString(cerr)
		C.free(unsafe.Pointer(cerr))
		return nil, &ParseError{msg}
	}
	return &pipeline{p: p, label: label, elems: map[string]*element{}}, nil
}

func (p *pipeline) Label() string { return p.label }

// use is the open pipeline under its read lock, or nil (closed); done releases the lock.
func (p *pipeline) use() (el *C.GstElement, done func()) {
	p.mu.RLock()
	if p.p == nil {
		p.mu.RUnlock()
		return nil, func() {}
	}
	return p.p, p.mu.RUnlock
}

func (p *pipeline) ByName(name string) Element {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.p == nil {
		return nil
	}
	if e, ok := p.elems[name]; ok {
		return e
	}
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	el := C.eg_by_name(p.p, cs)
	if el == nil {
		return nil
	}
	e := &element{el, p}
	p.elems[name] = e
	return e
}

// ErrStateChange is a state change that failed (or a pipeline already closed).
var ErrStateChange = errors.New("state change failed")

func (p *pipeline) SetState(s State) error {
	el, done := p.use()
	defer done()
	if el == nil || C.eg_set_state(el, C.int(s)) == C.GST_STATE_CHANGE_FAILURE {
		return ErrStateChange
	}
	return nil
}

func (p *pipeline) FixBase(base uint64) {
	if el, done := p.use(); el != nil {
		C.eg_fix_base(el, C.GstClockTime(base))
		done()
	}
}

func (p *pipeline) BaseTime() uint64 {
	el, done := p.use()
	defer done()
	if el == nil {
		return 0
	}
	return uint64(C.eg_base_time(el))
}

func (p *pipeline) Pop(timeout time.Duration, types MessageType) *Message {
	el, done := p.use()
	if el == nil {
		done()
		time.Sleep(timeout) // a closed pipeline: callers poll; do not spin
		return nil
	}
	defer done()
	var m C.EgMessage
	if C.eg_pop(el, C.gint64(timeout), C.int(types), &m) == 0 {
		return nil
	}
	defer C.eg_message_free(&m)
	return &Message{Type: MessageType(m._type), Src: cstring(m.src), Text: cstring(m.text), Debug: cstring(m.debug)}
}

func (p *pipeline) SetLatency(d time.Duration) {
	if el, done := p.use(); el != nil {
		C.eg_set_latency(el, C.GstClockTime(d))
		done()
	}
}

func (p *pipeline) Latency() (time.Duration, bool) {
	el, done := p.use()
	defer done()
	if el == nil {
		return 0, false
	}
	var min C.GstClockTime
	if C.eg_query_latency(el, &min) == 0 {
		return 0, false
	}
	return time.Duration(min), true
}

func (p *pipeline) Close() {
	p.mu.Lock()
	el := p.p
	p.p = nil
	elems := p.elems
	p.elems = nil
	if el != nil {
		// Under the lock: no call can be using it, and none starts until it is NULL and released
		C.eg_set_state(el, C.int(StateNull))
		for _, e := range elems {
			C.gst_object_unref(C.gpointer(e.p))
		}
		C.gst_object_unref(C.gpointer(el))
	}
	p.mu.Unlock()
}

func cstring(s *C.char) string {
	if s == nil {
		return ""
	}
	return C.GoString(s)
}

// use is the element while its pipeline is open, under the pipeline's read lock, or nil.
func (e *element) use() (*C.GstElement, func()) {
	if pl, done := e.pl.use(); pl != nil {
		return e.p, done
	} else {
		return nil, done
	}
}

func (e *element) Name() string {
	el, done := e.use()
	defer done()
	if el == nil {
		return ""
	}
	return C.GoString(C.eg_name(el))
}

func (e *element) Set(prop, value string) bool {
	el, done := e.use()
	defer done()
	if el == nil {
		return false
	}
	cp, cv := C.CString(prop), C.CString(value)
	defer C.free(unsafe.Pointer(cp))
	defer C.free(unsafe.Pointer(cv))
	return C.eg_set_arg(el, cp, cv) != 0
}

func (e *element) SetPad(pad, prop, value string) bool {
	el, done := e.use()
	defer done()
	if el == nil {
		return false
	}
	cpad, cp, cv := C.CString(pad), C.CString(prop), C.CString(value)
	defer C.free(unsafe.Pointer(cpad))
	defer C.free(unsafe.Pointer(cp))
	defer C.free(unsafe.Pointer(cv))
	return C.eg_set_pad_arg(el, cpad, cp, cv) != 0
}

func (e *element) SetPadOffset(pad string, d time.Duration) bool {
	el, done := e.use()
	defer done()
	if el == nil {
		return false
	}
	cpad := C.CString(pad)
	defer C.free(unsafe.Pointer(cpad))
	return C.eg_set_pad_offset(el, cpad, C.gint64(d)) != 0
}

func (e *element) SetBool(prop string, v bool) {
	el, done := e.use()
	defer done()
	if el == nil {
		return
	}
	cp := C.CString(prop)
	defer C.free(unsafe.Pointer(cp))
	b := C.int(0)
	if v {
		b = 1
	}
	C.eg_set_bool(el, cp, b)
}

func (e *element) SetFloat(prop string, v float64) {
	el, done := e.use()
	defer done()
	if el == nil {
		return
	}
	cp := C.CString(prop)
	defer C.free(unsafe.Pointer(cp))
	C.eg_set_double(el, cp, C.double(v))
}

func (e *element) PullBytes(timeout time.Duration) []byte {
	el, done := e.use()
	if el == nil {
		done()
		time.Sleep(timeout)
		return nil
	}
	defer done()
	var data *C.guint8
	var n C.gsize
	if C.eg_pull_bytes(el, C.gint64(timeout), &data, &n) == 0 {
		return nil
	}
	defer C.g_free(C.gpointer(data))
	return C.GoBytes(unsafe.Pointer(data), C.int(n))
}

type buffer struct {
	p    *C.GstBuffer
	w, h int
}

func (e *element) PullBuffer(timeout time.Duration) Buffer {
	el, done := e.use()
	defer done()
	if el == nil {
		return nil
	}
	var w, h C.int
	b := C.eg_pull_buffer(el, C.gint64(timeout), &w, &h)
	if b == nil {
		return nil
	}
	return &buffer{p: b, w: int(w), h: int(h)}
}

func (e *element) CapsString(pad string) string {
	el, done := e.use()
	defer done()
	if el == nil {
		return ""
	}
	cs := C.CString(pad)
	defer C.free(unsafe.Pointer(cs))
	s := C.eg_caps_string(el, cs)
	if s == nil {
		return ""
	}
	defer C.g_free(C.gpointer(s))
	return C.GoString(s)
}

func (e *element) SelectStream(media string) bool {
	el, done := e.use()
	defer done()
	if el == nil {
		return false
	}
	cs := C.CString(media)
	defer C.free(unsafe.Pointer(cs))
	return C.eg_select_stream(el, cs) != 0
}

func (b *buffer) Size() (int, int) { return b.w, b.h }

func (b *buffer) Unref() {
	if b != nil && b.p != nil {
		C.eg_buffer_unref(b.p)
		b.p = nil
	}
}

func raw(e Element) *C.GstElement {
	if e == nil {
		return nil
	}
	return e.(*element).p
}

// --- bridges and counters ---

type bridge struct{ p *C.EgBridge }

func (Real) NewBridge(kind BridgeKind, pgm Pipeline, dst Element) Bridge {
	return &bridge{C.eg_bridge_new(C.int(kind), pgm.(*pipeline).p, raw(dst))}
}

func (b *bridge) SetParams(p BridgeParams) {
	rc := C.int(0)
	if p.RawClock {
		rc = 1
	}
	C.eg_bridge_params(b.p, C.gint64(p.Sync), C.gint64(p.Extra), C.gint64(p.Early), rc, C.gint64(p.JudgeLimit))
}

func (b *bridge) SetScreen(dst Element, gap time.Duration) {
	C.eg_bridge_mv(b.p, raw(dst), C.gint64(gap))
}

func (b *bridge) SetThumb(dst Element, every time.Duration) {
	C.eg_bridge_thumb(b.p, raw(dst), C.gint64(every))
}

func (b *bridge) SetDst(dst Element) { C.eg_bridge_dst(b.p, raw(dst)) }

func (b *bridge) SetEvery(n int) { C.eg_bridge_every(b.p, C.int(n)) }

func (b *bridge) Attach(appsink Element) { C.eg_bridge_attach(b.p, raw(appsink)) }

func (b *bridge) HoldIf(appsink Element, hold bool) {
	h := C.int(0)
	if hold {
		h = 1
	}
	C.eg_bridge_hold_if(b.p, raw(appsink), h)
}

func (b *bridge) Detach() { C.eg_bridge_detach(b.p) }

func (b *bridge) DetachIf(appsink Element) { C.eg_bridge_detach_if(b.p, raw(appsink)) }

func (b *bridge) PushStill(s Buffer) {
	if s == nil {
		return
	}
	if bb := s.(*buffer); bb.p != nil {
		C.eg_bridge_push_still(b.p, bb.p)
	}
}

func (b *bridge) Stats(takeTiming, takeLag bool) BridgeStats {
	var st C.EgBridgeStats
	take := C.int(0)
	if takeTiming {
		take |= 1
	}
	if takeLag {
		take |= 2
	}
	C.eg_bridge_stats(b.p, &st, take)
	r := BridgeStats{
		Frames:    uint64(st.frames),
		LastFrame: time.Duration(st.last_frame_ns),
		N:         uint64(st.n),
		MaxGap:    time.Duration(st.max_gap_ns),
		Judge:     int(st.judge),
		JudgeMin:  time.Duration(st.judge_min_ns),
		Lag:       -1,
	}
	if st.n > 0 {
		r.AgeMin = time.Duration(st.age_min_ns)
		r.AgeMax = time.Duration(st.age_max_ns)
		r.AgeAvg = time.Duration(int64(st.age_sum_ns) / int64(st.n))
	}
	if int64(st.lag_ns) != int64(^uint64(0)>>1) {
		r.Lag = time.Duration(st.lag_ns)
	}
	return r
}

func (b *bridge) Free() {
	if b.p != nil {
		C.eg_bridge_free(b.p)
		b.p = nil
	}
}

type cadence struct{ p *C.EgCadence }

func (Real) NewCadence(appsink Element) Cadence { return &cadence{C.eg_cadence_new(raw(appsink))} }

func (c *cadence) Take() (uint64, uint64) {
	var a, f C.guint64
	C.eg_cadence_take(c.p, &a, &f)
	return uint64(a), uint64(f)
}

type animator struct {
	p    *C.EgAnim
	pads int
}

func (Real) NewAnimator(comp Element, pads int, frame time.Duration) Animator {
	return &animator{C.eg_anim_new(raw(comp), C.int(pads), C.gint64(frame)), pads}
}

func (a *animator) Play(steps [][]Layer) {
	if len(steps) == 0 || a.pads == 0 {
		return
	}
	// EgLayer holds no pointers: Go memory can go to C, which copies it
	buf := make([]C.EgLayer, len(steps)*a.pads)
	for k, step := range steps {
		for i := range a.pads {
			var l Layer
			if i < len(step) {
				l = step[i]
			}
			buf[k*a.pads+i] = C.EgLayer{x: C.gint32(l.X), y: C.gint32(l.Y), w: C.gint32(l.W), h: C.gint32(l.H),
				z: C.gint32(l.Z), alpha: C.double(l.Alpha)}
		}
	}
	C.eg_anim_play(a.p, &buf[0], C.int(len(steps)))
}

func (a *animator) Busy() bool { return C.eg_anim_busy(a.p) != 0 }

func (a *animator) Free() {
	if a.p != nil {
		C.eg_anim_free(a.p)
		a.p = nil
	}
}

type counter struct{ p *C.EgCounter }

func (Real) NewCounter(el Element, pad string) Counter {
	cs := C.CString(pad)
	defer C.free(unsafe.Pointer(cs))
	c := C.eg_counter_new(raw(el), cs)
	if c == nil {
		return nil
	}
	return &counter{c}
}

func (c *counter) Free() {
	if c.p != nil {
		C.eg_counter_free(c.p)
		c.p = nil
	}
}

func (c *counter) Take() (uint64, uint64) {
	var n, b C.guint64
	C.eg_counter_take(c.p, &n, &b)
	return uint64(n), uint64(b)
}

type tap struct{ p *C.EgTap }

func (Real) NewTap(appsink Element) Tap { return &tap{C.eg_tap_new(raw(appsink))} }

func (t *tap) Connect(appsrc Element) { C.eg_tap_connect(t.p, raw(appsrc)) }

func (t *tap) TakeLate() time.Duration { return time.Duration(C.eg_tap_take_late(t.p)) }

func (t *tap) Free() {
	if t.p != nil {
		C.eg_tap_free(t.p)
		t.p = nil
	}
}

type feeder struct{ p *C.EgFeeder }

func (Real) NewFeeder(appsrc Element, size, buffers int) Feeder {
	return &feeder{C.eg_feeder_new(raw(appsrc), C.gsize(size), C.guint(buffers))}
}

func (f *feeder) Push(frame []byte) bool {
	if len(frame) == 0 {
		return false
	}
	return C.eg_feeder_push(f.p, (*C.guint8)(unsafe.Pointer(&frame[0])), C.gsize(len(frame))) != 0
}

func (f *feeder) Free() {
	if f.p != nil {
		C.eg_feeder_free(f.p)
		f.p = nil
	}
}

// judge and captureTime expose the bridge's per-frame logic to the tests (cgo is not allowed in them).
type judge struct{ j C.EgJudge }

func newJudge() *judge { return &judge{C.EgJudge{n: 0, min: C.gint64(^uint64(0) >> 1), ok: -1}} }

func (j *judge) add(limit, age time.Duration) bool {
	return C.eg_judge_step(&j.j, C.gint64(limit), C.gint64(age)) != 0
}

func captureTime(pts, base, early, rawOff time.Duration) time.Duration {
	return time.Duration(C.eg_capture_time(C.gint64(pts), C.gint64(base), C.gint64(early), C.gint64(rawOff)))
}

// abiCheck pins the hard-coded constants of types.go to the C headers (gst_test.go).
func abiCheck() []string {
	var bad []string
	check := func(name string, got, want int) {
		if got != want {
			bad = append(bad, fmt.Sprintf("%s = %d, C says %d", name, got, want))
		}
	}
	check("StateNull", int(StateNull), int(C.GST_STATE_NULL))
	check("StateReady", int(StateReady), int(C.GST_STATE_READY))
	check("StatePaused", int(StatePaused), int(C.GST_STATE_PAUSED))
	check("StatePlaying", int(StatePlaying), int(C.GST_STATE_PLAYING))
	check("MessageEOS", int(MessageEOS), int(C.GST_MESSAGE_EOS))
	check("MessageError", int(MessageError), int(C.GST_MESSAGE_ERROR))
	check("MessageWarning", int(MessageWarning), int(C.GST_MESSAGE_WARNING))
	check("MessageElement", int(MessageElement), int(C.GST_MESSAGE_ELEMENT))
	return bad
}
