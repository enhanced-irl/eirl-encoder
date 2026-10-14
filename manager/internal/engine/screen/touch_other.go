//go:build !linux

package screen

import "os"

// absMax: evdev is Linux only (builds on the Mac for the tests).
func absMax(*os.File, int) int32 { return 1 }
