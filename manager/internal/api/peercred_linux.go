package api

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerCred: uid and pid of the other end (SO_PEERCRED).
func peerCred(c *net.UnixConn) Peer {
	p := Peer{UID: -1, PID: -1}
	raw, err := c.SyscallConn()
	if err != nil {
		return p
	}
	_ = raw.Control(func(fd uintptr) {
		if u, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			p = Peer{UID: int(u.Uid), PID: int(u.Pid)}
		}
	})
	return p
}
