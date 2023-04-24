package router

import (
	"bufio"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"net"
	"os"
	"regexp"
	"strings"
)

var (
	RouteServerDefault = &Route{
		Destination: EgressDirect,
		MatchType:   TypeDefault,
	}
	RouteDefault = &Route{
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

type Route struct {
	Sources     []string `json:"src"`
	Ext         string   `json:"path,omitempty"`
	Destination Egress   `json:"dst"`
	MatchType   Type     `json:"type"`
	cache       MatchCache
}

type MatchCache struct {
	RegexS []*regexp.Regexp
	CIDRs  []*net.IPNet
}

func (r *Route) GenerateCache() error {
	if r.Ext != "" {
		f, err := os.Open(r.Ext)
		if err != nil {
			return fmt.Errorf("read from path: %s failed: %w", r.Ext, err)
		}
		b := bufio.NewScanner(f)
		for b.Scan() {
			r.Sources = append(r.Sources, b.Text())
		}
		utils.ForceClose(f)
	}
	switch r.MatchType {
	case TypeDefault:
	case TypeExact:
	case TypeDomains:
	case TypeCIDR:
		for _, cidr := range r.Sources {
			_, IPNet, err := net.ParseCIDR(cidr)
			if err != nil {
				return fmt.Errorf("cidr: %s parse failed: %w", cidr, err)
			}
			r.cache.CIDRs = append(r.cache.CIDRs, IPNet)
		}
	case TypeRegex:
		for _, rx := range r.Sources {
			regx, err := regexp.Compile(rx)
			if err != nil {
				return fmt.Errorf("regex: %s parse failed: %w", rx, err)
			}
			r.cache.RegexS = append(r.cache.RegexS, regx)
		}
	default:
		return fmt.Errorf("unknown route type: %s, cannot generate cache", r.MatchType)
	}
	return nil
}

func (r *Route) Match(dst string) bool {
	switch r.MatchType {
	case TypeDefault:
		return true
	case TypeExact:
		for _, src := range r.Sources {
			if src == dst {
				return true
			}
		}
	case TypeDomains:
		for _, domain := range r.Sources {
			if strings.Contains(dst, domain) {
				return true
			}
		}
	case TypeRegex:
		if r.cache.RegexS != nil {
			for _, regx := range r.cache.RegexS {
				if regx.MatchString(dst) {
					return true
				}
			}
		}
	case TypeCIDR:
		if r.cache.CIDRs != nil {
			if ip := net.ParseIP(dst); ip != nil {
				for _, cidr := range r.cache.CIDRs {
					if cidr.Contains(ip) {
						return true
					}
				}
			}
		}
	}
	return false
}
