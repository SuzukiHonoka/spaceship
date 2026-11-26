package client

// TUN configures the Linux userspace TCP/IP frontend. The section is optional;
// when omitted no TUN device or route-related socket marking is enabled.
type TUN struct {
	// Name is the interface name. Empty selects "spaceship0" for a device
	// Spaceship creates. A Linux external descriptor must match the name
	// attached to the fd, or be empty to accept that name. On Android the name
	// is not queried; empty selects the placeholder "tun".
	Name string `json:"name,omitempty"`
	// MTU is the IP MTU exposed to gVisor. Zero selects 1500.
	MTU int `json:"mtu,omitempty"`
	// FileDescriptor optionally supplies an already-open descriptor. Spaceship
	// duplicates it and owns only the duplicate. The shared file status is
	// made nonblocking for gVisor.
	//
	// On Linux the descriptor must be a single-queue IFF_TUN|IFF_NO_PI device
	// without a virtio-net header. Spaceship checks that with TUNGETIFF and
	// uses the interface MTU. On Android it is the raw IP fd from VpnService:
	// Spaceship does not create /dev/net/tun and does not issue TUNGETIFF,
	// SIOCGIFMTU, or SIOCSIFFLAGS. The configured MTU is used as-is, and an
	// empty name stays a placeholder because the interface is often invisible
	// to the app.
	FileDescriptor *int `json:"file_descriptor,omitempty"`
	// RouteMode selects how host routes are managed: "manual" (operator owns
	// routes; default) or "auto" (Spaceship configures interface addresses,
	// routes, and DNS automatically on Linux, macOS, and Windows). Android and
	// iOS always use an external descriptor and manual routing.
	RouteMode string `json:"route_mode,omitempty"`
	// Address lists IPv4/IPv6 interface CIDRs assigned to the TUN device when
	// route_mode is "auto" (for example ["198.18.0.1/30", "fdfe:dcba:9876::1/126"]).
	Address []string `json:"address,omitempty"`
	// RouteExcludeAddress lists CIDRs excluded from TUN capture when route_mode
	// is "auto" (for example private LAN subnets).
	RouteExcludeAddress []string `json:"route_exclude_address,omitempty"`
	// OutboundInterface optionally overrides the physical egress interface used
	// for loop prevention on macOS (IP_BOUND_IF), Windows (IP_UNICAST_IF), and
	// Linux (SO_BINDTODEVICE). Empty auto-detects the default physical interface
	// on macOS and Windows.
	OutboundInterface string `json:"outbound_interface,omitempty"`
	// AutoRedirect enables Linux nftables transparent TCP redirection into
	// Spaceship's native redirect server while routing UDP to TUN. Requires
	// route_mode "auto" and is supported only on Linux.
	AutoRedirect bool `json:"auto_redirect,omitempty"`
	// AutoRedirectIncludeForward also captures routed/LAN traffic in nftables
	// PREROUTING chains when auto_redirect is enabled (for Linux routers).
	AutoRedirectIncludeForward bool `json:"auto_redirect_include_forward,omitempty"`
	// WintunPath optionally specifies the path to wintun.dll on Windows. When
	// empty, Spaceship looks next to the executable and in standard DLL search
	// paths.
	WintunPath string `json:"wintun_path,omitempty"`
	// BypassMark is applied with SO_MARK to Spaceship-created outbound sockets.
	//
	// On Linux, omitting the field or setting it to 0 selects the built-in
	// non-zero mark. That mark is required for a TUN Spaceship created and for
	// a Linux external fd, so policy routing does not recapture Spaceship's
	// own egress.
	//
	// On Android, omitting the field or setting it to 0 installs no mark.
	// VpnService.Builder.addDisallowedApplication, or a socket protector
	// passed to api.SetSocketProtector, keeps Spaceship's own sockets
	// out of the VPN. An explicit non-zero value is installed and startup
	// fails if it cannot be set.
	BypassMark *uint32 `json:"bypass_mark,omitempty"`
	// MaxConnections bounds active gVisor TCP and UDP endpoints plus their
	// proxy goroutines. General UDP is further capped by max_udp_sessions, and
	// DNS-over-UDP sessions also count against dns_hijack.max_in_flight.
	MaxConnections int `json:"max_connections,omitempty"`
	// MaxPendingConnections bounds TCP handshakes being completed by gVisor.
	MaxPendingConnections int `json:"max_pending_connections,omitempty"`
	// MaxUDPSessions caps general UDP sessions inside max_connections, so TCP
	// always keeps the remainder. At the cap, a new UDP flow replaces the
	// least recently active session. Zero selects a quarter of
	// max_connections, and at least one.
	MaxUDPSessions int `json:"max_udp_sessions,omitempty"`
	// UDPIdleTimeoutSeconds closes an idle UDP NAT session after this many
	// seconds without traffic in either direction. Zero selects 60s.
	UDPIdleTimeoutSeconds int `json:"udp_idle_timeout_seconds,omitempty"`
	// UDPMapping selects RFC 4787 UDP NAT mapping behavior: "eim" (default,
	// Endpoint-Independent Mapping / Full Cone), "ad" (Address-Dependent), or
	// "apd" (Address-and-Port-Dependent / 4-tuple).
	UDPMapping string `json:"udp_mapping,omitempty"`
	// UDPFiltering selects RFC 4787 UDP NAT filtering behavior: "eif" (default,
	// Endpoint-Independent Filtering / Full Cone), "ad" (Address-Dependent), or
	// "apd" (Address-and-Port-Dependent).
	UDPFiltering string `json:"udp_filtering,omitempty"`
	// DNSHijack intercepts classic DNS on TCP and UDP destination port 53
	// (and any configured hijack addresses).
	DNSHijack *DNSHijack `json:"dns_hijack,omitempty"`
}

type DNSHijack struct {
	// Enabled intercepts DNS on TCP and UDP destination port 53 and answers it
	// through the authenticated Spaceship DNS RPC.
	//
	// It does not gate other UDP. QUIC, WebRTC, and the rest are forwarded as
	// UDP sessions whether this is set or not. With it disabled, port 53 is
	// forwarded as an ordinary UDP flow instead of the Spaceship resolver, and
	// config application logs a warning for that combination.
	Enabled bool `json:"enabled,omitempty"`
	// Addresses optionally lists virtual DNS IP addresses (for example the TUN
	// peer IP "198.18.0.2") whose traffic on port 53 is hijacked in addition to
	// destination port 53.
	Addresses []string `json:"addresses,omitempty"`
	// QueryTimeoutSeconds bounds each authenticated DNS exchange RPC.
	QueryTimeoutSeconds int `json:"query_timeout_seconds,omitempty"`
	// TCPIdleTimeoutSeconds bounds idle DNS-over-TCP sessions.
	TCPIdleTimeoutSeconds int `json:"tcp_idle_timeout_seconds,omitempty"`
	// MaxInFlight bounds DNS RPCs across TCP and UDP clients.
	// It also bounds active UDP DNS sessions to keep their receive buffers
	// proportional to the configured DNS capacity. Zero selects 256; the
	// accepted maximum is 1024 because each active UDP session owns a buffer of
	// up to 64 KiB.
	MaxInFlight int `json:"max_in_flight,omitempty"`
	// MaxInFlightPerConnection bounds concurrent DNS RPCs owned by one
	// DNS-over-TCP connection, so a pipelining client cannot hold every slot in
	// MaxInFlight and leave every other client behind the TUN with SERVFAIL.
	// Zero reserves a quarter of MaxInFlight for other connections, which is
	// far more headroom than a stub resolver pipelines. It may not exceed
	// MaxInFlight. UDP is unaffected: each UDP flow answers one query at a time.
	MaxInFlightPerConnection int `json:"max_in_flight_per_connection,omitempty"`
}
