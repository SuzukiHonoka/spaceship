package tun

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	clientConfig "github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
)

const (
	DefaultName                     = "spaceship0"
	DefaultMTU                      = 1500
	DefaultBypassMark        uint32 = 0x5350
	DefaultMaxConnections           = 4096
	DefaultMaxPending               = 1024
	DefaultDNSMaxInFlight           = 256
	DefaultDNSQueryTimeout          = 5 * time.Second
	DefaultDNSTCPIdleTimeout        = 10 * time.Second
	DefaultDNSUDPIdleTimeout        = 30 * time.Second

	minMTU           = 1280
	maxMTU           = 65535
	maxResourceLimit = 1 << 16
	// Each admitted UDP DNS flow owns a 64 KiB receive buffer for its idle
	// lifetime, so DNS needs a tighter ceiling than the aggregate TCP limit.
	maxDNSInFlightLimit = 1024
)

var ErrUnsupported = errors.New("TUN is only supported on Linux")

// DNSConfig controls classic DNS interception on TCP and UDP destination port
// 53. It intentionally has no resolver address: every accepted query goes
// through the authenticated Spaceship DNS RPC.
type DNSConfig struct {
	Enabled        bool
	BlockIPv6      bool
	QueryTimeout   time.Duration
	TCPIdleTimeout time.Duration
	MaxInFlight    int
}

// Config controls the Linux TUN frontend. RouteMode is deliberately limited to
// manual policy routing so startup never rewrites a host's routing tables.
type Config struct {
	Name                  string
	MTU                   int
	FileDescriptor        *int
	RouteMode             string
	BypassMark            uint32
	MaxConnections        int
	MaxPendingConnections int
	DNS                   DNSConfig
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
	if cfg.RouteMode != "manual" {
		return Config{}, fmt.Errorf("tun: unsupported route mode %q", cfg.RouteMode)
	}
	if cfg.BypassMark == 0 {
		cfg.BypassMark = DefaultBypassMark
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

	return cfg, nil
}

// FromClientConfig converts the public JSON config into a validated runtime
// config. Keeping duration conversion here prevents validation and launcher
// behavior from drifting apart.
func FromClientConfig(raw *clientConfig.TUN, blockIPv6 bool) (Config, error) {
	if raw == nil {
		return Config{}, errors.New("tun: nil client config")
	}

	cfg := Config{
		Name:                  raw.Name,
		MTU:                   raw.MTU,
		FileDescriptor:        raw.FileDescriptor,
		RouteMode:             raw.RouteMode,
		BypassMark:            raw.BypassMark,
		MaxConnections:        raw.MaxConnections,
		MaxPendingConnections: raw.MaxPendingConnections,
	}
	if raw.DNSHijack != nil {
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
			Enabled:        raw.DNSHijack.Enabled,
			BlockIPv6:      blockIPv6,
			QueryTimeout:   queryTimeout,
			TCPIdleTimeout: tcpIdleTimeout,
			MaxInFlight:    raw.DNSHijack.MaxInFlight,
		}
	}
	return NormalizeConfig(cfg)
}

// RequiredRPCPoolSize returns the minimum number of persistent gRPC
// connections needed for cfg's worst-case stream demand. General TCP flows use
// one streaming RPC each; intercepted DNS queries use unary streams in
// addition to their owning TCP or UDP flow.
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
