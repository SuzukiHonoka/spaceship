//go:build android
// +build android

package main

import (
	"github.com/SuzukiHonoka/spaceship/pkg/dns"
)

// android may not resolve dns correctly through DefaultResolver
func init() {
	// use google dns server by default
	d := &dns.DNS{
		Type:   dns.TypeCommon,
		Server: "8.8.8.8",
	}
	d.SetDefault()
}
