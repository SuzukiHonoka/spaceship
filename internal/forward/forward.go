package forward

import (
	"golang.org/x/net/proxy"
	"net"
	"net/url"
)

type Forward struct {
	Dialer func(network, addr string) (c net.Conn, err error)
}

func (f *Forward) Dial(network, addr string) (c net.Conn, err error) {
	return f.Dialer(network, addr)
}

func NewForward(fp string) (*Forward, error) {
	u, err := url.Parse(fp)
	if err != nil {
		return nil, err
	}
	// switch proto
	d, err := proxy.FromURL(u, nil)
	if err != nil {
		return nil, err
	}
	return &Forward{
		Dialer: d.Dial,
	}, nil
	// todo: RegisterDialerType for other scheme
}
