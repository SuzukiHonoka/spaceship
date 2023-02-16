package http

import (
	"context"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"io"
	"log"
	"net"
	"os"
)

type Server struct {
	Ctx context.Context
}

func New(ctx context.Context) *Server {
	return &Server{ctx}
}

func (s *Server) ListenAndServe(network, addr string) error {
	l, err := net.Listen(network, addr)
	if err != nil {
		panic(err)
	}
	log.Printf("http started at %s", addr)
	return s.Serve(l)
}

func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			if err := s.ServeConn(conn); err != nil && err != io.EOF && err != os.ErrDeadlineExceeded {
				log.Printf("http: %v", err)
			}
		}()
	}
}

func (s *Server) ServeConn(conn net.Conn) error {
	defer transport.ForceClose(conn)
	f := &Forwarder{
		Ctx:  s.Ctx,
		Conn: conn,
	}
	return f.Forward()
}
