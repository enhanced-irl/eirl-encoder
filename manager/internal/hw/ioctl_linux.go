//go:build linux

package hw

import (
	"encoding/binary"
	"errors"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// VIDIOC_SUBDEV_QUERY_DV_TIMINGS = _IOR('V', 99, struct v4l2_dv_timings) (132 bytes: u32 type +
// 128-byte union). The TC358743 subdev is asked, not /dev/video0: that way the VI node the stream
// is using is not touched.
const vidiocQueryDVTimings = 0x80845663

func QueryDVTimings(subdev string) (Signal, error) {
	f, err := os.OpenFile(subdev, os.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		return Signal{}, err
	}
	defer f.Close()
	var buf [132]byte
	_, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), vidiocQueryDVTimings, uintptr(unsafe.Pointer(&buf[0])))
	if e != 0 {
		// ENOLINK: no link · ENOLCK: no sync · ERANGE: out of range
		if e == unix.ENOLINK || e == unix.ENOLCK || e == unix.ERANGE {
			return Signal{}, ErrNoSignal
		}
		return Signal{}, e
	}
	le := binary.LittleEndian
	u32 := func(off int) int { return int(le.Uint32(buf[off:])) }
	// struct v4l2_bt_timings (packed) starts at byte 4
	s := Signal{
		Present:      true,
		Width:        u32(4),
		Height:       u32(8),
		Interlaced:   u32(12) != 0,
		PixelClockHz: le.Uint64(buf[20:]),
	}
	htotal := s.Width + u32(28) + u32(32) + u32(36)
	vtotal := s.Height + u32(40) + u32(44) + u32(48)
	if htotal > 0 && vtotal > 0 {
		s.FPS = float64(s.PixelClockHz) / float64(htotal*vtotal)
	}
	s.HTotal = htotal
	s.fill()
	return s, nil
}

// struct i2c_msg / i2c_rdwr_ioctl_data on arm64 (Go adds the same padding as C)
type i2cMsg struct {
	Addr  uint16
	Flags uint16
	Len   uint16
	Buf   *byte
}

type i2cRdwr struct {
	Msgs  *i2cMsg
	Nmsgs uint32
}

const (
	i2cRDWR = 0x0707
	i2cMRD  = 0x0001
)

// ReadReg8 reads an 8-bit register with a 16-bit address (the TC358743) in a single transaction
// with a repeated start. I2C_RDWR does not check whether a driver owns the address (which is what
// forces i2ctransfer -f) and the kernel serializes it with the driver's own accesses.
func ReadReg8(bus string, addr uint16, reg uint16) (byte, error) {
	f, err := os.OpenFile(bus, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	w := []byte{byte(reg >> 8), byte(reg)}
	r := []byte{0}
	msgs := []i2cMsg{{Addr: addr, Len: 2, Buf: &w[0]}, {Addr: addr, Flags: i2cMRD, Len: 1, Buf: &r[0]}}
	data := i2cRdwr{Msgs: &msgs[0], Nmsgs: 2}
	_, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), i2cRDWR, uintptr(unsafe.Pointer(&data)))
	runtime.KeepAlive(w)
	runtime.KeepAlive(r)
	runtime.KeepAlive(msgs)
	if e != 0 {
		return 0, errors.New("I2C: " + e.Error())
	}
	return r[0], nil
}
