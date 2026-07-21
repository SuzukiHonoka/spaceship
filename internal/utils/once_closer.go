package utils

import (
	"io"
	"net"
	"sync"
)

// OnceNetConn returns a net.Conn whose Close is idempotent.
//
// Front ends (HTTP/SOCKS) both defer-Close the client connection and pass it
// into transports that also close src/dst to unblock copies. Without this,
// the second Close surfaces "use of closed network connection" as log noise
// even though teardown is otherwise correct.
func OnceNetConn(c net.Conn) net.Conn {
	if c == nil {
		return nil
	}
	if _, ok := c.(*onceConn); ok {
		return c
	}
	return &onceConn{Conn: c}
}

type onceConn struct {
	net.Conn
	once sync.Once
	err  error
}

func (c *onceConn) Close() error {
	c.once.Do(func() {
		if c.Conn != nil {
			c.err = c.Conn.Close()
		}
	})
	return c.err
}

// OnceReadWriteCloser returns an io.ReadWriteCloser whose Close is idempotent.
func OnceReadWriteCloser(c io.ReadWriteCloser) io.ReadWriteCloser {
	if c == nil {
		return nil
	}
	if _, ok := c.(*onceRWC); ok {
		return c
	}
	return &onceRWC{ReadWriteCloser: c}
}

type onceRWC struct {
	io.ReadWriteCloser
	once sync.Once
	err  error
}

func (c *onceRWC) Close() error {
	c.once.Do(func() {
		if c.ReadWriteCloser != nil {
			c.err = c.ReadWriteCloser.Close()
		}
	})
	return c.err
}
