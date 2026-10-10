package router

import (
	"fmt"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

type Routes []*Route

func (r Routes) GenerateCache() error {
	for i, route := range r {
		if route == nil {
			return fmt.Errorf("route %d is nil", i)
		}
		if err := route.GenerateCache(); err != nil {
			return fmt.Errorf("route %d: %w", i, err)
		}
	}
	return nil
}

func (r Routes) GetRoute(dst string) (transport.Transport, error) {
	// dst is expected to already be a normalizeRouteKey result when called from
	// the package GetRoute entrypoint; normalize again so direct callers are safe.
	key := normalizeRouteKey(dst)
	egress, err := r.findEgress(key, key, func(route *Route) bool { return route.Match(key) })
	if err != nil {
		return nil, err
	}
	return egress.GetTransport()
}

// findEgress returns the first route for which match reports true and caches
// that egress under cacheKey. errKey is the destination named in the error
// when nothing matches. The caller holds routesMu for reading.
func (r Routes) findEgress(cacheKey, errKey string, match func(*Route) bool) (Egress, error) {
	for i, route := range r {
		if route == nil {
			return "", fmt.Errorf("route %d is nil", i)
		}
		if match(route) {
			table.Set(cacheKey, route.Destination)
			return route.Destination, nil
		}
	}
	return "", fmt.Errorf("route not found: %s -> nil", errKey)
}

// getObserved matches name rules against nameKey and CIDR rules against ipKey.
// The caller holds routesMu for reading.
func (r Routes) getObserved(cacheKey, ipKey, nameKey string) (transport.Transport, error) {
	egress, err := r.findEgress(cacheKey, nameKey, func(route *Route) bool {
		return matchObserved(route, ipKey, nameKey)
	})
	if err != nil {
		return nil, err
	}
	return egress.GetTransport()
}

// decidedIPEgress is the egress an IP already selects when no earlier name
// rule can outrank that choice. The second result is false when the list ends
// without a decision, and when an exact, domain, or regex rule does not match
// the IP: the recovered name might still match that rule.
func decidedIPEgress(ip string) (Egress, bool) {
	key := normalizeRouteKey(ip)
	routesMu.RLock()
	defer routesMu.RUnlock()
	for _, route := range routesCache {
		if route == nil {
			continue
		}
		switch route.MatchType {
		case TypeCIDR:
			if route.Match(key) {
				return route.Destination, true
			}
		case TypeDefault:
			return route.Destination, true
		default:
			if route.Match(key) {
				return route.Destination, true
			}
			return "", false
		}
	}
	return "", false
}

// IPBlockDecisive reports whether ip is blocked by a rule a recovered name
// cannot outrank. An earlier name rule might still match that name and win,
// so this is false until the flight has been read.
func IPBlockDecisive(ip string) bool {
	egress, ok := decidedIPEgress(ip)
	return ok && egress == EgressBlock
}

// IPNeedsSniff reports whether a front end must read the first client flight
// before dialing or refusing ip. An undecided IP might match an earlier name
// rule once the name is known. Proxy and forward dial that name. Direct,
// blackhole, and block are already settled, so the connection is dialed or
// refused immediately.
func IPNeedsSniff(ip string) bool {
	egress, ok := decidedIPEgress(ip)
	if !ok {
		return true
	}
	return egress == EgressProxy || egress == EgressForward
}

// AdmitProxyFirst reports whether an IP connection should be authenticated to
// the tunnel before the front end tells the client that connection is open.
// A recovered name may still change which host is dialed. It cannot select
// direct, blackhole, or forward from this table; those wait for the flight,
// because that flight can choose an egress that never touches the tunnel.
func AdmitProxyFirst(ip string) bool {
	key := normalizeRouteKey(ip)
	routesMu.RLock()
	defer routesMu.RUnlock()
	var proxyPossible, skipsTunnel bool
	for _, route := range routesCache {
		if route == nil {
			continue
		}
		switch route.MatchType {
		case TypeCIDR:
			if !route.Match(key) {
				continue
			}
			return admitProxyDecision(route.Destination, proxyPossible, skipsTunnel)
		case TypeDefault:
			return admitProxyDecision(route.Destination, proxyPossible, skipsTunnel)
		default:
			if route.Match(key) {
				return admitProxyDecision(route.Destination, proxyPossible, skipsTunnel)
			}
			switch route.Destination {
			case EgressProxy:
				proxyPossible = true
			case EgressBlock:
				// Block refuses the name. It does not dial locally.
			default:
				skipsTunnel = true
			}
		}
	}
	if skipsTunnel {
		return false
	}
	return proxyPossible
}

func admitProxyDecision(dest Egress, proxyPossible, skipsTunnel bool) bool {
	if skipsTunnel {
		return false
	}
	switch dest {
	case EgressProxy:
		return true
	case EgressBlock:
		return proxyPossible
	default:
		return false
	}
}

func matchObserved(route *Route, ipKey, nameKey string) bool {
	switch route.MatchType {
	case TypeCIDR:
		return route.Match(ipKey)
	case TypeDefault:
		return true
	default:
		return route.Match(nameKey) || route.Match(ipKey)
	}
}
