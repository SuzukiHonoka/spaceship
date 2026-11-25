package client

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
	"google.golang.org/grpc/credentials"
)

func TestParseTLSFingerprint(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantID    utls.ClientHelloID
		wantUTLS  bool
		wantError bool
	}{
		{name: "empty defaults to chrome", input: "", wantID: utls.HelloChrome_Auto, wantUTLS: true},
		{name: "chrome", input: "chrome", wantID: utls.HelloChrome_Auto, wantUTLS: true},
		{name: "chrome mixed case and spaces", input: " Chrome ", wantID: utls.HelloChrome_Auto, wantUTLS: true},
		{name: "firefox", input: "firefox", wantID: utls.HelloFirefox_Auto, wantUTLS: true},
		{name: "safari", input: "safari", wantID: utls.HelloSafari_Auto, wantUTLS: true},
		{name: "edge", input: "edge", wantID: utls.HelloEdge_Auto, wantUTLS: true},
		{name: "ios", input: "ios", wantID: utls.HelloIOS_Auto, wantUTLS: true},
		{name: "random", input: "random", wantUTLS: true},
		{name: "randomized", input: "randomized", wantUTLS: true},
		{name: "none disables utls", input: "none", wantUTLS: false},
		{name: "golang disables utls", input: "golang", wantUTLS: false},
		{name: "unknown rejected", input: "invalid-browser", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, useUTLS, err := ParseTLSFingerprint(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatalf("ParseTLSFingerprint(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTLSFingerprint(%q) unexpected error: %v", tt.input, err)
			}
			if useUTLS != tt.wantUTLS {
				t.Fatalf("ParseTLSFingerprint(%q) useUTLS = %v, want %v", tt.input, useUTLS, tt.wantUTLS)
			}
			if useUTLS && id != tt.wantID {
				t.Fatalf("ParseTLSFingerprint(%q) id = %v, want %v", tt.input, id, tt.wantID)
			}
		})
	}
}

func TestUTLSCredentials_ChromeClientHelloMimicsBrowser(t *testing.T) {
	// Capture the raw ClientHello handshake message sent by Chrome uTLS.
	captureClientHello := func(t *testing.T, creds credentials.TransportCredentials) []byte {
		t.Helper()
		clientConn, serverConn := net.Pipe()
		defer func() { _ = clientConn.Close() }()
		defer func() { _ = serverConn.Close() }()

		helloCh := make(chan []byte, 1)
		go func() {
			// Read TLS record header (5 bytes): type(1) + version(2) + length(2).
			header := make([]byte, 5)
			if _, err := io.ReadFull(serverConn, header); err != nil {
				helloCh <- nil
				return
			}
			recordLen := int(binary.BigEndian.Uint16(header[3:5]))
			payload := make([]byte, recordLen)
			if _, err := io.ReadFull(serverConn, payload); err != nil {
				helloCh <- nil
				return
			}
			_ = serverConn.Close()
			helloCh <- payload
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, _ = creds.ClientHandshake(ctx, "example.com:443", clientConn)

		raw := <-helloCh
		if len(raw) == 0 {
			t.Fatal("failed to capture ClientHello record")
		}
		return raw
	}

	chromeCreds := newUTLSCredentials(&utls.Config{ServerName: "example.com"}, tlsFingerprintSpec{useUTLS: true, helloID: utls.HelloChrome_Auto})
	goCreds := credentials.NewTLS(&tls.Config{
		ServerName: "example.com",
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
	})

	chromeHello := captureClientHello(t, chromeCreds)
	goHello := captureClientHello(t, goCreds)

	if bytes.Equal(chromeHello, goHello) {
		t.Fatal("Chrome uTLS ClientHello should differ from standard Go crypto/tls ClientHello")
	}

	parsed := utls.UnmarshalClientHello(chromeHello)
	if parsed == nil {
		t.Fatal("utls.UnmarshalClientHello returned nil for Chrome ClientHello")
	}
	if parsed.ServerName != "example.com" {
		t.Errorf("ClientHello SNI = %q, want %q", parsed.ServerName, "example.com")
	}
	if !slices.Contains(parsed.AlpnProtocols, "h2") || !slices.Contains(parsed.AlpnProtocols, "http/1.1") {
		t.Errorf("Chrome ClientHello ALPN = %v, want both h2 and http/1.1", parsed.AlpnProtocols)
	}
}

func TestUTLSCredentials_RejectsNonH2ALPN(t *testing.T) {
	cert, pool := newSelfSignedCert(t, "spaceship.test")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	serverDone := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		srv := tls.Server(raw, &tls.Config{
			Certificates:           []tls.Certificate{cert},
			NextProtos:             []string{"http/1.1"},
			MinVersion:             tls.VersionTLS13,
			SessionTicketsDisabled: true,
		})
		serverDone <- srv.Handshake()
		_ = srv.Close()
	}()

	clientConn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = clientConn.Close() }()

	creds := newUTLSCredentials(&utls.Config{
		ServerName: "spaceship.test",
		RootCAs:    pool,
	}, tlsFingerprintSpec{useUTLS: true, helloID: utls.HelloChrome_Auto})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, err = creds.ClientHandshake(ctx, "spaceship.test:443", clientConn)
	<-serverDone
	if err == nil {
		t.Fatal("ClientHandshake() succeeded with http/1.1 ALPN, want error")
	}
	if !strings.Contains(err.Error(), "http/1.1") {
		t.Errorf("ClientHandshake() error = %v, want mention of negotiated http/1.1 ALPN", err)
	}
}

func TestUTLSCredentials_MetadataAndClone(t *testing.T) {
	creds := newUTLSCredentials(&utls.Config{ServerName: "a.example"}, tlsFingerprintSpec{useUTLS: true, helloID: utls.HelloChrome_Auto})
	if info := creds.Info(); info.SecurityProtocol != "tls" {
		t.Fatalf("Info() = %+v, want SecurityProtocol=tls", info)
	}

	cloned := creds.Clone().(*utlsCredentials)
	cloned.config.ServerName = "b.example"
	if orig := creds.(*utlsCredentials).config.ServerName; orig != "a.example" {
		t.Errorf("original ServerName mutated to %q", orig)
	}
	if cloned.config.ServerName != "b.example" {
		t.Errorf("cloned ServerName = %q, want b.example", cloned.config.ServerName)
	}
	if _, _, err := creds.ServerHandshake(nil); err == nil {
		t.Error("ServerHandshake() expected error for client-only uTLS credentials")
	}
}

func TestUTLSCredentials_ContextCanceled(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	creds := newUTLSCredentials(&utls.Config{ServerName: "spaceship.test"}, tlsFingerprintSpec{useUTLS: true, helloID: utls.HelloChrome_Auto})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := creds.ClientHandshake(ctx, "spaceship.test:443", clientConn); err == nil {
		t.Fatal("ClientHandshake() succeeded with canceled context")
	}
}

func newSelfSignedCert(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        parsed,
	}, pool
}

func BenchmarkTLSHandshake(b *testing.B) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "spaceship.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"spaceship.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		b.Fatalf("create cert: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		b.Fatalf("parse cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}

	serverTLS := &tls.Config{
		Certificates:           []tls.Certificate{cert},
		NextProtos:             []string{"h2"},
		MinVersion:             tls.VersionTLS13,
		MaxVersion:             tls.VersionTLS13,
		CurvePreferences:       []tls.CurveID{tls.X25519MLKEM768, tls.X25519, tls.CurveP256},
		SessionTicketsDisabled: true,
	}

	cases := []struct {
		name  string
		creds credentials.TransportCredentials
	}{
		{
			name: "go_stdlib",
			creds: credentials.NewTLS(&tls.Config{
				ServerName:       "spaceship.test",
				RootCAs:          pool,
				MinVersion:       tls.VersionTLS13,
				MaxVersion:       tls.VersionTLS13,
				CurvePreferences: []tls.CurveID{tls.X25519MLKEM768, tls.X25519, tls.CurveP256},
			}),
		},
		{
			name: "utls_chrome",
			creds: newUTLSCredentials(&utls.Config{
				ServerName: "spaceship.test",
				RootCAs:    pool,
				MinVersion: utls.VersionTLS12,
			}, tlsFingerprintSpec{useUTLS: true, helloID: utls.HelloChrome_Auto}),
		},
		{
			name: "utls_firefox",
			creds: newUTLSCredentials(&utls.Config{
				ServerName: "spaceship.test",
				RootCAs:    pool,
				MinVersion: utls.VersionTLS12,
			}, tlsFingerprintSpec{useUTLS: true, helloID: utls.HelloFirefox_Auto}),
		},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				b.Fatalf("listen: %v", err)
			}
			defer func() { _ = ln.Close() }()

			go func() {
				for {
					raw, err := ln.Accept()
					if err != nil {
						return
					}
					go func(c net.Conn) {
						srv := tls.Server(c, serverTLS)
						_ = srv.Handshake()
						_ = srv.Close()
					}(raw)
				}
			}()

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				raw, err := net.Dial("tcp", ln.Addr().String())
				if err != nil {
					b.Fatalf("dial: %v", err)
				}
				conn, _, err := tc.creds.ClientHandshake(context.Background(), "spaceship.test:443", raw)
				if err != nil {
					_ = raw.Close()
					b.Fatalf("ClientHandshake: %v", err)
				}
				_ = conn.Close()
			}
		})
	}
}
