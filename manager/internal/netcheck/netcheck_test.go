package netcheck

import (
	"testing"
	"time"
)

func TestRecord(t *testing.T) {
	var s State
	t0 := time.Unix(0, 0)
	at := func(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }
	steps := []struct {
		sec  int
		ok   bool
		off  bool
		note string
	}{
		{0, true, false, "the first good check is accepted at once"},
		{30, false, false, "one bad check is not enough"},
		{60, false, true, "two in a row, 60 s after the last change"},
		{90, true, true, "one good check is not enough"},
	}
	for _, st := range steps {
		s.Record(st.ok, at(st.sec), 2, 60*time.Second)
		if s.Off() != st.off {
			t.Fatalf("t=%d: %s (off=%v)", st.sec, st.note, s.Off())
		}
	}
	s.Record(true, at(100), 2, 60*time.Second)
	if !s.Off() {
		t.Fatal("t=100: the dwell (60 s since the change at 60) holds it")
	}
	s.Record(true, at(130), 2, 60*time.Second)
	if s.Off() {
		t.Fatal("t=130: three good ones and 70 s: back")
	}
	var first State
	first.Record(false, at(0), 2, time.Hour)
	first.Record(false, at(1), 2, time.Hour)
	if !first.Off() {
		t.Fatal("never known: two bad checks are enough, no dwell")
	}
}
