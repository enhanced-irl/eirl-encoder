package live

import (
	"errors"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// After a script restart (new SSRC, sequence and timestamps) the output stays continuous.
func TestRewriterKeepsContinuityAcrossRestart(t *testing.T) {
	w := rewriter{step: 3000}
	at := time.Unix(1_800_000_000, 0)
	out := func(ssrc uint32, seq uint16, ts uint32) (uint16, uint32) {
		p := &rtp.Packet{Header: rtp.Header{SSRC: ssrc, SequenceNumber: seq, Timestamp: ts}}
		w.apply(p, at, 90000)
		return p.SequenceNumber, p.Timestamp
	}
	if s, ts := out(1, 100, 9000); s != 100 || ts != 9000 {
		t.Fatalf("the first one passes as is: %d %d", s, ts)
	}
	out(1, 101, 12000)
	if s, ts := out(2, 50000, 7); s != 102 || ts != 15000 {
		t.Fatalf("after the restart: %d %d, expected 102 15000", s, ts)
	}
	if s, ts := out(2, 50001, 3007); s != 103 || ts != 18000 {
		t.Fatalf("and continues: %d %d", s, ts)
	}
	// A 2 s restart: the timestamp advances those 2 s (180000), not one frame
	at = at.Add(2 * time.Second)
	if s, ts := out(3, 9, 77); s != 104 || ts != 18000+180000 {
		t.Fatalf("after 2 s stopped: %d %d, expected 104 198000", s, ts)
	}
	// The sequence wraps without jumps
	w = rewriter{step: 3000}
	out(3, 65535, 0)
	if s, _ := out(3, 0, 3000); s != 0 {
		t.Fatalf("sequence wrap: %d", s)
	}
}

func TestAnswerRejects(t *testing.T) {
	r, err := New("127.0.0.1:0", "127.0.0.1:0", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Answer("k", "not SDP", func() bool { return true }); !errors.Is(err, ErrBadOffer) {
		t.Fatalf("garbage offer: %v", err)
	}
	if r.Viewers() != 0 {
		t.Fatal("a rejected offer leaves no viewer")
	}
	r.MaxViewers = 0
	if _, err := r.Answer("k", "v=0\r\n", func() bool { return true }); !errors.Is(err, ErrBusy) {
		t.Fatalf("no slot: %v", err)
	}
}

// Sender Reports: both tracks with the same base, from the SOURCE timestamp (the pipeline), even if
// the audio arrives 100 ms later than the video and their timestamps wrap differently.
func TestReportBaseIsShared(t *testing.T) {
	r, err := New("127.0.0.1:0", "127.0.0.1:0", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	rt := 50000.0 // pipeline seconds: the video has already wrapped once (13.25 h), the audio not yet
	v := &rtp.Packet{Header: rtp.Header{SSRC: 1, Timestamp: uint32(uint64(rt*90000) % (1 << 32))}}
	a := &rtp.Packet{Header: rtp.Header{SSRC: 2, Timestamp: uint32(uint64(rt*48000) % (1 << 32))}}
	r.tracks[0].received(v, 100, now)
	r.tracks[1].received(a, 100, now.Add(100*time.Millisecond))
	r.report() // no viewers: it only computes (and corrects the audio wrap)
	vs, as := r.tracks[0].snap(), r.tracks[1].snap()
	base := vs.arrival.Add(-time.Duration(float64(vs.ext) / 90000 * 1e9))
	at := base.Add(time.Duration(float64(as.ext) / 48000 * 1e9))
	if d := at.Sub(now); d < -time.Millisecond || d > time.Millisecond {
		t.Fatalf("audio from the same pipeline instant must land at the same time as the video: %v", d)
	}
	// After aligning, normal progress keeps the relationship
	r.tracks[0].received(&rtp.Packet{Header: rtp.Header{SSRC: 1, Timestamp: v.Timestamp + 90000}}, 100, now.Add(time.Second))
	r.tracks[1].received(&rtp.Packet{Header: rtp.Header{SSRC: 2, Timestamp: a.Timestamp + 48000}}, 100, now.Add(1100*time.Millisecond))
	r.report()
	vs, as = r.tracks[0].snap(), r.tracks[1].snap()
	base = vs.arrival.Add(-time.Duration(float64(vs.ext) / 90000 * 1e9))
	if d := base.Add(time.Duration(float64(as.ext) / 48000 * 1e9)).Sub(now.Add(time.Second)); d < -time.Millisecond || d > time.Millisecond {
		t.Fatalf("1 s later: %v", d)
	}
}

// Script restart: the new video arrives before the new audio. No alignment (and no Sender Reports)
// until both tracks are on the new generation.
func TestReportWaitsForBothTracksAfterRestart(t *testing.T) {
	r, err := New("127.0.0.1:0", "127.0.0.1:0", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.tracks[0].received(&rtp.Packet{Header: rtp.Header{SSRC: 1, Timestamp: 600 * 90000}}, 100, now)
	r.tracks[1].received(&rtp.Packet{Header: rtp.Header{SSRC: 2, Timestamp: 600 * 48000}}, 100, now)
	r.report()
	if r.aligned != [2]int{1, 1} {
		t.Fatalf("first alignment: %v", r.aligned)
	}
	now = now.Add(5 * time.Second)
	r.tracks[0].received(&rtp.Packet{Header: rtp.Header{SSRC: 3, Timestamp: 0}}, 100, now)
	r.report()
	if r.aligned != [2]int{1, 1} {
		t.Fatalf("with only the new video it must not realign: %v", r.aligned)
	}
	r.tracks[1].received(&rtp.Packet{Header: rtp.Header{SSRC: 4, Timestamp: 0}}, 100, now.Add(50*time.Millisecond))
	r.report()
	if r.aligned != [2]int{2, 2} {
		t.Fatalf("with both new it does: %v", r.aligned)
	}
	vs, as := r.tracks[0].snap(), r.tracks[1].snap()
	base := vs.arrival.Add(-time.Duration(float64(vs.ext) / 90000 * 1e9))
	if d := base.Add(time.Duration(float64(as.ext) / 48000 * 1e9)).Sub(now); d < -time.Millisecond || d > time.Millisecond {
		t.Fatalf("after the restart, the same pipeline instant at the same time: %v", d)
	}
}

// A new offer from the same session replaces the previous one instead of taking another slot.
func TestSameSessionReplaces(t *testing.T) {
	r, err := New("127.0.0.1:0", "127.0.0.1:0", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	pc, _ := r.api.NewPeerConnection(webrtc.Configuration{})
	r.viewers[pc] = &viewer{key: "session"}
	if _, err := r.Answer("other", "v=0\r\n", func() bool { return true }); !errors.Is(err, ErrBusy) {
		t.Fatalf("other session, no slot: %v", err)
	}
	if _, err := r.Answer("session", "v=0\r\n", func() bool { return true }); errors.Is(err, ErrBusy) {
		t.Fatal("the same session must replace its own")
	}
	if r.has(pc) {
		t.Fatal("the previous one must have been closed")
	}
}
