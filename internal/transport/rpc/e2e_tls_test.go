package rpc_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/server"
	serverconfig "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

const testTLSHost = "spaceship.test"

// generateTestCert writes a self-signed certificate valid for testTLSHost and
// loopback, returning the cert and key paths. It doubles as its own CA so the
// client can trust it by passing the cert as a custom CA.
func generateTestCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generating serial: %v", err)
	}

	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: testTLSHost},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{testTLSHost},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
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

	if err := os.WriteFile(certPath,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing cert: %v", err)
	}
	if err := os.WriteFile(keyPath,
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("writing key: %v", err)
	}
	return certPath, keyPath
}

// startTLSProxyServer runs a proxy server terminating TLS itself.
func startTLSProxyServer(t *testing.T, certPath, keyPath string) string {
	t.Helper()

	addr := freeLoopbackAddr(t)
	ctx, cancel := context.WithCancel(context.Background())

	srv, err := server.NewServer(ctx, serverconfig.Users{{UUID: testUUID}},
		&serverconfig.SSL{PublicKey: certPath, PrivateKey: keyPath}, nil)
	if err != nil {
		cancel()
		t.Fatalf("NewServer() with TLS error = %v", err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe(addr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(10 * time.Second):
			t.Error("TLS proxy server did not shut down")
		}
	})

	waitForListener(t, addr)
	return addr
}

// connectClientTLS initializes the pool with TLS enabled and the given CA list.
func connectClientTLS(t *testing.T, addr string, cas []string) {
	t.Helper()
	client.SetUUID(testUUID)
	if err := client.Init(addr, testTLSHost, true, 1, cas); err != nil {
		t.Fatalf("client.Init() with TLS error = %v", err)
	}
	t.Cleanup(client.Destroy)
}

// TestEndToEnd_TCPRoundTripOverTLS proves the tunnel works over a real TLS 1.3
// handshake, which is how this is meant to be deployed. Every other end-to-end
// test runs h2c, so nothing else exercises certificate loading, the pinned TLS
// version, or the configured curve preferences.
func TestEndToEnd_TCPRoundTripOverTLS(t *testing.T) {
	routeAllDirect(t)
	certPath, keyPath := generateTestCert(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp echo listen: %v", err)
	}
	defer ln.Close()

	payload := []byte("hello through TLS")
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		_, _ = conn.Write(buf)
	}()

	connectClientTLS(t, startTLSProxyServer(t, certPath, keyPath), []string{certPath})

	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	srcReader, srcWriter := io.Pipe()
	received := make(chan []byte, 1)
	dst := &signalWriter{want: len(payload), done: received}

	proxyErr := make(chan error, 1)
	go func() {
		proxyErr <- c.Proxy(ctx, ln.Addr().String(), make(chan string, 1), dst, srcReader)
	}()

	if _, err := srcWriter.Write(payload); err != nil {
		t.Fatalf("writing to the proxied source: %v", err)
	}

	select {
	case got := <-received:
		if !bytes.Equal(got, payload) {
			t.Errorf("proxied payload = %q, want %q", got, payload)
		}
	case err := <-proxyErr:
		t.Fatalf("Proxy() returned before the reply arrived: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("no reply completed the round trip over TLS")
	}

	_ = srcWriter.Close()
	select {
	case <-proxyErr:
	case <-time.After(20 * time.Second):
		t.Error("Proxy() did not return after the source closed")
	}
}

// TestEndToEnd_UDPRoundTripOverTLS covers the UDP path over TLS as well, since
// the packet stream takes a different code path through the same connection.
func TestEndToEnd_UDPRoundTripOverTLS(t *testing.T) {
	routeAllDirect(t)
	certPath, keyPath := generateTestCert(t)
	echoAddr := startUDPEcho(t)

	connectClientTLS(t, startTLSProxyServer(t, certPath, keyPath), []string{certPath})

	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	defer c.Close()

	pc, err := c.DialPacket("udp", echoAddr)
	if err != nil {
		t.Fatalf("DialPacket() over TLS error = %v", err)
	}
	defer pc.Close()

	target, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		t.Fatalf("resolve echo addr: %v", err)
	}

	payload := []byte("datagram over TLS")
	if _, err := pc.WriteTo(payload, target); err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}
	if err := pc.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buf := make([]byte, 2048)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom() error = %v", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Errorf("round trip payload = %q, want %q", buf[:n], payload)
	}
}

// TestEndToEnd_TLSRejectsUntrustedCertificate verifies the client actually
// validates the server certificate.
//
// Without this, the TLS tests above would still pass if verification were
// silently disabled — and an unverified TLS tunnel offers no protection against
// the interception it exists to prevent.
func TestEndToEnd_TLSRejectsUntrustedCertificate(t *testing.T) {
	routeAllDirect(t)
	certPath, keyPath := generateTestCert(t)

	// Same server, but the client is given no CA for the self-signed cert.
	connectClientTLS(t, startTLSProxyServer(t, certPath, keyPath), nil)

	c, err := client.New()
	if err != nil {
		// Failing this early is a valid rejection too.
		return
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, _, err := c.DnsResolve(ctx, []*client.DnsRequest{{Fqdn: "known.test", QType: 1}})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("RPC succeeded against a server whose certificate is not trusted; " +
				"the client is not verifying the chain")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RPC neither completed nor failed against an untrusted certificate")
	}
}
