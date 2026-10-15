package rtmpsend

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// gop builds frames from a pattern: K keyframe, v video, a audio; one every 10 ms from start.
func gop(start int64, pattern string) []*frame {
	var out []*frame
	for i, c := range pattern {
		f := &frame{dts: start + int64(i)*900, data: []byte{byte(c)}}
		f.pts = f.dts
		switch c {
		case 'K':
			f.video, f.key = true, true
		case 'v':
			f.video = true
		}
		out = append(out, f)
	}
	return out
}

func pattern(q *queue) string {
	var b strings.Builder
	for _, f := range q.items {
		b.WriteByte(f.data[0])
	}
	return b.String()
}

func TestQueue(t *testing.T) {
	cases := []struct {
		name    string
		maxMs   int
		live    bool   // goLive before pushing
		pushes  string // frames pushed, 10 ms apart
		want    string // what stays queued
		dropped int64
		needKey bool
	}{
		{"waits for a keyframe", 1000, false, "vavaKvav", "Kvav", 0, false},
		{"preroll keeps the newest gop", 1000, false, "KvavKvaKva", "Kva", 0, false},
		{"live keeps everything under max", 1000, true, "KvavKva", "KvavKva", 0, false},
		{"live counts what precedes the first keyframe", 1000, true, "vavK", "K", 2, false},
		{"over max drops to the next keyframe", 50, true, "KvavvaKvav", "Kvav", 4, false},
		{"over max with no keyframe drops all and waits", 30, true, "Kvavvv", "", 5, true},
		{"after that, only from a keyframe", 30, true, "KvavvvavKv", "Kv", 6, false},
	}
	for _, c := range cases {
		q := newQueue(time.Duration(c.maxMs) * time.Millisecond)
		if c.live {
			q.goLive()
		}
		for _, f := range gop(0, c.pushes) {
			q.push(f)
		}
		if got := pattern(q); got != c.want || q.dropped != c.dropped || q.needKey != c.needKey {
			t.Errorf("%s: queued %q dropped %d needKey %v; want %q %d %v", c.name, got, q.dropped, q.needKey,
				c.want, c.dropped, c.needKey)
		}
	}
}

func TestQueueSnapshotAndDropMs(t *testing.T) {
	q := newQueue(50 * time.Millisecond)
	q.goLive()
	for _, f := range gop(0, "KvavvaKv") {
		q.push(f)
	}
	waiting, dropped, pushed, dropMs := q.snapshot()
	if waiting != 10*time.Millisecond || dropped != 4 || pushed != 8 || dropMs != 60 {
		t.Errorf("waiting %v dropped %d pushed %d dropMs %d", waiting, dropped, pushed, dropMs)
	}
}

func TestQueuePopAndEnd(t *testing.T) {
	q := newQueue(time.Second)
	for _, f := range gop(0, "Kva") {
		q.push(f)
	}
	q.goLive()
	q.finish(io.EOF)
	stop := make(chan struct{})
	var got string
	for {
		f, err := q.pop(stop)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("end: %v", err)
			}
			break
		}
		got += string(f.data)
	}
	if got != "Kva" {
		t.Errorf("popped %q", got)
	}

	// An empty queue waits; stop ends the wait.
	q2 := newQueue(time.Second)
	close(stop)
	if _, err := q2.pop(stop); !errors.Is(err, errStopped) {
		t.Errorf("stop: %v", err)
	}
	// A push wakes it.
	q3 := newQueue(time.Second)
	go func() {
		time.Sleep(20 * time.Millisecond)
		q3.push(gop(0, "K")[0])
	}()
	if f, err := q3.pop(make(chan struct{})); err != nil || !f.key {
		t.Errorf("wake: %v %v", f, err)
	}
}
