package client

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// These tests share the process-wide connection pool and early-admit cache.
// They must not call t.Parallel.

func prepareAdmitPool(t *testing.T, addr string, probe time.Duration) {
	t.Helper()
	t.Cleanup(func() {
		earlyAdmitProbeNanos.Store(int64(defaultEarlyAdmitProbeTimeout))
		resetEarlyAdmitLegacy()
		Destroy()
	})
	SetUUID(poolTestUUID)
	// One connection so a later Proxy reuses the connection whose admit stream was canceled.
	if err := Init(addr, "", false, 1, nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	earlyAdmitProbeNanos.Store(int64(probe))
}

func admitFor(t *testing.T, ctx context.Context) (transport.AdmittedSession, error, time.Duration) {
	t.Helper()
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	session, err := c.Admit(ctx)
	elapsed := time.Since(start)
	if err != nil && session != nil {
		_ = session.Close()
		t.Fatalf("Admit returned a session and error %v", err)
	}
	return session, err, elapsed
}

func TestAdmitLegacyServerProbeAndCache(t *testing.T) {
	const probe = 300 * time.Millisecond
	addr := startStubProxy(t)
	prepareAdmitPool(t, addr, probe)

	session, err, elapsed := admitFor(t, nil)
	if session != nil {
		t.Fatal("legacy Admit returned a session")
	}
	if !errors.Is(err, ErrEarlyAdmitUnavailable) {
		t.Fatalf("Admit error = %v, want ErrEarlyAdmitUnavailable", err)
	}
	if elapsed < 200*time.Millisecond || elapsed >= time.Second {
		t.Fatalf("legacy Admit took %s, want a %s probe well under a second", elapsed, probe)
	}
	if _, _, load := GetConnectionSummary(); load != 0 {
		t.Fatalf("load after legacy Admit = %d, want 0", load)
	}
	if !earlyAdmitNotice.Load() {
		t.Fatal("legacy probe did not record the one-time notice")
	}
	if time.Now().UnixNano() >= earlyAdmitLegacyUntil.Load() {
		t.Fatal("legacy probe did not cache the result")
	}

	_, err, elapsed = admitFor(t, nil)
	if !errors.Is(err, ErrEarlyAdmitUnavailable) {
		t.Fatalf("cached Admit error = %v, want ErrEarlyAdmitUnavailable", err)
	}
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("cached Admit took %s, want well under the %s probe", elapsed, probe)
	}
	if _, _, load := GetConnectionSummary(); load != 0 {
		t.Fatalf("load after cached Admit = %d, want 0", load)
	}
	// The probe cancels its stream. The pooled connection has to carry a
	// normal target-first Proxy afterwards, which is the legacy IP path.
	assertProxyStillWorks(t)

	Destroy()
	if earlyAdmitLegacyUntil.Load() != 0 || earlyAdmitNotice.Load() {
		t.Fatal("Destroy did not reset early admission")
	}
	earlyAdmitLegacyUntil.Store(time.Now().Add(time.Minute).UnixNano())
	earlyAdmitNotice.Store(true)
	if err := Init(addr, "", false, 1, nil); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	if earlyAdmitLegacyUntil.Load() != 0 || earlyAdmitNotice.Load() {
		t.Fatal("Init did not reset early admission")
	}
	if got := time.Duration(earlyAdmitProbeNanos.Load()); got != probe {
		t.Fatalf("Init changed the probe timeout to %s", got)
	}

	_, err, elapsed = admitFor(t, nil)
	if !errors.Is(err, ErrEarlyAdmitUnavailable) {
		t.Fatalf("Admit after reload error = %v, want ErrEarlyAdmitUnavailable", err)
	}
	if elapsed < 200*time.Millisecond || elapsed >= time.Second {
		t.Fatalf("Admit after reload took %s, want a fresh %s probe", elapsed, probe)
	}
}

type headerFirstProxy struct {
	stubProxy
}

func (p *headerFirstProxy) Proxy(stream proto.Proxy_ProxyServer) error {
	if err := stream.SendHeader(metadata.Pairs("x-spaceship-admitted", "1")); err != nil {
		return err
	}
	return p.stubProxy.Proxy(stream)
}

func assertProxyStillWorks(t *testing.T) {
	t.Helper()
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	localAddr := make(chan string, 1)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	payload := []byte("after-legacy-probe")
	if err := c.Proxy(ctx, "127.0.0.1:9", localAddr, &out, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Proxy after legacy probe: %v", err)
	}
}

func TestAdmitEarlyHeaderReturnsSession(t *testing.T) {
	const probe = 300 * time.Millisecond
	addr := startProxyServer(t, &headerFirstProxy{})
	prepareAdmitPool(t, addr, probe)

	session, err, elapsed := admitFor(t, nil)
	if err != nil {
		t.Fatalf("Admit error = %v after %s", err, elapsed)
	}
	if session == nil {
		t.Fatal("Admit returned a nil session")
	}
	if elapsed >= time.Second {
		t.Fatalf("Admit took %s, want a quick admission", elapsed)
	}
	if _, _, load := GetConnectionSummary(); load != 1 {
		t.Fatalf("load with open session = %d, want 1", load)
	}
	if earlyAdmitLegacyUntil.Load() != 0 {
		t.Fatal("successful admission was cached as legacy")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, load := GetConnectionSummary(); load != 0 {
		t.Fatalf("load after Close = %d, want 0", load)
	}
}

type statusProxy struct {
	proto.UnimplementedProxyServer
	code codes.Code
	msg  string
}

func (s *statusProxy) Proxy(proto.Proxy_ProxyServer) error {
	return status.Error(s.code, s.msg)
}

func TestAdmitFastErrorDoesNotMarkLegacy(t *testing.T) {
	const probe = 300 * time.Millisecond
	addr := startProxyServer(t, &statusProxy{
		code: codes.ResourceExhausted,
		msg:  "proxy: session capacity exhausted",
	})
	prepareAdmitPool(t, addr, probe)

	for i := 0; i < 2; i++ {
		_, err, elapsed := admitFor(t, nil)
		if errors.Is(err, ErrEarlyAdmitUnavailable) {
			t.Fatalf("call %d treated a fast RPC error as legacy admission", i+1)
		}
		if status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("call %d error = %v, want ResourceExhausted", i+1, err)
		}
		if elapsed >= 200*time.Millisecond {
			t.Fatalf("call %d took %s, want a fast RPC error", i+1, elapsed)
		}
		if earlyAdmitLegacyUntil.Load() != 0 || earlyAdmitNotice.Load() {
			t.Fatal("fast RPC error marked the server legacy")
		}
		if _, _, load := GetConnectionSummary(); load != 0 {
			t.Fatalf("load after call %d = %d, want 0", i+1, load)
		}
	}
}

func TestAdmitParentCancelDoesNotMarkLegacy(t *testing.T) {
	const probe = 800 * time.Millisecond
	addr := startStubProxy(t)
	prepareAdmitPool(t, addr, probe)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err, elapsed := admitFor(t, ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("already-canceled Admit error = %v, want context.Canceled", err)
	}
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("already-canceled Admit took %s", elapsed)
	}
	if earlyAdmitLegacyUntil.Load() != 0 || earlyAdmitNotice.Load() {
		t.Fatal("already-canceled Admit marked the server legacy")
	}

	ctx, cancel = context.WithCancel(context.Background())
	t.Cleanup(cancel)
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err, elapsed = admitFor(t, ctx)
	if errors.Is(err, ErrEarlyAdmitUnavailable) {
		t.Fatalf("parent cancel treated as legacy after %s", elapsed)
	}
	if !errors.Is(err, context.Canceled) && status.Code(err) != codes.Canceled {
		t.Fatalf("Admit error = %v, want context canceled", err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("canceled Admit took %s, probe is %s", elapsed, probe)
	}
	if earlyAdmitLegacyUntil.Load() != 0 || earlyAdmitNotice.Load() {
		t.Fatal("parent cancel marked the server legacy")
	}
	if _, _, load := GetConnectionSummary(); load != 0 {
		t.Fatalf("load after canceled Admit = %d, want 0", load)
	}
}
