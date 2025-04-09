package utils

import (
	"golang.org/x/net/proxy"
	"golang.org/x/net/publicsuffix"
	"net"
	"net/url"
	"strconv"
	"strings"
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
	// Check if port is in valid range for uint16
	if port < 0 || port > 65535 {
		return "", 0, &net.AddrError{Err: "invalid port", Addr: sport}
	}
	return host, uint16(port), nil
}

func LoadProxy(p string) (proxy.Dialer, error) {
	u, err := url.Parse(p)
	if err != nil {
		return nil, err
	}
	return proxy.FromURL(u, nil)
}

func ExtractDomain(s string) string {
	eTLD, icann := publicsuffix.PublicSuffix(s)
	if icann {
		domain := s[:len(s)-len(eTLD)-1]
		if index := strings.LastIndexByte(domain, '.'); index != -1 {
			return s[index+1:]
		}
		return s
	}
	return eTLD
}
