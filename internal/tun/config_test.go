package tun

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	clientConfig "github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
)

func TestNormalizeConfigDefaults(t *testing.T) {
	cfg, err := NormalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != DefaultName ||
		cfg.MTU != DefaultMTU ||
		cfg.RouteMode != "manual" ||
		cfg.BypassMark != DefaultBypassMark ||
		cfg.MaxConnections != DefaultMaxConnections ||
		cfg.MaxPendingConnections != DefaultMaxPending {
		t.Fatalf("NormalizeConfig() = %+v", cfg)
	}
	if cfg.DNS.QueryTimeout != DefaultDNSQueryTimeout ||
		cfg.DNS.TCPIdleTimeout != DefaultDNSTCPIdleTimeout ||
		cfg.DNS.MaxInFlight != DefaultDNSMaxInFlight {
		t.Fatalf("NormalizeConfig() DNS = %+v", cfg.DNS)
	}
}

func TestNormalizeConfigLeavesExternalInterfaceNameUnspecified(t *testing.T) {
	fd := 7
	cfg, err := NormalizeConfig(Config{FileDescriptor: &fd})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "" {
		t.Fatalf("external descriptor default name = %q, want actual attached name", cfg.Name)
	}
}

func TestNormalizeConfigRejectsUnsafeValues(t *testing.T) {
	negativeFD := -1
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "long name", cfg: Config{Name: strings.Repeat("x", 16)}, want: "interface name"},
		{name: "slash name", cfg: Config{Name: "bad/name"}, want: "interface name"},
		{name: "colon name", cfg: Config{Name: "bad:name"}, want: "interface name"},
		{name: "space name", cfg: Config{Name: "bad name"}, want: "interface name"},
		{name: "dot name", cfg: Config{Name: "."}, want: "interface name"},
		{name: "parent name", cfg: Config{Name: ".."}, want: "interface name"},
		{name: "small mtu", cfg: Config{MTU: 1279}, want: "mtu"},
		{name: "large mtu", cfg: Config{MTU: 65536}, want: "mtu"},
		{name: "negative fd", cfg: Config{FileDescriptor: &negativeFD}, want: "file descriptor"},
		{name: "route mode", cfg: Config{RouteMode: "auto"}, want: "route mode"},
		{name: "negative connections", cfg: Config{MaxConnections: -1}, want: "max_connections"},
		{name: "excessive pending", cfg: Config{MaxPendingConnections: maxResourceLimit + 1}, want: "max_pending_connections"},
		{name: "pending exceeds connections", cfg: Config{MaxConnections: 2, MaxPendingConnections: 3}, want: "must not exceed"},
		{name: "negative query timeout", cfg: Config{DNS: DNSConfig{QueryTimeout: -time.Second}}, want: "query timeout"},
		{name: "negative idle timeout", cfg: Config{DNS: DNSConfig{TCPIdleTimeout: -time.Second}}, want: "idle timeout"},
		{name: "negative DNS limit", cfg: Config{DNS: DNSConfig{MaxInFlight: -1}}, want: "dns.max_in_flight"},
		{name: "excessive DNS limit", cfg: Config{DNS: DNSConfig{MaxInFlight: maxDNSInFlightLimit + 1}}, want: "dns.max_in_flight"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeConfig(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NormalizeConfig() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestNormalizeConfigAcceptsDNSLimitCeiling(t *testing.T) {
	cfg, err := NormalizeConfig(Config{
		DNS: DNSConfig{MaxInFlight: maxDNSInFlightLimit},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if cfg.DNS.MaxInFlight != maxDNSInFlightLimit {
		t.Fatalf("MaxInFlight = %d, want %d", cfg.DNS.MaxInFlight, maxDNSInFlightLimit)
	}
}

func TestFromClientConfig(t *testing.T) {
	fd := 7
	cfg, err := FromClientConfig(&clientConfig.TUN{
		Name:                  "ss-tun",
		MTU:                   1400,
		FileDescriptor:        &fd,
		RouteMode:             "manual",
		BypassMark:            123,
		MaxConnections:        10,
		MaxPendingConnections: 4,
		DNSHijack: &clientConfig.DNSHijack{
			Enabled:               true,
			QueryTimeoutSeconds:   2,
			TCPIdleTimeoutSeconds: 9,
			MaxInFlight:           3,
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "ss-tun" || cfg.FileDescriptor == nil || *cfg.FileDescriptor != fd ||
		cfg.BypassMark != 123 || cfg.MaxConnections != 10 || cfg.MaxPendingConnections != 4 {
		t.Fatalf("FromClientConfig() = %+v", cfg)
	}
	if !cfg.DNS.Enabled || !cfg.DNS.BlockIPv6 ||
		cfg.DNS.QueryTimeout != 2*time.Second ||
		cfg.DNS.TCPIdleTimeout != 9*time.Second ||
		cfg.DNS.MaxInFlight != 3 {
		t.Fatalf("FromClientConfig() DNS = %+v", cfg.DNS)
	}

	if _, err := FromClientConfig(nil, false); err == nil {
		t.Fatal("FromClientConfig(nil) error = nil")
	}
	_, err = FromClientConfig(&clientConfig.TUN{
		DNSHijack: &clientConfig.DNSHijack{QueryTimeoutSeconds: -1},
	}, false)
	if err == nil || errors.Is(err, ErrUnsupported) {
		t.Fatalf("FromClientConfig(negative duration) error = %v", err)
	}
	_, err = FromClientConfig(&clientConfig.TUN{
		DNSHijack: &clientConfig.DNSHijack{TCPIdleTimeoutSeconds: -1},
	}, false)
	if err == nil {
		t.Fatal("FromClientConfig(negative TCP idle timeout) error = nil")
	}
}

func TestPendingDefaultDoesNotExceedConnectionLimit(t *testing.T) {
	cfg, err := NormalizeConfig(Config{MaxConnections: 4})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxPendingConnections != 4 {
		t.Fatalf("MaxPendingConnections = %d, want 4", cfg.MaxPendingConnections)
	}
}

func TestRequiredRPCPoolSizeAccountsForTCPAndDNSStreams(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		perConn uint32
		want    uint8
		wantErr bool
	}{
		{
			name:    "defaults with DNS",
			cfg:     Config{DNS: DNSConfig{Enabled: true}},
			perConn: 4096,
			want:    2,
		},
		{
			name:    "defaults without DNS",
			cfg:     Config{},
			perConn: 4096,
			want:    1,
		},
		{
			name: "configured capacity",
			cfg: Config{
				MaxConnections: 5,
				DNS: DNSConfig{
					Enabled:     true,
					MaxInFlight: 3,
				},
			},
			perConn: 4,
			want:    2,
		},
		{
			name:    "zero stream capacity",
			cfg:     Config{},
			perConn: 0,
			wantErr: true,
		},
		{
			name:    "pool exceeds uint8 config",
			cfg:     Config{MaxConnections: maxResourceLimit},
			perConn: 1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RequiredRPCPoolSize(tt.cfg, tt.perConn)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("RequiredRPCPoolSize() = %d, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("RequiredRPCPoolSize() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSecondsDurationRejectsOverflow(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("overflowing time.Duration seconds cannot be represented by int")
	}

	const overflowSeconds = int64(1<<63-1)/int64(time.Second) + 1
	if _, err := secondsDuration("timeout", int(overflowSeconds)); err == nil {
		t.Fatal("secondsDuration accepted a value that overflows time.Duration")
	}
}
