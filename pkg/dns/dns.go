package dns

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

var (
	DefaultTimeout = 3 * time.Second

	// platformDefaultResolver preserves an early platform-specific resolver
	// override (Android uses one) across config reloads. Configuration-provided
	// resolvers are intentionally not stored here.
	platformDefaultResolver atomic.Pointer[platformResolverPolicy]
)

type platformResolverPolicy struct {
	resolver *net.Resolver
	custom   bool
}

func init() {
	platformDefaultResolver.Store(&platformResolverPolicy{resolver: net.DefaultResolver})
}

type DNS struct {
	Type
	Server string
}

func (s *DNS) String() string {
	return fmt.Sprintf("dns(type=%s, server=%s)", s.Type, s.Server)
}

// Address returns the resolver endpoint to query.
//
// Server may be a bare host ("1.1.1.1", "::1", "resolver.example") or already
// carry a port ("127.0.0.1:5353"). Bare hosts get the standard port 53 appended;
// anything that already parses as host:port is used verbatim, so a resolver on a
// non-standard port — a local stub resolver, a container sidecar — is reachable.
func (s *DNS) Address() string {
	switch s.Type {
	case TypeDefault, TypeCommon:
		if _, _, err := net.SplitHostPort(s.Server); err == nil {
			return s.Server
		}
		return net.JoinHostPort(s.Server, "53")
	}
	return ""
}

func (s *DNS) SetDefault() error {
	switch s.Type {
	case TypeDefault, TypeCommon:
		address, err := resolveServerAddress(s.Address())
		if err != nil {
			return err
		}
		transport.SetOutboundResolver(transport.NewFixedResolver(address, DefaultTimeout))
	default:
		return fmt.Errorf("dns: type %s not implemented, abort setting default", s.Type)
	}
	return nil
}

// SetPlatformDefault installs s as the process baseline resolver. Platform
// initialization may call this before concurrent network activity begins;
// ordinary config application must use SetDefault so a later config reload can
// still restore the platform baseline.
func (s *DNS) SetPlatformDefault() error {
	if s == nil {
		return fmt.Errorf("dns: nil platform default")
	}
	if err := s.SetDefault(); err != nil {
		return err
	}
	platformDefaultResolver.Store(&platformResolverPolicy{
		resolver: transport.OutboundResolver(),
		custom:   true,
	})
	return nil
}

func resolveServerAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("dns: invalid resolver address %q: %w", address, err)
	}
	if host == "" {
		return "", fmt.Errorf("dns: invalid resolver address %q: empty host", address)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", fmt.Errorf("dns: invalid resolver address %q: invalid port %q", address, port)
	}
	port = strconv.FormatUint(portNumber, 10)
	if addr, err := netip.ParseAddr(host); err == nil {
		return net.JoinHostPort(addr.String(), port), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	addrs, err := transport.NewSystemResolver(DefaultTimeout).LookupIPAddr(ctx, host)
	if err != nil {
		return "", fmt.Errorf("dns: resolve resolver host %q: %w", host, err)
	}
	if len(addrs) == 0 || addrs[0].IP == nil {
		return "", fmt.Errorf("dns: resolver host %q returned no addresses", host)
	}
	return net.JoinHostPort(addrs[0].IP.String(), port), nil
}

// SetSystemDefault restores the platform baseline resolver for ordinary
// operation, or a mark-aware resolver for TUN operation. It is atomically
// swapped without mutating net.DefaultResolver, which would race with unrelated
// concurrent network operations.
func SetSystemDefault(markedSockets bool) {
	platformDefault := platformDefaultResolver.Load()
	if markedSockets {
		if platformDefault.custom {
			// Fixed/platform resolvers created by this package already use the
			// dynamic outbound socket control, so they pick up SO_MARK safely.
			transport.SetOutboundResolver(platformDefault.resolver)
			return
		}
		transport.SetOutboundResolver(transport.NewSystemResolver(DefaultTimeout))
		return
	}
	transport.SetOutboundResolver(platformDefault.resolver)
}
