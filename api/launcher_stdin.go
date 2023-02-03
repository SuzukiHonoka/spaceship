package api

import "github.com/SuzukiHonoka/spaceship/internal/config"

func LaunchFromString(c string) {
	m := config.NewFromString(c)
	Launch(m)
}
