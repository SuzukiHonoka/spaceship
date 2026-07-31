package client

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

func newQueueTestWrapper(t *testing.T, id int) *ConnWrapper {
	t.Helper()
	conn, err := grpc.NewClient(
		"passthrough:///queue-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &ConnWrapper{ClientConn: conn, ID: id}
}

func newQueueTestParams() *Params {
	return NewParams(
		"127.0.0.1:1",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	)
}

func TestConnQueueConcurrentReservationsStayBalanced(t *testing.T) {
	first := newQueueTestWrapper(t, 1)
	second := newQueueTestWrapper(t, 2)
	queue := &ConnQueue{
		Size: 2,
		Conn: ConnWrappers{first, second},
	}
	t.Cleanup(queue.Destroy)

	const callers = 1000
	start := make(chan struct{})
	doneFunctions := make(chan func() error, callers)
	var workers sync.WaitGroup
	workers.Add(callers)
	for range callers {
		go func() {
			defer workers.Done()
			<-start
			_, done, err := queue.GetConn()
			if err != nil {
				t.Errorf("GetConn() error = %v", err)
				return
			}
			doneFunctions <- done
		}()
	}
	close(start)
	workers.Wait()
	close(doneFunctions)

	firstLoad := first.GetCurrentLoad()
	secondLoad := second.GetCurrentLoad()
	if firstLoad+secondLoad != callers {
		t.Fatalf("reserved load = %d + %d, want %d", firstLoad, secondLoad, callers)
	}
	if difference := int64(firstLoad) - int64(secondLoad); difference < -1 || difference > 1 {
		t.Fatalf("concurrent reservation load = (%d, %d), want difference <= 1", firstLoad, secondLoad)
	}

	for done := range doneFunctions {
		if err := done(); err != nil {
			t.Errorf("Done() error = %v", err)
		}
		if err := done(); err != nil {
			t.Errorf("second Done() error = %v", err)
		}
	}
	if first.GetCurrentLoad() != 0 || second.GetCurrentLoad() != 0 {
		t.Fatalf(
			"idempotent release left load = (%d, %d), want (0, 0)",
			first.GetCurrentLoad(),
			second.GetCurrentLoad(),
		)
	}
}

func TestConnQueueDestroyHandlesPartiallyInitializedPool(t *testing.T) {
	wrapper := newQueueTestWrapper(t, 1)
	queue := &ConnQueue{
		Size: 4,
		Conn: ConnWrappers{wrapper},
	}
	queue.Destroy()
	if !queue.shutdown {
		t.Fatal("Destroy() did not mark partial queue as shut down")
	}
}

func TestConnQueueAddRejectsNilAndClosesAfterShutdown(t *testing.T) {
	queue := NewConnQueue(1, newQueueTestParams())
	queue.Add(nil)
	if len(queue.Conn) != 0 {
		t.Fatalf("nil Add() changed queue length to %d", len(queue.Conn))
	}

	queue.Destroy()
	wrapper := newQueueTestWrapper(t, 1)
	queue.Add(wrapper)
	if len(queue.Conn) != 0 {
		t.Fatalf("Add() after shutdown changed queue length to %d", len(queue.Conn))
	}
	if state := wrapper.GetState(); state != connectivity.Shutdown {
		t.Fatalf("wrapper added after shutdown has state %s, want Shutdown", state)
	}
}

func TestConnQueueInitFailureClosesPartialPool(t *testing.T) {
	queue := NewConnQueue(1, nil)
	if err := queue.Init(); err == nil {
		t.Fatal("Init() with nil parameters succeeded")
	}
	if !queue.shutdown {
		t.Fatal("Init() failure did not shut down the partial pool")
	}
}

func TestConnQueueReportsUnavailablePoolStates(t *testing.T) {
	shutdown := &ConnQueue{Size: 1, shutdown: true}
	if _, _, err := shutdown.GetConn(); err == nil {
		t.Fatal("GetConn() accepted a shut down queue")
	}

	empty := &ConnQueue{Size: 1}
	if _, _, err := empty.GetConn(); err == nil {
		t.Fatal("GetConn() accepted an empty persistent pool")
	}
}

func TestConnQueueLegacyUnpooledConnection(t *testing.T) {
	params := newQueueTestParams()
	queue := NewConnQueue(0, params)
	wrapper, done, err := queue.GetConn()
	if err != nil {
		t.Fatal(err)
	}
	if wrapper == nil || done == nil {
		t.Fatalf(
			"GetConn() returned wrapper=%t release=%t, want both",
			wrapper != nil,
			done != nil,
		)
	}
	if err := done(); err != nil {
		t.Fatalf("release unpooled wrapper: %v", err)
	}
	if err := done(); err != nil {
		t.Fatalf("second release unpooled wrapper: %v", err)
	}

	invalid := NewConnQueue(0, nil)
	if _, _, err := invalid.GetConn(); err == nil {
		t.Fatal("unpooled GetConn() accepted nil parameters")
	}
}

func TestConnQueueDestroyedUnpooledQueueCannotDial(t *testing.T) {
	queue := NewConnQueue(0, newQueueTestParams())
	queue.Destroy()
	if _, _, err := queue.GetConn(); err == nil {
		t.Fatal("GetConn() dialed after Destroy")
	}
	if _, _, err := queue.GetConnOutSide(); err == nil {
		t.Fatal("GetConnOutSide() dialed after Destroy")
	}
}

func TestOnceErrorRunsOnceAndCachesFailure(t *testing.T) {
	sentinel := errors.New("sentinel close error")
	var calls int
	done := onceError(func() error {
		calls++
		return sentinel
	})

	if err := done(); !errors.Is(err, sentinel) {
		t.Fatalf("first call error = %v, want sentinel", err)
	}
	if err := done(); !errors.Is(err, sentinel) {
		t.Fatalf("second call error = %v, want cached sentinel", err)
	}
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
}

func TestConnQueuePersistentPoolGrowsAtStreamCapacity(t *testing.T) {
	first := newQueueTestWrapper(t, 1)
	first.InUse.Store(2)
	queue := &ConnQueue{
		Params:                   newQueueTestParams(),
		Size:                     1,
		Conn:                     ConnWrappers{first},
		maxLoadPerConnection:     2,
		maxPersistentConnections: 3,
	}
	t.Cleanup(queue.Destroy)

	got, done, err := queue.GetConn()
	if err != nil {
		t.Fatal(err)
	}
	if got == first || got.ID != 2 || len(queue.Conn) != 2 {
		t.Fatalf("grown queue returned ID %d with %d connections", got.ID, len(queue.Conn))
	}
	if got.GetCurrentLoad() != 1 {
		t.Fatalf("new connection load = %d, want 1", got.GetCurrentLoad())
	}
	if err := done(); err != nil {
		t.Fatal(err)
	}
}

func TestConnQueuePersistentPoolEnforcesGrowthCeiling(t *testing.T) {
	first := newQueueTestWrapper(t, 1)
	first.InUse.Store(1)
	queue := &ConnQueue{
		Params:                   newQueueTestParams(),
		Size:                     1,
		Conn:                     ConnWrappers{first},
		maxLoadPerConnection:     1,
		maxPersistentConnections: 1,
	}
	t.Cleanup(queue.Destroy)

	if _, _, err := queue.GetConn(); err == nil {
		t.Fatal("GetConn() exceeded the persistent pool capacity")
	}
	if first.GetCurrentLoad() != 1 || len(queue.Conn) != 1 {
		t.Fatalf("capacity rejection mutated queue: load=%d size=%d", first.GetCurrentLoad(), len(queue.Conn))
	}
}

func TestConnQueuePersistentPoolReportsGrowthDialFailure(t *testing.T) {
	first := newQueueTestWrapper(t, 1)
	first.InUse.Store(1)
	queue := &ConnQueue{
		Size:                     1,
		Conn:                     ConnWrappers{first},
		maxLoadPerConnection:     1,
		maxPersistentConnections: 2,
	}
	t.Cleanup(queue.Destroy)

	if _, _, err := queue.GetConn(); err == nil {
		t.Fatal("GetConn() ignored an elastic growth dial failure")
	}
	if first.GetCurrentLoad() != 1 || len(queue.Conn) != 1 {
		t.Fatalf("growth failure mutated queue: load=%d size=%d", first.GetCurrentLoad(), len(queue.Conn))
	}
}

func TestConnQueueSynchronouslyReplacesFullyShutdownPool(t *testing.T) {
	first := newQueueTestWrapper(t, 1)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	queue := &ConnQueue{
		Params:                   newQueueTestParams(),
		Size:                     1,
		Conn:                     ConnWrappers{first},
		maxLoadPerConnection:     1,
		maxPersistentConnections: 2,
	}
	t.Cleanup(queue.Destroy)

	got, done, err := queue.GetConn()
	if err != nil {
		t.Fatal(err)
	}
	if got == first || got.ID != first.ID || len(queue.Conn) != 1 {
		t.Fatalf("replacement = %p ID %d, original %p ID %d", got, got.ID, first, first.ID)
	}
	if err := done(); err != nil {
		t.Fatal(err)
	}
}

func TestConnQueueSynchronouslyRepairsNilPoolEntry(t *testing.T) {
	queue := &ConnQueue{
		Params: newQueueTestParams(),
		Size:   1,
		Conn:   ConnWrappers{nil},
	}
	t.Cleanup(queue.Destroy)

	got, done, err := queue.GetConn()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != 1 || queue.Conn[0] != got {
		t.Fatalf("replacement = %p ID %d, queue = %+v", got, got.ID, queue.Conn)
	}
	if err := done(); err != nil {
		t.Fatal(err)
	}
}

func TestConnWrappersPreferUsableConnectionOverTransientFailure(t *testing.T) {
	degradedConn, err := grpc.NewClient(
		"passthrough:///degraded",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return nil, errors.New("intentional dial failure")
		}),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  time.Hour,
				Multiplier: 1,
				Jitter:     0,
				MaxDelay:   time.Hour,
			},
			MinConnectTimeout: 10 * time.Millisecond,
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	degraded := &ConnWrapper{ClientConn: degradedConn, ID: 1}
	t.Cleanup(func() { _ = degraded.Close() })
	degraded.Connect()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for state := degraded.GetState(); state != connectivity.TransientFailure; state = degraded.GetState() {
		if !degraded.WaitForStateChange(ctx, state) {
			t.Fatalf("degraded connection did not enter TransientFailure: state=%s err=%v", state, ctx.Err())
		}
	}

	usable := newQueueTestWrapper(t, 2)
	t.Cleanup(func() { _ = usable.Close() })
	usable.InUse.Store(7)
	if got := (ConnWrappers{degraded, usable}).PickLeastLoaded(); got != usable {
		t.Fatalf("PickLeastLoaded() = connection %d, want usable connection %d", got.ID, usable.ID)
	}
	if got := (ConnWrappers{nil, degraded}).PickLeastLoaded(); got != degraded {
		t.Fatalf("degraded-only fallback = %p, want %p", got, degraded)
	}
}

func TestConnQueueAsynchronousReplacementPreservesPoolIdentity(t *testing.T) {
	first := newQueueTestWrapper(t, 7)
	queue := &ConnQueue{
		Params: newQueueTestParams(),
		Size:   1,
		Conn:   ConnWrappers{first},
	}
	t.Cleanup(queue.Destroy)

	queue.replaceConn(first)
	if queue.Conn[0] == first || queue.Conn[0].ID != first.ID {
		t.Fatalf(
			"replacement = %p ID %d, original %p ID %d",
			queue.Conn[0],
			queue.Conn[0].ID,
			first,
			first.ID,
		)
	}
}

func TestConnQueueConcurrentReservationsGrowWithoutOverloadingWrapper(t *testing.T) {
	first := newQueueTestWrapper(t, 1)
	queue := &ConnQueue{
		Params:                   newQueueTestParams(),
		Size:                     1,
		Conn:                     ConnWrappers{first},
		maxLoadPerConnection:     10,
		maxPersistentConnections: 4,
	}
	t.Cleanup(queue.Destroy)

	const callers = 35
	start := make(chan struct{})
	releases := make(chan func() error, callers)
	var workers sync.WaitGroup
	workers.Add(callers)
	for range callers {
		go func() {
			defer workers.Done()
			<-start
			_, done, err := queue.GetConn()
			if err != nil {
				t.Errorf("GetConn() error = %v", err)
				return
			}
			releases <- done
		}()
	}
	close(start)
	workers.Wait()
	close(releases)

	queue.mu.RLock()
	if len(queue.Conn) != 4 {
		queue.mu.RUnlock()
		t.Fatalf("connection count = %d, want 4", len(queue.Conn))
	}
	var total uint32
	for _, wrapper := range queue.Conn {
		load := wrapper.GetCurrentLoad()
		if load > queue.maxLoadPerConnection {
			queue.mu.RUnlock()
			t.Fatalf("connection %d load = %d, limit %d", wrapper.ID, load, queue.maxLoadPerConnection)
		}
		total += load
	}
	queue.mu.RUnlock()
	if total != callers {
		t.Fatalf("reserved load = %d, want %d", total, callers)
	}

	for release := range releases {
		if err := release(); err != nil {
			t.Error(err)
		}
	}
}

func TestConnQueueRejectsInvalidInitialSize(t *testing.T) {
	for _, size := range []int{-1, MaxPersistentConnections + 1} {
		queue := NewConnQueue(size, newQueueTestParams())
		if err := queue.Init(); err == nil {
			t.Fatalf("Init() accepted size %d", size)
		}
	}
}
