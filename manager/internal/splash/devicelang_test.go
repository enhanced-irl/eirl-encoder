package splash

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDeviceLanguageReadsSafely(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "device.json")
	os.WriteFile(p, []byte(`{"v":1,"language":"fr"}`), 0o640)
	if l := DeviceLanguage(p); l != "fr" {
		t.Errorf("device.json: %q", l)
	}
	os.Remove(p)
	other := filepath.Join(t.TempDir(), "x.json")
	os.WriteFile(other, []byte(`{"language":"es"}`), 0o640)
	os.Symlink(other, p)
	if l := DeviceLanguage(p); l != "" {
		t.Errorf("followed a symlink: %q", l)
	}
	os.Remove(p)
	if err := unix.Mkfifo(p, 0o640); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { done <- DeviceLanguage(p) }()
	select {
	case l := <-done:
		if l != "" {
			t.Errorf("fifo: %q", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
}
