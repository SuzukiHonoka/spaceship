package api

import (
	"github.com/SuzukiHonoka/spaceship/pkg/config"
)

func (l *Launcher) LaunchFromFile(path string) {
	m := config.NewFromConfigFile(path)
	l.Launch(m)
}
