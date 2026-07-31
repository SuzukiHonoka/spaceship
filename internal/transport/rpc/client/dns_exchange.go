package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/SuzukiHonoka/spaceship/v2/internal/dnswire"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/miekg/dns"
)

// DnsExchange sends one complete DNS query to the Spaceship server and returns
// its complete wire response. It never consults a local resolver.
func (c *Client) DnsExchange(ctx context.Context, wireQuery []byte, network proto.Network, blockIPv6 bool) ([]byte, error) {
	if c == nil || c.ProxyClient == nil {
		return nil, errors.New("rpc dns: client is not initialized")
	}
	query, err := dnswire.ParseQuery(wireQuery)
	if err != nil {
		return nil, err
	}
	if network != proto.Network_UDP && network != proto.Network_TCP {
		return nil, fmt.Errorf("rpc dns: unsupported network %d", network)
	}

	response, err := c.ProxyClient.DnsExchange(ctx, &proto.DnsExchangeRequest{
		WireQuery: wireQuery,
		Network:   network,
		BlockIpv6: blockIPv6,
	})
	if err != nil {
		return nil, fmt.Errorf("rpc dns exchange: %w", err)
	}
	if response == nil || len(response.WireResponse) == 0 {
		return nil, errors.New("rpc dns exchange returned an empty response")
	}
	if len(response.WireResponse) > dnswire.MaxMessageSize {
		return nil, errors.New("rpc dns exchange response exceeds maximum wire size")
	}

	msg := new(dns.Msg)
	if err := msg.Unpack(response.WireResponse); err != nil {
		return nil, fmt.Errorf("rpc dns exchange returned malformed wire data: %w", err)
	}
	if !msg.Response || !dnswire.QuestionsEqual(query, msg) {
		return nil, errors.New("rpc dns exchange response does not match the query")
	}

	// Detach the result from protobuf-owned storage before returning it to
	// callers that may retain the slice for asynchronous TCP writes.
	return append([]byte(nil), response.WireResponse...), nil
}
