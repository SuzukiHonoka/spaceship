package config

import (
	"encoding/json"
	"errors"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/router"
	rpcClient "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/client"
	proxy "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/internal/util"
	"github.com/SuzukiHonoka/spaceship/pkg/config/client"
	"github.com/SuzukiHonoka/spaceship/pkg/config/server"
	"github.com/SuzukiHonoka/spaceship/pkg/dns"
	"github.com/SuzukiHonoka/spaceship/pkg/logger"
	"log"
	"os"
)

type MixedConfig struct {
	Role     `json:"role"`
	*dns.DNS `json:"dns,omitempty"`
	CAs      []string    `json:"cas,omitempty"`
	LogMode  logger.Mode `json:"log,omitempty"`
	client.Client
	server.Server
}

func NewFromConfigFile(path string) (*MixedConfig, error) {
	if !util.FileExist(path) {
		return nil, errors.New("config file not exist")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config MixedConfig
	err = json.Unmarshal(b, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func NewFromString(c string) (*MixedConfig, error) {
	var config MixedConfig
	err := json.Unmarshal([]byte(c), &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (c *MixedConfig) Apply() error {
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
		proxy.SetServiceName(c.Path)
	}
	if c.Role == RoleClient {
		rpcClient.SetUUID(c.UUID)
	}
	if c.Routes != nil {
		router.SetRoutes(*c.Routes)
	}

	if !c.IPv6 {
		log.Println("ipv6 disabled")
		switch c.Role {
		case RoleServer:
			transport.DisableIPv6()
		default:
			if router.GetCount() == 0 {
				router.SetRoutes(router.Routes{
					router.RouteBlockIPv6,
					router.RouteDefault,
				})
			} else {
				router.AddToFirstRoute(router.RouteBlockIPv6)
			}
		}
	}
	if err := router.GetRoutes().GenerateCache(); err != nil {
		return err
	}
	return nil
}
