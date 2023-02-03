package api

import (
	"github.com/SuzukiHonoka/spaceship/pkg/config"
)

func LaunchFromString(c string) {
	m := config.NewFromString(c)
	Launch(m)
}
