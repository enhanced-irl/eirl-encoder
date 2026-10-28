// Package record is the encoder's local recording (RECORDING.md): the program, with its own high-bitrate
// encode, to a USB disk, independent of the stream. It is the output's sibling (MIXER.md §9): the program's
// recv/reca taps, pass bridges, and a pipeline per session on the program's clock and base_time, created
// when recording starts and destroyed when it stops.
//
//	program ─ tee vt ─▶ appsink recv ┐ bridges ─▶ [record session] NVENC VBR + AAC ─▶ mp4mux (fragmented)
//	audio   ─ tee at ─▶ appsink reca ┘                                 ─▶ appsink rout ─▶ writer ─▶ disk
//
// The disk is the storage helper's (eirl-storage, root): the recorder only reads its state file and writes
// in the recordings' folder it names. A failure of the recording never touches the stream, nor the
// reverse. The muxed MP4 (~3 MB/s) enters Go; frames never do.
package record

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// States and reasons (RECORDING.md §5.2).
const (
	StateOff      = "off"
	StateStarting = "starting"
	StateOn       = "on"
	StateError    = "error"

	ReasonNoDisk       = "no_disk"
	ReasonDiskRemoved  = "disk_removed"
	ReasonDiskFull     = "disk_full"
	ReasonDiskSlow     = "disk_slow"
	ReasonWriteError   = "write_error"
	ReasonEncoderError = "encoder_error"
	ReasonPowerLost    = "power_lost"
	ReasonInternal     = "internal"
)

// Timings, variables so that the tests can shorten them.
var (
	tick         = 250 * time.Millisecond // the session's watch, and the idle loop's storage read
	idleTick     = time.Second
	measureEvery = 10 * time.Second
	freeEvery    = 5 * time.Second
	eosWait      = 3 * time.Second // the muxer's last fragments after EOS
	pipeClose    = 3 * time.Second
	slowFor      = 30 * time.Second // disk_slow stays this long after the last dropped fragment
	retryWaits   = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second}
	// reserve is the room kept free: the recording stops cleanly before the disk is full (D7)
	reserve int64 = 64 << 20
	// reserveTime is the bitrate's worth of room kept on top of reserve
	reserveTime = 30 * time.Second
)

// markerFile, in STATE_DIR while a recording is open: if the engine starts with it, the power was cut
// (D6) and the file is cut at its last complete fragment when that disk is back.
const markerFile = "recording.json"

type marker struct {
	UUID    string `json:"uuid"`
	File    string `json:"file"` // relative to the recordings' folder
	Started string `json:"started"`
}

// Options is what the recorder needs from the engine.
type Options struct {
	Config *config.Config
	Media  gst.Media
	// Program has the appsinks recv and reca; its base_time is set (PLAYING) before the first session.
	Program gst.Pipeline
	// Desc builds a session's pipeline for its configuration (pipeline.Platform.Recording).
	Desc func(c *config.Config) string
	// StateDir keeps the open recording's marker and the names' counter ("" = neither).
	StateDir string
	// OnChange is called when Info may have changed, from the recorder's goroutines; it must not block.
	OnChange func()

	// For the tests ("" / nil = the real ones)
	ReadStorage func() Storage
	Free        func(dir string) (int64, error)
	Now         func() time.Time
	Synced      func() bool
}

// Disk is the state's record.disk: the storage helper's state and the room left.
type Disk struct {
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	FS          string `json:"fs,omitempty"`
	Label       string `json:"label,omitempty"`
	SizeBytes   *int64 `json:"sizeBytes,omitempty"`
	FreeBytes   *int64 `json:"freeBytes,omitempty"`
	MinutesLeft *int   `json:"minutesLeft,omitempty"`
	Dirty       bool   `json:"dirty,omitempty"`
}

// Info is the state's `record` object without by/seq, which the engine owns (RECORDING.md §5.2).
type Info struct {
	Mode     string `json:"mode"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	Detail   string `json:"detail,omitempty"`
	ElapsedS *int   `json:"elapsedS,omitempty"`
	Kbps     *int   `json:"kbps,omitempty"`
	DroppedS *int   `json:"droppedS,omitempty"`
	File     string `json:"file,omitempty"`
	Disk     Disk   `json:"disk"`
}

// failure ends a session: a reason of the contract and a detail for the journal.
type failure struct{ reason, detail string }

func (f *failure) Error() string { return f.reason + ": " + f.detail }

func fail(reason, detail string) error { return &failure{reason, detail} }

var errReconfig = errors.New("recording configuration replaced")

// Recorder is the local recording. One goroutine runs it from New to Close.
type Recorder struct {
	o        Options
	taps     [2]gst.Element // recv, reca
	bridges  [2]gst.Bridge
	wake     chan struct{}
	done     chan struct{}
	cfgMu    sync.Mutex
	cfg      *config.Config
	gen      int
	mu       sync.Mutex
	want     bool
	stopping bool
	state    string
	reason   string
	detail   string
	fullSeq  int64 // the storage seq of the disk that filled up: disk_full stays while that disk does
	started  time.Time
	file     string
	kbps     int
	dropped  int
	lastDrop time.Time
	storage  Storage
	freeB    int64
	freeOK   bool
	freeAt   time.Time
	elapsed  int     // the last elapsedS published
	pending  *marker // a recording a power cut left open, until its disk is back
}

// New prepares the recorder on the program; it fails without the recv/reca taps.
func New(o Options) (*Recorder, error) {
	if o.ReadStorage == nil {
		path := o.Config.RecStorageState
		o.ReadStorage = func() Storage { return readStorage(path) }
	}
	if o.Free == nil {
		o.Free = free
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Synced == nil {
		o.Synced = clockSynced
	}
	if o.OnChange == nil {
		o.OnChange = func() {}
	}
	r := &Recorder{o: o, cfg: o.Config, state: StateOff, wake: make(chan struct{}, 1), done: make(chan struct{})}
	for i, name := range []string{"recv", "reca"} {
		el := o.Program.ByName(name)
		if el == nil {
			return nil, fmt.Errorf("the program has no %s: no recording", name)
		}
		r.taps[i] = el
	}
	for i := range r.taps {
		r.bridges[i] = o.Media.NewBridge(gst.BridgePass, o.Program, nil)
		r.bridges[i].Attach(r.taps[i])
	}
	if m := r.readMarker(); m != nil {
		// D6: the recording stays stopped; the state says why until the next one starts
		r.pending = m
		r.state, r.reason, r.detail = StateOff, ReasonPowerLost, "the power was cut while recording "+m.File
		elog.Event("record", "power_lost", "file", m.File)
	}
	r.storage = o.ReadStorage()
	go r.run()
	return r, nil
}

// SetConfig takes a new configuration (output_config): a live session ends and the next one, in a new
// file, takes it. REC_MODE off stops the recording.
func (r *Recorder) SetConfig(c *config.Config) {
	r.cfgMu.Lock()
	changed := c.RecQuality != r.cfg.RecQuality
	r.cfg = c
	if changed {
		r.gen++
	}
	r.cfgMu.Unlock()
	if !c.RecordingEnabled() {
		r.Set(false)
	}
	r.o.OnChange()
	r.poke()
}

// Config is the recorder's configuration now (its REC_MODE decides what Go live does).
func (r *Recorder) Config() *config.Config {
	c, _ := r.config()
	return c
}

func (r *Recorder) config() (*config.Config, int) {
	r.cfgMu.Lock()
	defer r.cfgMu.Unlock()
	return r.cfg, r.gen
}

// Set starts or stops recording; it returns at once (the goroutine does the work).
func (r *Recorder) Set(on bool) {
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return
	}
	if on != r.want {
		code := "record_off_requested"
		if on {
			code = "record_on_requested"
		}
		elog.Event("record", code)
	}
	r.want = on
	r.mu.Unlock()
	r.o.OnChange()
	r.poke()
}

// Recording: recording is wanted (the mode); the engine counts it as busy, as streaming.
func (r *Recorder) Recording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.want
}

// DiskReady: the storage helper has a disk ready for recordings.
func (r *Recorder) DiskReady() bool {
	r.mu.Lock()
	s := r.storage
	r.mu.Unlock()
	return s.Ready(r.root())
}

// RoomLeft: the disk has more room than a clean stop needs (D7); true while the room is not known.
func (r *Recorder) RoomLeft() bool {
	c, _ := r.config()
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.freeOK || !lowRoom(r.freeB, c.Recording())
}

// lowRoom: less room than a clean stop needs: reserve, and reserveTime at the quality's bitrate.
func lowRoom(free int64, q config.RecQuality) bool {
	return free < reserve+int64(q.VKbps+q.AKbps)*1000/8*int64(reserveTime/time.Second)
}

func (r *Recorder) root() string {
	c, _ := r.config()
	return c.RecDir
}

// Info is the recording for the state.
func (r *Recorder) Info() Info {
	c, _ := r.config()
	q := c.Recording()
	r.mu.Lock()
	defer r.mu.Unlock()
	i := Info{Mode: "off", State: r.state, Reason: r.reason, Detail: r.detail}
	if r.want {
		i.Mode = "on"
	}
	s := r.storage
	i.Disk = Disk{State: s.State, Reason: s.Reason, FS: s.FS, Label: s.Label, Dirty: s.Dirty}
	if s.SizeBytes > 0 {
		v := s.SizeBytes
		i.Disk.SizeBytes = &v
	}
	if r.freeOK && s.Ready(c.RecDir) {
		f := r.freeB
		i.Disk.FreeBytes = &f
		m := int(f / (int64(q.VKbps+q.AKbps) * 1000 / 8) / 60)
		i.Disk.MinutesLeft = &m
	}
	if r.state != StateOn {
		return i
	}
	el := int(r.o.Now().Sub(r.started).Seconds())
	i.ElapsedS = &el
	k, d := r.kbps, r.dropped
	i.Kbps, i.DroppedS = &k, &d
	i.File = r.file
	if !r.lastDrop.IsZero() && r.o.Now().Sub(r.lastDrop) < slowFor {
		i.Reason = ReasonDiskSlow
	}
	return i
}

func (r *Recorder) setState(state, reason, detail string) {
	r.mu.Lock()
	same := state == r.state && reason == r.reason && detail == r.detail
	r.state, r.reason, r.detail = state, reason, detail
	r.mu.Unlock()
	if !same {
		r.o.OnChange()
	}
}

func (r *Recorder) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Recorder) sleep(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-r.wake:
	case <-t.C:
	}
}

func (r *Recorder) flags() (want, stopping bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.want, r.stopping
}

func (r *Recorder) running() bool {
	w, s := r.flags()
	return w && !s
}

// Close ends the recording when the engine stops: a live file is finished cleanly within budget.
func (r *Recorder) Close(budget time.Duration) bool {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
	r.poke()
	select {
	case <-r.done:
		return true
	case <-time.After(budget):
		elog.Event("record", "record_close_timeout", "budget_s", budget.Seconds())
		return false
	}
}

// Release frees the bridges, once the recorder is closed and the program is in NULL.
func (r *Recorder) Release() {
	for i, b := range r.bridges {
		if b != nil {
			b.Free()
			r.bridges[i] = nil
		}
	}
}

// --- the loop ---

func (r *Recorder) run() {
	defer close(r.done)
	fails := 0
	var lastSeq int64 = -1 // a disk_removed waits for another mount (another seq)
	for {
		want, stopping := r.flags()
		if stopping {
			return
		}
		st := r.refreshStorage()
		r.trimPending(st)
		if !want {
			// Off, but a full disk keeps saying why it stopped until the next recording or another disk (D7)
			r.mu.Lock()
			idle := r.state == StateOff || (r.reason == ReasonDiskFull && st.Seq == r.fullSeq)
			r.mu.Unlock()
			if !idle {
				r.setState(StateOff, "", "")
			}
			fails, lastSeq = 0, -1
			r.sleep(idleTick)
			continue
		}
		if !st.Ready(r.root()) || st.Seq == lastSeq {
			reason := ReasonNoDisk
			if lastSeq >= 0 {
				reason = ReasonDiskRemoved
			}
			r.setState(StateError, reason, "waiting for a disk")
			r.sleep(idleTick)
			continue
		}
		if !r.RoomLeft() {
			// D7 before any file: a disk that is already full gets no recording, not even a stub
			elog.Event("record", "record_failed", "reason", ReasonDiskFull, "detail", "no room left")
			r.mu.Lock()
			r.want, r.fullSeq = false, st.Seq
			r.mu.Unlock()
			r.setState(StateError, ReasonDiskFull, "the disk is full")
			continue
		}
		err := r.safeSession(st)
		if errors.Is(err, errReconfig) {
			fails = 0
			continue
		}
		if !r.running() {
			r.setState(StateOff, "", "")
			continue
		}
		var f *failure
		if !errors.As(err, &f) {
			f = &failure{ReasonInternal, fmt.Sprint(err)}
		}
		elog.Event("record", "record_failed", "reason", f.reason, "detail", f.detail)
		switch f.reason {
		case ReasonDiskFull:
			// D7: stop and say so
			r.mu.Lock()
			r.want, r.fullSeq = false, st.Seq
			r.mu.Unlock()
			r.setState(StateError, f.reason, f.detail)
			continue
		case ReasonDiskRemoved:
			lastSeq = st.Seq
			r.setState(StateError, f.reason, f.detail)
			continue
		}
		r.setState(StateError, f.reason, f.detail)
		fails++
		wait := retryWaits[min(fails, len(retryWaits))-1]
		end := time.Now().Add(wait)
		for r.running() && time.Now().Before(end) {
			r.sleep(min(tick, time.Until(end)))
		}
	}
}

// refreshStorage reads the helper's state, and the room left at most every freeEvery or when the state
// changed (another disk); the state changes when either does (the room by whole MB).
func (r *Recorder) refreshStorage() Storage {
	st := r.o.ReadStorage()
	r.mu.Lock()
	changed := st != r.storage
	r.storage = st
	due := st.Ready(r.root()) && (changed || time.Since(r.freeAt) >= freeEvery)
	if due {
		r.freeAt = time.Now()
	}
	r.mu.Unlock()
	if due {
		b, err := r.o.Free(st.Dir)
		r.mu.Lock()
		changed = changed || r.freeOK != (err == nil) || r.freeB>>20 != b>>20
		r.freeB, r.freeOK = b, err == nil
		r.mu.Unlock()
	}
	if changed {
		r.o.OnChange()
	}
	return st
}

// trimPending cuts the recording a power cut left open, once its disk (by uuid) is ready.
func (r *Recorder) trimPending(st Storage) {
	r.mu.Lock()
	m := r.pending
	r.mu.Unlock()
	// That disk by its uuid; without one no disk is that disk (an empty uuid would match any other without one)
	if m == nil || m.UUID == "" || !st.Ready(r.root()) || st.UUID != m.UUID {
		return
	}
	p := filepath.Join(st.Dir, m.File)
	n, err := trimFile(p)
	switch {
	case err == nil:
		elog.Event("record", "power_lost_trimmed", "file", m.File, "bytes", n)
	case errors.Is(err, os.ErrNotExist):
	default:
		elog.Event("record", "power_lost_trim_failed", "file", m.File, "err", err)
	}
	r.mu.Lock()
	r.pending = nil
	r.mu.Unlock()
	r.removeMarker()
}

func (r *Recorder) safeSession(st Storage) (err error) {
	defer func() {
		if x := recover(); x != nil {
			err = fail(ReasonInternal, fmt.Sprint(x))
		}
	}()
	return r.session(st)
}

// --- a session: one file (or one FAT32 series) ---

type session struct {
	r       *Recorder
	cfg     *config.Config
	gen     int
	st      Storage
	pipe    gst.Pipeline
	out     gst.Element
	counter gst.Counter
	w       *writer
	stopPl  chan struct{}
	pulled  chan struct{}
	feeding bool
	frames  uint64
}

func (r *Recorder) session(st Storage) error {
	cfg, gen := r.config()
	r.mu.Lock()
	r.pending = nil // a new recording: the old marker is overwritten below
	r.mu.Unlock()
	r.setState(StateStarting, "", "")
	s := &session{r: r, cfg: cfg, gen: gen, st: st}
	defer s.teardown()
	base := baseName(r.o.Now(), r.o.Synced(), r.o.StateDir)
	f, name, err := create(st.Dir, base)
	if err != nil {
		return s.diskErr(err, "cannot create the recording")
	}
	r.writeMarker(marker{UUID: st.UUID, File: name, Started: r.o.Now().Format(time.RFC3339)})
	next := splitBase(name)
	s.w = newWriter(st.Dir, st.FAT32(), f, name, func(dir string) (*os.File, string, error) { return create(dir, next) })
	if err := s.start(); err != nil {
		// Nothing was recorded: no empty file and no marker left behind by each retry
		s.w.drop()
		_ = os.Remove(filepath.Join(st.Dir, name))
		r.removeMarker()
		return err
	}
	r.mu.Lock()
	r.started, r.file, r.kbps, r.dropped, r.lastDrop, r.elapsed = r.o.Now(), name, 0, 0, time.Time{}, 0
	r.mu.Unlock()
	r.setState(StateOn, "", "")
	elog.Event("record", "record_started", "file", name, "fs", st.FS, "quality", cfg.RecQuality)
	return s.watch()
}

// start builds and plays the session's pipeline and hooks it on the program.
func (s *session) start() error {
	r := s.r
	p, err := r.o.Media.Parse("record", r.o.Desc(s.cfg))
	if err != nil {
		return fail(ReasonEncoderError, "the recording pipeline does not build: "+err.Error())
	}
	s.pipe = p
	p.FixBase(r.o.Program.BaseTime())
	// The file's timeline starts at 0, not where the program is (minutes or hours in)
	if !p.ZeroStart("rv", "ra") {
		return fail(ReasonInternal, "the recording pipeline has no rv or ra")
	}
	if enc := p.ByName("renc"); enc != nil {
		s.counter = r.o.Media.NewCounter(enc, "src")
	}
	s.out = p.ByName("rout")
	if s.out == nil {
		return fail(ReasonInternal, "the recording pipeline has no rout")
	}
	if err := p.SetState(gst.StatePlaying); err != nil {
		detail := "the recording pipeline does not start"
		if m := p.Pop(0, gst.MessageError); m != nil {
			detail = m.Text
		}
		return fail(ReasonEncoderError, detail)
	}
	s.stopPl, s.pulled = make(chan struct{}), make(chan struct{})
	go s.pull()
	s.frames = r.bridges[0].Stats(false, false).Frames
	for i, name := range []string{"rv", "ra"} {
		r.bridges[i].SetDst(p.ByName(name))
	}
	s.feeding = true
	return nil
}

// pull drains the muxer into the writer until stopped; once stopped it still takes what the appsink holds
// (after EOS its last fragments), and ends at the first empty pull.
func (s *session) pull() {
	defer close(s.pulled)
	var sp splitter
	stopping := false
	for {
		if !stopping {
			select {
			case <-s.stopPl:
				stopping = true
			default:
			}
		}
		b := s.out.PullBytes(100 * time.Millisecond)
		if b == nil {
			if stopping {
				return
			}
			continue
		}
		boxes, err := sp.push(b)
		for _, x := range boxes {
			s.w.offer(x)
		}
		if err != nil {
			elog.Event("record", "record_bad_stream", "err", err)
			return
		}
	}
}

// watch follows the session until it is stopped or something falls.
func (s *session) watch() error {
	r := s.r
	tMeas := time.Now()
	var lastWritten int64
	for {
		if !r.running() {
			return s.finish()
		}
		if _, gen := r.config(); gen != s.gen {
			if err := s.finish(); err != nil {
				return err
			}
			return errReconfig
		}
		if m := s.pipe.Pop(tick, gst.MessageError); m != nil {
			elog.Event("record", "record_encoder_error", "element", m.Src, "error", m.Text)
			s.abort()
			return fail(ReasonEncoderError, m.Text)
		}
		ws := s.w.stats()
		if ws.err != nil {
			s.abort()
			return s.diskErr(ws.err, "writing the recording")
		}
		st := r.refreshStorage()
		if st.Seq != s.st.Seq || !st.Ready(r.root()) {
			s.abort()
			return fail(ReasonDiskRemoved, "the disk went away")
		}
		r.mu.Lock()
		changed := ws.dropped != r.dropped || ws.name != r.file
		if ws.dropped != r.dropped {
			r.dropped, r.lastDrop = ws.dropped, r.o.Now()
			elog.Event("record", "record_dropping", "dropped_s", ws.dropped, "queued", ws.queued)
		}
		r.file = ws.name
		if el := int(r.o.Now().Sub(r.started).Seconds()); el != r.elapsed {
			r.elapsed, changed = el, true
		}
		freeB, freeOK := r.freeB, r.freeOK
		r.mu.Unlock()
		if freeOK && lowRoom(freeB, s.cfg.Recording()) {
			if err := s.finish(); err != nil {
				return err
			}
			return fail(ReasonDiskFull, "the disk is full")
		}
		if dt := time.Since(tMeas); dt >= measureEvery {
			s.measure(dt, ws.written-lastWritten)
			lastWritten, tMeas = ws.written, time.Now()
			changed = true
		}
		if changed {
			r.o.OnChange()
		}
	}
}

// measure: what enters the encoder, what it makes, what reaches the disk.
func (s *session) measure(dt time.Duration, written int64) {
	r := s.r
	var nIn, nEnc uint64
	if s.feeding {
		f := r.bridges[0].Stats(false, false).Frames
		nIn, s.frames = f-s.frames, f
	}
	if s.counter != nil {
		nEnc, _ = s.counter.Take()
	}
	sec := dt.Seconds()
	kbps := int(float64(written) * 8 / 1000 / sec)
	r.mu.Lock()
	r.kbps = kbps
	r.mu.Unlock()
	elog.Event("record", "record_rates", "in_fps", round1(float64(nIn)/sec), "enc_fps", round1(float64(nEnc)/sec), "kbps", kbps)
}

func round1(x float64) float64 { return float64(int(x*10+0.5)) / 10 }

// finish ends the file cleanly: nothing more in, EOS so the muxer closes its last fragments, the writer
// empties its queue and syncs; only then the marker goes.
func (s *session) finish() error {
	r := s.r
	s.unhook()
	if s.pipe.SendEOS() {
		end := time.Now().Add(eosWait)
		for time.Now().Before(end) {
			if m := s.pipe.Pop(min(tick, time.Until(end)), gst.MessageEOS|gst.MessageError); m != nil {
				break
			}
		}
	}
	s.stopPull()
	err := s.w.finish()
	if err != nil {
		return s.diskErr(err, "finishing the recording")
	}
	r.removeMarker()
	elog.Event("record", "record_stopped", "file", s.w.stats().name)
	return nil
}

// abort ends a session that cannot finish: the disk went, or the encoder fell. The marker stays when the
// file could not be closed cleanly: it is cut when that disk is back.
func (s *session) abort() {
	s.unhook()
	s.stopPull()
	s.w.drop()
}

func (s *session) unhook() {
	if s.feeding {
		for _, b := range s.r.bridges {
			b.SetDst(nil)
		}
		s.feeding = false
	}
}

func (s *session) stopPull() {
	if s.stopPl != nil {
		select {
		case <-s.stopPl:
		default:
			close(s.stopPl)
		}
		select {
		case <-s.pulled:
		case <-time.After(time.Second):
		}
	}
}

// diskErr names a write error: full, gone, or another one.
func (s *session) diskErr(err error, what string) error {
	switch {
	case full(err):
		return fail(ReasonDiskFull, what+": "+err.Error())
	case gone(err):
		return fail(ReasonDiskRemoved, what+": "+err.Error())
	}
	st := s.r.o.ReadStorage()
	if st.Seq != s.st.Seq || !st.Ready(s.r.root()) {
		return fail(ReasonDiskRemoved, what+": "+err.Error())
	}
	return fail(ReasonWriteError, what+": "+err.Error())
}

func (s *session) teardown() {
	s.unhook()
	s.stopPull()
	if s.pipe != nil {
		if !gst.CloseWithin(s.pipe, pipeClose) {
			elog.Event("record", "record_close_stuck", "after_s", pipeClose.Seconds())
		} else if s.counter != nil {
			s.counter.Free()
		}
	}
	gst.TrimMalloc()
}

// --- the marker ---

func (r *Recorder) markerPath() string {
	if r.o.StateDir == "" {
		return ""
	}
	return filepath.Join(r.o.StateDir, markerFile)
}

func (r *Recorder) readMarker() *marker {
	p := r.markerPath()
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var m marker
	if json.Unmarshal(b, &m) != nil || m.File == "" || strings.ContainsAny(m.File, "/\\") || m.File == ".." {
		_ = os.Remove(p)
		return nil
	}
	return &m
}

func (r *Recorder) writeMarker(m marker) {
	p := r.markerPath()
	if p == "" {
		return
	}
	b, _ := json.Marshal(m)
	if err := writeFileAtomic(p, b, 0o644); err != nil {
		elog.Event("record", "record_marker_failed", "err", err)
	}
}

func (r *Recorder) removeMarker() {
	if p := r.markerPath(); p != "" {
		_ = os.Remove(p)
	}
}
