package api

import (
	"github.com/SuzukiHonoka/spaceship/pkg/config"
)

func LaunchFromFile(path string) {
	m := config.NewFromConfigFile(path)
	Launch(m)
}
