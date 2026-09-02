// Command e2e drives spaceship end to end as real processes: a server binary
// and a client binary talking over a real gRPC tunnel, exercised through the
// front ends an operator actually uses.
//
// The in-tree e2e package cannot do this — the router is process-global, so one
// test process cannot route a destination to the proxy on the client side and
// to direct on the server side. Separate processes is the only way to cover
// both legs at once.
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	failures int
	results  []string
)

func check(name string, err error, detail string) {
	if err != nil {
		failures++
		results = append(results, fmt.Sprintf("  FAIL  %s\n        %v", name, err))
		if detail != "" {
			results = append(results, detail)
		}
		fmt.Printf("  FAIL  %s: %v\n", name, err)
		return
	}
	results = append(results, fmt.Sprintf("  PASS  %s", name))
	fmt.Printf("  PASS  %s\n", name)
}

func checkf(name string, err error, format string, args ...any) {
	if err == nil {
		results = append(results, fmt.Sprintf("  PASS  %s (%s)", name, fmt.Sprintf(format, args...)))
		fmt.Printf("  PASS  %s (%s)\n", name, fmt.Sprintf(format, args...))
		return
	}
	check(name, err, "")
}

func main() {
	bin := os.Args[1]
	workDir := os.Args[2]

	fmt.Println("== spaceship end-to-end ==")
	fmt.Printf("binary: %s\n\n", bin)

	out, err := exec.Command(bin, "-v").Output()
	check("version reports", err, "")
	fmt.Printf("  version: %s\n", strings.TrimSpace(string(out)))

	runSuite(bin, workDir, false)
	runSuite(bin, workDir, true)
	runAuthSuite(bin, workDir)
	runShutdownSuite(bin, workDir)

	fmt.Println("\n== summary ==")
	for _, r := range results {
		fmt.Println(r)
	}
	if failures > 0 {
		fmt.Printf("\n%d FAILURE(S)\n", failures)
		os.Exit(1)
	}
	fmt.Printf("\nall %d checks passed\n", len(results))
}

type stack struct {
	server *proc
	client *proc
	socks  string
	http   string
	echo   *echoServer
	udp    string
	closes []func()
}

func (s *stack) teardown() {
	if s.client != nil {
		s.client.kill()
	}
	if s.server != nil {
		s.server.kill()
	}
	for _, f := range s.closes {
		f()
	}
}

// buildStack starts a server and client pair wired to fresh loopback ports.
func buildStack(bin, workDir string, useTLS bool, basicAuth bool) (*stack, error) {
	label := "h2c"
	if useTLS {
		label = "tls"
	}
	dir := workDir + "/" + label
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	s := &stack{}
	echo, err := startEchoServer()
	if err != nil {
		return nil, err
	}
	s.echo = echo
	s.closes = append(s.closes, echo.close)

	udpAddr, closeUDP, err := startUDPEcho()
	if err != nil {
		return nil, err
	}
	s.udp = udpAddr
	s.closes = append(s.closes, closeUDP)

	rpcPort, _ := freePort()
	socksPort, _ := freePort()
	httpPort, _ := freePort()
	rpcAddr := fmt.Sprintf("127.0.0.1:%d", rpcPort)
	s.socks = fmt.Sprintf("127.0.0.1:%d", socksPort)
	s.http = fmt.Sprintf("127.0.0.1:%d", httpPort)

	const uuid = "11111111-1111-1111-1111-111111111111"

	serverCfg := fmt.Sprintf(`{
  "role": "server",
  "listen": %q,
  "users": [{"uuid": %q}]%s
}`, rpcAddr, uuid, "")

	clientTLS := ""
	if useTLS {
		certPath, keyPath, err := writeSelfSigned(dir)
		if err != nil {
			return nil, err
		}
		serverCfg = fmt.Sprintf(`{
  "role": "server",
  "listen": %q,
  "users": [{"uuid": %q}],
  "ssl": {"cert": %q, "key": %q}
}`, rpcAddr, uuid, certPath, keyPath)
		clientTLS = fmt.Sprintf(`,
  "tls": true,
  "host": "spaceship.test",
  "cas": [%q]`, certPath)
	}

	auth := ""
	if basicAuth {
		auth = `,
  "basic_auth": ["e2e:secret"]`
	}

	clientCfg := fmt.Sprintf(`{
  "role": "client",
  "server_addr": %q,
  "uuid": %q,
  "listen_socks": %q,
  "listen_http": %q,
  "mux": 2%s%s
}`, rpcAddr, uuid, s.socks, s.http, clientTLS, auth)

	serverPath := dir + "/server.json"
	clientPath := dir + "/client.json"
	if err := os.WriteFile(serverPath, []byte(serverCfg), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(clientPath, []byte(clientCfg), 0o600); err != nil {
		return nil, err
	}

	s.server, err = startSpaceship(bin, label+"-server", serverPath, dir)
	if err != nil {
		return nil, err
	}
	if err := waitDialable(rpcAddr, 10*time.Second); err != nil {
		return nil, fmt.Errorf("server: %w\n%s", err, s.server.tail(15))
	}
	s.client, err = startSpaceship(bin, label+"-client", clientPath, dir)
	if err != nil {
		return nil, err
	}
	if err := waitDialable(s.socks, 10*time.Second); err != nil {
		return nil, fmt.Errorf("client socks: %w\n%s", err, s.client.tail(15))
	}
	if err := waitDialable(s.http, 10*time.Second); err != nil {
		return nil, fmt.Errorf("client http: %w\n%s", err, s.client.tail(15))
	}
	return s, nil
}

func runSuite(bin, workDir string, useTLS bool) {
	label := "h2c"
	if useTLS {
		label = "TLS"
	}
	fmt.Printf("\n-- transport: %s --\n", label)

	s, err := buildStack(bin, workDir, useTLS, false)
	if err != nil {
		check(label+"/stack starts", err, "")
		if s != nil {
			s.teardown()
		}
		return
	}
	defer s.teardown()
	check(label+"/stack starts", nil, "")

	// 4 MiB round trip through SOCKS5, checksummed.
	err = roundTrip(s.socks, s.echo.addr, 4<<20, "", "")
	checkf(label+"/socks5 4MiB round trip", err, "sha256 matched")

	// Many concurrent tunnels at once.
	err = concurrentRoundTrips(s.socks, s.echo.addr, 40, 64<<10)
	checkf(label+"/40 concurrent tunnels", err, "all checksums matched")

	// HTTP CONNECT front end.
	err = httpConnectRoundTrip(s.http, s.echo.addr, 256<<10)
	checkf(label+"/http CONNECT round trip", err, "sha256 matched")

	// Plain HTTP proxying (absolute-form GET), against a real origin server.
	err = httpForwardProxy(s.http)
	checkf(label+"/http forward proxy GET", err, "body matched")

	// UDP associate.
	err = udpRoundTrip(s.socks, s.udp)
	checkf(label+"/socks5 udp associate", err, "datagram echoed")
}

// runAuthSuite proves basic_auth is enforced on the SOCKS front end in both
// directions: right credentials in, wrong credentials out.
func runAuthSuite(bin, workDir string) {
	fmt.Println("\n-- basic auth --")

	s, err := buildStack(bin, workDir+"/auth", false, true)
	if err != nil {
		check("auth/stack starts", err, "")
		if s != nil {
			s.teardown()
		}
		return
	}
	defer s.teardown()

	err = roundTrip(s.socks, s.echo.addr, 32<<10, "e2e", "secret")
	checkf("auth/valid credentials accepted", err, "sha256 matched")

	c, err := socks5Connect(s.socks, s.echo.addr, "e2e", "wrong")
	if err == nil {
		_ = c.Close()
		check("auth/wrong credentials rejected",
			fmt.Errorf("connection succeeded with a bad password"), "")
	} else {
		checkf("auth/wrong credentials rejected", nil, "%v", err)
	}
}

func roundTrip(socksAddr, target string, size int, user, pass string) error {
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		return err
	}
	want := sha256.Sum256(payload)

	c, err := socks5Connect(socksAddr, target, user, pass)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(60 * time.Second))

	got := sha256.New()
	var wg sync.WaitGroup
	var readErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := io.CopyN(got, c, int64(size)); err != nil {
			readErr = fmt.Errorf("read back: %w", err)
		}
	}()
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

func concurrentRoundTrips(socksAddr, target string, n, size int) error {
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- roundTrip(socksAddr, target, size, "", "")
		}()
	}
	wg.Wait()
	close(errs)
	failed := 0
	var first error
	for err := range errs {
		if err != nil {
			failed++
			if first == nil {
				first = err
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d/%d tunnels failed, first: %w", failed, n, first)
	}
	return nil
}

func httpConnectRoundTrip(httpAddr, target string, size int) error {
	c, err := net.DialTimeout("tcp", httpAddr, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(60 * time.Second))

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if _, err := io.WriteString(c, req); err != nil {
		return err
	}
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read CONNECT status: %w", err)
	}
	if !strings.Contains(status, "200") {
		return fmt.Errorf("CONNECT rejected: %s", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		return err
	}
	want := sha256.Sum256(payload)
	got := sha256.New()
	var wg sync.WaitGroup
	var readErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := io.CopyN(got, br, int64(size)); err != nil {
			readErr = err
		}
	}()
	if _, err := c.Write(payload); err != nil {
		return err
	}
	wg.Wait()
	if readErr != nil {
		return readErr
	}
	if !bytes.Equal(got.Sum(nil), want[:]) {
		return fmt.Errorf("payload corrupted over CONNECT")
	}
	return nil
}

func httpForwardProxy(proxyAddr string) error {
	const body = "spaceship-e2e-origin"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	c, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))

	origin := ln.Addr().String()
	req := fmt.Sprintf("GET http://%s/ HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", origin, origin)
	if _, err := io.WriteString(c, req); err != nil {
		return err
	}
	resp, err := io.ReadAll(c)
	if err != nil {
		return err
	}
	if !bytes.Contains(resp, []byte(body)) {
		return fmt.Errorf("origin body missing from proxied response: %.120q", resp)
	}
	return nil
}

func udpRoundTrip(socksAddr, udpTarget string) error {
	ctrl, relay, err := socks5UDPAssociate(socksAddr)
	if err != nil {
		return err
	}
	defer func() { _ = ctrl.Close() }()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() { _ = pc.Close() }()

	relayAddr, err := net.ResolveUDPAddr("udp", relay)
	if err != nil {
		return err
	}
	payload := []byte("spaceship-udp-e2e")
	pkt, err := encapsulateUDP(udpTarget, payload)
	if err != nil {
		return err
	}
	if _, err := pc.WriteTo(pkt, relayAddr); err != nil {
		return err
	}
	_ = pc.SetReadDeadline(time.Now().Add(15 * time.Second))
	buf := make([]byte, 65535)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		return fmt.Errorf("no datagram came back: %w", err)
	}
	got, err := decapsulateUDP(buf[:n])
	if err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("datagram mismatch: got %q want %q", got, payload)
	}
	return nil
}
