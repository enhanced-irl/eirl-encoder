package cloud

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// /etc/eirl-encoder is writable by its group (the engine): a cloud.json or identity created there by someone
// else before the app created its own is never trusted (safefile)
func TestPlantedCloudFilesAreIgnored(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	// A cloud.json that points the device at another backend, through a symlink
	evil := filepath.Join(elsewhere, "cloud.json")
	os.WriteFile(evil, []byte(`{"apiBase":"https://evil.example","token":"x","deviceId":"y"}`), 0o600)
	os.Symlink(evil, filepath.Join(dir, "cloud.json"))
	st := &StateFile{Path: filepath.Join(dir, "cloud.json")}
	if _, linked, err := st.Load(); linked || err != nil {
		t.Errorf("planted cloud.json: linked=%v err=%v", linked, err)
	}
	// An identity whose value the planter knows: refused, and never overwritten through the link
	known := filepath.Join(elsewhere, "identity")
	os.WriteFile(known, []byte(strings.Repeat("A", 43)+"\n"), 0o600)
	os.Symlink(known, filepath.Join(dir, "identity"))
	id := &IdentityFile{Path: filepath.Join(dir, "identity")}
	if v, err := id.LoadOrCreate(); err == nil || v != "" {
		t.Errorf("planted identity used: %q %v", v, err)
	}
	if b, _ := os.ReadFile(known); string(b) != strings.Repeat("A", 43)+"\n" {
		t.Error("the link's target was written")
	}
}
