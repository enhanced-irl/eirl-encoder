package rtmpsend

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/srtstats"
)

func TestFormatStats(t *testing.T) {
	line := formatStats(23.4567, 187500, 412, 3, 250*time.Millisecond)
	want := `{"link":{"rtt":23.457},"send":{"bytes":187500,"msBuf":412,"packetsDropped":3,"mbitRate":6}}`
	if string(line) != want {
		t.Fatalf("got  %s\nwant %s", line, want)
	}
	// The shape is srt-live-transmit's: the app's reader takes it as is.
	s := srtstats.Parse(line, time.Now())
	if s == nil || s.RTTMs != 23.457 || s.BufferMs != 412 || s.Dropped != 3 || s.SendMbps != 6 {
		t.Errorf("srtstats: %+v", s)
	}
	// And the engine's ABR reads link.rtt, send.msBuf, send.packetsDropped and send.bytes as numbers.
	var d map[string]map[string]any
	if err := json.Unmarshal(line, &d); err != nil {
		t.Fatal(err)
	}
	for _, k := range [][2]string{{"link", "rtt"}, {"send", "msBuf"}, {"send", "packetsDropped"}, {"send", "bytes"}} {
		if _, ok := d[k[0]][k[1]].(float64); !ok {
			t.Errorf("%s.%s missing or not a number", k[0], k[1])
		}
	}
	if string(formatStats(0, 0, 0, 0, 0)) != `{"link":{"rtt":0},"send":{"bytes":0,"msBuf":0,"packetsDropped":0,"mbitRate":0}}` {
		t.Errorf("zero line: %s", formatStats(0, 0, 0, 0, 0))
	}
}

func TestBufferMs(t *testing.T) {
	cases := []struct {
		queue  time.Duration
		unsent int
		rate   float64 // bytes/s
		want   int64
	}{
		{0, 0, 0, 0},
		{300 * time.Millisecond, 0, 750000, 300},
		{300 * time.Millisecond, 75000, 750000, 400}, // 75 kB at 6 Mbps = 100 ms
		{0, 75000, 0, 0},                             // no rate: the kernel's part is unknown
		{2 * time.Second, 100 << 20, 1000, maxBufMs},
	}
	for _, c := range cases {
		if got := bufferMs(c.queue, c.unsent, c.rate); got != c.want {
			t.Errorf("bufferMs(%v, %d, %v) = %d, want %d", c.queue, c.unsent, c.rate, got, c.want)
		}
	}
}

func TestRateMeter(t *testing.T) {
	var m rateMeter
	if r := m.add(250000, 250*time.Millisecond); r != 1e6 {
		t.Errorf("first sample: %v", r)
	}
	if r := m.add(0, 250*time.Millisecond); r != 750000 {
		t.Errorf("smoothed: %v", r)
	}
	if r := m.add(100, 0); r != 750000 {
		t.Errorf("no interval: %v", r)
	}
}
