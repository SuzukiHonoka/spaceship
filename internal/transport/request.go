package transport

type Request struct {
	Host string
	Port uint16
}

func NewRequest(host string, port uint16) *Request {
	return &Request{
		Host: host,
		Port: port,
	}
}
