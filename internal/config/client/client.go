package client

type Client struct {
	ServerAddr  string `json:"server_addr"`
	Host        string `json:"host,omitempty"`
	UUID        string `json:"uuid"` // user id
	ListenSocks string `json:"listen_socks,omitempty"`
	ListenHttp  string `json:"listen_http,omitempty"`
	Mux         uint8  `json:"mux"` // 0 -> disabled, n (>0) -> limited connection
	TLS         bool   `json:"tls"`
}
