package server

import (
	"context"
	"fmt"
	serverConfig "github.com/SuzukiHonoka/spaceship/internal/config/server"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc"
	proxy "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"log"
)

var Users *serverConfig.Users

type server struct {
	proxy.UnimplementedProxyServer
	Ctx context.Context
}

func NewServer(ctx context.Context, users *serverConfig.Users, ssl *serverConfig.SSL) *grpc.Server {
	// check users
	if users.IsNullOrEmpty() {
		log.Fatalln("users can not be empty")
	}
	// create server and register
	var transportOption grpc.ServerOption
	if ssl != nil {
		credential, err := credentials.NewServerTLSFromFile(ssl.Cert, ssl.Key)
		if err != nil {
			log.Fatalf("failed to setup TLS: %v", err)
		}
		log.Println("using secure grpc [h2]")
		transportOption = grpc.Creds(credential)
	} else {
		log.Println("using insecure grpc [h2c]")
		transportOption = grpc.Creds(insecure.NewCredentials())
	}
	s := grpc.NewServer(append(rpc.ServerOptions, transportOption)...)
	Users = users
	proxy.RegisterProxyServer(s, &server{
		Ctx: ctx,
	})
	return s
}

func (s *server) Proxy(stream proxy.Proxy_ProxyServer) error {
	//log.Println("rpc server incomes")
	// block main until canceled
	ctx, cancel := context.WithCancel(s.Ctx)
	defer cancel()
	// Forwarder
	f := NewForwarder(ctx, stream)
	// target <- client
	go func() {
		err := fmt.Errorf("client -> target error: %w", f.CopyClientToTarget())
		transport.PrintErrorIfCritical(err, "rpc")
		cancel()
	}()
	// target -> client
	go func() {
		err := fmt.Errorf("client <- target error: %w", f.CopyTargetToClient())
		transport.PrintErrorIfCritical(err, "rpc")
		cancel()
	}()
	<-ctx.Done()
	// close target connection
	_ = f.Close()
	// send session end to client
	_ = stream.Send(&proxy.ProxyDST{
		Status: proxy.ProxyStatus_EOF,
	})
	return nil
}
