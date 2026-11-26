//go:build darwin

package tun

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/netmon"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

type darwinRouteController struct {
	deviceName string
	monitor    netmon.Monitor
	prevIfIdx  uint32
	v4Routes   []string
	v6Routes   []string
	closeOnce  sync.Once
	closeErr   error
}

func setupPlatformAutoRoute(_ context.Context, cfg Config, deviceName string, service *Service) (routeController, error) {
	prevIfIdx := transport.OutboundInterfaceIndex()

	mon, err := netmon.NewMonitor(deviceName)
	if err != nil {
		return nil, fmt.Errorf("tun: create darwin interface monitor: %w", err)
	}

	var defaultIf netmon.DefaultInterface
	if cfg.OutboundInterface != "" {
		if err := transport.SetOutboundInterface(cfg.OutboundInterface); err != nil {
			_ = mon.Close()
			return nil, err
		}
		if cur, curErr := mon.Current(); curErr == nil {
			defaultIf = cur
		}
	} else {
		cur, curErr := mon.Current()
		if curErr != nil {
			_ = mon.Close()
			return nil, fmt.Errorf("tun: detect darwin default interface: %w", curErr)
		}
		defaultIf = cur
		transport.SetOutboundInterfaceIndex(cur.Index)
	}

	ctrl := &darwinRouteController{
		deviceName: deviceName,
		monitor:    mon,
		prevIfIdx:  prevIfIdx,
	}

	// Assign IPv4/IPv6 addresses to utunN.
	hasV4, hasV6 := false, false
	for _, prefix := range cfg.Address {
		addr := prefix.Addr().Unmap()
		if addr.Is4() {
			peer := addr.Next()
			if !prefix.Contains(peer) {
				peer = addr
			}
			if out, err := exec.Command("ifconfig", deviceName, "inet", addr.String(), peer.String(), "up").CombinedOutput(); err != nil {
				_ = ctrl.Close()
				return nil, fmt.Errorf("tun: ifconfig %s %s: %w (%s)", deviceName, prefix, err, string(out))
			}
			hasV4 = true
		} else if addr.Is6() {
			if out, err := exec.Command("ifconfig", deviceName, "inet6", prefix.String(), "up").CombinedOutput(); err != nil {
				_ = ctrl.Close()
				return nil, fmt.Errorf("tun: ifconfig %s %s: %w (%s)", deviceName, prefix, err, string(out))
			}
			hasV6 = true
		}
	}

	// Install explicit exclusions via physical gateway before split-default routes.
	for _, exc := range cfg.RouteExcludeAddress {
		if exc.Addr().Is4() && defaultIf.Gateway4.IsValid() {
			if err := runDarwinRoute("add", "-net", exc.String(), defaultIf.Gateway4.String()); err == nil {
				ctrl.v4Routes = append(ctrl.v4Routes, exc.String())
			}
		} else if exc.Addr().Is6() && defaultIf.Gateway6.IsValid() {
			if err := runDarwinRoute("add", "-inet6", "-net", exc.String(), defaultIf.Gateway6.String()); err == nil {
				ctrl.v6Routes = append(ctrl.v6Routes, exc.String())
			}
		}
	}

	// Split-default routes override 0.0.0.0/0 and ::/0 without deleting the host's
	// physical default gateway, making rollback atomic and crash-resilient.
	if hasV4 {
		for _, cidr := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
			if err := runDarwinRoute("add", "-net", cidr, "-interface", deviceName); err != nil {
				_ = ctrl.Close()
				return nil, fmt.Errorf("tun: add darwin IPv4 route %s: %w", cidr, err)
			}
			ctrl.v4Routes = append(ctrl.v4Routes, cidr)
		}
	}
	if hasV6 {
		for _, cidr := range []string{"::/1", "8000::/1"} {
			if err := runDarwinRoute("add", "-inet6", "-net", cidr, "-interface", deviceName); err != nil {
				_ = ctrl.Close()
				return nil, fmt.Errorf("tun: add darwin IPv6 route %s: %w", cidr, err)
			}
			ctrl.v6Routes = append(ctrl.v6Routes, cidr)
		}
	}

	if err := mon.Start(func(next netmon.DefaultInterface) {
		if cfg.OutboundInterface == "" && next.Index != 0 {
			transport.SetOutboundInterfaceIndex(next.Index)
		}
		if service != nil {
			service.ResetNetwork()
		}
	}); err != nil {
		_ = ctrl.Close()
		return nil, fmt.Errorf("tun: start darwin interface monitor: %w", err)
	}

	return ctrl, nil
}

func runDarwinRoute(args ...string) error {
	out, err := exec.Command("route", append([]string{"-n"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (%s)", err, string(out))
	}
	return nil
}

func (c *darwinRouteController) Close() error {
	c.closeOnce.Do(func() {
		var errs []error
		if c.monitor != nil {
			errs = append(errs, c.monitor.Close())
		}
		for _, cidr := range c.v4Routes {
			_ = runDarwinRoute("delete", "-net", cidr)
		}
		for _, cidr := range c.v6Routes {
			_ = runDarwinRoute("delete", "-inet6", "-net", cidr)
		}
		transport.SetOutboundInterfaceIndex(c.prevIfIdx)
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}

var _ = netip.Prefix{}
