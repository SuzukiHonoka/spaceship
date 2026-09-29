package direct

import (
	"context"
	"errors"
	"testing"
)

func TestPacketDialHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &Direct{}
	conn, _, err := d.DialPacketTargetContext(ctx, "udp", "127.0.0.1:53")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("dial returned a socket after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}
}
