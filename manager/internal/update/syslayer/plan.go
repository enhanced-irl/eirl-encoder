package syslayer

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Plan: what an apply changes, from comparing the layer with the board. Everything is decided from the
// board's state, never from the version it runs (UPDATES.md §12.6): what is already there is left alone.
type Plan struct {
	Files   []fileOp
	Install []Package // not installed at the layer's version
	New     []string  // of Install, the ones dpkg does not have installed at all (purged if the apply is undone)
	Upgrade []string  // of Install, the ones installed at an older version (completed if the apply is undone)
	Retire  []string  // packages the layer added before and no longer lists, plus its Remove that are installed
	Units   []unitOp
	Groups  []string // missing system groups
	Depmod  bool
	Drift   Drift
}

type fileOp struct {
	Op   string // add | replace | remove
	File File   // for remove: Path (and what the previous layer said, if known)
}

type unitOp struct {
	Unit, From, To string
}

// Drift: what the board has that neither the base nor the layer says, reported and never touched
// (UPDATES.md §12.6): packages installed by hand, base packages at another version, a layer package that is
// newer on the board (never downgraded).
type Drift struct {
	Extra   []string `json:"extra,omitempty"`   // "name version"
	Changed []string `json:"changed,omitempty"` // "name version (base: version)"
	Newer   []string `json:"newer,omitempty"`   // "name version (layer: version)"
	Missing []string `json:"missing,omitempty"` // base packages that are not installed
}

func (d Drift) empty() bool {
	return len(d.Extra)+len(d.Changed)+len(d.Newer)+len(d.Missing) == 0
}

// Empty: nothing to change (drift aside).
func (p *Plan) Empty() bool {
	return len(p.Files)+len(p.Install)+len(p.Retire)+len(p.Units)+len(p.Groups) == 0 && !p.Depmod
}

// changes is the part that must be empty after an apply (retiring packages comes after the commit).
func (p *Plan) changes() bool {
	return len(p.Files)+len(p.Install)+len(p.Units)+len(p.Groups) > 0
}

func (p *Plan) String() string {
	var b strings.Builder
	for _, f := range p.Files {
		fmt.Fprintf(&b, "file %s %s\n", f.Op, f.File.Path)
	}
	for _, x := range p.Install {
		kind := "upgrade"
		for _, n := range p.New {
			if n == x.Name {
				kind = "new"
			}
		}
		fmt.Fprintf(&b, "package install %s %s (%s)\n", x.Name, x.Version, kind)
	}
	for _, n := range p.Retire {
		fmt.Fprintf(&b, "package purge %s\n", n)
	}
	for _, u := range p.Units {
		fmt.Fprintf(&b, "unit %s %s -> %s\n", u.Unit, u.From, u.To)
	}
	for _, g := range p.Groups {
		fmt.Fprintf(&b, "group add %s\n", g)
	}
	if p.Depmod {
		b.WriteString("depmod\n")
	}
	for _, s := range p.Drift.Extra {
		fmt.Fprintf(&b, "drift extra %s\n", s)
	}
	for _, s := range p.Drift.Changed {
		fmt.Fprintf(&b, "drift changed %s\n", s)
	}
	for _, s := range p.Drift.Newer {
		fmt.Fprintf(&b, "drift newer %s\n", s)
	}
	for _, s := range p.Drift.Missing {
		fmt.Fprintf(&b, "drift missing %s\n", s)
	}
	if p.Empty() {
		b.WriteString("nothing to change\n")
	}
	return b.String()
}

// makePlan compares the layer (l, in dir) with the board under root, given what the layer owned after its
// last apply (prev; nil = never applied).
func makePlan(root string, l *Layer, prev *State, dpkg map[string]dpkgPkg) (*Plan, error) {
	p := &Plan{}
	// Files
	declared := map[string]bool{}
	for _, f := range l.Files {
		declared[f.Path] = true
		sum, err := fileSHA(filepath.Join(root, f.Path))
		if err != nil {
			return nil, err
		}
		switch {
		case sum == "":
			p.Files = append(p.Files, fileOp{Op: "add", File: f})
		case sum != f.SHA256:
			p.Files = append(p.Files, fileOp{Op: "replace", File: f})
		}
	}
	if prev != nil {
		for _, path := range sortedKeys(prev.Files) {
			if declared[path] || !AllowedFile(path, l.Requires.Kernel) || sharedWithPackages[path] {
				continue // a file the base's packages ship (the system's name) is left as it is, not removed
			}
			if sum, err := fileSHA(filepath.Join(root, path)); err == nil && sum != "" {
				p.Files = append(p.Files, fileOp{Op: "remove", File: File{Path: path, Module: moduleName(path)}})
			}
		}
	}
	for _, op := range p.Files {
		if strings.HasSuffix(op.File.Path, ".ko") {
			p.Depmod = true
		}
	}
	// Packages
	base := l.baseVersions()
	want := map[string]bool{}
	for _, x := range l.Packages {
		want[x.Name] = true
		have, ok := dpkg[x.Name]
		switch {
		case ok && have.installed() && have.Version == x.Version:
		case ok && have.installed() && compareDebVersions(have.Version, x.Version) > 0:
			p.Drift.Newer = append(p.Drift.Newer, fmt.Sprintf("%s %s (layer: %s)", x.Name, have.Version, x.Version))
		default:
			p.Install = append(p.Install, x)
			// New: what dpkg does not have at all (purged if the apply is undone). Anything else is the board's
			// already (dpkgReady configured what an earlier cut left half done)
			if !ok || have.Status == "not-installed" || have.Status == "config-files" {
				p.New = append(p.New, x.Name)
			} else {
				p.Upgrade = append(p.Upgrade, x.Name)
			}
		}
	}
	retire := map[string]bool{}
	if prev != nil {
		for name := range prev.Packages {
			if _, inBase := base[name]; !want[name] && !inBase {
				retire[name] = true
			}
		}
	}
	for _, name := range l.Remove {
		retire[name] = true
	}
	for name := range retire {
		if have, ok := dpkg[name]; ok && have.Status != "not-installed" && AllowedPackage(name) {
			p.Retire = append(p.Retire, name)
		}
	}
	sort.Strings(p.Retire)
	// Drift: installed packages outside base ∪ layer, base packages at another version or missing
	removed := map[string]bool{}
	for _, n := range l.Remove {
		removed[n] = true
	}
	for _, name := range sortedKeys(dpkg) {
		have := dpkg[name]
		if !have.installed() || want[name] {
			continue
		}
		bv, inBase := base[name]
		switch {
		case !inBase && !retire[name]:
			p.Drift.Extra = append(p.Drift.Extra, name+" "+have.Version)
		case inBase && have.Version != bv && !removed[name]:
			p.Drift.Changed = append(p.Drift.Changed, fmt.Sprintf("%s %s (base: %s)", name, have.Version, bv))
		}
	}
	for _, b := range l.Base {
		if have, ok := dpkg[b.Name]; (!ok || !have.installed()) && !removed[b.Name] && !want[b.Name] {
			p.Drift.Missing = append(p.Drift.Missing, b.Name+" "+b.Version)
		}
	}
	// Units
	for _, u := range l.Units {
		if cur := unitState(root, u.Unit); cur != u.State {
			p.Units = append(p.Units, unitOp{Unit: u.Unit, From: cur, To: u.State})
		}
	}
	// Groups
	for _, g := range l.Groups {
		if !groupExists(root, g) {
			p.Groups = append(p.Groups, g)
		}
	}
	return p, nil
}

// sharedWithPackages: paths the layer writes that a package of the base also ships (base-files): a layer that stops
// listing one leaves it as it is instead of removing it (/etc/os-release links to /usr/lib/os-release).
var sharedWithPackages = map[string]bool{"/usr/lib/os-release": true, "/etc/lsb-release": true, "/etc/issue": true,
	"/etc/issue.net": true}

// moduleName: the module a .ko path holds ("updates/eirl-modems/huawei_cdc_ncm.ko" → huawei_cdc_ncm).
func moduleName(p string) string {
	if !strings.HasSuffix(p, ".ko") {
		return ""
	}
	return strings.ReplaceAll(strings.TrimSuffix(filepath.Base(p), ".ko"), "-", "_")
}
