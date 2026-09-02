package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
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

func TestNewServerNormalizesNilContextAndAdmissionDefaults(t *testing.T) {
	var nilContext context.Context
	srv, err := NewServer(nilContext, config.Users{{UUID: "default-user"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Ctx == nil || srv.dnsAdmission == nil || srv.proxyAdmission == nil {
		t.Fatalf(
			"NewServer defaults: context=%t dns_admission=%t proxy_admission=%t",
			srv.Ctx != nil,
			srv.dnsAdmission != nil,
			srv.proxyAdmission != nil,
		)
	}
	if srv.proxyHandshakeTimeout != config.DefaultProxyHandshakeTimeout {
		t.Fatalf(
			"proxy handshake timeout = %s, want %s",
			srv.proxyHandshakeTimeout,
			config.DefaultProxyHandshakeTimeout,
		)
	}
}

func TestNewServerRejectsInvalidUsers(t *testing.T) {
	tests := []struct {
		name  string
		users config.Users
		want  string
	}{
		{name: "nil", users: config.Users{nil}, want: "is nil"},
		{name: "empty UUID", users: config.Users{{}}, want: "uuid can not be empty"},
		{
			name:  "duplicate UUID",
			users: config.Users{{UUID: "same"}, {UUID: "same"}},
			want:  "duplicate user uuid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewServer(context.Background(), tt.users, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewServer() error = %v, want containing %q", err, tt.want)
			}
		})
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

	started := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			started = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !started {
		select {
		case err := <-errCh:
			t.Fatalf("ListenAndServe exited before startup: %v", err)
		default:
			t.Fatal("ListenAndServe did not start")
		}
	}

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ListenAndServe error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after cancel")
	}
}

func TestServerServeRejectsNilListener(t *testing.T) {
	srv, err := NewServer(context.Background(), config.Users{{UUID: "nil-listener-user"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.serve(nil); err == nil {
		t.Fatal("serve(nil) succeeded")
	}
}

type readSignalConn struct {
	net.Conn
	reads chan<- struct{}
}

func (c *readSignalConn) Read(p []byte) (int, error) {
	select {
	case c.reads <- struct{}{}:
	default:
	}
	return c.Conn.Read(p)
}

type readSignalListener struct {
	net.Listener
	reads chan<- struct{}
}

func (l *readSignalListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &readSignalConn{Conn: conn, reads: l.reads}, nil
}

func waitForReadCall(t *testing.T, reads <-chan struct{}) {
	t.Helper()
	select {
	case <-reads:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not enter the connection handshake read")
	}
}

func TestServerCancelClosesPartialHandshakesImmediately(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		tls     bool
	}{
		{
			name:    "HTTP2 preface",
			payload: []byte("PRI * HT"),
		},
		{
			name:    "TLS record",
			payload: []byte{0x16, 0x03, 0x03, 0x01, 0x00, 0x01},
			tls:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var sslConfig *config.SSL
			if tt.tls {
				certPath, keyPath := writeSelfSigned(t)
				sslConfig = &config.SSL{PublicKey: certPath, PrivateKey: keyPath}
			}
			srv, err := NewServer(ctx, config.Users{{UUID: "handshake-user"}}, sslConfig, nil)
			if err != nil {
				t.Fatal(err)
			}

			base, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			reads := make(chan struct{}, 8)
			listener := &readSignalListener{Listener: base, reads: reads}
			serveErr := make(chan error, 1)
			go func() {
				serveErr <- srv.serve(listener)
			}()

			client, err := net.Dial("tcp", base.Addr().String())
			if err != nil {
				_ = base.Close()
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()

			// The first read is blocked waiting for handshake bytes. Supplying an
			// intentionally incomplete preface/record makes it return once, then
			// the second read proves grpc is waiting for the missing remainder.
			waitForReadCall(t, reads)
			if _, err := client.Write(tt.payload); err != nil {
				t.Fatal(err)
			}
			waitForReadCall(t, reads)

			started := time.Now()
			cancel()
			select {
			case err := <-serveErr:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("serve() error = %v, want context.Canceled", err)
				}
				if elapsed := time.Since(started); elapsed > 2*time.Second {
					t.Fatalf("forced shutdown took %s, want at most 2s", elapsed)
				}
			case <-time.After(3 * time.Second):
				_ = client.Close()
				t.Fatal("server waited for the handshake timeout during forced shutdown")
			}
		})
	}
}

func TestNewServerTLS(t *testing.T) {
	certPath, keyPath := writeSelfSigned(t)
	ctx := t.Context()

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
