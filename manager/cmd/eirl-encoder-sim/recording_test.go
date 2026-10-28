package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/storage"
)

// The simulated disk and recorder follow RECORDING.md §4-§5: a disk plugged in through the control file is checked
// then ready, REC refuses as the engine does, Go live records with with_output, pulling the disk is disk_removed and a
// new disk a new file, and eject and format refuse while recording
func TestSimRecording(t *testing.T) {
	simEjectWait, simFormatWait = 10*time.Millisecond, 10*time.Millisecond
	dir, err := os.MkdirTemp("", "simrec") // short path (Unix socket limit on macOS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	disk, err := newSimStorage(dir, "", log)
	if err != nil {
		t.Fatal(err)
	}
	e, err := startSimEngine(ctx, dir, disk, true, log)
	if err != nil {
		t.Fatal(err)
	}
	mode, quality := "off", "high"
	e.recConfig = func() (string, string) { return mode, quality }
	record := func() *mixer.Record {
		t.Helper()
		st, err := e.client().Read()
		if err != nil || st.Record == nil {
			t.Fatalf("no record: %v", err)
		}
		return st.Record
	}
	plug := func(line string) {
		t.Helper()
		if line == "" {
			os.Remove(disk.ctlPath)
		} else {
			os.WriteFile(disk.ctlPath, []byte(line+"\n"), 0o644)
		}
		disk.poll()
		disk.checked = time.Now().Add(-time.Minute) // the check is over
		disk.poll()
	}
	on, off := true, false
	rec := func(v *bool) string { return e.command(mixer.Command{Cmd: "record", On: v}) }
	// step: the session started that long ago (past its first second, it is on), then it writes that long at once
	step := func(started, written time.Duration) {
		e.mu.Lock()
		e.rec.started = time.Now().Add(-started)
		e.stepRecordLocked(time.Now())
		e.rec.written = time.Now().Add(-written)
		e.stepRecordLocked(time.Now())
		e.mu.Unlock()
		e.publish()
	}
	helper := func(action string) int {
		_, code, _ := disk.Run(ctx, "", "/usr/bin/sudo", "-n", "/usr/local/sbin/eirl-storage", action)
		return code
	}

	// After a power cut: stopped, and why
	if r := record(); r.Mode != "off" || r.Reason != "power_lost" || r.Disk.State != "none" {
		t.Errorf("at start %+v", r)
	}
	if st, _ := storage.ReadState(disk.statePath); st.State != "none" || st.Seq != 0 {
		t.Errorf("state file at start %+v", st)
	}
	if code := rec(&on); code != "disabled" {
		t.Errorf("mode off: %q", code)
	}
	mode = "manual"
	e.command(mixer.Command{Cmd: "output_config"})
	if code := rec(&on); code != "no_disk" {
		t.Errorf("no disk: %q", code)
	}
	plug("exfat full")
	if code := rec(&on); code != "disk_full" {
		t.Errorf("full disk: %q", code)
	}
	plug("exfat dirty")
	st, _ := storage.ReadState(disk.statePath)
	if st.State != "ready" || st.FS != "exfat" || st.Dirty == nil || !*st.Dirty {
		t.Fatalf("plugged %+v", st)
	}

	// REC: starting, then on, writing to the disk
	if code := rec(&on); code != "" {
		t.Fatalf("rec: %q", code)
	}
	if r := record(); r.Mode != "on" || r.State != "starting" || r.By != "socket" || r.Reason != "" {
		t.Errorf("starting %+v", r)
	}
	free := *record().Disk.FreeBytes
	step(2*time.Second, 10*time.Second)
	r := record()
	first := r.File
	if r.State != "on" || r.ElapsedS == nil || r.Kbps == nil || r.File == "" || *r.Disk.FreeBytes >= free || !r.Disk.Dirty {
		t.Errorf("on %+v", r)
	}
	if helper("eject") != storage.ExitRecording || helper("format") != storage.ExitRecording {
		t.Error("eject and format while recording")
	}

	// Pulled out: disk_removed, still on; another disk: a new file
	plug("")
	if r := record(); r.Mode != "on" || r.State != "error" || r.Reason != "disk_removed" || r.Disk.State != "none" {
		t.Errorf("pulled %+v", r)
	}
	plug("fat32 another")
	if r := record(); r.State != "starting" || r.Disk.FS != "fat32" {
		t.Errorf("another disk %+v", r)
	}
	step(2*time.Second, time.Second)
	if r := record(); r.State != "on" || r.File == "" || r.File == first {
		t.Errorf("resumed in %q after %q", r.File, first)
	} else if base := strings.TrimSuffix(first, ".mp4"); r.File != base+"-2.mp4" && !strings.HasPrefix(r.File, time.Now().Format("2006-01-02")) {
		t.Errorf("resumed in %q after %q", r.File, first)
	}
	if st, _ := storage.ReadState(disk.statePath); st.AnotherDisk != "sdb" {
		t.Errorf("anotherDisk %+v", st)
	}
	// The second disk going away keeps the seq (the mounted disk is the same one)
	seqNow := func() int64 { st, _ := storage.ReadState(disk.statePath); return st.Seq }
	before := seqNow()
	plug("fat32")
	if st, _ := storage.ReadState(disk.statePath); st.AnotherDisk != "" || st.Seq != before || st.State != "ready" {
		t.Errorf("second disk gone: %+v (seq %d before)", st, before)
	}
	if code := rec(&off); code != "" || record().Mode != "off" {
		t.Errorf("stop: %q", code)
	}

	// with_output: Go live records, Stop stops what Go live started; a REC recording stays
	mode = "with_output"
	e.command(mixer.Command{Cmd: "output_config"})
	e.command(mixer.Command{Cmd: "output", On: &on})
	if r := record(); r.Mode != "on" || r.By != "output" {
		t.Errorf("go live %+v", r)
	}
	e.command(mixer.Command{Cmd: "output", On: &off})
	if r := record(); r.Mode != "off" {
		t.Errorf("stop %+v", r)
	}
	rec(&on)
	e.command(mixer.Command{Cmd: "output", On: &on})
	e.command(mixer.Command{Cmd: "output", On: &off})
	if r := record(); r.Mode != "on" || r.By != "socket" {
		t.Errorf("REC's recording %+v", r)
	}
	// The mode off stops it
	mode = "off"
	e.command(mixer.Command{Cmd: "output_config"})
	if r := record(); r.Mode != "off" {
		t.Errorf("mode off %+v", r)
	}

	// Disk full: stopped, and it says so while that disk stays (another disk next to it changes nothing); a different
	// disk clears it
	mode = "manual"
	e.command(mixer.Command{Cmd: "output_config"})
	plug("exfat low")
	if code := rec(&on); code != "" {
		t.Fatalf("rec on a low disk: %q", code)
	}
	step(2*time.Second, 10*time.Minute)
	if r := record(); r.Mode != "off" || r.State != "error" || r.Reason != "disk_full" {
		t.Errorf("filled %+v", r)
	}
	plug("exfat low another")
	e.diskChanged()
	if r := record(); r.Reason != "disk_full" {
		t.Errorf("a second disk cleared disk_full: %+v", r)
	}
	plug("fat32")
	if r := record(); r.State != "off" || r.Reason != "" {
		t.Errorf("another disk kept disk_full: %+v", r)
	}

	// Eject, then format: the helper's state with a new seq each time
	plug("fat32 another")
	before = seqNow()
	if code := helper("eject"); code != storage.ExitOK {
		t.Fatalf("eject %d", code)
	}
	if st, _ := storage.ReadState(disk.statePath); st.State != "ejected" || st.Seq != before+1 || st.AnotherDisk != "sdb" {
		t.Errorf("ejected %+v", st)
	}
	if code := helper("format"); code != storage.ExitOK {
		t.Fatalf("format %d", code)
	}
	if st, _ := storage.ReadState(disk.statePath); st.State != "ready" || st.FS != "exfat" || st.Label != "EIRL-REC" || st.Seq != before+2 {
		t.Errorf("formatted %+v", st)
	}
	plug("")
	if code := helper("eject"); code != storage.ExitNoDisk {
		t.Errorf("eject without a disk: %d", code)
	}
	if _, code, _ := disk.Run(ctx, "", "/usr/bin/sudo", "-n", "/usr/local/sbin/eirl-storage", "attach"); code != storage.ExitUsage {
		t.Errorf("another action: %d", code)
	}
	if _, err := newSimStorage(filepath.Join(dir, "x"), "zfs", log); err == nil {
		t.Error("-disk zfs accepted")
	}
}
