package server

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/SuzukiHonoka/spaceship/v2/internal/dnswire"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DnsExchange forwards one complete DNS message through the resolver configured
// on the Spaceship server. Upstream failures are returned as DNS SERVFAIL
// messages so clients never fall back to the original or local resolver.
func (s *Server) DnsExchange(ctx context.Context, request *proto.DnsExchangeRequest) (*proto.DnsExchangeResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	globalDNSExchangeCounters.requests.Add(1)
	userID, _ := rpc.UserIDFromContext(ctx)
	release, rejection := s.dnsAdmission.acquire(userID)
	if rejection != dnsAdmissionAllowed {
		recordDNSAdmissionRejection(rejection)
		return nil, status.Error(codes.ResourceExhausted, "dns: exchange capacity exhausted")
	}
	defer release()

	if request == nil {
		globalDNSExchangeCounters.invalidRequests.Add(1)
		return nil, status.Error(codes.InvalidArgument, "dns: nil exchange request")
	}

	query, err := dnswire.ParseQuery(request.WireQuery)
	if err != nil {
		globalDNSExchangeCounters.invalidRequests.Add(1)
		return nil, status.Errorf(codes.InvalidArgument, "dns: invalid exchange request: %v", err)
	}

	network, err := dnsNetwork(request.Network)
	if err != nil {
		globalDNSExchangeCounters.invalidRequests.Add(1)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	var response *dns.Msg
	if request.BlockIpv6 && query.Question[0].Qtype == dns.TypeAAAA {
		globalDNSExchangeCounters.blockedIPv6.Add(1)
		response = dnswire.EmptySuccessResponse(query)
	} else {
		globalDNSExchangeCounters.forwarded.Add(1)
		response, err = s.exchangeDNSMessage(ctx, query, network)
		if err != nil {
			if ctx.Err() != nil {
				return nil, status.FromContextError(ctx.Err()).Err()
			}
			globalDNSExchangeCounters.upstreamFailures.Add(1)
			// Do not log query names at the default log level. The transport
			// and resolver address are enough for operational diagnosis
			// without turning logs into a browsing-history database.
			log.Printf("dns: %s exchange via configured resolver failed: %v", network, err)
			response = dnswire.ErrorResponse(query, dns.RcodeServerFailure)
		}
	}

	if network == "udp" {
		response.Truncate(dnswire.UDPSize(query))
	}
	wire, err := response.Pack()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "dns: pack exchange response: %v", err)
	}
	if len(wire) > dnswire.MaxMessageSize {
		return nil, status.Error(codes.Internal, "dns: exchange response exceeds maximum wire size")
	}
	return &proto.DnsExchangeResponse{WireResponse: wire}, nil
}

func dnsNetwork(network proto.Network) (string, error) {
	switch network {
	case proto.Network_UDP:
		return "udp", nil
	case proto.Network_TCP:
		return "tcp", nil
	default:
		return "", fmt.Errorf("dns: unsupported exchange network %d", network)
	}
}

func (s *Server) exchangeDNSMessage(ctx context.Context, query *dns.Msg, network string) (*dns.Msg, error) {
	if s.dnsClient == nil || s.dnsAddr == "" {
		return nil, errors.New("resolver is not configured")
	}

	// dns.Client has request-local connection state. Copy the shared template so
	// UDP and TCP requests may run concurrently without mutating it.
	client := *s.dnsClient
	client.Net = network
	if network == "udp" {
		// Receive the complete datagram even when an upstream incorrectly ignores
		// the request's advertised size. We validate it first and truncate the
		// response ourselves below; miekg/dns otherwise defaults to a 512-byte
		// receive buffer and turns such replies into an unpacking error/SERVFAIL.
		client.UDPSize = uint16(dnswire.MaxMessageSize)
	}
	response, _, err := exchangeDNSContext(ctx, &client, query, s.dnsAddr)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("resolver returned an empty response")
	}
	if !response.Response {
		return nil, errors.New("resolver returned a query instead of a response")
	}
	if !dnswire.QuestionsEqual(query, response) {
		return nil, errors.New("resolver response does not match the query")
	}
	return response, nil
}
