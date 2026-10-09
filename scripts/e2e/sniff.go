//go:build unix

package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/sniff"
	"github.com/miekg/dns"
)

// runSniffSuite connects to a poisoned IP and sends a TLS ClientHello. The
// client route is proxy, so the recovered name is what the server dials, and
// the server's resolver — not the IP the client supplied — chooses the origin.
func runSniffSuite(bin, workDir string) {
	fmt.Println("\n-- sniffed name re-lookup --")

	dir := workDir + "/sniff"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		check("sniff/workdir", err, "")
		return
	}

	echo, err := startEchoServer()
	if err != nil {
		check("sniff/echo starts", err, "")
		return
	}
	defer echo.close()
	_, echoPort, err := splitPort(echo.addr)
	if err != nil {
		check("sniff/echo starts", err, "")
		return
	}

	stub, err := startNamedResolver("sniff.example.", "127.0.0.1")
	if err != nil {
		check("sniff/resolver starts", err, "")
		return
	}
	defer stub.close()

	rpcPort, _ := freePort()
	socksPort, _ := freePort()
	rpcAddr := fmt.Sprintf("127.0.0.1:%d", rpcPort)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	const uuid = "77777777-7777-7777-7777-777777777777"

	serverCfg := fmt.Sprintf(`{
  "role": "server",
  "listen": %q,
  "users": [{"uuid": %q}],
  "dns": {"type": "common", "server": %q}
}`, rpcAddr, uuid, stub.addr)
	clientCfg := fmt.Sprintf(`{
  "role": "client",
  "server_addr": %q,
  "uuid": %q,
  "listen_socks": %q,
  "mux": 1,
  "route": [
    {"src": ["sniff.example"], "dst": "proxy", "type": "exact"}
  ]
}`, rpcAddr, uuid, socksAddr)

	serverPath, clientPath := dir+"/server.json", dir+"/client.json"
	if err := os.WriteFile(serverPath, []byte(serverCfg), 0o600); err != nil {
		check("sniff/write configs", err, "")
		return
	}
	if err := os.WriteFile(clientPath, []byte(clientCfg), 0o600); err != nil {
		check("sniff/write configs", err, "")
		return
	}

	server, err := startSpaceship(bin, "sniff-server", serverPath, dir)
	if err != nil {
		check("sniff/stack starts", err, "")
		return
	}
	defer server.shutdown()
	if err := waitDialable(rpcAddr, 10*time.Second); err != nil {
		check("sniff/stack starts", fmt.Errorf("server: %w\n%s", err, server.tail(15)), "")
		return
	}
	client, err := startSpaceship(bin, "sniff-client", clientPath, dir)
	if err != nil {
		check("sniff/stack starts", err, "")
		return
	}
	defer client.shutdown()
	if err := waitDialable(socksAddr, 10*time.Second); err != nil {
		check("sniff/stack starts", fmt.Errorf("client: %w\n%s", err, client.tail(20)), "")
		return
	}
	check("sniff/stack starts", nil, "")

	// 127.0.0.2 is loopback but not the address the echo server bound. Dialing
	// the client-supplied IP refuses the connection. The server resolver
	// answers sniff.example with 127.0.0.1, which is the echo server.
	poisoned := fmt.Sprintf("127.0.0.2:%d", echoPort)
	conn, err := socks5Connect(socksAddr, poisoned, "", "")
	if err != nil {
		check("sniff/https reaches the server-resolved address", err, client.tail(20))
		return
	}
	defer func() { _ = conn.Close() }()

	hello := sniff.BuildClientHello("sniff.example")
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(hello); err != nil {
		check("sniff/https reaches the server-resolved address", err, client.tail(20))
		return
	}
	got := make([]byte, len(hello))
	if _, err := io.ReadFull(conn, got); err != nil {
		check("sniff/https reaches the server-resolved address",
			fmt.Errorf("read echo: %w\nclient:\n%s\nserver:\n%s", err, client.tail(25), server.tail(20)), "")
		return
	}
	if string(got) != string(hello) {
		check("sniff/https reaches the server-resolved address", fmt.Errorf("echo mismatch"), "")
		return
	}
	checkf("sniff/https reaches the server-resolved address", nil, "ClientHello echoed from 127.0.0.1 via sniff.example")

	if n := stub.count("sniff.example."); n == 0 {
		check("sniff/server resolver answered the recovered name",
			fmt.Errorf("stub resolver saw no query for sniff.example"), server.tail(20))
		return
	}
	checkf("sniff/server resolver answered the recovered name", nil, "A 127.0.0.1")
}

type namedResolver struct {
	srv  *dns.Server
	addr string
	name string
	mu   sync.Mutex
	hits map[string]int
}

func startNamedResolver(name, answer string) (*namedResolver, error) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &namedResolver{addr: pc.LocalAddr().String(), name: name, hits: map[string]int{}}
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(req)
		if len(req.Question) == 1 {
			q := req.Question[0]
			r.mu.Lock()
			r.hits[q.Name]++
			r.mu.Unlock()
			if q.Qtype == dns.TypeA {
				rr, err := dns.NewRR(q.Name + " 60 IN A " + answer)
				if err == nil {
					m.Answer = append(m.Answer, rr)
				}
			}
		}
		_ = w.WriteMsg(m)
	})
	r.srv = &dns.Server{PacketConn: pc, Handler: mux}
	go func() { _ = r.srv.ActivateAndServe() }()
	return r, nil
}

func (r *namedResolver) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[name]
}

func (r *namedResolver) close() { _ = r.srv.Shutdown() }
