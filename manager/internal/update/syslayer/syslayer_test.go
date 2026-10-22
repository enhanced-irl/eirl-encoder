package syslayer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const testKernel = BaseKernel

// fakeModule writes a minimal ELF relocatable object with a .modinfo section (what moduleVermagic reads).
func fakeModule(t *testing.T, p, vermagic, body string) {
	t.Helper()
	modinfo := []byte("license=GPL\x00vermagic=" + vermagic + " SMP preempt mod_unload aarch64\x00x=" + body + "\x00")
	shstr := []byte("\x00.modinfo\x00.shstrtab\x00")
	var b bytes.Buffer
	hdr := elf.Header64{Type: uint16(elf.ET_REL), Machine: uint16(elf.EM_AARCH64), Version: 1, Ehsize: 64, Shentsize: 64, Shnum: 3, Shstrndx: 2}
	copy(hdr.Ident[:], []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	modOff := uint64(64)
	strOff := modOff + uint64(len(modinfo))
	shOff := (strOff + uint64(len(shstr)) + 7) &^ 7
	hdr.Shoff = shOff
	binary.Write(&b, binary.LittleEndian, hdr)
	b.Write(modinfo)
	b.Write(shstr)
	for uint64(b.Len()) < shOff {
		b.WriteByte(0)
	}
	secs := []elf.Section64{{},
		{Name: 1, Type: uint32(elf.SHT_PROGBITS), Off: modOff, Size: uint64(len(modinfo)), Addralign: 1},
		{Name: 10, Type: uint32(elf.SHT_STRTAB), Off: strOff, Size: uint64(len(shstr)), Addralign: 1}}
	for _, s := range secs {
		binary.Write(&b, binary.LittleEndian, s)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func shaOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func write(t *testing.T, p, s string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
}

// --- a fake board: dpkg, apt, systemctl, depmod and groupadd acting on a root directory ---------------------

type fakeExec struct {
	t      *testing.T
	root   string
	calls  []string
	failOn string // a call whose text contains this fails
	failIf func(call string) bool
	active map[string]bool // units "systemctl is-active" reports active
	before func(call string)
}

type pkg struct{ name, version, status string }

func (f *fakeExec) status() []pkg {
	b, _ := os.ReadFile(filepath.Join(f.root, "var/lib/dpkg/status"))
	var out []pkg
	for _, rec := range strings.Split(strings.TrimSpace(string(b)), "\n\n") {
		var p pkg
		for _, line := range strings.Split(rec, "\n") {
			k, v, _ := strings.Cut(line, ": ")
			switch k {
			case "Package":
				p.name = v
			case "Version":
				p.version = v
			case "Status":
				p.status = v
			}
		}
		if p.name != "" {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeExec) setStatus(ps []pkg) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].name < ps[j].name })
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "Package: %s\nStatus: %s\nArchitecture: arm64\nVersion: %s\n\n", p.name, p.status, p.version)
	}
	write(f.t, filepath.Join(f.root, "var/lib/dpkg/status"), b.String(), 0o644)
}

func debName(p string) (string, string) {
	base := strings.TrimSuffix(filepath.Base(p), ".deb")
	parts := strings.Split(base, "_")
	return parts[0], strings.ReplaceAll(parts[1], "%3a", ":")
}

func (f *fakeExec) Run(_ context.Context, name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.before != nil {
		f.before(call)
	}
	if f.failOn != "" && strings.Contains(call, f.failOn) || f.failIf != nil && f.failIf(call) {
		return "boom", errors.New("injected failure: " + call)
	}
	sys := filepath.Join(f.root, "etc/systemd/system")
	switch name {
	case "dpkg-deb":
		n, v := debName(args[1])
		return fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: arm64\n", n, v), nil
	case "apt-get":
		sim := false
		var debs []string
		for i, a := range args {
			if a == "-s" {
				sim = true
			}
			if a == "install" {
				debs = args[i+1:]
			}
		}
		if sim {
			return "Inst …", nil
		}
		st := f.status()
		for _, d := range debs {
			n, v := debName(d)
			found := false
			for i := range st {
				if st[i].name == n {
					st[i].version, st[i].status, found = v, "install ok installed", true
				}
			}
			if !found {
				st = append(st, pkg{n, v, "install ok installed"})
			}
		}
		f.setStatus(st)
	case "dpkg":
		switch args[0] {
		case "--audit":
			for _, p := range f.status() {
				if p.status != "install ok installed" {
					return "The following packages are only half configured:\n " + p.name + "\n", nil
				}
			}
		case "--configure":
			st := f.status()
			for i := range st {
				if st[i].status == "install ok half-configured" || st[i].status == "install ok unpacked" {
					st[i].status = "install ok installed"
				}
			}
			f.setStatus(st)
		case "--purge":
			drop := map[string]bool{}
			for _, n := range args[1:] {
				drop[n] = true
			}
			var keep []pkg
			for _, p := range f.status() {
				if !drop[p.name] {
					keep = append(keep, p)
				}
			}
			f.setStatus(keep)
		case "--force-confdef":
			n, v := debName(args[len(args)-1])
			st := f.status()
			for i := range st {
				if st[i].name == n {
					st[i].version, st[i].status = v, "install ok installed"
				}
			}
			f.setStatus(st)
		}
	case "systemctl":
		if strings.HasPrefix(args[0], "--root=") {
			args = args[1:]
		}
		switch args[0] {
		case "mask":
			os.MkdirAll(sys, 0o755)
			os.Symlink("/dev/null", filepath.Join(sys, args[1]))
		case "unmask":
			os.Remove(filepath.Join(sys, args[1]))
		case "enable":
			os.MkdirAll(filepath.Join(sys, "multi-user.target.wants"), 0o755)
			os.Symlink("/lib/systemd/system/"+args[1], filepath.Join(sys, "multi-user.target.wants", args[1]))
		case "disable":
			os.Remove(filepath.Join(sys, "multi-user.target.wants", args[1]))
		case "is-active":
			if f.active[args[1]] {
				return "active\n", nil
			}
			return "inactive\n", errors.New("inactive")
		}
	case "groupadd":
		g := args[len(args)-1]
		p := filepath.Join(f.root, "etc/group")
		b, _ := os.ReadFile(p)
		write(f.t, p, string(b)+g+":x:991:\n", 0o644)
	}
	return "", nil
}

// newBoard: a root that looks like the encoder's base (L4T, kernel, dpkg with the base packages, a unit).
func newBoard(t *testing.T) (string, *fakeExec) {
	root := t.TempDir()
	write(t, filepath.Join(root, "etc/nv_tegra_release"), "# R32 (release), REVISION: 7.6, GCID: 38171779, BOARD: t210ref\n", 0o644)
	os.MkdirAll(filepath.Join(root, "lib/modules", testKernel), 0o755)
	write(t, filepath.Join(root, "etc/group"), "root:x:0:\n", 0o644)
	os.MkdirAll(filepath.Join(root, "etc/systemd/system/multi-user.target.wants"), 0o755)
	os.Symlink("/lib/systemd/system/ModemManager.service", filepath.Join(root, "etc/systemd/system/multi-user.target.wants/ModemManager.service"))
	fx := &fakeExec{t: t, root: root, active: map[string]bool{}}
	fx.setStatus([]pkg{{"dpkg", "1.19.0.5ubuntu2.4", "install ok installed"}, {"bash", "4.4.18-2ubuntu1.3", "install ok installed"},
		{"libplist3", "2.0.0-2ubuntu1", "install ok installed"}})
	return root, fx
}

// newLayer writes a layer directory: a module, a firmware file, a drop-in, two packages, units and a group.
func newLayer(t *testing.T, release, modBody string) (string, *Layer) {
	dir := t.TempDir()
	modPath := "/lib/modules/" + testKernel + "/updates/eirl-modems/ipheth.ko"
	fakeModule(t, filepath.Join(dir, "files"+modPath), testKernel, modBody)
	modSum, _ := sha256File(filepath.Join(dir, "files"+modPath))
	write(t, filepath.Join(dir, "files/lib/firmware/rtl_bt/rtl8761bu_fw.bin"), "firmware", 0o644)
	write(t, filepath.Join(dir, "files/etc/systemd/system/usbmuxd.service.d/90-eirl.conf"), "[Service]\nUMask=0077\n", 0o644)
	l := &Layer{V: Format, Release: release, Requires: Requires{L4T: BaseL4T, Kernel: testKernel, Arch: "arm64"},
		Base: []Pkg{{"dpkg", "1.19.0.5ubuntu2.4"}, {"bash", "4.4.18-2ubuntu1.3"}, {"libplist3", "2.0.0-2ubuntu1"}},
		Files: []File{
			{Path: modPath, Src: "files" + modPath, SHA256: modSum, Mode: "0644", Area: "uplinks", Module: "ipheth"},
			{Path: "/lib/firmware/rtl_bt/rtl8761bu_fw.bin", Src: "files/lib/firmware/rtl_bt/rtl8761bu_fw.bin",
				SHA256: shaOf([]byte("firmware")), Mode: "0644", Area: "bluetooth", UsedBy: "btrtl"},
			{Path: "/etc/systemd/system/usbmuxd.service.d/90-eirl.conf", Src: "files/etc/systemd/system/usbmuxd.service.d/90-eirl.conf",
				SHA256: shaOf([]byte("[Service]\nUMask=0077\n")), Mode: "0644", Area: "uplinks", UsedBy: "usbmuxd.service"},
		},
		Units:  []Unit{{"ModemManager.service", Disabled}, {"apt-daily.timer", Masked}},
		Groups: []string{"debug"},
	}
	for _, p := range []struct{ name, version string }{{"usbmuxd", "1.1.0-2ubuntu0.1"}, {"libusbmuxd4", "1.1.0~git20171206.c724e70f-0.1"}} {
		f := "debs/" + p.name + "_" + p.version + "_arm64.deb"
		body := []byte("deb " + p.name)
		write(t, filepath.Join(dir, f), string(body), 0o644)
		l.Packages = append(l.Packages, Package{Name: p.name, Version: p.version, Arch: "arm64", File: f, SHA256: shaOf(body),
			Size: int64(len(body)), InstalledSize: 96, Source: p.name, SourceVersion: p.version})
	}
	writeLayer(t, dir, l)
	return dir, l
}

func writeLayer(t *testing.T, dir string, l *Layer) {
	b, _ := json.MarshalIndent(l, "", "  ")
	write(t, filepath.Join(dir, LayerFile), string(b), 0o644)
}

func applier(root string, fx *fakeExec) *Applier {
	return &Applier{Root: root, Exec: fx, Proc: "", Sys: filepath.Join(root, "sys"), LockWait: time.Second,
		MinFree: 1, Now: func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }}
}

func installed(fx *fakeExec) map[string]string {
	m := map[string]string{}
	for _, p := range fx.status() {
		if strings.HasSuffix(p.status, " installed") {
			m[p.name] = p.version
		}
	}
	return m
}

func TestApplyFromScratchAndAgain(t *testing.T) {
	root, fx := newBoard(t)
	dir, l := newLayer(t, "0.10.0", "a")
	a := applier(root, fx)
	res, err := a.Apply(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("nothing changed")
	}
	for _, f := range l.Files {
		if sum, _ := fileSHA(filepath.Join(root, f.Path)); sum != f.SHA256 {
			t.Errorf("%s not installed", f.Path)
		}
	}
	got := installed(fx)
	if got["usbmuxd"] != "1.1.0-2ubuntu0.1" || got["libusbmuxd4"] == "" {
		t.Errorf("packages: %v", got)
	}
	if s := unitState(root, "apt-daily.timer"); s != Masked {
		t.Errorf("apt-daily.timer %s", s)
	}
	if s := unitState(root, "ModemManager.service"); s != Disabled {
		t.Errorf("ModemManager %s", s)
	}
	if !groupExists(root, "debug") {
		t.Error("no group debug")
	}
	if _, err := os.Stat(filepath.Join(root, DefaultStateDir, markerFile)); err == nil {
		t.Error("the marker stayed")
	}
	st := ReadState(filepath.Join(root, DefaultStateDir))
	if st.Release != "0.10.0" || st.Packages["usbmuxd"] != "1.1.0-2ubuntu0.1" || len(st.Files) != 3 || st.Error != "" {
		t.Errorf("state: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(root, "usr/sbin/policy-rc.d")); err == nil {
		t.Error("policy-rc.d left behind")
	}
	var depmod, simulated bool
	for _, c := range fx.calls {
		depmod = depmod || strings.HasPrefix(c, "depmod -b "+root+" -a "+testKernel)
		simulated = simulated || strings.HasPrefix(c, "apt-get") && strings.Contains(c, " -s ")
	}
	if !depmod || !simulated {
		t.Errorf("depmod %v, apt simulation %v: %v", depmod, simulated, fx.calls)
	}
	// Idempotent: the second apply changes nothing and runs nothing that changes the board
	fx.calls = nil
	res, err = a.Apply(context.Background(), dir)
	if err != nil || res.Changed {
		t.Fatalf("second apply: changed %v, %v", res != nil && res.Changed, err)
	}
	for _, c := range fx.calls {
		if c != "dpkg --audit" { // read only
			t.Errorf("second apply ran %q", c)
		}
	}
}

func TestApplyFailureUndoesEverything(t *testing.T) {
	root, fx := newBoard(t)
	dir, l := newLayer(t, "0.10.0", "a")
	// An older drop-in is there (the test board's, by hand): it must come back
	write(t, filepath.Join(root, "etc/systemd/system/usbmuxd.service.d/90-eirl.conf"), "[Service]\nUMask=0022\n", 0o640)
	fx.failOn = "systemctl --root=" + root + " mask apt-daily.timer" // after the files and the packages
	_, err := applier(root, fx).Apply(context.Background(), dir)
	var f *Failure
	if !errors.As(err, &f) || f.Code != "apply_failed" {
		t.Fatalf("want apply_failed, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/systemd/system/usbmuxd.service.d/90-eirl.conf")); string(b) != "[Service]\nUMask=0022\n" {
		t.Errorf("drop-in not restored: %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(root, "etc/systemd/system/usbmuxd.service.d/90-eirl.conf")); fi.Mode().Perm() != 0o640 {
		t.Errorf("drop-in mode %v", fi.Mode().Perm())
	}
	for _, f := range l.Files[:2] {
		if _, err := os.Stat(filepath.Join(root, f.Path)); err == nil {
			t.Errorf("%s stayed", f.Path)
		}
	}
	got := installed(fx)
	if _, ok := got["usbmuxd"]; ok {
		t.Errorf("a new package stayed: %v", got)
	}
	if s := unitState(root, "ModemManager.service"); s != Enabled {
		t.Errorf("ModemManager not restored: %s", s)
	}
	st := ReadState(filepath.Join(root, DefaultStateDir))
	if !strings.HasPrefix(st.Error, "apply_failed") || st.Failed != "0.10.0" || st.Release != "" {
		t.Errorf("state: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(root, DefaultStateDir, markerFile)); err == nil {
		t.Error("the marker stayed after undoing")
	}
}

func TestPowerCutIsUndoneAtBoot(t *testing.T) {
	root, fx := newBoard(t)
	dir, _ := newLayer(t, "0.10.0", "a")
	// The "power cut": the run stops right in the middle of installing the packages
	cut := errors.New("power cut")
	fx.before = func(call string) {
		if strings.HasPrefix(call, "apt-get") && !strings.Contains(call, " -s ") {
			// dpkg had unpacked usbmuxd when the power went
			fx.setStatus(append(fx.status(), pkg{"usbmuxd", "1.1.0-2ubuntu0.1", "install ok unpacked"}))
			panic(cut)
		}
	}
	func() {
		defer func() {
			if r := recover(); r != cut {
				panic(r)
			}
		}()
		applier(root, fx).Apply(context.Background(), dir)
	}()
	fx.before = nil
	a := applier(root, fx)
	if !a.Interrupted() {
		t.Fatal("no marker after the cut")
	}
	was, err := a.Recover(context.Background())
	if !was || err != nil {
		t.Fatalf("recover: %v %v", was, err)
	}
	for _, p := range fx.status() {
		if p.name == "usbmuxd" {
			t.Errorf("the half-installed package stayed: %+v", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "lib/modules", testKernel, "updates/eirl-modems/ipheth.ko")); err == nil {
		t.Error("the module stayed")
	}
	st := ReadState(filepath.Join(root, DefaultStateDir))
	if !strings.HasPrefix(st.Error, "interrupted") {
		t.Errorf("state: %+v", st)
	}
	// The next apply goes through
	if _, err := applier(root, fx).Apply(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
}

func TestPendingOnlyForWhatIsInUse(t *testing.T) {
	root, fx := newBoard(t)
	dir, _ := newLayer(t, "0.10.0", "a")
	os.MkdirAll(filepath.Join(root, "sys/module/btrtl"), 0o755) // the Bluetooth adapter is plugged in
	fx.active["usbmuxd.service"] = true                         // an iPhone is tethered
	res, err := applier(root, fx).Apply(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"bluetooth/btrtl": true, "uplinks/usbmuxd.service": true}
	for _, p := range res.Pending {
		if !want[p.Area+"/"+p.What] {
			t.Errorf("unexpected pending %+v", p)
		}
		delete(want, p.Area+"/"+p.What)
	}
	if len(want) > 0 {
		t.Errorf("missing pending %v (got %+v)", want, res.Pending)
	}
	// A new module that is not loaded (ipheth, no iPhone ever) acts at once: not pending
}

func TestRetiresWhatTheLayerNoLongerLists(t *testing.T) {
	root, fx := newBoard(t)
	dir, l := newLayer(t, "0.10.0", "a")
	a := applier(root, fx)
	if _, err := a.Apply(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	// 0.11.0 drops libusbmuxd4 and the drop-in, and changes the module
	dir2, _ := newLayer(t, "0.11.0", "b")
	l2, _ := LoadLayer(dir2)
	l2.Packages = l2.Packages[:1]
	l2.Files = append(l2.Files[:1:1], l2.Files[1])
	writeLayer(t, dir2, l2)
	res, err := a.Apply(context.Background(), dir2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := installed(fx)["libusbmuxd4"]; ok {
		t.Error("libusbmuxd4 not retired")
	}
	if _, err := os.Stat(filepath.Join(root, l.Files[2].Path)); err == nil {
		t.Error("the drop-in the layer no longer lists stayed")
	}
	if sum, _ := fileSHA(filepath.Join(root, l2.Files[0].Path)); sum != l2.Files[0].SHA256 {
		t.Error("the module was not replaced")
	}
	if len(res.Plan.Retire) != 1 || res.Plan.Retire[0] != "libusbmuxd4" {
		t.Errorf("retire %v", res.Plan.Retire)
	}
	// A base package is never retired
	for _, c := range fx.calls {
		if strings.Contains(c, "--purge") && strings.Contains(c, "libplist3") {
			t.Errorf("purged a base package: %s", c)
		}
	}
}

func TestDriftIsReportedNotTouched(t *testing.T) {
	root, fx := newBoard(t)
	dir, _ := newLayer(t, "0.10.0", "a")
	fx.setStatus(append(fx.status(), pkg{"libimobiledevice-utils", "1.2.1", "install ok installed"},
		pkg{"usbmuxd", "1.2.0", "install ok installed"})) // installed by hand, and newer than the layer's
	a := applier(root, fx)
	res, err := a.Apply(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plan.Drift.Extra) != 1 || !strings.HasPrefix(res.Plan.Drift.Extra[0], "libimobiledevice-utils") ||
		len(res.Plan.Drift.Newer) != 1 {
		t.Errorf("drift %+v", res.Plan.Drift)
	}
	if installed(fx)["usbmuxd"] != "1.2.0" {
		t.Error("downgraded usbmuxd")
	}
	st := ReadState(filepath.Join(root, DefaultStateDir))
	if st.Drift == nil || len(st.Drift.Extra) != 1 {
		t.Errorf("state drift %+v", st.Drift)
	}
}

func TestSkippedOnAnotherBase(t *testing.T) {
	root, fx := newBoard(t)
	write(t, filepath.Join(root, "etc/nv_tegra_release"), "# R35 (release), REVISION: 4.1\n", 0o644)
	dir, _ := newLayer(t, "0.10.0", "a")
	res, err := applier(root, fx).Apply(context.Background(), dir)
	if err != nil || !strings.Contains(res.Skipped, "L4T") {
		t.Fatalf("want skipped, got %+v %v", res, err)
	}
	if len(fx.calls) != 0 {
		t.Errorf("ran %v", fx.calls)
	}
}

func TestPreflightRefusesBadPayloads(t *testing.T) {
	cases := map[string]func(dir string, l *Layer){
		"bad_package": func(dir string, l *Layer) { write(t, filepath.Join(dir, l.Packages[0].File), "tampered deb", 0o644) },
		"bad_file":    func(dir string, l *Layer) { write(t, filepath.Join(dir, l.Files[1].Src), "tampered", 0o644) },
		"bad_file ": func(dir string, l *Layer) { // a module built for another kernel
			fakeModule(t, filepath.Join(dir, l.Files[0].Src), "4.9.253-tegra", "a")
			l.Files[0].SHA256, _ = sha256File(filepath.Join(dir, l.Files[0].Src))
			writeLayer(t, dir, l)
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			root, fx := newBoard(t)
			dir, l := newLayer(t, "0.10.0", "a")
			spoil(dir, l)
			_, err := applier(root, fx).Apply(context.Background(), dir)
			var f *Failure
			if !errors.As(err, &f) || f.Code != strings.TrimSpace(name) {
				t.Fatalf("want %s, got %v", name, err)
			}
			for _, c := range fx.calls {
				if strings.HasPrefix(c, "apt-get") && !strings.Contains(c, " -s ") || strings.HasPrefix(c, "systemctl") {
					t.Errorf("changed the board after a refusal: %s", c)
				}
			}
			if len(installed(fx)) != 3 {
				t.Errorf("packages changed: %v", installed(fx))
			}
		})
	}
}

func TestValidateAllowLists(t *testing.T) {
	_, l := newLayer(t, "0.10.0", "a")
	if err := l.Validate(); err != nil {
		t.Fatalf("a good layer: %v", err)
	}
	bad := map[string]func(l *Layer){
		"/boot": func(l *Layer) {
			l.Files[1].Path = "/boot/extlinux/extlinux.conf"
			l.Files[1].Src = "files/boot/extlinux/extlinux.conf"
		},
		"sudoers":    func(l *Layer) { l.Files[1].Path = "/etc/sudoers.d/x"; l.Files[1].Src = "files/etc/sudoers.d/x" },
		"app config": func(l *Layer) { l.Files[1].Path = "/etc/eirl-encoder/x"; l.Files[1].Src = "files/etc/eirl-encoder/x" },
		"dotdot": func(l *Layer) {
			l.Files[1].Path = "/lib/firmware/../../etc/shadow"
			l.Files[1].Src = "files/lib/firmware/../../etc/shadow"
		},
		"other modules": func(l *Layer) {
			l.Files[0].Path = "/lib/modules/" + testKernel + "/kernel/x.ko"
			l.Files[0].Src = "files" + l.Files[0].Path
		},
		"nvidia package":  func(l *Layer) { l.Packages[0].Name = "nvidia-l4t-kernel" },
		"kernel package":  func(l *Layer) { l.Packages[0].Name = "linux-image-generic" },
		"ssh unit":        func(l *Layer) { l.Units[0].Unit = "ssh.service" },
		"app unit":        func(l *Layer) { l.Units[0].Unit = "eirl-encoderd.service" },
		"unit state":      func(l *Layer) { l.Units[0].State = "started" },
		"deb path":        func(l *Layer) { l.Packages[0].File = "../x.deb" },
		"src mismatch":    func(l *Layer) { l.Files[1].Src = "files/etc/passwd" },
		"module unnamed":  func(l *Layer) { l.Files[0].Module = "" },
		"area":            func(l *Layer) { l.Files[0].Area = "everything" },
		"arch":            func(l *Layer) { l.Packages[0].Arch = "amd64" },
		"duplicate":       func(l *Layer) { l.Packages[1].Name = l.Packages[0].Name },
		"format":          func(l *Layer) { l.V = 2 },
		"remove a kernel": func(l *Layer) { l.Remove = []string{"linux-image-4.9"} },
		"group":           func(l *Layer) { l.Groups = []string{"root;x"} },
		"drop-in for anything": func(l *Layer) {
			l.Files[2].Path = "/etc/systemd/system/x.service.d/override.conf"
			l.Files[2].Src = "files" + l.Files[2].Path
		},
	}
	for name, spoil := range bad {
		_, l := newLayer(t, "0.10.0", "a")
		spoil(l)
		if err := l.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCompareDebVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0}, {"1.0", "1.1", -1}, {"1.10", "1.9", 1}, {"1.0~rc1", "1.0", -1}, {"1:0.1", "2.0", 1},
		{"1.1.0-2ubuntu0.1", "1.1.0-2", 1}, {"2.0.0-2ubuntu1", "2.0.0-2ubuntu1", 0}, {"1.0a", "1.0", 1},
		{"1.1.0~git20171206.c724e70f-0.1", "1.1.0-0.1", -1}, {"2026.05.30-0ubuntu1~18.04.1", "2026.05.30-0ubuntu1", -1},
	} {
		got := compareDebVersions(c.a, c.b)
		if (got < 0) != (c.want < 0) || (got > 0) != (c.want > 0) {
			t.Errorf("compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestFingerprintAndCompare(t *testing.T) {
	rootA, fxA := newBoard(t)
	rootB, fxB := newBoard(t)
	dir, l := newLayer(t, "0.10.0", "a")
	for _, x := range []struct {
		root string
		fx   *fakeExec
	}{{rootA, fxA}, {rootB, fxB}} {
		if _, err := applier(x.root, x.fx).Apply(context.Background(), dir); err != nil {
			t.Fatal(err)
		}
	}
	fa, err := TakeFingerprint(rootA, testKernel, l)
	if err != nil {
		t.Fatal(err)
	}
	fb, _ := TakeFingerprint(rootB, testKernel, l)
	if d := Compare(fa, fb); len(d) != 0 {
		t.Fatalf("same system, differences: %v", d)
	}
	// B gets a hand-installed package and an unmasked unit
	fxB.setStatus(append(fxB.status(), pkg{"libimobiledevice-utils", "1.2.1", "install ok installed"}))
	os.Remove(filepath.Join(rootB, "etc/systemd/system/apt-daily.timer"))
	fb, _ = TakeFingerprint(rootB, testKernel, l)
	d := Compare(fa, fb)
	if len(d) != 2 {
		t.Fatalf("want 2 differences, got %v", d)
	}
	if bad := Unexpected(d, ParseExpected([]byte("package:libimobiledevice-* # by hand on the test board\nmasked:apt-**\n"))); len(bad) != 0 {
		t.Errorf("patterns did not allow %v", bad)
	}
	if bad := Unexpected(d, nil); len(bad) != 2 {
		t.Errorf("no patterns: %v", bad)
	}
}

func TestBuildLayer(t *testing.T) {
	repo := t.TempDir()
	buildOut := t.TempDir()
	debs := t.TempDir()
	write(t, filepath.Join(repo, "tools/image/base.lock"), "# base\nbash 4.4.18-2ubuntu1.3\ndpkg 1.19.0.5ubuntu2.4\n", 0o644)
	deb := []byte("a deb")
	write(t, filepath.Join(debs, "usbmuxd_1.1.0-2ubuntu0.1_arm64.deb"), string(deb), 0o644)
	write(t, filepath.Join(repo, "system/packages.lock"), fmt.Sprintf("# lock\nusbmuxd 1.1.0-2ubuntu0.1 arm64 %s %d 96 usbmuxd 1.1.0-2ubuntu0.1 pool/main/u/usbmuxd/usbmuxd_1.1.0-2ubuntu0.1_arm64.deb\n",
		shaOf(deb), len(deb)), 0o644)
	fakeModule(t, filepath.Join(buildOut, "tc358743.ko"), testKernel, "tc")
	write(t, filepath.Join(buildOut, "firmware/rtl_bt/rtl8761bu_fw.bin"), "fw", 0o644)
	write(t, filepath.Join(repo, "system/built.txt"), "/lib/modules/<kernel>/extra/tc358743.ko tc358743.ko hdmi reboot\n"+
		"/lib/firmware/rtl_bt/rtl8761bu_fw.bin firmware/rtl_bt/rtl8761bu_fw.bin bluetooth usedBy=btrtl\n", 0o644)
	write(t, filepath.Join(repo, "system/files/usr/lib/os-release"), "VERSION=\"@RELEASE@\"\n", 0o644)
	write(t, filepath.Join(repo, "system/files/usr/lib/eirl-image/eirl-firstboot"), "#!/bin/sh\n", 0o755)
	write(t, filepath.Join(repo, "system/overlay.txt"), "/usr/lib/os-release system -\n/usr/lib/eirl-image/eirl-firstboot system -\n", 0o644)
	write(t, filepath.Join(repo, "system/units.txt"), "apt-daily.timer masked # why\n", 0o644)
	write(t, filepath.Join(repo, "system/groups.txt"), "debug\n", 0o644)
	in := BuildInput{Repo: repo, Out: filepath.Join(t.TempDir(), "system"), Release: "0.10.0", DebCache: debs, BuildOut: buildOut,
		UpdateModulesLock: true, Commit: "abc", SourcesOut: filepath.Join(t.TempDir(), "SOURCES.md")}
	l, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if lock, _ := os.ReadFile(filepath.Join(repo, "system/modules.lock")); !strings.Contains(string(lock), "  tc358743.ko") {
		t.Errorf("modules.lock: %s", lock)
	}
	if b, _ := os.ReadFile(filepath.Join(in.Out, "files/usr/lib/os-release")); string(b) != "VERSION=\"0.10.0\"\n" {
		t.Errorf("os-release: %q", b)
	}
	l2, err := LoadLayer(in.Out)
	if err != nil || len(l2.Files) != 4 || len(l2.Packages) != 1 {
		t.Fatalf("layer: %+v %v", l2, err)
	}
	for _, f := range l.Files {
		switch f.Path {
		case "/usr/lib/eirl-image/eirl-firstboot":
			if f.Mode != "0755" {
				t.Errorf("an executable file: mode %s", f.Mode)
			}
		case "/lib/modules/" + testKernel + "/extra/tc358743.ko":
			if !f.Reboot || f.Module != "tc358743" || f.Area != "hdmi" {
				t.Errorf("tc358743: %+v", f)
			}
		case "/lib/firmware/rtl_bt/rtl8761bu_fw.bin":
			if f.UsedBy != "btrtl" {
				t.Errorf("firmware: %+v", f)
			}
		}
	}
	if s, _ := os.ReadFile(in.SourcesOut); !strings.Contains(string(s), "| usbmuxd | 1.1.0-2ubuntu0.1 | usbmuxd |") {
		t.Errorf("SOURCES.md: %s", s)
	}
	// A module that changed without a reviewed lock is refused
	in.UpdateModulesLock = false
	fakeModule(t, filepath.Join(buildOut, "tc358743.ko"), testKernel, "changed")
	if _, err := Build(in); err == nil || !strings.Contains(err.Error(), "modules.lock") {
		t.Errorf("a changed module: %v", err)
	}
	// A file of system/files that overlay.txt does not list is refused
	fakeModule(t, filepath.Join(buildOut, "tc358743.ko"), testKernel, "tc")
	write(t, filepath.Join(repo, "system/files/etc/stray.conf"), "x", 0o644)
	if _, err := Build(in); err == nil || !strings.Contains(err.Error(), "overlay.txt") {
		t.Errorf("a stray file: %v", err)
	}
}

func TestCommentOnlyChangeDoesNotWait(t *testing.T) {
	root, fx := newBoard(t)
	dir, _ := newLayer(t, "0.10.0", "a")
	// The board has the drop-in with other comments (the test board's, by hand) and usbmuxd runs
	write(t, filepath.Join(root, "etc/systemd/system/usbmuxd.service.d/90-eirl.conf"), "# old comment\n[Service]\n\nUMask=0077\n", 0o644)
	fx.active["usbmuxd.service"] = true
	res, err := applier(root, fx).Apply(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Pending {
		if p.What == "usbmuxd.service" {
			t.Errorf("a comment-only change waits for a restart: %+v", res.Pending)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/systemd/system/usbmuxd.service.d/90-eirl.conf")); string(b) != "[Service]\nUMask=0077\n" {
		t.Errorf("not replaced: %q", b)
	}
}

func TestDTBWithoutBuildTime(t *testing.T) {
	// Two builds of the same tree a day apart, as NVIDIA's dtc output records them
	dtb := func(when string) []byte {
		strs := []byte("model\x00nvidia,dtbbuildtime\x00")
		var st bytes.Buffer
		be := func(v uint32) { binary.Write(&st, binary.BigEndian, v) }
		pad := func() {
			for st.Len()%4 != 0 {
				st.WriteByte(0)
			}
		}
		be(1)
		st.WriteString("\x00")
		pad()
		prop := func(nameOff int, val string) {
			be(3)
			be(uint32(len(val)))
			be(uint32(nameOff))
			st.WriteString(val)
			pad()
		}
		prop(0, "jetson-nano\x00")
		prop(6, when)
		be(2)
		be(9)
		var b bytes.Buffer
		hdr := []uint32{0xd00dfeed, 0, 40, uint32(40 + st.Len()), 40, 17, 16, 0, uint32(len(strs)), uint32(st.Len())}
		hdr[1] = uint32(40 + st.Len() + len(strs))
		for _, v := range hdr {
			binary.Write(&b, binary.BigEndian, v)
		}
		b.Write(st.Bytes())
		b.Write(strs)
		return b.Bytes()
	}
	a, b := dtb("Oct 16 2026\x0009:00:22\x00"), dtb("Oct 19 2026\x0013:29:58\x00")
	if bytes.Equal(a, b) {
		t.Fatal("the test trees are the same")
	}
	if !bytes.Equal(dtbWithoutBuildTime(a), dtbWithoutBuildTime(b)) {
		t.Error("the build time still makes them differ")
	}
	c := dtb("Oct 16 2026\x0009:00:22\x00")
	copy(c[bytes.Index(c, []byte("jetson")):], "JETSON")
	if bytes.Equal(dtbWithoutBuildTime(a), dtbWithoutBuildTime(c)) {
		t.Error("a change of the tree is hidden")
	}
	if got := dtbWithoutBuildTime([]byte("not a dtb")); string(got) != "not a dtb" {
		t.Error("not a DTB is changed")
	}
}

func TestUndoWhenANewPackageCannotBePurged(t *testing.T) {
	root, fx := newBoard(t)
	dir, _ := newLayer(t, "0.10.0", "a")
	fx.failOn = "systemctl --root=" + root + " mask apt-daily.timer"
	// Something the board keeps depends on libusbmuxd4 now: dpkg refuses to purge it
	fx.failIf = func(call string) bool {
		return strings.HasPrefix(call, "dpkg --purge") && strings.Contains(call, "libusbmuxd4")
	}
	_, err := applier(root, fx).Apply(context.Background(), dir)
	var f *Failure
	if !errors.As(err, &f) || f.Code != "apply_failed" || !strings.Contains(f.Reason, "libusbmuxd4 stays") {
		t.Fatalf("want apply_failed with libusbmuxd4 staying, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, DefaultStateDir, markerFile)); err == nil {
		t.Fatal("the marker stayed: every later apply would be blocked")
	}
	got := installed(fx)
	if _, ok := got["usbmuxd"]; ok {
		t.Error("the other new package was not purged")
	}
	// The next apply is not blocked
	fx.failOn, fx.failIf = "", nil
	if _, err := applier(root, fx).Apply(context.Background(), dir); err != nil {
		t.Fatalf("the next apply: %v", err)
	}
}

func TestHalfConfiguredPackageIsNotNew(t *testing.T) {
	root, fx := newBoard(t)
	dir, _ := newLayer(t, "0.10.0", "a")
	// An earlier cut left libusbmuxd4 half configured: dpkg --configure -a finishes it before the plan
	fx.setStatus(append(fx.status(), pkg{"libusbmuxd4", "1.1.0~git20171206.c724e70f-0.1", "install ok half-configured"}))
	res, err := applier(root, fx).Apply(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range res.Plan.New {
		if n == "libusbmuxd4" {
			t.Error("a package the board already had counts as new (an undo would purge it)")
		}
	}
	if !strings.Contains(strings.Join(fx.calls, "\n"), "dpkg --configure -a") {
		t.Error("dpkg --configure -a did not run")
	}
}

func TestFailedApplyKeepsThePreviousState(t *testing.T) {
	root, fx := newBoard(t)
	dir, _ := newLayer(t, "0.10.0", "a")
	if _, err := applier(root, fx).Apply(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	dir2, _ := newLayer(t, "0.11.0", "b")
	n := 0
	fx.failIf = func(call string) bool { // the apply's depmod fails, the undo's works
		if strings.HasPrefix(call, "depmod") {
			n++
			return n == 1
		}
		return false
	}
	if _, err := applier(root, fx).Apply(context.Background(), dir2); err == nil {
		t.Fatal("no failure")
	}
	st := ReadState(filepath.Join(root, DefaultStateDir))
	if st.Release != "0.10.0" || st.Failed != "0.11.0" || !strings.HasPrefix(st.Error, "apply_failed") {
		t.Errorf("state after a failed 0.11.0: %+v", st)
	}
}

func TestSharedFilesAreNotRemoved(t *testing.T) {
	root, fx := newBoard(t)
	dir, l := newLayer(t, "0.10.0", "a")
	write(t, filepath.Join(dir, "files/usr/lib/os-release"), "VERSION=\"0.10.0\"\n", 0o644)
	l.Files = append(l.Files, File{Path: "/usr/lib/os-release", Src: "files/usr/lib/os-release", SHA256: shaOf([]byte("VERSION=\"0.10.0\"\n")),
		Mode: "0644", Area: "system"})
	writeLayer(t, dir, l)
	if _, err := applier(root, fx).Apply(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	dir2, _ := newLayer(t, "0.11.0", "a") // without os-release
	if _, err := applier(root, fx).Apply(context.Background(), dir2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr/lib/os-release")); err != nil {
		t.Error("a file the base's packages ship was removed")
	}
}

func TestNoPolicyRcDNoPackages(t *testing.T) {
	root, fx := newBoard(t)
	dir, _ := newLayer(t, "0.10.0", "a")
	write(t, filepath.Join(root, "usr/sbin"), "not a directory", 0o644) // policy-rc.d cannot be written
	if _, err := applier(root, fx).Apply(context.Background(), dir); err == nil {
		t.Fatal("applied without policy-rc.d")
	}
	for _, c := range fx.calls {
		if strings.HasPrefix(c, "apt-get") && !strings.Contains(c, " -s ") {
			t.Errorf("installed packages without policy-rc.d: %s", c)
		}
	}
}
