package syslayer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The layer is built on the release machine (eirl-release layer, from tools/release.sh) out of the
// repository's system/ (UPDATES.md §12.2):
//
//	system/packages.lock   the packages: the closure of system/packages.txt over the base, exactly
//	system/built.txt       what comes from build/out: our modules and firmware, BlueALSA's ALSA plugins
//	system/modules.lock    the sha256 of each module build/build.sh builds (reproducible builds)
//	system/overlay.txt     the files of system/files/ and who reads them
//	system/units.txt       the units' state
//	system/groups.txt      system groups
//	tools/image/base.lock  the frozen base (the image of 0.7.0)
//
// and written to a directory that the bundle carries as system/.

// BuildInput: what Build needs.
type BuildInput struct {
	Repo     string // the repository's root
	Out      string // the layer's directory (made from scratch)
	Release  string
	DebCache string // the .deb files of system/packages.lock, by their file name (build/system.sh fetches them)
	BuildOut string // build/out: the modules and firmware of build/build.sh
	// UpdateModulesLock rewrites system/modules.lock with what was built instead of checking it
	UpdateModulesLock bool
	Commit            string // the repository's commit, for SOURCES.md
	SourcesOut        string // where to write the layer's SOURCES.md ("" = not written)
}

// Pinned base of the layer: the board and BSP the encoder's image is made of (IMAGE.md §1).
const (
	BaseL4T    = "32.7.6"
	BaseKernel = "4.9.337-tegra"
	BaseArch   = "arm64"
)

// LockedPackage is a line of system/packages.lock.
type LockedPackage struct {
	Package
	Path string // its path in Ubuntu's archive (pool/…), where build/system.sh fetches it
}

// ReadPackagesLock parses system/packages.lock: "name version arch sha256 size installed-size source
// source-version pool-path" per line.
func ReadPackagesLock(p string) ([]LockedPackage, error) {
	rows, err := readRows(p, 9)
	if err != nil {
		return nil, err
	}
	var out []LockedPackage
	for _, f := range rows {
		size, e1 := strconv.ParseInt(f[4], 10, 64)
		isz, e2 := strconv.ParseInt(f[5], 10, 64)
		if e1 != nil || e2 != nil {
			return nil, fmt.Errorf("%s: sizes of %s", p, f[0])
		}
		base := filepath.Base(f[8])
		out = append(out, LockedPackage{Package: Package{Name: f[0], Version: f[1], Arch: f[2], SHA256: f[3], Size: size,
			InstalledSize: isz, Source: f[6], SourceVersion: f[7], File: "debs/" + base}, Path: f[8]})
	}
	return out, nil
}

// readRows reads a whitespace-separated file with "#" comments: n columns per line (n = 0: any).
func readRows(p string, n int) ([][]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][]string
	sc := bufio.NewScanner(f)
	for i := 1; sc.Scan(); i++ {
		line, _, _ := strings.Cut(sc.Text(), "#")
		fs := strings.Fields(line)
		if len(fs) == 0 {
			continue
		}
		if n > 0 && len(fs) != n {
			return nil, fmt.Errorf("%s:%d: %d fields, want %d", p, i, len(fs), n)
		}
		out = append(out, fs)
	}
	return out, sc.Err()
}

// Build writes the layer into in.Out and returns it.
func Build(in BuildInput) (*Layer, error) {
	sys := filepath.Join(in.Repo, "system")
	l := &Layer{V: Format, Release: in.Release, Requires: Requires{L4T: BaseL4T, Kernel: BaseKernel, Arch: BaseArch}}
	if err := os.RemoveAll(in.Out); err != nil {
		return nil, err
	}
	for _, d := range []string{"debs", "files"} {
		if err := os.MkdirAll(filepath.Join(in.Out, d), 0o755); err != nil {
			return nil, err
		}
	}
	// The base
	rows, err := readRows(filepath.Join(in.Repo, "tools/image/base.lock"), 2)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		l.Base = append(l.Base, Pkg{Name: r[0], Version: r[1]})
	}
	// Packages, checked byte for byte against the lock
	pkgs, err := ReadPackagesLock(filepath.Join(sys, "packages.lock"))
	if err != nil {
		return nil, err
	}
	for _, p := range pkgs {
		src := filepath.Join(in.DebCache, filepath.Base(p.File))
		if err := copyChecked(src, filepath.Join(in.Out, p.File), p.SHA256, p.Size); err != nil {
			return nil, fmt.Errorf("%s: %w (build/system.sh fetches it)", p.Name, err)
		}
		l.Packages = append(l.Packages, p.Package)
	}
	if rows, err := readRows(filepath.Join(sys, "remove.txt"), 1); err == nil {
		for _, r := range rows {
			l.Remove = append(l.Remove, r[0])
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Our kernel modules and firmware, from build/out
	modLock := map[string]string{}
	if !in.UpdateModulesLock {
		rows, err := readRows(filepath.Join(sys, "modules.lock"), 2)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			modLock[r[1]] = r[0]
		}
	}
	krows, err := readRows(filepath.Join(sys, "built.txt"), 0)
	if err != nil {
		return nil, err
	}
	var newLock []string
	for _, r := range krows {
		if len(r) < 3 {
			return nil, fmt.Errorf("system/built.txt: %v", r)
		}
		dst := strings.ReplaceAll(r[0], "<kernel>", BaseKernel)
		f := File{Path: dst, Src: "files" + dst, Mode: "0644", Area: r[2]}
		for _, flag := range r[3:] {
			switch {
			case flag == "reboot":
				f.Reboot = true
			case strings.HasPrefix(flag, "usedBy="):
				f.UsedBy = strings.TrimPrefix(flag, "usedBy=")
			default:
				return nil, fmt.Errorf("system/built.txt: %s: flag %q", r[0], flag)
			}
		}
		src := filepath.Join(in.BuildOut, r[1])
		sum, err := sha256File(src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w (build/build.sh or build/bluealsa.sh builds it)", r[1], err)
		}
		if strings.HasSuffix(dst, ".ko") {
			f.Module = moduleName(dst)
			if vm, err := moduleVermagic(src); err != nil || vm != BaseKernel {
				return nil, fmt.Errorf("%s is built for %q (%v), not %s", r[1], vm, err, BaseKernel)
			}
			if in.UpdateModulesLock {
				newLock = append(newLock, sum+"  "+r[1])
			} else if modLock[r[1]] != sum {
				return nil, fmt.Errorf("%s: sha256 %s, system/modules.lock says %q: a module changed (review it, then eirl-release layer -update-modules-lock)", r[1], sum, modLock[r[1]])
			}
		}
		f.SHA256 = sum
		if err := copyChecked(src, filepath.Join(in.Out, f.Src), sum, -1); err != nil {
			return nil, err
		}
		l.Files = append(l.Files, f)
	}
	if in.UpdateModulesLock {
		sort.Slice(newLock, func(i, j int) bool { return strings.Fields(newLock[i])[1] < strings.Fields(newLock[j])[1] })
		hdr := "# system/modules.lock — the sha256 of each module build/build.sh builds (its builds are reproducible). A change\n" +
			"# here is a change on every board: review it (eirl-release layer -update-modules-lock rewrites it).\n"
		if err := os.WriteFile(filepath.Join(sys, "modules.lock"), []byte(hdr+strings.Join(newLock, "\n")+"\n"), 0o644); err != nil {
			return nil, err
		}
	}
	// The files of system/files/
	orows, err := readRows(filepath.Join(sys, "overlay.txt"), 3)
	if err != nil {
		return nil, err
	}
	for _, r := range orows {
		src := filepath.Join(sys, "files", r[0])
		fi, err := os.Stat(src)
		if err != nil || !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("system/files%s: %v", r[0], err)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		b = []byte(strings.ReplaceAll(string(b), "@RELEASE@", in.Release))
		f := File{Path: r[0], Src: "files" + r[0], Mode: "0644", Area: r[1], SHA256: sha256String(string(b))}
		if fi.Mode()&0o111 != 0 {
			f.Mode = "0755"
		}
		if r[2] != "-" {
			f.UsedBy = r[2]
		}
		dst := filepath.Join(in.Out, f.Src)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, b, parseMode(f.Mode)); err != nil {
			return nil, err
		}
		l.Files = append(l.Files, f)
	}
	// Every file of system/files/ must be listed (no file left behind)
	err = filepath.WalkDir(filepath.Join(sys, "files"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := "/" + filepath.ToSlash(mustRel(filepath.Join(sys, "files"), p))
		for _, r := range orows {
			if r[0] == rel {
				return nil
			}
		}
		return fmt.Errorf("system/files%s is not in system/overlay.txt", rel)
	})
	if err != nil {
		return nil, err
	}
	// Units and groups
	urows, err := readRows(filepath.Join(sys, "units.txt"), 2)
	if err != nil {
		return nil, err
	}
	for _, r := range urows {
		l.Units = append(l.Units, Unit{Unit: r[0], State: r[1]})
	}
	if grows, err := readRows(filepath.Join(sys, "groups.txt"), 1); err == nil {
		for _, r := range grows {
			l.Groups = append(l.Groups, r[0])
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sort.Slice(l.Files, func(i, j int) bool { return l.Files[i].Path < l.Files[j].Path })
	if err := l.Validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(in.Out, LayerFile), append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	if in.SourcesOut != "" {
		if err := writeSources(in, l); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// copyChecked copies src to dst if its sha256 (and size, if ≥ 0) are the ones given.
func copyChecked(src, dst, sum string, size int64) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if size >= 0 && fi.Size() != size {
		return fmt.Errorf("%s has %d bytes, want %d", src, fi.Size(), size)
	}
	if got, err := sha256File(src); err != nil || got != sum {
		return fmt.Errorf("%s: sha256 %s, want %s", src, got, sum)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeSources writes where the source of everything the layer installs is, with the written offer
// (UPDATES.md §12.9; the same terms as the image's, IMAGE.md §9.3).
func writeSources(in BuildInput, l *Layer) error {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("# Sources of the EnhancedIRL Encoder system layer %s", l.Release)
	w("")
	w("The system layer of release %s installs the kernel modules, firmware and Ubuntu packages below on the encoder", l.Release)
	w("(the same ones its SD card image has). It is built by the EnhancedIRL Encoder repository at commit `%s`", in.Commit)
	w("(https://github.com/enhanced-irl/eirl-encoder: the commit \"Sync with internal commit %s\").", in.Commit)
	w("This file says where their source code is, and carries our written offer for it.")
	w("")
	w("## Written offer")
	w("")
	w("For at least three years after we last distribute this release, EnhancedIRL will give anyone who asks a complete")
	w("machine-readable copy of the corresponding source code of every program below licensed under the GNU General")
	w("Public License, the GNU Lesser General Public License or the GNU Affero General Public License (any version), on")
	w("a medium customarily used for software interchange or as a download, for a charge no more than our cost of")
	w("physically performing that source distribution (nothing, for a download). Write to support@enhancedirl.com,")
	w("naming the release (%s). The sections below are also the directions to that source that version 3 of those", l.Release)
	w("licences asks for; we keep copies of what they point to.")
	w("")
	w("## Kernel modules")
	w("")
	w("GNU General Public License version 2. Built by `build/build.sh` of the repository at the commit above from the")
	w("kernel source in NVIDIA's L4T R%s `public_sources.tbz2`", BaseL4T)
	w("(https://developer.download.nvidia.com/embedded/L4T/r32_Release_v7.6/sources/T210/public_sources.tbz2), with")
	w("NVIDIA's kernel configuration (`build/kernel-%s.config`) and the patches in `driver/`:", BaseKernel)
	w("")
	for _, f := range l.Files {
		if f.Module != "" {
			w("- `%s`", f.Path)
		}
	}
	w("")
	w("## Firmware")
	w("")
	for _, f := range l.Files {
		if strings.HasPrefix(f.Path, "/lib/firmware/") && !strings.Contains(f.Path, "LICENCE") {
			w("- `%s`: from linux-firmware (https://git.kernel.org/pub/scm/linux/kernel/git/firmware/linux-firmware.git),", f.Path)
			w("  unmodified, redistributable under the licence installed next to it.")
		}
	}
	w("")
	w("## Ubuntu 18.04 packages")
	w("")
	w("Unmodified packages of Ubuntu 18.04 (\"bionic\", arm64) from Ubuntu's archive. Each package's licence is installed")
	w("at `/usr/share/doc/<package>/copyright`. The source packages, at the exact versions: `apt-get source")
	w("<package>=<version>`, or https://launchpad.net/ubuntu/+source/<package>/<version>. The encoder is built from")
	w("Ubuntu packages and is not Ubuntu: it is not affiliated with or endorsed by Canonical.")
	w("")
	w("| Package | Version | Source package | Source version |")
	w("|---|---|---|---|")
	for _, p := range l.Packages {
		w("| %s | %s | %s | %s |", p.Name, p.Version, p.Source, p.SourceVersion)
	}
	if err := os.MkdirAll(filepath.Dir(in.SourcesOut), 0o755); err != nil {
		return err
	}
	return os.WriteFile(in.SourcesOut, []byte(b.String()), 0o644)
}
