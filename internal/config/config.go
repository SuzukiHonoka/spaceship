package config

import (
	"encoding/json"
	"log"
	"os"
	"spaceship/internal/config/client"
	"spaceship/internal/config/server"
	"spaceship/internal/dns"
	"spaceship/internal/logger"
	"spaceship/internal/transport"
	rpcClient "spaceship/internal/transport/rpc/client"
	proxy "spaceship/internal/transport/rpc/proto"
	"spaceship/internal/util"
)

type MixedConfig struct {
	Role     `json:"role"`
	*dns.DNS `json:"dns,omitempty"`
	CAs      []string    `json:"cas,omitempty"`
	LogMode  logger.Mode `json:"log,omitempty"`
	client.Client
	server.Server
}

func NewFromConfigFile(path string) *MixedConfig {
	var config MixedConfig
	if !util.FileExist(path) {
		log.Fatalln("config file not exist")
	}
	b, err := os.ReadFile(path)
	util.StopIfError(err)
	err = json.Unmarshal(b, &config)
	util.StopIfError(err)
	return &config
}

func NewFromString(c string) *MixedConfig {
	var config MixedConfig
	err := json.Unmarshal([]byte(c), &config)
	util.StopIfError(err)
	return &config
}

func (c *MixedConfig) Apply() {
	c.LogMode.Set()
	if c.DNS != nil {
		c.DNS.SetDefault()
	}
	if c.Buffer > 0 {
		log.Printf("custom buffer size: %dK", c.Buffer)
		transport.SetBufferSize(c.Buffer)
	}
	if c.Path != "" {
		log.Printf("custom service name: %s", c.Path)
		proxy.SetServiceName(c.Path)
	}
	if !c.IPv6 {
		log.Println("ipv6 disabled")
		transport.DisableIPv6()
	}
	if c.Role == RoleClient {
		rpcClient.UUID = c.UUID
	}
}
