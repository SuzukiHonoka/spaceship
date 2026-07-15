package client

import (
	"context"
	"errors"
	"net"
	"testing"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	rpcutils "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/utils"
	"github.com/miekg/dns"
	"google.golang.org/grpc"
)

type dnsProxyClient struct {
	mockProxyClient2
	response *proto.DnsResponse
	err      error
	request  *proto.DnsRequest
}

func (m *dnsProxyClient) DnsResolve(_ context.Context, request *proto.DnsRequest, _ ...grpc.CallOption) (*proto.DnsResponse, error) {
	m.request = request
	return m.response, m.err
}

func TestDecodeDNSResponsePreservesRcodeAndRecords(t *testing.T) {
	record := &dns.A{
		Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   net.ParseIP("192.0.2.1").To4(),
	}
	protoRecords, err := rpcutils.ConvertRRSliceToProto([]dns.RR{record})
	if err != nil {
		t.Fatal(err)
	}

	records, rcode, err := decodeDNSResponse(&proto.DnsResponse{Result: []*proto.DnsResult{
		{Fqdn: "example.com.", Records: protoRecords},
		{Fqdn: "missing.example.", Rcode: dns.RcodeNameError},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %d, want NXDOMAIN", rcode)
	}
	if len(records) != 1 || records[0].String() != record.String() {
		t.Fatalf("records = %v, want %v", records, record)
	}
}

func TestDecodeDNSResponseRejectsMalformedRecord(t *testing.T) {
	_, rcode, err := decodeDNSResponse(&proto.DnsResponse{Result: []*proto.DnsResult{
		{Fqdn: "bad.example.", Records: []*proto.RR_Record{{WireData: []byte{0xff}}}},
	}})
	if err == nil {
		t.Fatal("decodeDNSResponse() accepted malformed wire data")
	}
	if rcode != dns.RcodeServerFailure {
		t.Fatalf("rcode = %d, want SERVFAIL", rcode)
	}
}

func TestDnsResolveRejectsNilRequest(t *testing.T) {
	client := new(Client)
	_, rcode, err := client.DnsResolve(context.Background(), []*DnsRequest{nil})
	if err == nil {
		t.Fatal("DnsResolve() accepted a nil request")
	}
	if rcode != dns.RcodeFormatError {
		t.Fatalf("rcode = %d, want FORMERR", rcode)
	}
}

func TestDecodeDNSResponseRejectsExtendedRcodeWithoutEDNS(t *testing.T) {
	_, rcode, err := decodeDNSResponse(&proto.DnsResponse{Result: []*proto.DnsResult{
		{Fqdn: "example.com.", Rcode: 16},
	}})
	if err == nil {
		t.Fatal("decodeDNSResponse() accepted an extended RCODE without EDNS")
	}
	if rcode != dns.RcodeServerFailure {
		t.Fatalf("rcode = %d, want SERVFAIL", rcode)
	}
}

func TestDnsResolveCallsRPCAndReturnsRcode(t *testing.T) {
	proxyClient := &dnsProxyClient{response: &proto.DnsResponse{Result: []*proto.DnsResult{
		{Fqdn: "missing.example.", Rcode: dns.RcodeNameError},
	}}}
	client := &Client{ProxyClient: proxyClient}

	records, rcode, err := client.DnsResolve(context.Background(), []*DnsRequest{
		{Fqdn: "missing.example.", QType: dns.TypeA, BlockIPv6: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 || rcode != dns.RcodeNameError {
		t.Fatalf("DnsResolve() = (%v, %d), want empty NXDOMAIN", records, rcode)
	}
	if len(proxyClient.request.Items) != 1 {
		t.Fatalf("RPC request items = %d, want 1", len(proxyClient.request.Items))
	}
	item := proxyClient.request.Items[0]
	if item.Fqdn != "missing.example." || item.QType != uint32(dns.TypeA) || !item.BlockIpv6 {
		t.Fatalf("RPC request item = %+v", item)
	}

	proxyClient.err = errors.New("rpc unavailable")
	if _, rcode, err = client.DnsResolve(context.Background(), []*DnsRequest{{Fqdn: "example.com.", QType: dns.TypeA}}); err == nil || rcode != dns.RcodeServerFailure {
		t.Fatalf("RPC error result = (rcode %d, err %v), want SERVFAIL", rcode, err)
	}
}
