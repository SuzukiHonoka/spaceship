package server

import (
	"errors"
	"log"
	"net"
	"sync"
	"syscall"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
)

// connectionTrackingListener owns every connection returned from Accept.
//
// grpc.Server.Stop closes listeners first, then waits for its Serve goroutines
// before closing registered HTTP/2 transports. A socket that is still inside
// the TLS or HTTP/2 handshake is not registered yet, so Stop cannot close it and
// waits until grpc.ConnectionTimeout expires. Tracking at the listener boundary
// lets signal-driven shutdown close those raw sockets before calling Stop.
type connectionTrackingListener struct {
	net.Listener

	mu     sync.Mutex
	conns  map[*trackedConn]struct{}
	closed bool

	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
}

func newConnectionTrackingListener(listener net.Listener) *connectionTrackingListener {
	return &connectionTrackingListener{
		Listener:  listener,
		conns:     make(map[*trackedConn]struct{}),
		closeDone: make(chan struct{}),
	}
}

func (l *connectionTrackingListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			if conn != nil {
				_ = conn.Close()
			}
			return nil, err
		}
		if conn == nil {
			return nil, errors.New("rpc: listener returned a nil connection")
		}

		// Wrapping a *net.TCPConn hides its concrete type from grpc-go's Linux
		// TCP_USER_TIMEOUT setup. Apply the same timeout before wrapping so raw
		// connection tracking does not weaken dead-peer detection.
		if err := setTCPUserTimeout(conn, rpc.GeneralTimeout); err != nil {
			log.Printf("rpc: configure accepted connection from %s: %v", conn.RemoteAddr(), err)
			_ = conn.Close()
			continue
		}

		tracked := &trackedConn{Conn: conn, owner: l}

		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			_ = conn.Close()
			return nil, net.ErrClosed
		}
		l.conns[tracked] = struct{}{}
		l.mu.Unlock()

		// Preserve syscall.Conn when the underlying socket exposes it. TLS and
		// channelz use this optional interface to retain access to socket metadata
		// through net.Conn wrappers.
		if syscallConn, ok := conn.(syscall.Conn); ok {
			return &trackedSyscallConn{
				trackedConn: tracked,
				syscallConn: syscallConn,
			}, nil
		}
		return tracked, nil
	}
}

func (l *connectionTrackingListener) unregister(conn *trackedConn) {
	l.mu.Lock()
	delete(l.conns, conn)
	l.mu.Unlock()
}

func (l *connectionTrackingListener) connectionCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.conns)
}

// Close atomically prevents later Accept registrations, closes the underlying
// listener, force-closes every accepted socket, and waits for that work to
// finish. It is safe for the shutdown watcher, grpc.Server, and deferred cleanup
// to call concurrently.
func (l *connectionTrackingListener) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		conns := make([]*trackedConn, 0, len(l.conns))
		for conn := range l.conns {
			conns = append(conns, conn)
		}
		l.mu.Unlock()

		var closeErrors []error
		if err := l.Listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErrors = append(closeErrors, err)
		}
		for _, conn := range conns {
			if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				closeErrors = append(closeErrors, err)
			}
		}
		l.closeErr = errors.Join(closeErrors...)
		close(l.closeDone)
	})

	<-l.closeDone
	return l.closeErr
}

type trackedConn struct {
	net.Conn
	owner *connectionTrackingListener

	closeOnce sync.Once
	closeErr  error
}

func (c *trackedConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
		c.owner.unregister(c)
	})
	return c.closeErr
}

type trackedSyscallConn struct {
	*trackedConn
	syscallConn syscall.Conn
}

func (c *trackedSyscallConn) SyscallConn() (syscall.RawConn, error) {
	return c.syscallConn.SyscallConn()
}

var (
	_ net.Listener = (*connectionTrackingListener)(nil)
	_ net.Conn     = (*trackedConn)(nil)
	_ net.Conn     = (*trackedSyscallConn)(nil)
	_ syscall.Conn = (*trackedSyscallConn)(nil)
)
