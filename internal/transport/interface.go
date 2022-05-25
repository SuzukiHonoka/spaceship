package transport

import "io"

type Transport interface {
	Proxy(localAddr chan string, dst io.Writer, src io.Reader, uuid, fqdn string, port int)
}
