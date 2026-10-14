//go:build !linux

package usbhub

import (
	"errors"
	"os"
)

// usbfsControl: usbfs only exists on Linux.
func usbfsControl(node string, _, _ uint8, _, _ uint16, _ int) ([]byte, error) {
	return nil, &os.PathError{Op: "USBDEVFS_CONTROL", Path: node, Err: errors.ErrUnsupported}
}
