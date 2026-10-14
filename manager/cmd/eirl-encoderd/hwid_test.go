package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
)

func TestPrintHardwareID(t *testing.T) {
	dir := t.TempDir()
	mid := filepath.Join(dir, "machine-id")
	if err := os.WriteFile(mid, []byte("0123456789abcdef0123456789abcdef\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if rc := printHardwareID(&out, &errOut, mid); rc != 0 {
		t.Fatalf("exit %d: %s", rc, errOut.String())
	}
	id := strings.TrimSuffix(out.String(), "\n")
	// The app's id: what the cloud and the default Wi-Fi name use
	if want := cloud.DeviceID(mid); id != want || want == "" {
		t.Fatalf("printed %q, the app uses %q", id, want)
	}
	// sha256("eirl-encoder:" + machine-id), first 8 bytes in hex: fixed, a device must never change id
	if id != "f47bd3181b35efc3" {
		t.Errorf("id %q changed", id)
	}
	if ssid := netcfg.DefaultSSID(id); ssid != "EnhancedIRL-EFC3" {
		t.Errorf("default Wi-Fi name %q", ssid)
	}
	if errOut.Len() != 0 || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("stdout %q, stderr %q", out.String(), errOut.String())
	}

	// No machine-id yet (an image before its first boot), or an empty one: no id, exit 1, nothing on stdout
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dir, "missing"), empty} {
		out.Reset()
		errOut.Reset()
		if rc := printHardwareID(&out, &errOut, p); rc != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "no hardware id") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", p, rc, out.String(), errOut.String())
		}
	}
}
