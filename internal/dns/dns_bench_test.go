package dns

import (
	"context"
	"testing"
	"time"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	mdns "github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Count RPC attempts as well as CPU/allocations: a mock has no RTT, so timing
// alone would hide the actual saving on older servers (especially mux=0).
func BenchmarkServeDNSLegacyCapability(b *testing.B) {
	for _, cached := range []bool{false, true} {
		name := "probe-every-query"
		if cached {
			name = "cached"
		}
		b.Run(name, func(b *testing.B) {
			s, _ := NewServer("127.0.0.1:0", false)
			attempts := 0
			s.exchanger = fakeWireExchanger{exchange: func(context.Context, []byte, proto.Network, bool) ([]byte, error) {
				attempts++
				return nil, status.Error(codes.Unimplemented, "old server")
			}}
			s.legacy = fakeLegacyResolver{resolve: func(context.Context, *mdns.Msg, bool) ([]mdns.RR, int, error) {
				attempts++
				return nil, mdns.RcodeSuccess, nil
			}}
			s.legacyNotice.Do(func() {})
			query := new(mdns.Msg).SetQuestion("example.com.", mdns.TypeA)
			if cached {
				s.legacyUntil.Store(time.Now().Add(time.Hour).UnixNano())
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if !cached {
					s.legacyUntil.Store(0)
				}
				s.ServeDNS(new(responseRecorder), query)
			}
			b.ReportMetric(float64(attempts)/float64(b.N), "RPCs/query")
		})
	}
}
