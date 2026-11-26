//go:build windows

package tun

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/netmon"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

type windowsRouteController struct {
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
		return nil, fmt.Errorf("tun: create windows interface monitor: %w", err)
	}

	if cfg.OutboundInterface != "" {
		if err := transport.SetOutboundInterface(cfg.OutboundInterface); err != nil {
			_ = mon.Close()
			return nil, err
		}
	} else {
		cur, curErr := mon.Current()
		if curErr != nil {
			_ = mon.Close()
			return nil, fmt.Errorf("tun: detect windows default interface: %w", curErr)
		}
		transport.SetOutboundInterfaceIndex(cur.Index)
	}

	ctrl := &windowsRouteController{
		deviceName: deviceName,
		monitor:    mon,
		prevIfIdx:  prevIfIdx,
	}

	hasV4, hasV6 := false, false
	for _, prefix := range cfg.Address {
		addr := prefix.Addr().Unmap()
		if addr.Is4() {
			mask := ipv4PrefixMask(prefix.Bits())
			if out, err := exec.Command("netsh", "interface", "ipv4", "set", "address",
				"name="+deviceName, "source=static",
				"address="+addr.String(), "mask="+mask).CombinedOutput(); err != nil {
				_ = ctrl.Close()
				return nil, fmt.Errorf("tun: set windows ipv4 address %s: %w (%s)", prefix, err, string(out))
			}
			_ = exec.Command("netsh", "interface", "ipv4", "set", "subinterface",
				deviceName, "mtu="+strconv.Itoa(cfg.MTU), "store=active").Run()
			hasV4 = true
		} else if addr.Is6() {
			if out, err := exec.Command("netsh", "interface", "ipv6", "add", "address",
				deviceName, prefix.String()).CombinedOutput(); err != nil {
				_ = ctrl.Close()
				return nil, fmt.Errorf("tun: set windows ipv6 address %s: %w (%s)", prefix, err, string(out))
			}
			_ = exec.Command("netsh", "interface", "ipv6", "set", "subinterface",
				deviceName, "mtu="+strconv.Itoa(cfg.MTU), "store=active").Run()
			hasV6 = true
		}
	}

	if hasV4 {
		for _, cidr := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
			if out, err := exec.Command("netsh", "interface", "ipv4", "add", "route",
				cidr, deviceName, "metric=0", "store=active").CombinedOutput(); err != nil {
				_ = ctrl.Close()
				return nil, fmt.Errorf("tun: add windows ipv4 route %s: %w (%s)", cidr, err, string(out))
			}
			ctrl.v4Routes = append(ctrl.v4Routes, cidr)
		}
	}
	if hasV6 {
		for _, cidr := range []string{"::/1", "8000::/1"} {
			if out, err := exec.Command("netsh", "interface", "ipv6", "add", "route",
				cidr, deviceName, "metric=0", "store=active").CombinedOutput(); err != nil {
				_ = ctrl.Close()
				return nil, fmt.Errorf("tun: add windows ipv6 route %s: %w (%s)", cidr, err, string(out))
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
		return nil, fmt.Errorf("tun: start windows interface monitor: %w", err)
	}

	return ctrl, nil
}

func ipv4PrefixMask(bits int) string {
	var m uint32
	if bits > 0 {
		m = ^uint32(0) << (32 - bits)
	}
	addr := netip.AddrFrom4([4]byte{byte(m >> 24), byte(m >> 16), byte(m >> 8), byte(m)})
	return addr.String()
}

func (c *windowsRouteController) Close() error {
	c.closeOnce.Do(func() {
		var errs []error
		if c.monitor != nil {
			errs = append(errs, c.monitor.Close())
		}
		for _, cidr := range c.v4Routes {
			_ = exec.Command("netsh", "interface", "ipv4", "delete", "route", cidr, c.deviceName, "store=active").Run()
		}
		for _, cidr := range c.v6Routes {
			_ = exec.Command("netsh", "interface", "ipv6", "delete", "route", cidr, c.deviceName, "store=active").Run()
		}
		transport.SetOutboundInterfaceIndex(c.prevIfIdx)
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}
