//go:build android
// +build android

package main

import "spaceship/internal/dns"

// android may not resolve dns correctly through DefaultResolver
func init() {
	// use dns server of dnspod by default
	d := dns.DNS{
		Type:   dns.TypeCommon,
		Server: "119.29.29.29",
	}
	d.SetDefault()
}
