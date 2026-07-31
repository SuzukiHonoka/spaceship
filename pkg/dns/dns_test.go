package dns

import (
	"net"
	"net/netip"
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

func TestDNSAddress(t *testing.T) {
	tests := []struct {
		name   string
		dns    DNS
		want   string
		reason string
	}{
		{
			name: "bare ipv4 gets the standard port",
			dns:  DNS{Type: TypeCommon, Server: "1.1.1.1"},
			want: "1.1.1.1:53",
		},
		{
			name: "bare ipv6 is bracketed",
			dns:  DNS{Type: TypeCommon, Server: "2606:4700:4700::1111"},
			want: "[2606:4700:4700::1111]:53",
		},
		{
			name: "bare hostname gets the standard port",
			dns:  DNS{Type: TypeCommon, Server: "resolver.example"},
			want: "resolver.example:53",
		},
		{
			name:   "explicit port is preserved",
			dns:    DNS{Type: TypeCommon, Server: "127.0.0.1:5353"},
			want:   "127.0.0.1:5353",
			reason: "a stub resolver on a non-standard port must be reachable",
		},
		{
			name: "explicit ipv6 port is preserved",
			dns:  DNS{Type: TypeCommon, Server: "[::1]:5353"},
			want: "[::1]:5353",
		},
		{
			name: "default type behaves like common",
			dns:  DNS{Type: TypeDefault, Server: "8.8.8.8"},
			want: "8.8.8.8:53",
		},
		{
			name:   "unimplemented types have no address",
			dns:    DNS{Type: TypeDOT, Server: "1.1.1.1"},
			want:   "",
			reason: "DOT/DOH are not wired up; an address here would silently query plaintext",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.dns.Address(); got != tt.want {
				if tt.reason != "" {
					t.Errorf("Address() = %q, want %q (%s)", got, tt.want, tt.reason)
					return
				}
				t.Errorf("Address() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlatformDefaultSurvivesResolverPolicyChanges(t *testing.T) {
	originalPolicy := platformDefaultResolver.Load()
	originalOutbound := transport.OutboundResolver()
	t.Cleanup(func() {
		platformDefaultResolver.Store(originalPolicy)
		transport.SetOutboundResolver(originalOutbound)
	})

	configured := &DNS{Type: TypeCommon, Server: "127.0.0.1:5353"}
	if err := configured.SetPlatformDefault(); err != nil {
		t.Fatal(err)
	}
	platformResolver := transport.OutboundResolver()
	if platformResolver == nil || platformResolver == net.DefaultResolver {
		t.Fatalf("platform resolver = %p, want fixed resolver", platformResolver)
	}

	transport.SetOutboundResolver(net.DefaultResolver)
	SetSystemDefault(false)
	if got := transport.OutboundResolver(); got != platformResolver {
		t.Fatalf("unmarked reset resolver = %p, want platform resolver %p", got, platformResolver)
	}

	transport.SetOutboundResolver(net.DefaultResolver)
	SetSystemDefault(true)
	if got := transport.OutboundResolver(); got != platformResolver {
		t.Fatalf("marked reset resolver = %p, want mark-aware platform resolver %p", got, platformResolver)
	}
}

func TestSystemDefaultUsesPureGoResolverWhenSocketMarksAreRequired(t *testing.T) {
	originalPolicy := platformDefaultResolver.Load()
	originalOutbound := transport.OutboundResolver()
	t.Cleanup(func() {
		platformDefaultResolver.Store(originalPolicy)
		transport.SetOutboundResolver(originalOutbound)
	})
	platformDefaultResolver.Store(&platformResolverPolicy{resolver: net.DefaultResolver})

	SetSystemDefault(true)
	resolver := transport.OutboundResolver()
	if resolver == nil || resolver == net.DefaultResolver || !resolver.PreferGo || resolver.Dial == nil {
		t.Fatalf("marked system resolver = %+v, want dedicated pure-Go resolver", resolver)
	}
}

func TestResolveServerAddress(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{name: "IPv4 literal", address: "192.0.2.53:5353", want: "192.0.2.53:5353"},
		{name: "IPv6 literal", address: "[2001:db8::53]:5353", want: "[2001:db8::53]:5353"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveServerAddress(tt.address)
			if err != nil {
				t.Fatalf("resolveServerAddress(%q) error = %v", tt.address, err)
			}
			if got != tt.want {
				t.Fatalf("resolveServerAddress(%q) = %q, want %q", tt.address, got, tt.want)
			}
		})
	}

	resolved, err := resolveServerAddress("localhost:5353")
	if err != nil {
		t.Fatalf("resolveServerAddress(localhost) error = %v", err)
	}
	host, port, err := net.SplitHostPort(resolved)
	if err != nil {
		t.Fatalf("resolved localhost address %q is invalid: %v", resolved, err)
	}
	if port != "5353" {
		t.Fatalf("resolved localhost port = %q, want 5353", port)
	}
	if _, err := netip.ParseAddr(host); err != nil {
		t.Fatalf("resolved localhost host = %q, want IP literal: %v", host, err)
	}

	for _, invalid := range []string{
		"missing-port",
		":53",
		"192.0.2.53:0",
		"192.0.2.53:65536",
		"192.0.2.53:dns",
	} {
		if _, err := resolveServerAddress(invalid); err == nil {
			t.Fatalf("resolveServerAddress accepted invalid address %q", invalid)
		}
	}
}

func TestResolverPolicyRejectsUnsupportedAndNilDefaults(t *testing.T) {
	if err := (*DNS)(nil).SetPlatformDefault(); err == nil {
		t.Fatal("SetPlatformDefault accepted a nil DNS policy")
	}
	if err := (&DNS{Type: TypeDOT, Server: "1.1.1.1"}).SetDefault(); err == nil {
		t.Fatal("SetDefault accepted an unsupported resolver type")
	}

	if got := (&DNS{Type: TypeCommon, Server: "192.0.2.53"}).String(); got == "" {
		t.Fatal("DNS.String returned an empty description")
	}
}
