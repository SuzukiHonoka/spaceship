package http

import (
	"context"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"log"
	"net"
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
	if s.Listener != nil {
		return s.Listener.Close()
	}
	return nil
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
			if err := s.ServeConn(conn); err != nil {
				utils.PrintErrorIfCritical(err, "http")
			}
		}()
	}
}

func (s *Server) ServeConn(conn net.Conn) error {
	defer utils.ForceClose(conn)
	f := &Forwarder{
		Ctx:  s.Ctx,
		Conn: conn,
	}
	defer utils.ForceClose(conn)
	return f.Forward()
}
