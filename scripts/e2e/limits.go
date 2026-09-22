//go:build unix

package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

// runLimitSuite proves the server-side admission ceiling is real. A v2.1.7
// config inherits these limits implicitly, so a deployment can hit them without
// having configured anything — nothing else verifies they refuse work rather
// than silently queueing it.
func runLimitSuite(bin, workDir string) {
	fmt.Println("\n-- proxy session limits --")

	const maxConcurrent = 2
	dir := workDir + "/limits"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		check("limits/workdir", err, "")
		return
	}

	echo, err := startEchoServer()
	if err != nil {
		check("limits/echo starts", err, "")
		return
	}
	defer echo.close()

	rpcPort, _ := freePort()
	socksPort, _ := freePort()
	rpcAddr := fmt.Sprintf("127.0.0.1:%d", rpcPort)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	const uuid = "33333333-3333-3333-3333-333333333333"

	serverCfg := fmt.Sprintf(`{
  "role": "server",
  "listen": %q,
  "users": [{"uuid": %q}],
  "proxy_sessions": {"max_concurrent": %d}
}`, rpcAddr, uuid, maxConcurrent)
	clientCfg := fmt.Sprintf(`{
  "role": "client",
  "server_addr": %q,
  "uuid": %q,
  "listen_socks": %q,
  "mux": 1
}`, rpcAddr, uuid, socksAddr)

	serverPath, clientPath := dir+"/server.json", dir+"/client.json"
	if err := os.WriteFile(serverPath, []byte(serverCfg), 0o600); err != nil {
		check("limits/write configs", err, "")
		return
	}
	if err := os.WriteFile(clientPath, []byte(clientCfg), 0o600); err != nil {
		check("limits/write configs", err, "")
		return
	}

	server, err := startSpaceship(bin, "limits-server", serverPath, dir)
	if err != nil {
		check("limits/stack starts", err, "")
		return
	}
	defer server.shutdown()
	if err := waitDialable(rpcAddr, 10*time.Second); err != nil {
		check("limits/stack starts", fmt.Errorf("server: %w\n%s", err, server.tail(15)), "")
		return
	}
	client, err := startSpaceship(bin, "limits-client", clientPath, dir)
	if err != nil {
		check("limits/stack starts", err, "")
		return
	}
	defer client.shutdown()
	if err := waitDialable(socksAddr, 10*time.Second); err != nil {
		check("limits/stack starts", fmt.Errorf("client: %w\n%s", err, client.tail(15)), "")
		return
	}
	check("limits/stack starts", nil, "")

	// Hold every tunnel open so the sessions are concurrent, not sequential.
	var held []net.Conn
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()

	attempts := maxConcurrent + 3
	var refused int
	for range attempts {
		conn, err := socks5Connect(socksAddr, echo.addr, "", "")
		if err != nil {
			refused++
			continue
		}
		held = append(held, conn)
	}

	switch {
	case len(held) > maxConcurrent:
		check("limits/ceiling is enforced",
			fmt.Errorf("%d concurrent sessions established, ceiling is %d", len(held), maxConcurrent),
			server.tail(20))
	case refused == 0:
		check("limits/ceiling is enforced",
			fmt.Errorf("%d attempts beyond the ceiling all succeeded", attempts-maxConcurrent), "")
	default:
		checkf("limits/ceiling is enforced", nil,
			"%d established, %d refused beyond ceiling %d", len(held), refused, maxConcurrent)
	}

	// Capacity must come back once sessions end, or a burst would wedge the
	// server until restart.
	for _, c := range held {
		_ = c.Close()
	}
	held = nil
	var recovered error
	for i := range 20 {
		time.Sleep(100 * time.Millisecond)
		conn, err := socks5Connect(socksAddr, echo.addr, "", "")
		if err == nil {
			_ = conn.Close()
			recovered = nil
			break
		}
		recovered = fmt.Errorf("attempt %d: %w", i+1, err)
	}
	checkf("limits/capacity returns after sessions end", recovered, "new session admitted")
}
