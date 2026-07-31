package dns

import (
	"context"
	"errors"

	rpcClient "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
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
