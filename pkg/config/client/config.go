package client

import (
	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
)

type Client struct {
	ServerAddr      string    `json:"server_addr"`
	Host            string    `json:"host,omitempty"`
	UUID            string    `json:"uuid"` // user id
	ListenSocks     string    `json:"listen_socks,omitempty"`
	ListenSocksUnix string    `json:"listen_socks_unix,omitempty"`
	ListenHttp      string    `json:"listen_http,omitempty"`
	ListenRedirect  string    `json:"listen_redirect,omitempty"` // Linux TCP transparent REDIRECT listener
	Redirect        *Redirect `json:"redirect,omitempty"`
	TUN             *TUN      `json:"tun,omitempty"`
	ListenDns       string    `json:"listen_dns,omitempty"`
	BasicAuth       []string  `json:"basic_auth,omitempty"` // user:password
	// Mux controls the warm minimum of persistent gRPC connections. A non-zero
	// pool grows on demand up to the uint8 ceiling when wrappers reach the native
	// server stream limit. Zero keeps legacy unpooled behavior unless TUN is
	// enabled, in which case Apply selects a safe persistent minimum.
	Mux       uint8         `json:"mux"`
	EnableTLS bool          `json:"tls"`
	Routes    router.Routes `json:"route,omitempty"`
	// IdleTimeout is gRPC connection idle timeout in seconds.
	// For decoded JSON, omission keeps the transport default and explicit 0
	// disables the timeout. The int type is retained for source compatibility.
	IdleTimeout  int  `json:"idle_timeout,omitempty"`
	BlockIPv6DNS bool `json:"block_ipv6_dns,omitempty"` // block IPv6 DNS queries (AAAA records)
	// UDP tunes the SOCKS5 UDP ASSOCIATE relay. Omit the whole section to keep
	// UDP enabled with built-in defaults.
	UDP *UDP `json:"udp,omitempty"`
}

// Redirect configures the Linux TCP transparent redirect listener. Zero-valued
// limits select safe built-in defaults.
type Redirect struct {
	// MaxConnections bounds accepted TCP sessions and proxy goroutines.
	MaxConnections int `json:"max_connections,omitempty"`
	// BypassMark is applied with SO_MARK to Spaceship's own outbound sockets so
	// an OUTPUT-chain REDIRECT rule can exempt them with `-m mark`. Unlike
	// `-m owner --uid-owner`, that exemption still works when Spaceship runs as
	// root. Without it, an unexempted OUTPUT rule makes the listener
	// recursively capture its own egress.
	//
	// Omitting the field enables the default mark, because that recursion is
	// the most damaging way to misconfigure this listener. Setting it to 0
	// disables marking explicitly, which a LAN-only PREROUTING deployment may
	// prefer since nothing there captures locally-originated traffic. When the
	// mark is only defaulted and the process lacks the capability to apply it,
	// startup warns and continues unmarked rather than failing; an explicit
	// value is treated as a requirement and fails instead.
	//
	// When TUN is also enabled its mark is inherited; setting a different value
	// here is rejected because a process has exactly one outbound mark.
	BypassMark *uint32 `json:"bypass_mark,omitempty"`
}

// UDP configures the SOCKS5 UDP ASSOCIATE relay. Every numeric field is
// optional; zero selects the built-in default.
//
// The limits exist because each association owns a client-facing socket and a
// worker pool, and each NAT entry owns an outbound socket plus a reverse-relay
// goroutine holding a ~64KB buffer for the flow's lifetime. MaxNATEntriesTotal
// is therefore the dominant memory term — roughly 64KB per entry — so raise it
// deliberately rather than by reflex.
type UDP struct {
	// Disable makes UDP ASSOCIATE report "command not supported", so clients
	// fall back to TCP. Use this to turn the feature off without downgrading.
	Disable bool `json:"disable,omitempty"`
	// MaxAssociations bounds concurrent associations process-wide;
	// MaxAssociationsPerClient bounds them for a single client IP.
	MaxAssociations          int `json:"max_associations,omitempty"`
	MaxAssociationsPerClient int `json:"max_associations_per_client,omitempty"`
	// MaxNATEntries bounds outbound sockets a single association may open.
	// MaxNATEntriesTotal and MaxNATEntriesPerClient bound them process-wide and
	// for a single client IP.
	MaxNATEntries          int `json:"max_nat_entries,omitempty"`
	MaxNATEntriesTotal     int `json:"max_nat_entries_total,omitempty"`
	MaxNATEntriesPerClient int `json:"max_nat_entries_per_client,omitempty"`
}
