//go:build unix

package main

import (
	"fmt"
	"os"
	"time"
)

// runRouteSuite proves route rules actually select an egress. Every other suite
// runs on the default route, so a rule that silently failed to match — or an
// egress that quietly fell through to proxy — would go unnoticed.
func runRouteSuite(bin, workDir string) {
	fmt.Println("\n-- routing --")

	dir := workDir + "/routing"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		check("routing/workdir", err, "")
		return
	}

	echo, err := startEchoServer()
	if err != nil {
		check("routing/echo starts", err, "")
		return
	}
	defer echo.close()

	rpcPort, _ := freePort()
	socksPort, _ := freePort()
	rpcAddr := fmt.Sprintf("127.0.0.1:%d", rpcPort)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	const uuid = "44444444-4444-4444-4444-444444444444"

	serverCfg := fmt.Sprintf(`{
  "role": "server",
  "listen": %q,
  "users": [{"uuid": %q}]
}`, rpcAddr, uuid)

	// The echo server is on loopback, so a block rule for 127.0.0.1 must refuse
	// it while the default route still carries everything else.
	clientCfg := fmt.Sprintf(`{
  "role": "client",
  "server_addr": %q,
  "uuid": %q,
  "listen_socks": %q,
  "mux": 1,
  "route": [
    {"src": ["127.0.0.1/32"], "dst": "block", "type": "cidr"},
    {"dst": "proxy", "type": "default"}
  ]
}`, rpcAddr, uuid, socksAddr)

	serverPath, clientPath := dir+"/server.json", dir+"/client.json"
	if err := os.WriteFile(serverPath, []byte(serverCfg), 0o600); err != nil {
		check("routing/write configs", err, "")
		return
	}
	if err := os.WriteFile(clientPath, []byte(clientCfg), 0o600); err != nil {
		check("routing/write configs", err, "")
		return
	}

	server, err := startSpaceship(bin, "routing-server", serverPath, dir)
	if err != nil {
		check("routing/stack starts", err, "")
		return
	}
	defer server.shutdown()
	if err := waitDialable(rpcAddr, 10*time.Second); err != nil {
		check("routing/stack starts", fmt.Errorf("server: %w\n%s", err, server.tail(15)), "")
		return
	}
	client, err := startSpaceship(bin, "routing-client", clientPath, dir)
	if err != nil {
		check("routing/stack starts", err, "")
		return
	}
	defer client.shutdown()
	if err := waitDialable(socksAddr, 10*time.Second); err != nil {
		check("routing/stack starts", fmt.Errorf("client: %w\n%s", err, client.tail(15)), "")
		return
	}
	check("routing/stack starts", nil, "")

	conn, err := socks5Connect(socksAddr, echo.addr, "", "")
	if err == nil {
		_ = conn.Close()
		check("routing/block rule refuses a matching destination",
			fmt.Errorf("connection to %s succeeded despite a block route", echo.addr),
			client.tail(20))
		return
	}
	checkf("routing/block rule refuses a matching destination", nil, "refused: %v", err)
}
