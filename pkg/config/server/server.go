package server

type Server struct {
	Listen string  `json:"listen"`
	SSL    *SSL    `json:"ssl,omitempty"`
	Path   string  `json:"path,omitempty"` // grpc service name
	Users  *Users  `json:"users"`
	Blocks *Blocks `json:"blocks,omitempty"`
	Buffer uint16  `json:"buffer,omitempty"` // transport buffer size in KB, up to 65535
	IPv6   bool    `json:"ipv6,omitempty"`   // enable ipv6 in tcp network, disable by default
	Proxy  string  `json:"proxy,omitempty"`  // extra proxy for dialing
}
