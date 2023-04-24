package client

import (
	"github.com/SuzukiHonoka/spaceship/internal/router"
)

type Client struct {
	ServerAddr  string        `json:"server_addr"`
	Host        string        `json:"host,omitempty"`
	UUID        string        `json:"uuid"` // user id
	ListenSocks string        `json:"listen_socks,omitempty"`
	ListenHttp  string        `json:"listen_http,omitempty"`
	Mux         uint8         `json:"mux"` // 0 -> disabled, n (>0) -> limited connection
	EnableTLS   bool          `json:"tls"`
	Routes      router.Routes `json:"route,omitempty"`
}
