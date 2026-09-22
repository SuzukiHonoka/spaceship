package tun

import (
	"testing"
)

func BenchmarkFairDNSShare(b *testing.B) {
	s := &Service{dnsSlots: make(chan struct{}, 256)}
	for _, active := range []int64{1, 8, 100} {
		b.Run(benchActiveName(active), func(b *testing.B) {
			s.dnsClients.Store(active)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = s.fairDNSShare(64)
			}
		})
	}
}

func BenchmarkAcquireReleaseDNS(b *testing.B) {
	s := &Service{dnsSlots: make(chan struct{}, 256)}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !s.acquireDNS() {
			b.Fatal("acquireDNS rejected with free capacity")
		}
		s.releaseDNS()
	}
}

func BenchmarkAcquireReleaseDNSParallel(b *testing.B) {
	s := &Service{dnsSlots: make(chan struct{}, 256)}
	s.dnsClients.Store(8)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for !s.acquireDNS() {
				// Another worker holds every slot; retry until one frees.
			}
			s.releaseDNS()
		}
	})
}

func benchActiveName(active int64) string {
	switch active {
	case 1:
		return "active1"
	case 8:
		return "active8"
	case 100:
		return "active100"
	default:
		return "active"
	}
}
