package splash

import (
	"os"
	"path/filepath"
	"strings"
)

// ConsoleHasFB: is the text console (fbcon) bound to a framebuffer? With the splash's extlinux
// entry (fbcon=map:9) it is not; with any other entry the console would write over the logo, so the
// splash does not paint. sysVT: /sys/class/vtconsole.
func ConsoleHasFB(sysVT string) bool {
	dirs, _ := filepath.Glob(filepath.Join(sysVT, "vtcon*"))
	for _, d := range dirs {
		name, err1 := os.ReadFile(filepath.Join(d, "name"))
		bind, err2 := os.ReadFile(filepath.Join(d, "bind"))
		if err1 == nil && err2 == nil && strings.Contains(string(name), "frame buffer") &&
			strings.TrimSpace(string(bind)) == "1" {
			return true
		}
	}
	return false
}
