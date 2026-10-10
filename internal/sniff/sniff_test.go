package sniff

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPeekHTTPHost(t *testing.T) {
	body := []byte("hello")
	raw := []byte("GET /path HTTP/1.1\r\nHost: Example.COM.:443\r\nX-A: b\r\n\r\n")
	raw = append(raw, body...)
	host, replay := Peek(bytes.NewReader(raw), time.Second)
	if host != "example.com" {
		t.Fatalf("host = %q, want example.com", host)
	}
	got, err := io.ReadAll(replay)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("replay = %q, want %q", got, raw)
	}
}

func TestPeekHTTPHostIsIP(t *testing.T) {
	raw := []byte("GET / HTTP/1.1\r\nHost: 192.0.2.10:80\r\n\r\n")
	host, replay := Peek(bytes.NewReader(raw), time.Second)
	if host != "" {
		t.Fatalf("host = %q, want empty", host)
	}
	got, err := io.ReadAll(replay)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("replay = %q, want the original request", got)
	}
}

func TestPeekTLSServerName(t *testing.T) {
	hello := BuildClientHello("WWW.Example.COM.")
	extra := []byte("next-flight")
	raw := append(append([]byte(nil), hello...), extra...)
	host, replay := Peek(bytes.NewReader(raw), time.Second)
	if host != "www.example.com" {
		t.Fatalf("host = %q, want www.example.com", host)
	}
	got, err := io.ReadAll(replay)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("replay length = %d, want %d", len(got), len(raw))
	}
}

func TestPeekOneByteFragments(t *testing.T) {
	raw := BuildClientHello("fragment.example")
	host, replay := Peek(&oneByteReader{rest: raw}, time.Second)
	if host != "fragment.example" {
		t.Fatalf("host = %q, want fragment.example", host)
	}
	got, err := io.ReadAll(replay)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("fragmented ClientHello was not replayed intact")
	}
}

func TestPeekIgnoresOtherProtocols(t *testing.T) {
	raw := []byte("SSH-2.0-OpenSSH\r\n")
	r := &onceReader{data: raw}
	host, replay := Peek(r, time.Second)
	if host != "" {
		t.Fatalf("host = %q, want empty", host)
	}
	if r.reads != 1 {
		t.Fatalf("reads = %d, want 1", r.reads)
	}
	got := make([]byte, len(raw))
	if _, err := io.ReadFull(replay, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("replay = %q, want %q", got, raw)
	}
	if r.reads != 1 {
		t.Fatalf("replay read past the first flight: reads = %d", r.reads)
	}
}

func TestPeekTimesOutWithoutData(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()

	done := make(chan struct{})
	var host string
	var replay io.Reader
	go func() {
		host, replay = Peek(server, 20*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("peek did not return after the read deadline")
	}
	if host != "" {
		t.Fatalf("host = %q, want empty", host)
	}

	got := make([]byte, 4)
	errCh := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(replay, got)
		errCh <- err
	}()
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if string(got) != "ping" {
		t.Fatalf("replay = %q, want ping", got)
	}
}

func TestPeekRejectsMalformedNames(t *testing.T) {
	cases := [][]byte{
		[]byte("GET / HTTP/1.1\r\nHost: bad name.com\r\n\r\n"),
		[]byte("GET / HTTP/1.1\r\nHost: exam_ple.com\r\n\r\n"),
		BuildClientHello("not a host"),
		BuildClientHello("*.example.com"),
	}
	for _, raw := range cases {
		host, replay := Peek(bytes.NewReader(raw), time.Second)
		if host != "" {
			t.Fatalf("host = %q, want empty for %q", host, raw)
		}
		got, err := io.ReadAll(replay)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, raw) {
			t.Fatal("malformed flight was not replayed")
		}
	}
}

func TestPeekTLSVersionAndEncryptedServerName(t *testing.T) {
	sni := extension(sniExtension, sniPayload("origin.example"))
	publicSNI := extension(sniExtension, sniPayload("public.example"))
	tls13 := extension(extSupportedVersions, supportedVersionsPayload(0x7a7a, tls13, tls12))

	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "tls 1.3 cleartext sni",
			raw:  testClientHello(tls12, append(tls13, sni...)),
			want: "origin.example",
		},
		{
			name: "tls 1.0 cleartext sni",
			raw:  testClientHello(tls10, sni),
			want: "origin.example",
		},
		{
			name: "ssl 3 legacy version",
			raw:  testClientHello(0x0300, sni),
		},
		{
			name: "ssl 3 record version",
			raw:  withRecordVersion(testClientHello(tls12, sni), 0x0300),
		},
		{
			name: "encrypted client hello outer name",
			raw:  testClientHello(tls12, append(append(tls13, publicSNI...), extension(extEncryptedClientHello, outerECH())...)),
			want: "public.example",
		},
		{
			name: "browser ech grease",
			raw:  testClientHello(tls12, append(append(tls13, sni...), extension(extEncryptedClientHello, greaseECH())...)),
			want: "origin.example",
		},
		{
			name: "encrypted client hello inner name",
			raw:  testClientHello(tls12, append(append(tls13, sni...), extension(extEncryptedClientHello, []byte{1})...)),
			want: "origin.example",
		},
		{
			name: "legacy encrypted server name",
			raw:  testClientHello(tls12, append(sni, extension(extEncryptedServerName, []byte{0})...)),
		},
		{
			name: "malformed extension block",
			raw:  testClientHello(tls12, append(append([]byte(nil), sni...), 0x00, 0x0b, 0x00, 0x08)),
		},
		{
			name: "malformed supported versions",
			raw:  testClientHello(tls12, append(extension(extSupportedVersions, []byte{0xff}), sni...)),
		},
		{
			name: "only grease versions",
			raw:  testClientHello(tls12, append(extension(extSupportedVersions, supportedVersionsPayload(0x0a0a)), sni...)),
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			host, replay := Peek(bytes.NewReader(tt.raw), time.Second)
			if host != tt.want {
				t.Fatalf("host = %q, want %q", host, tt.want)
			}
			got, err := io.ReadAll(replay)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tt.raw) {
				t.Fatal("flight was not replayed")
			}
		})
	}
}

func testClientHello(legacy uint16, exts []byte) []byte {
	body := []byte{byte(legacy >> 8), byte(legacy)}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0) // session id
	body = append(body, 0x00, 0x02, 0x00, 0x2f)
	body = append(body, 0x01, 0x00) // compression
	body = append(body, byte(len(exts)>>8), byte(len(exts)))
	body = append(body, exts...)

	hs := []byte{tlsClientHello, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	hs = append(hs, body...)
	rec := []byte{tlsHandshake, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}
	return append(rec, hs...)
}

func withRecordVersion(record []byte, version uint16) []byte {
	out := append([]byte(nil), record...)
	out[1] = byte(version >> 8)
	out[2] = byte(version)
	return out
}

func extension(typ int, data []byte) []byte {
	out := []byte{byte(typ >> 8), byte(typ), byte(len(data) >> 8), byte(len(data))}
	return append(out, data...)
}

func sniPayload(name string) []byte {
	listLen := 3 + len(name)
	out := []byte{byte(listLen >> 8), byte(listLen), sniHostName, byte(len(name) >> 8), byte(len(name))}
	return append(out, name...)
}

func supportedVersionsPayload(versions ...uint16) []byte {
	out := []byte{byte(len(versions) * 2)}
	for _, version := range versions {
		out = append(out, byte(version>>8), byte(version))
	}
	return out
}

// outerECH is a structurally valid ClientHelloOuter extension body.
func outerECH() []byte {
	return []byte{
		0,          // outer
		0x00, 0x01, // kdf
		0x00, 0x01, // aead
		0x0a,       // config_id
		0x00, 0x00, // empty enc
		0x00, 0x01, // payload length
		0xff, // encrypted inner hello
	}
}

// greaseECH is a ClientHelloOuter-shaped encrypted_client_hello body of the
// kind BoringSSL sends as GREASE: HKDF-SHA256 + AES-128-GCM, plus a fixed
// stand-in for the random config_id, enc, and payload. It is intentionally
// indistinguishable from a real outer hello.
func greaseECH() []byte {
	enc := []byte{
		0x2a, 0x91, 0xc4, 0x07, 0x58, 0xe3, 0x1b, 0x6d,
		0xf0, 0x44, 0x9a, 0x12, 0x77, 0xab, 0x30, 0x5e,
		0x88, 0x19, 0xc6, 0x4f, 0xd2, 0x63, 0x0b, 0xae,
		0x71, 0x95, 0x28, 0xfc, 0x53, 0x14, 0xbe, 0x60,
	}
	payload := []byte{
		0x11, 0x8c, 0x3e, 0x70, 0xa4, 0x55, 0xd9, 0x02,
		0x6b, 0xe7, 0x18, 0x9f, 0x4c, 0x33, 0xca, 0x81,
	}
	out := []byte{
		0,          // outer
		0x00, 0x01, // HKDF-SHA256
		0x00, 0x01, // AES-128-GCM
		0x4e, // config_id
		0x00, byte(len(enc)),
	}
	out = append(out, enc...)
	out = append(out, byte(len(payload)>>8), byte(len(payload)))
	return append(out, payload...)
}

func TestAcceptName(t *testing.T) {
	if got := acceptName("LocalHost"); got != "localhost" {
		t.Fatalf("acceptName = %q", got)
	}
	if got := acceptName("2001:db8::1"); got != "" {
		t.Fatalf("ipv6 literal accepted as %q", got)
	}
	if got := acceptName(strings.Repeat("a", 64) + ".example"); got != "" {
		t.Fatal("oversized label accepted")
	}
}

type oneByteReader struct {
	rest []byte
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.rest) == 0 {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.rest[0]
	r.rest = r.rest[1:]
	return 1, nil
}

// onceReader fails a second read so a non-HTTP flight cannot be pulled past
// the first packet.
type onceReader struct {
	data  []byte
	reads int
}

func (r *onceReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads > 1 {
		return 0, errors.New("second read")
	}
	return copy(p, r.data), nil
}
