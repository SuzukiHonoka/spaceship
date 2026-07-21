package router

import (
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
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
