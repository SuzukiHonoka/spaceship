package api

import "spaceship/internal/config"

func LaunchFromString(c string) {
	m := config.NewFromString(c)
	Launch(m)
}
