package api

import (
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config"
	"log"
)

func (l *Launcher) LaunchFromString(c string) bool {
	m, err := config.NewFromString(c)
	if err != nil {
		log.Printf("Load configuration from string err: %v", err)
		return false
	}
	return l.Launch(m)
}
