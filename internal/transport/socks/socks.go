package socks

import (
	"bufio"
	"context"
	"fmt"
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

	// Resolver can be provided to do custom name resolution.
	// Defaults to DNSResolver if not provided.
	Resolver []DNSResolver

	// Rules is provided to enable custom logic around permitting
	// various commands. If not provided, PermitAll is used.
	Rules RuleSet
}

// Server is responsible for accepting connections and handling
// the details of the SOCKS5 protocol
type Server struct {
	Ctx    context.Context
	Config *Config
}

// New creates a new Server and potentially returns an error
func New(ctx context.Context, conf *Config) *Server {
	// Ensure we have a DNS resolver
	if conf.Resolver == nil {
		conf.Resolver = []DNSResolver{
			{},
		}
	}
	// Ensure we have a rule set
	if conf.Rules == nil {
		conf.Rules = PermitAll()
	}
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
	log.Printf("socks started at %s", addr)
	return s.Serve(l)
}

// Serve is used to serve connections from a listener
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go s.ServeConn(conn)
	}
}

// ServeConn is used to serve a single connection.
func (s *Server) ServeConn(conn net.Conn) error {
	defer conn.Close()
	bufConn := bufio.NewReader(conn)
	// Read the version byte
	version := []byte{0}
	if _, err := bufConn.Read(version); err != nil {
		log.Printf("[ERR] socks: Failed to get version byte: %v\n", err)
		return err
	}
	// Ensure we are compatible
	if version[0] != socks5Version {
		err := fmt.Errorf("unsupported socks version: %v", version)
		log.Printf("[ERR] socks: %v\n", err)
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
		if err == unrecognizedAddrType {
			if err := sendReply(conn, addrTypeNotSupported, nil); err != nil {
				return fmt.Errorf("failed to send reply: %v", err)
			}
		}
		return fmt.Errorf("failed to read destination address: %v", err)
	}
	request.AuthContext = authContext
	if client, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		request.RemoteAddr = &AddrSpec{IP: client.IP, Port: client.Port}
	}

	// Process the client request
	if err := s.handleRequest(request, conn); err != nil {
		err = fmt.Errorf("failed to handle request: %v", err)
		log.Printf("[ERR] socks: %v", err)
		return err
	}

	return nil
}
