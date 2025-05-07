package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"io"
	"log"
	"net"
)

type Server struct {
	proto.UnimplementedProxyServer
	Ctx   context.Context
	Users config.Users
	srv   *grpc.Server
}

func NewServer(ctx context.Context, users config.Users, ssl *config.SSL) (*Server, error) {
	// check users
	if len(users) == 0 {
		return nil, errors.New("users can not be empty")
	}

	// create server and register
	var transportOption grpc.ServerOption

	// apply tls if set
	if ssl != nil {
		credential, err := credentials.NewServerTLSFromFile(ssl.PublicKey, ssl.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("setup tls failed, err=%w", err)
		}
		log.Println("using secure grpc [h2]")
		transportOption = grpc.Creds(credential)
	} else {
		log.Println("using insecure grpc [h2c]")
		transportOption = grpc.Creds(insecure.NewCredentials())
	}

	// create grpc server and register
	s := grpc.NewServer(append(rpc.ServerOptions, transportOption)...)
	wrapper := &Server{
		Ctx:   ctx,
		Users: users,
		srv:   s,
	}
	proto.RegisterProxyServer(s, wrapper)
	return wrapper, nil
}

// ListenAndServe starts the server at the given address
func (s *Server) ListenAndServe(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen at %s error %w", addr, err)
	}
	defer utils.Close(listener)
	log.Printf("rpc started at %s", addr)
	return s.srv.Serve(listener)
}

func (s *Server) Proxy(stream proto.Proxy_ProxyServer) error {
	//log.Println("rpc server incomes")
	// cancel forwarder
	ctx, cancel := context.WithCancel(s.Ctx)
	defer cancel()

	// create forwarder
	f := NewForwarder(ctx, s.Users, stream)
	defer utils.Close(f)

	if err := f.Start(); err != nil && err != io.EOF && !errors.Is(err, context.Canceled) {
		if ev, ok := status.FromError(err); ok {
			if ev.Code() == codes.Canceled {
				return nil
			}
		}
		log.Printf("rpc: forwarder error=%v", err)
	}
	// send session end to client
	return stream.Send(&proto.ProxyDST{
		Status: proto.ProxyStatus_EOF,
	})
}
