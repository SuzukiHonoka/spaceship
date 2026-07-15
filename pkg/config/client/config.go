package client

import (
	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
)

type Client struct {
	ServerAddr      string        `json:"server_addr"`
	Host            string        `json:"host,omitempty"`
	UUID            string        `json:"uuid"` // user id
	ListenSocks     string        `json:"listen_socks,omitempty"`
	ListenSocksUnix string        `json:"listen_socks_unix,omitempty"`
	ListenHttp      string        `json:"listen_http,omitempty"`
	ListenDns       string        `json:"listen_dns,omitempty"`
	BasicAuth       []string      `json:"basic_auth,omitempty"` // user:password
	Mux             uint8         `json:"mux"`                  // 0 -> disabled, n (>0) -> limited connection
	EnableTLS       bool          `json:"tls"`
	Routes          router.Routes `json:"route,omitempty"`
	// IdleTimeout is gRPC connection idle timeout in seconds.
	// For decoded JSON, omission keeps the transport default and explicit 0
	// disables the timeout. The int type is retained for source compatibility.
	IdleTimeout  int  `json:"idle_timeout,omitempty"`
	BlockIPv6DNS bool `json:"block_ipv6_dns,omitempty"` // block IPv6 DNS queries (AAAA records)
}
