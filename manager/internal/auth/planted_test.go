package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// An admin.hash created by someone else (a symlink to a hash the planter knows) is not a password
func TestPlantedAdminHashIgnored(t *testing.T) {
	dir := t.TempDir()
	h, _ := HashPassword("planted-password-1")
	known := filepath.Join(t.TempDir(), "admin.hash")
	WriteHash(known, h)
	os.Symlink(known, filepath.Join(dir, "admin.hash"))
	a := New(filepath.Join(dir, "admin.hash"))
	if a.Configured() || a.LoadErr != nil {
		t.Errorf("planted admin.hash: configured=%v err=%v", a.Configured(), a.LoadErr)
	}
}
