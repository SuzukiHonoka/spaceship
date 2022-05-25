package config

import (
	"encoding/json"
	"log"
	"os"
	"spaceship/internal/config/client"
	"spaceship/internal/config/server"
	"spaceship/internal/util"
)

type Config struct {
	Role
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
	var config Config
	err = json.Unmarshal(b, &config)
	util.StopIfError(err)
	return config
}
