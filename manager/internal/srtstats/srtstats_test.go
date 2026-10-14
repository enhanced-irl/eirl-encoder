package srtstats

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Real SRT 1.5.7 sample on the Jetson (2026-10-08)
const sample = `{"sid":209223506,"timepoint":"2026-10-08T00:23:58.880259+0200","time":8824,"window":{"flow":8157,"congestion":8192,"flight":1},"link":{"rtt":0.125,"bandwidth":510.384,"maxBandwidth":1000},"send":{"packets":154,"packetsUnique":154,"packetsLost":2,"packetsDropped":0,"packetsRetransmitted":2,"packetsFilterExtra":0,"bytes":209440,"bytesUnique":209440,"bytesDropped":0,"byteAvailBuf":12286500,"msBuf":1,"mbitRate":1.67,"sendPeriod":10},"recv":{"packets":0}}`

func TestParseAndAge(t *testing.T) {
	s := Parse([]byte(sample), time.Now())
	if s == nil || s.RTTMs != 0.125 || s.Packets != 154 || s.Lost != 2 || s.Retrans != 2 || s.SendMbps != 1.67 {
		t.Fatalf("%+v", s)
	}
	if s.LossPct < 1.29 || s.LossPct > 1.31 {
		t.Errorf("lossPct %v", s.LossPct)
	}
	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte(sample), 0o644)
	if Read(p, time.Now()) == nil {
		t.Error("fresh sample not read")
	}
	if Read(p, time.Now().Add(10*time.Second)) != nil {
		t.Error("stale sample must give nil (not connected)")
	}
	if Read(filepath.Join(t.TempDir(), "no"), time.Now()) != nil {
		t.Error("no file must give nil")
	}
}
