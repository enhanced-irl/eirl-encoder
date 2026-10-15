//go:build linux

package rtmpsend

import (
	"net"

	"golang.org/x/sys/unix"
)

// notsentLowat bounds what the kernel keeps unsent (TCP_NOTSENT_LOWAT): beyond it a write blocks, so
// the backlog of a slow link builds up in the sender's queue, where it is measured and dropped by
// keyframes, and not in a socket buffer of several seconds.
const notsentLowat = 256 << 10

func tuneSocket(c *net.TCPConn) {
	if raw, err := c.SyscallConn(); err == nil {
		raw.Control(func(fd uintptr) {
			unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT, notsentLowat)
		})
	}
}

// sockStats reads TCP_INFO: the smoothed RTT (tcpi_rtt, µs → ms) and the bytes acknowledged
// (tcpi_bytes_acked, Linux ≥ 4.1; 0 if older); and the bytes the kernel has not sent yet (SIOCOUTQNSD,
// the same as tcpi_notsent_bytes; SIOCOUTQ, which also counts the bytes in flight, if it lacks it).
func sockStats(c *net.TCPConn) (s sockInfo) {
	raw, err := c.SyscallConn()
	if err != nil {
		return s
	}
	raw.Control(func(fd uintptr) {
		info, err := unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
		if err != nil {
			return
		}
		s.rttMs, s.acked = float64(info.Rtt)/1000, int64(info.Bytes_acked)
		if n, err := unix.IoctlGetInt(int(fd), unix.SIOCOUTQNSD); err == nil {
			s.unsent = n
		} else if n, err := unix.IoctlGetInt(int(fd), unix.SIOCOUTQ); err == nil {
			s.unsent = n
		}
	})
	return s
}
