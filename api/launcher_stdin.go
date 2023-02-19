package api

import (
	"github.com/SuzukiHonoka/spaceship/pkg/config"
)

func (l *Launcher) LaunchFromString(c string) {
	m := config.NewFromString(c)
	l.Launch(m)
}
