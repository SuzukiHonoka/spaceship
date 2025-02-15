package router

import (
	"bufio"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"net"
	"os"
	"regexp"
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
	CIDRList   []*net.IPNet
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
		utils.Close(f)
	}
	switch r.MatchType {
	case TypeDefault:
	case TypeExact:
		r.cache.ExactMap = make(map[string]struct{}, len(r.Sources))
		for _, src := range r.Sources {
			r.cache.ExactMap[src] = struct{}{}
		}
	case TypeDomain:
		r.cache.DomainMap = make(map[string]struct{}, len(r.Sources))
		for _, src := range r.Sources {
			r.cache.DomainMap[src] = struct{}{}
		}
	case TypeCIDR:
		for _, cidr := range r.Sources {
			_, IPNet, err := net.ParseCIDR(cidr)
			if err != nil {
				return fmt.Errorf("cidr: %s parse failed: %w", cidr, err)
			}
			r.cache.CIDRList = append(r.cache.CIDRList, IPNet)
		}
	case TypeRegex:
		for _, rx := range r.Sources {
			regx, err := regexp.Compile(rx)
			if err != nil {
				return fmt.Errorf("regex: %s parse failed: %w", rx, err)
			}
			r.cache.RegexpList = append(r.cache.RegexpList, regx)
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
		if r.cache.ExactMap != nil {
			if _, ok := r.cache.ExactMap[dst]; ok {
				return true
			}
		}
	case TypeDomain:
		dst = utils.ExtractDomain(dst)
		if r.cache.DomainMap != nil {
			if _, ok := r.cache.DomainMap[dst]; ok {
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
	case TypeCIDR:
		if r.cache.CIDRList != nil {
			if ip := net.ParseIP(dst); ip != nil {
				for _, cidr := range r.cache.CIDRList {
					if cidr.Contains(ip) {
						return true
					}
				}
			}
		}
	}
	return false
}
