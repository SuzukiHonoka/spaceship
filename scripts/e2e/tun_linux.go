//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const tunChildEnv = "SPACESHIP_E2E_TUN_CHILD"

func runTUNChildIfRequested(bin, workDir string) bool {
	if os.Getenv(tunChildEnv) != "1" {
		return false
	}
	runTUNChecks(bin, workDir)
	return true
}

// runTUNSuite covers the TUN front end's process-level wiring: a config file
// must produce a real kernel interface, and stopping the binary must take it
// away again.
//
// The data path is deliberately not driven here. Reaching an echo server
// through the interface needs a destination that is routable into the TUN but
// not locally, which means a second namespace and a veth pair; the in-tree
// integration test already drives packets through gVisor against a real device.
// What no in-process test can reach is this: config parsing, launcher ordering,
// device creation under a real binary, and teardown on SIGTERM.
func runTUNSuite(bin, workDir string) {
	if os.Getenv(tunChildEnv) == "1" {
		return
	}
	if os.Geteuid() != 0 {
		skip("tun/device lifecycle", "needs root for /dev/net/tun and netns")
		return
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		skip("tun/device lifecycle", "/dev/net/tun unavailable")
		return
	}
	for _, tool := range []string{"unshare", "ip"} {
		if _, err := exec.LookPath(tool); err != nil {
			skip("tun/device lifecycle", tool+" not found")
			return
		}
	}

	fmt.Println("\n-- tun front end --")
	cmd := exec.Command("unshare", "--net", "--fork", "--",
		os.Args[0], bin, workDir+"/tun")
	cmd.Env = append(os.Environ(), tunChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		check("tun/device lifecycle", fmt.Errorf("%w\n%s", err, out), "")
		return
	}
	fmt.Print(string(out))
	checkf("tun/device lifecycle", nil, "interface created from config and removed on stop")
}

func runTUNChecks(bin, workDir string) {
	fail := func(format string, args ...any) {
		fmt.Printf("  tun child: "+format+"\n", args...)
		os.Exit(1)
	}

	if err := os.MkdirAll(workDir, 0o755); err != nil {
		fail("workdir: %v", err)
	}
	if err := exec.Command("ip", "link", "set", "lo", "up").Run(); err != nil {
		fail("bring lo up: %v", err)
	}

	rpcPort, _ := freePort()
	rpcAddr := fmt.Sprintf("127.0.0.1:%d", rpcPort)
	const uuid = "66666666-6666-6666-6666-666666666666"
	const ifName = "ss-e2e0"

	serverCfg := fmt.Sprintf(`{
  "role": "server",
  "listen": %q,
  "users": [{"uuid": %q}]
}`, rpcAddr, uuid)
	// A small connection ceiling keeps the required RPC pool at one connection.
	clientCfg := fmt.Sprintf(`{
  "role": "client",
  "server_addr": %q,
  "uuid": %q,
  "tun": {
    "name": %q,
    "mtu": 1400,
    "max_connections": 32,
    "max_pending_connections": 8,
    "dns_hijack": {"enabled": true, "max_in_flight": 8}
  }
}`, rpcAddr, uuid, ifName)

	serverPath, clientPath := workDir+"/server.json", workDir+"/client.json"
	if err := os.WriteFile(serverPath, []byte(serverCfg), 0o600); err != nil {
		fail("write server config: %v", err)
	}
	if err := os.WriteFile(clientPath, []byte(clientCfg), 0o600); err != nil {
		fail("write client config: %v", err)
	}

	server, err := startSpaceship(bin, "tun-server", serverPath, workDir)
	if err != nil {
		fail("start server: %v", err)
	}
	defer server.shutdown()
	if err := waitDialable(rpcAddr, 10*time.Second); err != nil {
		fail("server not listening: %v\n%s", err, server.tail(20))
	}

	client, err := startSpaceship(bin, "tun-client", clientPath, workDir)
	if err != nil {
		fail("start client: %v", err)
	}
	if err := waitInterface(ifName, 15*time.Second); err != nil {
		client.kill()
		fail("interface never appeared: %v\nclient log:\n%s", err, client.tail(25))
	}
	fmt.Printf("  tun child: %s created from config\n", ifName)

	// The device must be up and carry the configured MTU, which proves the
	// launcher configured it rather than merely opening /dev/net/tun.
	out, err := exec.Command("ip", "-o", "link", "show", ifName).Output()
	if err != nil {
		client.kill()
		fail("ip link show: %v", err)
	}
	line := string(out)
	if !strings.Contains(line, "mtu 1400") {
		client.kill()
		fail("expected mtu 1400 in %q", strings.TrimSpace(line))
	}
	if !strings.Contains(line, "UP") {
		client.kill()
		fail("interface is not UP: %q", strings.TrimSpace(line))
	}
	fmt.Println("  tun child: interface is UP with the configured MTU")

	// Stopping the binary must release the device, or a restart would collide
	// with the interface its predecessor left behind.
	if _, err := client.stop(10 * time.Second); err != nil {
		fail("client did not stop: %v\n%s", err, client.tail(25))
	}
	if err := waitInterfaceGone(ifName, 10*time.Second); err != nil {
		fail("interface outlived the process: %v", err)
	}
	fmt.Println("  tun child: interface removed when the process stopped")
}

func waitInterface(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := exec.Command("ip", "link", "show", name).Run(); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", name)
}

func waitInterfaceGone(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := exec.Command("ip", "link", "show", name).Run(); err != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s still present", name)
}
