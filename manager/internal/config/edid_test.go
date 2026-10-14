package config

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The EDIDs the app offers (EDIDModes) are the ones a device has: each one has its source
// (edid/edid-<mode>-lpcm48k.hex, built by build/edid.sh), it is in the list that tools/deploy-engine.sh and
// the release bundle install (EDIDS), and eirl-hdmi-init.sh accepts it at boot.
func TestEDIDModesAreInstalled(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	deploy, err := os.ReadFile(filepath.Join(root, "tools", "deploy-engine.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^EDIDS="([^"]*)"`).FindSubmatch(deploy)
	if m == nil {
		t.Fatal("tools/deploy-engine.sh has no EDIDS=\"…\"")
	}
	installed := strings.Fields(string(m[1]))
	var want []string
	for _, mode := range EDIDModes {
		name := "edid-" + mode + "-lpcm48k"
		if _, err := os.Stat(filepath.Join(root, "edid", name+".hex")); err != nil {
			t.Errorf("mode %s without its source: %v", mode, err)
		}
		want = append(want, name+".bin")
	}
	slices.Sort(installed)
	slices.Sort(want)
	if !slices.Equal(installed, want) {
		t.Errorf("tools/deploy-engine.sh installs %v, the app offers %v", installed, want)
	}
	initScript, err := os.ReadFile(filepath.Join(root, "scripts", "eirl-hdmi-init.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if alt := "(" + strings.Join(EDIDModes, "|") + ")"; !strings.Contains(string(initScript), "EDID_PREFERRED="+alt) {
		t.Errorf("eirl-hdmi-init.sh does not accept exactly %s", alt)
	}
}
