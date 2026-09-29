package socks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

var ErrIllegalRequest = errors.New("illegal request")

const (
	socks5Version           = uint8(5)
	DefaultMaxConnections   = 4096
	DefaultHandshakeTimeout = 15 * time.Second
)

var ErrConnectionLimit = errors.New("socks: connection limit reached")

// Config is used to set up and configure a Server
type Config struct {
	// If provided, username/password authentication is enabled,
	// by appending a UserPassAuthenticator to AuthMethods. If not provided,
	// and AUthMethods is nil, then "auth-less" mode is enabled.
	Credentials StaticCredentials
	// Zero selects the safe defaults. Limits include pending handshakes.
	MaxConnections   int
	HandshakeTimeout time.Duration
}

// Server is responsible for accepting connections and handling
// the details of the SOCKS5 protocol
type Server struct {
	ctx         context.Context
	config      *Config
	listener    net.Listener
	closeOnce   sync.Once
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	closed      bool
	cancel      context.CancelFunc
	handlers    sync.WaitGroup
}

// New creates a new Server and potentially returns an error
func New(ctx context.Context, cfg *Config) *Server {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	settings := Config{}
	if cfg != nil {
		settings = *cfg
	}
	if settings.MaxConnections <= 0 {
		settings.MaxConnections = DefaultMaxConnections
	}
	if settings.HandshakeTimeout <= 0 {
		settings.HandshakeTimeout = DefaultHandshakeTimeout
	}
	server := &Server{
		ctx:         ctx,
		config:      &settings,
		cancel:      cancel,
		connections: make(map[net.Conn]struct{}),
	}
	return server
}

// ListenAndServe is used to create a listener and serve on it
func (s *Server) ListenAndServe(network, addr string) error {
	l, err := net.Listen(network, addr)
	if err != nil {
		return fmt.Errorf("failed to listen addr [%s] %s: %v", network, addr, err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = l.Close()
		return net.ErrClosed
	}
	s.listener = l
	s.mu.Unlock()
	defer utils.Close(s)

	// Create error channel for server errors
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- s.Serve()
	}()

	// Wait for context done or server error
	select {
	case err = <-serverErr:
		return err
	case <-s.ctx.Done():
		utils.Close(s)
		return s.ctx.Err()
	}
}

func (s *Server) Close() (err error) {
	s.closeOnce.Do(func() {
		log.Println("socks: shutting down")
		s.mu.Lock()
		s.closed = true
		listener := s.listener
		connections := make([]net.Conn, 0, len(s.connections))
		for conn := range s.connections {
			connections = append(connections, conn)
		}
		s.mu.Unlock()
		s.cancel()
		if listener != nil {
			err = listener.Close()
		}
		for _, conn := range connections {
			_ = conn.Close()
		}
		s.handlers.Wait()
	})
	return err
}

// Serve is used to serve connections from a listener
func (s *Server) Serve() error {
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()
	if listener == nil {
		return net.ErrClosed
	}
	defer utils.Close(s)
	stop := context.AfterFunc(s.ctx, func() { _ = s.Close() })
	defer stop()
	log.Printf("socks: listening at %s", listener.Addr())
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil // normal shutdown
			}
			if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
				continue
			}
			return err
		}
		conn = utils.OnceNetConn(conn)
		if err := s.admit(conn); err != nil {
			_ = conn.Close()
			continue
		}
		go func() {
			if err := s.serveConn(conn); err != nil {
				log.Printf("socks: %v", err)
			}
		}()
	}
}

// ServeConn is used to serve a single connection.
func (s *Server) ServeConn(conn net.Conn) error {
	// Transports close the client conn (as Proxy dst) to unblock copies; we
	// also defer-Close here. Idempotent Close makes that ownership overlap safe.
	conn = utils.OnceNetConn(conn)
	if err := s.admit(conn); err != nil {
		_ = conn.Close()
		return err
	}
	return s.serveConn(conn)
}

func (s *Server) admit(conn net.Conn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return net.ErrClosed
	}
	if len(s.connections) >= s.config.MaxConnections {
		return ErrConnectionLimit
	}
	s.connections[conn] = struct{}{}
	s.handlers.Add(1)
	return nil
}

func (s *Server) serveConn(conn net.Conn) error {
	defer func() {
		s.mu.Lock()
		delete(s.connections, conn)
		s.mu.Unlock()
		s.handlers.Done()
	}()
	defer utils.Close(conn)
	stop := context.AfterFunc(s.ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(s.config.HandshakeTimeout)); err != nil {
		return err
	}
	bufConn := bufio.NewReader(conn)

	// Read the version byte
	version := []byte{0}
	if _, err := bufConn.Read(version); err != nil {
		err = fmt.Errorf("read version: %w", err)
		log.Printf("socks: %v", err)
		return err
	}

	// Ensure we are compatible
	if version[0] != socks5Version {
		err := fmt.Errorf("unsupported version %d", version[0])
		log.Printf("socks: %v", err)
		return err
	}

	// Authenticate the connection
	authContext, err := s.authenticate(conn, bufConn)
	if err != nil {
		err = fmt.Errorf("authenticate: %w", err)
		log.Printf("socks: %v", err)
		return err
	}

	request, err := NewRequest(bufConn)
	if err != nil {
		if errors.Is(err, ErrUnrecognizedAddrType) {
			if err := sendReply(conn, addrTypeNotSupported, nil); err != nil {
				return fmt.Errorf("send reply: %w", err)
			}
		}
		return fmt.Errorf("read request: %w", err)
	}
	request.AuthContext = authContext
	// The handshake budget must not become a lifetime limit on the tunnel.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	if client, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		if client.Port < 0 || client.Port > 65535 {
			return fmt.Errorf("%w: invalid port: %d", ErrIllegalRequest, client.Port)
		}
		request.RemoteAddr = &AddrSpec{IP: client.IP, Port: uint16(client.Port)}
	}

	// Process the client request
	if err = s.handleRequest(request, conn); err != nil {
		err = fmt.Errorf("handle request: %w", err)
		log.Printf("socks: %v", err)
		return err
	}

	return nil
}
