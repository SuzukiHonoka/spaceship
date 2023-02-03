package api

import "github.com/SuzukiHonoka/spaceship/internal/config"

func LaunchFromFile(path string) {
	m := config.NewFromConfigFile(path)
	Launch(m)
}
