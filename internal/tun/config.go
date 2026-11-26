package tun

import (
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	clientConfig "github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
)

// UDPMappingMode selects RFC 4787 UDP NAT mapping behavior.
type UDPMappingMode string

const (
	UDPMappingEIM UDPMappingMode = "eim"
	UDPMappingAD  UDPMappingMode = "ad"
	UDPMappingAPD UDPMappingMode = "apd"
)

// UDPFilteringMode selects RFC 4787 UDP NAT filtering behavior.
type UDPFilteringMode string

const (
	UDPFilteringEIF UDPFilteringMode = "eif"
	UDPFilteringAD  UDPFilteringMode = "ad"
	UDPFilteringAPD UDPFilteringMode = "apd"
)

func (m UDPFilteringMode) transportFiltering() transport.PacketFiltering {
	switch m {
	case UDPFilteringAD:
		return transport.PacketFilteringAD
	case UDPFilteringAPD:
		return transport.PacketFilteringAPD
	default:
		return transport.PacketFilteringEIF
	}
}

const (
	DefaultName                     = "spaceship0"
	DefaultMTU                      = 1500
	DefaultBypassMark        uint32 = transport.DefaultBypassMark
	DefaultMaxConnections           = 4096
	DefaultMaxPending               = 1024
	DefaultDNSMaxInFlight           = 256
	DefaultDNSQueryTimeout          = 5 * time.Second
	DefaultDNSTCPIdleTimeout        = 10 * time.Second
	DefaultDNSUDPIdleTimeout        = 30 * time.Second
	// DefaultUDPIdleTimeout closes a proxied UDP session after this long with
	// no datagram in either direction. DNS hijack sessions keep their own
	// idle limits. There is no JSON field for this; set Config.UDPIdleTimeout
	// from Go, or leave it zero to use this default.
	DefaultUDPIdleTimeout = 60 * time.Second
	// defaultUDPSessionShare is the fraction of MaxConnections that general UDP
	// may hold when MaxUDPSessions is zero. The rest stays reserved for TCP.
	defaultUDPSessionShare = 4

	minMTU           = 1280
	maxMTU           = 65535
	maxResourceLimit = 1 << 16
	// Each admitted UDP DNS flow owns a 64 KiB receive buffer for its idle
	// lifetime, so DNS needs a tighter ceiling than the aggregate TCP limit.
	maxDNSInFlightLimit = 1024
)

var ErrUnsupported = errors.New("TUN is only supported on Linux, Android, macOS/iOS, and Windows")

// DNSConfig controls classic DNS interception on TCP and UDP destination port
// 53. It intentionally has no resolver address: every accepted query goes
// through the authenticated Spaceship DNS RPC.
type DNSConfig struct {
	// Enabled controls whether port 53 is answered by the Spaceship DNS RPC.
	// When it is set, TCP and UDP port 53 stay on that path. When it is not,
	// port 53 is forwarded as an ordinary UDP flow. Other UDP is always
	// forwarded through the route's packet transport; it is not rejected for
	// being something other than DNS.
	Enabled        bool
	BlockIPv6      bool
	Addresses      []netip.Addr
	QueryTimeout   time.Duration
	TCPIdleTimeout time.Duration
	MaxInFlight    int
	// MaxInFlightPerConnection bounds concurrent DNS RPCs owned by a single
	// DNS-over-TCP connection. RFC 7766 pipelining lets one client hold every
	// slot in MaxInFlight, which answers every other client behind the TUN with
	// SERVFAIL until those RPCs drain. Zero reserves a share of the pool for
	// other connections; see defaultDNSMaxInFlightPerConnection.
	MaxInFlightPerConnection int
}

// defaultDNSMaxInFlightPerConnection reserves roughly a quarter of the DNS pool
// for connections other than the busiest one. The busiest still keeps far more
// headroom than a stub resolver pipelines, so the common single-resolver
// deployment is not throttled, while a pipelining client can no longer starve
// its neighbours.
//
// Pools too small for that reservation to mean anything are left unrestricted:
// a quarter of four slots is one, which would disable pipelining outright in
// exchange for a slot of protection.
func defaultDNSMaxInFlightPerConnection(maxInFlight int) int {
	const pipeliningFloor = 4
	return max(1, min(maxInFlight, max(maxInFlight*3/4, pipeliningFloor)))
}

// Config controls the Linux TUN frontend. RouteMode is deliberately limited to
// manual policy routing so startup never rewrites a host's routing tables.
type Config struct {
	Name           string
	MTU            int
	FileDescriptor *int
	RouteMode      string
	// BypassMark is the SO_MARK installed for outbound sockets. On Linux, zero
	// is replaced with DefaultBypassMark and BypassMarkRequired is true. On
	// Android, zero stays zero and the mark is not required.
	BypassMark uint32
	// BypassMarkRequired reports whether BypassMark must be installed and
	// verified. Linux TUN always requires it. Android requires it only when
	// the configured mark is non-zero.
	BypassMarkRequired    bool
	MaxConnections        int
	MaxPendingConnections int
	// MaxUDPSessions bounds general UDP sessions within MaxConnections, so
	// TCP and DNS keep at least MaxConnections-MaxUDPSessions slots. When UDP
	// is at its limit, a new 4-tuple replaces the least recently active
	// session instead of being refused. Zero selects a quarter of
	// MaxConnections, and at least one.
	MaxUDPSessions int
	// UDPIdleTimeout closes a general UDP session after this long without a
	// datagram in either direction. Zero selects DefaultUDPIdleTimeout.
	UDPIdleTimeout time.Duration
	// UDPMapping selects RFC 4787 NAT mapping ("eim", "ad", "apd"). Empty
	// defaults to "eim" (Full Cone / Endpoint-Independent Mapping).
	UDPMapping UDPMappingMode
	// UDPFiltering selects RFC 4787 NAT filtering ("eif", "ad", "apd"). Empty
	// defaults to "eif" (Full Cone / Endpoint-Independent Filtering).
	UDPFiltering UDPFilteringMode
	// Address lists interface CIDRs for route_mode "auto".
	Address []netip.Prefix
	// RouteExcludeAddress lists CIDRs excluded from auto routing.
	RouteExcludeAddress []netip.Prefix
	// OutboundInterface optionally overrides the physical egress interface.
	OutboundInterface string
	// AutoRedirect enables Linux nftables transparent TCP redirect acceleration.
	AutoRedirect bool
	// AutoRedirectIncludeForward enables PREROUTING capture for LAN forwarding.
	AutoRedirectIncludeForward bool
	// WintunPath optionally locates wintun.dll on Windows.
	WintunPath string
	DNS        DNSConfig

	// ResolveRoute overrides the process-global router when non-nil. It must be
	// supplied to New rather than assigned on Service afterwards: CreateNIC
	// starts gVisor's packet processors before New returns, so a later write
	// races with handleTCPRequest.
	//
	// The function receives a recovered HTTP Host or TLS server name when the
	// client flight has one, and the destination IP otherwise. The default
	// router instead matches CIDR rules against that IP and name rules against
	// the recovered host in one pass.
	ResolveRoute func(string) (transport.Transport, error)
}

// NormalizeConfig validates cfg and fills all zero-valued defaults.
func NormalizeConfig(cfg Config) (Config, error) {
	if cfg.Name == "" && cfg.FileDescriptor == nil {
		cfg.Name = DefaultName
	}
	if cfg.Name != "" && !validInterfaceName(cfg.Name) {
		return Config{}, fmt.Errorf("tun: invalid interface name %q", cfg.Name)
	}

	if cfg.MTU == 0 {
		cfg.MTU = DefaultMTU
	}
	if cfg.MTU < minMTU || cfg.MTU > maxMTU {
		return Config{}, fmt.Errorf("tun: mtu must be between %d and %d: %d", minMTU, maxMTU, cfg.MTU)
	}

	if cfg.FileDescriptor != nil && *cfg.FileDescriptor < 0 {
		return Config{}, fmt.Errorf("tun: file descriptor must be non-negative: %d", *cfg.FileDescriptor)
	}

	if cfg.RouteMode == "" {
		cfg.RouteMode = "manual"
	}
	if cfg.RouteMode != "manual" && cfg.RouteMode != "auto" {
		return Config{}, fmt.Errorf("tun: unsupported route mode %q (want \"manual\" or \"auto\")", cfg.RouteMode)
	}
	if cfg.RouteMode == "auto" {
		if runtime.GOOS == "android" || runtime.GOOS == "ios" {
			return Config{}, fmt.Errorf("tun: route_mode \"auto\" is not supported on %s (OS VPN service owns routes)", runtime.GOOS)
		}
		if cfg.FileDescriptor != nil {
			return Config{}, errors.New("tun: route_mode \"auto\" cannot be combined with an external file_descriptor")
		}
		if len(cfg.Address) == 0 {
			cfg.Address = []netip.Prefix{
				netip.MustParsePrefix("198.18.0.1/30"),
				netip.MustParsePrefix("fdfe:dcba:9876::1/126"),
			}
		}
	}
	if cfg.AutoRedirect {
		if runtime.GOOS != "linux" {
			return Config{}, errors.New("tun: auto_redirect is only supported on Linux")
		}
		if cfg.RouteMode != "auto" {
			return Config{}, errors.New("tun: auto_redirect requires route_mode \"auto\"")
		}
	}
	if cfg.AutoRedirectIncludeForward && !cfg.AutoRedirect {
		return Config{}, errors.New("tun: auto_redirect_include_forward requires auto_redirect")
	}
	cfg.BypassMark, cfg.BypassMarkRequired = normalizeBypassMark(runtime.GOOS, cfg.BypassMark)
	if cfg.UDPIdleTimeout == 0 {
		cfg.UDPIdleTimeout = DefaultUDPIdleTimeout
	}
	if cfg.UDPIdleTimeout < 0 {
		return Config{}, fmt.Errorf("tun: udp idle timeout must be positive: %s", cfg.UDPIdleTimeout)
	}
	if cfg.UDPMapping == "" {
		cfg.UDPMapping = UDPMappingEIM
	}
	switch cfg.UDPMapping {
	case UDPMappingEIM, UDPMappingAD, UDPMappingAPD:
	default:
		return Config{}, fmt.Errorf("tun: unsupported udp_mapping %q", cfg.UDPMapping)
	}
	if cfg.UDPFiltering == "" {
		cfg.UDPFiltering = UDPFilteringEIF
	}
	switch cfg.UDPFiltering {
	case UDPFilteringEIF, UDPFilteringAD, UDPFilteringAPD:
	default:
		return Config{}, fmt.Errorf("tun: unsupported udp_filtering %q", cfg.UDPFiltering)
	}

	var err error
	if cfg.MaxConnections, err = normalizeLimit(
		"max_connections", cfg.MaxConnections, DefaultMaxConnections, maxResourceLimit,
	); err != nil {
		return Config{}, err
	}
	if cfg.MaxPendingConnections == 0 {
		cfg.MaxPendingConnections = min(DefaultMaxPending, cfg.MaxConnections)
	} else {
		if cfg.MaxPendingConnections, err = normalizeLimit(
			"max_pending_connections", cfg.MaxPendingConnections, DefaultMaxPending, maxResourceLimit,
		); err != nil {
			return Config{}, err
		}
		if cfg.MaxPendingConnections > cfg.MaxConnections {
			return Config{}, fmt.Errorf(
				"tun: max_pending_connections must not exceed max_connections: %d > %d",
				cfg.MaxPendingConnections,
				cfg.MaxConnections,
			)
		}
	}

	if cfg.MaxUDPSessions == 0 {
		cfg.MaxUDPSessions = max(1, cfg.MaxConnections/defaultUDPSessionShare)
	}
	if cfg.MaxUDPSessions < 0 || cfg.MaxUDPSessions > cfg.MaxConnections {
		return Config{}, fmt.Errorf(
			"tun: max_udp_sessions must be between 0 and max_connections %d: %d",
			cfg.MaxConnections,
			cfg.MaxUDPSessions,
		)
	}

	if cfg.DNS.QueryTimeout == 0 {
		cfg.DNS.QueryTimeout = DefaultDNSQueryTimeout
	}
	if cfg.DNS.QueryTimeout < 0 {
		return Config{}, fmt.Errorf("tun: dns query timeout must be positive: %s", cfg.DNS.QueryTimeout)
	}
	if cfg.DNS.TCPIdleTimeout == 0 {
		cfg.DNS.TCPIdleTimeout = DefaultDNSTCPIdleTimeout
	}
	if cfg.DNS.TCPIdleTimeout < 0 {
		return Config{}, fmt.Errorf("tun: dns TCP idle timeout must be positive: %s", cfg.DNS.TCPIdleTimeout)
	}
	if cfg.DNS.MaxInFlight, err = normalizeLimit(
		"dns.max_in_flight", cfg.DNS.MaxInFlight, DefaultDNSMaxInFlight, maxDNSInFlightLimit,
	); err != nil {
		return Config{}, err
	}
	if cfg.DNS.MaxInFlightPerConnection, err = normalizeLimit(
		"dns.max_in_flight_per_connection",
		cfg.DNS.MaxInFlightPerConnection,
		defaultDNSMaxInFlightPerConnection(cfg.DNS.MaxInFlight),
		maxDNSInFlightLimit,
	); err != nil {
		return Config{}, err
	}
	if cfg.DNS.MaxInFlightPerConnection > cfg.DNS.MaxInFlight {
		return Config{}, fmt.Errorf(
			"tun: dns.max_in_flight_per_connection must not exceed dns.max_in_flight: %d > %d",
			cfg.DNS.MaxInFlightPerConnection,
			cfg.DNS.MaxInFlight,
		)
	}

	return cfg, nil
}

// normalizeBypassMark distinguishes an omitted or explicit zero mark from a
// requested one. Linux TUN (non-Android) defaults a zero to the built-in mark
// and requires it: policy routing would otherwise recapture Spaceship's own
// egress. Android (VpnService), macOS/iOS (IP_BOUND_IF / NEPacketTunnelProvider),
// and Windows (IP_UNICAST_IF) do not use Linux SO_MARK, so zero installs
// nothing and is not required there. An explicit non-zero mark is always
// treated as required and fails startup if the platform cannot set it.
func normalizeBypassMark(goos string, mark uint32) (effective uint32, required bool) {
	if goos == "linux" {
		if mark == 0 {
			return DefaultBypassMark, true
		}
		return mark, true
	}
	if mark == 0 {
		return 0, false
	}
	return mark, true
}

// FromClientConfig converts the public JSON config into a validated runtime
// config. Keeping duration conversion here prevents validation and launcher
// behavior from drifting apart.
func FromClientConfig(raw *clientConfig.TUN, blockIPv6 bool) (Config, error) {
	if raw == nil {
		return Config{}, errors.New("tun: nil client config")
	}

	var bypassMark uint32
	if raw.BypassMark != nil {
		bypassMark = *raw.BypassMark
	}
	udpIdleTimeout, err := secondsDuration("udp_idle_timeout_seconds", raw.UDPIdleTimeoutSeconds)
	if err != nil {
		return Config{}, err
	}
	var addresses []netip.Prefix
	for _, s := range raw.Address {
		p, parseErr := netip.ParsePrefix(strings.TrimSpace(s))
		if parseErr != nil {
			return Config{}, fmt.Errorf("tun: invalid address %q: %w", s, parseErr)
		}
		addresses = append(addresses, p)
	}
	var excludeAddresses []netip.Prefix
	for _, s := range raw.RouteExcludeAddress {
		p, parseErr := netip.ParsePrefix(strings.TrimSpace(s))
		if parseErr != nil {
			return Config{}, fmt.Errorf("tun: invalid route_exclude_address %q: %w", s, parseErr)
		}
		excludeAddresses = append(excludeAddresses, p)
	}
	cfg := Config{
		Name:                       raw.Name,
		MTU:                        raw.MTU,
		FileDescriptor:             raw.FileDescriptor,
		RouteMode:                  raw.RouteMode,
		Address:                    addresses,
		RouteExcludeAddress:        excludeAddresses,
		OutboundInterface:          strings.TrimSpace(raw.OutboundInterface),
		AutoRedirect:               raw.AutoRedirect,
		AutoRedirectIncludeForward: raw.AutoRedirectIncludeForward,
		WintunPath:                 strings.TrimSpace(raw.WintunPath),
		BypassMark:                 bypassMark,
		MaxConnections:             raw.MaxConnections,
		MaxPendingConnections:      raw.MaxPendingConnections,
		MaxUDPSessions:             raw.MaxUDPSessions,
		UDPIdleTimeout:             udpIdleTimeout,
		UDPMapping:                 UDPMappingMode(strings.ToLower(strings.TrimSpace(raw.UDPMapping))),
		UDPFiltering:               UDPFilteringMode(strings.ToLower(strings.TrimSpace(raw.UDPFiltering))),
	}
	if raw.DNSHijack != nil {
		var dnsAddrs []netip.Addr
		for _, s := range raw.DNSHijack.Addresses {
			addr, parseErr := netip.ParseAddr(strings.TrimSpace(s))
			if parseErr != nil {
				return Config{}, fmt.Errorf("tun: invalid dns_hijack.addresses entry %q: %w", s, parseErr)
			}
			dnsAddrs = append(dnsAddrs, addr.Unmap())
		}
		queryTimeout, err := secondsDuration(
			"dns_hijack.query_timeout_seconds",
			raw.DNSHijack.QueryTimeoutSeconds,
		)
		if err != nil {
			return Config{}, err
		}
		tcpIdleTimeout, err := secondsDuration(
			"dns_hijack.tcp_idle_timeout_seconds",
			raw.DNSHijack.TCPIdleTimeoutSeconds,
		)
		if err != nil {
			return Config{}, err
		}
		cfg.DNS = DNSConfig{
			Enabled:                  raw.DNSHijack.Enabled,
			BlockIPv6:                blockIPv6,
			Addresses:                dnsAddrs,
			QueryTimeout:             queryTimeout,
			TCPIdleTimeout:           tcpIdleTimeout,
			MaxInFlight:              raw.DNSHijack.MaxInFlight,
			MaxInFlightPerConnection: raw.DNSHijack.MaxInFlightPerConnection,
		}
	}
	return NormalizeConfig(cfg)
}

// RequiredRPCPoolSize returns the minimum number of persistent gRPC
// connections needed for cfg's worst-case stream demand. General TCP and UDP
// flows share MaxConnections and use one streaming RPC each; intercepted DNS
// queries use unary RPCs in addition to their owning TCP or UDP flow.
func RequiredRPCPoolSize(cfg Config, streamsPerConnection uint32) (uint8, error) {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return 0, err
	}
	if streamsPerConnection == 0 {
		return 0, errors.New("tun: RPC streams per connection must be positive")
	}
	if uint64(streamsPerConnection) > uint64(^uint(0)>>1) {
		return 0, fmt.Errorf(
			"tun: RPC streams per connection %d exceeds the platform int range",
			streamsPerConnection,
		)
	}

	demand := normalized.MaxConnections
	if normalized.DNS.Enabled {
		demand += normalized.DNS.MaxInFlight
	}
	perConnection := int(streamsPerConnection) // #nosec G115 -- checked against the platform int range above.
	required := demand / perConnection
	if demand%perConnection != 0 {
		required++
	}
	if required == 0 || required > 255 {
		return 0, fmt.Errorf(
			"tun: required RPC pool size %d is outside the supported range 1..255",
			required,
		)
	}
	return uint8(required), nil // #nosec G115 -- required is explicitly bounded to 1..255 above.
}

// validInterfaceName mirrors Linux's dev_valid_name restrictions before any
// privileged ioctl is attempted. Rejecting these at config-validation time
// yields a deterministic error and avoids relying on kernel-version-specific
// errno text.
func validInterfaceName(name string) bool {
	if name == "" || len(name) >= 16 || name == "." || name == ".." ||
		strings.IndexByte(name, 0) >= 0 {
		return false
	}
	for _, char := range name {
		if char == '/' || char == ':' || unicode.IsSpace(char) {
			return false
		}
	}
	return true
}

func secondsDuration(name string, seconds int) (time.Duration, error) {
	if seconds < 0 {
		return 0, fmt.Errorf("tun: %s must be non-negative: %d", name, seconds)
	}
	if int64(seconds) > int64(^uint64(0)>>1)/int64(time.Second) {
		return 0, fmt.Errorf("tun: %s exceeds maximum duration: %d", name, seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

func normalizeLimit(name string, value, fallback, maximum int) (int, error) {
	if value < 0 || value > maximum {
		return 0, fmt.Errorf("tun: %s must be between 0 and %d: %d", name, maximum, value)
	}
	if value == 0 {
		return fallback, nil
	}
	return value, nil
}
