package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	const G = int64(1) << 30
	exfat := Candidate{Name: "sda1", Size: 60 * G, Type: "exfat"}
	ntfs := Candidate{Name: "sda1", Size: 60 * G, Type: "ntfs"}
	uefi := Candidate{Name: "sda2", Size: 1 << 20, Type: "vfat", Label: "UEFI_NTFS"}
	cases := []struct {
		name   string
		size   int64
		parts  []Candidate
		whole  *Candidate
		state  string
		reason string
		pick   string
	}{
		{"exfat partition", 64 * G, []Candidate{exfat}, &Candidate{Name: "sda", Size: 64 * G}, StateReady, "", "sda1"},
		{"windows installer: ntfs + tiny UEFI_NTFS", 64 * G, []Candidate{ntfs, uefi}, nil, StateUnsupported, ReasonFsNTFS, "sda1"},
		{"largest supported wins", 64 * G, []Candidate{{Name: "sda1", Size: 2 * G, Type: "vfat"}, {Name: "sda2", Size: 50 * G, Type: "ext4"}, {Name: "sda3", Size: 10 * G, Type: "exfat"}}, nil, StateReady, "", "sda2"},
		{"supported beats a larger ntfs", 64 * G, []Candidate{{Name: "sda1", Size: 50 * G, Type: "ntfs"}, {Name: "sda2", Size: 10 * G, Type: "exfat"}}, nil, StateReady, "", "sda2"},
		{"small supported is ignored", 64 * G, []Candidate{uefi, {Name: "sda1", Size: 60 * G, Type: "hfsplus"}}, nil, StateUnsupported, ReasonFsUnknown, "sda1"},
		{"only small filesystems", 64 * G, []Candidate{uefi}, nil, StateUnsupported, ReasonTooSmall, ""},
		{"blank disk", 64 * G, nil, &Candidate{Name: "sda", Size: 64 * G}, StateUnformatted, "", ""},
		{"partition table, no filesystem", 64 * G, []Candidate{{Name: "sda1", Size: 60 * G}}, &Candidate{Name: "sda", Size: 64 * G}, StateUnformatted, "", ""},
		{"whole-disk FAT32", 16 * G, nil, &Candidate{Name: "sda", Size: 16 * G, Type: "vfat", Version: "FAT32"}, StateReady, "", "sda"},
		{"superfloppy read as bogus partitions", 16 * G, []Candidate{{Name: "sda1", Size: 8 * G}}, &Candidate{Name: "sda", Size: 16 * G, Type: "vfat"}, StateReady, "", "sda"},
		{"partition fs wins over a whole-disk signature", 16 * G, []Candidate{{Name: "sda1", Size: 8 * G, Type: "iso9660"}}, &Candidate{Name: "sda", Size: 16 * G, Type: "exfat"}, StateUnsupported, ReasonFsUnknown, "sda1"},
		{"tiny disk", 512 << 20, nil, &Candidate{Name: "sda", Size: 512 << 20, Type: "vfat"}, StateUnsupported, ReasonTooSmall, ""},
		{"ext3 is not supported", 64 * G, []Candidate{{Name: "sda1", Size: 60 * G, Type: "ext3"}}, nil, StateUnsupported, ReasonFsUnknown, "sda1"},
	}
	for _, c := range cases {
		d := Decide(c.size, c.parts, c.whole)
		pick := ""
		if d.Pick != nil {
			pick = d.Pick.Name
		}
		if d.State != c.state || d.Reason != c.reason || pick != c.pick {
			t.Errorf("%s: got %s/%s pick %q, want %s/%s pick %q", c.name, d.State, d.Reason, pick, c.state, c.reason, c.pick)
		}
	}
}

func TestParseBlkid(t *testing.T) {
	out := "ID_FS_LABEL=My_Disk\nID_FS_LABEL_ENC=My\\x20Disk\\x2f\\xc3\\xa9\nID_FS_UUID=1234-ABCD\nID_FS_UUID_ENC=1234-ABCD\n" +
		"ID_FS_VERSION=FAT32\nID_FS_TYPE=vfat\nID_FS_USAGE=filesystem\nID_PART_ENTRY_SIZE=2048\n"
	typ, ver, label, uuid := ParseBlkid(out)
	if typ != "vfat" || ver != "FAT32" || label != "My Disk/é" || uuid != "1234-ABCD" {
		t.Fatalf("got %q %q %q %q", typ, ver, label, uuid)
	}
	if FSName(Candidate{Type: typ, Version: ver}) != "fat32" || FSName(Candidate{Type: "vfat", Version: "FAT16"}) != "fat16" {
		t.Fatal("FAT names")
	}
	if typ, _, _, _ := ParseBlkid("ID_FS_TYPE=../../x\n"); typ != "unknown" {
		t.Fatalf("an odd type must not pass through: %q", typ)
	}
	if typ, _, _, _ := ParseBlkid("ID_PART_TABLE_TYPE=dos\n"); typ != "" {
		t.Fatalf("a partition table is not a filesystem: %q", typ)
	}
	if _, _, l, _ := ParseBlkid("ID_FS_TYPE=exfat\nID_FS_LABEL_ENC=a\\x0ab\\x00c\n"); l != "abc" {
		t.Fatalf("control characters must go: %q", l)
	}
}

func TestMountinfo(t *testing.T) {
	mi := "22 1 179:1 / / rw,relatime shared:1 - ext4 /dev/mmcblk0p1 rw,data=ordered\n" +
		"40 22 0:45 / /media/eirl\\040rec rw,nosuid,nodev,relatime shared:30 - fuseblk /dev/sda1 rw,user_id=0,group_id=0,allow_other,blksize=4096\n" +
		"41 22 8:2 / /mnt ro,nosuid,nodev,noexec shared:31 - vfat /dev/sdb2 ro,fmask=0137\n"
	ms := ParseMountinfo(mi)
	if len(ms) != 3 {
		t.Fatalf("got %d mounts", len(ms))
	}
	if ms[1].Mountpoint != "/media/eirl rec" || ms[1].Source != "/dev/sda1" || ms[1].FSType != "fuseblk" || ms[1].DevNum != "0:45" {
		t.Fatalf("got %+v", ms[1])
	}
	if miss := MissingFlags(ms[1]); strings.Join(miss, ",") != "noexec" {
		t.Fatalf("missing %v", miss)
	}
	if MountReadOnly(ms[1]) || !MountReadOnly(ms[2]) {
		t.Fatal("read-only")
	}
}

func TestMountArgs(t *testing.T) {
	for _, c := range []struct{ typ, fstype, opts string }{
		{"exfat", "exfat", "nosuid,nodev,noexec,noatime,uid=998,gid=997,fmask=0137,dmask=0027"},
		{"vfat", "vfat", "nosuid,nodev,noexec,noatime,uid=998,gid=997,fmask=0137,dmask=0027"},
		{"ext4", "ext4", "nosuid,nodev,noexec,noatime,errors=remount-ro"},
		{"ntfs", "", ""},
	} {
		fstype, opts := MountArgs(Candidate{Type: c.typ}, 998, 997)
		if fstype != c.fstype || opts != c.opts {
			t.Errorf("%s: %s %s", c.typ, fstype, opts)
		}
	}
}

func TestRecording(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mixer.json")
	if rec, _ := Recording(p); rec {
		t.Fatal("no file is not recording")
	}
	for body, want := range map[string]bool{
		`{"output":{"mode":"on"}}`:                     false,
		`{"record":{"mode":"off","state":"off"}}`:      false,
		`{"record":{"mode":"on","state":"error"}}`:     true,
		`{"record":{"mode":"off","state":"starting"}}`: true,
		`{"record":{"mode":"off","state":"on"}}`:       true,
		`not json`:                                     false,
	} {
		os.WriteFile(p, []byte(body), 0o644)
		if rec, why := Recording(p); rec != want {
			t.Errorf("%s: %t (%s)", body, rec, why)
		}
	}
	// A link is never followed
	os.Remove(p)
	target := filepath.Join(dir, "elsewhere.json")
	os.WriteFile(target, []byte(`{"record":{"mode":"on"}}`), 0o644)
	os.Symlink(target, p)
	if rec, _ := Recording(p); rec {
		t.Fatal("followed a link")
	}
}

func TestStateFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	st, err := ReadState(p)
	if err != nil || st.State != StateNone || st.Seq != 0 {
		t.Fatalf("missing file: %+v %v", st, err)
	}
	in := State{State: StateReady, Seq: 7, Device: "sda", Partition: "sda1", Model: "DataTraveler 3.0", FS: "exfat",
		Label: "EIRL-REC", UUID: "1234-ABCD", SizeBytes: 62008590336, Mountpoint: "/media/eirl-rec",
		Dir: "/media/eirl-rec/EnhancedIRL", ReadOnly: boolPtr(false), Dirty: boolPtr(false), WriteMBps: 31.2,
		At: "2026-10-24T12:41:03+02:00"}
	if err := writeStateFile(p, in); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode())
	}
	b, _ := os.ReadFile(p)
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, k := range []string{"state", "reason", "seq", "device", "partition", "model", "fs", "label", "uuid", "sizeBytes",
		"mountpoint", "dir", "readOnly", "dirty", "writeMBps", "at"} {
		if _, ok := m[k]; !ok {
			t.Errorf("no %s in %s", k, b)
		}
	}
	if _, ok := m["anotherDisk"]; ok {
		t.Error("anotherDisk must be left out when there is none")
	}
	writeStateFile(p, State{State: StateNone, Seq: 8})
	b, _ = os.ReadFile(p)
	if strings.TrimSpace(string(b)) != `{"state":"none","reason":"","seq":8}` {
		t.Fatalf("none: %s", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("leftovers: %v", ents)
	}
}

func TestPartitionScript(t *testing.T) {
	if PartitionScript(64<<30) != "label: dos\n,,7\n" {
		t.Fatal("mbr")
	}
	if !strings.HasPrefix(PartitionScript(4<<40), "label: gpt\n") {
		t.Fatal("a 4 TiB disk needs GPT")
	}
}

// --- The helper against a fake system ---

type fake struct {
	t       *testing.T
	h       *Helper
	mu      sync.Mutex
	calls   []string
	blkid   map[string]string // device name → blkid output; missing = nothing found
	fsck    int               // exit code of every fsck
	mountRC int
	// fuse: the mount comes up without noexec (as exfat-fuse may), and the bind remount adds it
	fuse       bool
	mountRO    bool
	umountBusy int // how many normal unmounts fail
	stdin      map[string]string
	restart    func() // systemctl restart eirl-storage@…
	startRC    int    // systemctl start eirl-storage-<action>.service
	mainStatus string // systemctl show -p ExecMainStatus
	engine     string // systemctl is-active eirl-engine.service ("" = inactive)
}

func (f *fake) Run(ctx context.Context, stdin, name string, args ...string) (string, int, error) {
	f.mu.Lock()
	call := strings.TrimSpace(filepath.Base(name) + " " + strings.Join(args, " "))
	f.calls = append(f.calls, call)
	if stdin != "" {
		f.stdin[filepath.Base(name)] = stdin
	}
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", -1, err
	}
	h := f.h
	switch name {
	case CmdBlkid:
		out, ok := f.blkid[filepath.Base(args[len(args)-1])]
		if !ok {
			return "", 2, nil
		}
		return out, 0, nil
	case CmdFsckExfat, CmdFsckVfat, CmdE2fsck:
		return "", f.fsck, nil
	case CmdMount:
		if args[0] == "-o" { // remount,bind
			f.setMountOpts("rw,nosuid,nodev,noexec,noatime")
			return "", 0, nil
		}
		if f.mountRC != 0 {
			return "mount: wrong fs type", f.mountRC, nil
		}
		opts := "rw,nosuid,nodev,noexec,noatime"
		if f.fuse {
			opts = "rw,nosuid,nodev,noatime"
		}
		if f.mountRO {
			opts = strings.Replace(opts, "rw", "ro", 1)
		}
		f.addMount(fmt.Sprintf("50 22 0:50 / %s %s shared:40 - %s %s rw", h.MountPoint, opts, args[1], args[4]))
		return "", 0, nil
	case CmdUmount:
		if args[0] != "-l" && f.umountBusy > 0 {
			f.umountBusy--
			return "umount: target is busy", 32, nil
		}
		f.removeMount(h.MountPoint)
		return "", 0, nil
	case CmdSfdisk:
		os.WriteFile(filepath.Join(h.DevDir, "sda1"), nil, 0o644)
		return "", 0, nil
	case CmdSystemctl:
		switch {
		case args[0] == "restart" && f.restart != nil:
			f.restart()
		case args[0] == "start":
			return "Job failed", f.startRC, nil
		case args[0] == "show":
			return f.mainStatus + "\n", 0, nil
		case args[0] == "is-system-running":
			return "running\n", 0, nil
		case args[0] == "is-active":
			if f.engine == "" {
				return "inactive\n", 3, nil
			}
			return f.engine + "\n", 0, nil
		}
		return "", 0, nil
	}
	return "", 0, nil
}

func (f *fake) mountinfoPath() string { return filepath.Join(f.h.ProcDir, "self", "mountinfo") }

func (f *fake) addMount(line string) {
	b, _ := os.ReadFile(f.mountinfoPath())
	os.WriteFile(f.mountinfoPath(), append(b, []byte(line+"\n")...), 0o644)
}

func (f *fake) removeMount(mp string) {
	b, _ := os.ReadFile(f.mountinfoPath())
	ls := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(ls) - 1; i >= 0; i-- {
		if fs := strings.Fields(ls[i]); len(fs) > 4 && fs[4] == mp {
			ls = append(ls[:i], ls[i+1:]...)
			break
		}
	}
	out := strings.Join(ls, "\n")
	if out != "" {
		out += "\n"
	}
	os.WriteFile(f.mountinfoPath(), []byte(out), 0o644)
}

func (f *fake) setMountOpts(opts string) {
	b, _ := os.ReadFile(f.mountinfoPath())
	ls := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i, l := range ls {
		if fs := strings.Fields(l); len(fs) > 5 && fs[4] == f.h.MountPoint {
			fs[5] = opts
			ls[i] = strings.Join(fs, " ")
		}
	}
	os.WriteFile(f.mountinfoPath(), []byte(strings.Join(ls, "\n")+"\n"), 0o644)
}

func (f *fake) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// newFake: a USB disk sda of 64 GiB with the given partitions (name → size in GiB), under xusb's USB bus as on the board.
func newFake(t *testing.T) *fake {
	t.Helper()
	root := t.TempDir()
	h := &Helper{
		SysDir: filepath.Join(root, "sys"), ProcDir: filepath.Join(root, "proc"), DevDir: filepath.Join(root, "dev"),
		RunDir: filepath.Join(root, "run"), MountPoint: filepath.Join(root, "media", "eirl-rec"),
		EngineState: filepath.Join(root, "engine", "mixer.json"),
		EngineUID:   os.Getuid(), EncoderGID: os.Getgid(),
		Log:  func(format string, args ...any) { t.Logf(format, args...) },
		Unit: "eirl-storage@%s.service", Direct: true,
	}
	for _, d := range []string{"sys/block", "proc/self", "dev", "engine"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(h.ProcDir, "self", "mountinfo"), []byte("22 1 179:1 / / rw,relatime shared:1 - ext4 /dev/mmcblk0p1 rw\n"), 0o644)
	f := &fake{t: t, h: h, blkid: map[string]string{}, stdin: map[string]string{}}
	h.Runner = f
	return f
}

// addDisk: a disk in sysfs (sizes in GiB) and its device nodes. usb=false puts it on a SATA host.
func (f *fake) addDisk(name string, sizeGiB int64, usb bool, parts map[string]int64) {
	parent := "devices/platform/70090000.xusb/usb2/2-1/2-1:1.0/host0/target0:0:0/0:0:0:0/block"
	if !usb {
		parent = "devices/platform/ahci/ata1/host0/target0:0:0/0:0:0:0/block"
	}
	real := filepath.Join(f.h.SysDir, parent, name)
	os.MkdirAll(filepath.Join(real, "device"), 0o755)
	os.WriteFile(filepath.Join(real, "size"), fmt.Appendf(nil, "%d\n", sizeGiB<<30/512), 0o644)
	os.WriteFile(filepath.Join(real, "ro"), []byte("0\n"), 0o644)
	os.WriteFile(filepath.Join(real, "dev"), []byte("8:0\n"), 0o644)
	os.WriteFile(filepath.Join(real, "device", "model"), []byte("DataTraveler 3.0\n"), 0o644)
	os.WriteFile(filepath.Join(f.h.DevDir, name), nil, 0o644)
	i := 1
	for p, s := range parts {
		os.MkdirAll(filepath.Join(real, p), 0o755)
		os.WriteFile(filepath.Join(real, p, "partition"), []byte("1\n"), 0o644)
		os.WriteFile(filepath.Join(real, p, "size"), fmt.Appendf(nil, "%d\n", s<<30/512), 0o644)
		os.WriteFile(filepath.Join(real, p, "dev"), fmt.Appendf(nil, "8:%d\n", i), 0o644)
		os.WriteFile(filepath.Join(f.h.DevDir, p), nil, 0o644)
		i++
	}
	rel, _ := filepath.Rel(filepath.Join(f.h.SysDir, "block"), real)
	os.Symlink(rel, filepath.Join(f.h.SysDir, "block", name))
}

func (f *fake) state() State {
	st, err := ReadState(filepath.Join(f.h.RunDir, "state.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func (f *fake) mounted() bool { _, ok := f.h.ours(); return ok }

func TestAttachExfat(t *testing.T) {
	f := newFake(t)
	f.fuse = true
	f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
	f.blkid["sda1"] = "ID_FS_TYPE=exfat\nID_FS_LABEL_ENC=EIRL-REC\nID_FS_UUID_ENC=1234-ABCD\n"
	f.blkid["sda"] = "ID_PART_TABLE_TYPE=dos\n"
	if rc := f.h.Attach("sda"); rc != ExitOK {
		t.Fatalf("rc %d", rc)
	}
	st := f.state()
	if st.State != StateReady || st.Seq != 2 || st.Partition != "sda1" || st.FS != "exfat" || st.Label != "EIRL-REC" ||
		st.UUID != "1234-ABCD" || st.SizeBytes != 60<<30 || st.Model != "DataTraveler 3.0" || st.WriteMBps <= 0 ||
		st.Dir != filepath.Join(f.h.MountPoint, FolderName) || st.Dirty == nil || *st.Dirty || st.ReadOnly == nil || *st.ReadOnly {
		t.Fatalf("state %+v", st)
	}
	want := fmt.Sprintf("mount -t exfat -o nosuid,nodev,noexec,noatime,uid=%d,gid=%d,fmask=0137,dmask=0027 %s %s",
		os.Getuid(), os.Getgid(), filepath.Join(f.h.DevDir, "sda1"), f.h.MountPoint)
	if !f.called(want) {
		t.Fatalf("no %q in %v", want, f.calls)
	}
	if !f.called("fsck.exfat ") || !f.called("mount -o remount,bind,nosuid,nodev,noexec,noatime") {
		t.Fatalf("calls %v", f.calls)
	}
	if fi, err := os.Stat(st.Dir); err != nil || !fi.IsDir() {
		t.Fatal("no recordings folder")
	}
	if _, err := os.Stat(filepath.Join(st.Dir, testName)); !os.IsNotExist(err) {
		t.Fatal("the write test left its file")
	}
}

func TestAttachExt4Dirty(t *testing.T) {
	f := newFake(t)
	f.fsck = 4
	f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
	f.blkid["sda1"] = "ID_FS_TYPE=ext4\n"
	os.MkdirAll(filepath.Join(f.h.MountPoint, FolderName), 0o777) // a folder from another computer
	f.h.Attach("sda")
	st := f.state()
	if st.State != StateReady || st.FS != "ext4" || !*st.Dirty {
		t.Fatalf("state %+v", st)
	}
	if !f.called("e2fsck -n ") || !f.called("mount -t ext4 -o nosuid,nodev,noexec,noatime,errors=remount-ro ") {
		t.Fatalf("calls %v", f.calls)
	}
	if fi, _ := os.Stat(st.Dir); fi.Mode().Perm() != 0o750 || fi.Mode()&fs.ModeSetgid == 0 {
		t.Fatalf("folder mode %v", fi.Mode())
	}
}

func TestAttachFolderIsALink(t *testing.T) {
	f := newFake(t)
	f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
	f.blkid["sda1"] = "ID_FS_TYPE=ext4\n"
	os.MkdirAll(f.h.MountPoint, 0o755)
	os.Symlink("/etc", filepath.Join(f.h.MountPoint, FolderName))
	f.h.Attach("sda")
	if st := f.state(); st.State != StateError || st.Reason != ReasonCheckFailed || st.Mountpoint != "" {
		t.Fatalf("state %+v", st)
	}
	if f.mounted() {
		t.Fatal("left mounted after an error")
	}
}

func TestAttachReports(t *testing.T) {
	cases := []struct {
		name   string
		parts  map[string]int64
		blkid  map[string]string
		state  string
		reason string
		fs     string
	}{
		{"ntfs", map[string]int64{"sda1": 60, "sda2": 0}, map[string]string{"sda1": "ID_FS_TYPE=ntfs\nID_FS_LABEL=Win\n", "sda2": "ID_FS_TYPE=vfat\nID_FS_LABEL=UEFI_NTFS\n"}, StateUnsupported, ReasonFsNTFS, "ntfs"},
		{"blank", nil, nil, StateUnformatted, "", ""},
		{"hfs+", map[string]int64{"sda1": 60}, map[string]string{"sda1": "ID_FS_TYPE=hfsplus\n"}, StateUnsupported, ReasonFsUnknown, "hfsplus"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			f.addDisk("sda", 64, true, c.parts)
			for k, v := range c.blkid {
				f.blkid[k] = v
			}
			if rc := f.h.Attach("sda"); rc != ExitOK {
				t.Fatalf("rc %d", rc)
			}
			st := f.state()
			if st.State != c.state || st.Reason != c.reason || st.FS != c.fs || st.Device != "sda" || st.Dirty != nil {
				t.Fatalf("state %+v", st)
			}
			if f.called("mount") {
				t.Fatal("mounted something it does not use")
			}
		})
	}
}

func TestAttachWholeDiskFAT(t *testing.T) {
	f := newFake(t)
	f.addDisk("sda", 16, true, nil)
	f.blkid["sda"] = "ID_FS_TYPE=vfat\nID_FS_VERSION=FAT32\n"
	f.h.Attach("sda")
	st := f.state()
	if st.State != StateReady || st.Partition != "sda" || st.FS != "fat32" {
		t.Fatalf("state %+v", st)
	}
	if !f.called("fsck.vfat -n ") || !f.called("mount -t vfat ") {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestAttachErrors(t *testing.T) {
	t.Run("write-protected", func(t *testing.T) {
		f := newFake(t)
		f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
		os.WriteFile(filepath.Join(f.h.SysDir, "block", "sda", "ro"), []byte("1\n"), 0o644)
		f.blkid["sda1"] = "ID_FS_TYPE=exfat\n"
		f.h.Attach("sda")
		if st := f.state(); st.State != StateError || st.Reason != ReasonReadOnly || !*st.ReadOnly {
			t.Fatalf("state %+v", st)
		}
	})
	t.Run("mounted read-only", func(t *testing.T) {
		f := newFake(t)
		f.mountRO = true
		f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
		f.blkid["sda1"] = "ID_FS_TYPE=vfat\n"
		f.h.Attach("sda")
		if st := f.state(); st.State != StateError || st.Reason != ReasonReadOnly || f.mounted() {
			t.Fatalf("state %+v", st)
		}
	})
	t.Run("mount fails", func(t *testing.T) {
		f := newFake(t)
		f.mountRC = 32
		f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
		f.blkid["sda1"] = "ID_FS_TYPE=exfat\n"
		f.h.Attach("sda")
		if st := f.state(); st.State != StateError || st.Reason != ReasonMountFailed || st.Partition != "sda1" {
			t.Fatalf("state %+v", st)
		}
	})
}

func TestAttachIgnored(t *testing.T) {
	t.Run("not usb", func(t *testing.T) {
		f := newFake(t)
		f.addDisk("sda", 64, false, map[string]int64{"sda1": 60})
		f.blkid["sda1"] = "ID_FS_TYPE=ext4\n"
		f.h.Attach("sda")
		if st := f.state(); st.State != StateNone || st.Seq != 0 {
			t.Fatalf("state %+v", st)
		}
	})
	t.Run("boot device", func(t *testing.T) {
		f := newFake(t)
		f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
		f.blkid["sda1"] = "ID_FS_TYPE=ext4\n"
		os.WriteFile(f.mountinfoPath(), []byte("22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/root rw\n"), 0o644)
		f.h.Attach("sda")
		if st := f.state(); st.State != StateNone || f.called("mount") {
			t.Fatalf("state %+v, calls %v", st, f.calls)
		}
	})
	t.Run("bad name", func(t *testing.T) {
		f := newFake(t)
		for _, n := range []string{"sda1", "mmcblk0", "../sda", "sdaa", ""} {
			if rc := f.h.Attach(n); rc != ExitUsage {
				t.Fatalf("%q: rc %d", n, rc)
			}
		}
	})
}

func TestAnotherDiskAndTakeover(t *testing.T) {
	f := newFake(t)
	f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
	f.addDisk("sdb", 32, true, map[string]int64{"sdb1": 30})
	f.blkid["sda1"] = "ID_FS_TYPE=exfat\n"
	f.blkid["sdb1"] = "ID_FS_TYPE=exfat\n"
	f.h.Attach("sda")
	seq := f.state().Seq
	f.h.Attach("sdb")
	st := f.state()
	// The same seq: to the engine a new one is a new mount, and a recording on sda would stop
	if st.State != StateReady || st.Device != "sda" || st.AnotherDisk != "sdb" || st.Seq != seq {
		t.Fatalf("a second disk must not replace the first: %+v (seq was %d)", st, seq)
	}
	// sda pulled out: none, and sdb's unit is restarted to take its place
	os.Remove(filepath.Join(f.h.SysDir, "block", "sda"))
	f.h.Detach("sda")
	if st := f.state(); st.State != StateNone || st.AnotherDisk != "" {
		t.Fatalf("state %+v", st)
	}
	if !f.called("umount -l "+f.h.MountPoint) || !f.called("systemctl --no-block restart eirl-storage@sdb.service") {
		t.Fatalf("calls %v", f.calls)
	}
	if f.mounted() {
		t.Fatal("still mounted")
	}
}

func TestAnotherDiskGone(t *testing.T) {
	f := newFake(t)
	f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
	f.addDisk("sdb", 32, true, nil)
	f.blkid["sda1"] = "ID_FS_TYPE=exfat\n"
	f.h.Attach("sda")
	seq := f.state().Seq
	f.h.Attach("sdb")
	os.Remove(filepath.Join(f.h.SysDir, "block", "sdb"))
	f.h.Detach("sdb")
	if st := f.state(); st.State != StateReady || st.AnotherDisk != "" || !f.mounted() || st.Seq != seq {
		t.Fatalf("state %+v (seq was %d)", st, seq)
	}
}

func TestDetachIdempotent(t *testing.T) {
	f := newFake(t)
	f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
	f.blkid["sda1"] = "ID_FS_TYPE=exfat\n"
	f.h.Attach("sda")
	os.Remove(filepath.Join(f.h.SysDir, "block", "sda"))
	f.h.Detach("sda") // ExecStop
	seq := f.state().Seq
	f.h.Detach("sda") // ExecStopPost
	if st := f.state(); st.State != StateNone || st.Seq != seq {
		t.Fatalf("state %+v", st)
	}
}

func TestAttachCancelled(t *testing.T) {
	f := newFake(t)
	f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
	f.blkid["sda1"] = "ID_FS_TYPE=exfat\n"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.h.Ctx = ctx
	if rc := f.h.Attach("sda"); rc != ExitFailed {
		t.Fatalf("rc %d", rc)
	}
	if st := f.state(); st.State != StateNone || f.mounted() {
		t.Fatalf("state %+v", st)
	}
}

func readyFake(t *testing.T) *fake {
	f := newFake(t)
	f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
	f.blkid["sda1"] = "ID_FS_TYPE=exfat\n"
	f.h.Attach("sda")
	if !f.mounted() {
		t.Fatal("not mounted")
	}
	f.calls = nil
	return f
}

func TestEject(t *testing.T) {
	f := readyFake(t)
	os.WriteFile(f.h.EngineState, []byte(`{"record":{"mode":"on","state":"on"}}`), 0o644)
	if rc := f.h.Eject(); rc != ExitRecording || !f.mounted() || f.state().State != StateReady {
		t.Fatalf("rc %d while recording", rc)
	}
	os.WriteFile(f.h.EngineState, []byte(`{"record":{"mode":"off","state":"off"}}`), 0o644)
	if rc := f.h.Eject(); rc != ExitOK {
		t.Fatalf("rc %d", rc)
	}
	st := f.state()
	if st.State != StateEjected || st.Device != "sda" || st.Mountpoint != "" || f.mounted() {
		t.Fatalf("state %+v", st)
	}
	if f.calls[0] != "sync" || !f.called("umount "+f.h.MountPoint) || f.called("umount -l") {
		t.Fatalf("calls %v", f.calls)
	}
	if rc := f.h.Eject(); rc != ExitOK {
		t.Fatalf("again: rc %d", rc)
	}
	// Pulled out after the eject: none
	os.Remove(filepath.Join(f.h.SysDir, "block", "sda"))
	f.h.Detach("sda")
	if f.state().State != StateNone {
		t.Fatal("not none")
	}
	if rc := f.h.Eject(); rc != ExitNoDisk {
		t.Fatalf("no disk: rc %d", rc)
	}
}

func TestEjectBusy(t *testing.T) {
	f := readyFake(t)
	f.umountBusy = 100
	if rc := f.h.Eject(); rc != ExitFailed {
		t.Fatalf("rc %d", rc)
	}
	if st := f.state(); st.State != StateReady || !f.mounted() || f.called("umount -l") {
		t.Fatalf("an eject that cannot unmount must change nothing: %+v", st)
	}
}

func TestLockBusy(t *testing.T) {
	f := readyFake(t)
	old := LockWaitAction
	LockWaitAction = 300e6
	defer func() { LockWaitAction = old }()
	unlock, err := f.h.lock(0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	g := *f.h
	if rc := g.Eject(); rc != ExitBusy {
		t.Fatalf("rc %d", rc)
	}
}

func TestFormat(t *testing.T) {
	f := readyFake(t)
	f.restart = func() { // the disk's unit: detach, then attach of what is there now
		f.h.Detach("sda")
		f.blkid["sda1"] = "ID_FS_TYPE=exfat\nID_FS_LABEL=EIRL-REC\n"
		g := *f.h
		g.Attach("sda")
	}
	seq := f.state().Seq
	os.WriteFile(f.h.EngineState, []byte(`{"record":{"mode":"on"}}`), 0o644)
	if rc := f.h.Format(seq); rc != ExitRecording || f.called("wipefs") {
		t.Fatalf("rc %d while recording", rc)
	}
	os.Remove(f.h.EngineState)
	// Not the disk the user confirmed (another seq): nothing is touched
	if rc := f.h.Format(seq + 1); rc != ExitChanged || f.called("wipefs") || f.called("umount") {
		t.Fatalf("another seq: rc %d, calls %v", rc, f.calls)
	}
	if rc := f.h.Format(seq); rc != ExitOK {
		t.Fatalf("rc %d, calls %v", rc, f.calls)
	}
	dev := func(n string) string { return filepath.Join(f.h.DevDir, n) }
	want := []string{
		"wipefs -a " + dev("sda1"),
		"wipefs -a " + dev("sda"),
		"sfdisk -q " + dev("sda"),
		"udevadm settle",
		"mkfs.exfat -n EIRL-REC " + dev("sda1"),
		"systemctl restart eirl-storage@sda.service",
	}
	i := 0
	for _, c := range f.calls {
		if i < len(want) && strings.HasPrefix(c, want[i]) {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("calls %v, want in order %v", f.calls, want)
	}
	if f.stdin["sfdisk"] != "label: dos\n,,7\n" {
		t.Fatalf("sfdisk script %q", f.stdin["sfdisk"])
	}
	if st := f.state(); st.State != StateReady || st.Label != "EIRL-REC" {
		t.Fatalf("state %+v", st)
	}
}

// Formatting with a second disk plugged in: the formatted disk's unit restarts, and the second disk does not take its
// place (it is not gone): the first one is mounted again, formatted.
func TestFormatWithAnotherDisk(t *testing.T) {
	f := readyFake(t)
	f.addDisk("sdb", 32, true, map[string]int64{"sdb1": 30})
	f.blkid["sdb1"] = "ID_FS_TYPE=exfat\n"
	f.h.Attach("sdb")
	f.restart = func() {
		f.h.Detach("sda")
		f.blkid["sda1"] = "ID_FS_TYPE=exfat\nID_FS_LABEL=EIRL-REC\n"
		g := *f.h
		g.Attach("sda")
	}
	f.calls = nil
	if rc := f.h.Format(f.state().Seq); rc != ExitOK {
		t.Fatalf("rc %d, calls %v", rc, f.calls)
	}
	if f.called("restart eirl-storage@sdb.service") {
		t.Fatalf("the second disk took over a disk that was only restarting: %v", f.calls)
	}
	if st := f.state(); st.State != StateReady || st.Device != "sda" || st.Label != "EIRL-REC" {
		t.Fatalf("state %+v", st)
	}
}

// With the disk still there (a shutdown) and the engine recording on it, detach waits for the recording to end, at
// most RecordingDrain; a stopped engine is not waited for, whatever its state file says.
func TestDetachDrainsARecording(t *testing.T) {
	old := RecordingDrain
	RecordingDrain = 400 * time.Millisecond
	defer func() { RecordingDrain = old }()
	f := readyFake(t)
	os.WriteFile(f.h.EngineState, []byte(`{"record":{"mode":"on","state":"on"}}`), 0o644)
	f.engine = "deactivating"
	go func() {
		time.Sleep(150 * time.Millisecond)
		os.WriteFile(f.h.EngineState, []byte(`{"record":{"mode":"off","state":"off"}}`), 0o644)
	}()
	start := time.Now()
	f.h.Detach("sda")
	if d := time.Since(start); d < 100*time.Millisecond || d > 380*time.Millisecond {
		t.Fatalf("detach took %v: it should wait for the recording to end", d)
	}
	if f.state().State != StateNone || f.mounted() {
		t.Fatal("not detached")
	}
	// A stopped engine (its file still says recording): no wait
	f = readyFake(t)
	os.WriteFile(f.h.EngineState, []byte(`{"record":{"mode":"on","state":"on"}}`), 0o644)
	start = time.Now()
	f.h.Detach("sda")
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("waited %v for a stopped engine", d)
	}
}

func TestRelay(t *testing.T) {
	f := newFake(t)
	f.h.Direct = false
	if rc := f.h.Eject(); rc != ExitOK || !f.called("systemctl start eirl-storage-eject.service") {
		t.Fatalf("rc %d calls %v", rc, f.calls)
	}
	f.startRC, f.mainStatus = 1, "3"
	if rc := f.h.Format(7); rc != ExitRecording || !f.called("systemctl start eirl-storage-format@7.service") {
		t.Fatalf("rc %d", rc)
	}
	f.mainStatus = "5" // the disk changed
	if rc := f.h.Format(7); rc != ExitChanged {
		t.Fatalf("rc %d", rc)
	}
	f.mainStatus = "203" // exec failed
	if rc := f.h.Format(7); rc != ExitFailed {
		t.Fatalf("rc %d", rc)
	}
	f.calls = nil
	os.WriteFile(f.h.EngineState, []byte(`{"record":{"mode":"on"}}`), 0o644)
	if rc := f.h.Eject(); rc != ExitRecording || f.called("systemctl") {
		t.Fatalf("recording: rc %d calls %v", rc, f.calls)
	}
}

func TestHostNamespace(t *testing.T) {
	f := newFake(t)
	os.MkdirAll(filepath.Join(f.h.ProcDir, "self", "ns"), 0o755)
	os.MkdirAll(filepath.Join(f.h.ProcDir, "1", "ns"), 0o755)
	os.Symlink("mnt:[4026531840]", filepath.Join(f.h.ProcDir, "1", "ns", "mnt"))
	os.Symlink("mnt:[4026532001]", filepath.Join(f.h.ProcDir, "self", "ns", "mnt"))
	f.addDisk("sda", 64, true, map[string]int64{"sda1": 60})
	f.blkid["sda1"] = "ID_FS_TYPE=exfat\n"
	if rc := f.h.Attach("sda"); rc != ExitFailed || f.called("mount") {
		t.Fatalf("attached from a sandbox's namespace: rc %d", rc)
	}
	if rc := f.h.Eject(); rc != ExitFailed {
		t.Fatalf("ejected from a sandbox's namespace: rc %d", rc)
	}
}
