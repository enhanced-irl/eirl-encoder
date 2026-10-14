package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a semantic version (semver 2.0.0): MAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]. Release builds
// are "0.1.0"; dev builds "0.0.0-dev+<commit>" (UPDATES.md §2), which sort below every release.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string // dot-separated prerelease identifiers ("test", "1")
	Build               string   // ignored for precedence
}

// ParseVersion parses a strict semver string (no leading "v", no leading zeros in numbers).
func ParseVersion(s string) (Version, error) {
	var v Version
	if s == "" || len(s) > 128 {
		return v, fmt.Errorf("version %q: empty or too long", s)
	}
	rest := s
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		v.Build = rest[i+1:]
		rest = rest[:i]
		if !validIdents(v.Build, false) {
			return Version{}, fmt.Errorf("version %q: bad build metadata", s)
		}
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		pre := rest[i+1:]
		rest = rest[:i]
		if !validIdents(pre, true) {
			return Version{}, fmt.Errorf("version %q: bad prerelease", s)
		}
		v.Pre = strings.Split(pre, ".")
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q: want MAJOR.MINOR.PATCH", s)
	}
	nums := [3]*uint64{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if !numeric(p) || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("version %q: bad number %q", s, p)
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("version %q: %w", s, err)
		}
		*nums[i] = n
	}
	return v, nil
}

// MustVersion is ParseVersion for constants in tests.
func MustVersion(s string) Version {
	v, err := ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// Compare returns -1, 0 or 1 by semver precedence (build metadata ignored).
func (v Version) Compare(o Version) int {
	for _, d := range [][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	// A version without prerelease is greater than one with it
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := compareIdent(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.Pre) < len(o.Pre):
		return -1
	case len(v.Pre) > len(o.Pre):
		return 1
	}
	return 0
}

// Less reports v < o.
func (v Version) Less(o Version) bool { return v.Compare(o) < 0 }

func compareIdent(a, b string) int {
	an, bn := numeric(a), numeric(b)
	switch {
	case an && bn:
		// No leading zeros (validated), so a longer number is larger
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1 // numeric identifiers sort below alphanumeric ones
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

func numeric(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// validIdents: dot-separated, non-empty identifiers of [0-9A-Za-z-]; prerelease numbers without leading zeros.
func validIdents(s string, pre bool) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for i := 0; i < len(id); i++ {
			c := id[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
				return false
			}
		}
		if pre && numeric(id) && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}
