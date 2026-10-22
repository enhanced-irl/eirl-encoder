package syslayer

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Fingerprint is what makes two root filesystems "the same system" for the encoder (UPDATES.md §12.10): a
// board migrated by the OTA must give the fingerprint of the image of the same release, except for the
// differences listed as expected. eirl-update system fingerprint writes it; compare diffs two.
type Fingerprint struct {
	Kernel     string            `json:"kernel"`
	Packages   map[string]string `json:"packages"`   // installed: name → version
	Held       []string          `json:"held"`       // on hold (the BSP)
	Modules    map[string]string `json:"modules"`    // /lib/modules/<kernel>/{extra,updates}/… → sha256
	ModulesDep string            `json:"modulesDep"` // sha256 of modules.dep, its lines sorted
	Firmware   map[string]int64  `json:"firmware"`   // /lib/firmware/… → size (the layer's own are in Files)
	Enabled    []string          `json:"enabled"`    // "<target>.wants/<unit>" and .requires
	Masked     []string          `json:"masked"`
	Files      map[string]string `json:"files"` // the layer's files → sha256 ("" = absent)
	Groups     []string          `json:"groups"`
	Users      []string          `json:"users"`
	Boot       map[string]string `json:"boot"` // /boot: extlinux.conf and the DTBs → sha256 (the frozen base)
}

// TakeFingerprint reads the fingerprint of the root filesystem under root. The layer (optional) names the
// files the layer owns; without it Files is empty.
func TakeFingerprint(root, kernel string, l *Layer) (*Fingerprint, error) {
	fp := &Fingerprint{Kernel: kernel, Packages: map[string]string{}, Modules: map[string]string{},
		Firmware: map[string]int64{}, Files: map[string]string{}, Boot: map[string]string{}}
	dpkg, err := readDpkg(root)
	if err != nil {
		return nil, err
	}
	for name, p := range dpkg {
		if p.installed() {
			fp.Packages[name] = p.Version
			if p.Want == "hold" {
				fp.Held = append(fp.Held, name)
			}
		}
	}
	sort.Strings(fp.Held)
	mods := filepath.Join(root, "lib/modules", kernel)
	for _, sub := range []string{"extra", "updates"} {
		err := filepath.WalkDir(filepath.Join(mods, sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.Type().IsRegular() {
				sum, err := sha256File(p)
				if err != nil {
					return err
				}
				fp.Modules["/"+mustRel(root, p)] = sum
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if b, err := os.ReadFile(filepath.Join(mods, "modules.dep")); err == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		sort.Strings(lines)
		fp.ModulesDep = sha256String(strings.Join(lines, "\n"))
	}
	fw := filepath.Join(root, "lib/firmware")
	filepath.WalkDir(fw, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				fp.Firmware["/"+mustRel(root, p)] = fi.Size()
			}
		}
		return nil
	})
	sys := filepath.Join(root, "etc/systemd/system")
	ents, _ := os.ReadDir(sys)
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() && (strings.HasSuffix(n, ".wants") || strings.HasSuffix(n, ".requires")) {
			sub, _ := os.ReadDir(filepath.Join(sys, n))
			for _, s := range sub {
				fp.Enabled = append(fp.Enabled, n+"/"+s.Name())
			}
			continue
		}
		if t, err := os.Readlink(filepath.Join(sys, n)); err == nil && t == "/dev/null" {
			fp.Masked = append(fp.Masked, n)
		}
	}
	sort.Strings(fp.Enabled)
	sort.Strings(fp.Masked)
	if l != nil {
		for _, f := range l.Files {
			sum, err := fileSHA(filepath.Join(root, f.Path))
			if err != nil {
				return nil, err
			}
			fp.Files[f.Path] = sum
		}
	}
	fp.Groups = names(filepath.Join(root, "etc/group"))
	fp.Users = names(filepath.Join(root, "etc/passwd"))
	bootDir := filepath.Join(root, "boot")
	if ents, err := os.ReadDir(bootDir); err == nil {
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".dtb") {
				if b, err := os.ReadFile(filepath.Join(bootDir, e.Name())); err == nil {
					fp.Boot["/boot/"+e.Name()] = sha256String(string(dtbWithoutBuildTime(b)))
				}
			}
		}
	}
	if sum, err := fileSHA(filepath.Join(bootDir, "extlinux/extlinux.conf")); err == nil {
		fp.Boot["/boot/extlinux/extlinux.conf"] = sum
	}
	return fp, nil
}

// dtbWithoutBuildTime: a flattened device tree with the value of NVIDIA's nvidia,dtbbuildtime zeroed: the time it was
// compiled, which otherwise makes every build of the same tree a different file (UPDATES.md §12.11). Anything that
// is not a well-formed FDT is returned as it is.
func dtbWithoutBuildTime(b []byte) []byte {
	be := func(o int) int {
		if o < 0 || o+4 > len(b) {
			return -1
		}
		return int(uint32(b[o])<<24 | uint32(b[o+1])<<16 | uint32(b[o+2])<<8 | uint32(b[o+3]))
	}
	if be(0) != 0xd00dfeed {
		return b
	}
	out := append([]byte(nil), b...)
	structOff, stringsOff, structSize := be(8), be(12), be(36)
	name := func(off int) string {
		start := stringsOff + off
		if start < 0 || start >= len(b) {
			return ""
		}
		end := start
		for end < len(b) && b[end] != 0 {
			end++
		}
		return string(b[start:end])
	}
	for o, end := structOff, structOff+structSize; o >= 0 && o+4 <= end && o+4 <= len(b); {
		switch be(o) {
		case 1: // FDT_BEGIN_NODE: a NUL-terminated name, padded to 4
			o += 4
			for o < len(b) && b[o] != 0 {
				o++
			}
			o = (o + 4) &^ 3
		case 3: // FDT_PROP: length, name offset, value padded to 4
			n, nameOff := be(o+4), be(o+8)
			if n < 0 || o+12+n > len(b) {
				return b
			}
			if name(nameOff) == "nvidia,dtbbuildtime" {
				clear(out[o+12 : o+12+n])
			}
			o = (o + 12 + n + 3) &^ 3
		case 2, 4: // FDT_END_NODE, FDT_NOP
			o += 4
		default: // FDT_END, or not a token: done
			return out
		}
	}
	return out
}

func mustRel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

func names(file string) []string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if n, _, ok := strings.Cut(sc.Text(), ":"); ok && n != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Difference is one way two fingerprints differ: "<category>:<key>" and what each side has.
type Difference struct {
	Key  string // e.g. "package:libimobiledevice-utils", "enabled:multi-user.target.wants/ssh.service"
	A, B string // "" = absent on that side
}

func (d Difference) String() string {
	return fmt.Sprintf("%s: %s | %s", d.Key, orAbsent(d.A), orAbsent(d.B))
}

func orAbsent(s string) string {
	if s == "" {
		return "(absent)"
	}
	return s
}

// Compare lists every difference between a and b.
func Compare(a, b *Fingerprint) []Difference {
	var out []Difference
	diffMap := func(cat string, x, y map[string]string) {
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		for _, k := range sortedKeys(keys) {
			if x[k] != y[k] {
				out = append(out, Difference{Key: cat + ":" + k, A: x[k], B: y[k]})
			}
		}
	}
	diffSet := func(cat string, x, y []string) {
		mx, my := map[string]string{}, map[string]string{}
		for _, v := range x {
			mx[v] = "yes"
		}
		for _, v := range y {
			my[v] = "yes"
		}
		diffMap(cat, mx, my)
	}
	if a.Kernel != b.Kernel {
		out = append(out, Difference{Key: "kernel", A: a.Kernel, B: b.Kernel})
	}
	diffMap("package", a.Packages, b.Packages)
	diffSet("held", a.Held, b.Held)
	diffMap("module", a.Modules, b.Modules)
	if a.ModulesDep != b.ModulesDep {
		out = append(out, Difference{Key: "modules.dep", A: a.ModulesDep, B: b.ModulesDep})
	}
	fa, fb := map[string]string{}, map[string]string{}
	for k, v := range a.Firmware {
		fa[k] = fmt.Sprint(v)
	}
	for k, v := range b.Firmware {
		fb[k] = fmt.Sprint(v)
	}
	diffMap("firmware", fa, fb)
	diffSet("enabled", a.Enabled, b.Enabled)
	diffSet("masked", a.Masked, b.Masked)
	diffMap("file", a.Files, b.Files)
	diffSet("group", a.Groups, b.Groups)
	diffSet("user", a.Users, b.Users)
	diffMap("boot", a.Boot, b.Boot)
	return out
}

// ParseExpected reads the patterns of the differences that are allowed, one per line with "#" comments (each
// with its reason in the file, UPDATES.md §12.10): path.Match against a difference's key, or a prefix when
// the pattern ends in "**".
func ParseExpected(b []byte) []string {
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line, _, _ = strings.Cut(line, "#")
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Unexpected: the differences no pattern allows.
func Unexpected(diffs []Difference, patterns []string) []Difference {
	var out []Difference
	for _, d := range diffs {
		ok := false
		for _, p := range patterns {
			if pre, isPrefix := strings.CutSuffix(p, "**"); isPrefix && strings.HasPrefix(d.Key, pre) {
				ok = true
				break
			}
			if m, _ := path.Match(p, d.Key); m {
				ok = true
				break
			}
		}
		if !ok {
			out = append(out, d)
		}
	}
	return out
}
