package router

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/blackhole"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/direct"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/forward"
	rpcClient "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
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

// SupportsUDP reports whether this egress can carry packet-oriented traffic.
//
// Determined statically because constructing an EgressProxy transport checks out
// a pooled gRPC connection, which is far too side-effecting for a capability
// probe. TestEgressSupportsUDPMatchesTransports keeps this in sync with the
// transports that actually implement transport.PacketDialer.
func (e Egress) SupportsUDP() bool {
	switch e {
	case EgressDirect, EgressProxy:
		return true
	default:
		return false
	}
}

// DialHost is the host to pass to route.Proxy. Direct and blackhole keep the
// IP the client resolved. Every other egress receives a recovered hostname so
// that egress resolves it: a proxy route uses the Spaceship server's DNS. An
// empty name keeps the IP.
func DialHost(route transport.Transport, ip, name string) string {
	if name == "" || resolvesLocally(route) {
		return ip
	}
	return name
}

func resolvesLocally(route transport.Transport) bool {
	switch route.(type) {
	case nil, *direct.Direct, *blackhole.BlackHole:
		return true
	default:
		return false
	}
}

// HostForEgress is the host DialHost selects for a transport built from egress.
func HostForEgress(egress Egress, ip, name string) string {
	if name == "" || egress == EgressDirect || egress == EgressBlackHole {
		return ip
	}
	return name
}

// TransportName is the String of the transport egress constructs.
func (e Egress) TransportName() string {
	switch e {
	case EgressDirect:
		return direct.TransportName
	case EgressProxy:
		return rpcClient.TransportName
	case EgressForward:
		return forward.TransportName
	case EgressBlackHole:
		return blackhole.TransportName
	default:
		return string(e)
	}
}

type proxyAdmitter interface {
	Admit(context.Context) (transport.AdmittedSession, error)
}

// AdmitProxy checks a pooled tunnel connection out and waits until the server
// has authenticated and admitted the session. The caller closes the session,
// which returns the connection to the pool. A nil session and a nil error mean
// the server does not admit before the target address; the caller dials with
// GetTransport after answering its own client.
func AdmitProxy(ctx context.Context) (transport.AdmittedSession, error) {
	tr, err := EgressProxy.GetTransport()
	if err != nil {
		return nil, err
	}
	admitter, ok := tr.(proxyAdmitter)
	if !ok {
		utils.Close(tr)
		return nil, errors.New("proxy egress cannot admit a session")
	}
	session, err := admitter.Admit(ctx)
	if errors.Is(err, rpcClient.ErrEarlyAdmitUnavailable) {
		return nil, nil
	}
	return session, err
}

// DialEgress carries addr on an admitted proxy session when egress is the
// tunnel, so a second connection is not taken from the pool. Any other egress
// closes that session and dials on its own transport.
func DialEgress(ctx context.Context, admitted transport.AdmittedSession, egress Egress, addr string, localAddr chan<- string, w io.Writer, r io.Reader) error {
	if admitted != nil && egress == EgressProxy {
		return admitted.Proxy(ctx, addr, localAddr, w, r)
	}
	utils.Close(admitted)
	tr, err := egress.GetTransport()
	if err != nil {
		return err
	}
	defer utils.Close(tr)
	return tr.Proxy(ctx, addr, localAddr, w, r)
}

func (e Egress) GetTransport() (transport.Transport, error) {
	switch e {
	case EgressUnknown:
		return nil, fmt.Errorf("unknown transport")
	case EgressDirect:
		return direct.New(), nil
	case EgressProxy:
		return rpcClient.New()
	case EgressForward:
		return forward.New(), nil
	case EgressBlackHole:
		return blackhole.New(), nil
	case EgressBlock:
		return nil, transport.ErrBlocked
	}
	return nil, fmt.Errorf("desired transport [%s] not implemented", e)
}
