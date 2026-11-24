package config

import (
	"fmt"
	"testing"
	"time"

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

func TestApplyReloadResetsOmittedBufferPathAndIdleTimeout(t *testing.T) {
	oldBuffer := transport.GetBufferSize()
	oldIdle := transport.GetIdleTimeout()
	oldService := rpc.GetServiceName()
	t.Cleanup(func() {
		transport.EnableIPv6()
		transport.SetBufferSize(uint16(oldBuffer / 1024))
		transport.SetIdleTimeout(oldIdle)
		rpc.SetServiceName(oldService)
	})

	custom, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001",
		"buffer":64,
		"path":"custom.v1.Tunnel",
		"idle_timeout":45
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := custom.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := transport.GetBufferSize(); got != 64*1024 {
		t.Fatalf("custom buffer = %d, want %d", got, 64*1024)
	}
	if got := rpc.GetServiceName(); got != "custom.v1.Tunnel" {
		t.Fatalf("custom service = %q, want custom.v1.Tunnel", got)
	}
	if got := transport.GetIdleTimeout(); got != 45*time.Second {
		t.Fatalf("custom idle_timeout = %v, want 45s", got)
	}

	reloaded, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := transport.GetBufferSize(); got != int(transport.DefaultBufferSizeKiB)*1024 {
		t.Fatalf("reloaded buffer = %d, want default %d", got, int(transport.DefaultBufferSizeKiB)*1024)
	}
	if got := rpc.GetServiceName(); got != "proxy.Proxy" {
		t.Fatalf("reloaded service = %q, want default proxy.Proxy", got)
	}
	if got := transport.GetIdleTimeout(); got != transport.DefaultIdleTimeout {
		t.Fatalf("reloaded idle_timeout = %v, want default %v", got, transport.DefaultIdleTimeout)
	}
}
