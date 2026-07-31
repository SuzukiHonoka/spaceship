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

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
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
	Ctx                   context.Context
	srv                   *grpc.Server
	dnsAddr               string
	dnsClient             *mdns.Client
	dnsAdmission          *dnsAdmission
	proxyAdmission        *admission
	proxyHandshakeTimeout time.Duration
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

func NewServer(
	ctx context.Context,
	users config.Users,
	ssl *config.SSL,
	dnsConfig *dns.DNS,
	options ...Option,
) (*Server, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// check users
	if len(users) == 0 {
		return nil, errors.New("users can not be empty")
	}
	seenUsers := make(map[string]struct{}, len(users))
	for i, user := range users {
		if user == nil {
			return nil, fmt.Errorf("user %d is nil", i)
		}
		if user.UUID == "" {
			return nil, fmt.Errorf("user %d uuid can not be empty", i)
		}
		if _, exists := seenUsers[user.UUID]; exists {
			return nil, fmt.Errorf("duplicate user uuid %q", user.UUID)
		}
		seenUsers[user.UUID] = struct{}{}
	}
	serverOptions, err := normalizeServerOptions(options)
	if err != nil {
		return nil, err
	}

	// create server and register
	var transportOption grpc.ServerOption

	// apply tls if set
	if ssl != nil {
		tlsConfig, err := buildTLSConfig(ssl.PublicKey, ssl.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("setup tls: %w", err)
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
	s := grpc.NewServer(append(rpc.ServerOptions(),
		transportOption,
		grpc.UnaryInterceptor(rpc.UnaryServerAuthInterceptor(matchMap.Match)),
		grpc.StreamInterceptor(rpc.StreamServerAuthInterceptor(matchMap.Match)),
	)...)
	wrapper := &Server{
		Ctx:            ctx,
		srv:            s,
		dnsAddr:        dnsAddr,
		dnsClient:      &mdns.Client{Timeout: DNSClientTimeout},
		dnsAdmission:   newDNSAdmission(serverOptions.dnsExchange, users),
		proxyAdmission: newProxyAdmission(serverOptions.proxySessions, users),
		proxyHandshakeTimeout: time.Duration(
			serverOptions.proxySessions.HandshakeTimeoutSeconds,
		) * time.Second,
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
	return s.serve(listener)
}

func (s *Server) serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("rpc: nil listener")
	}

	trackedListener, ok := listener.(*connectionTrackingListener)
	if !ok {
		trackedListener = newConnectionTrackingListener(listener)
	}
	defer utils.Close(trackedListener)
	log.Printf("rpc: listening at %s", trackedListener.Addr())

	serveDone := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-s.Ctx.Done():
			log.Println("rpc: stopping all connections")
			// Close application-owned sockets before Stop. In particular, this
			// releases grpc-go's serveWG entries that have not completed their
			// TLS/HTTP2 handshake and are therefore absent from grpc's transport
			// registry.
			if err := trackedListener.Close(); err != nil {
				log.Printf("rpc: close listener and connections: %v", err)
			}
			s.srv.Stop()
		case <-serveDone:
		}
	}()

	err := s.srv.Serve(trackedListener)
	close(serveDone)
	<-watchDone
	if s.Ctx.Err() != nil &&
		(err == nil || errors.Is(err, grpc.ErrServerStopped) || errors.Is(err, net.ErrClosed)) {
		return s.Ctx.Err()
	}
	return err
}

func (s *Server) Proxy(stream proto.Proxy_ProxyServer) error {
	if stream == nil {
		return status.Error(codes.InvalidArgument, "proxy: nil stream")
	}

	globalProxySessionCounters.requests.Add(1)
	userID, _ := rpc.UserIDFromContext(stream.Context())
	release, rejection := s.proxyAdmission.acquire(userID)
	if rejection != admissionAllowed {
		recordProxyAdmissionRejection(rejection)
		return status.Error(codes.ResourceExhausted, "proxy: session capacity exhausted")
	}
	defer release()
	globalProxySessionCounters.admitted.Add(1)
	globalProxySessionCounters.active.Add(1)
	defer globalProxySessionCounters.active.Add(-1)

	serverCtx := s.Ctx
	if serverCtx == nil {
		serverCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(stream.Context())
	stopServerCancel := context.AfterFunc(serverCtx, cancel)
	defer stopServerCancel()
	defer cancel()

	handshakeTimeout := s.proxyHandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = config.DefaultProxyHandshakeTimeout
	}
	firstMessage, err := receiveProxyFirstMessage(ctx, stream, handshakeTimeout)
	if err != nil {
		if errors.Is(err, errProxyFirstMessageTimeout) {
			globalProxySessionCounters.handshakeTimeouts.Add(1)
			return status.Error(codes.DeadlineExceeded, "proxy: first message timeout")
		}
		return err
	}

	// create forwarder
	f := NewForwarder(ctx, stream)
	f.firstMessage = firstMessage
	defer utils.Close(f)

	if err := f.Start(); err != nil && err != io.EOF && !errors.Is(err, context.Canceled) {
		if ev, ok := status.FromError(err); ok {
			if ev.Code() == codes.Canceled {
				return nil
			}
		}
		// One readable line, e.g.:
		//   rpc: proxy 199.96.58.85:443 failed: dial: dial tcp …: connection timed out
		if target := f.Target(); target != "" {
			log.Printf("rpc: proxy %s failed: %v", target, err)
		} else {
			log.Printf("rpc: proxy failed: %v", err)
		}
	}
	// send session end to client
	return stream.Send(&proto.ProxyDST{
		Status: proto.ProxyStatus_EOF,
	})
}
