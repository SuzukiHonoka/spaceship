//go:build android || (!linux && !darwin && !windows)

package tun

import (
	"context"
	"errors"
)

func setupPlatformAutoRoute(context.Context, Config, string, *Service) (routeController, error) {
	return nil, errors.New("tun: route_mode \"auto\" is not supported on this platform")
}
