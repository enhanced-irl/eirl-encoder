package output

import (
	"encoding/json"
	"math"
	"strconv"
)

// The adaptive bitrate (ABR=1): engine-run.py's Controller. With mobile uplinks (SRTLA) the bandwidth
// goes up and down minute by minute; with a fixed bitrate, when the network cannot carry it, SRT's send
// buffer grows up to the latency and SRT DROPS packets: cuts. The controller lowers the bitrate before
// that and raises it slowly when the network recovers.
const (
	abrDownEvery     = 0.5  // s between two decreases (lets the buffer react)
	abrUpEvery       = 1.0  // s between two increases
	abrHoldSevere    = 5.0  // s without increasing after a severe decrease
	abrHoldCongested = 3.0  // s without increasing after a decrease for congestion
	abrRTTWindow     = 10.0 // s of the base RTT: a change of split between uplinks stops penalising soon
	abrStep          = 50   // rounding: the encoder is not touched for less than this
)

// abrCause is why the controller last changed the bitrate.
type abrCause struct {
	Kind                 string // severe | congestion | stable
	BufMs, RTTMs, BaseMs float64
	Dropped              int
}

type rttSecond struct {
	sec int64
	rtt float64
}

// controller decides the video bitrate from SRT samples. No GStreamer: tested on its own.
//
// Signals (belacoder style, simplified):
//   - severe: SRT already drops, or the buffer is over half the latency, or the RTT shoots up → ×0.6
//   - congestion: the buffer grows above the base RTT, or the RTT goes up → ×0.85
//   - stable for a while: +max(100 kbps, 5 % of the maximum) per second up to the maximum
//
// The base RTT is the minimum of the last 10 s (with several uplinks it changes with the split).
// Not safe for concurrent use: one stats reader owns it.
type controller struct {
	min, max   int
	latency    int // ms
	kbps       int
	rtts       []rttSecond // (second, minimum of that second) in the window
	lastDown   float64
	lastUp     float64
	holdUntil  float64
	lastReason abrCause
}

func newController(minKbps, maxKbps, latencyMs int) *controller {
	return &controller{min: minKbps, max: maxKbps, latency: max(latencyMs, 20), kbps: maxKbps,
		lastDown: -1e9, lastUp: -1e9}
}

func (c *controller) baseRTT(now, rtt float64) float64 {
	sec := int64(now) // Python's int(): towards zero, like the monotonic clock's positive values
	if n := len(c.rtts); n > 0 && c.rtts[n-1].sec == sec {
		if rtt < c.rtts[n-1].rtt {
			c.rtts[n-1].rtt = rtt
		}
	} else {
		c.rtts = append(c.rtts, rttSecond{sec, rtt})
	}
	i := 0
	for i < len(c.rtts) && float64(c.rtts[i].sec) < float64(sec)-abrRTTWindow {
		i++
	}
	c.rtts = c.rtts[i:]
	base := c.rtts[0].rtt
	for _, r := range c.rtts[1:] {
		base = math.Min(base, r.rtt)
	}
	return base
}

// update takes one sample; it returns the new bitrate (kbps) and true if it changes.
func (c *controller) update(now, rtt, bufMs float64, dropped int) (int, bool) {
	base := c.baseRTT(now, rtt)
	lat := float64(c.latency)
	prev := c.kbps
	severe := dropped > 0 || bufMs > lat*0.5 || rtt > math.Max(lat*0.4, base+lat*0.3)
	congested := bufMs > base+math.Max(100, lat*0.1) || rtt > base*1.5+50
	switch {
	case severe || congested:
		if now-c.lastDown >= abrDownEvery && c.kbps > c.min {
			f, kind := 0.85, "congestion"
			if severe {
				f, kind = 0.6, "severe"
			}
			c.kbps = c.snap(float64(c.kbps) * f)
			c.lastDown = now
			c.lastReason = abrCause{Kind: kind, BufMs: bufMs, RTTMs: rtt, BaseMs: base, Dropped: dropped}
		}
		hold := abrHoldCongested
		if severe {
			hold = abrHoldSevere
		}
		c.holdUntil = math.Max(c.holdUntil, now+hold)
	case now >= c.holdUntil && now-c.lastUp >= abrUpEvery && c.kbps < c.max &&
		bufMs <= base+math.Max(50, lat*0.05):
		c.kbps = c.snap(float64(c.kbps + max(100, c.max/20)))
		c.lastUp = now
		c.lastReason = abrCause{Kind: "stable"}
	}
	return c.kbps, c.kbps != prev
}

// snap keeps the bitrate between the minimum and the maximum, and the values in between on multiples
// of abrStep. The ends go as they are: a VBITRATE of 6025 must be reachable.
func (c *controller) snap(kbps float64) int {
	if kbps >= float64(c.max) {
		return c.max
	}
	if kbps <= float64(c.min) {
		return c.min
	}
	return min(c.max, max(c.min, int(math.Floor(kbps/abrStep))*abrStep))
}

// statsLine is the part of a `-pf:json` line of srt-live-transmit the output reads.
type statsLine struct {
	Link struct {
		RTT json.RawMessage `json:"rtt"`
	} `json:"link"`
	Send struct {
		MsBuf          json.RawMessage `json:"msBuf"`
		PacketsDropped json.RawMessage `json:"packetsDropped"`
		Bytes          json.RawMessage `json:"bytes"`
	} `json:"send"`
}

// sample is one statistic of srt-live-transmit (counters of the LAST interval).
type sample struct {
	rtt, bufMs float64
	dropped    int
	bytes      int64
	hasBytes   bool
}

// parseSample reads link.rtt, send.msBuf and send.packetsDropped (and send.bytes, for the real bitrate)
// of a JSON line; false if one of the first three is missing or is not a number.
func parseSample(line string) (sample, bool) {
	var d statsLine
	if json.Unmarshal([]byte(line), &d) != nil {
		return sample{}, false
	}
	var s sample
	var ok1, ok2, ok3 bool
	s.rtt, ok1 = jsonFloat(d.Link.RTT)
	s.bufMs, ok2 = jsonFloat(d.Send.MsBuf)
	dropped, ok3 := jsonFloat(d.Send.PacketsDropped)
	if !ok1 || !ok2 || !ok3 {
		return sample{}, false
	}
	s.dropped = int(dropped) // Python's int(): towards zero
	if b, ok := jsonFloat(d.Send.Bytes); ok {
		s.bytes, s.hasBytes = int64(b), true
	}
	return s, true
}

// jsonFloat is Python's float() of a JSON value: a number, or a string holding one.
func jsonFloat(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}
