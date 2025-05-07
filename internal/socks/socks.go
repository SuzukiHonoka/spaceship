package socks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"log"
	"net"
	"sync"
)

var ErrIllegalRequest = errors.New("illegal request")

const (
	socks5Version = uint8(5)
)

// Config is used to set up and configure a Server
type Config struct {
	// If provided, username/password authentication is enabled,
	// by appending a UserPassAuthenticator to AuthMethods. If not provided,
	// and AUthMethods is nil, then "auth-less" mode is enabled.
	Credentials StaticCredentials
}

// Server is responsible for accepting connections and handling
// the details of the SOCKS5 protocol
type Server struct {
	ctx       context.Context
	config    *Config
	listener  net.Listener
	closeOnce sync.Once
}

// New creates a new Server and potentially returns an error
func New(ctx context.Context, cfg *Config) *Server {
	server := &Server{
		ctx:    ctx,
		config: cfg,
	}
	return server
}

// ListenAndServe is used to create a listener and serve on it
func (s *Server) ListenAndServe(network, addr string) error {
	l, err := net.Listen(network, addr)
	if err != nil {
		return fmt.Errorf("failed to listen addr [%s] %s: %v", network, addr, err)
	}
	s.listener = l

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
		return s.ctx.Err()
	}
}

func (s *Server) Close() (err error) {
	if s.listener == nil {
		return nil
	}

	s.closeOnce.Do(func() {
		log.Println("socks: shutting down")
		err = s.listener.Close()
	})
	return err
}

// Serve is used to serve connections from a listener
func (s *Server) Serve() error {
	log.Printf("socks started at %s", s.listener.Addr())
	for {
		select {
		case <-s.ctx.Done():
			return nil
		default:
			conn, err := s.listener.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return nil // normal shutdown
				}
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					continue
				}
				return err
			}
			go func() {
				if err := s.ServeConn(conn); err != nil {
					log.Printf("[ERR] socks: %v", err)
				}
			}()
		}
	}
}

// ServeConn is used to serve a single connection.
func (s *Server) ServeConn(conn net.Conn) error {
	defer utils.Close(conn)
	bufConn := bufio.NewReader(conn)

	// Read the version byte
	version := []byte{0}
	if _, err := bufConn.Read(version); err != nil {
		err = fmt.Errorf("failed to get version byte: %v", err)
		log.Printf("[ERR] socks: %v", err)
		return err
	}

	// Ensure we are compatible
	if version[0] != socks5Version {
		err := fmt.Errorf("unsupported socks version: %v", version)
		log.Printf("[ERR] socks: %v", err)
		return err
	}

	// Authenticate the connection
	authContext, err := s.authenticate(conn, bufConn)
	if err != nil {
		err = fmt.Errorf("failed to authenticate: %v", err)
		log.Printf("[ERR] socks: %v", err)
		return err
	}

	request, err := NewRequest(bufConn)
	if err != nil {
		if errors.Is(err, ErrUnrecognizedAddrType) {
			if err := sendReply(conn, addrTypeNotSupported, nil); err != nil {
				return fmt.Errorf("failed to send reply: %v", err)
			}
		}
		return fmt.Errorf("failed to read destination address: %v", err)
	}
	request.AuthContext = authContext
	if client, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		if client.Port < 0 || client.Port > 65535 {
			return fmt.Errorf("%w: invalid port: %d", ErrIllegalRequest, client.Port)
		}
		request.RemoteAddr = &AddrSpec{IP: client.IP, Port: uint16(client.Port)}
	}

	// Process the client request
	if err = s.handleRequest(request, conn); err != nil {
		err = fmt.Errorf("failed to handle request: %v", err)
		log.Printf("[ERR] socks: %v", err)
		return err
	}

	return nil
}
