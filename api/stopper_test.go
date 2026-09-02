package api

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/pkg/config"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

// forceExitChildEnv marks the subprocess that is allowed to kill itself.
const forceExitChildEnv = "SPACESHIP_TEST_FORCE_EXIT_CHILD"

// armedForceExitTimer reports whether a watchdog is pending. The timers are
// deliberately long enough here that firing one would kill the test binary, so
// every assertion inspects the field instead of waiting.
func armedForceExitTimer(l *Launcher) bool {
	l.forceExitMu.Lock()
	defer l.forceExitMu.Unlock()
	return l.forceExitTimer != nil
}

func TestListenSignalArmsForceExit(t *testing.T) {
	l := NewLauncher()
	l.SetForceExitTimeout(time.Hour)

	errCh := make(chan error, 1)
	go func() { errCh <- l.listenSignal(context.Background()) }()
	l.Stop()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrSignalArrived) {
			t.Fatalf("listenSignal() error = %v, want ErrSignalArrived", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("listenSignal did not return after Stop")
	}

	if !armedForceExitTimer(l) {
		t.Fatal("stop signal did not arm the force-exit watchdog")
	}
	l.disarmForceExit()
}

func TestListenSignalWithoutTimeoutDoesNotArm(t *testing.T) {
	l := NewLauncher()

	errCh := make(chan error, 1)
	go func() { errCh <- l.listenSignal(context.Background()) }()
	l.Stop()
	<-errCh

	if armedForceExitTimer(l) {
		t.Fatal("zero timeout must leave the watchdog disabled")
	}
}

// TestListenSignalContextCancelDoesNotArm keeps the watchdog tied to an operator
// stop. A sibling listener failing the errgroup cancels this context, and that
// teardown is already bounded by the returning error.
func TestListenSignalContextCancelDoesNotArm(t *testing.T) {
	l := NewLauncher()
	l.SetForceExitTimeout(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.listenSignal(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("listenSignal() error = %v, want context.Canceled", err)
	}
	if armedForceExitTimer(l) {
		t.Fatal("context cancellation must not arm the watchdog")
	}
}

// TestLaunchDisarmsForceExit covers the embedder case: a library caller keeps
// running after Launch returns and must not inherit a timer that would take its
// process down.
func TestLaunchDisarmsForceExit(t *testing.T) {
	launcher := NewLauncher()
	launcher.SkipInternalLogging()
	launcher.SetForceExitTimeout(time.Hour)

	cfg := &config.MixedConfig{
		Role:   config.RoleServer,
		Client: &client.Client{},
		Server: &server.Server{
			Listen: "127.0.0.1:0",
			Users:  server.Users{{UUID: "disarm-user"}},
		},
	}

	done := make(chan error, 1)
	go func() { done <- launcher.Launch(cfg) }()

	time.Sleep(50 * time.Millisecond)
	launcher.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Launch() error after Stop() = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Launch did not return after Stop")
	}

	if armedForceExitTimer(launcher) {
		t.Fatal("Launch left the force-exit watchdog armed")
	}
}

// TestForceExitTerminatesProcess runs the watchdog for real in a subprocess:
// firing it in-process would take the test binary down with it.
func TestForceExitTerminatesProcess(t *testing.T) {
	if os.Getenv(forceExitChildEnv) == "1" {
		l := NewLauncher()
		l.SetForceExitTimeout(50 * time.Millisecond)
		l.armForceExit()
		// The watchdog fires from its own goroutine; block until it does.
		select {}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestForceExitTerminatesProcess$", "-test.v")
	cmd.Env = append(os.Environ(), forceExitChildEnv+"=1")
	output, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child exited with %v, want a non-zero status", err)
	}
	if code := exitErr.ExitCode(); code != 1 {
		t.Errorf("child exit code = %d, want 1", code)
	}
	if !bytes.Contains(output, []byte("forcing exit")) {
		t.Errorf("child did not log the forced exit; output:\n%s", output)
	}
	if !bytes.Contains(output, []byte("goroutine profile")) {
		t.Errorf("child did not dump goroutines; output:\n%s", output)
	}
}

// SetForceExitTimeout is exported, so it can be called while the watchdog is
// being armed from the signal goroutine. The budget must be synchronised with
// the arm and fire paths rather than relying on callers to sequence them.
func TestForceExitTimeoutIsRaceFree(t *testing.T) {
	l := NewLauncher()
	t.Cleanup(l.disarmForceExit)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 500 {
			// Large enough that the watchdog never actually fires here.
			l.SetForceExitTimeout(time.Duration(i+1) * time.Hour)
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			l.armForceExit()
			l.disarmForceExit()
		}
	}()
	wg.Wait()
}
