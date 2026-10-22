// Package syslayer is the system layer of the over-the-air update (UPDATES.md §12): what used to come only
// with a new SD card image, now in every release bundle. Kernel modules (and depmod), firmware, the Ubuntu
// packages added on top of the frozen base of the image, system files (drop-ins, configuration) and the state
// of units. The same code applies it to a running board (eirl-update system apply, from the bundle's
// install.sh) and to an SD card image being built (the same, with -image, in its chroot), so the two cannot
// drift apart.
//
// The layer is DATA inside the signed bundle (its sha256 is in the signed manifest): system/layer.json, the
// .deb files and the files. What it may touch is bounded here (AllowedFile, AllowedUnit, AllowedPackage), on
// top of the signature.
//
// An apply is a transaction until it commits: anything that fails, or a power cut (eirl-update recover at
// boot), puts files and units back and purges the packages it had just added. Once committed it only moves
// forward: a rollback of the app does not undo it, so a change of the layer must keep the previous release
// working (UPDATES.md §12.4).
package syslayer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Format is the version of layer.json and state.json this code reads and writes.
const Format = 1

// LayerFile is the layer's manifest inside its directory (the bundle's system/).
const LayerFile = "layer.json"

// Layer is system/layer.json.
type Layer struct {
	V        int      `json:"v"`
	Release  string   `json:"release"`  // the release that carries it
	Requires Requires `json:"requires"` // the base it applies to; any other is skipped (UPDATES.md §12.6)
	// Base: the frozen base's packages (the image of 0.7.0, tools/image/base.lock), name and version. The
	// layer never touches them unless it lists one in Packages (an update) or Remove. Used for drift
	Base     []Pkg     `json:"base"`
	Packages []Package `json:"packages"`
	// Remove: packages of the base the layer removes (purges), by name. Packages it added itself and no
	// longer lists are retired without being named here
	Remove []string `json:"remove,omitempty"`
	Files  []File   `json:"files"`
	Units  []Unit   `json:"units"`
	Groups []string `json:"groups,omitempty"` // system groups that must exist
}

// Requires: what the layer is built for.
type Requires struct {
	L4T    string `json:"l4t"`    // "32.7.6": /etc/nv_tegra_release
	Kernel string `json:"kernel"` // "4.9.337-tegra": the modules' vermagic and /lib/modules/<kernel>
	Arch   string `json:"arch"`   // "arm64": dpkg's architecture
}

// Pkg is a package name and version.
type Pkg struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Package is a .deb the layer installs at that exact version.
type Package struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Arch          string `json:"arch"`
	File          string `json:"file"` // relative to the layer directory: debs/<name>_<version>_<arch>.deb
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	InstalledSize int64  `json:"installedSize"`           // KiB (the control's Installed-Size)
	Source        string `json:"source,omitempty"`        // its source package (licences, UPDATES.md §12.9)
	SourceVersion string `json:"sourceVersion,omitempty"` // and version, when not the binary's
}

// File is a file the layer owns at Path.
type File struct {
	Path   string `json:"path"` // absolute, on the board
	Src    string `json:"src"`  // relative to the layer directory: files/<path without the leading />
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"` // "0644" or "0755"
	// Area: what a user would call the part it changes, for "restart to finish": hdmi | bluetooth | uplinks | system
	Area string `json:"area"`
	// Module: a kernel module's name (with underscores, as in /sys/module). A new one acts when it is loaded
	// next: now if it is not loaded, at the next boot if it is (UPDATES.md §12.5)
	Module string `json:"module,omitempty"`
	// UsedBy: who reads it. A module name for firmware (it is read when that module probes a device), a unit
	// for a configuration file (it is read when that unit starts)
	UsedBy string `json:"usedBy,omitempty"`
	// Reboot: it only acts after a reboot whatever is loaded (tc358743: the camera input keeps it)
	Reboot bool `json:"reboot,omitempty"`
}

// Unit is a unit's state that the layer fixes.
type Unit struct {
	Unit  string `json:"unit"`
	State string `json:"state"` // enabled | disabled | masked
}

// Unit states.
const (
	Enabled  = "enabled"
	Disabled = "disabled"
	Masked   = "masked"
)

// Areas a change belongs to.
var areas = map[string]bool{"hdmi": true, "bluetooth": true, "uplinks": true, "system": true}

var (
	reName    = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	reVersion = regexp.MustCompile(`^[0-9][A-Za-z0-9.+~:-]*$`)
	reSHA     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reUnit    = regexp.MustCompile(`^[A-Za-z0-9:_.@-]+\.(service|socket|timer|target|path|mount)$`)
	reModule  = regexp.MustCompile(`^[a-z0-9_]+$`)
	reGroup   = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	reKernel  = regexp.MustCompile(`^[0-9][0-9A-Za-z.+_-]*$`)
)

// allowedPrefixes: where the layer may own files (UPDATES.md §12.8). Never /boot, /etc/eirl-encoder,
// sudoers, /home, /root or dpkg's database; the app's own files (files.txt) are not the layer's either.
// "<kernel>" is replaced by Requires.Kernel.
var allowedPrefixes = []string{
	"/lib/modules/<kernel>/updates/eirl-",
	"/lib/firmware/",
	"/etc/systemd/journald.conf.d/90-eirl-",
	"/etc/NetworkManager/conf.d/",
	"/usr/lib/eirl-image/",
}

// allowedExact: single files the layer may own.
var allowedExact = []string{
	"/lib/modules/<kernel>/extra/tc358743.ko",
	// The first-boot units of the image (they only run once, but a board and an image must not differ)
	"/etc/systemd/system/eirl-firstboot.service",
	"/etc/systemd/system/eirl-firmware.service",
	// The version the system says (UPDATES.md §12.12)
	"/usr/lib/os-release", "/etc/lsb-release", "/etc/issue", "/etc/issue.net",
	// Bluetooth audio (BLUETOOTH.md §3.7): BlueALSA's ALSA plugins, their configuration, the D-Bus policy
	"/usr/lib/aarch64-linux-gnu/alsa-lib/libasound_module_pcm_bluealsa.so",
	"/usr/lib/aarch64-linux-gnu/alsa-lib/libasound_module_ctl_bluealsa.so",
	"/usr/share/alsa/alsa.conf.d/20-bluealsa.conf",
	"/etc/dbus-1/system.d/eirl-encoder-bluetooth.conf",
}

// A drop-in of ours for any unit: /etc/systemd/system/<unit>.d/90-eirl*.conf
var reDropIn = regexp.MustCompile(`^/etc/systemd/system/[A-Za-z0-9:_.@-]+\.(service|socket|timer)\.d/90-eirl[A-Za-z0-9_.-]*\.conf$`)

// AllowedFile reports whether the layer may own p (absolute, clean) for that kernel release.
func AllowedFile(p, kernel string) bool {
	if p == "" || p[0] != '/' || path.Clean(p) != p || strings.ContainsAny(p, "\x00\n\r") || strings.Contains(p, "/../") {
		return false
	}
	for _, e := range allowedExact {
		if p == strings.ReplaceAll(e, "<kernel>", kernel) {
			return true
		}
	}
	if reDropIn.MatchString(p) {
		return true
	}
	for _, pre := range allowedPrefixes {
		pre = strings.ReplaceAll(pre, "<kernel>", kernel)
		if strings.HasPrefix(p, pre) && len(p) > len(pre) && !strings.HasSuffix(p, "/") {
			return true
		}
	}
	return false
}

// unitsOfLayer: the units the layer may enable or mask although they are named eirl-*: its own.
var unitsOfLayer = map[string]bool{"eirl-firstboot.service": true, "eirl-firmware.service": true}

// AllowedUnit reports whether the layer may change u's state. Never SSH (the debug image runs it: masking it
// would lock the test board out) nor the app's units, which install.sh enables.
func AllowedUnit(u string) bool {
	if !reUnit.MatchString(u) {
		return false
	}
	switch u {
	case "ssh.service", "ssh.socket", "sshd.service":
		return false
	}
	if strings.HasPrefix(u, "eirl-") && !unitsOfLayer[u] {
		return false
	}
	return true
}

// AllowedPackage reports whether the layer may install or remove a package of that name: never NVIDIA's
// BSP (on hold), a kernel or a bootloader.
func AllowedPackage(name string) bool {
	if !reName.MatchString(name) {
		return false
	}
	for _, pre := range []string{"nvidia-l4t-", "linux-image", "linux-headers", "linux-modules", "jetson-", "u-boot", "grub"} {
		if strings.HasPrefix(name, pre) {
			return false
		}
	}
	return true
}

// ParseLayer parses and validates layer.json.
func ParseLayer(b []byte) (*Layer, error) {
	var l Layer
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("layer.json: %w", err)
	}
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return &l, nil
}

// LoadLayer reads dir/layer.json.
func LoadLayer(dir string) (*Layer, error) {
	b, err := os.ReadFile(path.Join(dir, LayerFile))
	if err != nil {
		return nil, err
	}
	if len(b) > 4<<20 {
		return nil, errors.New("layer.json: too large")
	}
	return ParseLayer(b)
}

// Validate checks everything the layer names against the allow-lists and the formats.
func (l *Layer) Validate() error {
	if l.V != Format {
		return fmt.Errorf("layer.json: format %d, this helper reads %d", l.V, Format)
	}
	if l.Release == "" || !reVersion.MatchString(l.Release) {
		return fmt.Errorf("layer.json: release %q", l.Release)
	}
	r := l.Requires
	if r.L4T == "" || !reVersion.MatchString(r.L4T) || !reKernel.MatchString(r.Kernel) || !reName.MatchString(r.Arch) {
		return fmt.Errorf("layer.json: requires %+v", r)
	}
	for _, p := range l.Base {
		if !reName.MatchString(p.Name) || !reVersion.MatchString(p.Version) {
			return fmt.Errorf("layer.json: base package %q %q", p.Name, p.Version)
		}
	}
	seen := map[string]bool{}
	for _, p := range l.Packages {
		switch {
		case !AllowedPackage(p.Name):
			return fmt.Errorf("layer.json: package %q is not allowed", p.Name)
		case seen[p.Name]:
			return fmt.Errorf("layer.json: package %q twice", p.Name)
		case !reVersion.MatchString(p.Version) || !reName.MatchString(p.Arch):
			return fmt.Errorf("layer.json: package %s: version %q arch %q", p.Name, p.Version, p.Arch)
		case p.Arch != r.Arch && p.Arch != "all":
			return fmt.Errorf("layer.json: package %s is for %s", p.Name, p.Arch)
		case !reSHA.MatchString(p.SHA256) || p.Size <= 0 || p.InstalledSize < 0:
			return fmt.Errorf("layer.json: package %s: sha256 or size", p.Name)
		case p.File != "debs/"+path.Base(p.File) || !strings.HasSuffix(p.File, ".deb") || strings.HasPrefix(path.Base(p.File), "."):
			return fmt.Errorf("layer.json: package %s: file %q", p.Name, p.File)
		}
		seen[p.Name] = true
	}
	for _, n := range l.Remove {
		if !AllowedPackage(n) || seen[n] {
			return fmt.Errorf("layer.json: remove %q", n)
		}
	}
	files := map[string]bool{}
	for _, f := range l.Files {
		switch {
		case !AllowedFile(f.Path, r.Kernel):
			return fmt.Errorf("layer.json: the layer may not own %q", f.Path)
		case files[f.Path]:
			return fmt.Errorf("layer.json: file %q twice", f.Path)
		case f.Src != "files"+f.Path:
			return fmt.Errorf("layer.json: file %s: src %q", f.Path, f.Src)
		case !reSHA.MatchString(f.SHA256):
			return fmt.Errorf("layer.json: file %s: sha256", f.Path)
		case f.Mode != "0644" && f.Mode != "0755":
			return fmt.Errorf("layer.json: file %s: mode %q", f.Path, f.Mode)
		case !areas[f.Area]:
			return fmt.Errorf("layer.json: file %s: area %q", f.Path, f.Area)
		case f.Module != "" && !reModule.MatchString(f.Module):
			return fmt.Errorf("layer.json: file %s: module %q", f.Path, f.Module)
		case f.UsedBy != "" && !reModule.MatchString(f.UsedBy) && !reUnit.MatchString(f.UsedBy):
			return fmt.Errorf("layer.json: file %s: usedBy %q", f.Path, f.UsedBy)
		}
		if strings.HasSuffix(f.Path, ".ko") != (f.Module != "") {
			return fmt.Errorf("layer.json: file %s: a module (and only a module) names its module", f.Path)
		}
		files[f.Path] = true
	}
	units := map[string]bool{}
	for _, u := range l.Units {
		if !AllowedUnit(u.Unit) || units[u.Unit] {
			return fmt.Errorf("layer.json: unit %q", u.Unit)
		}
		if u.State != Enabled && u.State != Disabled && u.State != Masked {
			return fmt.Errorf("layer.json: unit %s: state %q", u.Unit, u.State)
		}
		units[u.Unit] = true
	}
	for _, g := range l.Groups {
		if !reGroup.MatchString(g) {
			return fmt.Errorf("layer.json: group %q", g)
		}
	}
	return nil
}

// baseVersions: the base's packages by name.
func (l *Layer) baseVersions() map[string]string {
	m := make(map[string]string, len(l.Base))
	for _, p := range l.Base {
		m[p.Name] = p.Version
	}
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
