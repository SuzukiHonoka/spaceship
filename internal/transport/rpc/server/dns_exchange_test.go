package server

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/dnswire"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func startTestDNSTCPServer(t *testing.T, handler dns.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := &dns.Server{Listener: listener, Handler: handler}
	go func() {
		_ = srv.ActivateAndServe()
	}()
	t.Cleanup(func() {
		_ = srv.Shutdown()
		_ = listener.Close()
	})
	return listener.Addr().String()
}

func TestDnsExchangePreservesWireResponse(t *testing.T) {
	addr := startTestDNSServer(t, dns.HandlerFunc(func(w dns.ResponseWriter, query *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(query)
		response.Authoritative = true
		response.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{
				Name:   query.Question[0].Name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    120,
			},
			A: net.ParseIP("192.0.2.44").To4(),
		}}
		response.Extra = []dns.RR{&dns.TXT{
			Hdr: dns.RR_Header{
				Name:   query.Question[0].Name,
				Rrtype: dns.TypeTXT,
				Class:  dns.ClassINET,
				Ttl:    120,
			},
			Txt: []string{"preserved additional data"},
		}}
		_ = w.WriteMsg(response)
	}))
	server := &Server{
		dnsAddr:   addr,
		dnsClient: &dns.Client{Timeout: time.Second},
	}

	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeA)
	query.SetEdns0(1232, true)
	wireQuery, _ := query.Pack()
	result, err := server.DnsExchange(context.Background(), &proto.DnsExchangeRequest{
		WireQuery: wireQuery,
		Network:   proto.Network_UDP,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := new(dns.Msg)
	if err := response.Unpack(result.WireResponse); err != nil {
		t.Fatal(err)
	}
	if !response.Response || !response.Authoritative ||
		!dnswire.QuestionsEqual(query, response) ||
		len(response.Answer) != 1 ||
		len(response.Extra) != 1 {
		t.Fatalf("DnsExchange response = %+v", response)
	}
}

func TestDnsExchangeUsesTCPUpstreamWithoutUDPTruncation(t *testing.T) {
	addr := startTestDNSTCPServer(t, dns.HandlerFunc(func(w dns.ResponseWriter, query *dns.Msg) {
		response := largeDNSResponse(query)
		_ = w.WriteMsg(response)
	}))
	server := &Server{
		dnsAddr:   addr,
		dnsClient: &dns.Client{Timeout: time.Second},
	}

	query := new(dns.Msg)
	query.SetQuestion("large.example.", dns.TypeTXT)
	wireQuery, _ := query.Pack()
	result, err := server.DnsExchange(context.Background(), &proto.DnsExchangeRequest{
		WireQuery: wireQuery,
		Network:   proto.Network_TCP,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.WireResponse) <= dns.MinMsgSize {
		t.Fatalf("TCP response size = %d, want larger than classic UDP limit", len(result.WireResponse))
	}
	response := new(dns.Msg)
	if err := response.Unpack(result.WireResponse); err != nil {
		t.Fatal(err)
	}
	if response.Truncated || len(response.Answer) != 12 {
		t.Fatalf("TCP response truncated=%t answers=%d", response.Truncated, len(response.Answer))
	}
}

func TestDnsExchangeTruncatesUDPToAdvertisedSize(t *testing.T) {
	addr := startTestDNSServer(t, dns.HandlerFunc(func(w dns.ResponseWriter, query *dns.Msg) {
		response := largeDNSResponse(query)
		_ = w.WriteMsg(response)
	}))
	server := &Server{
		dnsAddr:   addr,
		dnsClient: &dns.Client{Timeout: time.Second},
	}

	query := new(dns.Msg)
	query.SetQuestion("large.example.", dns.TypeTXT)
	wireQuery, _ := query.Pack()
	result, err := server.DnsExchange(context.Background(), &proto.DnsExchangeRequest{
		WireQuery: wireQuery,
		Network:   proto.Network_UDP,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.WireResponse) > dns.MinMsgSize {
		t.Fatalf("UDP response size = %d, want at most %d", len(result.WireResponse), dns.MinMsgSize)
	}
	response := new(dns.Msg)
	if err := response.Unpack(result.WireResponse); err != nil {
		t.Fatal(err)
	}
	if !response.Truncated {
		t.Fatal("oversized UDP response did not set the TC bit")
	}
}

func largeDNSResponse(query *dns.Msg) *dns.Msg {
	response := new(dns.Msg)
	response.SetReply(query)
	for range 12 {
		response.Answer = append(response.Answer, &dns.TXT{
			Hdr: dns.RR_Header{
				Name:   query.Question[0].Name,
				Rrtype: dns.TypeTXT,
				Class:  dns.ClassINET,
				Ttl:    60,
			},
			Txt: []string{strings.Repeat("x", 100)},
		})
	}
	return response
}

func TestDnsExchangeBlocksAAAAWithoutResolverFallback(t *testing.T) {
	server := new(Server)
	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeAAAA)
	wireQuery, _ := query.Pack()

	result, err := server.DnsExchange(context.Background(), &proto.DnsExchangeRequest{
		WireQuery: wireQuery,
		Network:   proto.Network_UDP,
		BlockIpv6: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := new(dns.Msg)
	if err := response.Unpack(result.WireResponse); err != nil {
		t.Fatal(err)
	}
	if response.Rcode != dns.RcodeSuccess || len(response.Answer) != 0 {
		t.Fatalf("blocked AAAA response = %+v", response)
	}
}

func TestDnsExchangeReturnsSERVFAILOnUpstreamFailure(t *testing.T) {
	server := &Server{
		dnsAddr: "127.0.0.1:1",
		dnsClient: &dns.Client{
			Timeout: 50 * time.Millisecond,
		},
	}
	query := new(dns.Msg)
	query.SetQuestion("failure.example.", dns.TypeA)
	wireQuery, _ := query.Pack()
	before := DNSExchangeStatistics()

	result, err := server.DnsExchange(context.Background(), &proto.DnsExchangeRequest{
		WireQuery: wireQuery,
		Network:   proto.Network_UDP,
	})
	if err != nil {
		t.Fatalf("DnsExchange returned gRPC error instead of DNS SERVFAIL: %v", err)
	}
	response := new(dns.Msg)
	if err := response.Unpack(result.WireResponse); err != nil {
		t.Fatal(err)
	}
	if response.Rcode != dns.RcodeServerFailure ||
		!dnswire.QuestionsEqual(query, response) {
		t.Fatalf("upstream failure response = %+v", response)
	}
	after := DNSExchangeStatistics()
	if after.ForwardedTotal-before.ForwardedTotal != 1 ||
		after.UpstreamFailuresTotal-before.UpstreamFailuresTotal != 1 {
		t.Fatalf("upstream counter deltas: before=%+v after=%+v", before, after)
	}
}

func TestDnsExchangeReturnsResourceExhaustedWhenServerAdmissionIsFull(t *testing.T) {
	limits := normalizedDNSLimits(t, config.DNSExchange{
		MaxConcurrent:        1,
		MaxConcurrentPerUser: 1,
		QueriesPerSecond:     10,
		QueriesPerUser:       10,
		Burst:                1,
		BurstPerUser:         1,
	})
	admission := newDNSAdmission(limits, config.Users{{UUID: "test-user"}})
	release, rejection := admission.acquire("")
	if rejection != dnsAdmissionAllowed {
		t.Fatalf("reserve admission rejection = %v", rejection)
	}
	defer release()

	server := &Server{dnsAdmission: admission}
	query := new(dns.Msg)
	query.SetQuestion("overload.example.", dns.TypeA)
	wireQuery, _ := query.Pack()
	before := DNSExchangeStatistics()

	result, err := server.DnsExchange(context.Background(), &proto.DnsExchangeRequest{
		WireQuery: wireQuery,
		Network:   proto.Network_UDP,
	})
	if status.Code(err) != codes.ResourceExhausted || result != nil {
		t.Fatalf("DnsExchange() = (%v, %v), want nil ResourceExhausted", result, err)
	}

	after := DNSExchangeStatistics()
	if got := after.RequestsTotal - before.RequestsTotal; got != 1 {
		t.Fatalf("request counter delta = %d, want 1", got)
	}
	if got := after.RejectedGlobalConcurrencyTotal -
		before.RejectedGlobalConcurrencyTotal; got != 1 {
		t.Fatalf("global concurrency rejection delta = %d, want 1", got)
	}
	if got := after.ForwardedTotal - before.ForwardedTotal; got != 0 {
		t.Fatalf("forwarded counter delta = %d, want 0", got)
	}
}

func TestDnsExchangeBlockedAAAAIsStillAdmissionControlled(t *testing.T) {
	limits := normalizedDNSLimits(t, config.DNSExchange{
		MaxConcurrent:        1,
		MaxConcurrentPerUser: 1,
		QueriesPerSecond:     10,
		QueriesPerUser:       10,
		Burst:                1,
		BurstPerUser:         1,
	})
	admission := newDNSAdmission(limits, config.Users{{UUID: "test-user"}})
	release, rejection := admission.acquire("")
	if rejection != dnsAdmissionAllowed {
		t.Fatalf("reserve admission rejection = %v", rejection)
	}
	defer release()

	server := &Server{dnsAdmission: admission}
	query := new(dns.Msg)
	query.SetQuestion("blocked.example.", dns.TypeAAAA)
	wireQuery, _ := query.Pack()
	before := DNSExchangeStatistics()

	result, err := server.DnsExchange(context.Background(), &proto.DnsExchangeRequest{
		WireQuery: wireQuery,
		Network:   proto.Network_UDP,
		BlockIpv6: true,
	})
	if status.Code(err) != codes.ResourceExhausted || result != nil {
		t.Fatalf("DnsExchange() = (%v, %v), want nil ResourceExhausted", result, err)
	}

	after := DNSExchangeStatistics()
	if got := after.BlockedIPv6Total - before.BlockedIPv6Total; got != 0 {
		t.Fatalf("blocked IPv6 counter delta = %d, want 0 for rejected request", got)
	}
	if got := after.RejectedGlobalConcurrencyTotal -
		before.RejectedGlobalConcurrencyTotal; got != 1 {
		t.Fatalf("admission rejection delta = %d, want 1", got)
	}
}

func TestDnsExchangeRejectsMismatchedUpstreamQuestion(t *testing.T) {
	addr := startTestDNSServer(t, dns.HandlerFunc(func(w dns.ResponseWriter, query *dns.Msg) {
		other := new(dns.Msg)
		other.SetQuestion("poison.example.", dns.TypeA)
		other.Id = query.Id
		response := new(dns.Msg)
		response.SetReply(other)
		_ = w.WriteMsg(response)
	}))
	server := &Server{
		dnsAddr:   addr,
		dnsClient: &dns.Client{Timeout: time.Second},
	}
	query := new(dns.Msg)
	query.SetQuestion("expected.example.", dns.TypeA)
	wireQuery, _ := query.Pack()

	result, err := server.DnsExchange(context.Background(), &proto.DnsExchangeRequest{
		WireQuery: wireQuery,
		Network:   proto.Network_UDP,
	})
	if err != nil {
		t.Fatalf("DnsExchange returned gRPC error instead of DNS SERVFAIL: %v", err)
	}
	response := new(dns.Msg)
	if err := response.Unpack(result.WireResponse); err != nil {
		t.Fatal(err)
	}
	if response.Rcode != dns.RcodeServerFailure ||
		!dnswire.QuestionsEqual(query, response) {
		t.Fatalf("mismatched upstream response = %+v", response)
	}
}

func TestDnsExchangeRejectsInvalidRequests(t *testing.T) {
	server := new(Server)
	before := DNSExchangeStatistics()
	tests := []struct {
		name    string
		request *proto.DnsExchangeRequest
	}{
		{name: "nil"},
		{name: "malformed", request: &proto.DnsExchangeRequest{WireQuery: []byte{0xff}}},
		{
			name: "network",
			request: func() *proto.DnsExchangeRequest {
				query := new(dns.Msg)
				query.SetQuestion("example.com.", dns.TypeA)
				wire, _ := query.Pack()
				return &proto.DnsExchangeRequest{
					WireQuery: wire,
					Network:   proto.Network(99),
				}
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := server.DnsExchange(context.Background(), tt.request)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("DnsExchange() error = %v, want InvalidArgument", err)
			}
		})
	}
	after := DNSExchangeStatistics()
	if after.RequestsTotal-before.RequestsTotal != uint64(len(tests)) ||
		after.InvalidRequestsTotal-before.InvalidRequestsTotal != uint64(len(tests)) {
		t.Fatalf("invalid request deltas: before=%+v after=%+v", before, after)
	}
}

func TestDnsExchangeAdmissionPrecedesParsing(t *testing.T) {
	limits := normalizedDNSLimits(t, config.DNSExchange{
		MaxConcurrent:        1,
		MaxConcurrentPerUser: 1,
		QueriesPerSecond:     10,
		QueriesPerUser:       10,
		Burst:                10,
		BurstPerUser:         10,
	})
	admission := newDNSAdmission(limits, config.Users{{UUID: "test-user"}})
	release, rejection := admission.acquire("")
	if rejection != dnsAdmissionAllowed {
		t.Fatal(rejection)
	}
	defer release()
	server := &Server{dnsAdmission: admission}
	before := DNSExchangeStatistics()

	_, err := server.DnsExchange(context.Background(), &proto.DnsExchangeRequest{
		WireQuery: []byte{0xff},
		Network:   proto.Network(99),
	})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("DnsExchange() error = %v, want ResourceExhausted", err)
	}
	after := DNSExchangeStatistics()
	if after.InvalidRequestsTotal != before.InvalidRequestsTotal {
		t.Fatal("overloaded malformed request was parsed before admission")
	}
	if after.RejectedGlobalConcurrencyTotal-before.RejectedGlobalConcurrencyTotal != 1 {
		t.Fatalf("admission rejection delta: before=%+v after=%+v", before, after)
	}
}

func TestDNSNetwork(t *testing.T) {
	if got, err := dnsNetwork(proto.Network_TCP); err != nil || got != "tcp" {
		t.Fatalf("dnsNetwork(TCP) = (%q, %v)", got, err)
	}
	if got, err := dnsNetwork(proto.Network_UDP); err != nil || got != "udp" {
		t.Fatalf("dnsNetwork(UDP) = (%q, %v)", got, err)
	}
	if _, err := dnsNetwork(proto.Network(5)); err == nil {
		t.Fatal("dnsNetwork() accepted unknown network")
	}
}

func TestExchangeDNSMessageRejectsUnconfiguredResolver(t *testing.T) {
	_, err := new(Server).exchangeDNSMessage(context.Background(), new(dns.Msg), "udp")
	if err == nil || err.Error() != "resolver is not configured" {
		t.Fatalf("exchangeDNSMessage() error = %v", err)
	}
}
