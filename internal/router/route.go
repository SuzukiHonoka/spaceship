package router

import (
	"bufio"
	"fmt"
	"log"
	"net/netip"
	"os"
	"regexp"
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

type MatchCache struct {
	ExactMap   map[string]struct{}
	DomainMap  map[string]struct{}
	RegexpList []*regexp.Regexp
	CIDRList   []netip.Prefix
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
			if r.cache.CIDRList != nil {
				for _, cidr := range r.cache.CIDRList {
					if cidr.Contains(addr) {
						return true
					}
				}
			}
		}
	}
	return false
}
