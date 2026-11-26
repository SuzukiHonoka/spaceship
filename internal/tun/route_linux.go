//go:build linux && !android

package tun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/netmon"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	linuxAutoRouteTableIndex   = 2022
	linuxAutoRouteRuleBasePrio = 9000
	// linuxAutoRedirectTUNMark marks UDP traffic in nftables so a dedicated ip
	// rule routes only marked UDP into the TUN device while TCP goes straight
	// to the native redirect server. Distinct from transport.DefaultBypassMark.
	linuxAutoRedirectTUNMark uint32 = 0x5351
)

type linuxRouteController struct {
	deviceName string
	linkIndex  int
	tableIndex int

	addedAddrs []*netlink.Addr
	addedRules []*netlink.Rule
	addedRoute []*netlink.Route

	rpFilterPath string
	prevRPFilter string
	rpFilterSet  bool

	autoRedirect *linuxAutoRedirect
	monitor      netmon.Monitor

	closeOnce sync.Once
	closeErr  error
}

func setupPlatformAutoRoute(ctx context.Context, cfg Config, deviceName string, service *Service) (routeController, error) {
	link, err := netlink.LinkByName(deviceName)
	if err != nil {
		return nil, fmt.Errorf("tun: netlink lookup %s: %w", deviceName, err)
	}
	ctrl := &linuxRouteController{
		deviceName: deviceName,
		linkIndex:  link.Attrs().Index,
		tableIndex: linuxAutoRouteTableIndex,
	}

	// Assign IPv4/IPv6 addresses.
	hasV4, hasV6 := false, false
	for _, prefix := range cfg.Address {
		nlAddr, parseErr := netlink.ParseAddr(prefix.String())
		if parseErr != nil {
			_ = ctrl.Close()
			return nil, fmt.Errorf("tun: parse address %s: %w", prefix, parseErr)
		}
		if err := netlink.AddrReplace(link, nlAddr); err != nil {
			_ = ctrl.Close()
			return nil, fmt.Errorf("tun: add address %s on %s: %w", prefix, deviceName, err)
		}
		ctrl.addedAddrs = append(ctrl.addedAddrs, nlAddr)
		if prefix.Addr().Is4() {
			hasV4 = true
		} else if prefix.Addr().Is6() {
			hasV6 = true
		}
	}

	// Relax strict rp_filter (1 -> 2 loose) on the TUN interface so full-cone
	// UDP replies returning from TUN with arbitrary source IPs are not dropped
	// by the Linux kernel's reverse-path check.
	ctrl.relaxRPFilter(deviceName)

	// Install default routes inside our dedicated policy routing table.
	if hasV4 {
		route4 := &netlink.Route{
			LinkIndex: ctrl.linkIndex,
			Table:     ctrl.tableIndex,
			Dst:       &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		}
		if err := netlink.RouteReplace(route4); err != nil {
			_ = ctrl.Close()
			return nil, fmt.Errorf("tun: add ipv4 policy route: %w", err)
		}
		ctrl.addedRoute = append(ctrl.addedRoute, route4)
	}
	if hasV6 {
		route6 := &netlink.Route{
			LinkIndex: ctrl.linkIndex,
			Table:     ctrl.tableIndex,
			Dst:       &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		}
		if err := netlink.RouteReplace(route6); err != nil {
			_ = ctrl.Close()
			return nil, fmt.Errorf("tun: add ipv6 policy route: %w", err)
		}
		ctrl.addedRoute = append(ctrl.addedRoute, route6)
	}

	if cfg.AutoRedirect {
		tunMark := pickAutoRedirectTUNMark(cfg.BypassMark)
		if err := ctrl.installAutoRedirectRules(hasV4, hasV6, tunMark); err != nil {
			_ = ctrl.Close()
			return nil, err
		}
		ar, err := startLinuxAutoRedirect(ctx, cfg, deviceName, tunMark, service)
		if err != nil {
			_ = ctrl.Close()
			return nil, err
		}
		ctrl.autoRedirect = ar
	} else {
		if err := ctrl.installPolicyRules(cfg, hasV4, hasV6); err != nil {
			_ = ctrl.Close()
			return nil, err
		}
	}

	if cfg.OutboundInterface != "" {
		if err := transport.SetOutboundInterface(cfg.OutboundInterface); err != nil {
			_ = ctrl.Close()
			return nil, err
		}
	}
	if mon, err := netmon.NewMonitor(deviceName); err == nil {
		if startErr := mon.Start(func(netmon.DefaultInterface) {
			if service != nil {
				service.ResetNetwork()
			}
		}); startErr == nil {
			ctrl.monitor = mon
		} else {
			_ = mon.Close()
		}
	}

	return ctrl, nil
}

func pickAutoRedirectTUNMark(bypassMark uint32) uint32 {
	if linuxAutoRedirectTUNMark != bypassMark {
		return linuxAutoRedirectTUNMark
	}
	return bypassMark ^ 1
}

func (c *linuxRouteController) relaxRPFilter(ifName string) {
	effective := 0
	for _, path := range []string{
		"/proc/sys/net/ipv4/conf/all/rp_filter",
		"/proc/sys/net/ipv4/conf/" + ifName + "/rp_filter",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if v, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			effective = max(effective, v)
		}
	}
	if effective != 1 {
		return
	}
	ifPath := "/proc/sys/net/ipv4/conf/" + ifName + "/rp_filter"
	prev, err := os.ReadFile(ifPath)
	if err != nil {
		return
	}
	if err := os.WriteFile(ifPath, []byte("2"), 0o644); err == nil {
		c.rpFilterPath = ifPath
		c.prevRPFilter = strings.TrimSpace(string(prev))
		c.rpFilterSet = true
	}
}

func (c *linuxRouteController) addRule(rule *netlink.Rule) error {
	// Clean any identical stale rule before adding.
	_ = netlink.RuleDel(rule)
	if err := netlink.RuleAdd(rule); err != nil {
		return err
	}
	c.addedRules = append(c.addedRules, rule)
	return nil
}

func (c *linuxRouteController) installAutoRedirectRules(hasV4, hasV6 bool, tunMark uint32) error {
	families := make([]int, 0, 2)
	if hasV4 {
		families = append(families, unix.AF_INET)
	}
	if hasV6 {
		families = append(families, unix.AF_INET6)
	}
	for _, fam := range families {
		rule := netlink.NewRule()
		rule.Family = fam
		rule.Priority = linuxAutoRouteRuleBasePrio
		rule.Mark = tunMark
		mask := uint32(0xffffffff)
		rule.Mask = &mask
		rule.Table = c.tableIndex
		if err := c.addRule(rule); err != nil {
			return fmt.Errorf("tun: add auto_redirect fwmark rule (family=%d): %w", fam, err)
		}
	}
	return nil
}

func (c *linuxRouteController) installPolicyRules(cfg Config, hasV4, hasV6 bool) error {
	addForFamily := func(fam int, isV6 bool) error {
		// Priority 8990: keep main-table non-default routes (LAN / loopback / host routes).
		suppress := netlink.NewRule()
		suppress.Family = fam
		suppress.Priority = linuxAutoRouteRuleBasePrio - 10
		suppress.Table = unix.RT_TABLE_MAIN
		suppress.SuppressPrefixlen = 0
		if err := c.addRule(suppress); err != nil {
			return fmt.Errorf("tun: add suppress_prefixlength rule: %w", err)
		}

		// Priority 8995: route_exclude_address stays in main table.
		for _, exc := range cfg.RouteExcludeAddress {
			if exc.Addr().Is6() != isV6 {
				continue
			}
			r := netlink.NewRule()
			r.Family = fam
			r.Priority = linuxAutoRouteRuleBasePrio - 5
			r.Dst = prefixToIPNet(exc)
			r.Table = unix.RT_TABLE_MAIN
			if err := c.addRule(r); err != nil {
				return fmt.Errorf("tun: add exclude rule %s: %w", exc, err)
			}
		}

		// Priority 9000: all unmarked traffic (mark != BypassMark) goes to TUN table.
		capture := netlink.NewRule()
		capture.Family = fam
		capture.Priority = linuxAutoRouteRuleBasePrio
		capture.Mark = cfg.BypassMark
		mask := uint32(0xffffffff)
		capture.Mask = &mask
		capture.Invert = true
		capture.Table = c.tableIndex
		if err := c.addRule(capture); err != nil {
			return fmt.Errorf("tun: add policy capture rule: %w", err)
		}
		return nil
	}

	if hasV4 {
		if err := addForFamily(unix.AF_INET, false); err != nil {
			return err
		}
	}
	if hasV6 {
		if err := addForFamily(unix.AF_INET6, true); err != nil {
			return err
		}
	}
	return nil
}

func prefixToIPNet(p netip.Prefix) *net.IPNet {
	p = p.Masked()
	return &net.IPNet{
		IP:   net.IP(p.Addr().AsSlice()),
		Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
	}
}

func (c *linuxRouteController) Close() error {
	c.closeOnce.Do(func() {
		var errs []error
		if c.monitor != nil {
			errs = append(errs, c.monitor.Close())
		}
		if c.autoRedirect != nil {
			errs = append(errs, c.autoRedirect.Close())
		}
		for i := len(c.addedRules) - 1; i >= 0; i-- {
			_ = netlink.RuleDel(c.addedRules[i])
		}
		for i := len(c.addedRoute) - 1; i >= 0; i-- {
			_ = netlink.RouteDel(c.addedRoute[i])
		}
		if link, err := netlink.LinkByIndex(c.linkIndex); err == nil {
			for _, addr := range c.addedAddrs {
				_ = netlink.AddrDel(link, addr)
			}
		}
		if c.rpFilterSet && c.rpFilterPath != "" {
			_ = os.WriteFile(c.rpFilterPath, []byte(c.prevRPFilter), 0o644)
		}
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}
