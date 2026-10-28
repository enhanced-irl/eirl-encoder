// Package storage is the logic of eirl-storage, the root helper that mounts the USB disk of local recordings
// (RECORDING.md §4): which disk and partition, how it is mounted and checked, and the state file the engine and the app
// read. The commands it runs (blkid, mount, fsck…) go through a Runner and every path has a root prefix, so the tests
// run on any machine without root.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// States of the state file (RECORDING.md §4.3).
const (
	StateNone        = "none"
	StateChecking    = "checking"
	StateReady       = "ready"
	StateUnsupported = "unsupported"
	StateUnformatted = "unformatted"
	StateError       = "error"
	StateEjected     = "ejected"
)

// Reasons of the state file.
const (
	ReasonFsNTFS      = "fs_ntfs"
	ReasonFsUnknown   = "fs_unknown"
	ReasonTooSmall    = "too_small"
	ReasonAnotherDisk = "another_disk"
	ReasonMountFailed = "mount_failed"
	ReasonReadOnly    = "readonly"
	ReasonCheckFailed = "check_failed"
)

// States and Reasons: the closed lists of the state file (RECORDING.md §4.3). The app forwards nothing else, in its
// storage object or in the engine's record.disk.
var (
	States = map[string]bool{StateNone: true, StateChecking: true, StateReady: true, StateUnsupported: true,
		StateUnformatted: true, StateError: true, StateEjected: true}
	Reasons = map[string]bool{ReasonFsNTFS: true, ReasonFsUnknown: true, ReasonTooSmall: true, ReasonMountFailed: true,
		ReasonReadOnly: true, ReasonCheckFailed: true}
)

// ParseSeq reads a state's seq given to format: 1 to 18 digits, nothing else.
func ParseSeq(s string) (int64, bool) {
	if len(s) == 0 || len(s) > 18 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// State is /run/eirl-storage/state.json. Fields that do not apply are left out; reason is always there ("" when there is
// none), as in the contract's example.
type State struct {
	State      string  `json:"state"`
	Reason     string  `json:"reason"`
	Seq        int64   `json:"seq"`
	Device     string  `json:"device,omitempty"`
	Partition  string  `json:"partition,omitempty"`
	Model      string  `json:"model,omitempty"`
	FS         string  `json:"fs,omitempty"`
	Label      string  `json:"label,omitempty"`
	UUID       string  `json:"uuid,omitempty"`
	SizeBytes  int64   `json:"sizeBytes,omitempty"`
	Mountpoint string  `json:"mountpoint,omitempty"`
	Dir        string  `json:"dir,omitempty"`
	ReadOnly   *bool   `json:"readOnly,omitempty"`
	Dirty      *bool   `json:"dirty,omitempty"`
	WriteMBps  float64 `json:"writeMBps,omitempty"`
	// AnotherDisk: a second USB disk plugged in while this one is in use, left alone (RECORDING.md §4.2). It is
	// reported here instead of replacing the state, and without a new seq, so that plugging a second disk never
	// stops a recording
	AnotherDisk string `json:"anotherDisk,omitempty"`
	At          string `json:"at,omitempty"`
}

// maxStateFile: the largest state file that is read.
const maxStateFile = 64 << 10

// ReadState reads a state file. A missing file is the state "none" (no disk since the boot).
func ReadState(path string) (State, error) {
	b, err := readRegular(path, maxStateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return State{State: StateNone}, nil
	}
	if err != nil {
		return State{State: StateNone}, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{State: StateNone}, fmt.Errorf("%s: %w", path, err)
	}
	if st.State == "" {
		st.State = StateNone
	}
	return st, nil
}

// writeStateFile writes the state whole: a temporary name in the same folder, 0644, and a rename.
func writeStateFile(path string, st State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o644); err != nil { // whatever the umask
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

func boolPtr(b bool) *bool { return &b }
