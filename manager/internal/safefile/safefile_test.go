package safefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadOwned(t *testing.T) {
	var warned []string
	OnUntrusted = func(path, reason string) { warned = append(warned, filepath.Base(path)+": "+reason) }
	dir := t.TempDir()
	good := filepath.Join(dir, "cloud.json")
	os.WriteFile(good, []byte(`{"ok":true}`), 0o600)
	if b, err := ReadOwned(good, 0); err != nil || string(b) != `{"ok":true}` {
		t.Fatalf("own file: %q %v", b, err)
	}
	// Missing: os.ErrNotExist, no warning
	if _, err := ReadOwned(filepath.Join(dir, "identity"), 0); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrUntrusted) {
		t.Errorf("missing: %v", err)
	}
	if len(warned) != 0 {
		t.Errorf("warned for a missing file: %v", warned)
	}
	// A symlink planted under the name (to a secret or to a file the planter controls): not followed
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("s3cret"), 0o600)
	link := filepath.Join(dir, "identity")
	os.Symlink(secret, link)
	if b, err := ReadOwned(link, 0); err == nil || !errors.Is(err, fs.ErrNotExist) || !errors.Is(err, ErrUntrusted) || len(b) != 0 {
		t.Errorf("symlink: %q %v", b, err)
	}
	// A FIFO: neither blocks nor is read
	fifo := filepath.Join(dir, "device.json")
	if err := unix.Mkfifo(fifo, 0o640); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadOwned(fifo, 0); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUntrusted) {
			t.Errorf("fifo: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
	// Too large
	big := filepath.Join(dir, "slots.json")
	os.WriteFile(big, []byte(strings.Repeat("x", 100)), 0o640)
	if _, err := ReadOwned(big, 10); !errors.Is(err, ErrUntrusted) {
		t.Errorf("too large: %v", err)
	}
	// Owned by someone else (the engine's user)
	old := ownerOK
	ownerOK = func(uid uint32) bool { return false }
	if _, err := ReadOwned(good, 0); !errors.Is(err, ErrUntrusted) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("foreign owner: %v", err)
	}
	ownerOK = old
	if len(warned) != 4 {
		t.Errorf("warnings: %v", warned)
	}
}

func TestReadFrom(t *testing.T) {
	var warned []string
	OnUntrusted = func(path, reason string) { warned = append(warned, filepath.Base(path)+": "+reason) }
	p := filepath.Join(t.TempDir(), "fan.json")
	os.WriteFile(p, []byte(`{"fan2":"off"}`), 0o644)
	me := uint32(os.Geteuid())
	if b, err := ReadFrom(p, 0, me); err != nil || string(b) != `{"fan2":"off"}` {
		t.Fatalf("the given user's file: %q %v", b, err)
	}
	if me != 0 { // as root, the file is root's: trusted with any uid
		if _, err := ReadFrom(p, 0, me+1); !errors.Is(err, ErrUntrusted) || !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("another user's file: %v", err)
		}
		if len(warned) != 1 {
			t.Errorf("warnings: %v", warned)
		}
	}
	link := filepath.Join(filepath.Dir(p), "link.json")
	os.Symlink(p, link)
	if _, err := ReadFrom(link, 0, me); !errors.Is(err, ErrUntrusted) {
		t.Errorf("symlink: %v", err)
	}
}
