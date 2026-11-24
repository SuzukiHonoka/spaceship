package router

import (
	"bufio"
	"cmp"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

var (
	RouteServerDefault = &Route{
		Destination: EgressDirect,
		MatchType:   TypeDefault,
	}
	RouteClientDefault = &Route{
		Destination: EgressProxy,
		MatchType:   TypeDefault,
	}
	RouteBlockIPv6 = &Route{
		Sources: []string{
			"::/0",
		},
		Destination: EgressBlock,
		MatchType:   TypeCIDR,
	}
)

// CloneRoute returns a shallow copy of r so shared templates (defaults, IPv6
// block) are not mutated when installed into a live route table.
func CloneRoute(r *Route) *Route {
	if r == nil {
		return nil
	}
	out := *r
	if r.Sources != nil {
		out.Sources = append([]string(nil), r.Sources...)
	}
	// Match caches are rebuilt by GenerateCache; do not share them.
	out.cache = MatchCache{}
	return &out
}

type Route struct {
	Sources     []string `json:"src"`
	Ext         string   `json:"path,omitempty"`
	Destination Egress   `json:"dst"`
	MatchType   Type     `json:"type"`
	cache       MatchCache
}

type ip4Range struct {
	start uint32
	end   uint32
}

type ip6Range struct {
	startHi uint64
	startLo uint64
	endHi   uint64
	endLo   uint64
}

type MatchCache struct {
	ExactMap     map[string]struct{}
	DomainMap    map[string]struct{}
	RegexpList   []*regexp.Regexp
	CIDRList     []netip.Prefix
	cidrCompiled bool
	cidr4        []ip4Range
	cidr6        []ip6Range
}

func (r *Route) GenerateCache() error {
	log.Printf("generating %s-route cache..", r.MatchType)

	// Build a local merged slice so r.Sources is never mutated.
	// This prevents duplicate entries if GenerateCache() is called more than once
	// (e.g., on config reload) when r.Ext points to a file.
	sources := r.Sources
	if r.Ext != "" {
		log.Printf("reading route-ext from file: %s", r.Ext)
		f, err := os.Open(r.Ext)
		if err != nil {
			return fmt.Errorf("read from path: %s failed: %w", r.Ext, err)
		}
		var fileSources []string
		b := bufio.NewScanner(f)
		for b.Scan() {
			line := strings.TrimSpace(b.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fileSources = append(fileSources, line)
		}
		if err := b.Err(); err != nil {
			utils.Close(f)
			return fmt.Errorf("read from path: %s scan failed: %w", r.Ext, err)
		}
		utils.Close(f)
		sources = append(sources, fileSources...)
	}
	// Reset caches before rebuilding to ensure idempotency.
	r.cache = MatchCache{}
	switch r.MatchType {
	case TypeDefault:
	case TypeExact:
		r.cache.ExactMap = make(map[string]struct{}, len(sources))
		for _, src := range sources {
			host := utils.NormalizeHost(src)
			if host == "" {
				continue
			}
			r.cache.ExactMap[host] = struct{}{}
		}
		log.Printf("exact-route count: %d", len(r.cache.ExactMap))
	case TypeDomain:
		// Domain rules use suffix matching: a rule "google.com" matches
		// "google.com" and any subdomain. Sources are normalized (case /
		// trailing dots) but NOT reduced to eTLD+1, so "api.google.com"
		// matches that host and its subdomains without matching "www.google.com".
		r.cache.DomainMap = make(map[string]struct{}, len(sources))
		for _, src := range sources {
			host := utils.NormalizeHost(src)
			if host == "" {
				continue
			}
			r.cache.DomainMap[host] = struct{}{}
		}
		log.Printf("domain-route count: %d", len(r.cache.DomainMap))
	case TypeCIDR:
		for _, cidr := range sources {
			cidr = strings.TrimSpace(cidr)
			if cidr == "" {
				continue
			}
			prefix, err := netip.ParsePrefix(cidr)
			if err != nil {
				return fmt.Errorf("cidr: %s parse failed: %w", cidr, err)
			}
			r.cache.CIDRList = append(r.cache.CIDRList, prefix)
		}
		r.cache.cidr4, r.cache.cidr6 = compileCIDRRanges(r.cache.CIDRList)
		r.cache.cidrCompiled = true
		log.Printf("cidr-route count: %d", len(r.cache.CIDRList))
	case TypeRegex:
		for _, rx := range sources {
			if strings.TrimSpace(rx) == "" {
				continue
			}
			regx, err := regexp.Compile(rx)
			if err != nil {
				return fmt.Errorf("regex: %s parse failed: %w", rx, err)
			}
			r.cache.RegexpList = append(r.cache.RegexpList, regx)
		}
		log.Printf("regex-route count: %d", len(r.cache.RegexpList))
	default:
		return fmt.Errorf("unknown route type: %s, cannot generate cache", r.MatchType)
	}
	return nil
}

func (r *Route) Match(dst string) bool {
	//log.Printf("route matching type: %s", r.MatchType)
	switch r.MatchType {
	case TypeDefault:
		return true
	case TypeExact:
		if r.cache.ExactMap != nil {
			if _, ok := r.cache.ExactMap[utils.NormalizeHost(dst)]; ok {
				return true
			}
		}
	case TypeRegex:
		if r.cache.RegexpList != nil {
			for _, regx := range r.cache.RegexpList {
				if regx.MatchString(dst) {
					return true
				}
			}
		}
	case TypeDomain:
		// Only match if dst is not an IP address
		host := utils.NormalizeHost(dst)
		if host == "" {
			return false
		}
		if _, err := netip.ParseAddr(host); err == nil {
			return false
		}
		for candidate := host; r.cache.DomainMap != nil; {
			if _, ok := r.cache.DomainMap[candidate]; ok {
				return true
			}
			dot := strings.IndexByte(candidate, '.')
			if dot < 0 {
				break
			}
			candidate = candidate[dot+1:]
		}
	case TypeCIDR:
		// Only match if dst is a valid IP address
		if addr, err := netip.ParseAddr(dst); err == nil {
			if r.cache.cidrCompiled {
				return matchCompiledCIDR(&r.cache, addr)
			}
			for _, cidr := range r.cache.CIDRList {
				if cidr.Contains(addr) {
					return true
				}
			}
		}
	}
	return false
}

func compileCIDRRanges(prefixes []netip.Prefix) ([]ip4Range, []ip6Range) {
	var ranges4 []ip4Range
	var ranges6 []ip6Range
	for _, prefix := range prefixes {
		if !prefix.IsValid() {
			continue
		}
		mp := prefix.Masked()
		bits := mp.Bits()
		if mp.Addr().Is4() {
			b := mp.Addr().As4()
			start := binary.BigEndian.Uint32(b[:])
			var end uint32
			if bits == 0 {
				start = 0
				end = math.MaxUint32
			} else {
				end = start | (uint32(1)<<(32-bits) - 1)
			}
			ranges4 = append(ranges4, ip4Range{start: start, end: end})
			continue
		}
		b := mp.Addr().As16()
		hi := binary.BigEndian.Uint64(b[:8])
		lo := binary.BigEndian.Uint64(b[8:])
		var endHi, endLo uint64
		switch {
		case bits == 0:
			hi, lo = 0, 0
			endHi, endLo = math.MaxUint64, math.MaxUint64
		case bits < 64:
			endHi = hi | (uint64(1)<<(64-bits) - 1)
			endLo = math.MaxUint64
		case bits == 64:
			endHi = hi
			endLo = math.MaxUint64
		default:
			endHi = hi
			endLo = lo | (uint64(1)<<(128-bits) - 1)
		}
		ranges6 = append(ranges6, ip6Range{startHi: hi, startLo: lo, endHi: endHi, endLo: endLo})
	}
	return mergeIP4Ranges(ranges4), mergeIP6Ranges(ranges6)
}

func mergeIP4Ranges(ranges []ip4Range) []ip4Range {
	if len(ranges) <= 1 {
		return ranges
	}
	slices.SortFunc(ranges, func(a, b ip4Range) int {
		if c := cmp.Compare(a.start, b.start); c != 0 {
			return c
		}
		return cmp.Compare(a.end, b.end)
	})
	out := ranges[:1]
	for _, cur := range ranges[1:] {
		last := &out[len(out)-1]
		if last.end == math.MaxUint32 || cur.start <= last.end+1 {
			if cur.end > last.end {
				last.end = cur.end
			}
			continue
		}
		out = append(out, cur)
	}
	return out
}

func cmp128(aHi, aLo, bHi, bLo uint64) int {
	if c := cmp.Compare(aHi, bHi); c != 0 {
		return c
	}
	return cmp.Compare(aLo, bLo)
}

func mergeIP6Ranges(ranges []ip6Range) []ip6Range {
	if len(ranges) <= 1 {
		return ranges
	}
	slices.SortFunc(ranges, func(a, b ip6Range) int {
		if c := cmp128(a.startHi, a.startLo, b.startHi, b.startLo); c != 0 {
			return c
		}
		return cmp128(a.endHi, a.endLo, b.endHi, b.endLo)
	})
	out := ranges[:1]
	for _, cur := range ranges[1:] {
		last := &out[len(out)-1]
		adjacent := false
		if last.endHi == math.MaxUint64 && last.endLo == math.MaxUint64 {
			adjacent = true
		} else {
			nextHi, nextLo := last.endHi, last.endLo+1
			if nextLo == 0 {
				nextHi++
			}
			adjacent = cmp128(cur.startHi, cur.startLo, nextHi, nextLo) <= 0
		}
		if adjacent {
			if cmp128(cur.endHi, cur.endLo, last.endHi, last.endLo) > 0 {
				last.endHi, last.endLo = cur.endHi, cur.endLo
			}
			continue
		}
		out = append(out, cur)
	}
	return out
}

func matchCompiledCIDR(cache *MatchCache, addr netip.Addr) bool {
	if addr.Zone() != "" {
		return false
	}
	if addr.Is4() {
		ranges := cache.cidr4
		if len(ranges) == 0 {
			return false
		}
		b := addr.As4()
		v := binary.BigEndian.Uint32(b[:])
		lo, hi := 0, len(ranges)
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			if ranges[mid].start <= v {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		return lo > 0 && v <= ranges[lo-1].end
	}
	ranges := cache.cidr6
	if len(ranges) == 0 {
		return false
	}
	b := addr.As16()
	vHi := binary.BigEndian.Uint64(b[:8])
	vLo := binary.BigEndian.Uint64(b[8:])
	lo, hi := 0, len(ranges)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if cmp128(ranges[mid].startHi, ranges[mid].startLo, vHi, vLo) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo > 0 && cmp128(vHi, vLo, ranges[lo-1].endHi, ranges[lo-1].endLo) <= 0
}
