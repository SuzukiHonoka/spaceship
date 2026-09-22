//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

const redirectChildEnv = "SPACESHIP_E2E_REDIRECT_CHILD"

// runRedirectChildIfRequested short-circuits main when this process is the
// re-exec inside the network namespace. Without it the child would replay every
// other suite before reaching its own checks.
func runRedirectChildIfRequested(bin, workDir string) bool {
	if os.Getenv(redirectChildEnv) != "1" {
		return false
	}
	runRedirectChecks(bin, workDir)
	return true
}

// bypassMark is the mark spaceship applies to its own egress by default. The
// rules below exempt it; without that exemption the listener captures its own
// outbound connection and recurses until max_connections is exhausted.
const bypassMark = "0x5350/0xffffffff"

// runRedirectSuite drives transparent REDIRECT as real processes: a kernel
// netfilter rule captures a connection, the client recovers the original
// destination through SO_ORIGINAL_DST, and the tunnel carries it. The in-tree
// integration test covers the same kernel facilities in-process; this covers
// the seam that test cannot — config file, launcher wiring, and the bypass mark
// actually being applied to a running binary's egress.
//
// It runs in a disposable network namespace so no host rule is ever touched.
func runRedirectSuite(bin, workDir string) {
	if os.Geteuid() != 0 {
		skip("redirect/transparent capture", "needs root for netns and iptables")
		return
	}
	for _, tool := range []string{"unshare", "ip", "iptables"} {
		if _, err := exec.LookPath(tool); err != nil {
			skip("redirect/transparent capture", tool+" not found")
			return
		}
	}

	fmt.Println("\n-- transparent redirect --")
	cmd := exec.Command("unshare", "--net", "--fork", "--",
		os.Args[0], bin, workDir+"/redirect")
	cmd.Env = append(os.Environ(), redirectChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		check("redirect/transparent capture", fmt.Errorf("%w\n%s", err, out), "")
		return
	}
	// The child reports its own checks; surface them in this run's summary.
	fmt.Print(string(out))
	checkf("redirect/transparent capture", nil, "captured via SO_ORIGINAL_DST and proxied in a netns")
}

func runRedirectChecks(bin, workDir string) {
	fail := func(format string, args ...any) {
		fmt.Printf("  redirect child: "+format+"\n", args...)
		os.Exit(1)
	}

	if err := os.MkdirAll(workDir, 0o755); err != nil {
		fail("workdir: %v", err)
	}
	if err := exec.Command("ip", "link", "set", "lo", "up").Run(); err != nil {
		fail("bring lo up: %v", err)
	}

	echo, err := startEchoServer()
	if err != nil {
		fail("echo server: %v", err)
	}
	defer echo.close()
	_, echoPort, err := splitPort(echo.addr)
	if err != nil {
		fail("echo addr: %v", err)
	}

	rpcPort, _ := freePort()
	redirPort, _ := freePort()
	rpcAddr := fmt.Sprintf("127.0.0.1:%d", rpcPort)
	redirAddr := fmt.Sprintf("127.0.0.1:%d", redirPort)
	const uuid = "55555555-5555-5555-5555-555555555555"

	serverCfg := fmt.Sprintf(`{
  "role": "server",
  "listen": %q,
  "users": [{"uuid": %q}]
}`, rpcAddr, uuid)
	// Route captured traffic to direct egress rather than through the tunnel.
	//
	// Both roles share one namespace here, and only a client applies the bypass
	// mark — it is a client-side setting. So the *server* dialling the recovered
	// destination would match the REDIRECT rule below and feed the listener back
	// into itself. That is an artifact of colocating the roles; in a real
	// deployment the server is on another host and never traverses this chain.
	//
	// Direct egress keeps the mark exemption load-bearing: without it the
	// client's own outbound dial is captured and loops. What this exercises is
	// exactly what is unique to the redirect front end — accept, recover the
	// original destination, route it, and stay out of its own way. Everything
	// after routing is the shared tunnel path the other suites already cover.
	clientCfg := fmt.Sprintf(`{
  "role": "client",
  "server_addr": %q,
  "uuid": %q,
  "listen_redirect": %q,
  "mux": 1,
  "route": [{"dst": "direct", "type": "default"}]
}`, rpcAddr, uuid, redirAddr)

	serverPath, clientPath := workDir+"/server.json", workDir+"/client.json"
	if err := os.WriteFile(serverPath, []byte(serverCfg), 0o600); err != nil {
		fail("write server config: %v", err)
	}
	if err := os.WriteFile(clientPath, []byte(clientCfg), 0o600); err != nil {
		fail("write client config: %v", err)
	}

	server, err := startSpaceship(bin, "redirect-server", serverPath, workDir)
	if err != nil {
		fail("start server: %v", err)
	}
	defer server.shutdown()
	if err := waitDialable(rpcAddr, 10*time.Second); err != nil {
		fail("server not listening: %v\n%s", err, server.tail(20))
	}
	client, err := startSpaceship(bin, "redirect-client", clientPath, workDir)
	if err != nil {
		fail("start client: %v", err)
	}
	defer client.shutdown()
	if err := waitDialable(redirAddr, 10*time.Second); err != nil {
		fail("redirect listener not up: %v\n%s", err, client.tail(20))
	}

	// Exempt spaceship's own marked egress first. The client dials the recovered
	// destination itself, so without this the REDIRECT rule below captures that
	// dial and the listener feeds itself.
	rules := [][]string{
		{"-t", "nat", "-A", "OUTPUT", "-m", "mark", "--mark", bypassMark, "-j", "RETURN"},
		{"-t", "nat", "-A", "OUTPUT", "-p", "tcp", "-d", "127.0.0.1",
			"--dport", strconv.Itoa(echoPort), "-j", "REDIRECT", "--to-ports", strconv.Itoa(redirPort)},
	}
	for _, r := range rules {
		if out, err := exec.Command("iptables", r...).CombinedOutput(); err != nil {
			fail("iptables %v: %v\n%s", r, err, out)
		}
	}

	// Nothing marks this connection, so netfilter hands it to the redirect
	// listener, which must recover 127.0.0.1:echoPort and tunnel it.
	if err := plainRoundTrip(echo.addr, 256<<10); err != nil {
		fail("redirected round trip: %v\nclient log:\n%s\nserver log:\n%s",
			err, client.tail(25), server.tail(15))
	}
	fmt.Println("  redirect child: captured connection proxied to its original destination, sha256 matched")

	// A loop would have consumed the session limit rather than completing, but
	// check the listener is still healthy so a partial capture cannot pass.
	if err := plainRoundTrip(echo.addr, 64<<10); err != nil {
		fail("second redirected round trip: %v\n%s", err, client.tail(25))
	}
	fmt.Println("  redirect child: listener still healthy after a second capture")
}
