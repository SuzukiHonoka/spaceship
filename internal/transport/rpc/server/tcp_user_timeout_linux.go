//go:build linux

package server

import (
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

func setTCPUserTimeout(conn net.Conn, timeout time.Duration) error {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return nil
	}
	rawConn, err := tcpConn.SyscallConn()
	if err != nil {
		return fmt.Errorf("access socket: %w", err)
	}

	milliseconds := timeout.Milliseconds()
	maxInt := int64(^uint(0) >> 1)
	if milliseconds <= 0 || milliseconds > maxInt {
		return fmt.Errorf("invalid TCP user timeout %s", timeout)
	}

	var socketErr error
	if err := rawConn.Control(func(fd uintptr) {
		maxIntDescriptor := uintptr(^uint(0) >> 1)
		if fd > maxIntDescriptor {
			socketErr = fmt.Errorf("socket descriptor %d exceeds int range", fd)
			return
		}
		socketErr = unix.SetsockoptInt(
			int(fd), // #nosec G115 -- explicitly bounded above
			unix.IPPROTO_TCP,
			unix.TCP_USER_TIMEOUT,
			int(milliseconds), // #nosec G115 -- explicitly bounded above
		)
	}); err != nil {
		return fmt.Errorf("inspect socket: %w", err)
	}
	if socketErr != nil {
		return fmt.Errorf("set TCP_USER_TIMEOUT: %w", socketErr)
	}
	return nil
}
