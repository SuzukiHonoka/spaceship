package dns

import (
	"context"
	"errors"

	rpcClient "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"github.com/miekg/dns"
)

// WireExchanger returns the complete DNS wire response produced by the
// Spaceship server. Implementations must not fall back to a local resolver.
type WireExchanger interface {
	Exchange(ctx context.Context, wireQuery []byte, network proto.Network, blockIPv6 bool) ([]byte, error)
}

type rpcExchanger struct{}

func NewRPCExchanger() WireExchanger {
	return rpcExchanger{}
}

func (rpcExchanger) Exchange(ctx context.Context, wireQuery []byte, network proto.Network, blockIPv6 bool) ([]byte, error) {
	client, err := rpcClient.New()
	if err != nil {
		return nil, err
	}
	defer utils.Close(client)
	if client == nil {
		return nil, errors.New("dns: rpc client is nil")
	}
	return client.DnsExchange(ctx, wireQuery, network, blockIPv6)
}

// legacyResolver answers a query through the DnsResolve RPC that predates
// DnsExchange. It only carries answer records and the header RCODE, so it is
// used solely when the server does not implement DnsExchange.
type legacyResolver interface {
	Resolve(ctx context.Context, query *dns.Msg, blockIPv6 bool) ([]dns.RR, int, error)
}

type rpcLegacyResolver struct{}

func (rpcLegacyResolver) Resolve(ctx context.Context, query *dns.Msg, blockIPv6 bool) ([]dns.RR, int, error) {
	client, err := rpcClient.New()
	if err != nil {
		return nil, dns.RcodeServerFailure, err
	}
	defer utils.Close(client)
	if client == nil {
		return nil, dns.RcodeServerFailure, errors.New("dns: rpc client is nil")
	}

	requests := make([]*rpcClient.DnsRequest, 0, len(query.Question))
	for _, question := range query.Question {
		requests = append(requests, &rpcClient.DnsRequest{
			Fqdn:      question.Name,
			QType:     question.Qtype,
			BlockIPv6: blockIPv6,
		})
	}
	return client.DnsResolve(ctx, requests)
}
