package client

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func newBenchQueueWrapper(b *testing.B, id int) *ConnWrapper {
	b.Helper()
	conn, err := grpc.NewClient(
		"passthrough:///queue-bench",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	)
	if err != nil {
		b.Fatal(err)
	}
	return &ConnWrapper{ClientConn: conn, ID: id}
}

func newBenchPooledQueue(b *testing.B) *ConnQueue {
	b.Helper()
	first := newBenchQueueWrapper(b, 1)
	second := newBenchQueueWrapper(b, 2)
	queue := &ConnQueue{
		Size:                 2,
		Conn:                 ConnWrappers{first, second},
		maxLoadPerConnection: 1 << 20,
	}
	b.Cleanup(queue.Destroy)
	return queue
}

func BenchmarkConnQueueGetConn(b *testing.B) {
	queue := newBenchPooledQueue(b)
	b.ReportAllocs()

	for b.Loop() {
		_, done, err := queue.GetConn()
		if err != nil {
			b.Fatal(err)
		}
		if err := done(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConnQueueGetConnParallel(b *testing.B) {
	queue := newBenchPooledQueue(b)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, done, err := queue.GetConn()
			if err != nil {
				b.Error(err)
				return
			}
			if err := done(); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkConnQueueGetConnBusy checks out from connections that already
// carry other sessions, as a pool under load does: a release that leaves a
// connection in use skips the idle bookkeeping an emptying release does.
func BenchmarkConnQueueGetConnBusy(b *testing.B) {
	queue := newBenchPooledQueue(b)
	for _, conn := range queue.Conn {
		conn.InUse.Store(8)
	}
	b.ReportAllocs()

	for b.Loop() {
		_, done, err := queue.GetConn()
		if err != nil {
			b.Fatal(err)
		}
		if err := done(); err != nil {
			b.Fatal(err)
		}
	}
}

// The existing hot-checkout benches never fill a wrapper. Exercise an actual
// burst at the growth boundary and report wasted creation explicitly.
func BenchmarkConnQueueGrowthBurst(b *testing.B) {
	const callers = 32
	var created atomic.Int64
	b.ReportAllocs()
	for range b.N {
		q := NewConnQueue(1, nil)
		warm := newBenchQueueWrapper(b, 1)
		warm.InUse.Store(callers)
		q.Conn = ConnWrappers{warm}
		q.maxLoadPerConnection = callers
		q.newWrapper = func() (*ConnWrapper, error) {
			created.Add(1)
			return newBenchQueueWrapper(b, 0), nil
		}
		var workers sync.WaitGroup
		dones := make(chan func() error, callers)
		start := make(chan struct{})
		for range callers {
			workers.Go(func() {
				<-start
				_, done, err := q.GetConn()
				if err != nil {
					b.Error(err)
					return
				}
				dones <- done
			})
		}
		close(start)
		workers.Wait()
		close(dones)
		for done := range dones {
			_ = done()
		}
		q.Destroy()
	}
	b.ReportMetric(float64(created.Load())/float64(b.N), "new-conns/burst")
}
