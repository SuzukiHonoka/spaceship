package rpc_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
)

func poolStates() string {
	s := ""
	for _, d := range client.GetConnectionDetails() {
		s += fmt.Sprintf("#%d:%s(load %d) ", d.ID, d.ConnectivityState, d.Load)
	}
	return s
}

// waitPool polls the pool until ok accepts its connections, failing after
// timeout.
func waitPool(t *testing.T, timeout time.Duration, what string, ok func([]client.ConnectionDetail) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if ok(client.GetConnectionDetails()) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pool never %s within %s: %s", what, timeout, poolStates())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func allReady(want int) func([]client.ConnectionDetail) bool {
	return func(details []client.ConnectionDetail) bool {
		ready := 0
		for _, d := range details {
			if d.ConnectivityState == "READY" {
				ready++
			}
		}
		return len(details) == want && ready == want
	}
}

func someNotReady(details []client.ConnectionDetail) bool {
	for _, d := range details {
		if d.ConnectivityState != "READY" {
			return true
		}
	}
	return false
}

// resetPreferredConnection starts a two-connection pool behind a relay and
// resets the connection the pool picks first, returning once the client has
// noticed.
func resetPreferredConnection(t *testing.T) {
	t.Helper()
	routeAllDirect(t)
	r := startTestRelay(t, startProxyServer(t))
	connectClientWithMux(t, r.addr(), 2)
	waitPool(t, 5*time.Second, "became ready", allReady(2))
	responder := startResponder(t, "hello", false, false)
	r.resetBusiest(t, func() {
		_, app, done := proxyThroughTunnel(t, responder, true)
		if got, err := readEnd(app); err != nil || got != "hello" {
			t.Fatalf("session read %q, %v", got, err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
	waitPool(t, 5*time.Second, "noticed the reset", someNotReady)
}

// A reset connection loses its sessions, so it reads as the least loaded.
// The next checkout must still go to a ready connection instead of paying
// the reset one's reconnect.
func TestEndToEnd_PoolPrefersReadyOverResetConnection(t *testing.T) {
	resetPreferredConnection(t)
	c, err := client.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for _, d := range client.GetConnectionDetails() {
		if d.Load == 1 && d.ConnectivityState != "READY" {
			t.Fatalf("checkout went to a %s connection while a ready one was free: %s",
				d.ConnectivityState, poolStates())
		}
	}
}

// grpc-go reconnects lazily, so without keep-warm a reset connection that no
// session picks stays down indefinitely.
func TestEndToEnd_PoolReconnectsResetConnectionInBackground(t *testing.T) {
	resetPreferredConnection(t)
	waitPool(t, 5*time.Second, "re-warmed the reset connection", allReady(2))
}

// A channel that went idle because nothing used it for its configured idle
// timeout must stay idle: keep-warm only replaces lost transports.
func TestEndToEnd_PoolKeepsConfiguredIdleness(t *testing.T) {
	previous := transport.GetIdleTimeout()
	transport.SetIdleTimeout(time.Second)
	t.Cleanup(func() { transport.SetIdleTimeout(previous) })

	routeAllDirect(t)
	connectClientWithMux(t, startProxyServer(t), 2)
	waitPool(t, 5*time.Second, "went idle", func(details []client.ConnectionDetail) bool {
		for _, d := range details {
			if d.ConnectivityState != "IDLE" {
				return false
			}
		}
		return len(details) == 2
	})
	time.Sleep(3 * time.Second)
	for _, d := range client.GetConnectionDetails() {
		if d.ConnectivityState != "IDLE" {
			t.Fatalf("keep-warm reconnected a connection idled by configuration: %s", poolStates())
		}
	}
}
