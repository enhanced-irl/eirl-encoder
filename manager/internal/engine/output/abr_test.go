package output

import (
	"strings"
	"testing"
)

// ControllerTest of scripts/test_engine_run.py. Its VideoMeterTest is not ported: it tests the legacy
// --video-stats of the single-source path (MIXER=0, gone with E5); the engine core writes `video`.

type smp struct {
	rtt, buf float64
	dropped  int
}

func repeat(s smp, n int) []smp {
	out := make([]smp, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// feed gives samples every `every` s from t0: the final t and the bitrate after each sample.
func feed(c *controller, samples []smp, t0, every float64) (float64, []int) {
	t, out := t0, []int{}
	for _, s := range samples {
		c.update(t, s.rtt, s.buf, s.dropped)
		out = append(out, c.kbps)
		t += every
	}
	return t, out
}

func set(xs []int) map[int]bool {
	m := map[int]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

var stable = smp{40, 45, 0}

func TestStableStaysAtMax(t *testing.T) {
	c := newController(1000, 6000, 2000)
	_, out := feed(c, repeat(stable, 100), 0, 0.2)
	if s := set(out); len(s) != 1 || !s[6000] {
		t.Errorf("bitrates %v", s)
	}
}

func TestCongestionLowersGradually(t *testing.T) {
	c := newController(1000, 6000, 2000)
	tt, _ := feed(c, repeat(stable, 10), 0, 0.2)
	// buffer 400 ms above the base RTT: congestion, not severe (< 1000 ms = half the latency)
	_, out := feed(c, repeat(smp{60, 440, 0}, 6), tt, 0.2)
	if out[0] != 5100 { // ×0.85
		t.Errorf("first %d", out[0])
	}
	if last := out[len(out)-1]; last >= 5100 || last <= 3000 { // keeps lowering, at most every 0.5 s
		t.Errorf("last %d", last)
	}
}

func TestDropsAreSevere(t *testing.T) {
	c := newController(1000, 6000, 2000)
	tt, _ := feed(c, repeat(stable, 10), 0, 0.2)
	c.update(tt, 45, 50, 3) // SRT already drops packets
	if c.kbps != 3600 {     // ×0.6
		t.Errorf("kbps %d", c.kbps)
	}
	if c.lastReason.Kind != "severe" { // Python: last_reason starts with «grave»
		t.Errorf("reason %+v", c.lastReason)
	}
}

func TestNeverBelowMin(t *testing.T) {
	c := newController(1000, 6000, 2000)
	feed(c, repeat(smp{900, 1500, 10}, 200), 0, 0.2)
	if c.kbps != 1000 {
		t.Errorf("kbps %d", c.kbps)
	}
}

func TestRecoversSlowlyAfterHold(t *testing.T) {
	c := newController(1000, 6000, 2000)
	tt, _ := feed(c, repeat(stable, 10), 0, 0.2)
	c.update(tt, 45, 50, 5) // severe → 3600 and 5 s without going up
	low := c.kbps
	tt, out := feed(c, repeat(stable, 20), tt+0.2, 0.2) // 4 s stable: still holding
	if s := set(out); len(s) != 1 || !s[low] {
		t.Errorf("during the hold %v", s)
	}
	tt, out = feed(c, repeat(stable, 10), tt, 0.2) // past the hold: +300 kbps/s
	if last := out[len(out)-1]; last <= low || last > low+2*300 {
		t.Errorf("after the hold %d (low %d)", last, low)
	}
	_, out = feed(c, repeat(stable, 100), tt, 0.2)
	if out[len(out)-1] != 6000 {
		t.Errorf("end %d", out[len(out)-1])
	}
}

// With another split between uplinks the base RTT goes up: past the window, it stops counting as congestion
func TestBaseRTTFollowsLinkChange(t *testing.T) {
	c := newController(1000, 6000, 2000)
	tt, _ := feed(c, repeat(stable, 10), 0, 0.2)
	feed(c, repeat(smp{120, 125, 0}, int(40/0.2)), tt, 0.2)
	if c.kbps != 6000 {
		t.Errorf("kbps %d", c.kbps)
	}
}

func TestRoundingAndBounds(t *testing.T) {
	c := newController(800, 4250, 500)
	c.update(0, 10, 10, 1)
	if c.kbps%50 != 0 || c.kbps < 800 {
		t.Errorf("kbps %d", c.kbps)
	}
}

func TestMaxNotMultipleOfStepIsReachable(t *testing.T) {
	c := newController(1000, 6025, 2000)
	_, out := feed(c, repeat(stable, 20), 0, 0.2)
	if s := set(out); len(s) != 1 || !s[6025] { // stable: it does not go down by the rounding
		t.Errorf("stable %v", s)
	}
	tt, _ := feed(c, []smp{{60, 440, 0}}, 0, 0.2) // congestion → 5100 (multiple of 50)
	if c.kbps%50 != 0 {
		t.Errorf("kbps %d", c.kbps)
	}
	_, out = feed(c, repeat(stable, 200), tt+0.2, 0.2)
	if out[len(out)-1] != 6025 { // and back to the exact maximum
		t.Errorf("end %d", out[len(out)-1])
	}
}

func TestParseSample(t *testing.T) {
	s, ok := parseSample(`{"link":{"rtt":12.5},"send":{"msBuf":30,"packetsDropped":2,"mbitRate":4.1}}`)
	if !ok || s.rtt != 12.5 || s.bufMs != 30 || s.dropped != 2 || s.hasBytes {
		t.Errorf("sample %+v %v", s, ok)
	}
	for _, bad := range []string{"no es json", `{"link":{}}`, `{"link":{"rtt":"x"},"send":{"msBuf":1,"packetsDropped":0}}`} {
		if _, ok := parseSample(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	// Python's float() also takes a number in a string
	if s, ok := parseSample(`{"link":{"rtt":"7"},"send":{"msBuf":1,"packetsDropped":0,"bytes":1316}}` + "\n"); !ok || s.rtt != 7 || s.bytes != 1316 {
		t.Errorf("sample %+v %v", s, ok)
	}
}

// The reason the controller keeps is what the abr_down line says
func TestControllerReasonFields(t *testing.T) {
	c := newController(1000, 6000, 2000)
	c.update(0, 60, 440, 0)
	r := c.lastReason
	if r.Kind != "congestion" || r.BufMs != 440 || r.RTTMs != 60 || !strings.Contains("severe congestion stable", r.Kind) {
		t.Errorf("reason %+v", r)
	}
}
