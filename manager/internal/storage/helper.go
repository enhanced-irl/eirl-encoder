package storage

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The commands it runs, by absolute path (Ubuntu 18.04; exfat-fuse, exfat-utils and dosfstools come with the system
// layer, the rest with the base).
const (
	CmdBlkid     = "/sbin/blkid"
	CmdMount     = "/bin/mount"
	CmdUmount    = "/bin/umount"
	CmdFsckExfat = "/sbin/fsck.exfat"
	CmdFsckVfat  = "/sbin/fsck.vfat"
	CmdE2fsck    = "/sbin/e2fsck"
	CmdWipefs    = "/sbin/wipefs"
	CmdSfdisk    = "/sbin/sfdisk"
	CmdMkfsExfat = "/sbin/mkfs.exfat"
	CmdSync      = "/bin/sync"
	CmdSystemctl = "/bin/systemctl"
	CmdUdevadm   = "/bin/udevadm"
)

// Exit codes.
const (
	ExitOK        = 0
	ExitFailed    = 1
	ExitNoDisk    = 2  // eject or format without a disk
	ExitRecording = 3  // eject or format while the engine records
	ExitBusy      = 4  // another action of the helper holds the disk (a disk being checked)
	ExitChanged   = 5  // format: the state's seq is not the one the user confirmed (another disk, or the same one again)
	ExitUsage     = 64 // bad arguments
)

// Names on the disk and of the formatted volume.
const (
	FolderName  = "EnhancedIRL"
	FormatLabel = "EIRL-REC"
	testName    = ".eirl-write-test"
	testSize    = 32 << 20 // the write test: 32 MiB, an fsync every 8 MiB
	testChunk   = 1 << 20
	testSync    = 8 << 20
)

// Time limits.
const (
	tProbe     = 20 * time.Second
	tCheck     = 120 * time.Second // a read-only fsck; past it the check is skipped, not failed
	tMount     = 60 * time.Second
	tUmount    = 30 * time.Second
	tSync      = 60 * time.Second
	tWriteTest = 60 * time.Second
	tWipe      = 30 * time.Second
	tPartition = 60 * time.Second
	tMkfs      = 5 * time.Minute
	tSettle    = 20 * time.Second
	tSystemctl = 15 * time.Second
	tRestart   = 11 * time.Minute // eirl-storage@<disk>.service: TimeoutStartSec=600 plus its stop
	// The relay waits for the unit that does the work (their TimeoutStartSec plus a margin)
	tRelayEject  = 150 * time.Second
	tRelayFormat = 21 * time.Minute
)

// RecordingDrain: how long detach waits, with the disk still there (a shutdown, a restart of its unit), for the engine
// to finish a recording before taking the disk down: its clean stop closes the file in a few seconds, and an fsync can
// stall for six. A variable for the tests.
var RecordingDrain = 20 * time.Second

// How long the lock is waited for: attach and detach come from systemd and may wait for a check of another disk; eject
// and format come from the app, which is told "busy" instead. Variables for the tests.
var (
	LockWaitUnit   = 5 * time.Minute
	LockWaitDetach = 60 * time.Second
	LockWaitAction = 10 * time.Second
)

// Helper: one invocation of eirl-storage. Every path has a root prefix (tests).
type Helper struct {
	SysDir      string // /sys
	ProcDir     string // /proc
	DevDir      string // /dev
	RunDir      string // /run/eirl-storage: state.json and the lock
	MountPoint  string // /media/eirl-rec
	EngineState string // /run/eirl-engine/mixer.json
	EngineUID   int    // eirl-engine
	EncoderGID  int    // eirl-encoder (the app; the engine has it as a supplementary group)
	Runner      Runner
	Log         func(format string, args ...any)
	Ctx         context.Context // cancelled when systemd stops the unit (SIGTERM)
	// Direct: do eject and format here (inside their units: EIRL_STORAGE_DIRECT=1). Otherwise they are relayed to
	// eirl-storage-eject.service / eirl-storage-format@<seq>.service (the app's sudo runs inside its sandbox, whose mount
	// namespace and read-only /run would make an unmount or a remount invisible to the rest of the system)
	Direct bool
	// Unit: the unit that attaches a disk ("eirl-storage@%s.service")
	Unit string

	cur   State  // the state file as this invocation last read or wrote it (seq)
	other string // a second disk, carried in every state of the current one
}

// errBusy: the lock is held for longer than the wait.
var errBusy = errors.New("busy")

func (h *Helper) statePath() string { return filepath.Join(h.RunDir, "state.json") }

func (h *Helper) logf(format string, args ...any) {
	if h.Log != nil {
		h.Log(format, args...)
	}
}

func (h *Helper) ctx() context.Context {
	if h.Ctx != nil {
		return h.Ctx
	}
	return context.Background()
}

func (h *Helper) run(timeout time.Duration, stdin, name string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(h.ctx(), timeout)
	defer cancel()
	return h.Runner.Run(ctx, stdin, name, args...)
}

// lastLine: the last non-empty line of a command's output, bounded (for the log).
func lastLine(out string) string {
	ls := strings.Split(strings.TrimSpace(out), "\n")
	return printable(ls[len(ls)-1], 200)
}

// lock takes the helper's lock (/run/eirl-storage/lock), waiting at most wait.
func (h *Helper) lock(wait time.Duration) (func(), error) {
	if err := os.MkdirAll(h.RunDir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(h.RunDir, "lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, errBusy
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// load reads the state file into h.cur (under the lock).
func (h *Helper) load() {
	st, err := ReadState(h.statePath())
	if err != nil {
		h.logf("event=state_unreadable detail=%q", err.Error())
	}
	h.cur = st
}

// put writes a new state: the next seq, the time, and the second disk (if any) with every state but none.
func (h *Helper) put(st State) error { return h.write(st, h.cur.Seq+1) }

// noteOther writes the state again with only the second disk changed, and the same seq: the mounted disk is the
// same one, and to the engine a new seq is a new mount (a recording would stop as if the disk had gone).
func (h *Helper) noteOther(st State) error { return h.write(st, h.cur.Seq) }

func (h *Helper) write(st State, seq int64) error {
	st.Seq = seq
	st.At = time.Now().Format(time.RFC3339)
	if st.State != StateNone {
		st.AnotherDisk = h.other
	}
	if err := os.MkdirAll(h.RunDir, 0o755); err != nil {
		return err
	}
	if err := writeStateFile(h.statePath(), st); err != nil {
		h.logf("event=state_write_failed detail=%q", err.Error())
		return err
	}
	h.cur = st
	h.logf("event=state state=%s reason=%s seq=%d device=%s partition=%s fs=%s", st.State, st.Reason, st.Seq, st.Device, st.Partition, st.FS)
	return nil
}

// checkHostNS refuses to act from another mount namespace than PID 1's (a sandboxed unit): a mount made there would not
// be the system's. When it cannot tell (no /proc), it goes on.
func (h *Helper) checkHostNS() error {
	self, err1 := os.Readlink(filepath.Join(h.ProcDir, "self", "ns", "mnt"))
	init, err2 := os.Readlink(filepath.Join(h.ProcDir, "1", "ns", "mnt"))
	if err1 == nil && err2 == nil && self != init {
		return fmt.Errorf("not in the system's mount namespace (%s, PID 1 %s)", self, init)
	}
	return nil
}

func (h *Helper) present(disk string) bool {
	if disk == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(h.SysDir, "block", disk))
	return err == nil
}

func (h *Helper) mounts() []Mount {
	b, err := os.ReadFile(filepath.Join(h.ProcDir, "self", "mountinfo"))
	if err != nil {
		return nil
	}
	return ParseMountinfo(string(b))
}

// ours: the top mount at the mount point, if any.
func (h *Helper) ours() (Mount, bool) {
	var m Mount
	found := false
	for _, x := range h.mounts() {
		if x.Mountpoint == h.MountPoint {
			m, found = x, true
		}
	}
	return m, found
}

// inUse: where the disk or one of its partitions is used outside our mount point (the root filesystem, another mount,
// swap); "" when nowhere. Never the boot device.
func (h *Helper) inUse(d Disk) string {
	nums := map[string]bool{}
	srcs := map[string]bool{filepath.Join(h.DevDir, d.Name): true}
	if d.DevNum != "" {
		nums[d.DevNum] = true
	}
	for _, p := range d.Parts {
		srcs[filepath.Join(h.DevDir, p.Name)] = true
		if p.DevNum != "" {
			nums[p.DevNum] = true
		}
	}
	for _, m := range h.mounts() {
		if m.Mountpoint == h.MountPoint {
			continue
		}
		if nums[m.DevNum] || srcs[m.Source] {
			return m.Mountpoint
		}
	}
	if b, err := os.ReadFile(filepath.Join(h.ProcDir, "swaps")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(l); len(f) > 0 && srcs[f[0]] {
				return "swap"
			}
		}
	}
	return ""
}

// unmount takes our mount point down. With the disk gone it is lazy at once; otherwise a normal unmount, tried three
// times, and lazy only if lazyOK (a pulled disk, a restart).
func (h *Helper) unmount(present, lazyOK bool) error {
	if _, ok := h.ours(); !ok {
		return nil
	}
	if present {
		var last string
		for i := 0; i < 3; i++ {
			out, code, err := h.run(tUmount, "", CmdUmount, h.MountPoint)
			if _, ok := h.ours(); !ok {
				return nil
			}
			last = lastLine(out)
			if err != nil {
				last = err.Error()
			} else if code == 0 {
				continue // stacked mounts: the next one
			}
			time.Sleep(time.Second)
		}
		if !lazyOK {
			return fmt.Errorf("umount: %s", last)
		}
		h.logf("event=umount_busy detail=%q", last)
	}
	for i := 0; i < 3; i++ {
		if _, ok := h.ours(); !ok {
			return nil
		}
		if out, code, err := h.run(tUmount, "", CmdUmount, "-l", h.MountPoint); err != nil || code != 0 {
			h.logf("event=umount_lazy_failed detail=%q", lastLine(out))
		}
	}
	if _, ok := h.ours(); ok {
		return errors.New("still mounted after a lazy unmount")
	}
	return nil
}

func (h *Helper) sync() {
	if out, code, err := h.run(tSync, "", CmdSync); err != nil || code != 0 {
		h.logf("event=sync_failed detail=%q", lastLine(out))
	}
}

// probe runs blkid on a device node.
func (h *Helper) probe(name string, size int64) Candidate {
	c := Candidate{Name: name, Size: size}
	out, code, err := h.run(tProbe, "", CmdBlkid, "-p", "-o", "udev", filepath.Join(h.DevDir, name))
	switch {
	case err != nil:
		h.logf("event=probe_failed device=%s detail=%q", name, err.Error())
		return c
	case code == 2: // nothing found
		return c
	case code != 0: // 8: ambivalent (two signatures): treated as nothing
		h.logf("event=probe_ambivalent device=%s code=%d", name, code)
		return c
	}
	c.Type, c.Version, c.Label, c.UUID = ParseBlkid(out)
	return c
}

// fsck: the read-only check of the filesystem before mounting it. true = errors found (dirty). A check that cannot run
// or does not finish in time is logged and counts as clean: it is a hint, never a reason not to record.
func (h *Helper) fsck(c Candidate) bool {
	dev := filepath.Join(h.DevDir, c.Name)
	var out string
	var code int
	var err error
	switch c.Type {
	case "exfat": // exfat-utils 1.2.8 only checks, it never repairs (and has no -n)
		out, code, err = h.run(tCheck, "", CmdFsckExfat, dev)
	case "vfat":
		out, code, err = h.run(tCheck, "", CmdFsckVfat, "-n", dev)
	case "ext4":
		out, code, err = h.run(tCheck, "", CmdE2fsck, "-n", dev)
	default:
		return false
	}
	if err != nil {
		h.logf("event=check_skipped device=%s detail=%q", c.Name, err.Error())
		return false
	}
	dirty := false
	switch c.Type {
	case "ext4": // a bit mask: 4 = errors left uncorrected; 8 and up = it could not check
		if code >= 8 {
			h.logf("event=check_skipped device=%s code=%d detail=%q", c.Name, code, lastLine(out))
			return false
		}
		dirty = code&4 != 0
	case "vfat": // 0 clean, 1 errors found
		if code > 1 {
			h.logf("event=check_skipped device=%s code=%d detail=%q", c.Name, code, lastLine(out))
			return false
		}
		dirty = code == 1
	default:
		dirty = code != 0
	}
	if dirty {
		h.logf("event=dirty device=%s fs=%s detail=%q", c.Name, c.Type, lastLine(out))
	}
	return dirty
}

// prepareMountPoint: /media/eirl-rec, a real folder (not a link), 0755, which only root can write: if nothing is
// mounted, nobody writes recordings to the card underneath.
func (h *Helper) prepareMountPoint() error {
	if err := os.MkdirAll(h.MountPoint, 0o755); err != nil {
		return err
	}
	fi, err := os.Lstat(h.MountPoint)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", h.MountPoint)
	}
	return os.Chmod(h.MountPoint, 0o755)
}

// Attach: a USB disk appeared (eirl-storage@<disk>.service). Always exits 0 once it has said what it found in the state
// file, so that the unit stays active and its stop (the disk pulled out) runs detach.
func (h *Helper) Attach(disk string) int {
	if !ValidDisk(disk) {
		h.logf("event=bad_request detail=%q", "not a disk name: "+disk)
		return ExitUsage
	}
	if err := h.checkHostNS(); err != nil {
		h.logf("event=refused detail=%q", err.Error())
		return ExitFailed
	}
	unlock, err := h.lock(LockWaitUnit)
	if err != nil {
		h.logf("event=lock_failed disk=%s detail=%q", disk, err.Error())
		return ExitFailed
	}
	defer unlock()
	h.load()
	d, err := readDisk(h.SysDir, disk)
	if err != nil {
		h.logf("event=ignored disk=%s reason=gone detail=%q", disk, err.Error())
		return ExitOK
	}
	if !d.USB {
		h.logf("event=ignored disk=%s reason=not_usb", disk)
		return ExitOK
	}
	if where := h.inUse(d); where != "" {
		h.logf("event=ignored disk=%s reason=in_use where=%q", disk, where)
		return ExitOK
	}
	cur := h.cur
	if cur.State != StateNone && cur.Device != "" && cur.Device != disk && h.present(cur.Device) {
		h.logf("event=another_disk disk=%s current=%s", disk, cur.Device)
		if cur.AnotherDisk != disk {
			h.other = disk
			cur.AnotherDisk = disk
			_ = h.noteOther(cur)
		}
		return ExitOK
	}
	if cur.AnotherDisk != disk && h.present(cur.AnotherDisk) {
		h.other = cur.AnotherDisk
	}
	h.logf("event=attach disk=%s model=%q size=%d usb=%t ro=%t partitions=%d", disk, d.Model, d.Size, d.USB, d.ReadOnly, len(d.Parts))
	// A mount of ours still there (a restart that did not detach): out first
	if err := h.unmount(true, true); err != nil {
		h.logf("event=stale_mount detail=%q", err.Error())
	}
	base := State{Device: disk, Model: d.Model, SizeBytes: d.Size}
	st := base
	st.State = StateChecking
	if h.put(st) != nil {
		return ExitFailed
	}
	if h.ctx().Err() != nil {
		return h.cancelled()
	}

	var parts []Candidate
	for _, p := range d.Parts {
		parts = append(parts, h.probe(p.Name, p.Size))
	}
	whole := h.probe(disk, d.Size)
	dec := Decide(d.Size, parts, &whole)
	if h.ctx().Err() != nil {
		return h.cancelled()
	}
	st = base
	st.State, st.Reason = dec.State, dec.Reason
	if c := dec.Pick; c != nil {
		st.Partition, st.FS, st.Label, st.UUID, st.SizeBytes = c.Name, FSName(*c), c.Label, c.UUID, c.Size
	}
	if dec.State != StateReady {
		h.logf("event=found disk=%s state=%s reason=%s", disk, dec.State, dec.Reason)
		_ = h.put(st)
		return ExitOK
	}

	// Mount the chosen one
	c := *dec.Pick
	fail := func(reason string, ro, dirty bool, detail string) int {
		if h.ctx().Err() != nil { // a command killed by the unit's stop, not a fault of the disk
			return h.cancelled()
		}
		h.logf("event=attach_failed disk=%s partition=%s reason=%s detail=%q", disk, c.Name, reason, detail)
		if err := h.unmount(h.present(disk), true); err != nil {
			h.logf("event=umount_failed detail=%q", err.Error())
		}
		st.State, st.Reason = StateError, reason
		st.ReadOnly, st.Dirty = boolPtr(ro), boolPtr(dirty)
		st.Mountpoint, st.Dir, st.WriteMBps = "", "", 0
		_ = h.put(st)
		return ExitOK
	}
	if d.ReadOnly {
		return fail(ReasonReadOnly, true, false, "the disk is write-protected")
	}
	dirty := h.fsck(c)
	if h.ctx().Err() != nil {
		return h.cancelled()
	}
	if err := h.prepareMountPoint(); err != nil {
		return fail(ReasonMountFailed, false, dirty, err.Error())
	}
	fstype, opts := MountArgs(c, h.EngineUID, h.EncoderGID)
	out, code, err := h.run(tMount, "", CmdMount, "-t", fstype, "-o", opts, filepath.Join(h.DevDir, c.Name), h.MountPoint)
	if err != nil || code != 0 {
		detail := lastLine(out)
		if err != nil {
			detail = err.Error()
		}
		return fail(ReasonMountFailed, false, dirty, detail)
	}
	m, ok := h.ours()
	if !ok {
		return fail(ReasonMountFailed, false, dirty, "not in mountinfo after mount")
	}
	if miss := MissingFlags(m); len(miss) > 0 {
		// exfat-fuse does not hand every flag to the kernel: the mount point gets them by a bind remount
		h.logf("event=remount_flags missing=%s", strings.Join(miss, ","))
		out, code, err := h.run(tMount, "", CmdMount, "-o", "remount,bind,nosuid,nodev,noexec,noatime", h.MountPoint)
		m, ok = h.ours()
		if err != nil || code != 0 || !ok || len(MissingFlags(m)) > 0 {
			return fail(ReasonMountFailed, false, dirty, "cannot set nosuid,nodev,noexec: "+lastLine(out))
		}
	}
	if MountReadOnly(m) {
		return fail(ReasonReadOnly, true, dirty, "mounted read-only")
	}
	if h.ctx().Err() != nil {
		return h.cancelled()
	}
	root, err := os.OpenRoot(h.MountPoint)
	if err != nil {
		return fail(ReasonMountFailed, false, dirty, err.Error())
	}
	defer root.Close()
	if err := h.prepareFolder(root, c.Type == "ext4"); err != nil {
		return fail(ReasonCheckFailed, false, dirty, err.Error())
	}
	mbps, err := h.writeTest(root)
	if err != nil {
		if errors.Is(err, syscall.EROFS) {
			return fail(ReasonReadOnly, true, dirty, err.Error())
		}
		return fail(ReasonCheckFailed, false, dirty, err.Error())
	}
	if h.ctx().Err() != nil {
		return h.cancelled()
	}
	st.State, st.Reason = StateReady, ""
	st.Mountpoint, st.Dir = h.MountPoint, filepath.Join(h.MountPoint, FolderName)
	st.ReadOnly, st.Dirty, st.WriteMBps = boolPtr(false), boolPtr(dirty), mbps
	h.logf("event=ready disk=%s partition=%s fs=%s dirty=%t write_mbps=%.1f", disk, c.Name, st.FS, dirty, mbps)
	if h.put(st) != nil {
		return ExitFailed
	}
	return ExitOK
}

// cancelled: systemd stopped the unit while it attached (the disk pulled out during the check). Its ExecStopPost runs
// detach too; this leaves nothing mounted and the state none already.
func (h *Helper) cancelled() int {
	h.Ctx = context.Background() // the commands below must run
	h.logf("event=attach_cancelled device=%s", h.cur.Device)
	_ = h.unmount(false, true)
	h.other = ""
	_ = h.put(State{State: StateNone})
	return ExitFailed
}

// prepareFolder: EnhancedIRL/ at the root of the disk, a real folder (on ext4, owned by the engine, 0750 and setgid: the
// engine's files take the folder's group, the app's, as the mount gives them on exFAT and FAT). Everything goes through
// os.Root: a link on the disk never takes it outside.
func (h *Helper) prepareFolder(root *os.Root, ext4 bool) error {
	fi, err := root.Lstat(FolderName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := root.Mkdir(FolderName, 0o750); err != nil {
			return err
		}
	case err != nil:
		return err
	case !fi.IsDir(): // a file or a link with that name
		return fmt.Errorf("%s on the disk is not a folder", FolderName)
	}
	if !ext4 {
		return nil // exFAT and FAT: the owner and modes are the mount's
	}
	if err := root.Lchown(FolderName, h.EngineUID, h.EncoderGID); err != nil {
		return err
	}
	if err := root.Chmod(FolderName, 0o750|fs.ModeSetgid); err != nil {
		return err
	}
	// The engine must be able to go through the disk's root folder
	rfi, err := root.Stat(".")
	if err != nil {
		return err
	}
	mode := rfi.Mode().Perm()
	ok := mode&0o001 != 0
	if s, isSys := rfi.Sys().(*syscall.Stat_t); isSys {
		ok = ok || (int(s.Uid) == h.EngineUID && mode&0o100 != 0) || (int(s.Gid) == h.EncoderGID && mode&0o010 != 0)
	}
	if !ok {
		return fmt.Errorf("the disk's root folder (%v) cannot be entered by the engine", mode)
	}
	return nil
}

// writeTest writes 32 MiB in EnhancedIRL/ with an fsync every 8 MiB, removes it and gives MB/s. A disk with less than
// 64 MiB free is not tested (0). Bounded by tWriteTest: a disk that stalls longer fails it.
func (h *Helper) writeTest(root *os.Root) (float64, error) {
	var sfs unix.Statfs_t
	if err := unix.Statfs(filepath.Join(h.MountPoint, FolderName), &sfs); err == nil {
		if uint64(sfs.Bavail)*uint64(sfs.Bsize) < 2*testSize {
			h.logf("event=write_test_skipped reason=low_space")
			return 0, nil
		}
	}
	name := filepath.Join(FolderName, testName)
	_ = root.Remove(name) // a leftover of a cut test (Remove never follows a link)
	type result struct {
		mbps float64
		err  error
	}
	done := make(chan result, 1)
	go func() {
		start := time.Now()
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			done <- result{err: err}
			return
		}
		buf := make([]byte, testChunk)
		_, _ = rand.Read(buf)
		for n := 0; n < testSize; n += testChunk {
			if _, err := f.Write(buf); err != nil {
				f.Close()
				done <- result{err: err}
				return
			}
			if (n+testChunk)%testSync == 0 {
				if err := f.Sync(); err != nil {
					f.Close()
					done <- result{err: err}
					return
				}
			}
		}
		if err := f.Close(); err != nil {
			done <- result{err: err}
			return
		}
		secs := time.Since(start).Seconds()
		done <- result{mbps: math.Round(float64(testSize)/1e6/math.Max(secs, 1e-3)*10) / 10}
	}()
	select {
	case r := <-done:
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) && r.err == nil {
			r.err = err
		}
		return r.mbps, r.err
	case <-time.After(tWriteTest):
		return 0, fmt.Errorf("write test: %d MiB not written in %v", testSize>>20, tWriteTest)
	case <-h.ctx().Done():
		return 0, h.ctx().Err()
	}
}

// Detach: the disk's unit stops (the disk pulled out, a restart, the shutdown). Idempotent: ExecStop and ExecStopPost
// both run it.
func (h *Helper) Detach(disk string) int {
	if !ValidDisk(disk) {
		h.logf("event=bad_request detail=%q", "not a disk name: "+disk)
		return ExitUsage
	}
	if err := h.checkHostNS(); err != nil {
		h.logf("event=refused detail=%q", err.Error())
		return ExitFailed
	}
	unlock, err := h.lock(LockWaitDetach)
	if err != nil {
		// A pulled disk is unmounted all the same: nothing may stay mounted on a disk that is gone
		h.logf("event=lock_failed disk=%s detail=%q", disk, err.Error())
		unlock = func() {}
	}
	defer unlock()
	h.load()
	cur := h.cur
	switch {
	case cur.Device == disk && cur.State != StateNone:
		present := h.present(disk)
		if present {
			h.drainRecording()
		}
		if err := h.unmount(present, true); err != nil {
			h.logf("event=umount_failed disk=%s detail=%q", disk, err.Error())
		}
		h.logf("event=detach disk=%s present=%t", disk, present)
		h.other = ""
		_ = h.put(State{State: StateNone})
		// A second disk waiting is the disk now, once this one is gone (not when its unit restarts: a format, a shutdown)
		if o := cur.AnotherDisk; !present && o != "" && o != disk && ValidDisk(o) && h.present(o) {
			out, _, _ := h.run(tSystemctl, "", CmdSystemctl, "is-system-running")
			if strings.TrimSpace(out) == "stopping" {
				return ExitOK
			}
			h.logf("event=takeover disk=%s", o)
			if out, code, err := h.run(tSystemctl, "", CmdSystemctl, "--no-block", "restart", fmt.Sprintf(h.Unit, o)); err != nil || code != 0 {
				h.logf("event=takeover_failed disk=%s detail=%q", o, lastLine(out))
			}
		}
	case cur.AnotherDisk == disk:
		h.other = ""
		cur.AnotherDisk = ""
		h.logf("event=another_disk_gone disk=%s", disk)
		_ = h.noteOther(cur)
	}
	return ExitOK
}

// drainRecording waits, at most RecordingDrain, while the engine runs and records on this disk: at a shutdown both units
// stop at once, and taking the disk down first would cut the file's last fragments.
func (h *Helper) drainRecording() {
	deadline := time.Now().Add(RecordingDrain)
	for {
		rec, _ := Recording(h.EngineState)
		if !rec {
			return
		}
		out, _, _ := h.run(tSystemctl, "", CmdSystemctl, "is-active", "eirl-engine.service")
		if st := strings.TrimSpace(out); st != "active" && st != "deactivating" && st != "reloading" {
			return // its state file says what it did last, not what it does: a stopped engine records nothing
		}
		if time.Now().After(deadline) {
			h.logf("event=drain_timeout after_s=%d", int(RecordingDrain.Seconds()))
			return
		}
		h.logf("event=draining detail=%q", "the engine is finishing a recording")
		select {
		case <-h.ctx().Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// relay: from the app's sudo, the action runs in its unit (in the system's mount namespace, with nothing of the app's
// sandbox), and its exit status comes back. A recording is refused here already, without starting the unit.
func (h *Helper) relay(action, unit string, timeout time.Duration) int {
	if rec, why := Recording(h.EngineState); rec {
		h.logf("event=refused action=%s reason=recording detail=%q", action, why)
		return ExitRecording
	}
	out, code, err := h.run(timeout, "", CmdSystemctl, "start", unit)
	if err == nil && code == 0 {
		return ExitOK
	}
	st, _, _ := h.run(tSystemctl, "", CmdSystemctl, "show", "-p", "ExecMainStatus", "--value", unit)
	if n, e := strconv.Atoi(strings.TrimSpace(st)); e == nil && n >= ExitFailed && n <= ExitChanged {
		return n
	}
	detail := lastLine(out)
	if err != nil {
		detail = err.Error()
	}
	h.logf("event=relay_failed action=%s detail=%q", action, detail)
	return ExitFailed
}

// begin: what eject and format share: their unit, the namespace, the lock, not while recording, a disk.
func (h *Helper) begin(action string) (func(), int) {
	if err := h.checkHostNS(); err != nil {
		h.logf("event=refused action=%s detail=%q", action, err.Error())
		return nil, ExitFailed
	}
	unlock, err := h.lock(LockWaitAction)
	if err != nil {
		h.logf("event=refused action=%s reason=busy detail=%q", action, err.Error())
		if errors.Is(err, errBusy) {
			return nil, ExitBusy
		}
		return nil, ExitFailed
	}
	if rec, why := Recording(h.EngineState); rec {
		unlock()
		h.logf("event=refused action=%s reason=recording detail=%q", action, why)
		return nil, ExitRecording
	}
	h.load()
	if h.cur.State == StateNone || h.cur.Device == "" {
		unlock()
		h.logf("event=refused action=%s reason=no_disk", action)
		return nil, ExitNoDisk
	}
	if h.cur.AnotherDisk != h.cur.Device && h.present(h.cur.AnotherDisk) {
		h.other = h.cur.AnotherDisk
	}
	return unlock, ExitOK
}

// Eject: sync and unmount, so that the disk can be pulled out (RECORDING.md §4.2).
func (h *Helper) Eject() int {
	if !h.Direct {
		return h.relay("eject", "eirl-storage-eject.service", tRelayEject)
	}
	unlock, code := h.begin("eject")
	if code != ExitOK {
		return code
	}
	defer unlock()
	cur := h.cur
	if cur.State == StateEjected {
		return ExitOK
	}
	h.sync()
	if err := h.unmount(h.present(cur.Device), false); err != nil {
		h.logf("event=eject_failed disk=%s detail=%q", cur.Device, err.Error())
		return ExitFailed
	}
	h.sync()
	st := State{State: StateEjected, Device: cur.Device, Partition: cur.Partition, Model: cur.Model, FS: cur.FS,
		Label: cur.Label, UUID: cur.UUID, SizeBytes: cur.SizeBytes}
	h.logf("event=ejected disk=%s", cur.Device)
	if h.put(st) != nil {
		return ExitFailed
	}
	return ExitOK
}

// Format: one partition (MBR type 7; GPT basic data on a disk over 2 TiB) with exFAT labelled EIRL-REC, then the disk's
// unit is restarted, which mounts it like a newly plugged disk (its FUSE daemon must live in that unit, not in this
// one, whose processes are killed when it ends).
func (h *Helper) Format(seq int64) int {
	if !h.Direct {
		return h.relay("format", fmt.Sprintf("eirl-storage-format@%d.service", seq), tRelayFormat)
	}
	unlock, code := h.begin("format")
	if code != ExitOK {
		return code
	}
	cur := h.cur
	// The disk the user confirmed, under the lock: one pulled out after the confirmation, or another one that took its
	// place, has another seq
	if cur.Seq != seq {
		unlock()
		h.logf("event=refused action=format reason=disk_changed seq=%d confirmed=%d", cur.Seq, seq)
		return ExitChanged
	}
	disk := cur.Device
	d, err := readDisk(h.SysDir, disk)
	switch {
	case err != nil:
		unlock()
		h.logf("event=refused action=format disk=%s reason=no_disk detail=%q", disk, err.Error())
		return ExitNoDisk
	case !d.USB:
		unlock()
		h.logf("event=refused action=format disk=%s reason=not_usb", disk)
		return ExitFailed
	case d.ReadOnly:
		unlock()
		h.logf("event=refused action=format disk=%s reason=readonly", disk)
		return ExitFailed
	}
	if where := h.inUse(d); where != "" {
		unlock()
		h.logf("event=refused action=format disk=%s reason=in_use where=%q", disk, where)
		return ExitFailed
	}
	h.logf("event=format disk=%s model=%q size=%d", disk, d.Model, d.Size)
	h.sync()
	if err := h.unmount(true, false); err != nil {
		unlock()
		h.logf("event=format_failed disk=%s step=unmount detail=%q", disk, err.Error())
		return ExitFailed // still mounted: the state stays as it was
	}
	_ = h.put(State{State: StateChecking, Device: disk, Model: d.Model, SizeBytes: d.Size})
	ferr := h.format(d)
	h.sync()
	unlock()
	// The disk's unit mounts what is there now (also after a failure: it says what the disk has become)
	unit := fmt.Sprintf(h.Unit, disk)
	if out, code, err := h.run(tRestart, "", CmdSystemctl, "restart", unit); err != nil || code != 0 {
		h.logf("event=restart_failed unit=%s detail=%q", unit, lastLine(out))
	}
	if ferr != nil {
		h.logf("event=format_failed disk=%s detail=%q", disk, ferr.Error())
		return ExitFailed
	}
	st, _ := ReadState(h.statePath())
	if st.State != StateReady || st.Device != disk {
		h.logf("event=format_failed disk=%s detail=%q", disk, "formatted, but the disk is "+st.State+" "+st.Reason)
		return ExitFailed
	}
	h.logf("event=formatted disk=%s", disk)
	return ExitOK
}

// PartitionScript: sfdisk's script for one partition over the whole disk.
func PartitionScript(size int64) string {
	if size > mbrLimit {
		return "label: gpt\n,,EBD0A0A2-B9E5-4433-87C0-68B6B72699C7\n"
	}
	return "label: dos\n,,7\n"
}

func (h *Helper) format(d Disk) error {
	step := func(name string, timeout time.Duration, stdin, cmd string, args ...string) error {
		out, code, err := h.run(timeout, stdin, cmd, args...)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if code != 0 {
			return fmt.Errorf("%s: exit %d: %s", name, code, lastLine(out))
		}
		return nil
	}
	diskDev := filepath.Join(h.DevDir, d.Name)
	for _, p := range d.Parts {
		if err := step("wipefs", tWipe, "", CmdWipefs, "-a", filepath.Join(h.DevDir, p.Name)); err != nil {
			return err
		}
	}
	if err := step("wipefs", tWipe, "", CmdWipefs, "-a", diskDev); err != nil {
		return err
	}
	if err := step("sfdisk", tPartition, PartitionScript(d.Size), CmdSfdisk, "-q", diskDev); err != nil {
		return err
	}
	_, _, _ = h.run(tSettle, "", CmdUdevadm, "settle", "--timeout=15")
	part := d.Name + "1"
	partDev := filepath.Join(h.DevDir, part)
	deadline := time.Now().Add(tSettle)
	for {
		if _, err := os.Stat(partDev); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not appear after partitioning", partDev)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return step("mkfs.exfat", tMkfs, "", CmdMkfsExfat, "-n", FormatLabel, partDev)
}
