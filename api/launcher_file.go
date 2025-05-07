package api

import (
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config"
	"log"
)

func (l *Launcher) LaunchFromFile(path string) bool {
	m, err := config.NewFromConfigFile(path)
	if err != nil {
		log.Printf("Load configuration from %s error: %v", path, err)
		return false
	}
	return l.Launch(m)
}
