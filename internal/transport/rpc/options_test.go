package rpc

import (
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

// TestPayloadBufferPoolSizesTierToBuffer guards the allocation fix behind
// payloadBufferPool.
//
// gRPC's default tiers jump 32KB → 1MB. A payload chunk is a full transport
// buffer plus protobuf framing, which lands just past 32KB, so with the default
// pool every in-flight message would take a 1MB slab. The pool must instead hand
// back a buffer proportional to the payload.
func TestPayloadBufferPoolSizesTierToBuffer(t *testing.T) {
	oldBuffer := transport.GetBufferSize()
	t.Cleanup(func() { transport.SetBufferSize(uint16(oldBuffer / 1024)) })

	for _, bufferKB := range []uint16{32, 64, 256} {
		transport.SetBufferSize(bufferKB)
		payload := transport.GetBufferSize() + 64 // a chunk plus a little framing

		buf := payloadBufferPool().Get(payload)
		got := cap(*buf)

		if got < payload {
			t.Errorf("buffer=%dK: Get(%d) cap = %d, smaller than requested", bufferKB, payload, got)
		}
		// Allow the tier to overshoot by the framing reserve, but nothing close
		// to the 1MB fallback tier the default pool would have used.
		if limit := payload + MessageFramingOverhead; got > limit {
			t.Errorf("buffer=%dK: Get(%d) cap = %d, want <= %d (fell into an oversized tier)",
				bufferKB, payload, got, limit)
		}
	}
}

// TestServerKeepaliveOmitsMaxConnectionAge is a regression guard: a non-zero
// MaxConnectionAge severs every proxied session still open at the age limit,
// regardless of activity. It is the single most damaging option to re-add to a
// tunnel, and nothing else in the suite would catch it.
func TestServerKeepaliveOmitsMaxConnectionAge(t *testing.T) {
	p := serverKeepaliveParams()

	if p.MaxConnectionAge != 0 {
		t.Errorf("MaxConnectionAge = %v, want 0 (infinite): a tunnel must not drop "+
			"long-lived proxied sessions on a timer", p.MaxConnectionAge)
	}
	if p.MaxConnectionAgeGrace != 0 {
		t.Errorf("MaxConnectionAgeGrace = %v, want 0: it is meaningless without "+
			"MaxConnectionAge", p.MaxConnectionAgeGrace)
	}
	if p.Time <= 0 || p.Timeout <= 0 {
		t.Errorf("keepalive Time=%v Timeout=%v, both must be positive or dead peers "+
			"are never detected", p.Time, p.Timeout)
	}
}

// TestKeepaliveNegotiation verifies the client pings less often than the server's
// enforcement minimum. Inverted, a server would GOAWAY its own clients for
// "too_many_pings" — a failure that only shows up under real traffic.
func TestKeepaliveNegotiation(t *testing.T) {
	c := clientKeepaliveParams()
	s := serverKeepaliveParams()

	if c.Time < keepaliveMinTime {
		t.Errorf("client ping interval %v is below the server's enforced minimum %v; "+
			"the server would terminate its own clients", c.Time, keepaliveMinTime)
	}
	if !c.PermitWithoutStream {
		t.Error("client PermitWithoutStream = false: an idle pooled connection would " +
			"never be probed, so a dead peer goes unnoticed until the next request")
	}
	if s.Time <= 0 {
		t.Error("server keepalive Time must be positive")
	}
}

// TestMessageSizeInvariants guards the relationship config validation relies on.
func TestMessageSizeInvariants(t *testing.T) {
	if MaxTransportBufferSize >= MaxMessageSize {
		t.Errorf("MaxTransportBufferSize %d must leave room under MaxMessageSize %d "+
			"for the protobuf envelope around a payload chunk",
			MaxTransportBufferSize, MaxMessageSize)
	}
	if MaxMessageSize-MaxTransportBufferSize < MessageFramingOverhead {
		t.Errorf("headroom %d is below MessageFramingOverhead %d",
			MaxMessageSize-MaxTransportBufferSize, MessageFramingOverhead)
	}
}

// TestDialOptionsConstructs is a smoke test: these options are built once at
// startup, so a panic here (a malformed buffer-pool tier, say) would be a
// process-level crash rather than a recoverable error.
func TestDialOptionsConstructs(t *testing.T) {
	oldBuffer := transport.GetBufferSize()
	t.Cleanup(func() { transport.SetBufferSize(uint16(oldBuffer / 1024)) })

	for _, bufferKB := range []uint16{1, 32, uint16(MaxTransportBufferSize / 1024)} {
		transport.SetBufferSize(bufferKB)
		if got := len(DialOptions()); got == 0 {
			t.Errorf("buffer=%dK: DialOptions() is empty", bufferKB)
		}
		if got := len(ServerOptions()); got == 0 {
			t.Errorf("buffer=%dK: ServerOptions() is empty", bufferKB)
		}
	}
}
