package audiolevel

import (
	"encoding/binary"
	"math"
	"net"
	"testing"
	"time"
)

func pcm(l, r int16, frames int) []byte {
	b := make([]byte, frames*4)
	for i := 0; i < frames; i++ {
		binary.LittleEndian.PutUint16(b[i*4:], uint16(l))
		binary.LittleEndian.PutUint16(b[i*4+2:], uint16(r))
	}
	return b
}

func TestAccumulator(t *testing.T) {
	var a Accumulator
	a.Add(pcm(16384, 0, 480)) // left at half scale (−6 dBFS), right silent
	l := a.Flush()
	if math.Abs(l.PeakDb[0]+6.0) > 0.1 || math.Abs(l.RmsDb[0]+6.0) > 0.1 {
		t.Errorf("left %+v", l)
	}
	if l.PeakDb[1] != Floor || l.RmsDb[1] != Floor {
		t.Errorf("right should be silence: %+v", l)
	}
	if l2 := a.Flush(); l2.RmsDb[0] != Floor {
		t.Errorf("Flush must reset: %+v", l2)
	}
}

func TestMeterOpensOnlyWithSubscribers(t *testing.T) {
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0") // a free port
	addr := pc.LocalAddr().String()
	pc.Close()
	m := &Meter{Addr: addr}
	ch, stop := m.Subscribe()
	time.Sleep(50 * time.Millisecond)
	c, _ := net.Dial("udp", addr)
	for i := 0; i < 5; i++ {
		c.Write(pcm(32767, 32767, 256))
	}
	select {
	case l := <-ch:
		if l.PeakDb[0] < -1 {
			t.Errorf("peak %+v", l)
		}
	case <-time.After(time.Second):
		t.Fatal("no levels")
	}
	stop()
	time.Sleep(50 * time.Millisecond)
	// with no subscribers the port is free again
	pc2, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("socket still open without subscribers: %v", err)
	}
	pc2.Close()
}
