//go:build unix

package main

import (
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
)

// shutdownBudget is what an operator should expect from `systemctl restart`.
// Teardown is not graceful, so anything approaching a second is a regression.
const shutdownBudget = 2 * time.Second

// runShutdownSuite covers the failure this release fixes: releases up to v2.1.7
// called grpc GracefulStop first, which only sends GOAWAY. A peer that is still
// connected but no longer reading never answers it, so shutdown blocked until a
// fallback timer fired.
func runShutdownSuite(bin, workDir string) {
	fmt.Println("\n-- shutdown under load --")

	s, err := buildStack(bin, workDir+"/shutdown", false, false)
	if err != nil {
		check("shutdown/stack starts", err, "")
		if s != nil {
			s.teardown()
		}
		return
	}
	defer s.teardown()

	silent, err := startSilentServer()
	if err != nil {
		check("shutdown/silent target starts", err, "")
		return
	}
	defer silent.close()

	// Open tunnels that stay established and idle: the state a real proxy fleet
	// is in at any moment.
	const tunnels = 40
	var held []net.Conn
	for i := range tunnels {
		c, err := socks5Connect(s.socks, silent.addr, "", "")
		if err != nil {
			check("shutdown/open tunnels", fmt.Errorf("tunnel %d: %w", i, err), s.client.tail(10))
			return
		}
		held = append(held, c)
	}
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for silent.count() < tunnels && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if silent.count() < tunnels {
		check("shutdown/tunnels established",
			fmt.Errorf("only %d/%d reached the target", silent.count(), tunnels), "")
		return
	}
	checkf("shutdown/tunnels established", nil, "%d live sessions", tunnels)

	// Freeze the client so it stops reading its socket. The peer stays
	// connected; only a force-close can end the session now.
	if err := s.client.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		check("shutdown/stall the peer", err, "")
		return
	}
	time.Sleep(500 * time.Millisecond)
	checkf("shutdown/stall the peer", nil, "client frozen mid-session")

	elapsed, err := s.server.stop(30 * time.Second)
	if err != nil {
		check("shutdown/server exits on SIGTERM", err, s.server.tail(40))
	} else if elapsed > shutdownBudget {
		check("shutdown/server exits on SIGTERM",
			fmt.Errorf("took %s, budget is %s", elapsed.Round(time.Millisecond), shutdownBudget),
			s.server.tail(20))
	} else {
		checkf("shutdown/server exits on SIGTERM", nil, "%s with %d stalled sessions",
			elapsed.Round(time.Millisecond), tunnels)
	}

	_ = s.client.cmd.Process.Signal(syscall.SIGCONT)
	elapsed, err = s.client.stop(30 * time.Second)
	if err != nil {
		check("shutdown/client exits on SIGTERM", err, s.client.tail(40))
	} else if elapsed > shutdownBudget {
		check("shutdown/client exits on SIGTERM",
			fmt.Errorf("took %s, budget is %s", elapsed.Round(time.Millisecond), shutdownBudget),
			s.client.tail(20))
	} else {
		checkf("shutdown/client exits on SIGTERM", nil, "%s", elapsed.Round(time.Millisecond))
	}

	runRestartCycle(bin, workDir)
	runWatchdogCheck(bin, workDir)
}

// runRestartCycle is the operator's actual workflow: restart the server while a
// client keeps running, then prove traffic flows again. It also proves the
// listening socket is released rather than left in a state that blocks rebind.
func runRestartCycle(bin, workDir string) {
	s, err := buildStack(bin, workDir+"/restart", false, false)
	if err != nil {
		check("restart/stack starts", err, "")
		if s != nil {
			s.teardown()
		}
		return
	}
	defer s.teardown()

	if err := roundTrip(s.socks, s.echo.addr, 64<<10, "", ""); err != nil {
		check("restart/traffic before restart", err, "")
		return
	}
	checkf("restart/traffic before restart", nil, "sha256 matched")

	serverCfg := workDir + "/restart/h2c/server.json"
	elapsed, err := s.server.stop(30 * time.Second)
	if err != nil {
		check("restart/server stops", err, s.server.tail(40))
		return
	}
	checkf("restart/server stops", nil, "%s", elapsed.Round(time.Millisecond))

	restarted, err := startSpaceship(bin, "restarted-server", serverCfg, workDir+"/restart/h2c")
	if err != nil {
		check("restart/server rebinds its port", err, "")
		return
	}
	s.server = restarted

	// The port must be free immediately; a lingering socket would fail the bind.
	addr, err := listenAddrFromConfig(serverCfg)
	if err != nil {
		check("restart/server rebinds its port", err, "")
		return
	}
	if err := waitDialable(addr, 10*time.Second); err != nil {
		check("restart/server rebinds its port", err, restarted.tail(20))
		return
	}
	checkf("restart/server rebinds its port", nil, "%s", addr)

	// The running client must recover on its own.
	var lastErr error
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if lastErr = roundTrip(s.socks, s.echo.addr, 64<<10, "", ""); lastErr == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if lastErr != nil {
		check("restart/client recovers without a restart", lastErr, s.client.tail(20))
		return
	}
	checkf("restart/client recovers without a restart", nil, "traffic resumed")
}

// runWatchdogCheck proves the new -stop-timeout guard stays out of the way on a
// healthy stop: exit status 0, no forced-exit line in the log.
func runWatchdogCheck(bin, workDir string) {
	s, err := buildStack(bin, workDir+"/watchdog", false, false)
	if err != nil {
		check("watchdog/stack starts", err, "")
		if s != nil {
			s.teardown()
		}
		return
	}
	defer s.teardown()

	elapsed, err := s.server.stop(30 * time.Second)
	if err != nil {
		check("watchdog/healthy stop is clean", err, s.server.tail(20))
		return
	}
	if s.server.cmd.ProcessState.ExitCode() != 0 {
		check("watchdog/healthy stop is clean",
			fmt.Errorf("exit code %d, want 0", s.server.cmd.ProcessState.ExitCode()),
			s.server.tail(20))
		return
	}
	if logged := s.server.tail(200); strings.Contains(logged, "forcing exit") {
		check("watchdog/healthy stop is clean",
			fmt.Errorf("watchdog fired on a healthy shutdown"), logged)
		return
	}
	checkf("watchdog/healthy stop is clean", nil, "exit 0 in %s, watchdog silent",
		elapsed.Round(time.Millisecond))
}
