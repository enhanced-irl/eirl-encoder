//go:build linux

package usbsignal

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// HIDIOCSFEATURE(9) and HIDIOCGFEATURE(9): _IOC(_IOC_READ|_IOC_WRITE, 'H', 0x06/0x07, 9).
const (
	hidiocSFeature9 = 0xC0094806
	hidiocGFeature9 = 0xC0094807
)

type hidraw struct {
	fd   int
	node string
}

// openHidraw opens the node read-write: a feature report is sent with an ioctl that needs write access
// (the eirl-usbhub group).
func openHidraw(node string) (HID, error) {
	fd, err := unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: node, Err: err}
	}
	return &hidraw{fd: fd, node: node}, nil
}

// Exchange sends the report and reads the answer. The usbhid driver bounds each control transfer (5 s).
func (h *hidraw) Exchange(out [9]byte) ([9]byte, error) {
	var in [9]byte
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(h.fd), hidiocSFeature9, uintptr(unsafe.Pointer(&out[0]))); e != 0 {
		return in, &os.PathError{Op: "HIDIOCSFEATURE", Path: h.node, Err: e}
	}
	in[0] = out[0] // the report id to get
	n, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(h.fd), hidiocGFeature9, uintptr(unsafe.Pointer(&in[0])))
	if e != 0 {
		return in, &os.PathError{Op: "HIDIOCGFEATURE", Path: h.node, Err: e}
	}
	if n == 8 { // without the report id in front (the board's 4.9 gives 9)
		copy(in[1:], in[:8])
		in[0] = 0
	}
	return in, nil
}

func (h *hidraw) Close() error { return unix.Close(h.fd) }
