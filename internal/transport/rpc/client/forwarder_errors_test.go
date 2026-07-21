package client

import (
	"errors"
	"testing"
)

func TestServerHandshakeSentinelErrors(t *testing.T) {
	// Stable strings: these appear in HTTP/SOCKS logs and must stay readable.
	if got := errServerAckTimeout.Error(); got != "server ack timeout" {
		t.Fatalf("errServerAckTimeout = %q", got)
	}
	if got := errServerRejected.Error(); got != "server rejected connection" {
		t.Fatalf("errServerRejected = %q", got)
	}

	// Callers should be able to classify with errors.Is.
	if !errors.Is(errServerAckTimeout, errServerAckTimeout) {
		t.Fatal("errors.Is self-match failed for errServerAckTimeout")
	}
	wrapped := errors.Join(errors.New("outer"), errServerAckTimeout)
	if !errors.Is(wrapped, errServerAckTimeout) {
		t.Fatal("errors.Is did not unwrap errServerAckTimeout")
	}
}
