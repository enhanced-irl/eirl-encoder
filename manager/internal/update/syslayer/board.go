package syslayer

import (
	"bufio"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// What the layer reads from a root filesystem: dpkg's database, the units' state, files. Every path is under
// a root ("/" on the board and in the image's chroot, a directory in tests and for a fingerprint).

// dpkgPkg is one package of /var/lib/dpkg/status.
type dpkgPkg struct {
	Version string
	Arch    string
	Want    string // install | hold | deinstall | purge | unknown
	Status  string // installed | config-files | half-installed | unpacked | half-configured | triggers-* | not-installed
}

func (p dpkgPkg) installed() bool { return p.Status == "installed" }

// readDpkg parses <root>/var/lib/dpkg/status: every package dpkg knows, whatever its state.
func readDpkg(root string) (map[string]dpkgPkg, error) {
	f, err := os.Open(filepath.Join(root, "var/lib/dpkg/status"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]dpkgPkg{}
	var name string
	var cur dpkgPkg
	flush := func() {
		if name != "" {
			out[name] = cur
		}
		name, cur = "", dpkgPkg{}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		v = strings.TrimSpace(v)
		switch k {
		case "Package":
			name = v
		case "Version":
			cur.Version = v
		case "Architecture":
			cur.Arch = v
		case "Status":
			if f := strings.Fields(v); len(f) == 3 {
				cur.Want, cur.Status = f[0], f[2]
			}
		}
	}
	flush()
	return out, sc.Err()
}

// unitState: masked if /etc/systemd/system/<unit> links to /dev/null; enabled if a target wants or requires it
// (a link in /etc/systemd/system/*.wants or *.requires); disabled otherwise.
func unitState(root, unit string) string {
	sys := filepath.Join(root, "etc/systemd/system")
	if t, err := os.Readlink(filepath.Join(sys, unit)); err == nil && t == "/dev/null" {
		return Masked
	}
	ents, _ := os.ReadDir(sys)
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() || !(strings.HasSuffix(n, ".wants") || strings.HasSuffix(n, ".requires")) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(sys, n, unit)); err == nil {
			return Enabled
		}
	}
	return Disabled
}

// fileSHA: the sha256 of a regular file ("" and no error when it does not exist). A link or anything but a
// regular file is an error: the layer never writes through one.
func fileSHA(p string) (string, error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", p)
	}
	return sha256File(p)
}

func sha256String(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// moduleVermagic: the kernel release a .ko was built for (the first field of its vermagic).
func moduleVermagic(p string) (string, error) {
	f, err := elf.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s := f.Section(".modinfo")
	if s == nil {
		return "", errors.New("no .modinfo section")
	}
	b, err := s.Data()
	if err != nil {
		return "", err
	}
	for _, kv := range strings.Split(string(b), "\x00") {
		if v, ok := strings.CutPrefix(kv, "vermagic="); ok {
			if f := strings.Fields(v); len(f) > 0 {
				return f[0], nil
			}
		}
	}
	return "", errors.New("no vermagic")
}

var reTegraRelease = regexp.MustCompile(`#\s*R(\d+)\s*\(release\),\s*REVISION:\s*([0-9.]+)`)

// l4tRelease: "32.7.6" from /etc/nv_tegra_release ("# R32 (release), REVISION: 7.6, …"), "" if unknown.
func l4tRelease(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "etc/nv_tegra_release"))
	if err != nil {
		return ""
	}
	m := reTegraRelease.FindSubmatch(b)
	if m == nil {
		return ""
	}
	return string(m[1]) + "." + string(m[2])
}

// groupExists: a group named g in <root>/etc/group.
func groupExists(root, g string) bool {
	b, err := os.ReadFile(filepath.Join(root, "etc/group"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if n, _, ok := strings.Cut(line, ":"); ok && n == g {
			return true
		}
	}
	return false
}

// bootID: this boot's id (the kernel's), "" where there is none (an image being built).
func bootID(proc string) string {
	if proc == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(proc, "sys/kernel/random/boot_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeFileAtomic writes p through a temporary name in its directory and a rename, fsynced, root-owned.
func writeFileAtomic(p string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(p), "."+filepath.Base(p)+".eirl-new")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(p))
	return nil
}

// copyFileAtomic copies src to dst like writeFileAtomic, streaming.
func copyFileAtomic(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".eirl-new")
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(mode)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(dst))
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

func parseMode(s string) os.FileMode {
	if s == "0755" {
		return 0o755
	}
	return 0o644
}

func nowUTC(now func() time.Time) time.Time { return now().UTC().Truncate(time.Second) }
