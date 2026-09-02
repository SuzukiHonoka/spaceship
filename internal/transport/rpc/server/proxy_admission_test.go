package server

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func proxyTestAdmission(t *testing.T, maxConcurrent int) *admission {
	t.Helper()
	cfg, err := config.NormalizeProxySessions(&config.ProxySessions{
		MaxConcurrent:        maxConcurrent,
		MaxConcurrentPerUser: maxConcurrent,
		SessionsPerSecond:    1000,
		SessionsPerUser:      1000,
		Burst:                1000,
		BurstPerUser:         1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return newProxyAdmission(cfg, config.Users{{UUID: "test-user"}})
}

func TestProxyRejectsNilStream(t *testing.T) {
	err := new(Server).Proxy(nil)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Proxy(nil) error = %v, want InvalidArgument", err)
	}
}

func TestProxyTimesOutBeforeFirstMessage(t *testing.T) {
	ctx := t.Context()
	stream := &mockProxyServer{
		ctx:         ctx,
		received:    make(chan *proto.ProxySRC),
		recvStarted: make(chan struct{}),
	}
	server := &Server{
		Ctx:                   context.Background(),
		proxyAdmission:        proxyTestAdmission(t, 1),
		proxyHandshakeTimeout: 20 * time.Millisecond,
	}
	before := ProxySessionStatistics()
	err := server.Proxy(stream)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("Proxy() error = %v, want DeadlineExceeded", err)
	}
	after := ProxySessionStatistics()
	if after.RequestsTotal-before.RequestsTotal != 1 ||
		after.AdmittedTotal-before.AdmittedTotal != 1 ||
		after.HandshakeTimeoutsTotal-before.HandshakeTimeoutsTotal != 1 ||
		after.Active != before.Active {
		t.Fatalf("proxy counter deltas: before=%+v after=%+v", before, after)
	}
}

func TestProxyAdmissionBoundsStreamsAcrossConnections(t *testing.T) {
	server := &Server{
		Ctx:                   context.Background(),
		proxyAdmission:        proxyTestAdmission(t, 1),
		proxyHandshakeTimeout: time.Second,
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first := &mockProxyServer{
		ctx:         firstCtx,
		received:    make(chan *proto.ProxySRC),
		recvStarted: make(chan struct{}),
	}
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- server.Proxy(first)
	}()
	select {
	case <-first.recvStarted:
	case <-time.After(time.Second):
		t.Fatal("first Proxy call did not enter Recv")
	}

	secondCtx := t.Context()
	second := &mockProxyServer{
		ctx:      secondCtx,
		received: make(chan *proto.ProxySRC),
	}
	before := ProxySessionStatistics()
	err := server.Proxy(second)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second Proxy() error = %v, want ResourceExhausted", err)
	}
	after := ProxySessionStatistics()
	if after.RequestsTotal-before.RequestsTotal != 1 ||
		after.AdmittedTotal-before.AdmittedTotal != 0 ||
		after.RejectedGlobalConcurrencyTotal-before.RejectedGlobalConcurrencyTotal != 1 {
		t.Fatalf("proxy rejection deltas: before=%+v after=%+v", before, after)
	}

	cancelFirst()
	select {
	case err := <-firstResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first Proxy() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first Proxy call did not stop after stream cancellation")
	}
}

func TestReceiveProxyFirstMessage(t *testing.T) {
	ctx := t.Context()
	want := &proto.ProxySRC{
		HeaderOrPayload: &proto.ProxySRC_Header{
			Header: &proto.ProxySRC_ProxyHeader{Addr: "example.com:443"},
		},
	}
	stream := &mockProxyServer{
		ctx:      ctx,
		received: make(chan *proto.ProxySRC, 1),
	}
	stream.received <- want

	got, err := receiveProxyFirstMessage(ctx, stream, time.Second)
	if err != nil || got != want {
		t.Fatalf("receiveProxyFirstMessage() = (%p, %v), want (%p, nil)", got, err, want)
	}
}

func TestReceiveProxyFirstMessageRejectsInvalidAndCanceledStreams(t *testing.T) {
	t.Run("EOF", func(t *testing.T) {
		received := make(chan *proto.ProxySRC)
		close(received)
		_, err := receiveProxyFirstMessage(
			context.Background(),
			&mockProxyServer{ctx: context.Background(), received: received},
			time.Second,
		)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("receiveProxyFirstMessage() error = %v, want EOF", err)
		}
	})

	t.Run("nil message", func(t *testing.T) {
		received := make(chan *proto.ProxySRC, 1)
		received <- nil
		_, err := receiveProxyFirstMessage(
			context.Background(),
			&mockProxyServer{ctx: context.Background(), received: received},
			time.Second,
		)
		if err == nil {
			t.Fatal("receiveProxyFirstMessage() accepted a nil message")
		}
	})

	t.Run("context canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		stream := &mockProxyServer{
			ctx:      ctx,
			received: make(chan *proto.ProxySRC),
		}
		cancel()
		_, err := receiveProxyFirstMessage(ctx, stream, time.Second)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("receiveProxyFirstMessage() error = %v, want context.Canceled", err)
		}
	})
}

func TestRecordProxyAdmissionRejectionCounters(t *testing.T) {
	before := ProxySessionStatistics()
	for _, reason := range []admissionRejection{
		admissionRejectedGlobalConcurrency,
		admissionRejectedUserConcurrency,
		admissionRejectedGlobalRate,
		admissionRejectedUserRate,
	} {
		recordProxyAdmissionRejection(reason)
	}
	after := ProxySessionStatistics()
	if after.RejectedGlobalConcurrencyTotal-before.RejectedGlobalConcurrencyTotal != 1 ||
		after.RejectedPerUserConcurrencyTotal-before.RejectedPerUserConcurrencyTotal != 1 ||
		after.RejectedGlobalRateTotal-before.RejectedGlobalRateTotal != 1 ||
		after.RejectedPerUserRateTotal-before.RejectedPerUserRateTotal != 1 {
		t.Fatalf("rejection counter deltas: before=%+v after=%+v", before, after)
	}
}
