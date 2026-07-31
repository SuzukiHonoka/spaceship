package client

import (
	"context"
	"errors"
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/dnswire"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/miekg/dns"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type wireProxyClient struct {
	mockProxyClient2
	request  *proto.DnsExchangeRequest
	response *proto.DnsExchangeResponse
	err      error
}

func (m *wireProxyClient) DnsExchange(
	_ context.Context,
	request *proto.DnsExchangeRequest,
	_ ...grpc.CallOption,
) (*proto.DnsExchangeResponse, error) {
	m.request = request
	return m.response, m.err
}

func TestDnsExchangeValidatesAndCallsRPC(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeA)
	query.Id = 10
	wireQuery, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	response := new(dns.Msg)
	response.SetReply(query)
	response.Rcode = dns.RcodeNameError
	wireResponse, err := response.Pack()
	if err != nil {
		t.Fatal(err)
	}

	proxyClient := &wireProxyClient{
		response: &proto.DnsExchangeResponse{WireResponse: wireResponse},
	}
	client := &Client{ProxyClient: proxyClient}
	got, err := client.DnsExchange(context.Background(), wireQuery, proto.Network_UDP, true)
	if err != nil {
		t.Fatal(err)
	}
	if proxyClient.request == nil ||
		proxyClient.request.Network != proto.Network_UDP ||
		!proxyClient.request.BlockIpv6 {
		t.Fatalf("RPC request = %+v", proxyClient.request)
	}
	if string(got) != string(wireResponse) {
		t.Fatalf("wire response = %x, want %x", got, wireResponse)
	}
	got[0] ^= 0xff
	if got[0] == proxyClient.response.WireResponse[0] {
		t.Fatal("DnsExchange returned protobuf-owned response storage")
	}
}

func TestDnsExchangeRejectsInvalidInputsAndResponses(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeA)
	query.Id = 20
	wireQuery, _ := query.Pack()

	client := new(Client)
	if _, err := client.DnsExchange(
		context.Background(),
		wireQuery,
		proto.Network_UDP,
		false,
	); err == nil {
		t.Fatal("DnsExchange accepted an uninitialized client")
	}

	proxyClient := &wireProxyClient{}
	client.ProxyClient = proxyClient
	if _, err := client.DnsExchange(
		context.Background(),
		[]byte{0xff},
		proto.Network_UDP,
		false,
	); err == nil {
		t.Fatal("DnsExchange accepted malformed query")
	}
	if _, err := client.DnsExchange(
		context.Background(),
		wireQuery,
		proto.Network(99),
		false,
	); err == nil {
		t.Fatal("DnsExchange accepted unknown network")
	}

	proxyClient.err = errors.New("unavailable")
	if _, err := client.DnsExchange(
		context.Background(),
		wireQuery,
		proto.Network_TCP,
		false,
	); err == nil {
		t.Fatal("DnsExchange hid RPC error")
	}
	proxyClient.err = nil

	tests := []struct {
		name     string
		response *proto.DnsExchangeResponse
	}{
		{name: "nil"},
		{name: "empty", response: &proto.DnsExchangeResponse{}},
		{
			name: "oversized",
			response: &proto.DnsExchangeResponse{
				WireResponse: make([]byte, dnswire.MaxMessageSize+1),
			},
		},
		{name: "malformed", response: &proto.DnsExchangeResponse{WireResponse: []byte{0xff}}},
		{
			name:     "query instead of response",
			response: &proto.DnsExchangeResponse{WireResponse: wireQuery},
		},
		{
			name: "mismatched question",
			response: func() *proto.DnsExchangeResponse {
				other := new(dns.Msg)
				other.SetQuestion("other.example.", dns.TypeA)
				other.Id = query.Id
				reply := new(dns.Msg)
				reply.SetReply(other)
				wire, _ := reply.Pack()
				return &proto.DnsExchangeResponse{WireResponse: wire}
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxyClient.response = tt.response
			if _, err := client.DnsExchange(
				context.Background(),
				wireQuery,
				proto.Network_UDP,
				false,
			); err == nil {
				t.Fatal("DnsExchange accepted invalid response")
			}
		})
	}
}

func TestDnsExchangeFailsClosedAgainstServerWithoutWireDNSRPC(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeA)
	wireQuery, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}

	proxyClient := &wireProxyClient{
		err: status.Error(codes.Unimplemented, "method DnsExchange not implemented"),
	}
	client := &Client{ProxyClient: proxyClient}
	response, err := client.DnsExchange(
		context.Background(),
		wireQuery,
		proto.Network_UDP,
		false,
	)
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("DnsExchange() error = %v, code = %s, want Unimplemented", err, status.Code(err))
	}
	if response != nil {
		t.Fatalf("DnsExchange() response = %x, want nil", response)
	}
	if proxyClient.request == nil {
		t.Fatal("DnsExchange() did not call the wire DNS RPC")
	}
}
