//go:build linux

package sources

import (
	"encoding/binary"
	"errors"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// V4L2 ioctls on arm64: _IOR/_IOWR('V', n, struct size). Queries only: opening a UVC node another
// process is capturing and asking it for formats does not disturb it (uvcvideo answers these from
// the descriptors it already read, without USB traffic).
const (
	vidiocQueryCap           = 0x80685600 // struct v4l2_capability (104 B)
	vidiocEnumFmt            = 0xc0405602 // struct v4l2_fmtdesc (64 B)
	vidiocEnumFrameSizes     = 0xc02c564a // struct v4l2_frmsizeenum (44 B)
	vidiocEnumFrameIntervals = 0xc034564b // struct v4l2_frmivalenum (52 B)

	capVideoCapture = 0x00000001
	capDeviceCaps   = 0x80000000
	bufTypeCapture  = 1
	sizeDiscrete    = 1
	ivalDiscrete    = 1

	maxEnum = 64 // cap on iterations per enumeration (a broken firmware does not keep us looping)
)

type v4l2Enum struct{}

var le = binary.LittleEndian

func ioctl(fd uintptr, req uintptr, buf []byte) error {
	for {
		_, _, e := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&buf[0])))
		runtime.KeepAlive(buf)
		switch e {
		case 0:
			return nil
		case unix.EINTR:
			continue
		default:
			return e
		}
	}
}

func (v4l2Enum) Formats(node string) ([]Format, error) {
	f, err := os.OpenFile(node, os.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fd := f.Fd()

	var caps [104]byte
	if err := ioctl(fd, vidiocQueryCap, caps[:]); err != nil {
		return nil, err
	}
	c := le.Uint32(caps[84:])
	if c&capDeviceCaps != 0 {
		c = le.Uint32(caps[88:])
	}
	if c&capVideoCapture == 0 {
		return nil, ErrNotCapture
	}

	var out []Format
	for i := 0; i < maxEnum; i++ {
		var d [64]byte
		le.PutUint32(d[0:], uint32(i))
		le.PutUint32(d[4:], bufTypeCapture)
		if err := ioctl(fd, vidiocEnumFmt, d[:]); err != nil {
			if errors.Is(err, unix.EINVAL) {
				break
			}
			return nil, err
		}
		pix := le.Uint32(d[44:])
		out = append(out, newFormat(fourcc(pix), modes(fd, pix)))
	}
	return out, nil
}

func modes(fd uintptr, pix uint32) []Mode {
	var out []Mode
	for i := 0; i < maxEnum; i++ {
		var s [44]byte
		le.PutUint32(s[0:], uint32(i))
		le.PutUint32(s[4:], pix)
		if ioctl(fd, vidiocEnumFrameSizes, s[:]) != nil {
			break
		}
		var w, h uint32
		if le.Uint32(s[8:]) == sizeDiscrete {
			w, h = le.Uint32(s[12:]), le.Uint32(s[16:])
		} else { // continuous or stepwise (UVC does not use it): the maximum
			w, h = le.Uint32(s[16:]), le.Uint32(s[28:])
		}
		if r := rates(fd, pix, w, h); len(r) > 0 {
			out = append(out, Mode{Width: int(w), Height: int(h), Rates: r})
		}
		if le.Uint32(s[8:]) != sizeDiscrete {
			break
		}
	}
	return out
}

func rates(fd uintptr, pix, w, h uint32) []Rate {
	var out []Rate
	for i := 0; i < maxEnum; i++ {
		var iv [52]byte
		le.PutUint32(iv[0:], uint32(i))
		le.PutUint32(iv[4:], pix)
		le.PutUint32(iv[8:], w)
		le.PutUint32(iv[12:], h)
		if ioctl(fd, vidiocEnumFrameIntervals, iv[:]) != nil {
			break
		}
		// Interval num/den seconds → fps den/num. Continuous or stepwise: the minimum interval.
		num, den := le.Uint32(iv[20:]), le.Uint32(iv[24:])
		if num > 0 && den > 0 {
			out = append(out, Rate{Num: int(den), Den: int(num)})
		}
		if le.Uint32(iv[16:]) != ivalDiscrete {
			break
		}
	}
	return out
}

func fourcc(v uint32) string {
	b := []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			b[i] = '?'
		}
	}
	return string(b)
}
