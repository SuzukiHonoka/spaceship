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
	"spaceship/internal/util"
)

type Config struct {
	Role
	*dns.DNS   `json:"dns,omitempty"`
	LoggerMode logger.Mode `json:"log,omitempty"`
	client.Client
	server.Server
}

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
	if LoadedConfig.Users != nil {
		for _, u := range LoadedConfig.Users {
			transport.UUIDs[u.UUID.String()] = struct{}{}
		}
	}
	return LoadedConfig
}
