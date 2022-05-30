package config

import (
	"encoding/json"
	"log"
	"os"
	"spaceship/internal/config/client"
	"spaceship/internal/config/server"
	"spaceship/internal/dns"
	"spaceship/internal/transport"
	"spaceship/internal/util"
)

type Config struct {
	Role
	*dns.DNS `json:"dns,omitempty"`
	client.Client
	server.Server
}

var LoadedConfig Config

func Load(path string) Config {
	// check if path exist
	if !util.FileExist(path) {
		log.Fatalln("config file not exist")
	}
	// load config
	b, err := os.ReadFile(path)
	util.StopIfError(err)
	// actual parsing
	err = json.Unmarshal(b, &LoadedConfig)
	util.StopIfError(err)
	// uuid table
	for _, u := range LoadedConfig.Users {
		transport.UUIDs[u.UUID.String()] = true
	}
	return LoadedConfig
}
