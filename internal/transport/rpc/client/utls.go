package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strings"

	utls "github.com/refraction-networking/utls"
	"google.golang.org/grpc/credentials"
)

const (
	// DefaultTLSFingerprint is used when TLS is enabled and no explicit
	// tls_fingerprint is configured. Chrome's ClientHello mimics normal browser
	// traffic to avoid Go crypto/tls JA3/JA4 fingerprinting by DPI.
	DefaultTLSFingerprint = "chrome"

	alpnH2 = "h2"
)

// randomBrowserHelloIDs lists the TLS 1.3 + h2 browser ClientHello profiles
// eligible for the "random"/"randomized" preset. Picking from real browser
// profiles guarantees TLS 1.3 compatibility and avoids synthetic extension
// combinations that DPI can flag.
var randomBrowserHelloIDs = []utls.ClientHelloID{
	utls.HelloChrome_Auto,
	utls.HelloFirefox_Auto,
	utls.HelloSafari_Auto,
	utls.HelloEdge_Auto,
	utls.HelloIOS_Auto,
}

// tlsFingerprintSpec resolves a configured tls_fingerprint name into how
// ClientHandshake constructs a uTLS ClientHello.
type tlsFingerprintSpec struct {
	useUTLS bool
	random  bool
	helloID utls.ClientHelloID
}

func (s tlsFingerprintSpec) pickHelloID() utls.ClientHelloID {
	if s.random {
		return randomBrowserHelloIDs[rand.IntN(len(randomBrowserHelloIDs))] // #nosec G404 -- non-cryptographic browser profile selection
	}
	return s.helloID
}

// ParseTLSFingerprint resolves a configured tls_fingerprint name into a uTLS
// ClientHelloID. For "random"/"randomized", it returns a zero ClientHelloID
// with useUTLS == true (a real browser profile is chosen per handshake). It
// returns useUTLS == false when the caller explicitly selects Go's standard
// crypto/tls ("none" or "golang"). An empty name selects DefaultTLSFingerprint.
func ParseTLSFingerprint(name string) (id utls.ClientHelloID, useUTLS bool, err error) {
	spec, err := parseTLSFingerprintSpec(name)
	if err != nil {
		return utls.ClientHelloID{}, false, err
	}
	return spec.helloID, spec.useUTLS, nil
}

func parseTLSFingerprintSpec(name string) (tlsFingerprintSpec, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		normalized = DefaultTLSFingerprint
	}

	switch normalized {
	case "chrome":
		return tlsFingerprintSpec{useUTLS: true, helloID: utls.HelloChrome_Auto}, nil
	case "firefox":
		return tlsFingerprintSpec{useUTLS: true, helloID: utls.HelloFirefox_Auto}, nil
	case "safari":
		return tlsFingerprintSpec{useUTLS: true, helloID: utls.HelloSafari_Auto}, nil
	case "edge":
		return tlsFingerprintSpec{useUTLS: true, helloID: utls.HelloEdge_Auto}, nil
	case "ios":
		return tlsFingerprintSpec{useUTLS: true, helloID: utls.HelloIOS_Auto}, nil
	case "random", "randomized":
		return tlsFingerprintSpec{useUTLS: true, random: true}, nil
	case "none", "golang":
		return tlsFingerprintSpec{useUTLS: false}, nil
	default:
		return tlsFingerprintSpec{}, fmt.Errorf(
			"unsupported tls_fingerprint %q: want one of chrome, firefox, safari, edge, ios, randomized, none",
			name,
		)
	}
}

// ValidateTLSFingerprint checks whether name is an accepted tls_fingerprint
// configuration value.
func ValidateTLSFingerprint(name string) error {
	_, err := parseTLSFingerprintSpec(name)
	return err
}

// utlsCredentials implements grpc/credentials.TransportCredentials using uTLS
// to present a browser-like TLS ClientHello over gRPC HTTP/2.
type utlsCredentials struct {
	config *utls.Config
	spec   tlsFingerprintSpec
}

func newUTLSCredentials(cfg *utls.Config, spec tlsFingerprintSpec) credentials.TransportCredentials {
	cloned := cfg.Clone()
	if len(cloned.NextProtos) == 0 {
		cloned.NextProtos = []string{alpnH2, "http/1.1"}
	}
	return &utlsCredentials{
		config: cloned,
		spec:   spec,
	}
}

func (c *utlsCredentials) ClientHandshake(ctx context.Context, authority string, rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	cfg := c.config.Clone()
	if cfg.ServerName == "" {
		serverName, _, err := net.SplitHostPort(authority)
		if err != nil {
			serverName = authority
		}
		cfg.ServerName = serverName
	}

	uconn := utls.UClient(rawConn, cfg, c.spec.pickHelloID())
	if err := uconn.HandshakeContext(ctx); err != nil {
		return nil, nil, err
	}

	state := uconn.ConnectionState()
	if state.NegotiatedProtocol != "" && state.NegotiatedProtocol != alpnH2 {
		_ = uconn.Close()
		return nil, nil, fmt.Errorf("utls: server negotiated ALPN %q, want %q", state.NegotiatedProtocol, alpnH2)
	}

	authInfo := credentials.TLSInfo{
		State: utlsToStdConnectionState(state),
		CommonAuthInfo: credentials.CommonAuthInfo{
			SecurityLevel: credentials.PrivacyAndIntegrity,
		},
	}
	return uconn, authInfo, nil
}

func (c *utlsCredentials) ServerHandshake(net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, errors.New("utls credentials are only supported for clients")
}

func (c *utlsCredentials) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{
		SecurityProtocol: "tls",
	}
}

func (c *utlsCredentials) Clone() credentials.TransportCredentials {
	return &utlsCredentials{
		config: c.config.Clone(),
		spec:   c.spec,
	}
}

func (c *utlsCredentials) OverrideServerName(serverNameOverride string) error {
	c.config.ServerName = serverNameOverride
	return nil
}

func utlsToStdConnectionState(s utls.ConnectionState) tls.ConnectionState {
	return tls.ConnectionState{
		Version:                     s.Version,
		HandshakeComplete:           s.HandshakeComplete,
		DidResume:                   s.DidResume,
		CipherSuite:                 s.CipherSuite,
		NegotiatedProtocol:          s.NegotiatedProtocol,
		ServerName:                  s.ServerName,
		PeerCertificates:            s.PeerCertificates,
		VerifiedChains:              s.VerifiedChains,
		SignedCertificateTimestamps: s.SignedCertificateTimestamps,
		OCSPResponse:                s.OCSPResponse,
		TLSUnique:                   s.TLSUnique,
		ECHAccepted:                 s.ECHAccepted,
	}
}
