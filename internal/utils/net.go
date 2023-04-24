package utils

import (
	"golang.org/x/net/proxy"
	"net"
	"net/url"
	"strconv"
)

// SplitHostPort uses net.SplitHostPort but converts port to uint16 format
func SplitHostPort(s string) (string, uint16, error) {
	host, sport, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(sport)
	if err != nil {
		return "", 0, err
	}
	return host, uint16(port), nil
}

func LoadProxy(p string) (proxy.Dialer, error) {
	u, err := url.Parse(p)
	if err != nil {
		return nil, err
	}
	d, err := proxy.FromURL(u, nil)
	if err != nil {
		return nil, err
	}
	return d, nil
	// todo: RegisterDialerType for other scheme
}
