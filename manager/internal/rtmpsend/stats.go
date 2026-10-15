package rtmpsend

import (
	"encoding/json"
	"math"
	"time"
)

// maxBufMs bounds msBuf: past it, the number says nothing more (the queue has dropped long before).
const maxBufMs = 60000

// statsLine is one line of the stats fd: srt-live-transmit's shape, counters of the LAST interval
// (OUTPUTS.md §4). The engine's ABR reads link.rtt, send.msBuf and send.packetsDropped, and send.bytes.
type statsLine struct {
	Link struct {
		RTT float64 `json:"rtt"` // ms
	} `json:"link"`
	Send struct {
		Bytes          int64   `json:"bytes"`
		MsBuf          int64   `json:"msBuf"`
		PacketsDropped int64   `json:"packetsDropped"`
		MbitRate       float64 `json:"mbitRate"`
	} `json:"send"`
}

// formatStats is one stats line (without the newline).
func formatStats(rttMs float64, bytes, msBuf, dropped int64, interval time.Duration) []byte {
	var s statsLine
	s.Link.RTT = round3(rttMs)
	s.Send.Bytes, s.Send.MsBuf, s.Send.PacketsDropped = bytes, msBuf, dropped
	if interval > 0 {
		s.Send.MbitRate = round3(float64(bytes) * 8 / interval.Seconds() / 1e6)
	}
	b, _ := json.Marshal(s)
	return b
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

// bufferMs is the media waiting to leave: the app's queue plus the kernel's unsent bytes at rate (bytes
// per second; nothing is added without a rate).
func bufferMs(queue time.Duration, unsent int, rate float64) int64 {
	ms := float64(queue.Milliseconds())
	if unsent > 0 && rate > 0 {
		ms += float64(unsent) / rate * 1000
	}
	return int64(min(math.Round(ms), maxBufMs))
}

// sockInfo is what the kernel says of the connection (zero where it cannot say).
type sockInfo struct {
	rttMs  float64
	unsent int   // bytes written by the sender and not sent yet
	acked  int64 // bytes acknowledged by the server, in total
}

// rateMeter is a smoothed byte rate (bytes per second) from per-interval counts.
type rateMeter struct {
	rate float64
}

func (m *rateMeter) add(bytes int64, interval time.Duration) float64 {
	if interval <= 0 {
		return m.rate
	}
	r := float64(bytes) / interval.Seconds()
	if m.rate == 0 {
		m.rate = r
	} else {
		m.rate += 0.25 * (r - m.rate) // about one second at 250 ms
	}
	return m.rate
}
