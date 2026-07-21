package api

import (
	"strings"
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

func TestTotalAndSpeed(t *testing.T) {
	// Seed global stats so Total/Speed have non-zero values.
	transport.GlobalStats.Add(transport.DirectionOut, 2048)
	transport.GlobalStats.Add(transport.DirectionIn, 4096)

	l := NewLauncher()
	total := l.Total()
	if len(total.BytesSent) != 8 || len(total.BytesReceived) != 8 {
		t.Fatalf("Total byte slices lengths = %d/%d, want 8/8",
			len(total.BytesSent), len(total.BytesReceived))
	}
	if total.bytesSentUint64 == 0 && total.bytesReceivedUint64 == 0 {
		t.Fatal("Total() returned zeros after seeding stats")
	}
	s := total.String()
	if !strings.Contains(s, "Total:") || !strings.Contains(s, "sent") {
		t.Fatalf("Total.String() = %q", s)
	}

	speed := l.Speed()
	// Speed may be zero depending on sample window, but String should format.
	ss := speed.String()
	if !strings.Contains(ss, "Speed:") || !strings.Contains(ss, "sent") {
		t.Fatalf("Speed.String() = %q", ss)
	}
}
