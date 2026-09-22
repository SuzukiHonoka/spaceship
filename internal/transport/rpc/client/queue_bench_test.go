package client

import (
	"context"
	"net"
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
	b.ResetTimer()
	for range b.N {
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
