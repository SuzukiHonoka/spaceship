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
	IdleTimeout     int           `json:"idle_timeout,omitempty"` // seconds, 0 -> disabled
}
