package server

import (
	"context"
	"log"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	rpcutils "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/utils"
	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MaxLegacyDNSItems prevents the compatibility RPC from turning one admitted
// protobuf message into an unbounded amount of sequential upstream work.
const MaxLegacyDNSItems = 16

// DnsResolve is the compatibility record-oriented DNS RPC. New frontends use
// DnsExchange, but this method remains authenticated, bounded, cancellable, and
// safe for older clients.
func (s *Server) DnsResolve(ctx context.Context, request *proto.DnsRequest) (*proto.DnsResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	globalDNSExchangeCounters.legacyRequests.Add(1)
	userID, _ := rpc.UserIDFromContext(ctx)

	// Admit before parsing the request so malformed traffic cannot bypass the
	// same CPU and rate boundary as valid DNS traffic.
	firstRelease, rejection := s.dnsAdmission.acquire(userID)
	if rejection != dnsAdmissionAllowed {
		recordDNSAdmissionRejection(rejection)
		return nil, status.Error(codes.ResourceExhausted, "dns: resolve capacity exhausted")
	}

	if request == nil || len(request.Items) == 0 || len(request.Items) > MaxLegacyDNSItems {
		firstRelease()
		globalDNSExchangeCounters.invalidRequests.Add(1)
		return nil, status.Errorf(
			codes.InvalidArgument,
			"dns: resolve request must contain between 1 and %d items",
			MaxLegacyDNSItems,
		)
	}

	response := &proto.DnsResponse{Result: make([]*proto.DnsResult, 0, len(request.Items))}
	for index, item := range request.Items {
		if err := ctx.Err(); err != nil {
			if index == 0 {
				firstRelease()
			}
			return nil, status.FromContextError(err).Err()
		}

		release := firstRelease
		if index > 0 {
			release, rejection = s.dnsAdmission.acquire(userID)
			if rejection != dnsAdmissionAllowed {
				recordDNSAdmissionRejection(rejection)
				globalDNSExchangeCounters.requests.Add(1)
				globalDNSExchangeCounters.legacyItems.Add(1)
				response.Result = append(response.Result, legacyDNSFailureResult(item))
				continue
			}
		}

		globalDNSExchangeCounters.requests.Add(1)
		globalDNSExchangeCounters.legacyItems.Add(1)
		result, err := s.resolveLegacyDNSItem(ctx, item)
		release()
		if err != nil {
			if ctx.Err() != nil {
				return nil, status.FromContextError(ctx.Err()).Err()
			}
			globalDNSExchangeCounters.upstreamFailures.Add(1)
			log.Printf("dns: legacy exchange via configured resolver failed: %v", err)
			result = legacyDNSFailureResult(item)
		}
		response.Result = append(response.Result, result)
	}
	return response, nil
}

func (s *Server) resolveLegacyDNSItem(
	ctx context.Context,
	item *proto.DnsRequestItem,
) (*proto.DnsResult, error) {
	if item == nil {
		globalDNSExchangeCounters.invalidRequests.Add(1)
		return &proto.DnsResult{Rcode: dns.RcodeFormatError}, nil
	}
	result := &proto.DnsResult{Fqdn: item.Fqdn}
	qtype, ok := safeUint32ToUint16(item.QType)
	if !ok || qtype == dns.TypeNone {
		globalDNSExchangeCounters.invalidRequests.Add(1)
		result.Rcode = dns.RcodeFormatError
		return result, nil
	}
	if item.Fqdn == "" || len(item.Fqdn) > 255 {
		globalDNSExchangeCounters.invalidRequests.Add(1)
		result.Rcode = dns.RcodeFormatError
		return result, nil
	}
	if _, valid := dns.IsDomainName(item.Fqdn); !valid {
		globalDNSExchangeCounters.invalidRequests.Add(1)
		result.Rcode = dns.RcodeFormatError
		return result, nil
	}
	if qtype == dns.TypeAXFR || qtype == dns.TypeIXFR {
		globalDNSExchangeCounters.invalidRequests.Add(1)
		result.Rcode = dns.RcodeRefused
		return result, nil
	}

	if item.BlockIpv6 && qtype == dns.TypeAAAA {
		globalDNSExchangeCounters.blockedIPv6.Add(1)
		result.Rcode = dns.RcodeSuccess
		return result, nil
	}

	globalDNSExchangeCounters.forwarded.Add(1)
	records, rcode, err := s.resolveDNSRecords(ctx, item.Fqdn, qtype)
	if err != nil {
		return result, err
	}
	// The compatibility protobuf carries only the base DNS header's four-bit
	// response code and has no OPT record for an extended RCODE.
	result.Rcode = encodeLegacyDNSRcode(rcode)

	if item.BlockIpv6 {
		filtered := records[:0]
		for _, record := range records {
			if record != nil && record.Header().Rrtype != dns.TypeAAAA {
				filtered = append(filtered, record)
			}
		}
		records = filtered
	}
	if len(records) == 0 {
		return result, nil
	}

	protoRecords, err := rpcutils.ConvertRRSliceToProto(records)
	if err != nil {
		return result, err
	}
	result.Records = protoRecords
	return result, nil
}

func encodeLegacyDNSRcode(rcode int) uint32 {
	if rcode < 0 || rcode > 0xF {
		return dns.RcodeServerFailure
	}
	return uint32(rcode)
}

func legacyDNSFailureResult(item *proto.DnsRequestItem) *proto.DnsResult {
	result := &proto.DnsResult{Rcode: dns.RcodeServerFailure}
	if item != nil {
		result.Fqdn = item.Fqdn
	}
	return result
}
