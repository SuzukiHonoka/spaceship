package router

import (
	"errors"
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/blackhole"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/direct"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/forward"
)

// TestEgressSupportsUDPMatchesTransports guards SupportsUDP against drifting from
// the transports that actually implement transport.PacketDialer.
//
// EgressProxy is excluded because constructing it checks out a pooled gRPC
// connection; it is covered by a compile-time assertion in the rpc client
// package instead.
func TestEgressSupportsUDPMatchesTransports(t *testing.T) {
	for _, egress := range []Egress{EgressDirect, EgressForward, EgressBlackHole} {
		tr, err := egress.GetTransport()
		if err != nil {
			t.Fatalf("%s: GetTransport() error = %v", egress, err)
		}
		_, isPacketDialer := tr.(transport.PacketDialer)
		if got := egress.SupportsUDP(); got != isPacketDialer {
			t.Errorf("%s: SupportsUDP() = %v, but implements PacketDialer = %v",
				egress, got, isPacketDialer)
		}
		if err := tr.Close(); err != nil {
			t.Errorf("%s: Close() error = %v", egress, err)
		}
	}
}

func TestIPBlockDecisive(t *testing.T) {
	if err := SetRoutes(Routes{
		{MatchType: TypeCIDR, Sources: []string{"::/0"}, Destination: EgressBlock},
		{MatchType: TypeCIDR, Sources: []string{"127.0.0.1/32"}, Destination: EgressBlock},
		{MatchType: TypeDefault, Destination: EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = SetRoutes(Routes{{MatchType: TypeDefault, Destination: EgressDirect}})
	})
	if !IPBlockDecisive("127.0.0.1") {
		t.Fatal("127.0.0.1 was not blocked")
	}
	if IPBlockDecisive("203.0.113.10") {
		t.Fatal("an address outside the block rule was blocked")
	}

	if err := SetRoutes(Routes{
		{MatchType: TypeExact, Sources: []string{"sniff.example"}, Destination: EgressDirect},
		{MatchType: TypeCIDR, Sources: []string{"203.0.113.0/24"}, Destination: EgressBlock},
	}); err != nil {
		t.Fatal(err)
	}
	if IPBlockDecisive("203.0.113.10") {
		t.Fatal("a CIDR block behind a name rule was treated as decided")
	}

	if err := SetRoutes(Routes{{MatchType: TypeDefault, Destination: EgressBlock}}); err != nil {
		t.Fatal(err)
	}
	if !IPBlockDecisive("127.0.0.1") {
		t.Fatal("a default block route did not refuse the IP")
	}
}

func TestAdmitProxyFirst(t *testing.T) {
	tests := []struct {
		name   string
		routes Routes
		ip     string
		want   bool
	}{
		{
			name:   "default proxy",
			routes: Routes{{MatchType: TypeDefault, Destination: EgressProxy}},
			ip:     "127.0.0.1",
			want:   true,
		},
		{
			name:   "default direct",
			routes: Routes{{MatchType: TypeDefault, Destination: EgressDirect}},
			ip:     "127.0.0.1",
			want:   false,
		},
		{
			name:   "default block",
			routes: Routes{{MatchType: TypeDefault, Destination: EgressBlock}},
			ip:     "127.0.0.1",
			want:   false,
		},
		{
			name: "ipv6 block then default proxy",
			routes: Routes{
				{MatchType: TypeCIDR, Sources: []string{"::/0"}, Destination: EgressBlock},
				{MatchType: TypeDefault, Destination: EgressProxy},
			},
			ip:   "127.0.0.2",
			want: true,
		},
		{
			name: "exact proxy does not match the IP",
			routes: Routes{
				{MatchType: TypeCIDR, Sources: []string{"::/0"}, Destination: EgressBlock},
				{MatchType: TypeExact, Sources: []string{"sniff.example"}, Destination: EgressProxy},
			},
			ip:   "127.0.0.2",
			want: true,
		},
		{
			name: "name rule can select direct",
			routes: Routes{
				{MatchType: TypeExact, Sources: []string{"sniff.example"}, Destination: EgressDirect},
				{MatchType: TypeDefault, Destination: EgressProxy},
			},
			ip:   "127.0.0.1",
			want: false,
		},
		{
			name: "name blocks then default proxy",
			routes: Routes{
				{MatchType: TypeExact, Sources: []string{"exact-blocked.test"}, Destination: EgressBlock},
				{MatchType: TypeDomain, Sources: []string{"domain-blocked.test"}, Destination: EgressBlock},
				{MatchType: TypeCIDR, Sources: []string{"198.51.100.0/24"}, Destination: EgressBlock},
				{MatchType: TypeDefault, Destination: EgressProxy},
			},
			ip:   "127.0.0.1",
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := SetRoutes(tt.routes); err != nil {
				t.Fatalf("SetRoutes() error = %v", err)
			}
			t.Cleanup(func() {
				_ = SetRoutes(Routes{{MatchType: TypeDefault, Destination: EgressDirect}})
			})
			if got := AdmitProxyFirst(tt.ip); got != tt.want {
				t.Fatalf("AdmitProxyFirst(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestTransportNameMatchesTransport(t *testing.T) {
	if EgressProxy.TransportName() != "rpc" {
		t.Fatalf("proxy TransportName() = %q, want rpc", EgressProxy.TransportName())
	}
	if EgressBlock.TransportName() != "block" {
		t.Fatalf("block TransportName() = %q, want block", EgressBlock.TransportName())
	}
	for _, egress := range []Egress{EgressDirect, EgressForward, EgressBlackHole} {
		tr, err := egress.GetTransport()
		if err != nil {
			t.Fatalf("%s: GetTransport() error = %v", egress, err)
		}
		if egress.TransportName() != tr.String() {
			t.Errorf("%s: TransportName() = %q, transport String() = %q", egress, egress.TransportName(), tr)
		}
		if err := tr.Close(); err != nil {
			t.Errorf("%s: Close() error = %v", egress, err)
		}
	}
}

func TestMatchEgressAgreesWithGetRoute(t *testing.T) {
	if err := SetRoutes(Routes{{MatchType: TypeDefault, Destination: EgressBlock}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = SetRoutes(Routes{{MatchType: TypeDefault, Destination: EgressDirect}})
	})
	egress, err := MatchEgress("127.0.0.1", "")
	if err != nil {
		t.Fatalf("MatchEgress() error = %v", err)
	}
	if egress != EgressBlock {
		t.Fatalf("MatchEgress() = %s, want block", egress)
	}
	if _, err := GetRoute("127.0.0.1"); !errors.Is(err, transport.ErrBlocked) {
		t.Fatalf("GetRoute() error = %v, want blocked", err)
	}
}

func TestDialHost(t *testing.T) {
	const (
		ip   = "203.0.113.10"
		name = "sniff.example"
	)
	tests := []struct {
		name  string
		route transport.Transport
		host  string
		want  string
	}{
		{name: "direct keeps the client IP", route: direct.New(), host: name, want: ip},
		{name: "blackhole keeps the client IP", route: blackhole.New(), host: name, want: ip},
		{name: "forward resolves the name", route: forward.New(), host: name, want: name},
		{name: "empty name keeps the client IP", route: forward.New(), want: ip},
		{name: "nil route keeps the client IP", host: name, want: ip},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DialHost(tt.route, ip, tt.host); got != tt.want {
				t.Fatalf("DialHost() = %q, want %q", got, tt.want)
			}
		})
	}
	if HostForEgress(EgressDirect, ip, name) != ip || HostForEgress(EgressBlackHole, ip, name) != ip {
		t.Fatal("direct and blackhole HostForEgress did not keep the client IP")
	}
	if HostForEgress(EgressProxy, ip, name) != name || HostForEgress(EgressForward, ip, name) != name {
		t.Fatal("proxy and forward HostForEgress did not keep the recovered name")
	}
	if HostForEgress(EgressProxy, ip, "") != ip {
		t.Fatal("an empty name did not keep the client IP")
	}
}

// TestAnyRouteSupportsUDP covers the capability check that refuses SOCKS5 UDP
// ASSOCIATE when no installed route could carry a datagram.
func TestAnyRouteSupportsUDP(t *testing.T) {
	tests := []struct {
		name   string
		routes Routes
		want   bool
	}{
		{"direct", Routes{{MatchType: TypeDefault, Destination: EgressDirect}}, true},
		{"proxy", Routes{{MatchType: TypeDefault, Destination: EgressProxy}}, true},
		{"forward only", Routes{{MatchType: TypeDefault, Destination: EgressForward}}, false},
		{"blackhole only", Routes{{MatchType: TypeDefault, Destination: EgressBlackHole}}, false},
		{"block only", Routes{{MatchType: TypeDefault, Destination: EgressBlock}}, false},
		{"mixed keeps capability", Routes{
			{MatchType: TypeCIDR, Sources: []string{"10.0.0.0/8"}, Destination: EgressBlackHole},
			{MatchType: TypeDefault, Destination: EgressDirect},
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := SetRoutes(tt.routes); err != nil {
				t.Fatalf("SetRoutes() error = %v", err)
			}
			if got := AnyRouteSupportsUDP(); got != tt.want {
				t.Errorf("AnyRouteSupportsUDP() = %v, want %v", got, tt.want)
			}
		})
	}
}
