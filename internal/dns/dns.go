package dns

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/dnswire"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var DefaultShutdownTimeout = 3 * time.Second

type Server struct {
	srv          *dns.Server
	blockIPv6DNS bool
	exchanger    WireExchanger
	legacy       legacyResolver
	legacyNotice sync.Once
}

func NewServer(addr string, blockIPv6DNS bool) (*Server, error) {
	srv := &Server{
		blockIPv6DNS: blockIPv6DNS,
		exchanger:    NewRPCExchanger(),
		legacy:       rpcLegacyResolver{},
	}
	dnsSrv := &dns.Server{
		Addr:    addr,
		Net:     "udp",
		Handler: srv,
	}
	srv.srv = dnsSrv
	return srv, nil
}

func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	wireQuery, err := r.Pack()
	if err != nil {
		s.writeErrorResponse(w, r, dns.RcodeFormatError, err)
		return
	}
	if _, err := dnswire.ParseQuery(wireQuery); err != nil {
		s.writeErrorResponse(w, r, dnswire.QueryErrorRcode(err), nil)
		return
	}

	// Perform DNS resolution via RPC client with a bounded timeout to prevent
	// indefinitely-hanging ServeDNS goroutines when the upstream is slow.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wireResponse, err := s.exchanger.Exchange(ctx, wireQuery, proto.Network_UDP, s.blockIPv6DNS)
	if status.Code(err) == codes.Unimplemented {
		// Servers older than 2.2.0 only implement DnsResolve, which this listener
		// used until then. Keep resolving through the tunnel instead of failing
		// every query for a deployment that upgraded its clients first.
		s.serveLegacy(ctx, w, r)
		return
	}
	if err != nil {
		s.writeErrorResponse(w, r, dns.RcodeServerFailure, err)
		return
	}

	if _, err = w.Write(wireResponse); err != nil {
		log.Printf("dns: write response failed: %v", err)
	}
}

// serveLegacy answers r through the pre-2.2 DnsResolve RPC, producing the same
// response this listener returned before DnsExchange existed.
func (s *Server) serveLegacy(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) {
	s.legacyNotice.Do(func() {
		log.Println("dns: server does not implement DnsExchange; falling back to " +
			"DnsResolve, which drops EDNS and non-answer sections. Upgrade the server")
	})

	answers, rcode, err := s.legacy.Resolve(ctx, r, s.blockIPv6DNS)
	if err != nil {
		s.writeErrorResponse(w, r, dns.RcodeServerFailure, err)
		return
	}
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.Answer = answers
	m.Rcode = rcode
	if err := w.WriteMsg(m); err != nil {
		log.Printf("dns: write response failed: %v", err)
	}
}

func (s *Server) writeErrorResponse(w dns.ResponseWriter, query *dns.Msg, rcode int, cause error) {
	if cause != nil {
		log.Printf("dns: exchange via rpc failed: %v", cause)
	}
	if err := w.WriteMsg(dnswire.ErrorResponse(query, rcode)); err != nil {
		log.Printf("dns: write response failed: %v", err)
	}
}

func (s *Server) Start(ctx context.Context) error {
	log.Printf("dns: listening at %s", s.srv.Addr)

	// Create error channel for server errors
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- s.srv.ListenAndServe()
	}()

	// Wait for context done or server error
	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		utils.Close(s)
		return ctx.Err()
	}
}

func (s *Server) Close() error {
	log.Println("dns: shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), DefaultShutdownTimeout)
	defer cancel()
	if s.srv != nil {
		return s.srv.ShutdownContext(ctx)
	}
	return nil
}
