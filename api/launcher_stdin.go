package api

import (
	"fmt"

	"github.com/SuzukiHonoka/spaceship/v2/pkg/config"
)

func (l *Launcher) LaunchFromString(c string) error {
	m, err := config.NewFromString(c)
	if err != nil {
		return fmt.Errorf("load configuration from string failed, err=%w", err)
	}
	return l.Launch(m)
}
