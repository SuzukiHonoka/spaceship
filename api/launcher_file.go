package api

import "spaceship/internal/config"

func LaunchFromFile(path string) {
	m := config.NewFromConfigFile(path)
	Launch(m)
}
