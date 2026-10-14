// Package audiolevel is the VU meter: the engine sends the mixer's PCM (S16LE 48 kHz stereo,
// what goes on air) over UDP to 127.0.0.1:5005. The socket is only open while someone is watching;
// otherwise the kernel drops the datagrams and the app does not wake up ~50 times a second for nothing.
package audiolevel

import (
	"context"
	"encoding/binary"
	"math"
	"net"
	"sync"
	"time"
)

// Level: peak and RMS per channel (left, right) in dBFS, every 100 ms. Silence = Floor.
type Level struct {
	PeakDb [2]float64 `json:"peakDb"`
	RmsDb  [2]float64 `json:"rmsDb"`
}

const Floor = -90.0

func dbfs(v float64) float64 {
	if v <= 0 {
		return Floor
	}
	return math.Max(Floor, math.Round(20*math.Log10(v)*10)/10)
}

// Accumulator aggregates interleaved 2-channel S16LE samples.
type Accumulator struct {
	peak [2]float64
	sq   [2]float64
	n    int
}

func (a *Accumulator) Add(pcm []byte) {
	for i := 0; i+3 < len(pcm); i += 4 {
		for c := 0; c < 2; c++ {
			v := math.Abs(float64(int16(binary.LittleEndian.Uint16(pcm[i+2*c:])))) / 32768
			a.peak[c] = math.Max(a.peak[c], v)
			a.sq[c] += v * v
		}
		a.n++
	}
}

func (a *Accumulator) Flush() Level {
	var l Level
	for c := 0; c < 2; c++ {
		l.PeakDb[c] = dbfs(a.peak[c])
		if a.n > 0 {
			l.RmsDb[c] = dbfs(math.Sqrt(a.sq[c] / float64(a.n)))
		} else {
			l.RmsDb[c] = Floor
		}
	}
	*a = Accumulator{}
	return l
}

// Meter opens the socket with the first subscriber and closes it with the last one.
type Meter struct {
	Addr string // 127.0.0.1:5005

	mu     sync.Mutex
	subs   map[chan Level]struct{}
	cancel context.CancelFunc
}

func (m *Meter) Subscribe() (<-chan Level, func()) {
	ch := make(chan Level, 4)
	m.mu.Lock()
	if m.subs == nil {
		m.subs = map[chan Level]struct{}{}
	}
	m.subs[ch] = struct{}{}
	if len(m.subs) == 1 {
		ctx, cancel := context.WithCancel(context.Background())
		m.cancel = cancel
		go m.run(ctx)
	}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs, ch)
		if len(m.subs) == 0 && m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.mu.Unlock()
	}
}

func (m *Meter) run(ctx context.Context) {
	// Retries: if someone leaves and another arrives right away, the previous socket is closing in parallel
	var conn net.PacketConn
	var err error
	for i := 0; i < 10; i++ {
		if conn, err = net.ListenPacket("udp", m.Addr); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		return // another process has the port: no VU meter
	}
	go func() { <-ctx.Done(); conn.Close() }()
	var acc Accumulator
	buf := make([]byte, 65536)
	next := time.Now().Add(100 * time.Millisecond)
	for {
		conn.SetReadDeadline(next)
		n, _, err := conn.ReadFrom(buf)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			acc.Add(buf[:n])
		}
		if now := time.Now(); !now.Before(next) {
			l := acc.Flush()
			m.mu.Lock()
			for ch := range m.subs {
				select {
				case ch <- l:
				default: // slow client: one level is skipped
				}
			}
			m.mu.Unlock()
			next = now.Add(100 * time.Millisecond)
		}
	}
}
