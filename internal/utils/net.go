package utils

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/proxy"
	"golang.org/x/net/publicsuffix"
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

// NormalizeHost lowercases a hostname and strips a trailing DNS root dot.
// IP literals are returned unchanged aside from case (IPs are case-insensitive
// for hex digits in IPv6). Empty input stays empty.
func NormalizeHost(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// Strip one or more trailing dots used by absolute FQDNs (e.g. "example.com.").
	s = strings.TrimRight(s, ".")
	return strings.ToLower(s)
}

// ExtractDomain returns the registrable domain (eTLD+1) for a hostname.
// The input is normalized first so trailing dots and mixed case do not break
// publicsuffix lookup.
func ExtractDomain(s string) string {
	s = NormalizeHost(s)
	if s == "" {
		return ""
	}
	eTLD, icann := publicsuffix.PublicSuffix(s)
	if icann {
		// Guard against malformed input where the suffix is the whole string
		// or longer (should not happen after NormalizeHost, but be safe).
		if len(s) <= len(eTLD) {
			return s
		}
		// s is "label(.label)*.eTLD" — drop the public suffix and its leading dot.
		domain := s[:len(s)-len(eTLD)-1]
		if index := strings.LastIndexByte(domain, '.'); index != -1 {
			return s[index+1:]
		}
		return s
	}
	return eTLD
}
