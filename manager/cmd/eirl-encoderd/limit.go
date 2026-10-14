package main

import (
	"net"
	"sync"
)

// limitListener accepts at most n connections at once; the rest wait in the kernel queue.
func limitListener(l net.Listener, n int) net.Listener {
	return &limited{Listener: l, sem: make(chan struct{}, n)}
}

type limited struct {
	net.Listener
	sem chan struct{}
}

func (l *limited) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.sem }}, nil
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

// NetConn: the connection underneath (like tls.Conn): the local socket gets the caller from it (SO_PEERCRED).
func (c *limitedConn) NetConn() net.Conn { return c.Conn }

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
