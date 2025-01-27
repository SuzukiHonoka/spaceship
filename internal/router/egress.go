package router

import (
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/blackhole"
	"github.com/SuzukiHonoka/spaceship/internal/transport/direct"
	"github.com/SuzukiHonoka/spaceship/internal/transport/forward"
	rpcClient "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/client"
)

type Egress string

const (
	EgressUnknown   Egress = ""
	EgressDirect    Egress = "direct"
	EgressProxy     Egress = "proxy"
	EgressForward   Egress = "forward"
	EgressBlock     Egress = "block"
	EgressBlackHole Egress = "blackhole"
)

func (e Egress) GetTransport() (transport.Transport, error) {
	switch e {
	case EgressUnknown:
		return nil, fmt.Errorf("unknown transport")
	case EgressDirect:
		return direct.Transport, nil
	case EgressProxy:
		return rpcClient.NewClient()
	case EgressForward:
		return forward.Transport, nil
	case EgressBlackHole:
		return blackhole.Transport, nil
	case EgressBlock:
		return nil, transport.ErrBlocked
	}
	return nil, fmt.Errorf("desired transport [%s] not implemented", e)
}
