package record

import (
	"errors"
	"os"
	"sync"
	"time"
)

// Variables so that the tests can shorten them.
var (
	queueCap   int64 = 64 << 20        // RECORDING.md §5.4: the stalls of a pendrive reached 6.5 s (§3)
	splitAt    int64 = 3_900_000_000   // FAT32: a file is at most 4 GiB; one fragment of margin and more
	syncEvery        = time.Second     // fsync after a fragment, at most this often
	finishWait       = 5 * time.Second // how long a writer may take to empty its queue at the end
	// fsync is f.Sync; the tests write to pipes, which cannot be synced
	fsync = func(f *os.File) error { return f.Sync() }
)

// writer is a recording's file side: the session's puller offers it the muxer's boxes (never blocking: the
// program must not wait for a disk), and its own goroutine writes them, with an fsync after each fragment.
// When the queue is full it drops whole fragments (moof + mdat) and resumes at a video fragment that starts
// with a keyframe: the file stays valid, with a jump. On FAT32 it starts a new file at such a fragment once
// the current one passes splitAt, with the same header.
type writer struct {
	dir   string
	fat32 bool
	open  func(dir string) (*os.File, string, error) // the next file of the recording (create)

	mu       sync.Mutex
	cond     *sync.Cond
	queue    []box
	queued   int64
	closed   bool // no more boxes (finish)
	abandon  bool // the disk went: write nothing more
	dropping bool // dropping fragments until the queue drains below half its size
	dropMdat bool // the moof just dropped: its mdat goes too
	header   [][]byte
	video    uint32
	dropped  int // keyframe video fragments dropped (≈ seconds)
	err      error

	// Owned by the goroutine (read under mu through stats)
	f        *os.File
	name     string // the current file, relative to dir
	names    []string
	inFile   int64
	written  int64
	lastSync time.Time
	done     chan struct{}
}

func newWriter(dir string, fat32 bool, f *os.File, name string, open func(string) (*os.File, string, error)) *writer {
	w := &writer{dir: dir, fat32: fat32, open: open, f: f, name: name, names: []string{name}, done: make(chan struct{})}
	w.cond = sync.NewCond(&w.mu)
	go w.run()
	return w
}

// offer takes one box from the muxer. ftyp and moov are the header (kept for a split); mfra is left out:
// its offsets would not survive a drop or a split, and a fragmented file needs none. So is the moov mp4mux
// sends again at the end, meant to go over the first one: appended, the file would have two.
func (w *writer) offer(b box) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.abandon || w.err != nil {
		return
	}
	switch b.typ {
	case "mfra":
		return
	case "ftyp", "moov":
		if len(w.header) == 2 { // ftyp and moov already
			return
		}
		w.header = append(w.header, b.data)
		if b.typ == "moov" {
			w.video = videoTrack(b.data)
		}
	case "moof":
		key := w.video != 0 && fragmentTrack(b.data) == w.video && fragmentKey(b.data)
		switch {
		case w.dropping && key && w.queued < queueCap/2:
			w.dropping = false
		case w.dropping, w.queued+int64(len(b.data)) > queueCap:
			w.dropping, w.dropMdat = true, true
			if key {
				w.dropped++
			}
			return
		}
	case "mdat":
		if w.dropMdat {
			w.dropMdat = false
			return
		}
	default:
		if w.dropping {
			return
		}
	}
	w.queue = append(w.queue, b)
	w.queued += int64(len(b.data))
	w.cond.Signal()
}

func (w *writer) run() {
	defer close(w.done)
	for {
		w.mu.Lock()
		for len(w.queue) == 0 && !w.closed && !w.abandon {
			w.cond.Wait()
		}
		if w.abandon || (len(w.queue) == 0 && w.closed) {
			w.mu.Unlock()
			break
		}
		b := w.queue[0]
		w.queue[0] = box{}
		w.queue = w.queue[1:]
		w.queued -= int64(len(b.data))
		header := w.header
		video := w.video
		w.mu.Unlock()
		if err := w.write(b, header, video); err != nil {
			w.mu.Lock()
			w.err = err
			w.queue, w.queued = nil, 0
			w.mu.Unlock()
			break
		}
	}
	if w.f != nil {
		w.mu.Lock()
		abandon := w.abandon
		w.mu.Unlock()
		var err error
		if !abandon { // a disk that went cannot take an fsync: the file is cut when it is back
			err = fsync(w.f)
		}
		if cerr := w.f.Close(); err == nil {
			err = cerr
		}
		w.f = nil
		w.mu.Lock()
		if w.err == nil && !w.abandon {
			w.err = err
		}
		w.mu.Unlock()
	}
}

// write puts one box in the file, after a split when it is due, and fsyncs after a fragment's data.
func (w *writer) write(b box, header [][]byte, video uint32) error {
	if w.fat32 && b.typ == "moof" && w.inFile >= splitAt && video != 0 && fragmentTrack(b.data) == video &&
		fragmentKey(b.data) {
		if err := w.split(header); err != nil {
			return err
		}
	}
	if err := w.put(b.data); err != nil {
		return err
	}
	if b.typ == "mdat" && time.Since(w.lastSync) >= syncEvery {
		if err := fsync(w.f); err != nil {
			return err
		}
		w.lastSync = time.Now()
	}
	return nil
}

func (w *writer) put(p []byte) error {
	n, err := w.f.Write(p)
	w.inFile += int64(n)
	w.mu.Lock()
	w.written += int64(n)
	w.mu.Unlock()
	return err
}

// split closes the current file and opens the next one with the recording's header.
func (w *writer) split(header [][]byte) error {
	if err := fsync(w.f); err != nil {
		return err
	}
	if err := w.f.Close(); err != nil {
		return err
	}
	w.f = nil
	f, name, err := w.open(w.dir)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.f, w.name, w.inFile = f, name, 0
	w.names = append(w.names, name)
	w.mu.Unlock()
	for _, h := range header {
		if err := w.put(h); err != nil {
			return err
		}
	}
	return nil
}

// finish: no more boxes; the queue is written, the file synced and closed. It waits at most finishWait (a
// disk that hangs in a write keeps the goroutine; the file is then left as a power cut would leave it).
func (w *writer) finish() error {
	w.mu.Lock()
	w.closed = true
	w.cond.Broadcast()
	w.mu.Unlock()
	select {
	case <-w.done:
	case <-time.After(finishWait):
		return errFinishStuck
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// drop: the disk went; nothing more is written. It waits for the goroutine at most finishWait (one stuck in
// a write to a dead disk is left behind).
func (w *writer) drop() {
	w.mu.Lock()
	w.abandon = true
	w.queue, w.queued = nil, 0
	w.cond.Broadcast()
	w.mu.Unlock()
	select {
	case <-w.done:
	case <-time.After(finishWait):
	}
}

var errFinishStuck = errors.New("the disk did not take the end of the recording")

// writerStats is what the session reports.
type writerStats struct {
	written int64
	queued  int64
	dropped int
	name    string
	err     error
}

func (w *writer) stats() writerStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return writerStats{written: w.written, queued: w.queued, dropped: w.dropped, name: w.name, err: w.err}
}
