package router

import (
	"bufio"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"net"
	"os"
	"regexp"
	"strings"
)

type Route struct {
	Sources     []string `json:"src"`
	Path        string   `json:"path,omitempty"`
	Destination Egress   `json:"dst"`
	MatchType   Type     `json:"type"`
	cache       MatchCache
}

type MatchCache struct {
	RegexS []*regexp.Regexp
	CIDRs  []*net.IPNet
}

func (r *Route) GenerateCache() error {
	if r.Path != "" {
		f, err := os.Open(r.Path)
		if err != nil {
			return fmt.Errorf("read from path: %s failed: %w", r.Path, err)
		}
		defer transport.ForceClose(f)
		b := bufio.NewScanner(f)
		for b.Scan() {
			r.Sources = append(r.Sources, b.Text())
		}
	}
	switch r.MatchType {
	case TypeDefault:
	case TypeExact:
	case TypeDomains:
		for i, s := range r.Sources {
			var sb strings.Builder
			sb.WriteRune('.')
			sb.WriteString(s)
			r.Sources[i] = sb.String()
		}
	case TypeCIDR:
		for _, cidr := range r.Sources {
			_, IPNet, err := net.ParseCIDR(cidr)
			if err != nil {
				return fmt.Errorf("CIDR: %s parse failed: %w", cidr, err)
			}
			r.cache.CIDRs = append(r.cache.CIDRs, IPNet)
		}
	case TypeRegex:
		for _, rx := range r.Sources {
			regx, err := regexp.Compile(rx)
			if err != nil {
				return fmt.Errorf("REGEX: %s parse failed: %w", r.Sources, err)
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
		return false
	case TypeCIDR:
		ip := net.ParseIP(dst)
		if ip == nil {
			return false
		}
		if r.cache.CIDRs != nil {
			for _, cidr := range r.cache.CIDRs {
				if cidr.Contains(ip) {
					return true
				}
			}
		}
	}
	return false
}
