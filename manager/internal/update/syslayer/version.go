package syslayer

import (
	"strconv"
	"strings"
)

// compareDebVersions compares two Debian versions as dpkg does ([epoch:]upstream[-revision]): <0, 0, >0.
func compareDebVersions(a, b string) int {
	ea, ua, ra := splitDebVersion(a)
	eb, ub, rb := splitDebVersion(b)
	if ea != eb {
		if ea < eb {
			return -1
		}
		return 1
	}
	if c := compareDebPart(ua, ub); c != 0 {
		return c
	}
	return compareDebPart(ra, rb)
}

func splitDebVersion(v string) (epoch int, upstream, revision string) {
	if e, rest, ok := strings.Cut(v, ":"); ok {
		epoch, _ = strconv.Atoi(e)
		v = rest
	}
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

// order: dpkg's ordering of a character in the non-digit parts: ~ before the end, the end before letters,
// letters before everything else.
func order(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return 0
	case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
		return int(c)
	case c == '~':
		return -1
	case c != 0:
		return int(c) + 256
	}
	return 0
}

func compareDebPart(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		// Non-digit prefixes
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			var ca, cb int
			if i < len(a) && !isDigit(a[i]) {
				ca = order(a[i])
			}
			if j < len(b) && !isDigit(b[j]) {
				cb = order(b[j])
			}
			if ca != cb {
				return ca - cb
			}
			i++
			j++
		}
		// Digit runs, numerically
		for i < len(a) && a[i] == '0' {
			i++
		}
		for j < len(b) && b[j] == '0' {
			j++
		}
		first := 0
		for i < len(a) && isDigit(a[i]) && j < len(b) && isDigit(b[j]) {
			if first == 0 {
				first = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		if i < len(a) && isDigit(a[i]) {
			return 1
		}
		if j < len(b) && isDigit(b[j]) {
			return -1
		}
		if first != 0 {
			return first
		}
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
