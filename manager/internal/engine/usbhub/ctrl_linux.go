//go:build linux

package usbhub

import (
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// usbfsControl opens the hub's node (it needs write access: the eirl-usbhub group) and does one
// control transfer with USBDEVFS_CONTROL.
func usbfsControl(node string, reqType, req uint8, value, index uint16, length int) ([]byte, error) {
	fd, err := unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: node, Err: err}
	}
	defer unix.Close(fd)
	ct := ctrlTransfer{ReqType: reqType, Req: req, Value: value, Index: index, Length: uint16(length), Timeout: controlTimeoutMs}
	var buf []byte
	if length > 0 {
		buf = make([]byte, length)
		ct.Data = unsafe.Pointer(&buf[0])
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), usbdevfsControl, uintptr(unsafe.Pointer(&ct)))
	runtime.KeepAlive(buf)
	if errno != 0 {
		return nil, &os.PathError{Op: "USBDEVFS_CONTROL", Path: node, Err: errno}
	}
	return buf, nil
}
