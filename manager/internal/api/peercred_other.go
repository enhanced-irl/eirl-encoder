//go:build !linux

package api

import "net"

// peerCred: outside Linux (tests on the Mac), without SO_PEERCRED.
func peerCred(*net.UnixConn) Peer { return Peer{UID: -1, PID: -1} }
