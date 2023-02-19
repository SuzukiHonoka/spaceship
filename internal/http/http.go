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
	Ctx      context.Context
	Listener net.Listener
}

func New(ctx context.Context) *Server {
	return &Server{
		Ctx: ctx,
	}
}

func (s *Server) Close() error {
	return s.Listener.Close()
}

func (s *Server) ListenAndServe(network, addr string) error {
	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	s.Listener = l
	log.Printf("http started at %s", addr)
	return s.Serve()
}

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
