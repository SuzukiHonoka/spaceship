//go:build android

package main

import (
	"github.com/SuzukiHonoka/spaceship/v2/pkg/dns"
)

// defaultDnsServer is the default dns server for android, use the dns server from Google for now.
const defaultDnsServer = "8.8.8.8"

// android may not resolve dns correctly through DefaultResolver, so we set a default dns server here.
func init() {
	d := &dns.DNS{
		Type:   dns.TypeCommon,
		Server: defaultDnsServer,
	}
	d.SetDefault()
}
