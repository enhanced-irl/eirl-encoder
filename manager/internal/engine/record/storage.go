package record

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Storage is what the storage helper (eirl-storage, RECORDING.md §4.3) says about the disk. The engine only
// reads it: the helper is root and owns the mount.
type Storage struct {
	State     string  `json:"state"`
	Reason    string  `json:"reason,omitempty"`
	Seq       int64   `json:"seq"`
	FS        string  `json:"fs,omitempty"`
	Label     string  `json:"label,omitempty"`
	UUID      string  `json:"uuid,omitempty"`
	SizeBytes int64   `json:"sizeBytes,omitempty"`
	Dir       string  `json:"dir,omitempty"`
	ReadOnly  bool    `json:"readOnly,omitempty"`
	Dirty     bool    `json:"dirty,omitempty"`
	WriteMBps float64 `json:"writeMBps,omitempty"`
}

// The storage states (RECORDING.md §4.3).
const (
	StorageNone  = "none"
	StorageReady = "ready"
)

// Ready: a mounted, writable disk whose recordings' folder is under the recording root.
func (s Storage) Ready(root string) bool {
	return s.State == StorageReady && !s.ReadOnly && s.Dir != "" && within(root, s.Dir)
}

// FAT32: files of at most 4 GB (D4). The helper says fat32, or fat16/fat12 for the small ones it mounts the same way.
func (s Storage) FAT32() bool { return strings.HasPrefix(s.FS, "fat") }

// within: dir is root or below it, once cleaned (a state that names somewhere else is not used).
func within(root, dir string) bool {
	r, d := filepath.Clean(root), filepath.Clean(dir)
	return d == r || strings.HasPrefix(d, r+"/")
}

// maxStorageFile bounds the state file.
const maxStorageFile = 64 << 10

// readStorage reads the helper's state file; a missing or unreadable one is no disk.
func readStorage(path string) Storage {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Storage{State: StorageNone}
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return Storage{State: StorageNone}
	}
	b, err := io.ReadAll(io.LimitReader(f, maxStorageFile))
	var s Storage
	if err != nil || json.Unmarshal(b, &s) != nil || s.State == "" {
		return Storage{State: StorageNone}
	}
	return s
}

// free is the space a recording can still use in dir.
func free(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// gone: an error that means the disk is not there any more (pulled out, unmounted under the file, the
// FUSE daemon of exFAT dead).
func gone(err error) bool {
	return errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENXIO) ||
		errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.EROFS) ||
		errors.Is(err, syscall.ESTALE)
}

// full: the disk has no room left.
func full(err error) bool { return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) }
