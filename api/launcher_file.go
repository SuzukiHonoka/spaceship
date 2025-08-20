package api

import (
	"fmt"

	"github.com/SuzukiHonoka/spaceship/v2/pkg/config"
)

func (l *Launcher) LaunchFromFile(path string) error {
	m, err := config.NewFromConfigFile(path)
	if err != nil {
		return fmt.Errorf("load configuration from %s failed, err=%w", path, err)
	}
	return l.Launch(m)
}
