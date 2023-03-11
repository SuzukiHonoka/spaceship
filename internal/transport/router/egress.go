package router

import (
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/direct"
	rpcClient "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/client"
)

type Egress string

const (
	Direct Egress = "direct"
	Proxy  Egress = "proxy"
	Block  Egress = "block"
)

func (e Egress) GetTransport() (transport.Transport, error) {
	switch e {
	case Direct:
		return direct.Transport, nil
	case Proxy:
		return rpcClient.NewClient()
	case Block:
	}
	return nil, fmt.Errorf("desired transport %s not implemented", e)
}
