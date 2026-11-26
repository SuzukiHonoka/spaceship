//go:build linux && !android

package tun

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"slices"
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/redirect"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const nftTableName = "spaceship"

type linuxAutoRedirect struct {
	server    *redirect.Server
	tableName string
	serveDone chan struct{}
	closeOnce sync.Once
	closeErr  error
}

type autoRedirectRuleSpec struct {
	tableName      string
	tunName        string
	redirectPort   uint16
	bypassMark     uint32
	tunMark        uint32
	includeForward bool
	excludeV4      []netip.Prefix
	excludeV6      []netip.Prefix
}

func startLinuxAutoRedirect(
	ctx context.Context,
	cfg Config,
	tunName string,
	tunMark uint32,
	service *Service,
) (*linuxAutoRedirect, error) {
	listenAddr := ":0"
	if !hasIPv6Address(cfg.Address) {
		listenAddr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		// Fallback to IPv4 loopback on IPv4-only kernels.
		ln, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("tun: auto_redirect bind listener: %w", err)
		}
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || tcpAddr.Port <= 0 || tcpAddr.Port > 65535 {
		_ = ln.Close()
		return nil, fmt.Errorf("tun: auto_redirect unexpected listener address %v", ln.Addr())
	}
	port := uint16(tcpAddr.Port)

	redirectCfg := &redirect.Config{
		MaxConnections: cfg.MaxConnections,
	}
	if cfg.DNS.Enabled && service != nil {
		redirectCfg.TCPDNSHandler = service.ServeTCPDNS
		redirectCfg.DNSHijackAddresses = cfg.DNS.Addresses
	}
	srv, err := redirect.New(ctx, redirectCfg)
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("tun: auto_redirect create server: %w", err)
	}

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, context.Canceled) && !errors.Is(serveErr, net.ErrClosed) {
			log.Printf("tun: auto_redirect server stopped: %v", serveErr)
		}
	}()

	spec := buildAutoRedirectSpec(cfg, tunName, port, tunMark)
	if err := installNFTables(spec); err != nil {
		_ = srv.Close()
		<-serveDone
		return nil, err
	}

	log.Printf("tun: linux auto_redirect active (table=inet/%s, tcp_port=%d, bypass_mark=%#x, udp_tun_mark=%#x)",
		spec.tableName, port, spec.bypassMark, spec.tunMark)
	return &linuxAutoRedirect{
		server:    srv,
		tableName: spec.tableName,
		serveDone: serveDone,
	}, nil
}

func (a *linuxAutoRedirect) Close() error {
	a.closeOnce.Do(func() {
		var errs []error
		if err := cleanupNFTables(a.tableName); err != nil {
			errs = append(errs, err)
		}
		if a.server != nil {
			errs = append(errs, a.server.Close())
		}
		if a.serveDone != nil {
			<-a.serveDone
		}
		a.closeErr = errors.Join(errs...)
	})
	return a.closeErr
}

func hasIPv6Address(addrs []netip.Prefix) bool {
	for _, a := range addrs {
		if a.Addr().Is6() {
			return true
		}
	}
	return false
}

func buildAutoRedirectSpec(cfg Config, tunName string, port uint16, tunMark uint32) autoRedirectRuleSpec {
	spec := autoRedirectRuleSpec{
		tableName:      nftTableName,
		tunName:        tunName,
		redirectPort:   port,
		bypassMark:     cfg.BypassMark,
		tunMark:        tunMark,
		includeForward: cfg.AutoRedirectIncludeForward,
	}
	// Always exclude loopback so host-local traffic is never redirected.
	spec.excludeV4 = append(spec.excludeV4, netip.MustParsePrefix("127.0.0.0/8"))
	spec.excludeV6 = append(spec.excludeV6, netip.MustParsePrefix("::1/128"))
	for _, p := range cfg.RouteExcludeAddress {
		if p.Addr().Is4() {
			spec.excludeV4 = append(spec.excludeV4, p.Masked())
		} else if p.Addr().Is6() {
			spec.excludeV6 = append(spec.excludeV6, p.Masked())
		}
	}
	return spec
}

func cleanupNFTables(tableName string) error {
	conn, err := nftables.New()
	if err != nil {
		return fmt.Errorf("tun: open nftables connection: %w", err)
	}
	defer func() { _ = conn.CloseLasting() }()

	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return fmt.Errorf("tun: list nftables tables: %w", err)
	}
	found := false
	for _, t := range tables {
		if t.Name == tableName {
			conn.DelTable(t)
			found = true
		}
	}
	if !found {
		return nil
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("tun: delete nftables table inet/%s: %w", tableName, err)
	}
	return nil
}

func installNFTables(spec autoRedirectRuleSpec) error {
	// Self-heal any stale table left behind by a prior SIGKILL.
	_ = cleanupNFTables(spec.tableName)

	conn, err := nftables.New()
	if err != nil {
		return fmt.Errorf("tun: open nftables connection: %w", err)
	}
	defer func() { _ = conn.CloseLasting() }()

	if err := populateNFTables(conn, spec); err != nil {
		return err
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("tun: flush nftables table inet/%s: %w", spec.tableName, err)
	}
	return nil
}

func populateNFTables(conn *nftables.Conn, spec autoRedirectRuleSpec) error {
	table := conn.AddTable(&nftables.Table{
		Name:   spec.tableName,
		Family: nftables.TableFamilyINet,
	})

	var setV4, setV6 *nftables.Set
	if elems := prefixesToNFTIntervalElements4(spec.excludeV4); len(elems) > 0 {
		setV4 = &nftables.Set{
			Table:    table,
			Name:     "exclude_v4",
			Interval: true,
			KeyType:  nftables.TypeIPAddr,
		}
		if err := conn.AddSet(setV4, elems); err != nil {
			return fmt.Errorf("tun: add nftables exclude_v4 set: %w", err)
		}
	}
	if elems := prefixesToNFTIntervalElements6(spec.excludeV6); len(elems) > 0 {
		setV6 = &nftables.Set{
			Table:    table,
			Name:     "exclude_v6",
			Interval: true,
			KeyType:  nftables.TypeIP6Addr,
		}
		if err := conn.AddSet(setV6, elems); err != nil {
			return fmt.Errorf("tun: add nftables exclude_v6 set: %w", err)
		}
	}

	addCommonSkips := func(chain *nftables.Chain, skipTUNIn bool) {
		// 1. Exempt Spaceship's own marked outbound sockets.
		conn.AddRule(&nftables.Rule{
			Table: table,
			Chain: chain,
			Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(spec.bypassMark)},
				&expr.Verdict{Kind: expr.VerdictAccept},
			},
		})
		if skipTUNIn && spec.tunName != "" {
			conn.AddRule(&nftables.Rule{
				Table: table,
				Chain: chain,
				Exprs: []expr.Any{
					&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
					&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: nftIfname(spec.tunName)},
					&expr.Verdict{Kind: expr.VerdictAccept},
				},
			})
		}
		// 2. Skip host-local, broadcast, and multicast destinations via FIB lookup.
		for _, addrType := range []uint32{unix.RTN_LOCAL, unix.RTN_BROADCAST, unix.RTN_MULTICAST} {
			conn.AddRule(&nftables.Rule{
				Table: table,
				Chain: chain,
				Exprs: []expr.Any{
					&expr.Fib{Register: 1, ResultADDRTYPE: true, FlagDADDR: true},
					&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(addrType)},
					&expr.Verdict{Kind: expr.VerdictAccept},
				},
			})
		}
		// 3. Skip configured excluded IPv4/IPv6 CIDRs.
		if setV4 != nil {
			conn.AddRule(&nftables.Rule{
				Table: table,
				Chain: chain,
				Exprs: []expr.Any{
					&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
					&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
					&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
					&expr.Lookup{SourceRegister: 1, SetName: setV4.Name, SetID: setV4.ID},
					&expr.Verdict{Kind: expr.VerdictAccept},
				},
			})
		}
		if setV6 != nil {
			conn.AddRule(&nftables.Rule{
				Table: table,
				Chain: chain,
				Exprs: []expr.Any{
					&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
					&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV6}},
					&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
					&expr.Lookup{SourceRegister: 1, SetName: setV6.Name, SetID: setV6.ID},
					&expr.Verdict{Kind: expr.VerdictAccept},
				},
			})
		}
	}

	// Chain 1: NAT OUTPUT — redirect local TCP directly to internal redirect.Server.
	natOutput := conn.AddChain(&nftables.Chain{
		Name:     "output_tcp",
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityRef(*nftables.ChainPriorityNATDest - 1),
	})
	addCommonSkips(natOutput, false)
	conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: natOutput,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
			&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(spec.redirectPort)},
			&expr.Redir{RegisterProtoMin: 1},
		},
	})

	// Chain 2: ROUTE OUTPUT — mark local UDP so the policy rule routes it into TUN.
	routeUDP := conn.AddChain(&nftables.Chain{
		Name:     "output_udp",
		Table:    table,
		Type:     nftables.ChainTypeRoute,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityMangle,
	})
	addCommonSkips(routeUDP, false)
	conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: routeUDP,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_UDP}},
			&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(spec.tunMark)},
			&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
		},
	})

	// Chain 3: FILTER INPUT — block direct non-NAT connections to the internal redirect port.
	filterInput := conn.AddChain(&nftables.Chain{
		Name:     "input_guard",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookInput,
		Priority: nftables.ChainPriorityFilter,
	})
	const ipsDstNAT = 0x20 // IPS_DST_NAT bit in nf_conntrack status
	conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: filterInput,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(spec.redirectPort)},
			&expr.Ct{Register: 1, Key: expr.CtKeySTATUS},
			&expr.Bitwise{
				SourceRegister: 1,
				DestRegister:   1,
				Len:            4,
				Mask:           binaryutil.NativeEndian.PutUint32(ipsDstNAT),
				Xor:            binaryutil.NativeEndian.PutUint32(0),
			},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
			&expr.Reject{Type: unix.NFT_REJECT_TCP_RST, Code: unix.NFT_REJECT_ICMPX_ADMIN_PROHIBITED},
		},
	})

	// Optional Chains 4 & 5: PREROUTING for routed/LAN gateway forwarding.
	if spec.includeForward {
		preTCP := conn.AddChain(&nftables.Chain{
			Name:     "prerouting_tcp",
			Table:    table,
			Type:     nftables.ChainTypeNAT,
			Hooknum:  nftables.ChainHookPrerouting,
			Priority: nftables.ChainPriorityRef(*nftables.ChainPriorityNATDest - 1),
		})
		addCommonSkips(preTCP, true)
		conn.AddRule(&nftables.Rule{
			Table: table,
			Chain: preTCP,
			Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
				&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(spec.redirectPort)},
				&expr.Redir{RegisterProtoMin: 1},
			},
		})

		preUDP := conn.AddChain(&nftables.Chain{
			Name:     "prerouting_udp",
			Table:    table,
			Type:     nftables.ChainTypeFilter,
			Hooknum:  nftables.ChainHookPrerouting,
			Priority: nftables.ChainPriorityMangle,
		})
		addCommonSkips(preUDP, true)
		conn.AddRule(&nftables.Rule{
			Table: table,
			Chain: preUDP,
			Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_UDP}},
				&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(spec.tunMark)},
				&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
			},
		})
	}

	return nil
}

func nftIfname(name string) []byte {
	b := make([]byte, 16)
	copy(b, name+"\x00")
	return b
}

// prefixesToNFTIntervalElements4 converts IPv4 CIDRs into merged, sorted
// half-open [start, end+1) nftables interval set elements.
func prefixesToNFTIntervalElements4(prefixes []netip.Prefix) []nftables.SetElement {
	type r4 struct{ start, end uint32 }
	var ranges []r4
	for _, p := range prefixes {
		if !p.IsValid() || !p.Addr().Is4() {
			continue
		}
		mp := p.Masked()
		b := mp.Addr().As4()
		start := binary.BigEndian.Uint32(b[:])
		var end uint32
		if mp.Bits() == 0 {
			start, end = 0, ^uint32(0)
		} else {
			end = start | (uint32(1)<<(32-mp.Bits()) - 1)
		}
		ranges = append(ranges, r4{start, end})
	}
	if len(ranges) == 0 {
		return nil
	}
	slices.SortFunc(ranges, func(a, b r4) int {
		if c := cmp.Compare(a.start, b.start); c != 0 {
			return c
		}
		return cmp.Compare(a.end, b.end)
	})
	merged := ranges[:1]
	for _, cur := range ranges[1:] {
		last := &merged[len(merged)-1]
		if last.end == ^uint32(0) || cur.start <= last.end+1 {
			if cur.end > last.end {
				last.end = cur.end
			}
			continue
		}
		merged = append(merged, cur)
	}
	var elems []nftables.SetElement
	for _, r := range merged {
		var s, e [4]byte
		binary.BigEndian.PutUint32(s[:], r.start)
		elems = append(elems, nftables.SetElement{Key: s[:], IntervalEnd: false})
		if r.end != ^uint32(0) {
			binary.BigEndian.PutUint32(e[:], r.end+1)
			elems = append(elems, nftables.SetElement{Key: e[:], IntervalEnd: true})
		}
	}
	return elems
}

func prefixesToNFTIntervalElements6(prefixes []netip.Prefix) []nftables.SetElement {
	type r6 struct{ sHi, sLo, eHi, eLo uint64 }
	cmp128 := func(aHi, aLo, bHi, bLo uint64) int {
		if c := cmp.Compare(aHi, bHi); c != 0 {
			return c
		}
		return cmp.Compare(aLo, bLo)
	}
	var ranges []r6
	for _, p := range prefixes {
		if !p.IsValid() || !p.Addr().Is6() {
			continue
		}
		mp := p.Masked()
		b := mp.Addr().As16()
		sHi := binary.BigEndian.Uint64(b[:8])
		sLo := binary.BigEndian.Uint64(b[8:])
		var eHi, eLo uint64
		switch bits := mp.Bits(); {
		case bits == 0:
			sHi, sLo, eHi, eLo = 0, 0, ^uint64(0), ^uint64(0)
		case bits < 64:
			eHi = sHi | (uint64(1)<<(64-bits) - 1)
			eLo = ^uint64(0)
		case bits == 64:
			eHi = sHi
			eLo = ^uint64(0)
		default:
			eHi = sHi
			eLo = sLo | (uint64(1)<<(128-bits) - 1)
		}
		ranges = append(ranges, r6{sHi, sLo, eHi, eLo})
	}
	if len(ranges) == 0 {
		return nil
	}
	slices.SortFunc(ranges, func(a, b r6) int {
		if c := cmp128(a.sHi, a.sLo, b.sHi, b.sLo); c != 0 {
			return c
		}
		return cmp128(a.eHi, a.eLo, b.eHi, b.eLo)
	})
	merged := ranges[:1]
	for _, cur := range ranges[1:] {
		last := &merged[len(merged)-1]
		adj := last.eHi == ^uint64(0) && last.eLo == ^uint64(0)
		if !adj {
			nHi, nLo := last.eHi, last.eLo+1
			if nLo == 0 {
				nHi++
			}
			adj = cmp128(cur.sHi, cur.sLo, nHi, nLo) <= 0
		}
		if adj {
			if cmp128(cur.eHi, cur.eLo, last.eHi, last.eLo) > 0 {
				last.eHi, last.eLo = cur.eHi, cur.eLo
			}
			continue
		}
		merged = append(merged, cur)
	}
	var elems []nftables.SetElement
	for _, r := range merged {
		var s, e [16]byte
		binary.BigEndian.PutUint64(s[:8], r.sHi)
		binary.BigEndian.PutUint64(s[8:], r.sLo)
		elems = append(elems, nftables.SetElement{Key: s[:], IntervalEnd: false})
		if r.eHi != ^uint64(0) || r.eLo != ^uint64(0) {
			nHi, nLo := r.eHi, r.eLo+1
			if nLo == 0 {
				nHi++
			}
			binary.BigEndian.PutUint64(e[:8], nHi)
			binary.BigEndian.PutUint64(e[8:], nLo)
			elems = append(elems, nftables.SetElement{Key: e[:], IntervalEnd: true})
		}
	}
	return elems
}
