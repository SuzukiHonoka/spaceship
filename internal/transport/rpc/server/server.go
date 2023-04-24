package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	config "github.com/SuzukiHonoka/spaceship/pkg/config/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"log"
)

var Users *config.Users

type server struct {
	proto.UnimplementedProxyServer
	Ctx context.Context
}

func NewServer(ctx context.Context, users *config.Users, ssl *config.SSL) (*grpc.Server, error) {
	// check users
	if users.IsNullOrEmpty() {
		return nil, errors.New("users can not be empty")
	}
	// create server and register
	var transportOption grpc.ServerOption
	if ssl != nil {
		credential, err := credentials.NewServerTLSFromFile(ssl.Cert, ssl.Key)
		if err != nil {
			return nil, fmt.Errorf("failed to setup TLS: %w", err)
		}
		log.Println("using secure grpc [h2]")
		transportOption = grpc.Creds(credential)
	} else {
		log.Println("using insecure grpc [h2c]")
		transportOption = grpc.Creds(insecure.NewCredentials())
	}
	s := grpc.NewServer(append(rpc.ServerOptions, transportOption)...)
	Users = users
	proto.RegisterProxyServer(s, &server{
		Ctx: ctx,
	})
	return s, nil
}

func (s *server) Proxy(stream proto.Proxy_ProxyServer) error {
	//log.Println("rpc server incomes")
	proxyError := make(chan error)
	// block main until canceled
	ctx, cancel := context.WithCancel(s.Ctx)
	defer cancel()
	// Forwarder
	f := NewForwarder(ctx, stream)
	// target <- client
	go func() {
		err := f.CopyClientToTarget()
		proxyError <- fmt.Errorf("client -> target error: %w", err)
	}()
	// target -> client
	go func() {
		err := f.CopyTargetToClient()
		proxyError <- fmt.Errorf("client <- target error: %w", err)
	}()
	err := <-proxyError
	utils.PrintErrorIfCritical(err, "rpc")
	// close target connection
	_ = f.Close()
	// send session end to client
	_ = stream.Send(&proto.ProxyDST{
		Status: proto.ProxyStatus_EOF,
	})
	return nil
}
