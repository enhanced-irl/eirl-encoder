package update

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// Streaming reports whether the engine is streaming (or trying to), from its run directory
// (/run/eirl-engine). It reads both files in one go, like tools/deploy-engine.sh:
//   - mixer.json (the live engine state, MIXER.md §5): output.mode "on", or output.state connecting/on;
//   - without a readable mixer.json, the state file of the launcher: OUTPUT=on (output at start).
//
// With the mixer, the state file only says how the engine STARTED (output can be switched off live), so
// mixer.json decides when it is there. No run directory = engine stopped = not streaming.
// The engine's user writes these files and root reads them (the helper): readEngineFile never follows a
// link, never blocks on a FIFO and never reads more than 1 MiB.
func Streaming(runDir string) (bool, string) {
	if b, err := readEngineFile(filepath.Join(runDir, "mixer.json")); err == nil {
		var st struct {
			Output *struct {
				Mode  string `json:"mode"`
				State string `json:"state"`
			} `json:"output"`
		}
		if json.Unmarshal(b, &st) == nil && st.Output != nil {
			switch {
			case st.Output.Mode == "on":
				return true, "mixer.json output.mode=on"
			case st.Output.State == "on" || st.Output.State == "connecting":
				return true, "mixer.json output.state=" + st.Output.State
			}
			return false, "mixer.json output off"
		}
	}
	b, err := readEngineFile(filepath.Join(runDir, "state"))
	if err != nil {
		return false, "no engine state"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "OUTPUT=on" {
			return true, "state OUTPUT=on"
		}
	}
	return false, "state OUTPUT not on"
}

// maxEngineFile: the largest engine state file that is read.
const maxEngineFile = 1 << 20

// readEngineFile reads a file written by the engine's user: no symlinks (O_NOFOLLOW), no blocking open
// (O_NONBLOCK: a FIFO), a regular file (fstat), at most maxEngineFile bytes.
func readEngineFile(p string) ([]byte, error) {
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), p)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxEngineFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxEngineFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, maxEngineFile)
	}
	return b, nil
}

// Paths of the installed software (all under a root prefix, "/" on the board).
const (
	LibDir       = "/usr/lib/eirl-encoder"
	VersionFile  = LibDir + "/VERSION"
	BuildFile    = LibDir + "/BUILD"
	FilesFile    = LibDir + "/files.txt"
	EngineVerTxt = LibDir + "/engine/VERSION"
)

// Versions is what is installed: the release (the bundle), the app binary, the engine and the commit.
type Versions struct {
	Release string `json:"release"`
	App     string `json:"app"`
	Engine  string `json:"engine,omitempty"`
	Commit  string `json:"commit,omitempty"`
}

// InstalledVersions reads the version files under root ("/" on the board). app is the running binary's
// version (main.version). A missing VERSION (an install from before the OTA) gives Release "".
func InstalledVersions(root, app string) Versions {
	v := Versions{App: app, Release: InstalledRelease(root)}
	if b, err := os.ReadFile(filepath.Join(root, EngineVerTxt)); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			v.Engine = f[0]
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, BuildFile)); err == nil {
		for _, f := range strings.Fields(string(b)) {
			if c, ok := strings.CutPrefix(f, "commit="); ok {
				v.Commit = c
			}
		}
	}
	return v
}

// InstalledRelease: the first line of /usr/lib/eirl-encoder/VERSION if it is a valid version, else "".
func InstalledRelease(root string) string {
	b, err := os.ReadFile(filepath.Join(root, VersionFile))
	if err != nil || len(b) > 256 {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	line = strings.TrimSpace(line)
	if _, err := ParseVersion(line); err != nil {
		return ""
	}
	return line
}

// allowedPrefixes: where a bundle may install files (files.txt). Defense in depth against a wrong
// files.txt: the helper never backs up, restores or deletes anything else. /etc/eirl-encoder is the
// app's own (writable by it) and is never touched by root (MANAGER.md §5).
var allowedPrefixes = []string{
	"/usr/local/bin/",
	"/usr/local/sbin/",
	LibDir + "/",
	"/etc/systemd/system/",
	"/etc/sudoers.d/",
	"/etc/polkit-1/localauthority/50-local.d/",
	"/etc/tmpfiles.d/",
	"/etc/udev/rules.d/",
	"/etc/NetworkManager/dispatcher.d/",
	"/usr/share/doc/eirl-encoder/",
}

// allowedExact: single files outside those directories that an install modifies.
var allowedExact = map[string]bool{"/boot/extlinux/extlinux.conf": true}

// AllowedPath reports whether p (absolute, as in files.txt) is a path the helper may manage.
func AllowedPath(p string) bool {
	if p == "" || p[0] != '/' || filepath.Clean(p) != p || strings.ContainsAny(p, "\x00\n\r") {
		return false
	}
	if allowedExact[p] {
		return true
	}
	for _, pre := range allowedPrefixes {
		if strings.HasPrefix(p, pre) && len(p) > len(pre) {
			return true
		}
	}
	return false
}

// ParseFiles parses files.txt: one absolute path per line; blank lines and "#" comments ignored.
// Returns the sorted, de-duplicated paths and an error naming the first path that is not allowed.
func ParseFiles(b []byte) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !AllowedPath(line) {
			return nil, fmt.Errorf("files.txt line %d: path not allowed: %q", i+1, line)
		}
		if !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out, nil
}
