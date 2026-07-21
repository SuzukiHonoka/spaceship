package e2e

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	nethttp "net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/api"
	"github.com/SuzukiHonoka/spaceship/v2/internal/socks"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config"
	mdns "github.com/miekg/dns"
)

// systemConfigEnvKey carries the path of the server config the helper process
// should boot from.
const systemConfigEnvKey = "SPACESHIP_E2E_SYSTEM_CONFIG"

const (
	// Both ends must agree on the gRPC service name: it becomes the HTTP/2 path
	// ("/<name>/Proxy"), so a mismatch fails every RPC.
	systemServiceName = "spaceship.e2e.Proxy"
	systemTLSHost     = "spaceship.e2e"

	// uuid.Parse rejects anything that is not a real UUID, so these are fixed
	// valid ones rather than readable strings.
	primaryUUID   = "6f1a6bb5-30f1-4a2e-9d0e-3a1c5f2b7e10"
	secondaryUUID = "b2d4c9a7-8e35-4f61-8a2c-1d7e6f4b3c90"

	proxyUser = "e2e-user"
	proxyPass = "e2e-pass"
)

// runSystemServer boots a full server from a config file, the same path
// cmd/spaceship takes. It never returns; the parent kills it.
func runSystemServer(configPath string) {
	cfg, err := config.NewFromConfigFile(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "system helper: loading config: %v\n", err)
		os.Exit(2)
	}
	if err := api.NewLauncher().Launch(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "system helper: launch: %v\n", err)
		os.Exit(2)
	}
}

// systemTLSCert writes a self-signed certificate valid for systemTLSHost and
// loopback, doubling as its own CA so the client can pin it.
func systemTLSCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generating serial: %v", err)
	}

	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: systemTLSHost},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{systemTLSHost},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling key: %v", err)
	}

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(
		&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("writing key: %v", err)
	}
	return certPath, keyPath
}

// systemEndpoints is every address the deployed pair listens on.
type systemEndpoints struct {
	socks     string
	socksUnix string
	http      string
	dns       string
	resolver  string // the upstream the far-end server queries
	tcpEcho   *net.TCPAddr
	udpEcho   *net.UDPAddr
	routeFile string // domains routed to block via a file source
}

// startSystem boots a complete server+client pair from real config files with
// every feature enabled, and returns the client's listening endpoints.
func startSystem(t *testing.T) systemEndpoints {
	t.Helper()
	return startSystemAs(t, primaryUUID)
}

// startSystemAs is startSystem with an explicit client identity, so the server's
// multi-user configuration can be exercised.
func startSystemAs(t *testing.T, clientUUID string) systemEndpoints {
	t.Helper()

	certPath, keyPath := systemTLSCert(t)
	serverAddr := freeLoopbackAddr(t)

	ep := systemEndpoints{
		socks:    freeLoopbackAddr(t),
		http:     freeLoopbackAddr(t),
		dns:      freeLoopbackAddr(t),
		resolver: startTestResolver(t),
		tcpEcho:  startTCPEcho(t),
		udpEcho:  startUDPEcho(t),
	}

	// Unix socket paths are capped around 104 bytes, so keep the directory short.
	sockDir, err := os.MkdirTemp("", "sp")
	if err != nil {
		t.Fatalf("creating socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	ep.socksUnix = filepath.Join(sockDir, "s.sock")
	if len(ep.socksUnix) > 100 {
		t.Skipf("socket path %q is too long for this platform", ep.socksUnix)
	}

	// A file-backed route source, exercising Route.Ext alongside inline sources.
	ep.routeFile = filepath.Join(t.TempDir(), "blocklist.txt")
	if err := os.WriteFile(ep.routeFile,
		[]byte("# blocked via file\nfile-blocked.test\n\n"), 0o600); err != nil {
		t.Fatalf("writing route file: %v", err)
	}

	startSystemServerProcess(t, serverAddr, certPath, keyPath, ep.resolver)
	startSystemClient(t, serverAddr, certPath, clientUUID, ep)
	return ep
}

// startSystemServerProcess writes a full server config and boots it out of process.
func startSystemServerProcess(t *testing.T, serverAddr, certPath, keyPath, resolver string) {
	t.Helper()

	cfg := map[string]any{
		"role":   "server",
		"log":    "skip",
		"listen": serverAddr,
		"path":   systemServiceName,
		"buffer": 64,
		"ipv6":   true,
		"users": []map[string]any{
			{"uuid": primaryUUID, "remark": "primary"},
			{"uuid": secondaryUUID, "remark": "secondary",
				"limit": map[string]any{"DownLink": 0, "UpLink": 0, "Bandwidth": 0}},
		},
		"ssl": map[string]any{"cert": certPath, "key": keyPath},
		"dns": map[string]any{"Type": "common", "Server": resolver},
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshaling server config: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatalf("writing server config: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=XXX_SYSTEM_HELPER_RUNS_NO_TESTS")
	cmd.Env = append(os.Environ(), systemConfigEnvKey+"="+configPath)
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the system server process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	if err := listenerReady(serverAddr, 30*time.Second); err != nil {
		t.Fatalf("system server never listened on %s: %v\nprocess output:\n%s",
			serverAddr, err, out.String())
	}
}

// startSystemClient boots the client in this process through api.Launcher, with
// every front end and routing feature turned on.
func startSystemClient(t *testing.T, serverAddr, certPath, clientUUID string, ep systemEndpoints) {
	t.Helper()

	cfg := map[string]any{
		"role":        "client",
		"log":         "skip",
		"server_addr": serverAddr,
		"host":        systemTLSHost,
		"uuid":        clientUUID,
		"tls":         true,
		"cas":         []string{certPath},
		"path":        systemServiceName,
		"mux":         2,
		"buffer":      64,
		"ipv6":        true,

		"listen_socks":      ep.socks,
		"listen_socks_unix": ep.socksUnix,
		"listen_http":       ep.http,
		"listen_dns":        ep.dns,

		"basic_auth":     []string{proxyUser + ":" + proxyPass},
		"idle_timeout":   0,
		"block_ipv6_dns": true,
		"udp": map[string]any{
			"max_associations": 16,
			"max_nat_entries":  8,
		},

		// Every match type, most specific first, with a catch-all to the tunnel.
		"route": []map[string]any{
			{"src": []string{"exact-blocked.test"}, "type": "exact", "dst": "block"},
			{"src": []string{"domain-blocked.test"}, "type": "domain", "dst": "block"},
			{"src": []string{"198.51.100.0/24"}, "type": "cidr", "dst": "block"},
			{"src": []string{`^regex-blocked\..*$`}, "type": "regex", "dst": "block"},
			{"path": ep.routeFile, "type": "domain", "dst": "block"},
			{"type": "default", "dst": "proxy"},
		},
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshaling client config: %v", err)
	}
	parsed, err := config.NewFromString(string(raw))
	if err != nil {
		t.Fatalf("parsing client config: %v", err)
	}

	// Apply mutates process-global state; put it back for the tests that follow.
	oldBuffer := transport.GetBufferSize()
	t.Cleanup(func() {
		rpc.SetServiceName("")
		transport.SetBufferSize(uint16(oldBuffer / 1024))
		transport.EnableIPv6()
		socks.SetUDPSettings(socks.UDPSettings{})
	})

	launcher := api.NewLauncher()
	launched := make(chan error, 1)
	go func() { launched <- launcher.Launch(parsed) }()
	t.Cleanup(func() {
		launcher.Stop()
		select {
		case <-launched:
		case <-time.After(15 * time.Second):
			t.Error("client launcher did not shut down")
		}
	})

	// Every front end must come up before the tests below touch it.
	for _, l := range []struct{ network, addr string }{
		{"tcp", ep.socks},
		{"unix", ep.socksUnix},
		{"tcp", ep.http},
	} {
		if err := listenerReadyNetwork(l.network, l.addr, 30*time.Second); err != nil {
			select {
			case lerr := <-launched:
				t.Fatalf("client launcher exited early: %v", lerr)
			default:
			}
			t.Fatalf("client front end %s/%s never came up: %v", l.network, l.addr, err)
		}
	}
	waitForUDPResponder(t, ep.dns)
}

func listenerReadyNetwork(network, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout(network, addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	return lastErr
}

// waitForUDPResponder polls the DNS front end until it answers, since a UDP
// listener accepts writes long before its handler is wired up.
func waitForUDPResponder(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		m := new(mdns.Msg)
		m.SetQuestion("known.test.", mdns.TypeA)
		c := &mdns.Client{Timeout: 500 * time.Millisecond}
		if _, _, err := c.Exchange(m, addr); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("dns front end at %s never answered", addr)
}

// socks5AuthConnect performs the username/password handshake (RFC 1929).
func socks5AuthConnect(t *testing.T, network, addr, user, pass string) (net.Conn, byte) {
	t.Helper()

	conn, err := net.DialTimeout(network, addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks %s/%s: %v", network, addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	// Offer username/password only, so a server without auth would have to reject.
	if _, err := conn.Write([]byte{socks5Ver, 1, 0x02}); err != nil {
		t.Fatalf("socks greeting: %v", err)
	}
	sel := make([]byte, 2)
	if _, err := io.ReadFull(conn, sel); err != nil {
		t.Fatalf("reading method selection: %v", err)
	}
	if sel[1] != 0x02 {
		t.Fatalf("method selection = %v, want username/password (2)", sel)
	}

	req := []byte{0x01, byte(len(user))}
	req = append(req, user...)
	req = append(req, byte(len(pass)))
	req = append(req, pass...)
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("writing auth: %v", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("reading auth reply: %v", err)
	}
	return conn, resp[1]
}

func proxyAuthHeader() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(proxyUser+":"+proxyPass))
}

// TestSystem_SOCKS5AuthenticatedConnectThroughTunnel is the headline case: a
// fully configured client and server, booted from real config files in separate
// processes, carrying a SOCKS5 CONNECT over TLS with a custom gRPC service name
// and a multiplexed connection pool.
func TestSystem_SOCKS5AuthenticatedConnectThroughTunnel(t *testing.T) {
	ep := startSystem(t)

	conn, authStatus := socks5AuthConnect(t, "tcp", ep.socks, proxyUser, proxyPass)
	if authStatus != 0 {
		t.Fatalf("socks auth status = %d, want 0 (success)", authStatus)
	}

	rep, _ := socks5Command(t, conn, cmdConnect, ep.tcpEcho)
	if rep != repSuccess {
		t.Fatalf("CONNECT reply = %d, want success", rep)
	}

	payload := []byte("full system round trip")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("writing through the system: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round trip payload = %q, want %q", got, payload)
	}
}

// TestSystem_SOCKS5RejectsBadCredentials verifies basic auth is enforced.
func TestSystem_SOCKS5RejectsBadCredentials(t *testing.T) {
	ep := startSystem(t)

	if _, status := socks5AuthConnect(t, "tcp", ep.socks, proxyUser, "wrong-password"); status == 0 {
		t.Error("socks accepted a wrong password")
	}
	if _, status := socks5AuthConnect(t, "tcp", ep.socks, "nobody", proxyPass); status == 0 {
		t.Error("socks accepted an unknown user")
	}
}

// TestSystem_SOCKS5UnixListener covers the unix-socket front end, including auth.
func TestSystem_SOCKS5UnixListener(t *testing.T) {
	ep := startSystem(t)

	conn, authStatus := socks5AuthConnect(t, "unix", ep.socksUnix, proxyUser, proxyPass)
	if authStatus != 0 {
		t.Fatalf("socks auth over unix status = %d, want 0", authStatus)
	}

	rep, _ := socks5Command(t, conn, cmdConnect, ep.tcpEcho)
	if rep != repSuccess {
		t.Fatalf("CONNECT over unix reply = %d, want success", rep)
	}

	payload := []byte("unix listener through the system")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("writing through the unix listener: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round trip payload = %q, want %q", got, payload)
	}
}

// TestSystem_SOCKS5UDPAssociateThroughTunnel carries datagrams over the fully
// configured system — the UDP relay, the tunnel, TLS, and the custom service
// name all at once.
func TestSystem_SOCKS5UDPAssociateThroughTunnel(t *testing.T) {
	ep := startSystem(t)

	ctrl, authStatus := socks5AuthConnect(t, "tcp", ep.socks, proxyUser, proxyPass)
	if authStatus != 0 {
		t.Fatalf("socks auth status = %d, want 0", authStatus)
	}

	rep, relayAddr := socks5Command(t, ctrl, cmdUDPAssociate, nil)
	if rep != repSuccess {
		t.Fatalf("UDP ASSOCIATE reply = %d, want success", rep)
	}

	sock := clientUDPSocket(t)
	payload := []byte("datagram across the whole system")
	if _, err := sock.WriteTo(encodeUDPRequest(t, ep.udpEcho, payload), relayAddr); err != nil {
		t.Fatalf("sending datagram: %v", err)
	}
	if err := sock.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 65535)
	n, _, err := sock.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no datagram completed the system round trip: %v", err)
	}
	src, got := decodeUDPReply(t, buf[:n])
	if !bytes.Equal(got, payload) {
		t.Errorf("round trip payload = %q, want %q", got, payload)
	}
	if !src.IP.Equal(ep.udpEcho.IP) || src.Port != ep.udpEcho.Port {
		t.Errorf("reply source = %v, want %v", src, ep.udpEcho)
	}
}

// TestSystem_HTTPProxyAuthenticated covers the HTTP front end: the 407 challenge,
// CONNECT tunnelling, and a plain proxied request.
func TestSystem_HTTPProxyAuthenticated(t *testing.T) {
	ep := startSystem(t)

	// Without credentials the proxy must challenge.
	conn, err := net.DialTimeout("tcp", ep.http, 5*time.Second)
	if err != nil {
		t.Fatalf("dial http proxy: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	target := ep.tcpEcho.String()
	if _, err := conn.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n")); err != nil {
		t.Fatalf("writing unauthenticated CONNECT: %v", err)
	}
	resp, err := nethttp.ReadResponse(bufio.NewReader(conn),
		&nethttp.Request{Method: nethttp.MethodConnect})
	if err != nil {
		t.Fatalf("reading challenge: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != nethttp.StatusProxyAuthRequired {
		t.Errorf("unauthenticated CONNECT status = %d, want 407", resp.StatusCode)
	}

	// With credentials it tunnels.
	authed, err := net.DialTimeout("tcp", ep.http, 5*time.Second)
	if err != nil {
		t.Fatalf("dial http proxy: %v", err)
	}
	defer authed.Close()
	if err := authed.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if _, err := authed.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target +
		"\r\nProxy-Authorization: " + proxyAuthHeader() + "\r\n\r\n")); err != nil {
		t.Fatalf("writing authenticated CONNECT: %v", err)
	}
	br := bufio.NewReader(authed)
	resp2, err := nethttp.ReadResponse(br, &nethttp.Request{Method: nethttp.MethodConnect})
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != nethttp.StatusOK {
		t.Fatalf("authenticated CONNECT status = %d, want 200", resp2.StatusCode)
	}

	payload := []byte("http connect across the system")
	if _, err := authed.Write(payload); err != nil {
		t.Fatalf("writing through the tunnel: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round trip payload = %q, want %q", got, payload)
	}
}

// TestSystem_HTTPProxyPlainRequest covers the absolute-URI path through the
// fully configured client.
func TestSystem_HTTPProxyPlainRequest(t *testing.T) {
	ep := startSystem(t)

	origin := &nethttp.Server{
		Handler: nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			_, _ = w.Write([]byte("origin-through-system"))
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("origin listen: %v", err)
	}
	go func() { _ = origin.Serve(ln) }()
	t.Cleanup(func() { _ = origin.Close() })

	proxyURL, err := url.Parse("http://" + proxyUser + ":" + proxyPass + "@" + ep.http)
	if err != nil {
		t.Fatalf("parsing proxy url: %v", err)
	}
	client := &nethttp.Client{
		Transport: &nethttp.Transport{Proxy: nethttp.ProxyURL(proxyURL)},
		Timeout:   30 * time.Second,
	}
	resp, err := client.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("proxied GET: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading proxied body: %v", err)
	}
	if string(body) != "origin-through-system" {
		t.Errorf("proxied body = %q, want %q", body, "origin-through-system")
	}
}

// TestSystem_DNSFrontEndResolvesThroughTunnel covers the local DNS server: a
// query reaches the far-end server over the tunnel, which asks its configured
// upstream resolver.
func TestSystem_DNSFrontEndResolvesThroughTunnel(t *testing.T) {
	ep := startSystem(t)

	m := new(mdns.Msg)
	m.SetQuestion("known.test.", mdns.TypeA)
	c := &mdns.Client{Timeout: 20 * time.Second}

	resp, _, err := c.Exchange(m, ep.dns)
	if err != nil {
		t.Fatalf("querying the dns front end: %v", err)
	}
	if resp.Rcode != mdns.RcodeSuccess {
		t.Fatalf("rcode = %d, want success", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("got %d answers, want 1", len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*mdns.A)
	if !ok {
		t.Fatalf("answer type = %T, want *dns.A", resp.Answer[0])
	}
	if !a.A.Equal(net.ParseIP("203.0.113.7")) {
		t.Errorf("resolved address = %v, want 203.0.113.7", a.A)
	}
}

// TestSystem_DNSFrontEndBlocksIPv6 covers block_ipv6_dns end to end: the flag
// travels with the request and the far-end server suppresses the AAAA lookup.
func TestSystem_DNSFrontEndBlocksIPv6(t *testing.T) {
	ep := startSystem(t)

	m := new(mdns.Msg)
	m.SetQuestion("known.test.", mdns.TypeAAAA)
	c := &mdns.Client{Timeout: 20 * time.Second}

	resp, _, err := c.Exchange(m, ep.dns)
	if err != nil {
		t.Fatalf("querying the dns front end: %v", err)
	}
	if resp.Rcode != mdns.RcodeSuccess {
		t.Errorf("rcode = %d, want success: a blocked AAAA is not a failure", resp.Rcode)
	}
	if len(resp.Answer) != 0 {
		t.Errorf("got %d AAAA answers, want 0 with block_ipv6_dns enabled", len(resp.Answer))
	}
}

// TestSystem_DNSFrontEndPropagatesNXDOMAIN verifies the rcode survives both hops.
func TestSystem_DNSFrontEndPropagatesNXDOMAIN(t *testing.T) {
	ep := startSystem(t)

	m := new(mdns.Msg)
	m.SetQuestion("missing.test.", mdns.TypeA)
	c := &mdns.Client{Timeout: 20 * time.Second}

	resp, _, err := c.Exchange(m, ep.dns)
	if err != nil {
		t.Fatalf("querying the dns front end: %v", err)
	}
	if resp.Rcode != mdns.RcodeNameError {
		t.Errorf("rcode = %d, want %d (NXDOMAIN)", resp.Rcode, mdns.RcodeNameError)
	}
}

// TestSystem_RoutingRulesHonored drives every route match type through the live
// client. Each blocked destination must be refused before any dial happens.
func TestSystem_RoutingRulesHonored(t *testing.T) {
	ep := startSystem(t)

	tests := []struct {
		name string
		host string
		port uint16
	}{
		{"exact match", "exact-blocked.test", 80},
		{"domain match", "domain-blocked.test", 80},
		{"domain match on a subdomain", "deep.domain-blocked.test", 80},
		{"cidr match", "198.51.100.10", 80},
		{"regex match", "regex-blocked.example", 80},
		{"file-sourced domain", "file-blocked.test", 80},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, status := socks5AuthConnect(t, "tcp", ep.socks, proxyUser, proxyPass)
			if status != 0 {
				t.Fatalf("socks auth status = %d, want 0", status)
			}
			rep := socks5CommandHost(t, conn, cmdConnect, tt.host, tt.port)
			if rep != repRuleFailure {
				t.Errorf("CONNECT to %s reply = %d, want %d (rule failure)",
					tt.host, rep, repRuleFailure)
			}
		})
	}

	// A destination that matches none of the block rules still reaches the tunnel.
	conn, status := socks5AuthConnect(t, "tcp", ep.socks, proxyUser, proxyPass)
	if status != 0 {
		t.Fatalf("socks auth status = %d, want 0", status)
	}
	if rep, _ := socks5Command(t, conn, cmdConnect, ep.tcpEcho); rep != repSuccess {
		t.Errorf("CONNECT to an unblocked target reply = %d, want success", rep)
	}

	// An unblocked name that simply does not resolve must fail *differently*
	// from a blocked one. Without this the cases above would still pass if the
	// route table were ignored entirely and every name merely failed to resolve.
	unresolvable, status := socks5AuthConnect(t, "tcp", ep.socks, proxyUser, proxyPass)
	if status != 0 {
		t.Fatalf("socks auth status = %d, want 0", status)
	}
	rep := socks5CommandHost(t, unresolvable, cmdConnect, "not-blocked-nonexistent.test", 80)
	if rep == repSuccess {
		t.Error("CONNECT to a nonexistent host succeeded")
	}
	if rep == repRuleFailure {
		t.Error("an unblocked but unresolvable host reported rule failure; the block " +
			"rules above may not be doing the work the test credits them for")
	}
}

// TestSystem_StatsAccounting verifies the byte counters the management API
// reports actually move when traffic crosses the system.
func TestSystem_StatsAccounting(t *testing.T) {
	ep := startSystem(t)

	beforeTx, beforeRx := transport.GlobalStats.Total()

	conn, status := socks5AuthConnect(t, "tcp", ep.socks, proxyUser, proxyPass)
	if status != 0 {
		t.Fatalf("socks auth status = %d, want 0", status)
	}
	if rep, _ := socks5Command(t, conn, cmdConnect, ep.tcpEcho); rep != repSuccess {
		t.Fatalf("CONNECT failed")
	}

	payload := bytes.Repeat([]byte("x"), 4096)
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("writing payload: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}

	afterTx, afterRx := transport.GlobalStats.Total()
	if afterTx <= beforeTx {
		t.Errorf("tx counter did not advance: %d -> %d", beforeTx, afterTx)
	}
	if afterRx <= beforeRx {
		t.Errorf("rx counter did not advance: %d -> %d", beforeRx, afterRx)
	}
}

// TestSystem_SecondaryUserAccepted boots the client as the *second* configured
// user, proving the server honours its whole user list rather than only the
// first entry.
func TestSystem_SecondaryUserAccepted(t *testing.T) {
	ep := startSystemAs(t, secondaryUUID)

	conn, status := socks5AuthConnect(t, "tcp", ep.socks, proxyUser, proxyPass)
	if status != 0 {
		t.Fatalf("socks auth status = %d, want 0", status)
	}
	if rep, _ := socks5Command(t, conn, cmdConnect, ep.tcpEcho); rep != repSuccess {
		t.Fatalf("CONNECT as the secondary user reply = %d, want success", rep)
	}

	payload := []byte("secondary user through the system")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("writing through the system: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round trip payload = %q, want %q", got, payload)
	}
}

// TestSystem_UnknownUserRejected verifies the far-end server refuses a UUID that
// is not in its user list, over the fully configured stack.
func TestSystem_UnknownUserRejected(t *testing.T) {
	ep := startSystemAs(t, "00000000-0000-4000-8000-000000000000")

	conn, status := socks5AuthConnect(t, "tcp", ep.socks, proxyUser, proxyPass)
	if status != 0 {
		t.Fatalf("socks auth status = %d, want 0", status)
	}

	start := time.Now()
	rep, _ := socks5Command(t, conn, cmdConnect, ep.tcpEcho)
	if rep == repSuccess {
		t.Error("CONNECT succeeded with a UUID the server does not know")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("rejection surfaced after %v; an authentication failure must not "+
			"wait out the ack timeout", elapsed)
	}
}

// startTestResolver runs a DNS server over a fixed zone, standing in for the
// upstream resolver the far-end server is configured to query.
func startTestResolver(t *testing.T) string {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test resolver listen: %v", err)
	}

	mux := mdns.NewServeMux()
	mux.HandleFunc(".", func(w mdns.ResponseWriter, r *mdns.Msg) {
		m := new(mdns.Msg)
		m.SetReply(r)
		if len(r.Question) == 0 {
			m.Rcode = mdns.RcodeFormatError
			_ = w.WriteMsg(m)
			return
		}
		switch q := r.Question[0]; {
		case q.Name == "known.test." && q.Qtype == mdns.TypeA:
			if rr, err := mdns.NewRR("known.test. 300 IN A 203.0.113.7"); err == nil {
				m.Answer = append(m.Answer, rr)
			}
		case q.Name == "known.test." && q.Qtype == mdns.TypeAAAA:
			if rr, err := mdns.NewRR("known.test. 300 IN AAAA 2001:db8::7"); err == nil {
				m.Answer = append(m.Answer, rr)
			}
		case q.Name == "missing.test.":
			m.Rcode = mdns.RcodeNameError
		default:
			m.Rcode = mdns.RcodeServerFailure
		}
		_ = w.WriteMsg(m)
	})

	srv := &mdns.Server{PacketConn: pc, Handler: mux}
	started := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(started) }
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("test resolver did not start")
	}
	return pc.LocalAddr().String()
}

// socks5CommandHost issues a request against a hostname using the domain address
// type, which is how a real client asks the proxy to resolve remotely.
func socks5CommandHost(t *testing.T, conn net.Conn, cmd byte, host string, port uint16) byte {
	t.Helper()

	req := []byte{socks5Ver, cmd, 0x00, atypDomain, byte(len(host))}
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port&0xff))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("writing socks request for %s: %v", host, err)
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("reading socks reply header for %s: %v", host, err)
	}
	if head[0] != socks5Ver {
		t.Fatalf("reply version = %d, want 5", head[0])
	}

	switch head[3] {
	case atypIPv4:
		if _, err := io.ReadFull(conn, make([]byte, 4)); err != nil {
			t.Fatalf("reading bound IPv4: %v", err)
		}
	case atypIPv6:
		if _, err := io.ReadFull(conn, make([]byte, 16)); err != nil {
			t.Fatalf("reading bound IPv6: %v", err)
		}
	case atypDomain:
		lb := make([]byte, 1)
		if _, err := io.ReadFull(conn, lb); err != nil {
			t.Fatalf("reading bound domain length: %v", err)
		}
		if _, err := io.ReadFull(conn, make([]byte, lb[0])); err != nil {
			t.Fatalf("reading bound domain: %v", err)
		}
	default:
		t.Fatalf("unknown bound address type %d", head[3])
	}
	if _, err := io.ReadFull(conn, make([]byte, 2)); err != nil {
		t.Fatalf("reading bound port: %v", err)
	}
	return head[1]
}
