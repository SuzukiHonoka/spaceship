package dns

import (
	"context"
	"log"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/dnswire"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"github.com/miekg/dns"
)

var DefaultShutdownTimeout = 3 * time.Second

type Server struct {
	srv          *dns.Server
	blockIPv6DNS bool
	exchanger    WireExchanger
}

func NewServer(addr string, blockIPv6DNS bool) (*Server, error) {
	srv := &Server{
		blockIPv6DNS: blockIPv6DNS,
		exchanger:    NewRPCExchanger(),
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
	if err != nil {
		s.writeErrorResponse(w, r, dns.RcodeServerFailure, err)
		return
	}

	if _, err = w.Write(wireResponse); err != nil {
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
