package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/socks"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/forward"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	rpcClient "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/dns"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/logger"
)

// MixedConfig is a server/client mixed config, along with general config.
type MixedConfig struct {
	// Role is an identifier for distinguish the role in spaceship since the server/client are not seperated.
	// supported roles: "server", "client"
	Role `json:"role"`
	// DNS is used for set up the custom dns as an upstream of global resolver.
	DNS *dns.DNS `json:"dns,omitempty"`
	// CAs is used for append the custom CA to the system cert pool.
	CAs []string `json:"cas,omitempty"`
	// LogMode is used for set up specific log mod, defaults to stdout.
	LogMode logger.Mode `json:"log,omitempty"`

	*client.Client
	*server.Server

	decodedFromJSON bool
	idleTimeoutSet  bool
}

const maxIdleTimeoutSeconds = int64(1<<63-1) / int64(time.Second)

func newMixedConfig() *MixedConfig {
	return &MixedConfig{
		Client: &client.Client{},
		Server: &server.Server{},
	}
}

func (c *MixedConfig) ensureEmbeddedConfigs() {
	if c.Client == nil {
		c.Client = &client.Client{}
	}
	if c.Server == nil {
		c.Server = &server.Server{}
	}
}

// UnmarshalJSON records whether idle_timeout was explicitly provided while
// preserving Client.IdleTimeout as an int for Go API compatibility.
func (c *MixedConfig) UnmarshalJSON(data []byte) error {
	type mixedConfigJSON MixedConfig
	defaults := newMixedConfig()
	*c = *defaults
	if err := json.Unmarshal(data, (*mixedConfigJSON)(c)); err != nil {
		return err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	raw, present := fields["idle_timeout"]
	c.decodedFromJSON = true
	c.idleTimeoutSet = present && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	return nil
}

// NewFromConfigFile loads the config from the file in the specific path.
func NewFromConfigFile(path string) (*MixedConfig, error) {
	// Clean the path to remove any directory traversal attempts
	cleanPath := filepath.Clean(path)

	// Read the config file
	f, err := os.Open(cleanPath)
	if err != nil {
		return nil, err
	}
	defer utils.Close(f)

	config := newMixedConfig()
	if err = json.NewDecoder(f).Decode(config); err != nil {
		return nil, err
	}
	return config, nil
}

// NewFromString loads the config from raw config string in JSON format (stick to the config structure).
func NewFromString(c string) (*MixedConfig, error) {
	config := newMixedConfig()
	if err := json.Unmarshal([]byte(c), config); err != nil {
		return nil, err
	}
	return config, nil
}

// Apply applies the MixedConfig
func (c *MixedConfig) Apply() error {
	c.ensureEmbeddedConfigs()

	// role check
	if c.Role != RoleClient && c.Role != RoleServer {
		return fmt.Errorf("invalid role: %s", c.Role)
	}

	applyIdleTimeout := !c.decodedFromJSON || c.idleTimeoutSet
	idleTimeoutSeconds := int64(c.IdleTimeout)
	if applyIdleTimeout {
		if idleTimeoutSeconds < 0 {
			return fmt.Errorf("idle_timeout must be non-negative: %d", c.IdleTimeout)
		}
		if idleTimeoutSeconds > maxIdleTimeoutSeconds {
			return fmt.Errorf("idle_timeout exceeds maximum duration: %d", c.IdleTimeout)
		}
	}

	// log mode
	c.LogMode.Set()

	// dns
	if c.DNS != nil {
		if err := c.DNS.SetDefault(); err != nil {
			return err
		}
	}

	// custom buffer size
	if c.Buffer > 0 {
		// A payload chunk is one full buffer wrapped in a protobuf envelope, so an
		// oversized buffer exceeds the gRPC message limit and fails every send at
		// runtime. Reject it at startup instead.
		if bufferBytes := int(c.Buffer) * 1024; bufferBytes > rpc.MaxTransportBufferSize {
			return fmt.Errorf("buffer too large: %dK exceeds maximum %dK",
				c.Buffer, rpc.MaxTransportBufferSize/1024)
		}
		log.Printf("custom buffer size: %dK", c.Buffer)
		transport.SetBufferSize(c.Buffer)
	}

	// socks5 udp associate relay. Applied unconditionally so a reload that drops
	// the section restores defaults, the same way ipv6 is re-enabled below —
	// otherwise a previously configured "disable" would silently persist.
	var udpSettings socks.UDPSettings
	if c.UDP != nil {
		for _, limit := range []struct {
			name  string
			value int
		}{
			{"max_associations", c.UDP.MaxAssociations},
			{"max_associations_per_client", c.UDP.MaxAssociationsPerClient},
			{"max_nat_entries", c.UDP.MaxNATEntries},
			{"max_nat_entries_total", c.UDP.MaxNATEntriesTotal},
			{"max_nat_entries_per_client", c.UDP.MaxNATEntriesPerClient},
		} {
			if limit.value < 0 {
				return fmt.Errorf("udp.%s must be non-negative: %d", limit.name, limit.value)
			}
		}
		udpSettings = socks.UDPSettings{
			Disable:                  c.UDP.Disable,
			MaxAssociations:          c.UDP.MaxAssociations,
			MaxAssociationsPerClient: c.UDP.MaxAssociationsPerClient,
			MaxNATEntries:            c.UDP.MaxNATEntries,
			MaxNATEntriesGlobal:      c.UDP.MaxNATEntriesTotal,
			MaxNATEntriesPerClient:   c.UDP.MaxNATEntriesPerClient,
		}
	}
	socks.SetUDPSettings(udpSettings)
	if udpSettings.Disable {
		log.Println("socks5 udp associate disabled")
	}

	// custom grpc service name
	if c.Path != "" {
		log.Printf("custom service name: %s", c.Path)
		// Use the new RPC configuration system
		rpc.SetServiceName(c.Path)
	}

	// client uuid
	if c.Role == RoleClient {
		if c.UUID == "" {
			return errors.New("client uuid empty")
		}
		rpcClient.SetUUID(c.UUID)
	}

	// forward proxy
	if c.Forward != "" {
		d, err := utils.LoadProxy(c.Forward)
		if err != nil {
			return err
		}
		forward.Attach(d)
		log.Println("forward-proxy attached")
	}

	// Routes: empty list installs the role default. An explicit list is used as-is
	// (fail-closed): if it has no "default" rule, unmatched hosts are rejected.
	// Operators who want catch-all behavior must add an explicit default route.
	routes := c.Routes
	if len(routes) == 0 {
		if c.Role == RoleClient {
			routes = router.Routes{router.CloneRoute(router.RouteClientDefault)}
		} else {
			routes = router.Routes{router.CloneRoute(router.RouteServerDefault)}
		}
	} else if !routesHasDefault(routes) {
		log.Println("warning: no default route configured; unmatched destinations will be rejected")
	}
	if !c.IPv6 {
		routes = append(router.Routes{router.RouteBlockIPv6}, routes...)
	}
	if err := router.SetRoutes(routes); err != nil {
		return err
	}

	// IPv6 dial preference must be set both ways so a later Apply/reload can
	// re-enable dual-stack after a previous DisableIPv6. Route validation and
	// installation happen first, so a rejected reload leaves this mode intact.
	if !c.IPv6 {
		transport.DisableIPv6()
		log.Println("ipv6 disabled")
	} else {
		transport.EnableIPv6()
		log.Println("ipv6 enabled")
	}

	// idle timeout: only apply when the field is present in config.
	// omitted -> keep transport default (30m); 0 -> disable; n > 0 -> n seconds.
	if applyIdleTimeout {
		log.Printf("custom idle timeout: %ds", c.IdleTimeout)
		transport.SetIdleTimeout(time.Duration(idleTimeoutSeconds) * time.Second)
	}

	return nil
}

// routesHasDefault reports whether any route is a catch-all default rule.
func routesHasDefault(routes router.Routes) bool {
	for _, r := range routes {
		if r != nil && r.MatchType == router.TypeDefault {
			return true
		}
	}
	return false
}
