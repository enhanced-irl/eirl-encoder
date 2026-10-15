//go:build linux && cgo

// Package displayhold keeps the board's HDMI output lit when the screen's pipeline closes (MIXER.md §3,
// trap 27): the engine's executable exports its own ioctl (hold.c), which holds back the power-down that
// NVIDIA's display libraries send when the last plane leaves the head. Between two screens, and while the
// engine restarts, the screen stays black with signal instead of "no signal". Importing the package is
// enough; Read is for the log and the tests.
package displayhold

/*
#cgo LDFLAGS: -ldl -Wl,--export-dynamic
#include "hold.h"
*/
import "C"

// Read is what the engine's ioctl has done since the start.
func Read() Stats {
	var h, r, c C.ulong
	C.eirl_hold_counts(&h, &r, &c)
	return Stats{Active: C.eirl_hold_active() != 0, Held: uint64(h), Released: uint64(r), Cancelled: uint64(c)}
}
