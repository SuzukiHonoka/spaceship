package config

import (
	"fmt"
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/redirect"
	"github.com/SuzukiHonoka/spaceship/v2/internal/socks"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
)

func TestFailedApplyPreservesUDPAndRPCPolicy(t *testing.T) {
	socks.SetUDPSettings(socks.UDPSettings{})
	defer socks.SetUDPSettings(socks.UDPSettings{})
	buffer, service := transport.GetBufferSize(), rpc.GetServiceName()
	for _, fields := range []string{
		`"uuid":""`,
		`"uuid":"valid","forward":"invalid://proxy"`,
		`"uuid":"valid","route":[{"egress":"proxy","rules":["cidr:not-a-cidr"]}]`,
	} {
		cfg, err := NewFromString(fmt.Sprintf(`{"role":"client","log":"skip","udp":{"disable":true},"buffer":128,"path":"rejected-service",%s}`, fields))
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.Apply(); err == nil {
			t.Fatal("invalid configuration accepted")
		}
		if socks.UDPDisabled() {
			t.Fatal("failed Apply changed UDP policy")
		}
		if transport.GetBufferSize() != buffer || rpc.GetServiceName() != service {
			t.Fatal("failed Apply changed buffer or RPC policy")
		}
	}
}

func TestRedirectOnlyUsesMarkedSystemResolver(t *testing.T) {
	if !redirect.Supported() {
		t.Skip("Linux redirect only")
	}
	mark, resolver := transport.BypassMark(), transport.OutboundResolver()
	defer func() { transport.SetBypassMark(mark); transport.SetOutboundResolver(resolver) }()
	cfg, err := NewFromString(`{"role":"client","uuid":"valid","log":"skip","ipv6":true,"listen_redirect":"127.0.0.1:12345","redirect":{"bypass_mark":21328}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := transport.OutboundResolver(); got.Dial == nil || !got.PreferGo {
		t.Fatal("REDIRECT egress DNS uses unmarked platform resolver")
	}
}

func TestApplyValidatesSOCKSLimits(t *testing.T) {
	for _, settings := range []string{`"max_connections":-1`, `"max_connections":65537`, `"handshake_timeout":-1`} {
		cfg, err := NewFromString(`{"role":"client","uuid":"valid","socks":{` + settings + `}}`)
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.Apply(); err == nil {
			t.Fatal("invalid SOCKS limit accepted")
		}
	}
}
