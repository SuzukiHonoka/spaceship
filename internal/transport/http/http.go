package http

import (
	"context"
	"log"
	"net"
	"spaceship/internal/transport/rpc"
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
			err := s.ServeConn(conn)
			if err != nil {
				log.Printf("error when serving http: %s", err)
			}
		}()
	}
}

func (s *Server) ServeConn(conn net.Conn) error {
	defer func(conn net.Conn) {
		_ = conn.Close()
	}(conn)
	f := &Forwarder{
		Ctx:       s.Ctx,
		Transport: rpc.NewClient(),
		Conn:      conn,
	}
	return f.Forward()
}
