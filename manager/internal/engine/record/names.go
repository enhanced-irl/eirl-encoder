package record

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// counterFile keeps the number of the last recording named without a trustworthy clock (STATE_DIR).
const counterFile = "recording-counter"

// baseName is a recording's name without its extension (RECORDING.md §5.3): the board's local time when the
// clock is synchronised, else a counter that survives restarts. Never a name a user typed.
func baseName(now time.Time, synced bool, stateDir string) string {
	if synced {
		return now.Format("2006-01-02_1504")
	}
	n := 0
	p := filepath.Join(stateDir, counterFile)
	if stateDir != "" {
		if b, err := os.ReadFile(p); err == nil {
			n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
	}
	n++
	if stateDir != "" {
		_ = writeFileAtomic(p, []byte(strconv.Itoa(n)+"\n"), 0o644)
	}
	return fmt.Sprintf("rec-%04d", n)
}

// create makes a new recording file in dir: base.mp4, or base-2.mp4, base-3.mp4… when taken. O_EXCL: an
// existing file is never written over.
func create(dir, base string) (*os.File, string, error) {
	for i := 1; i < 1000; i++ {
		name := base + ".mp4"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.mp4", base, i)
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
		if err == nil {
			return f, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("no free name for %s in %s", base, dir)
}

// splitBase is the base of a FAT32 split's next files: the first file's name without .mp4, so that its parts
// read base.mp4, base-2.mp4, base-3.mp4 (create finds the next free one).
func splitBase(first string) string { return strings.TrimSuffix(first, ".mp4") }

// writeFileAtomic writes a small file whole: a temporary name, fsync, rename.
func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
