package server

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	rpcutils "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/utils"
	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func startTestDNSServer(t *testing.T, handler dns.Handler) string {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := &dns.Server{PacketConn: conn, Handler: handler}
	go func() {
		_ = srv.ActivateAndServe()
	}()
	t.Cleanup(func() {
		_ = srv.Shutdown()
		_ = conn.Close()
	})
	return conn.LocalAddr().String()
}

func TestDnsResolvePreservesUpstreamRcode(t *testing.T) {
	addr := startTestDNSServer(t, dns.HandlerFunc(func(w dns.ResponseWriter, request *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Rcode = dns.RcodeNameError
		_ = w.WriteMsg(response)
	}))
	srv := &Server{
		dnsAddr:   addr,
		dnsClient: &dns.Client{Net: "udp", Timeout: time.Second},
	}

	response, err := srv.DnsResolve(context.Background(), &proto.DnsRequest{Items: []*proto.DnsRequestItem{
		{Fqdn: "missing.example.", QType: uint32(dns.TypeA)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Result) != 1 {
		t.Fatalf("results = %d, want 1", len(response.Result))
	}
	if got := response.Result[0].Rcode; got != dns.RcodeNameError {
		t.Fatalf("rcode = %d, want NXDOMAIN", got)
	}
	if len(response.Result[0].Records) != 0 {
		t.Fatalf("NXDOMAIN records = %d, want 0", len(response.Result[0].Records))
	}
}

func TestDnsResolveReturnsUpstreamRecords(t *testing.T) {
	addr := startTestDNSServer(t, dns.HandlerFunc(func(w dns.ResponseWriter, request *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: request.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("192.0.2.20").To4(),
		}}
		_ = w.WriteMsg(response)
	}))
	srv := &Server{dnsAddr: addr, dnsClient: &dns.Client{Net: "udp", Timeout: time.Second}}

	response, err := srv.DnsResolve(context.Background(), &proto.DnsRequest{Items: []*proto.DnsRequestItem{
		{Fqdn: "example.com.", QType: uint32(dns.TypeA)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Result) != 1 || response.Result[0].Rcode != dns.RcodeSuccess {
		t.Fatalf("result = %+v, want one successful result", response.Result)
	}
	records, err := rpcutils.ConvertProtoToRRSlice(response.Result[0].Records)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].String() != "example.com.\t60\tIN\tA\t192.0.2.20" {
		t.Fatalf("records = %v", records)
	}
}

func TestDnsResolveFiltersIPv6RecordsFromMixedAnswer(t *testing.T) {
	addr := startTestDNSServer(t, dns.HandlerFunc(func(w dns.ResponseWriter, request *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Answer = []dns.RR{
			&dns.A{
				Hdr: dns.RR_Header{
					Name: request.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET,
				},
				A: net.ParseIP("192.0.2.20").To4(),
			},
			&dns.AAAA{
				Hdr: dns.RR_Header{
					Name: request.Question[0].Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET,
				},
				AAAA: net.ParseIP("2001:db8::20"),
			},
		}
		_ = w.WriteMsg(response)
	}))
	srv := &Server{dnsAddr: addr, dnsClient: &dns.Client{Net: "udp", Timeout: time.Second}}

	response, err := srv.DnsResolve(context.Background(), &proto.DnsRequest{Items: []*proto.DnsRequestItem{
		{Fqdn: "example.com.", QType: uint32(dns.TypeA), BlockIpv6: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	records, err := rpcutils.ConvertProtoToRRSlice(response.Result[0].Records)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Header().Rrtype != dns.TypeA {
		t.Fatalf("filtered records = %v, want one A record", records)
	}
}

func TestDnsResolveReturnsResultForLocallyHandledQuestions(t *testing.T) {
	srv := new(Server)
	tests := []struct {
		name string
		item *proto.DnsRequestItem
		want uint32
	}{
		{
			name: "blocked AAAA is empty success",
			item: &proto.DnsRequestItem{Fqdn: "example.com.", QType: uint32(dns.TypeAAAA), BlockIpv6: true},
			want: dns.RcodeSuccess,
		},
		{
			name: "overflow qtype is format error",
			item: &proto.DnsRequestItem{Fqdn: "example.com.", QType: 1 << 16},
			want: dns.RcodeFormatError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := srv.DnsResolve(context.Background(), &proto.DnsRequest{Items: []*proto.DnsRequestItem{tt.item}})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Result) != 1 || response.Result[0].Rcode != tt.want {
				t.Fatalf("result = %+v, want one result with rcode %d", response.Result, tt.want)
			}
		})
	}
}

func TestDnsResolveValidatesAndBoundsLegacyBatches(t *testing.T) {
	srv := new(Server)
	for _, request := range []*proto.DnsRequest{
		nil,
		{},
		{Items: make([]*proto.DnsRequestItem, MaxLegacyDNSItems+1)},
	} {
		if _, err := srv.DnsResolve(context.Background(), request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("DnsResolve(%v) error = %v, want InvalidArgument", request, err)
		}
	}

	response, err := srv.DnsResolve(context.Background(), &proto.DnsRequest{
		Items: []*proto.DnsRequestItem{
			nil,
			{Fqdn: "", QType: uint32(dns.TypeA)},
			{Fqdn: strings.Repeat("a", 256), QType: uint32(dns.TypeA)},
			{Fqdn: "bad..example", QType: uint32(dns.TypeA)},
			{Fqdn: "example.com.", QType: 0},
			{Fqdn: "example.com.", QType: uint32(dns.TypeAXFR)},
			{Fqdn: "example.com.", QType: uint32(dns.TypeIXFR)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{
		dns.RcodeFormatError,
		dns.RcodeFormatError,
		dns.RcodeFormatError,
		dns.RcodeFormatError,
		dns.RcodeFormatError,
		dns.RcodeRefused,
		dns.RcodeRefused,
	}
	if len(response.Result) != len(want) {
		t.Fatalf("results = %d, want %d", len(response.Result), len(want))
	}
	for index, result := range response.Result {
		if result.Rcode != want[index] {
			t.Errorf("result %d rcode = %d, want %d", index, result.Rcode, want[index])
		}
	}
}

func TestDnsResolveAdmissionAppliesBeforeValidationAndPerItem(t *testing.T) {
	limits := normalizedDNSLimits(t, config.DNSExchange{
		MaxConcurrent:        2,
		MaxConcurrentPerUser: 2,
		QueriesPerSecond:     100,
		QueriesPerUser:       1,
		Burst:                10,
		BurstPerUser:         1,
	})
	srv := &Server{dnsAdmission: newDNSAdmission(limits, config.Users{{UUID: "test-user"}})}
	before := DNSExchangeStatistics()

	response, err := srv.DnsResolve(context.Background(), &proto.DnsRequest{
		Items: []*proto.DnsRequestItem{
			{Fqdn: "example.com.", QType: uint32(dns.TypeAAAA), BlockIpv6: true},
			{Fqdn: "example.net.", QType: uint32(dns.TypeAAAA), BlockIpv6: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Result) != 2 ||
		response.Result[0].Rcode != dns.RcodeSuccess ||
		response.Result[1].Rcode != dns.RcodeServerFailure {
		t.Fatalf("per-item admission response = %+v", response.Result)
	}
	after := DNSExchangeStatistics()
	if after.RequestsTotal-before.RequestsTotal != 2 ||
		after.LegacyRequestsTotal-before.LegacyRequestsTotal != 1 ||
		after.LegacyItemsTotal-before.LegacyItemsTotal != 2 ||
		after.RejectedPerUserRateTotal-before.RejectedPerUserRateTotal != 1 {
		t.Fatalf("legacy admission deltas: before=%+v after=%+v", before, after)
	}

	fullLimits := normalizedDNSLimits(t, config.DNSExchange{
		MaxConcurrent:        1,
		MaxConcurrentPerUser: 1,
		QueriesPerSecond:     10,
		QueriesPerUser:       10,
		Burst:                10,
		BurstPerUser:         10,
	})
	fullAdmission := newDNSAdmission(fullLimits, config.Users{{UUID: "test-user"}})
	release, rejection := fullAdmission.acquire("")
	if rejection != dnsAdmissionAllowed {
		t.Fatal(rejection)
	}
	defer release()
	fullServer := &Server{dnsAdmission: fullAdmission}
	invalidBefore := DNSExchangeStatistics()
	_, err = fullServer.DnsResolve(context.Background(), nil)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("DnsResolve(nil) under load error = %v, want ResourceExhausted", err)
	}
	invalidAfter := DNSExchangeStatistics()
	if invalidAfter.InvalidRequestsTotal != invalidBefore.InvalidRequestsTotal {
		t.Fatal("overloaded invalid request was parsed before admission")
	}
}

func TestDnsResolveCancellationInterruptsUpstreamExchange(t *testing.T) {
	started := make(chan struct{})
	releaseHandler := make(chan struct{})
	addr := startTestDNSServer(t, dns.HandlerFunc(func(dns.ResponseWriter, *dns.Msg) {
		close(started)
		<-releaseHandler
	}))
	t.Cleanup(func() { close(releaseHandler) })
	srv := &Server{
		dnsAddr:   addr,
		dnsClient: &dns.Client{Net: "udp", Timeout: 10 * time.Second},
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := srv.DnsResolve(ctx, &proto.DnsRequest{Items: []*proto.DnsRequestItem{
			{Fqdn: "cancel.example.", QType: uint32(dns.TypeA)},
		}})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream exchange did not start")
	}
	cancel()
	select {
	case err := <-result:
		if status.Code(err) != codes.Canceled {
			t.Fatalf("DnsResolve() error = %v, want Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("DnsResolve did not stop after context cancellation")
	}
}

func TestEncodeLegacyDNSRcode(t *testing.T) {
	for _, test := range []struct {
		rcode int
		want  uint32
	}{
		{rcode: -1, want: dns.RcodeServerFailure},
		{rcode: dns.RcodeSuccess, want: dns.RcodeSuccess},
		{rcode: dns.RcodeNameError, want: dns.RcodeNameError},
		{rcode: 0xF, want: 0xF},
		{rcode: 0x10, want: dns.RcodeServerFailure},
	} {
		if got := encodeLegacyDNSRcode(test.rcode); got != test.want {
			t.Errorf("encodeLegacyDNSRcode(%d) = %d, want %d", test.rcode, got, test.want)
		}
	}
}
