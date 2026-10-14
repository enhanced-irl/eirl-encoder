package output

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

// The uplinks of SRTLA (SRTLA.md, WIFI.md §11 and §13). The files are written by others (the
// NetworkManager dispatcher, the app): they are read as data, through a whitelist.

var (
	reIface = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
	reIPv4  = regexp.MustCompile(`^([0-9]{1,3}\.){3}[0-9]{1,3}$`)
)

// uplinksEvery is how often the uplinks are looked at while emitting with SRTLA.
var uplinksEvery = 2 * time.Second

// uplink is one uplink to the internet: an interface and its IPv4.
type uplink struct{ iface, ip string }

// selectUplinks is the uplinks the dispatcher left in available (`<iface> <ipv4> [conflict]`), without
// those listed in the excluded files (disabled by the app; the client wifi without internet, WIFI.md
// §13) nor those that share an IP with another (they cannot be told apart by source). A missing or
// unreadable file excludes nothing; a missing available file gives none.
func selectUplinks(available string, excluded ...string) []uplink {
	off := map[string]bool{}
	for _, path := range excluded {
		if path == "" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, n := range strings.Split(string(b), "\n") {
			if n = strings.TrimSpace(n); reIface.MatchString(n) {
				off[n] = true
			}
		}
	}
	b, err := os.ReadFile(available)
	if err != nil {
		return nil
	}
	var sel []uplink
	for _, line := range splitLines(string(b)) {
		p := strings.Fields(line)
		if len(p) < 2 || !reIface.MatchString(p[0]) || !reIPv4.MatchString(p[1]) {
			continue
		}
		if len(p) > 2 && p[2] == "conflict" {
			continue
		}
		if off[p[0]] {
			continue
		}
		sel = append(sel, uplink{p[0], p[1]})
	}
	return sel
}

// splitLines is Python's str.splitlines() for the separators a text file can have.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
}

// uplinksDesc is the uplinks for a log line.
func uplinksDesc(sel []uplink) string {
	if len(sel) == 0 {
		return "none"
	}
	parts := make([]string, len(sel))
	for i, u := range sel {
		parts[i] = u.iface + " (" + u.ip + ")"
	}
	return strings.Join(parts, ", ")
}

func sameUplinks(a, b []uplink) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// uplinkFiles is where the uplinks go: srtla-ips is srtla_send's list (reloaded on SIGHUP), uplinks
// is the app's view of what is in use.
type uplinkFiles struct {
	runDir                     string
	available, disabled, nonet string
}

func (f uplinkFiles) current() []uplink { return selectUplinks(f.available, f.disabled, f.nonet) }

func (f uplinkFiles) ipsPath() string { return filepath.Join(f.runDir, "srtla-ips") }

func (f uplinkFiles) write(sel []uplink) {
	var ips, links strings.Builder
	for _, u := range sel {
		ips.WriteString(u.ip + "\n")
		links.WriteString(u.iface + " " + u.ip + "\n")
	}
	if err := sysx.WriteAtomic(f.ipsPath(), []byte(ips.String()), 0o644); err != nil {
		elog.Event("engine", "uplinks_write_failed", "err", err)
	}
	if err := sysx.WriteAtomic(filepath.Join(f.runDir, "uplinks"), []byte(links.String()), 0o644); err != nil {
		elog.Event("engine", "uplinks_write_failed", "err", err)
	}
}

// uplinkProc is srtla_send as watchUplinks sees it.
type uplinkProc interface {
	Done() <-chan struct{}
	Hangup() error
	Terminate()
}

// watchUplinks passes the uplinks that come and go to srtla_send with SIGHUP, without cutting. If the
// LAST one goes, srtla_send is stopped and the session ends with no_uplinks (transient: «connecting · no
// uplinks», and the retry waits for one to come back, as at start): with a srtla_send alive without
// uplinks, the output ended in connection_lost by SRT's timeout and the screen did not tell the truth.
// It returns when the process ends.
func watchUplinks(f uplinkFiles, p uplinkProc, cur []uplink, res *result) {
	t := time.NewTicker(uplinksEvery)
	defer t.Stop()
	for {
		select {
		case <-p.Done():
			return
		case <-t.C:
		}
		next := f.current()
		if sameUplinks(next, cur) {
			continue
		}
		select {
		case <-p.Done():
			return
		default:
		}
		f.write(next)
		if len(next) == 0 {
			elog.Event("engine", "srtla_no_uplinks", "action", "stopping srtla_send until one comes back")
			res.setNoUplinks(ReasonNoUplinks, "sin enlaces a internet")
			p.Terminate()
			return
		}
		if p.Hangup() != nil {
			return
		}
		elog.Event("engine", "srtla_uplinks", "links", uplinksDesc(next))
		cur = next
	}
}
