//go:build linux

package camera

import (
	"encoding/binary"
	"errors"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// V4L2 control ioctls on arm64: _IOWR('V', n, struct size). uvcvideo answers a query from what it cached the first
// time (GET_MIN/MAX/RES/DEF/INFO); getting and setting a value are USB control requests (GET_CUR, SET_CUR).
const (
	vidiocGCtrl     = 0xc008561b // struct v4l2_control (8 B)
	vidiocSCtrl     = 0xc008561c
	vidiocQueryCtrl = 0xc0445624 // struct v4l2_queryctrl (68 B)
	vidiocQueryMenu = 0xc02c5625 // struct v4l2_querymenu (44 B, packed)
)

var le = binary.LittleEndian

type v4l2Device struct{ f *os.File }

func open(node string) (Device, error) {
	f, err := os.OpenFile(node, os.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENODEV) {
			return nil, ErrGone
		}
		return nil, err
	}
	return &v4l2Device{f: f}, nil
}

func (d *v4l2Device) ioctl(req uintptr, buf []byte) error {
	fd := d.f.Fd()
	for {
		_, _, e := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&buf[0])))
		runtime.KeepAlive(buf)
		switch e {
		case 0:
			return nil
		case unix.EINTR:
			continue
		case unix.ENODEV:
			return ErrGone
		default:
			return e
		}
	}
}

func (d *v4l2Device) Query(cid uint32) (Info, error) {
	var b [68]byte
	le.PutUint32(b[0:], cid)
	if err := d.ioctl(vidiocQueryCtrl, b[:]); err != nil {
		if errors.Is(err, unix.EINVAL) {
			return Info{}, ErrAbsent
		}
		return Info{}, err
	}
	if le.Uint32(b[0:]) != cid {
		return Info{}, ErrAbsent
	}
	return Info{Type: le.Uint32(b[4:]), Min: int32(le.Uint32(b[40:])), Max: int32(le.Uint32(b[44:])),
		Step: int32(le.Uint32(b[48:])), Default: int32(le.Uint32(b[52:])), Flags: le.Uint32(b[56:])}, nil
}

func (d *v4l2Device) HasMenu(cid uint32, index int32) bool {
	var b [44]byte
	le.PutUint32(b[0:], cid)
	le.PutUint32(b[4:], uint32(index))
	return d.ioctl(vidiocQueryMenu, b[:]) == nil
}

func (d *v4l2Device) Get(cid uint32) (int32, error) {
	var b [8]byte
	le.PutUint32(b[0:], cid)
	if err := d.ioctl(vidiocGCtrl, b[:]); err != nil {
		return 0, err
	}
	return int32(le.Uint32(b[4:])), nil
}

func (d *v4l2Device) Set(cid uint32, v int32) error {
	var b [8]byte
	le.PutUint32(b[0:], cid)
	le.PutUint32(b[4:], uint32(v))
	return d.ioctl(vidiocSCtrl, b[:])
}

func (d *v4l2Device) Close() error { return d.f.Close() }
