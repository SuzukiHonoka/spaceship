package router

import (
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
