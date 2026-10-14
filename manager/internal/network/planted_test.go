package network

import (
	"os"
	"path/filepath"
	"testing"
)

// The network files (cameras.json, network.json, ap.psk…) are read only if they are the app's
func TestPlantedNetworkFileIgnored(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(t.TempDir(), "ap.psk")
	os.WriteFile(other, []byte("plantedpassword12345\n"), 0o600)
	os.Symlink(other, filepath.Join(dir, "ap.psk"))
	m := &Manager{Dir: dir}
	if b := m.read("ap.psk"); b != nil {
		t.Errorf("planted ap.psk read: %q", b)
	}
	os.WriteFile(filepath.Join(dir, "network.json"), []byte(`{"v":1}`), 0o640)
	if b := m.read("network.json"); string(b) != `{"v":1}` {
		t.Errorf("own file: %q", b)
	}
}
