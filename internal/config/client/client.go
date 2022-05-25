package client

type Client struct {
	ServerAddr  string `json:"server_addr"`
	UUID        string `json:"uuid"` // user id
	ListenSocks string `json:"listen_socks,omitempty"`
	ListenHttp  string `json:"listen_http,omitempty"`
	Mux         int8   `json:"mux,omitempty"`
	TLS         bool   `json:"tls"`
}
