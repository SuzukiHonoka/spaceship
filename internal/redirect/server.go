package redirect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"golang.org/x/sync/semaphore"
)

var (
	// ErrUnsupported is returned when transparent REDIRECT is enabled on a
	// platform that cannot provide Linux's SO_ORIGINAL_DST socket option.
	ErrUnsupported = errors.New("transparent redirect is only supported on Linux")

	// ErrInvalidOriginalDestination means the kernel returned an address that
	// cannot be used as a TCP target.
	ErrInvalidOriginalDestination = errors.New("invalid original destination")

	// ErrRedirectLoop means a connection was sent directly to the redirect
	// listener, or a firewall rule redirected the listener back into itself.
	ErrRedirectLoop = errors.New("original destination is the redirect listener")

	// ErrInvalidMaxConnections means the configured redirect session limit is
	// negative. Zero selects DefaultMaxConnections.
	ErrInvalidMaxConnections = errors.New("redirect max connections must be non-negative")
)

const DefaultMaxConnections = 1024

type destinationResolver func(net.Conn) (*net.TCPAddr, error)
type routeResolver func(string) (transport.Transport, error)

// Config controls resource use by the transparent redirect listener.
type Config struct {
	// MaxConnections bounds accepted sessions and their proxy goroutines.
	// Zero selects DefaultMaxConnections.
	MaxConnections int
}

// Server accepts TCP connections redirected by Linux netfilter and proxies
// them to the destination recorded by SO_ORIGINAL_DST.
type Server struct {
	parentCtx context.Context
	ctx       context.Context
	cancel    context.CancelFunc

	resolveDestination destinationResolver
	resolveRoute       routeResolver

	mu       sync.Mutex
	listener net.Listener
	conns    map[net.Conn]struct{}
	closed   bool
	handlers sync.WaitGroup
	slots    *semaphore.Weighted

	maxConnections int

	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
}

// New creates a transparent redirect server bound to ctx's lifetime.
func New(ctx context.Context, cfg *Config) (*Server, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	maxConnections := DefaultMaxConnections
	if cfg != nil {
		if cfg.MaxConnections < 0 {
			return nil, fmt.Errorf("%w: %d", ErrInvalidMaxConnections, cfg.MaxConnections)
		}
		if cfg.MaxConnections > 0 {
			maxConnections = cfg.MaxConnections
		}
	}
	// #nosec G118 -- cancel is retained by Server and invoked by Close.
	serverCtx, cancel := context.WithCancel(ctx)
	return &Server{
		parentCtx:          ctx,
		ctx:                serverCtx,
		cancel:             cancel,
		resolveDestination: originalDestination,
		resolveRoute:       router.GetRoute,
		conns:              make(map[net.Conn]struct{}),
		slots:              semaphore.NewWeighted(int64(maxConnections)),
		maxConnections:     maxConnections,
		closeDone:          make(chan struct{}),
	}, nil
}

// ListenAndServe listens for TCP traffic redirected by iptables REDIRECT.
func (s *Server) ListenAndServe(addr string) error {
	if !Supported() {
		return ErrUnsupported
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	return s.serve(listener)
}

func (s *Server) serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("redirect: nil listener")
	}
	if err := s.attachListener(listener); err != nil {
		_ = listener.Close()
		return err
	}

	log.Printf("redirect: listening at %s", listener.Addr())

	watchDone := make(chan struct{})
	go func() {
		select {
		case <-s.ctx.Done():
			_ = s.Close()
		case <-watchDone:
		}
	}()

	err := s.acceptLoop(listener)
	close(watchDone)
	_ = s.Close()

	if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		if ctxErr := s.parentCtx.Err(); ctxErr != nil {
			return ctxErr
		}
		return nil
	}
	return err
}

func (s *Server) attachListener(listener net.Listener) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return net.ErrClosed
	}
	if s.listener != nil {
		return errors.New("redirect: server is already listening")
	}
	s.listener = listener
	return nil
}

func (s *Server) acceptLoop(listener net.Listener) error {
	var delay time.Duration
	for {
		if err := s.acquireSlot(); err != nil {
			return err
		}

		conn, err := listener.Accept()
		if err != nil {
			s.releaseSlot()
			var netErr net.Error
			// net/http.Server uses the same bounded-backoff policy for
			// transient Accept failures. Temporary is deprecated for general
			// network operations but remains the only portable classification
			// exposed by net.Listener implementations.
			if errors.As(err, &netErr) &&
				(netErr.Timeout() || netErr.Temporary()) { //nolint:staticcheck
				if delay == 0 {
					delay = 5 * time.Millisecond
				} else {
					delay *= 2
				}
				if max := time.Second; delay > max {
					delay = max
				}

				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
					continue
				case <-s.ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return s.ctx.Err()
				}
			}
			return err
		}
		delay = 0

		if !s.registerConn(conn) {
			_ = conn.Close()
			s.releaseSlot()
			return net.ErrClosed
		}
		go func() {
			defer s.finishConn(conn)
			if err := s.ServeConn(conn); err != nil &&
				!errors.Is(err, context.Canceled) &&
				!errors.Is(err, io.EOF) &&
				!errors.Is(err, net.ErrClosed) {
				log.Printf("redirect: %v", err)
			}
		}()
	}
}

func (s *Server) acquireSlot() error {
	return s.slots.Acquire(s.ctx, 1)
}

func (s *Server) releaseSlot() {
	s.slots.Release(1)
}

func (s *Server) registerConn(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}
	s.conns[conn] = struct{}{}
	// Add is serialized with Close setting closed under the same lock. Once
	// Close starts waiting, no later Add can race with Wait.
	s.handlers.Add(1)
	return true
}

func (s *Server) finishConn(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
	s.releaseSlot()
	s.handlers.Done()
}

// ServeConn recovers and proxies one redirected TCP connection.
func (s *Server) ServeConn(conn net.Conn) error {
	if conn == nil {
		return errors.New("redirect: nil connection")
	}
	rawConn := conn
	client := utils.OnceNetConn(conn)
	defer utils.Close(client)

	dst, err := s.resolveDestination(rawConn)
	if err != nil {
		return fmt.Errorf("get original destination: %w", err)
	}
	if err := validateDestination(dst); err != nil {
		return err
	}
	if sameEndpoint(dst, rawConn.LocalAddr()) {
		return fmt.Errorf("%w: %s", ErrRedirectLoop, dst)
	}

	host := dst.IP.String()
	route, err := s.resolveRoute(host)
	if err != nil {
		return fmt.Errorf("route %s: %w", host, err)
	}
	defer utils.Close(route)

	target := dst.String()
	log.Printf("redirect: %s -> %s -> %s", rawConn.RemoteAddr(), target, route)

	// Redirect has no application-layer handshake to acknowledge. A one-element
	// buffer lets every transport publish its single dial result without
	// requiring an otherwise-useless waiter goroutine.
	localAddr := make(chan string, 1)
	if err := route.Proxy(s.ctx, target, localAddr, client, client); err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, io.EOF) {
		return fmt.Errorf("proxy %s: %w", target, err)
	}
	return nil
}

func validateDestination(dst *net.TCPAddr) error {
	if dst == nil || dst.IP == nil || (dst.IP.To4() == nil && dst.IP.To16() == nil) ||
		dst.IP.IsUnspecified() || dst.Port < 1 || dst.Port > 65535 {
		return fmt.Errorf("%w: %v", ErrInvalidOriginalDestination, dst)
	}
	return nil
}

func sameEndpoint(dst *net.TCPAddr, local net.Addr) bool {
	localTCP, ok := local.(*net.TCPAddr)
	if !ok || localTCP == nil {
		return false
	}
	return dst.Port == localTCP.Port && dst.IP.Equal(localTCP.IP)
}

// Close stops accepting traffic, cancels outbound work, closes active client
// connections, and waits for every accepted handler to exit. Closing active
// sockets unblocks both copy directions; transports also close them, so
// ServeConn uses an idempotent wrapper.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		listener := s.listener
		conns := make([]net.Conn, 0, len(s.conns))
		for conn := range s.conns {
			conns = append(conns, conn)
		}
		s.mu.Unlock()

		log.Println("redirect: shutting down")
		s.cancel()
		if listener != nil {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				s.closeErr = err
			}
		}
		for _, conn := range conns {
			_ = conn.Close()
		}
		s.handlers.Wait()
		close(s.closeDone)
	})
	<-s.closeDone
	return s.closeErr
}
