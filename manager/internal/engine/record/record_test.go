package record

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst/fake"
)

// --- a synthetic fragmented MP4, shaped like mp4mux's (RECORDING.md §3) ---

func mkbox(typ string, payload ...[]byte) []byte {
	n := 8
	for _, p := range payload {
		n += len(p)
	}
	b := make([]byte, 8, n)
	binary.BigEndian.PutUint32(b, uint32(n))
	copy(b[4:], typ)
	for _, p := range payload {
		b = append(b, p...)
	}
	return b
}

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

func trak(id uint32, handler string) []byte {
	tkhd := mkbox("tkhd", []byte{0, 0, 0, 3}, u32(0), u32(0), u32(id), make([]byte, 72))
	hdlr := mkbox("hdlr", u32(0), u32(0), []byte(handler), make([]byte, 13))
	return mkbox("trak", tkhd, mkbox("mdia", mkbox("mdhd", make([]byte, 24)), hdlr))
}

var (
	ftyp = mkbox("ftyp", []byte("iso6"), u32(0), []byte("iso6mp41"))
	moov = mkbox("moov", mkbox("mvhd", make([]byte, 100)), trak(1, "vide"), trak(2, "soun"))
)

// moof is a fragment the way mp4mux sends it: a default duration and flags (not a sync sample) in the tfhd,
// and the first sample's own flags (a sync sample) in the trun, with one sample size.
func moof(track uint32) []byte {
	tfhd := mkbox("tfhd", u32(0x28), u32(track), u32(100), u32(0x000100c0))
	return mkbox("moof", mkbox("mfhd", make([]byte, 8)), mkbox("traf", tfhd, mkbox("trun", u32(0x205), u32(1), u32(0), u32(0x40), u32(0))))
}

// moofTail is the video fragment mp4mux closes when the keyframe comes a frame or two late: no sync sample.
func moofTail() []byte {
	tfhd := mkbox("tfhd", u32(0x28), u32(1), u32(100), u32(0x000100c0))
	return mkbox("moof", mkbox("mfhd", make([]byte, 8)), mkbox("traf", tfhd, mkbox("trun", u32(0x201), u32(2), u32(0), u32(0), u32(0))))
}

func mdat(n int, fill byte) []byte { return mkbox("mdat", bytes.Repeat([]byte{fill}, n)) }

// fragments is n seconds: a video and an audio fragment each.
func fragments(n, size int) [][]byte {
	var out [][]byte
	for i := 0; i < n; i++ {
		out = append(out, moof(1), mdat(size, byte('v'+i)), moof(2), mdat(size/10, byte('a'+i)))
	}
	return out
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// topTypes lists the top-level boxes of a stream.
func topTypes(t *testing.T, b []byte) []string {
	t.Helper()
	var sp splitter
	boxes, err := sp.push(b)
	if err != nil || len(sp.buf) != 0 {
		t.Fatalf("not whole boxes: %v, %d left", err, len(sp.buf))
	}
	var out []string
	for _, x := range boxes {
		out = append(out, x.typ)
		if x.typ == "moof" {
			out[len(out)-1] += string(rune('0' + fragmentTrack(x.data)))
			if !fragmentKey(x.data) {
				out[len(out)-1] += "~"
			}
		}
	}
	return out
}

// --- the boxes ---

func TestSplitterAndTracks(t *testing.T) {
	stream := cat(append([][]byte{ftyp, moov}, fragments(2, 300)...)...)
	var sp splitter
	var got []box
	// in odd pieces, as a muxer's buffers may come
	for i := 0; i < len(stream); i += 37 {
		boxes, err := sp.push(stream[i:min(i+37, len(stream))])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, boxes...)
	}
	types := ""
	for _, b := range got {
		types += b.typ + " "
	}
	if types != "ftyp moov moof mdat moof mdat moof mdat moof mdat " || len(sp.buf) != 0 {
		t.Fatalf("boxes: %q, %d left", types, len(sp.buf))
	}
	if !bytes.Equal(cat(got[0].data, got[1].data), cat(ftyp, moov)) {
		t.Fatal("the header changed on the way")
	}
	if v := videoTrack(got[1].data); v != 1 {
		t.Fatalf("video track %d", v)
	}
	if a, b := fragmentTrack(got[2].data), fragmentTrack(got[4].data); a != 1 || b != 2 {
		t.Fatalf("fragment tracks %d %d", a, b)
	}
	if !fragmentKey(moof(1)) || fragmentKey(moofTail()) {
		t.Fatal("keyframe fragments: the trun's first-sample flags over the tfhd's default")
	}
	// Flags per sample: the first sample's, after its duration and size
	perSample := mkbox("moof", mkbox("traf", mkbox("tfhd", u32(0), u32(1)),
		mkbox("trun", u32(0x701), u32(1), u32(0), u32(100), u32(5000), u32(0x00010000))))
	if fragmentKey(perSample) {
		t.Fatal("a first sample that is not a sync sample, in the per-sample flags")
	}
	// A whole box in one buffer is handed over as it is (no copy)
	var one splitter
	whole := mdat(64, 'x')
	boxes, _ := one.push(whole)
	if len(boxes) != 1 || &boxes[0].data[0] == nil || one.buf != nil {
		t.Fatalf("whole box: %d boxes", len(boxes))
	}
	var bad splitter
	if _, err := bad.push([]byte{0, 0, 0, 4, 'm', 'd', 'a', 't'}); err == nil {
		t.Fatal("a size smaller than its header was accepted")
	}
}

func TestTrimAfterPowerCut(t *testing.T) {
	dir := t.TempDir()
	good := cat(append([][]byte{ftyp, moov}, fragments(2, 500)...)...)
	cut := moof(1)
	torn := cat(good, cut, mdat(400, 'z')[:200]) // a fragment cut in half
	zeros := cat(good, make([]byte, 4096))       // what ext4 leaves after a cut without fsync
	for name, b := range map[string][]byte{"torn.mp4": torn, "zeros.mp4": zeros, "whole.mp4": good} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		n, err := trimFile(p)
		after, _ := os.ReadFile(p)
		if err != nil || !bytes.Equal(after, good) || n != int64(len(b)-len(good)) {
			t.Errorf("%s: trimmed %d (%v), %d bytes left, want %d", name, n, err, len(after), len(good))
		}
	}
	// The marker's name on another disk (the same uuid): a link is not followed, a FIFO is not waited on
	outside := filepath.Join(t.TempDir(), "elsewhere")
	os.WriteFile(outside, torn, 0o644)
	link := filepath.Join(dir, "link.mp4")
	os.Symlink(outside, link)
	if _, err := trimFile(link); err == nil {
		t.Error("trimmed through a link")
	}
	if b, _ := os.ReadFile(outside); !bytes.Equal(b, torn) {
		t.Error("the file behind the link changed")
	}
	fifo := filepath.Join(dir, "fifo.mp4")
	if err := syscall.Mkfifo(fifo, 0o644); err == nil {
		done := make(chan error, 1)
		go func() { _, err := trimFile(fifo); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Error("trimmed a FIFO")
			}
		case <-time.After(2 * time.Second):
			t.Error("trimFile blocked on a FIFO")
		}
	}
}

// --- the writer ---

func setVar[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func noSync(t *testing.T) { setVar(t, &fsync, func(*os.File) error { return nil }) }

// A disk that stalls: the queue fills, whole fragments are dropped, and writing resumes at a video
// fragment; what reaches the disk is still whole boxes, moof always followed by its mdat.
func TestWriterDropsWholeFragments(t *testing.T) {
	noSync(t)
	setVar(t, &queueCap, 700<<10)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	w := newWriter(t.TempDir(), false, pw, "x.mp4", nil)
	w.offer(box{"ftyp", ftyp})
	w.offer(box{"moov", moov})
	// The first mdat (200 KB) does not fit in the pipe: the writer is stuck in it while more arrives
	for _, b := range fragments(12, 200<<10) {
		w.offer(box{string(b[4:8]), b})
	}
	if st := w.stats(); st.dropped == 0 {
		t.Fatalf("nothing dropped with %d queued", st.queued)
	}
	var out bytes.Buffer
	read := make(chan struct{})
	go func() { io.Copy(&out, pr); close(read) }()
	deadline := time.Now().Add(5 * time.Second)
	for w.stats().queued >= queueCap/2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Still dropping: an audio fragment and a late keyframe's frames go; the next keyframe resumes
	before := w.stats().dropped
	for _, b := range [][]byte{moof(2), mdat(100, 'q'), moofTail(), mdat(20, 't'), moof(1), mdat(300, 'r'), moof(2), mdat(30, 's')} {
		w.offer(box{string(b[4:8]), b})
	}
	if err := w.finish(); err != nil {
		t.Fatal(err)
	}
	<-read
	types := topTypes(t, out.Bytes())
	if types[0] != "ftyp" || types[1] != "moov" {
		t.Fatalf("no header: %v", types[:2])
	}
	for i := 2; i < len(types); i += 2 {
		if types[i][:4] != "moof" || types[i+1] != "mdat" {
			t.Fatalf("not moof+mdat pairs: %v", types)
		}
	}
	if last := types[len(types)-4:]; last[0] != "moof1" || last[2] != "moof2" || slices.Contains(types, "moof1~") {
		t.Fatalf("did not resume at the keyframe: %v", types)
	}
	if w.stats().dropped != before {
		t.Fatalf("audio or a keyframe's tail counted as dropped seconds: %d → %d", before, w.stats().dropped)
	}
}

// FAT32: past splitAt, a new file at the next video fragment, with the same header.
func TestWriterSplitsOnFAT32(t *testing.T) {
	noSync(t)
	setVar(t, &splitAt, 2000)
	dir := t.TempDir()
	f, name, err := create(dir, "2026-10-24_1530")
	if err != nil {
		t.Fatal(err)
	}
	w := newWriter(dir, true, f, name, func(d string) (*os.File, string, error) { return create(d, splitBase(name)) })
	w.offer(box{"ftyp", ftyp})
	w.offer(box{"moov", moov})
	for range 4 {
		// A late keyframe's frames in every second, past splitAt in the second one: not where a file starts
		for _, b := range [][]byte{moof(1), mdat(900, 'v'), moofTail(), mdat(30, 't'), moof(2), mdat(90, 'a')} {
			w.offer(box{string(b[4:8]), b})
		}
	}
	w.offer(box{"mfra", mkbox("mfra", make([]byte, 16))})
	w.offer(box{"moov", moov})
	if err := w.finish(); err != nil {
		t.Fatal(err)
	}
	if got := w.stats().name; got != "2026-10-24_1530-2.mp4" && got != "2026-10-24_1530-3.mp4" {
		t.Fatalf("last file %q", got)
	}
	for _, n := range []string{"2026-10-24_1530.mp4", "2026-10-24_1530-2.mp4"} {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		types := topTypes(t, b)
		if types[0] != "ftyp" || types[1] != "moov" || types[2] != "moof1" || types[len(types)-1] != "mdat" {
			t.Errorf("%s: %v", n, types)
		}
	}
}

// --- the recorder ---

type disk struct {
	mu sync.Mutex
	st Storage
}

func (d *disk) get() Storage   { d.mu.Lock(); defer d.mu.Unlock(); return d.st }
func (d *disk) set(st Storage) { d.mu.Lock(); d.st = st; d.mu.Unlock() }

type rig struct {
	t        *testing.T
	media    *fake.Media
	rec      *Recorder
	disk     *disk
	root     string
	dir      string
	stateDir string
	freeMu   sync.Mutex
	free     int64
}

func fast(t *testing.T) {
	noSync(t)
	setVar(t, &tick, 5*time.Millisecond)
	setVar(t, &idleTick, 10*time.Millisecond)
	setVar(t, &eosWait, 200*time.Millisecond)
	setVar(t, &freeEvery, time.Duration(0))
	setVar(t, &measureEvery, 50*time.Millisecond)
	setVar(t, &retryWaits, []time.Duration{20 * time.Millisecond})
}

func newRig(t *testing.T, mode string, prepare func(r *rig)) *rig {
	fast(t)
	root := t.TempDir()
	r := &rig{t: t, media: fake.New(), root: root, dir: filepath.Join(root, "EnhancedIRL"), stateDir: t.TempDir(), free: 1 << 40, disk: &disk{}}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.disk.set(Storage{State: StorageReady, Seq: 1, FS: "exfat", UUID: "U1", Dir: r.dir, SizeBytes: 64e9})
	if prepare != nil {
		prepare(r)
	}
	pgm, _ := r.media.Parse("program", "appsink name=recv appsink name=reca")
	cfg := &config.Config{RecMode: mode, RecQuality: "high", RecDir: root}
	rec, err := New(Options{Config: cfg, Media: r.media, Program: pgm, StateDir: r.stateDir, ReadStorage: r.disk.get,
		Desc: func(*config.Config) string {
			return "appsrc name=rv appsrc name=ra nvv4l2h264enc name=renc appsink name=rout"
		},
		Free:   func(string) (int64, error) { r.freeMu.Lock(); defer r.freeMu.Unlock(); return r.free, nil },
		Synced: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	r.rec = rec
	t.Cleanup(func() { rec.Close(2 * time.Second) })
	return r
}

func (r *rig) wait(what string, ok func(Info) bool) Info {
	r.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		i := r.rec.Info()
		if ok(i) {
			return i
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("waiting for %s: %+v", what, i)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// session is the newest record pipeline and its rout.
func (r *rig) session() (*fake.Pipeline, *fake.Element) {
	p := r.media.Find("record")
	if p == nil {
		r.t.Fatal("no record pipeline")
	}
	return p, p.El("rout")
}

func (r *rig) waitFile(name string, size int) {
	r.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if st, err := os.Stat(filepath.Join(r.dir, name)); err == nil && st.Size() == int64(size) {
			return
		}
		if time.Now().After(deadline) {
			st, _ := os.Stat(filepath.Join(r.dir, name))
			r.t.Fatalf("%s never reached %d bytes (%v)", name, size, st)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (r *rig) marker() bool {
	_, err := os.Stat(filepath.Join(r.stateDir, markerFile))
	return err == nil
}

func TestRecordStartsAndStopsCleanly(t *testing.T) {
	r := newRig(t, config.RecManual, nil)
	if i := r.rec.Info(); i.Mode != "off" || i.State != StateOff || i.Disk.State != StorageReady || i.Disk.FS != "exfat" {
		t.Fatalf("idle: %+v", i)
	}
	r.rec.Set(true)
	i := r.wait("on", func(i Info) bool { return i.State == StateOn })
	if i.Mode != "on" || i.File == "" || !r.marker() {
		t.Fatalf("on: %+v marker=%v", i, r.marker())
	}
	if *r.rec.Info().Disk.MinutesLeft <= 0 {
		t.Fatal("no time left reported")
	}
	p, out := r.session()
	if p.Base != 0 && p.State == 0 {
		t.Fatal("pipeline not playing")
	}
	if p.Zero != [2]string{"rv", "ra"} {
		t.Fatalf("the file's timeline does not start at 0: %v", p.Zero)
	}
	stream := append([][]byte{ftyp, moov}, fragments(3, 1000)...)
	out.Feed(stream...)
	r.waitFile(i.File, len(cat(stream...)))
	r.rec.Set(false)
	r.wait("off", func(i Info) bool { return i.State == StateOff && i.Mode == "off" })
	if !p.EOS || !p.Closed || r.marker() {
		t.Fatalf("not finished cleanly: eos=%v closed=%v marker=%v", p.EOS, p.Closed, r.marker())
	}
	for _, b := range r.media.Bridges[len(r.media.Bridges)-2:] {
		if b.Dst != "" {
			t.Fatalf("bridge still feeding %s", b.Dst)
		}
	}
	got, _ := os.ReadFile(filepath.Join(r.dir, i.File))
	if !bytes.Equal(got, cat(stream...)) {
		t.Fatalf("the file is not what the muxer sent (%d vs %d bytes)", len(got), len(cat(stream...)))
	}
}

// D6: a marker at start = a power cut while recording. The recording stays stopped, says power_lost, and
// the file is cut at its last complete fragment once its disk is there.
func TestPowerLostTrimsWhenTheDiskIsBack(t *testing.T) {
	good := cat(append([][]byte{ftyp, moov}, fragments(2, 500)...)...)
	r := newRig(t, config.RecManual, func(r *rig) {
		os.WriteFile(filepath.Join(r.dir, "2026-10-24_1200.mp4"), cat(good, moof(1), mdat(800, 'x')[:300]), 0o640)
		b, _ := json.Marshal(marker{UUID: "U1", File: "2026-10-24_1200.mp4"})
		os.WriteFile(filepath.Join(r.stateDir, markerFile), b, 0o644)
		r.disk.set(Storage{State: StorageNone, Seq: 1}) // the disk comes later than the engine
	})
	i := r.rec.Info()
	if i.Mode != "off" || i.Reason != ReasonPowerLost {
		t.Fatalf("after a power cut: %+v", i)
	}
	r.disk.set(Storage{State: StorageReady, Seq: 2, UUID: "U1", Dir: r.dir})
	r.waitFile("2026-10-24_1200.mp4", len(good))
	deadline := time.Now().Add(time.Second)
	for r.marker() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if r.marker() || r.rec.Info().Reason != ReasonPowerLost {
		t.Fatalf("marker=%v %+v", r.marker(), r.rec.Info())
	}
}

// A marker without a uuid matches no disk, not even one without a uuid: nothing of another disk is cut.
func TestPowerLostWithoutUUID(t *testing.T) {
	torn := cat(ftyp, moov, moof(1), mdat(800, 'x')[:300])
	r := newRig(t, config.RecManual, func(r *rig) {
		os.WriteFile(filepath.Join(r.dir, "2026-10-24_1200.mp4"), torn, 0o640)
		b, _ := json.Marshal(marker{File: "2026-10-24_1200.mp4"})
		os.WriteFile(filepath.Join(r.stateDir, markerFile), b, 0o644)
		r.disk.set(Storage{State: StorageReady, Seq: 1, Dir: r.dir})
	})
	time.Sleep(50 * time.Millisecond)
	if b, _ := os.ReadFile(filepath.Join(r.dir, "2026-10-24_1200.mp4")); !bytes.Equal(b, torn) {
		t.Fatal("a disk without a uuid had a file cut")
	}
}

// A disk pulled out while recording: disk_removed, the marker stays (the file is cut when it is back), and
// with REC still on a new mount starts a new file.
func TestDiskRemovedThenBack(t *testing.T) {
	r := newRig(t, config.RecManual, nil)
	r.rec.Set(true)
	first := r.wait("on", func(i Info) bool { return i.State == StateOn }).File
	_, out := r.session()
	out.Feed(ftyp, moov, moof(1), mdat(100, 'v'))
	r.waitFile(first, len(cat(ftyp, moov, moof(1), mdat(100, 'v'))))
	r.disk.set(Storage{State: StorageNone, Seq: 2})
	r.wait("disk_removed", func(i Info) bool { return i.State == StateError && i.Reason == ReasonDiskRemoved })
	if !r.marker() || r.rec.Info().Mode != "on" {
		t.Fatalf("marker=%v mode=%s", r.marker(), r.rec.Info().Mode)
	}
	r.disk.set(Storage{State: StorageReady, Seq: 3, UUID: "U1", Dir: r.dir})
	i := r.wait("on again", func(i Info) bool { return i.State == StateOn && i.File != first })
	if i.File == "" {
		t.Fatal("no new file")
	}
}

// D7: the disk fills up: the file is finished, the recording stops and says so.
func TestDiskFullStops(t *testing.T) {
	r := newRig(t, config.RecManual, nil)
	r.rec.Set(true)
	i := r.wait("on", func(i Info) bool { return i.State == StateOn })
	_, out := r.session()
	out.Feed(ftyp, moov, moof(1), mdat(100, 'v'))
	r.waitFile(i.File, len(cat(ftyp, moov, moof(1), mdat(100, 'v'))))
	r.freeMu.Lock()
	r.free = reserve / 2
	r.freeMu.Unlock()
	i = r.wait("disk_full", func(i Info) bool { return i.Reason == ReasonDiskFull })
	if i.Mode != "off" || i.State != StateError || r.marker() {
		t.Fatalf("disk full: %+v marker=%v", i, r.marker())
	}
	// It stays while that disk does; another disk (a new seq) clears it
	time.Sleep(30 * time.Millisecond)
	if i := r.rec.Info(); i.Reason != ReasonDiskFull {
		t.Fatalf("disk_full went away by itself: %+v", i)
	}
	r.disk.set(Storage{State: StorageReady, Seq: 2, FS: "exfat", UUID: "U2", Dir: r.dir, SizeBytes: 64e9})
	r.wait("off", func(i Info) bool { return i.State == StateOff && i.Reason == "" })
}

// A disk already full: REC is refused before any file, and the state says why (the board test of Oct 28 left
// a 0.8 MB file for each press).
func TestDiskFullBeforeStart(t *testing.T) {
	r := newRig(t, config.RecManual, func(r *rig) { r.free = reserve / 2 })
	r.wait("the room", func(i Info) bool { return !r.rec.RoomLeft() })
	r.rec.Set(true)
	i := r.wait("disk_full", func(i Info) bool { return i.Reason == ReasonDiskFull })
	if i.Mode != "off" || i.State != StateError || r.media.Find("record") != nil {
		t.Fatalf("full disk: %+v, pipeline %v", i, r.media.Find("record") != nil)
	}
	if files, _ := os.ReadDir(r.dir); len(files) != 0 {
		t.Fatalf("files on a full disk: %v", files)
	}
	// Room again (files deleted elsewhere): REC works
	r.freeMu.Lock()
	r.free = 1 << 40
	r.freeMu.Unlock()
	r.wait("the room", func(i Info) bool { return r.rec.RoomLeft() })
	r.rec.Set(true)
	r.wait("on", func(i Info) bool { return i.State == StateOn })
}

func TestNoDisk(t *testing.T) {
	r := newRig(t, config.RecManual, func(r *rig) { r.disk.set(Storage{State: StorageNone}) })
	if r.rec.DiskReady() {
		t.Fatal("ready without a disk")
	}
	r.rec.Set(true)
	r.wait("no_disk", func(i Info) bool { return i.State == StateError && i.Reason == ReasonNoDisk })
	if r.media.Find("record") != nil {
		t.Fatal("a session without a disk")
	}
}

// A storage state that names somewhere else than the recording root is not a disk.
func TestStorageOutsideRoot(t *testing.T) {
	s := Storage{State: StorageReady, Dir: "/etc/x"}
	if s.Ready("/media/eirl-rec") || !(Storage{State: StorageReady, Dir: "/media/eirl-rec/EnhancedIRL"}).Ready("/media/eirl-rec") ||
		(Storage{State: StorageReady, Dir: "/media/eirl-rec/../etc"}).Ready("/media/eirl-rec") {
		t.Fatal("Ready")
	}
	p := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(p, []byte(`{"state":"ready","seq":4,"fs":"fat32","dir":"/media/eirl-rec/EnhancedIRL","anotherDisk":"sdb"}`), 0o644)
	if st := readStorage(p); st.Seq != 4 || !st.FAT32() || (Storage{FS: "exfat"}).FAT32() {
		t.Fatalf("readStorage: %+v", st)
	}
	if st := readStorage(filepath.Join(t.TempDir(), "missing")); st.State != StorageNone {
		t.Fatalf("missing: %+v", st)
	}
	if !full(syscall.ENOSPC) || !gone(syscall.EIO) || gone(syscall.ENOSPC) {
		t.Fatal("errors")
	}
}

func TestNames(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 24, 15, 30, 0, 0, time.Local)
	if b := baseName(now, true, dir); b != "2026-10-24_1530" {
		t.Fatalf("synced: %s", b)
	}
	if a, b := baseName(now, false, dir), baseName(now, false, dir); a != "rec-0001" || b != "rec-0002" {
		t.Fatalf("counter: %s %s", a, b)
	}
	f1, n1, _ := create(dir, "x")
	f2, n2, _ := create(dir, "x")
	f1.Close()
	f2.Close()
	if n1 != "x.mp4" || n2 != "x-2.mp4" {
		t.Fatalf("names: %s %s", n1, n2)
	}
}

// A stop right after the muxer's last buffers: they still reach the file (the puller drains the appsink).
func TestStopDrainsTheMuxer(t *testing.T) {
	r := newRig(t, config.RecManual, nil)
	r.rec.Set(true)
	i := r.wait("on", func(i Info) bool { return i.State == StateOn })
	_, out := r.session()
	stream := append([][]byte{ftyp, moov}, fragments(5, 2000)...)
	out.Feed(stream...)
	r.rec.Set(false)
	r.wait("off", func(i Info) bool { return i.State == StateOff })
	got, _ := os.ReadFile(filepath.Join(r.dir, i.File))
	if !bytes.Equal(got, cat(stream...)) || out.Pending() != 0 {
		t.Fatalf("%d of %d bytes, %d samples left in the appsink", len(got), len(cat(stream...)), out.Pending())
	}
}

// A pipeline that does not build: encoder_error, retried, and no empty files or marker left behind.
func TestStartFailureLeavesNothing(t *testing.T) {
	r := newRig(t, config.RecManual, nil)
	r.media.ParseErr["record"] = &gstParseError{}
	r.rec.Set(true)
	r.wait("encoder_error", func(i Info) bool { return i.State == StateError && i.Reason == ReasonEncoderError })
	time.Sleep(60 * time.Millisecond) // a few retries
	entries, _ := os.ReadDir(r.dir)
	if len(entries) != 0 || r.marker() {
		t.Fatalf("left behind: %d files, marker=%v", len(entries), r.marker())
	}
}

type gstParseError struct{}

func (*gstParseError) Error() string { return "no element mp4mux" }
