package server

import (
	"context"
	"net"
	"testing"
	"time"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	rpcutils "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/utils"
	"github.com/miekg/dns"
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
