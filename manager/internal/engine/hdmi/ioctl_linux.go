//go:build linux

package hdmi

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
)

// VIDIOC_QUERY_DV_TIMINGS = _IOR('V', 99, struct v4l2_dv_timings): 4 bytes of type + the 128-byte union
const vidiocQueryDVTimings = (2 << 30) | (132 << 16) | ('V' << 8) | 99

// queryIoctl asks the driver directly (v4l2-ctl every 1-2 s cost a process each time). ok=false without
// a signal (ENOLINK, ENOLCK, ERANGE); ErrNotImplemented if the driver does not understand it.
func queryIoctl(node string) (pipeline.Timings, bool, error) {
	fd, err := unix.Open(node, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return pipeline.Timings{}, false, ErrNotImplemented
	}
	defer unix.Close(fd)
	var buf [132]byte // type 0 = V4L2_DV_BT_656_1120
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocQueryDVTimings, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		if errors.Is(errno, unix.ENOLINK) || errors.Is(errno, unix.ENOLCK) || errors.Is(errno, unix.ERANGE) {
			return pipeline.Timings{}, false, nil
		}
		return pipeline.Timings{}, false, ErrNotImplemented
	}
	t, ok := FromBT(buf[:])
	return t, ok, nil
}
