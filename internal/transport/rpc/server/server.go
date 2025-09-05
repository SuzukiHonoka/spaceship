package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	rpcutils "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/utils"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/dns"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type Server struct {
	proto.UnimplementedProxyServer
	Ctx           context.Context
	usersMatchMap *config.UsersMatchMap
	srv           *grpc.Server
	dnsAddr       string
}

func NewServer(ctx context.Context, users config.Users, ssl *config.SSL, dnsConfig *dns.DNS) (*Server, error) {
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

	dnsAddr := "8.8.8.8:53" // default to google dns
	if dnsConfig != nil {
		dnsAddr = dnsConfig.Address()
	}

	// create grpc server and register
	s := grpc.NewServer(append(rpc.ServerOptions, transportOption)...)
	wrapper := &Server{
		Ctx:           ctx,
		usersMatchMap: users.ToMatchMap(),
		srv:           s,
		dnsAddr:       dnsAddr,
	}

	// Use dynamic proxy server registration for configurable service names
	dynamicServer := NewDynamicProxyServer(wrapper)
	dynamicServer.RegisterWithGRPC(s)

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
	f := NewForwarder(ctx, s.usersMatchMap, stream)
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

func (s *Server) DnsResolve(_ context.Context, request *proto.DnsRequest) (*proto.DnsResponse, error) {
	//// check user
	//if !s.usersMatchMap.Match(request.Id) {
	//	return nil, fmt.Errorf("%w: uuid=%s", transport.ErrUserNotFound, request.Id)
	//}

	// Validate request
	if request == nil || len(request.Items) == 0 {
		return nil, transport.ErrBadRequest
	}

	resp := new(proto.DnsResponse)

	for _, item := range request.Items {
		log.Printf("dns: resolving for %s (type %d)", item.Fqdn, item.QType)

		// Perform actual DNS resolution using configured DNS server
		records := s.resolveDNSRecords(item.Fqdn, uint16(item.QType))

		if len(records) == 0 {
			log.Printf("dns: no records found for %s (type %d)", item.Fqdn, item.QType)
			continue
		}

		// Convert DNS RR records to protobuf format using wire serialization
		protoRecords, err := rpcutils.ConvertRRSliceToProto(records)
		if err != nil {
			log.Printf("dns: failed to convert DNS records for %s: %v", item.Fqdn, err)
			continue
		}

		// Add to response
		result := &proto.DnsResult{
			Fqdn:    item.Fqdn,
			Records: protoRecords, // New format with complete record data
		}
		resp.Result = append(resp.Result, result)
	}

	return resp, nil
}
