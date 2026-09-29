package socks

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

func TestSOCKSHandshakeDeadline(t *testing.T) {
	s := New(context.Background(), &Config{HandshakeTimeout: 20 * time.Millisecond})
	defer func() { _ = s.Close() }()
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	done := make(chan error, 1)
	go func() { done <- s.ServeConn(server) }()
	select {
	case err := <-done:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("got %v, want handshake timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle handshake did not expire")
	}
}

func TestSOCKSAdmissionAndCloseDrainHandshakes(t *testing.T) {
	s := New(context.Background(), &Config{MaxConnections: 1})
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	if err := s.admit(server); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.serveConn(server) }()
	other, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	if err := s.ServeConn(other); !errors.Is(err, ErrConnectionLimit) {
		t.Fatalf("overload: %v", err)
	}
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not drain handshake")
	}
	<-done
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.connections) != 0 {
		t.Fatal("closed server retained connections")
	}
}

func TestSOCKSParentCancellationClosesHandshake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, nil)
	defer func() { _ = s.Close() }()
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	done := make(chan error, 1)
	go func() { done <- s.ServeConn(server) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handshake ignored parent cancellation")
	}
}

func TestSOCKSListenerRejectsExcessPendingHandshakes(t *testing.T) {
	s := New(context.Background(), &Config{MaxConnections: 1})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.listener = listener
	defer func() { _ = s.Close() }()
	served := make(chan error, 1)
	go func() { served <- s.Serve() }()
	first, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	// Complete only method negotiation: the first handler now waits for its
	// request and must already occupy the sole admission slot.
	_ = first.SetDeadline(time.Now().Add(time.Second))
	if _, err := first.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(first, make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	second, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	_, err = second.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("excess handshake accepted")
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("excess handshake stayed open")
	}
	_ = s.Close()
	select {
	case <-served:
	case <-time.After(time.Second):
		t.Fatal("Serve failed to stop")
	}
}

func TestUDPSettingsKeepLiveReservations(t *testing.T) {
	SetUDPSettings(UDPSettings{MaxAssociations: 2, MaxNATEntriesGlobal: 2})
	defer SetUDPSettings(UDPSettings{})
	associations, nat, _ := udpLimiters()
	for _, limiter := range []*udpResourceLimiter{associations, nat} {
		if !limiter.acquire("existing") {
			t.Fatal("initial reservation failed")
		}
	}
	SetUDPSettings(UDPSettings{MaxAssociations: 1, MaxNATEntriesGlobal: 1})
	newAssociations, newNAT, _ := udpLimiters()
	if newAssociations != associations || newNAT != nat {
		t.Fatal("replaced live limiter")
	}
	for _, limiter := range []*udpResourceLimiter{newAssociations, newNAT} {
		if limiter.acquire("new") {
			t.Fatal("reload forgot an existing reservation")
		}
		limiter.release("existing")
		if !limiter.acquire("new") {
			t.Fatal("capacity did not return after release")
		}
		limiter.release("new")
	}
}

type cancellablePacketTransport struct {
	closeCountingTransport
	entered chan struct{}
}

func (p *cancellablePacketTransport) DialPacket(string, string) (net.PacketConn, error) {
	return nil, errors.New("legacy dial must not be used")
}
func (p *cancellablePacketTransport) DialPacketContext(ctx context.Context, _, _ string) (net.PacketConn, error) {
	close(p.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestUDPRelayCloseCancelsPendingSetup(t *testing.T) {
	nat := newUDPResourceLimiter(1, 1)
	relay, err := newUDPRelayWithListener(nil, nil, newUDPResourceLimiter(1, 1), nat, 1,
		func(string, string) (net.PacketConn, error) { return newBlockingPacketConn(), nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = relay.Close() }()
	route := &cancellablePacketTransport{entered: make(chan struct{})}
	relay.getRoute = func(string) (transport.Transport, error) { return route, nil }
	done := make(chan error, 1)
	go func() { _, err := relay.getOrCreateNAT("example.com:53", testClientAddr()); done <- err }()
	<-route.entered
	_ = relay.Close()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("packet setup ignored relay shutdown")
	}
	if !nat.acquire("replacement") {
		t.Fatal("canceled setup leaked NAT budget")
	}
	nat.release("replacement")
	if route.closeCount.Load() != 1 {
		t.Fatal("canceled setup leaked route")
	}
}
