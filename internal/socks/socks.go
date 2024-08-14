package socks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"log"
	"net"
)

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
	Ctx      context.Context
	Config   *Config
	Listener net.Listener
}

// New creates a new Server and potentially returns an error
func New(ctx context.Context, conf *Config) *Server {
	server := &Server{
		Ctx:    ctx,
		Config: conf,
	}
	return server
}

// ListenAndServe is used to create a listener and serve on it
func (s *Server) ListenAndServe(network, addr string) error {
	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	s.Listener = l
	log.Printf("socks started at %s", addr)
	return s.Serve()
}

func (s *Server) Close() error {
	if s.Listener != nil {
		return s.Listener.Close()
	}
	return nil
}

// Serve is used to serve connections from a listener
func (s *Server) Serve() error {
	for {
		conn, err := s.Listener.Accept()
		if err != nil {
			select {
			case <-s.Ctx.Done():
				return nil
			default:
				return err
			}
		}
		go func() {
			if err := s.ServeConn(conn); err != nil {
				log.Printf("[ERR] socks: %v", err)
			}
		}()
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
		if errors.Is(err, unrecognizedAddrType) {
			if err := sendReply(conn, addrTypeNotSupported, nil); err != nil {
				return fmt.Errorf("failed to send reply: %v", err)
			}
		}
		return fmt.Errorf("failed to read destination address: %v", err)
	}
	request.AuthContext = authContext
	if client, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		request.RemoteAddr = &AddrSpec{IP: client.IP, Port: uint16(client.Port)}
	}

	// Process the client request
	if err := s.handleRequest(request, conn); err != nil {
		err = fmt.Errorf("failed to handle request: %v", err)
		log.Printf("[ERR] socks: %v", err)
		return err
	}

	return nil
}
