package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/dns"
)

func TestNewServerEmptyUsers(t *testing.T) {
	_, err := NewServer(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("NewServer accepted empty users")
	}
}

func TestNewServerAndListenCancel(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	srv, err := NewServer(ctx, config.Users{{UUID: "server-unit-user"}}, nil, &dns.DNS{
		Type:   dns.TypeCommon,
		Server: "1.1.1.1",
	})
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(addr) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			// Graceful stop may surface context.Canceled or nil/ErrServerStopped mapped.
			t.Logf("ListenAndServe returned: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after cancel")
	}
}

func TestNewServerTLS(t *testing.T) {
	certPath, keyPath := writeSelfSigned(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := NewServer(ctx, config.Users{{UUID: "tls-user"}}, &config.SSL{
		PublicKey:  certPath,
		PrivateKey: keyPath,
	}, nil)
	if err != nil {
		t.Fatalf("NewServer TLS: %v", err)
	}
	if srv == nil {
		t.Fatal("nil server")
	}
}

func TestNewServerTLSMissingFiles(t *testing.T) {
	_, err := NewServer(context.Background(), config.Users{{UUID: "u"}}, &config.SSL{
		PublicKey:  "/no/cert.pem",
		PrivateKey: "/no/key.pem",
	}, nil)
	if err == nil {
		t.Fatal("NewServer accepted missing TLS files")
	}
}

func TestBuildTLSConfig(t *testing.T) {
	certPath, keyPath := writeSelfSigned(t)
	cfg, err := buildTLSConfig(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("certs = %d", len(cfg.Certificates))
	}
}

func writeSelfSigned(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "spaceship-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
