package config

import (
	"os"
	"path/filepath"
	"testing"
)

// An engine.env created by someone else (a symlink) is not the app's configuration
func TestPlantedAppEnvIgnored(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(t.TempDir(), "engine.env")
	os.WriteFile(other, []byte("SRT_URL=srt://evil.example:1234\n"), 0o640)
	os.Symlink(other, filepath.Join(dir, "engine.env"))
	s, err := Load(filepath.Join(dir, "default"), filepath.Join(dir, "engine.env"))
	if err != nil || s.URL == "srt://evil.example:1234" {
		t.Errorf("planted engine.env: %+v %v", s.URL, err)
	}
	st := &Store{DefaultPath: filepath.Join(dir, "default"), AppPath: filepath.Join(dir, "engine.env")}
	if h := st.Hashes(); h.Full != "none" {
		t.Errorf("hash of a planted file: %v", h)
	}
}
