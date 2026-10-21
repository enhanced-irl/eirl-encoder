//go:build !linux

package usbsignal

import (
	"errors"
	"os"
)

// openHidraw: hidraw only exists on Linux.
func openHidraw(node string) (HID, error) {
	return nil, &os.PathError{Op: "open", Path: node, Err: errors.ErrUnsupported}
}
