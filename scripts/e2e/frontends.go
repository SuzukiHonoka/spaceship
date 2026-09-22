//go:build unix

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// stubResolver is an upstream the spaceship server forwards DNS to, so an
// answer proves the whole chain ran rather than something local resolving it.
type stubResolver struct {
	srv  *dns.Server
	addr string
	hits int
	mu   sync.Mutex
}

const stubName = "e2e.test."
const stubAnswer = "203.0.113.77"

func startStubResolver() (*stubResolver, error) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &stubResolver{addr: pc.LocalAddr().String()}
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		r.mu.Lock()
		r.hits++
		r.mu.Unlock()
		m := new(dns.Msg)
		m.SetReply(req)
		if len(req.Question) == 1 && req.Question[0].Qtype == dns.TypeA {
			rr, err := dns.NewRR(req.Question[0].Name + " 60 IN A " + stubAnswer)
			if err == nil {
				m.Answer = append(m.Answer, rr)
			}
		}
		_ = w.WriteMsg(m)
	})
	r.srv = &dns.Server{PacketConn: pc, Handler: mux}
	go func() { _ = r.srv.ActivateAndServe() }()
	return r, nil
}

func (r *stubResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits
}

func (r *stubResolver) close() { _ = r.srv.Shutdown() }

// runDNSSuite covers the DNS front end end to end: a query entering the
// client's listen_dns port must travel the authenticated DnsExchange RPC and be
// answered by the resolver the *server* is configured with. Nothing else in the
// harness exercises internal/dns, internal/dnswire, or that RPC as real
// processes.
func runDNSSuite(bin, workDir string) {
	fmt.Println("\n-- dns front end --")

	dir := workDir + "/dns"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		check("dns/workdir", err, "")
		return
	}

	stub, err := startStubResolver()
	if err != nil {
		check("dns/stub resolver starts", err, "")
		return
	}
	defer stub.close()

	rpcPort, _ := freePort()
	dnsPort, _ := freePort()
	rpcAddr := fmt.Sprintf("127.0.0.1:%d", rpcPort)
	dnsAddr := fmt.Sprintf("127.0.0.1:%d", dnsPort)
	const uuid = "22222222-2222-2222-2222-222222222222"

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
  "listen_dns": %q,
  "mux": 1
}`, rpcAddr, uuid, dnsAddr)

	serverPath, clientPath := dir+"/server.json", dir+"/client.json"
	if err := os.WriteFile(serverPath, []byte(serverCfg), 0o600); err != nil {
		check("dns/write configs", err, "")
		return
	}
	if err := os.WriteFile(clientPath, []byte(clientCfg), 0o600); err != nil {
		check("dns/write configs", err, "")
		return
	}

	server, err := startSpaceship(bin, "dns-server", serverPath, dir)
	if err != nil {
		check("dns/stack starts", err, "")
		return
	}
	defer server.shutdown()
	if err := waitDialable(rpcAddr, 10*time.Second); err != nil {
		check("dns/stack starts", fmt.Errorf("server: %w\n%s", err, server.tail(15)), "")
		return
	}
	client, err := startSpaceship(bin, "dns-client", clientPath, dir)
	if err != nil {
		check("dns/stack starts", err, "")
		return
	}
	defer client.shutdown()
	if err := waitUDPAnswers(dnsAddr, 10*time.Second); err != nil {
		check("dns/stack starts", fmt.Errorf("client dns: %w\n%s", err, client.tail(20)), "")
		return
	}
	check("dns/stack starts", nil, "")

	answer, err := queryA(dnsAddr, stubName)
	switch {
	case err != nil:
		check("dns/query resolves through the tunnel", err, client.tail(20))
	case answer != stubAnswer:
		check("dns/query resolves through the tunnel",
			fmt.Errorf("answer = %s, want %s", answer, stubAnswer), "")
	default:
		checkf("dns/query resolves through the tunnel", nil, "A %s", answer)
	}

	if n := stub.count(); n == 0 {
		check("dns/server-side resolver was used",
			fmt.Errorf("stub resolver received no queries; the answer did not come from the server"), "")
	} else {
		checkf("dns/server-side resolver was used", nil, "%d upstream queries", n)
	}
}

// waitUDPAnswers waits until the DNS listener answers, which also proves the
// client finished wiring its RPC pool.
func waitUDPAnswers(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if _, err := queryA(addr, stubName); err == nil {
			return nil
		} else {
			last = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timeout")
	}
	return last
}

func queryA(server, name string) (string, error) {
	m := new(dns.Msg)
	m.SetQuestion(name, dns.TypeA)
	c := &dns.Client{Timeout: 3 * time.Second}
	resp, _, err := c.Exchange(m, server)
	if err != nil {
		return "", err
	}
	if resp.Rcode != dns.RcodeSuccess {
		return "", fmt.Errorf("rcode %s", dns.RcodeToString[resp.Rcode])
	}
	for _, rr := range resp.Answer {
		if a, ok := rr.(*dns.A); ok {
			return a.A.String(), nil
		}
	}
	return "", fmt.Errorf("no A record in answer")
}

// plainRoundTrip echoes a checksummed payload over an ordinary TCP connection,
// with no proxy protocol in front of it. Transparent capture is invisible to
// the client by definition, so the connection must be made exactly as an
// unaware application would make it.
func plainRoundTrip(target string, size int) error {
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		return err
	}
	want := sha256.Sum256(payload)

	c, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(60 * time.Second))

	got := sha256.New()
	var wg sync.WaitGroup
	var readErr error
	wg.Go(func() {
		if _, err := io.CopyN(got, c, int64(size)); err != nil {
			readErr = fmt.Errorf("read back: %w", err)
		}
	})
	if _, err := c.Write(payload); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	wg.Wait()
	if readErr != nil {
		return readErr
	}
	if !bytes.Equal(got.Sum(nil), want[:]) {
		return fmt.Errorf("payload corrupted: checksum mismatch over %d bytes", size)
	}
	return nil
}

// splitPort returns the numeric port of a host:port address.
func splitPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}
