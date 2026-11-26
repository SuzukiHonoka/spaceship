package tun

import "io"

// routeController owns host-level addresses, policy routes, default-interface
// monitoring, and (on Linux) optional nftables auto_redirect state installed
// when RouteMode == "auto". Close reverses all installed host state.
type routeController interface {
	io.Closer
}
