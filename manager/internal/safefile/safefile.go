// Package safefile reads the app's own configuration files (/etc/eirl-encoder) without trusting a name
// someone else put there. The directory is 3770 root:eirl-encoder: the engine is in the group (to read
// engine.env), so with the sticky bit it cannot replace the app's files, but it can still create a name
// the app has not created yet (cloud.json before linking, identity before the first pairing, a symlink…).
// ReadOwned only trusts a regular file owned by the app (or by root, which the engine cannot create): no
// symlinks (O_NOFOLLOW), no blocking open (O_NONBLOCK: a FIFO), a size cap. Anything else is reported as
// missing, with a warning, so every caller keeps its "no file" behaviour.
package safefile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"
)

// MaxSize is the default cap of a configuration file.
const MaxSize = 1 << 20

// ErrUntrusted: the name exists but is not the app's file. It also matches os.ErrNotExist (errors.Is), so
// callers treat it as a missing file.
var ErrUntrusted = errors.New("not the app's file")

// OnUntrusted is called for every untrusted file (main points it at the app's logger).
var OnUntrusted = func(path, reason string) {
	slog.Warn("ignoring a configuration file that is not the app's", "path", path, "reason", reason)
}

// ownerOK: the uids whose files are trusted: the app's own and root (tests replace it).
var ownerOK = func(uid uint32) bool { return uid == uint32(os.Geteuid()) || uid == 0 }

type untrusted struct{ path, reason string }

func (e *untrusted) Error() string {
	return e.path + ": " + ErrUntrusted.Error() + " (" + e.reason + ")"
}
func (e *untrusted) Is(target error) bool {
	return target == ErrUntrusted || target == fs.ErrNotExist
}

func reject(path, reason string) error {
	OnUntrusted(path, reason)
	return &untrusted{path, reason}
}

// ReadOwned reads path if it is a regular file owned by the app (or root) of at most max bytes (0 =
// MaxSize). A missing file gives an error that matches os.ErrNotExist; so does an untrusted one (and
// ErrUntrusted, with a warning).
func ReadOwned(path string, max int64) ([]byte, error) {
	if max <= 0 {
		max = MaxSize
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, reject(path, "symbolic link")
		}
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, &fs.PathError{Op: "stat", Path: path, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, reject(path, "not a regular file")
	}
	if !ownerOK(st.Uid) {
		return nil, reject(path, fmt.Sprintf("owned by uid %d", st.Uid))
	}
	if st.Size > max {
		return nil, reject(path, fmt.Sprintf("%d bytes, more than %d", st.Size, max))
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, reject(path, fmt.Sprintf("more than %d bytes", max))
	}
	return b, nil
}
