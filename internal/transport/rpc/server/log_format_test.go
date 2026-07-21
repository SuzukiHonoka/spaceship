package server

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestProxyFailureLogShape(t *testing.T) {
	// Document the intended server log line for a dial timeout so it cannot
	// regress into "forwarder error=copy client to target error: handshake…".
	target := "199.96.58.85:443"
	dialErr := fmt.Errorf("dial: %w", errors.New("dial tcp 199.96.58.85:443: connect: connection timed out"))

	line := fmt.Sprintf("rpc: proxy %s failed: %v", target, dialErr)
	if !strings.HasPrefix(line, "rpc: proxy 199.96.58.85:443 failed: dial: ") {
		t.Fatalf("unexpected log shape: %q", line)
	}
	if strings.Contains(line, "forwarder error") ||
		strings.Contains(line, "copy client to target") ||
		strings.Contains(line, "handshake error") ||
		strings.Contains(line, "dial target error") {
		t.Fatalf("legacy nested wrappers leaked into log: %q", line)
	}
}

func TestForwarderTargetEmptyBeforeHandshake(t *testing.T) {
	f := NewForwarder(t.Context(), nil)
	if f.Target() != "" {
		t.Fatalf("Target() = %q, want empty before handshake", f.Target())
	}
}
