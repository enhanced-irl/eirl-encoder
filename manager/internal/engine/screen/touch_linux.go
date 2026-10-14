//go:build linux

package screen

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// absMax is the maximum of an absolute axis (EVIOCGABS: struct input_absinfo, 6 int32, the 3rd), at least 1.
func absMax(f *os.File, code int) int32 {
	var info [6]int32
	req := uintptr(0x80184540 + code) // _IOR('E', 0x40 + code, struct input_absinfo)
	rc, err := f.SyscallConn()
	if err != nil {
		return 1
	}
	var errno unix.Errno
	_ = rc.Control(func(fd uintptr) {
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&info[0])))
	})
	if errno != 0 {
		return 1
	}
	return max(1, info[2])
}
