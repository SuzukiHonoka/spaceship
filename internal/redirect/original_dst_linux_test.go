//go:build linux

package redirect

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestOriginalDestinationRejectsNonTCPConnection(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer func() { _ = serverSide.Close() }()
	defer func() { _ = clientSide.Close() }()

	_, err := originalDestination(serverSide)
	if err == nil {
		t.Fatal("originalDestination() accepted a non-TCP connection")
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatalf("originalDestination() error = %v, Linux implementation was not selected", err)
	}
}

func TestListenAndServeRejectsInvalidAddress(t *testing.T) {
	s, err := New(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	err = s.ListenAndServe("not a valid listen address")
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("ListenAndServe() error = %v, want wrapped listen error", err)
	}
}

func TestListenAndServeStopsWhenContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s, err := New(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ListenAndServe("127.0.0.1:0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListenAndServe() error = %v, want context.Canceled", err)
	}
}
