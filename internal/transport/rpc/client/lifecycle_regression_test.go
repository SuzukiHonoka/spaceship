package client

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

func TestWriteDeadlineResetWaitsForExpiredCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &mockProxyClient{ctx: ctx, sendChan: make(chan *proto.ProxySRC, 1)}
	c := NewStreamPacketConn(ctx, m, cancel, "127.0.0.1:53")
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	defer release()
	c.wdeadline.onFire = func() { close(entered); <-resume; c.writeDeadlineFired() }
	_ = c.SetWriteDeadline(time.Now().Add(time.Millisecond))
	<-entered // timer has closed cancel but has not finished onFire
	reset := make(chan struct{})
	go func() { _ = c.SetWriteDeadline(time.Time{}); close(reset) }()
	select {
	case <-reset:
		t.Fatal("deadline reset returned while old callback was still pending")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case <-reset:
	case <-time.After(time.Second):
		t.Fatal("reset blocked")
	}
	if _, err := c.WriteTo([]byte("fresh"), nil); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("stale callback canceled new write")
	}
}

type waitingStreamClient struct {
	proto.ProxyClient
	entered chan struct{}
}

func (m waitingStreamClient) Proxy(ctx context.Context, _ ...grpc.CallOption) (proto.Proxy_ProxyClient, error) {
	close(m.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDialPacketContextCancelsStreamCreation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := waitingStreamClient{entered: make(chan struct{})}
	c := &Client{ProxyClient: m}
	done := make(chan error, 1)
	go func() { _, err := c.DialPacketContext(ctx, "udp", "example.com:53"); done <- err }()
	<-m.entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream setup ignored owner cancellation")
	}
}

func TestDialPacketBoundsStreamCreation(t *testing.T) {
	old := transport.GetDialTimeout()
	transport.SetDialTimeout(20 * time.Millisecond)
	defer transport.SetDialTimeout(old)
	m := waitingStreamClient{entered: make(chan struct{})}
	c := &Client{ProxyClient: m}
	done := make(chan error, 1)
	go func() { _, err := c.DialPacket("udp", "example.com:53"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("setup succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("stream creation had no setup timeout")
	}
}

func TestConnQueueCoalescesConcurrentGrowth(t *testing.T) {
	q := NewConnQueue(1, newQueueTestParams())
	warm := newQueueTestWrapper(t, 1)
	warm.InUse.Store(32)
	q.Conn = ConnWrappers{warm}
	q.maxLoadPerConnection = 32
	var dials atomic.Int32
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(resume) })
	q.newWrapper = func() (*ConnWrapper, error) {
		if dials.Add(1) == 1 {
			close(entered)
		}
		<-resume
		return newQueueTestWrapper(t, 0), nil
	}
	defer q.Destroy()
	var workers sync.WaitGroup
	dones := make(chan func() error, 32)
	for range 32 {
		workers.Go(func() {
			_, done, err := q.GetConn()
			if err != nil {
				t.Error(err)
				return
			}
			dones <- done
		})
	}
	<-entered
	once.Do(func() { close(resume) })
	workers.Wait()
	close(dones)
	for done := range dones {
		_ = done()
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("created %d wrappers for one available pool slot", got)
	}
}

func TestConnQueueRetiresOnlyIdleSurplus(t *testing.T) {
	q := NewConnQueue(1, newQueueTestParams())
	q.retireAfter = 20 * time.Millisecond
	q.maxLoadPerConnection = 1
	defer q.Destroy()
	if err := q.Init(); err != nil {
		t.Fatal(err)
	}
	_, warmDone, err := q.GetConn()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = warmDone() }()
	extra, done, err := q.GetConn()
	if err != nil {
		t.Fatal(err)
	}
	// Active surplus must survive a retirement sweep.
	q.retireIdle()
	if extra.getState() == connectivity.Shutdown {
		t.Fatal("retired active wrapper")
	}
	_ = done()
	deadline := time.Now().Add(time.Second)
	for extra.getState() != connectivity.Shutdown && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if extra.getState() != connectivity.Shutdown {
		t.Fatal("idle surplus was not retired")
	}
	if total, _, _ := q.GetConnectionSummary(); total != 1 {
		t.Fatalf("pool size = %d", total)
	}
	if q.Conn[0].getState() == connectivity.Shutdown {
		t.Fatal("retired warm minimum")
	}
}

var _ transport.ContextPacketDialer = (*Client)(nil)
var _ net.PacketConn = (*StreamPacketConn)(nil)
