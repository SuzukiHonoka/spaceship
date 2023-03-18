package router

import (
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/blackhole"
	"github.com/SuzukiHonoka/spaceship/internal/transport/direct"
	rpcClient "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/client"
)

type Egress string

const (
	EgressDirect Egress = "direct"
	EgressProxy  Egress = "proxy"
	EgressBlock  Egress = "block"
)

func (e Egress) GetTransport() (transport.Transport, error) {
	switch e {
	case EgressDirect:
		return direct.Transport, nil
	case EgressProxy:
		return rpcClient.NewClient()
	case EgressBlock:
		return blackhole.Transport, nil
	}
	return nil, fmt.Errorf("desired transport [%s] not implemented", e)
}
