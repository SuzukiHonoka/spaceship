package client

import (
	"context"
	"math/rand/v2"
	"time"

	"google.golang.org/grpc/connectivity"
)

const (
	// warmStableAfter is how long a connection must stay ready before losing
	// it counts as an isolated failure rather than a path that keeps resetting
	// new connections.
	warmStableAfter = 30 * time.Second
	// warmSpread bounds the random delay before re-warming a stable
	// connection, so clients that lost the same server at the same moment do
	// not all reconnect in the same instant.
	warmSpread = 500 * time.Millisecond
	// warmBaseDelay and warmMaxDelay bound the backoff for connections that
	// drop again soon after becoming ready, or never become ready.
	warmBaseDelay = time.Second
	warmMaxDelay  = 30 * time.Second
)

// startKeepWarm reconnects w in the background whenever its transport drops
// to IDLE. grpc-go reconnects lazily: after a reset, the next session on that
// connection pays the TCP, TLS and HTTP/2 handshake, and a connection no
// session picks again stays down indefinitely while the pool reports it as a
// member. Call before w is published; Close stops the loop.
//
// idleTimeout is the channel's configured idle timeout (zero disables it). A
// connection that went idle because it was unused for that long is left idle
// as configured; the next checkout reconnects it.
func (w *ConnWrapper) startKeepWarm(idleTimeout time.Duration) {
	if w == nil || w.ClientConn == nil || w.stopWarm != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.stopWarm = cancel
	w.touch()
	go w.keepWarm(ctx, idleTimeout)
}

func (w *ConnWrapper) keepWarm(ctx context.Context, idleTimeout time.Duration) {
	var (
		readySince time.Time
		delay      time.Duration
	)
	state := w.GetState()
	for {
		switch state {
		case connectivity.Shutdown:
			return
		case connectivity.Ready:
			readySince = time.Now()
		case connectivity.Idle:
			if w.idleByConfiguration(idleTimeout) {
				break
			}
			delay = nextWarmDelay(delay, readySince)
			readySince = time.Time{}
			if !sleepContext(ctx, delay) {
				return
			}
			// A no-op if a checkout already started reconnecting.
			w.Connect()
		}
		if !w.WaitForStateChange(ctx, state) {
			return
		}
		state = w.GetState()
	}
}

// idleByConfiguration reports whether the channel most likely entered IDLE
// because nothing used it for its configured idle timeout, rather than because
// its transport was lost.
func (w *ConnWrapper) idleByConfiguration(idleTimeout time.Duration) bool {
	if idleTimeout <= 0 || w.InUse.Load() != 0 {
		return false
	}
	idleFor := time.Since(time.Unix(0, w.lastActive.Load()))
	return idleFor >= idleTimeout-time.Second
}

// nextWarmDelay returns how long to wait before re-warming a connection that
// dropped to IDLE. A connection that had been ready for warmStableAfter is
// reconnected almost at once; one that dropped again soon after connecting,
// or never connected, backs off exponentially so a path that resets every new
// connection is not hammered.
func nextWarmDelay(previous time.Duration, readySince time.Time) time.Duration {
	if !readySince.IsZero() && time.Since(readySince) >= warmStableAfter {
		return jitter(warmSpread)
	}
	next := warmBaseDelay
	if previous >= warmBaseDelay {
		next = min(previous*2, warmMaxDelay)
	}
	return next/2 + jitter(next/2)
}

// jitter returns a random duration in [0, limit). It spreads reconnects, so it
// needs no cryptographic quality.
func jitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(limit))) // #nosec G404 -- reconnect spreading, not security
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
