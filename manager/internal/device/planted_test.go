package device

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A setup-code or a device.json created by someone else (a symlink) is not read
func TestPlantedDeviceFilesIgnored(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "setup-code"), []byte("ABCDEFGH\n"), 0o640)
	os.Symlink(filepath.Join(other, "setup-code"), filepath.Join(dir, "setup-code"))
	if c, err := ReadCode(filepath.Join(dir, "setup-code")); !errors.Is(err, os.ErrNotExist) || c != "" {
		t.Errorf("planted setup-code: %q %v", c, err)
	}
	os.WriteFile(filepath.Join(other, "device.json"), []byte(`{"v":1,"language":"fr","name":"Planted","setup":{"done":true,"skipped":[]}}`), 0o640)
	os.Symlink(filepath.Join(other, "device.json"), filepath.Join(dir, "device.json"))
	st, _, _ := Open(filepath.Join(dir, "device.json"), false, "eirl", time.Now())
	if st.Get().Name == "Planted" || st.Get().Setup.Done {
		t.Errorf("planted device.json used: %+v", st.Get())
	}
}
