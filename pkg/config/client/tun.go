package client

// TUN configures the Linux userspace TCP/IP frontend. The section is optional;
// when omitted no TUN device or route-related socket marking is enabled.
type TUN struct {
	// Name is the Linux interface name. Empty selects "spaceship0" for a
	// process-created device; an external descriptor always uses its attached
	// interface name.
	Name string `json:"name,omitempty"`
	// MTU is the IP MTU exposed to gVisor. Zero selects 1500.
	MTU int `json:"mtu,omitempty"`
	// FileDescriptor optionally supplies an already-open IFF_TUN|IFF_NO_PI
	// descriptor. Spaceship duplicates it and owns only the duplicate, but the
	// shared file status is made nonblocking for gVisor.
	FileDescriptor *int `json:"file_descriptor,omitempty"`
	// RouteMode is currently "manual": the operator owns policy routes while
	// Spaceship creates and brings up the device.
	RouteMode string `json:"route_mode,omitempty"`
	// BypassMark is applied with SO_MARK to Spaceship-created outbound sockets.
	// Zero selects a built-in non-zero mark.
	BypassMark uint32 `json:"bypass_mark,omitempty"`
	// MaxConnections bounds active gVisor TCP and DNS-UDP endpoints plus their
	// proxy goroutines.
	MaxConnections int `json:"max_connections,omitempty"`
	// MaxPendingConnections bounds TCP handshakes being completed by gVisor.
	MaxPendingConnections int `json:"max_pending_connections,omitempty"`
	// DNSHijack intercepts classic DNS on TCP and UDP destination port 53.
	DNSHijack *DNSHijack `json:"dns_hijack,omitempty"`
}

type DNSHijack struct {
	// Enabled intercepts DNS on TCP and UDP destination port 53.
	//
	// It does not enable a general UDP tunnel. The TUN frontend terminates TCP
	// only, so non-DNS UDP is refused with an ICMP unreachable whether this is
	// set or not. What it changes is port 53: with it disabled, DNS entering
	// the TUN is refused like any other UDP, leaving the interface without name
	// resolution unless the capture rule keeps port 53 out of the TUN. Config
	// application logs a warning for that combination.
	Enabled bool `json:"enabled,omitempty"`
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
