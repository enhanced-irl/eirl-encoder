package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/storage"
)

// --- local recording to a USB disk, simulated (RECORDING.md §6) ---------------------------------------------------
//
// The storage helper (simStorage): its state file, which the real app reads, and its eject and format (a
// storage.Runner for api.Server.StorageRunner), which change it after a short delay and refuse while recording, like
// eirl-storage. The disk is plugged and pulled through a control file in the data folder, read twice a second:
//
//	echo exfat > <data>/disk           a 64 GB exFAT stick: checking for 2 s, then ready
//	echo 'fat32 dirty another' > <data>/disk
//	rm <data>/disk                     pulled out without ejecting (while recording: disk_removed)
//
// The first word is the disk: exfat, fat32 or ext4 (ready), ntfs (unsupported), small (unsupported, too small), blank
// (unformatted) or readonly (error). The words after it change it in place: dirty (not ejected last time), another (a
// second disk is plugged in too), low (about 2 minutes of room: the warning, then disk_full), full (no room: REC answers
// disk_full), slow (a disk that cannot keep up while recording: disk_slow) and checking (it stays checking). A new first
// word is a different disk: the old one is pulled and the new one checked.
//
// The recorder (the simEngine's record command and state, below) follows RECORDING.md §5.2: REC_MODE and REC_QUALITY
// from the saved settings (at start and on output_config), REC and Go live with with_output, the time, the bitrate,
// the free space going down, a new file at 3.9 GB on FAT32 and after a disk_removed (-2, -3… when the name is taken),
// disk_full until the next recording or another disk, and power_lost at start with -rec-power-lost.

// How long the simulated eject and format take (variables for the tests).
var (
	simEjectWait  = time.Second
	simFormatWait = 3 * time.Second
)

// simDisk: what the control file says is plugged in.
type simDisk struct {
	kind                                     string // exfat | fat32 | ext4 | ntfs | small | blank | readonly
	model                                    string
	size                                     int64
	dirty, another, low, full, slow, waiting bool
}

func parseSimDisk(s string) (simDisk, bool) {
	f := strings.Fields(strings.ToLower(s))
	if len(f) == 0 {
		return simDisk{}, false
	}
	d := simDisk{kind: f[0]}
	for _, w := range f[1:] {
		switch w {
		case "dirty":
			d.dirty = true
		case "another":
			d.another = true
		case "low":
			d.low = true
		case "full":
			d.full = true
		case "slow":
			d.slow = true
		case "checking":
			d.waiting = true
		}
	}
	// A 64 GB stick, a 1 TB hard disk, a 512 MB stick and an SD card in a reader
	d.model, d.size = "DataTraveler 3.0", 62008590336
	switch d.kind {
	case "exfat", "fat32", "ext4", "blank":
	case "ntfs":
		d.model, d.size = "Expansion HDD", 1000204886016
	case "small":
		d.size = 512 << 20
	case "readonly":
		d.model, d.size = "SD Card Reader", 31914983424
	default:
		return simDisk{}, false
	}
	return d, true
}

// simStorage: the storage helper, simulated.
type simStorage struct {
	statePath, ctlPath string
	log                *slog.Logger
	recording          func() bool // the engine records or starts to (eject and format refuse)
	onChange           func()      // the state changed (the engine publishes its record.disk)

	mu      sync.Mutex
	st      storage.State
	seq     int64   // st's: up on every change, like the helper's
	ctl     string  // the control file as last read
	disk    simDisk // what is plugged in (none: kind "")
	used    int64   // what is written on it
	mount   int     // goes up on every mount: a recording knows its disk is still the one it writes to
	checked time.Time
	busy    bool // an eject or a format is running
}

func newSimStorage(dir, initial string, log *slog.Logger) (*simStorage, error) {
	s := &simStorage{statePath: filepath.Join(dir, "storage-state.json"), ctlPath: filepath.Join(dir, "disk"), log: log,
		recording: func() bool { return false }, onChange: func() {}}
	if initial != "" {
		if _, ok := parseSimDisk(initial); !ok {
			return nil, fmt.Errorf("-disk %q: exfat, fat32, ext4, ntfs, small, blank or readonly, then dirty, another, low, full, slow or checking", initial)
		}
		if err := os.WriteFile(s.ctlPath, []byte(initial+"\n"), 0o644); err != nil {
			return nil, err
		}
	}
	// At boot the state is none, seq 0 (tmpfiles); what is plugged in is checked as it comes
	s.st = storage.State{State: storage.StateNone}
	return s, s.writeLocked()
}

// putLocked: a new state, written whole with the next seq. One that only changes the second disk keeps the seq, like
// the helper (to the engine a new seq is a new mount).
func (s *simStorage) putLocked(st storage.State) error {
	same := st
	same.AnotherDisk, same.Seq, same.At = s.st.AnotherDisk, s.st.Seq, s.st.At
	if !reflect.DeepEqual(same, s.st) {
		s.seq++
	}
	st.Seq, st.At = s.seq, time.Now().Format(time.RFC3339)
	s.st = st
	return s.writeLocked()
}

// run follows the control file.
func (s *simStorage) run(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		s.poll()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *simStorage) poll() {
	b, _ := os.ReadFile(s.ctlPath)
	ctl := strings.TrimSpace(string(b))
	s.mu.Lock()
	var next *storage.State
	if ctl != s.ctl && !s.busy {
		s.ctl = ctl
		d, ok := parseSimDisk(ctl)
		switch {
		case ctl != "" && !ok:
			s.log.Warn("sim disk: unknown control line: left as it was", "line", ctl)
		case !ok:
			if s.disk.kind != "" {
				s.log.Info("sim disk: pulled out")
				s.disk, s.used = simDisk{}, 0
				next = &storage.State{State: storage.StateNone}
			}
		case d.kind == s.disk.kind && s.st.State != storage.StateNone && s.st.State != storage.StateEjected:
			s.log.Info("sim disk: changed in place", "line", ctl)
			s.disk, s.used = d, s.usedFor(d)
			st := s.stateOf(d, s.st.State == storage.StateChecking)
			next = &st
		default:
			s.log.Info("sim disk: plugged in", "line", ctl)
			s.disk, s.used, s.checked = d, s.usedFor(d), time.Now()
			st := s.stateOf(d, true)
			next = &st
		}
	}
	// Checking takes 2 s (the stick of the board test took about 1)
	if st := s.st; next == nil && st.State == storage.StateChecking && !s.disk.waiting && !s.busy &&
		time.Since(s.checked) >= 2*time.Second {
		st = s.stateOf(s.disk, false)
		if st.State == storage.StateReady {
			s.mount++
		}
		next = &st
	}
	if next != nil {
		s.putLocked(*next)
	}
	s.mu.Unlock()
	if next != nil {
		s.onChange()
	}
}

// usedFor: what is on a disk when it is plugged in: a low one has about 2 minutes of room at the high quality,
// a full one none.
func (s *simStorage) usedFor(d simDisk) int64 {
	switch {
	case d.full:
		return d.size - 100<<20
	case d.low:
		return d.size - 600<<20
	}
	return 3 << 30
}

// stateOf: the state file for a disk (checking, or what the check found), without its seq (putLocked).
func (s *simStorage) stateOf(d simDisk, checking bool) storage.State {
	st := storage.State{State: storage.StateChecking, Device: "sda", Model: d.model, SizeBytes: d.size}
	if d.another {
		st.AnotherDisk = "sdb"
	}
	if checking {
		return st
	}
	f, t := false, true
	ready := func(fs, label string) {
		st.State, st.Partition, st.FS, st.Label, st.UUID = storage.StateReady, "sda1", fs, label, "1234-ABCD"
		st.Mountpoint, st.Dir, st.ReadOnly, st.Dirty, st.WriteMBps = "/media/eirl-rec", "/media/eirl-rec/EnhancedIRL", &f, &f, 31.2
		if d.dirty {
			st.Dirty = &t
		}
	}
	switch d.kind {
	case "exfat":
		ready("exfat", "EIRL-REC")
	case "fat32":
		ready("fat32", "KINGSTON")
	case "ext4":
		ready("ext4", "recordings")
	case "ntfs":
		st.State, st.Reason, st.Partition, st.FS, st.Label = storage.StateUnsupported, storage.ReasonFsNTFS, "sda1", "ntfs", "Seagate Expansion"
	case "small":
		st.State, st.Reason = storage.StateUnsupported, storage.ReasonTooSmall
	case "blank":
		st.State = storage.StateUnformatted
	case "readonly":
		st.State, st.Reason, st.Partition, st.FS, st.Label = storage.StateError, storage.ReasonReadOnly, "sda1", "exfat", "SD CARD"
		st.ReadOnly, st.Dirty = &t, &f
	}
	return st
}

func (s *simStorage) writeLocked() error {
	b, err := json.Marshal(s.st)
	if err != nil {
		return err
	}
	return config.WriteAtomic(s.statePath, append(b, '\n'), 0o644)
}

// simDiskNow: what the recorder needs of the disk.
type simDiskNow struct {
	st    storage.State
	free  int64
	mount int
	slow  bool
}

func (d simDiskNow) ready() bool { return d.st.State == storage.StateReady && d.mount > 0 }

func (s *simStorage) now() simDiskNow {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := simDiskNow{st: s.st, slow: s.disk.slow}
	if s.st.State == storage.StateReady {
		d.free, d.mount = max(s.disk.size-s.used, 0), s.mount
	}
	return d
}

// write: a recording wrote n bytes on the disk.
func (s *simStorage) write(n int64) {
	s.mu.Lock()
	s.used = min(s.used+n, s.disk.size)
	s.mu.Unlock()
}

// Run is eirl-storage's eject or format through sudo (storage.Runner): the action is the last argument. It waits like
// the helper (a short delay), refuses while recording (3), without a disk (2) or while one is checked (4).
func (s *simStorage) Run(ctx context.Context, _ string, _ string, args ...string) (string, int, error) {
	action := ""
	if len(args) > 0 {
		action = args[len(args)-1]
	}
	if action != "eject" && action != "format" {
		return "usage: eirl-storage attach|detach <disk> | eject | format", storage.ExitUsage, nil
	}
	if s.recording() {
		return "event=refused action=" + action + " reason=recording", storage.ExitRecording, nil
	}
	s.mu.Lock()
	switch {
	case s.busy || s.st.State == storage.StateChecking:
		s.mu.Unlock()
		return "event=refused action=" + action + " reason=busy", storage.ExitBusy, nil
	case s.st.State == storage.StateNone:
		s.mu.Unlock()
		return "event=refused action=" + action + " reason=no_disk", storage.ExitNoDisk, nil
	case action == "eject" && s.st.State == storage.StateEjected:
		s.mu.Unlock()
		return "", storage.ExitOK, nil
	}
	s.busy = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.busy = false; s.mu.Unlock() }()
	wait := simEjectWait
	if action == "format" {
		wait = simFormatWait
	}
	select {
	case <-ctx.Done():
		return "", -1, ctx.Err()
	case <-time.After(wait):
	}
	s.mu.Lock()
	if s.st.State == storage.StateNone { // pulled out meanwhile
		s.mu.Unlock()
		return "event=" + action + "_failed detail=\"the disk went away\"", storage.ExitFailed, nil
	}
	var next storage.State
	if action == "eject" {
		cur := s.st
		next = storage.State{State: storage.StateEjected, Device: cur.Device, Partition: cur.Partition, Model: cur.Model,
			FS: cur.FS, Label: cur.Label, UUID: cur.UUID, SizeBytes: cur.SizeBytes, AnotherDisk: cur.AnotherDisk}
	} else {
		// One exFAT partition labelled EIRL-REC, then the disk's unit mounts it again (checked at once: the sim's
		// format already took its time). Under 1 GB it is still too small to use
		kind := "exfat"
		if s.disk.size < storage.MinSize {
			kind = "small"
		}
		s.disk = simDisk{kind: kind, model: s.disk.model, size: s.disk.size, another: s.disk.another}
		s.used = 0
		next = s.stateOf(s.disk, false)
		if next.State == storage.StateReady {
			next.Label, next.UUID = storage.FormatLabel, fmt.Sprintf("%04X-%04X", rand.IntN(0x10000), rand.IntN(0x10000))
			s.mount++
		}
	}
	err := s.putLocked(next)
	s.mu.Unlock()
	s.onChange()
	if err != nil {
		return err.Error(), storage.ExitFailed, nil
	}
	s.log.Info("sim disk: " + action + " done")
	return "", storage.ExitOK, nil
}

// --- the recorder of the simulated engine -------------------------------------------------------------------------

// simRecord: the recording's state in the simEngine (under its lock).
type simRecord struct {
	mode, quality      string // REC_MODE, REC_QUALITY (recConfig)
	want, byOutput     bool
	state              string // off | starting | on | error
	reason, detail, by string
	seq                int
	started, written   time.Time // the session's start and its last write
	base, file         string    // the session's name (its start), and the file's (base, or base-2… when taken)
	names              map[string]bool
	fileBytes          int64 // in the current file
	mount, lastMount   int   // the disk it writes to; a disk_removed waits for another mount
	fullSeq            int64 // the storage seq of the disk that filled up: disk_full stays while that disk does
	dropped            int
	lastDrop           time.Time
}

// newFile: the next file of the session, base or, when that name is taken, base-2, base-3… (RECORDING.md §5.3)
func (r *simRecord) newFile() {
	if r.names == nil {
		r.names = map[string]bool{}
	}
	name := r.base
	for n := 2; r.names[name]; n++ {
		name = fmt.Sprintf("%s-%d", r.base, n)
	}
	r.names[name], r.file, r.fileBytes = true, name, 0
}

// recQuality: the encoding of a quality (high when it is unknown).
func recQuality(id string) config.RecQuality {
	for _, q := range config.RecQualities {
		if q.ID == id {
			return q
		}
	}
	return config.RecQualities[1]
}

// bytesPerSecond: what a quality writes.
func bytesPerSecond(q config.RecQuality) int64 { return int64(q.VideoKbps+q.AudioKbps) * 1000 / 8 }

// roomLeft: the engine's rule (D7): 64 MB plus 30 s at the quality's bitrate.
func roomLeft(free int64, q config.RecQuality) bool { return free >= 64<<20+30*bytesPerSecond(q) }

// recordingLocked: recording or starting to (the storage helper's rule).
func (e *simEngine) recordingLocked() bool {
	return e.rec.want || e.rec.state == "starting" || e.rec.state == "on"
}

func (e *simEngine) recordingNow() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.recordingLocked()
}

// toggleRecordLocked: REC (by socket) or Go live (by output) turn it on or off.
func (e *simEngine) toggleRecordLocked(on bool, by string) {
	if e.rec.want != on {
		e.rec.by, e.rec.seq = by, e.rec.seq+1
		e.log.Info("engine: record_toggled", "on", on, "by", by)
		if on { // a new recording: how the last one ended (disk_full, power_lost…) is gone
			e.rec.state, e.rec.reason, e.rec.detail, e.rec.lastMount = "off", "", "", 0
		}
	}
	e.rec.want = on
	e.stepRecordLocked(time.Now())
}

// recordCommand: the record command (RECORDING.md §5.2).
func (e *simEngine) recordCommandLocked(on bool) string {
	switch {
	case on && e.rec.mode == config.RecOff:
		return "disabled"
	case on && !e.recordingLocked() && !e.storage.now().ready():
		return "no_disk"
	case on && !e.recordingLocked() && !roomLeft(e.storage.now().free, recQuality(e.rec.quality)):
		return "disk_full"
	}
	e.toggleRecordLocked(on, "socket")
	e.rec.byOutput = false
	return ""
}

// recordWithOutputLocked: with with_output, Go live also records and Stop stops what Go live started.
func (e *simEngine) recordWithOutputLocked(on bool) {
	if e.rec.mode != config.RecWithOutput {
		return
	}
	switch {
	case on && !e.rec.want:
		e.toggleRecordLocked(true, "output")
		e.rec.byOutput = true
	case !on && e.rec.byOutput:
		e.toggleRecordLocked(false, "output")
		e.rec.byOutput = false
	}
}

// setRecConfigLocked: REC_MODE and REC_QUALITY, at start and on output_config. Off stops the recording; another quality
// starts a new file.
func (e *simEngine) setRecConfigLocked(mode, quality string) {
	changed := quality != e.rec.quality && e.rec.quality != ""
	e.rec.mode, e.rec.quality = mode, quality
	if mode == config.RecOff && e.rec.want {
		e.toggleRecordLocked(false, "socket")
		e.rec.byOutput = false
	}
	if changed && e.rec.state == "on" {
		e.rec.state = "off" // the session ends; the next step opens a new file
		e.stepRecordLocked(time.Now())
	}
}

// stepRecordLocked moves the recording along, what the real recorder's loop does.
func (e *simEngine) stepRecordLocked(now time.Time) {
	r := &e.rec
	d := e.storage.now()
	q := recQuality(r.quality)
	if !r.want {
		// Off; a full disk keeps saying why it stopped until the next recording or another disk (D7), and power_lost
		// until a recording starts
		if r.state != "off" && (r.reason != "disk_full" || d.st.Seq != r.fullSeq) {
			r.state, r.reason, r.detail = "off", "", ""
		}
		return
	}
	if (r.state == "starting" || r.state == "on") && (!d.ready() || d.mount != r.mount) {
		r.state, r.reason, r.detail, r.lastMount = "error", "disk_removed", "the disk went away", r.mount
		return
	}
	if r.state != "starting" && r.state != "on" {
		if !d.ready() || d.mount == r.lastMount {
			r.state, r.reason, r.detail = "error", "no_disk", "waiting for a disk"
			if r.lastMount != 0 {
				r.reason = "disk_removed"
			}
			return
		}
		if !roomLeft(d.free, q) {
			r.want, r.fullSeq = false, d.st.Seq
			r.state, r.reason, r.detail = "error", "disk_full", "the disk is full"
			return
		}
		// A new session, in a new file: after a disk_removed too, named after the time it starts again
		r.state, r.reason, r.detail, r.mount = "starting", "", "", d.mount
		r.started, r.written, r.base, r.dropped, r.lastDrop = now, now, now.Format("2006-01-02_1504"), 0, time.Time{}
		r.newFile()
		return
	}
	if r.state == "starting" {
		if now.Sub(r.started) >= time.Second {
			r.state, r.started, r.written = "on", now, now
		}
		return
	}
	n := bytesPerSecond(q) * int64(now.Sub(r.written)) / int64(time.Second)
	r.written = now
	e.storage.write(n)
	r.fileBytes += n
	if strings.HasPrefix(d.st.FS, "fat") && r.fileBytes >= 3_900_000_000 { // FAT32: the next file at 3.9 GB (D4)
		r.newFile()
	}
	if d.slow && now.Sub(r.lastDrop) >= 5*time.Second {
		r.dropped, r.lastDrop = r.dropped+1, now
	}
	if !roomLeft(d.free-n, q) {
		r.want, r.fullSeq = false, d.st.Seq
		r.state, r.reason, r.detail = "error", "disk_full", "the disk is full"
	}
}

// recordStateLocked: the state's record object.
func (e *simEngine) recordStateLocked() *mixer.Record {
	r := e.rec
	d := e.storage.now()
	q := recQuality(r.quality)
	rec := &mixer.Record{Mode: "off", State: r.state, Setting: r.mode, Quality: r.quality, Reason: r.reason, Detail: r.detail,
		By: r.by, Seq: r.seq, Disk: mixer.RecordDisk{State: d.st.State, Reason: d.st.Reason, FS: d.st.FS, Label: d.st.Label}}
	if r.want {
		rec.Mode = "on"
	}
	if d.st.Dirty != nil {
		rec.Disk.Dirty = *d.st.Dirty
	}
	if d.st.SizeBytes > 0 {
		size := d.st.SizeBytes
		rec.Disk.SizeBytes = &size
	}
	if d.ready() {
		free, left := d.free, int(d.free/bytesPerSecond(q)/60)
		rec.Disk.FreeBytes, rec.Disk.MinutesLeft = &free, &left
	}
	if r.state != "on" {
		return rec
	}
	el, dropped := int(time.Since(r.started).Seconds()), r.dropped
	kbps := q.VideoKbps + q.AudioKbps - 600 + rand.IntN(900) // VBR: around the quality's bitrate
	rec.ElapsedS, rec.Kbps, rec.DroppedS = &el, &kbps, &dropped
	rec.File = r.file + ".mp4"
	if !r.lastDrop.IsZero() && time.Since(r.lastDrop) < 30*time.Second {
		rec.Reason = "disk_slow"
	}
	return rec
}

// runRecorder: the recording's second-by-second (time, bytes, room), published while it is not idle.
func (e *simEngine) runRecorder(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		e.mu.Lock()
		before := e.rec
		e.stepRecordLocked(time.Now())
		busy := e.rec.want || e.rec.state != before.state || e.rec.reason != before.reason
		e.mu.Unlock()
		if busy {
			e.publish()
		}
	}
}

// diskChanged: the storage helper's state changed (a disk plugged, pulled, ejected or formatted).
func (e *simEngine) diskChanged() {
	e.mu.Lock()
	e.stepRecordLocked(time.Now())
	e.mu.Unlock()
	e.publish()
}
