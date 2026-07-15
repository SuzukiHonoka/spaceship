package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	rpcutils "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/utils"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/dns"
	mdns "github.com/miekg/dns"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type Server struct {
	proto.UnimplementedProxyServer
	Ctx       context.Context
	srv       *grpc.Server
	dnsAddr   string
	dnsClient *mdns.Client
}

func buildTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{
		Certificates:     []tls.Certificate{cert},
		MinVersion:       tls.VersionTLS13,
		MaxVersion:       tls.VersionTLS13,
		CurvePreferences: rpc.DefaultCurvePreferences,
	}

	return tlsConfig, nil
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
		tlsConfig, err := buildTLSConfig(ssl.PublicKey, ssl.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("setup tls failed, err=%w", err)
		}
		log.Println("using secure grpc [h2]")
		transportOption = grpc.Creds(credentials.NewTLS(tlsConfig))
	} else {
		log.Println("using insecure grpc [h2c]")
		transportOption = grpc.Creds(insecure.NewCredentials())
	}

	dnsAddr := "8.8.8.8:53" // default to google dns
	if dnsConfig != nil {
		dnsAddr = dnsConfig.Address()
	}

	// create grpc server and register
	matchMap := users.ToMatchMap()
	s := grpc.NewServer(append(rpc.ServerOptions,
		transportOption,
		grpc.UnaryInterceptor(rpc.UnaryServerAuthInterceptor(matchMap.Match)),
		grpc.StreamInterceptor(rpc.StreamServerAuthInterceptor(matchMap.Match)),
	)...)
	wrapper := &Server{
		Ctx:       ctx,
		srv:       s,
		dnsAddr:   dnsAddr,
		dnsClient: &mdns.Client{Timeout: DNSClientTimeout},
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

	serveDone := make(chan struct{})
	go func() {
		select {
		case <-s.Ctx.Done():
		case <-serveDone:
			return
		}
		stopped := make(chan struct{})
		go func() {
			s.srv.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(rpc.GeneralTimeout):
			s.srv.Stop()
		}
	}()

	err = s.srv.Serve(listener)
	close(serveDone)
	if s.Ctx.Err() != nil && (err == nil || errors.Is(err, grpc.ErrServerStopped)) {
		return s.Ctx.Err()
	}
	return err
}

func (s *Server) Proxy(stream proto.Proxy_ProxyServer) error {
	//log.Println("rpc server incomes")
	// cancel forwarder
	ctx, cancel := context.WithCancel(s.Ctx)
	defer cancel()

	// create forwarder
	f := NewForwarder(ctx, stream)
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
	// Auth is handled by the server interceptor.
	// Validate request
	if request == nil || len(request.Items) == 0 {
		return nil, transport.ErrBadRequest
	}

	resp := new(proto.DnsResponse)

	for _, item := range request.Items {
		if item == nil {
			resp.Result = append(resp.Result, &proto.DnsResult{Rcode: mdns.RcodeFormatError})
			continue
		}
		log.Printf("dns: resolving for %s (type %d, blockIPv6: %t)", item.Fqdn, item.QType, item.BlockIpv6)
		result := &proto.DnsResult{Fqdn: item.Fqdn}

		// Safely convert QType from uint32 to uint16 to prevent integer overflow
		qtype, ok := safeUint32ToUint16(item.QType)
		if !ok {
			log.Printf("dns: invalid QType value %d: exceeds uint16 range", item.QType)
			result.Rcode = mdns.RcodeFormatError
			resp.Result = append(resp.Result, result)
			continue
		}

		// Skip IPv6 (AAAA) queries if blocking is enabled
		if item.BlockIpv6 && qtype == mdns.TypeAAAA {
			log.Printf("dns: blocking IPv6 query for %s (AAAA record)", item.Fqdn)
			result.Rcode = mdns.RcodeSuccess
			resp.Result = append(resp.Result, result)
			continue
		}

		// Perform actual DNS resolution using configured DNS server
		records, rcode := s.resolveDNSRecords(item.Fqdn, qtype)
		result.Rcode = uint32(rcode)

		// Filter out IPv6 (AAAA) records if blocking is enabled
		if item.BlockIpv6 {
			filteredRecords := make([]mdns.RR, 0, len(records))
			for _, record := range records {
				if record.Header().Rrtype == mdns.TypeAAAA {
					log.Printf("dns: filtered out IPv6 record for %s", item.Fqdn)
					continue
				}
				filteredRecords = append(filteredRecords, record)
			}
			records = filteredRecords

		}

		if len(records) > 0 {
			// Convert DNS RR records to protobuf format using wire serialization.
			protoRecords, err := rpcutils.ConvertRRSliceToProto(records)
			if err != nil {
				log.Printf("dns: failed to convert DNS records for %s: %v", item.Fqdn, err)
				result.Rcode = mdns.RcodeServerFailure
			} else {
				result.Records = protoRecords
			}
		} else {
			log.Printf("dns: no records found for %s (type %d, rcode %d)", item.Fqdn, item.QType, rcode)
		}

		resp.Result = append(resp.Result, result)
	}

	return resp, nil
}
