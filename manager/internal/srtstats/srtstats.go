// Package srtstats reads the latest srt-live-transmit statistics sample that the script leaves in
// /run/eirl-engine/srt-stats.json (one JSON line, rewritten with rename ~once per second).
// It only exists while there is an SRT connection: no recent sample = not connected.
package srtstats

import (
	"encoding/json"
	"os"
	"time"
)

// Stats: counters of the LAST interval (srt-live-transmit without -f), not cumulative.
type Stats struct {
	Connected bool      `json:"connected"`
	RTTMs     float64   `json:"rttMs"`
	SendMbps  float64   `json:"sendMbps"`
	BufferMs  int       `json:"bufferMs"`
	Packets   int       `json:"pktSent"`
	Lost      int       `json:"pktLost"`
	Retrans   int       `json:"pktRetrans"`
	Dropped   int       `json:"pktDropped"`
	LossPct   float64   `json:"lossPct"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// MaxAge: older than this, the connection is gone (the script writes ~1/s).
const MaxAge = 5 * time.Second

type raw struct {
	Link struct {
		RTT float64 `json:"rtt"`
	} `json:"link"`
	Send struct {
		Packets  int     `json:"packets"`
		Lost     int     `json:"packetsLost"`
		Retrans  int     `json:"packetsRetransmitted"`
		Dropped  int     `json:"packetsDropped"`
		MbitRate float64 `json:"mbitRate"`
		MsBuf    int     `json:"msBuf"`
	} `json:"send"`
}

// Read returns nil if there is no recent sample.
func Read(path string, now time.Time) *Stats {
	fi, err := os.Stat(path)
	if err != nil || now.Sub(fi.ModTime()) > MaxAge {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return Parse(b, fi.ModTime())
}

func Parse(b []byte, at time.Time) *Stats {
	var r raw
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	s := &Stats{
		Connected: true, RTTMs: r.Link.RTT, SendMbps: r.Send.MbitRate, BufferMs: r.Send.MsBuf,
		Packets: r.Send.Packets, Lost: r.Send.Lost, Retrans: r.Send.Retrans, Dropped: r.Send.Dropped,
		UpdatedAt: at.UTC(),
	}
	if r.Send.Packets > 0 {
		s.LossPct = float64(r.Send.Lost) * 100 / float64(r.Send.Packets)
	}
	return s
}
