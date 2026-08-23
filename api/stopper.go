package api

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"
	"time"
)

var ErrSignalArrived = errors.New("signal arrived")

// DefaultForceExitTimeout is the shutdown budget the CLI arms by default.
//
// Shutdown is deliberately not graceful: the listeners and every accepted
// socket are force-closed as soon as the signal arrives, so a healthy process
// is gone in milliseconds. The watchdog only exists so that a future blocking
// path — an uncancellable dial, a stuck target write, a grpc-go change — can
// never hold a `systemctl restart` hostage until its stop timeout.
const DefaultForceExitTimeout = 5 * time.Second

func (l *Launcher) listenSignal(ctx context.Context) error {
	sys := make(chan os.Signal, 1)
	signal.Notify(sys, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sys)
	select {
	case <-sys:
	case <-l.sigStop:
	case <-ctx.Done():
		return ctx.Err()
	}
	log.Println("signal arrived")
	l.armForceExit()
	return ErrSignalArrived
}

// SetForceExitTimeout bounds how long teardown may take after a stop signal.
// Once the budget expires the process dumps every goroutine stack and exits
// non-zero, so the supervisor can restart immediately and the offending stack
// lands in the journal.
//
// Zero disables the watchdog. It stays off unless a caller opts in, because
// killing the process is only ever correct for a dedicated binary, never for an
// embedder that merely hosts a Launcher.
func (l *Launcher) SetForceExitTimeout(timeout time.Duration) {
	l.forceExitTimeout = timeout
}

func (l *Launcher) armForceExit() {
	if l.forceExitTimeout <= 0 {
		return
	}
	l.forceExitMu.Lock()
	defer l.forceExitMu.Unlock()
	if l.forceExitTimer != nil {
		return
	}
	l.forceExitTimer = time.AfterFunc(l.forceExitTimeout, l.forceExit)
}

// disarmForceExit cancels a pending watchdog. Launch defers it so a library
// caller that keeps running after a stop never inherits a timer that would kill
// its process.
func (l *Launcher) disarmForceExit() {
	l.forceExitMu.Lock()
	defer l.forceExitMu.Unlock()
	if l.forceExitTimer != nil {
		l.forceExitTimer.Stop()
		l.forceExitTimer = nil
	}
}

func (l *Launcher) forceExit() {
	log.Printf("shutdown did not finish within %s, forcing exit; goroutine dump follows", l.forceExitTimeout)
	if profile := pprof.Lookup("goroutine"); profile != nil {
		_ = profile.WriteTo(log.Writer(), 1)
	}
	os.Exit(1)
}

func (l *Launcher) Stop() {
	l.stopOnce.Do(func() {
		close(l.sigStop)
	})
}
