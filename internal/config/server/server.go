package server

type Server struct {
	Listen string  `json:"listen"`
	SSL    *SSL    `json:"ssl,omitempty"`  // since we use nginx to reserve proxy, this option is only useful if you connect it directly
	Path   string  `json:"path,omitempty"` // grpc service name
	Users  *Users  `json:"users"`
	Blocks *Blocks `json:"blocks,omitempty"`
	Buffer uint16  `json:"buffer,omitempty"` // transport buffer size in KB
	IPv6   bool    `json:"ipv6,omitempty"`   // disable(default) or enable ipv6 network in tcp
}
