package rtmpsend

import (
	"errors"
	"sync"
	"time"
)

// frame is one access unit read from the TS: a video access unit (AVCC, 4-byte lengths) or an AAC frame.
type frame struct {
	video bool
	key   bool     // a video keyframe whose parameter sets are known
	dts   int64    // 90 kHz, unwrapped
	pts   int64    // 90 kHz, unwrapped
	data  []byte   // AVCC access unit or raw AAC frame
	ps    [][]byte // keyframes: the parameter sets in force (VPS, SPS, PPS or SPS, PPS)
	info  *streamInfo
}

// errStopped: pop gave up because the session ended.
var errStopped = errors.New("stopped")

// queue holds the media between the TS reader and the network writer. Until the writer goes live it keeps
// only the newest GOP, so the first frame sent is a keyframe. Live, when the media waiting goes over max
// it drops up to the next keyframe (video and the audio of that span), as OBS does, and counts the video
// frames dropped.
type queue struct {
	mu      sync.Mutex
	items   []*frame
	max     int64 // 90 kHz
	newest  int64 // the highest dts pushed
	live    bool
	needKey bool // drop everything until the next keyframe

	dropped int64 // video frames dropped while live, in total
	dropMs  int64 // the queue that caused the last drop, ms
	pushed  int64 // bytes pushed, in total (the input rate)

	notify chan struct{}
	done   chan struct{} // closed by finish
	end    error         // the reader's end: io.EOF or its error
	endAt  time.Time
}

func newQueue(max time.Duration) *queue {
	return &queue{max: max.Milliseconds() * 90, needKey: true, notify: make(chan struct{}, 1),
		done: make(chan struct{})}
}

// push appends a frame, dropping what the policy says.
func (q *queue) push(f *frame) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pushed += int64(len(f.data))
	if q.needKey {
		if !f.video || !f.key {
			if f.video && q.live {
				q.dropped++
			}
			return
		}
		q.needKey = false
	}
	if !q.live && f.video && f.key {
		clear(q.items)
		q.items = q.items[:0]
	}
	q.items = append(q.items, f)
	q.newest = max(q.newest, f.dts)
	for len(q.items) > 0 && q.durationLocked() > q.max {
		q.dropToKeyLocked()
	}
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// dropToKeyLocked drops the frames before the next keyframe after the first one, or all and waits for a
// keyframe if the queue has none.
func (q *queue) dropToKeyLocked() {
	q.dropMs = q.durationLocked() / 90
	k := len(q.items)
	for i := 1; i < len(q.items); i++ {
		if q.items[i].video && q.items[i].key {
			k = i
			break
		}
	}
	if q.live {
		for _, f := range q.items[:k] {
			if f.video {
				q.dropped++
			}
		}
	}
	if k == len(q.items) {
		q.needKey = true
	}
	n := copy(q.items, q.items[k:])
	clear(q.items[n:])
	q.items = q.items[:n]
}

func (q *queue) durationLocked() int64 {
	if len(q.items) == 0 {
		return 0
	}
	return max(q.newest-q.items[0].dts, 0)
}

// pop takes the oldest frame, waiting for one; the reader's end once empty, errStopped if stop closes.
func (q *queue) pop(stop <-chan struct{}) (*frame, error) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			f := q.items[0]
			q.items[0] = nil
			q.items = q.items[1:]
			q.mu.Unlock()
			return f, nil
		}
		end := q.end
		q.mu.Unlock()
		if end != nil {
			return nil, end
		}
		select {
		case <-q.notify:
		case <-q.done:
		case <-stop:
			return nil, errStopped
		}
	}
}

// goLive: the writer starts sending; from now on drops are counted.
func (q *queue) goLive() {
	q.mu.Lock()
	q.live = true
	q.mu.Unlock()
}

// finish records the reader's end (io.EOF when stdin closed).
func (q *queue) finish(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.end == nil {
		q.end, q.endAt = err, time.Now()
		close(q.done)
	}
}

// ended is since when the reader ended and how (nil while it reads).
func (q *queue) ended() (time.Time, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.endAt, q.end
}

// snapshot is what the stats read: the media waiting, the video frames dropped and the bytes pushed (both
// in total), and the queue that caused the last drop.
func (q *queue) snapshot() (waiting time.Duration, dropped, pushed, dropMs int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return time.Duration(q.durationLocked()) * time.Second / 90000, q.dropped, q.pushed, q.dropMs
}
