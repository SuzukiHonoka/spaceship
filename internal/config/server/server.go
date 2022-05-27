package server

type Server struct {
	Listen string  `json:"listen"`
	SSL    *SSL    `json:"ssl,omitempty"` // since we use nginx to reserve proxy, this option is only useful if you connect it directly
	Mux    int8    `json:"mux"`           // -1 -> disabled, 0 -> unlimited, n (>0) -> limited connection
	Users  []User  `json:"users"`
	Blocks []Block `json:"blocks,omitempty"`
}
