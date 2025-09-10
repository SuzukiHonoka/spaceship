package dns

import (
	"context"
	"log"
	"time"

	rpcClient "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	"github.com/miekg/dns"
)

var DefaultShutdownTimeout = 3 * time.Second

type Server struct {
	srv    *dns.Server
	client *rpcClient.Client
}

func NewServer(addr string) (*Server, error) {
	client, err := rpcClient.New()
	if err != nil {
		return nil, err
	}

	srv := &Server{
		client: client,
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
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true

	dnsReqList := make([]*rpcClient.DnsRequest, 0, len(r.Question))
	for _, question := range r.Question {
		dnsReqList = append(dnsReqList, &rpcClient.DnsRequest{
			Fqdn:  question.Name,
			QType: question.Qtype,
		})
	}

	// Perform DNS resolution via RPC client
	ctx := context.Background()
	results, err := s.client.DnsResolve(ctx, dnsReqList)
	if err != nil {
		log.Printf("DNS resolution via RPC failed: %v", err)
		// Return empty response on error
		if err = w.WriteMsg(m); err != nil {
			log.Printf("Failed to write DNS response: %v", err)
		}
		return
	}

	// Convert RPC results back to DNS format
	m.Answer = results

	// Add all returned DNS records to the answer section
	//for _, rr := range results {
	//	// Check if this record matches any of our questions
	//	for _, question := range r.Question {
	//		if dns.Fqdn(question.Name) == dns.Fqdn(rr.Header().Name) &&
	//			question.Qtype == rr.Header().Rrtype {
	//			m.Answer = append(m.Answer, rr)
	//		}
	//	}
	//}

	//log.Printf("Returning %d DNS answers", len(m.Answer))
	if err = w.WriteMsg(m); err != nil {
		log.Printf("Failed to write DNS response: %v", err)
	}
}

func (s *Server) Start() error {
	log.Printf("dns will listen at %s", s.srv.Addr)
	return s.srv.ListenAndServe()
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
