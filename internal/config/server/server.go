package server

type Server struct {
	Listen string  `json:"listen"`
	SSL    *SSL    `json:"ssl,omitempty"`  // since we use nginx to reserve proxy, this option is only useful if you connect it directly
	Path   string  `json:"path,omitempty"` // grpc service name
	Users  []User  `json:"users"`
	Blocks []Block `json:"blocks,omitempty"`
}
