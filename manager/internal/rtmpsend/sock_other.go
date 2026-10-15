//go:build !linux

package rtmpsend

import "net"

func tuneSocket(*net.TCPConn) {}

// sockStats has no TCP_INFO outside Linux: no RTT, nothing unsent, no acknowledged bytes (the tests on
// the Mac).
func sockStats(*net.TCPConn) sockInfo {
	return sockInfo{}
}
