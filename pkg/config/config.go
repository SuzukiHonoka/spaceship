package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/router"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/forward"
	rpcClient "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/client"
	proto "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"github.com/SuzukiHonoka/spaceship/pkg/config/client"
	"github.com/SuzukiHonoka/spaceship/pkg/config/server"
	"github.com/SuzukiHonoka/spaceship/pkg/dns"
	"github.com/SuzukiHonoka/spaceship/pkg/logger"
	"log"
	"os"
)

// MixedConfig is a server/client mixed config, along with general config.
type MixedConfig struct {
	// Role is an identifier for distinguish the role in spaceship since the server/client are not seperated.
	// supported roles: "server", "client"
	Role `json:"role"`
	// DNS is used for set up the custom dns as an upstream of global resolver.
	*dns.DNS `json:"dns,omitempty"`
	// CAs is used for append the custom CA to the system cert pool.
	CAs []string `json:"cas,omitempty"`
	// LogMode is used for set up specific log mod, defaults to stdout.
	LogMode logger.Mode `json:"log,omitempty"`
	client.Client
	server.Server
}

// NewFromConfigFile loads the config from the file in the specific path.
func NewFromConfigFile(path string) (*MixedConfig, error) {
	if !utils.PathExist(path) {
		return nil, errors.New("config file not exist")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config MixedConfig
	if err = json.Unmarshal(b, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// NewFromString loads the config from raw config string in json format (stick to the config structure).
func NewFromString(c string) (*MixedConfig, error) {
	var config MixedConfig
	if err := json.Unmarshal([]byte(c), &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// Apply applies the MixedConfig
func (c *MixedConfig) Apply() error {
	switch c.Role {
	case RoleClient, RoleServer:
	default:
		return fmt.Errorf("unknown role: %s", c.Role)
	}
	c.LogMode.Set()
	if c.DNS != nil {
		if err := c.DNS.SetDefault(); err != nil {
			return err
		}
	}
	if c.Buffer > 0 {
		log.Printf("custom buffer size: %dK", c.Buffer)
		transport.SetBufferSize(c.Buffer)
	}
	if c.Path != "" {
		log.Printf("custom service name: %s", c.Path)
		proto.SetServiceName(c.Path)
	}
	if c.Role == RoleClient {
		if c.UUID == "" {
			return errors.New("client uuid empty")
		}
		rpcClient.SetUUID(c.UUID)
	}
	if c.Forward != "" {
		d, err := utils.LoadProxy(c.Forward)
		if err != nil {
			return err
		}
		forward.Transport.Attach(d)
		log.Println("forward-proxy attached")
	}
	if len(c.Routes) > 0 {
		router.SetRoutes(c.Routes)
	} else {
		var route *router.Route
		if c.Role == RoleClient {
			route = router.RouteDefault
		} else {
			route = router.RouteServerDefault
		}
		router.SetRoutes(router.Routes{route})
	}
	if !c.IPv6 {
		if c.Role == RoleServer {
			transport.DisableIPv6()
		}
		router.AddToFirstRoute(router.RouteBlockIPv6)
		log.Println("ipv6 disabled")
	}
	return router.GenerateCache()
}
