// Package sniff recovers a hostname from the first flight of a TCP connection
// when the client gave the proxy only an IP address.
//
// Plain HTTP yields the Host header. HTTPS yields the TLS Server Name
// Indication. Every other protocol is left alone. Peek replays the bytes it
// read, in order, so the origin still sees the original flight. Direct and
// blackhole dial the IP the client supplied. Every other egress dials the
// recovered name, so a proxy route resolves it on the Spaceship server.
//
// A server name is taken from a TLS 1.0–1.3 ClientHello. TLS 1.3 leaves that
// name in cleartext. encrypted_client_hello (0xfe0d) does not suppress it.
// Browsers send that extension as GREASE on ordinary handshakes, and BoringSSL
// builds the GREASE with a real HPKE cipher suite plus random config_id, enc,
// and payload, so it is indistinguishable from a real ClientHelloOuter. A real
// outer name is only the public client-facing placeholder; Peek still returns
// the cleartext SNI. The earlier encrypted_server_name extension encrypts the
// name, and Peek ignores it. HTTP/2 without TLS and QUIC are not inspected.
package sniff

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

const (
	// Timeout is how long Peek waits for the first flight. A client that
	// sends immediately is classified from the bytes already queued; a
	// client that sends nothing falls back to the original IP.
	Timeout = 200 * time.Millisecond

	maxHTTPHeader = 8 * 1024
	maxTLSRecord  = 16 * 1024
	maxSniff      = 5 + maxTLSRecord

	tlsHandshake   = 22
	tlsClientHello = 1
	sniExtension   = 0
	sniHostName    = 0

	// TLS 1.0 through 1.3. 1.3 is offered in supported_versions; the
	// ClientHello legacy_version stays at TLS 1.2.
	tls10 = 0x0301
	tls11 = 0x0302
	tls12 = 0x0303
	tls13 = 0x0304

	extSupportedVersions = 0x002b
	// RFC 9849. Browser GREASE and a real ClientHelloOuter share this type.
	// It does not mean the cleartext SNI is a cover name. A real outer name
	// is only the public client-facing placeholder.
	extEncryptedClientHello = 0xfe0d
	// draft-ietf-tls-esni. The server name is encrypted and any cleartext
	// SNI beside it is not the origin name.
	extEncryptedServerName = 0xffce
)

var httpMethods = []string{
	"GET ",
	"POST ",
	"HEAD ",
	"PUT ",
	"DELETE ",
	"OPTIONS ",
	"CONNECT ",
	"PATCH ",
	"TRACE ",
}

// Peek reads the opening flight of r and reports the hostname it carries.
// The returned reader yields those bytes and then the unread remainder of r.
// A timeout, a short read, or a flight that is not HTTP/1 or TLS leaves host
// empty and still replays whatever was read. r is returned unchanged when
// nothing was read.
func Peek(r io.Reader, timeout time.Duration) (host string, replay io.Reader) {
	if r == nil {
		return "", nil
	}
	defer disarm(arm(r, timeout))

	buf := make([]byte, 0, 512)
	tmp := make([]byte, 1024)
	for len(buf) < maxSniff && wantMore(buf) {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	if len(buf) == 0 {
		return "", r
	}
	return extract(buf), transport.Prepend(r, buf)
}

type deadliner interface {
	SetReadDeadline(time.Time) error
}

func arm(r io.Reader, timeout time.Duration) func() {
	if timeout <= 0 {
		return func() {}
	}
	d, ok := r.(deadliner)
	if !ok {
		return func() {}
	}
	_ = d.SetReadDeadline(time.Now().Add(timeout))
	return func() { _ = d.SetReadDeadline(time.Time{}) }
}

func disarm(restore func()) {
	if restore != nil {
		restore()
	}
}

// wantMore reports whether buf is an incomplete HTTP/1 header block or an
// incomplete TLS record. Any other first byte stops the peek so a non-HTTP
// protocol is not delayed past the flight already in hand.
func wantMore(buf []byte) bool {
	if len(buf) == 0 {
		return true
	}
	if buf[0] == tlsHandshake {
		if len(buf) < 5 {
			return true
		}
		size, ok := tlsRecordSize(buf)
		if !ok {
			return false
		}
		return len(buf) < size
	}
	return httpNeed(buf)
}

func tlsRecordSize(buf []byte) (int, bool) {
	if len(buf) < 5 || buf[0] != tlsHandshake || buf[1] != 3 {
		return 0, false
	}
	// ClientHello records use a TLS 1.0–1.2 version. TLS 1.3 still sends
	// 0x0301 or 0x0303 here; the real version is in the handshake.
	if buf[2] < 1 || buf[2] > 3 {
		return 0, false
	}
	n := int(buf[3])<<8 | int(buf[4])
	if n <= 0 || n > maxTLSRecord {
		return 0, false
	}
	return 5 + n, true
}

func httpNeed(buf []byte) bool {
	if len(buf) >= maxHTTPHeader || !methodPrefix(buf) {
		return false
	}
	if bytes.Contains(buf, []byte("\r\n\r\n")) {
		return false
	}
	if i := bytes.Index(buf, []byte("\r\n")); i >= 0 && !requestLine(buf[:i]) {
		return false
	}
	return true
}

func methodPrefix(buf []byte) bool {
	s := string(buf)
	for _, method := range httpMethods {
		if strings.HasPrefix(method, s) || strings.HasPrefix(s, method) {
			return true
		}
	}
	return false
}

func requestLine(line []byte) bool {
	parts := bytes.Split(line, []byte(" "))
	if len(parts) != 3 {
		return false
	}
	switch string(parts[0]) {
	case "GET", "POST", "HEAD", "PUT", "DELETE", "OPTIONS", "CONNECT", "PATCH", "TRACE":
	default:
		return false
	}
	return bytes.Equal(parts[2], []byte("HTTP/1.0")) || bytes.Equal(parts[2], []byte("HTTP/1.1"))
}

func extract(buf []byte) string {
	if len(buf) == 0 {
		return ""
	}
	if buf[0] == tlsHandshake {
		return tlsServerName(buf)
	}
	return httpHost(buf)
}

func tlsServerName(record []byte) string {
	legacy, exts, ok := clientHelloExtensions(record)
	if !ok {
		return ""
	}
	if _, ok = offeredTLSVersion(legacy, exts); !ok {
		return ""
	}
	if serverNameEncrypted(exts) {
		return ""
	}
	var host string
	if !forEachExtension(exts, func(typ int, data []byte) bool {
		if typ == sniExtension {
			host = sniHost(data)
			return false
		}
		return true
	}) {
		return ""
	}
	return host
}

// clientHelloExtensions returns the ClientHello legacy_version and its
// extension block. ok is false when the record is not a complete ClientHello.
func clientHelloExtensions(record []byte) (legacy uint16, exts []byte, ok bool) {
	size, ok := tlsRecordSize(record)
	if !ok || len(record) < size {
		return 0, nil, false
	}
	hs := record[5:size]
	if len(hs) < 4 || hs[0] != tlsClientHello {
		return 0, nil, false
	}
	hsLen := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
	if hsLen < 0 || len(hs) < 4+hsLen {
		return 0, nil, false
	}
	body := hs[4 : 4+hsLen]
	// legacy_version(2) + random(32)
	if len(body) < 34 {
		return 0, nil, false
	}
	legacy = uint16(body[0])<<8 | uint16(body[1])
	body = body[34:]
	if len(body) < 1 {
		return 0, nil, false
	}
	sidLen := int(body[0])
	if len(body) < 1+sidLen {
		return 0, nil, false
	}
	body = body[1+sidLen:]
	if len(body) < 2 {
		return 0, nil, false
	}
	csLen := int(body[0])<<8 | int(body[1])
	if csLen < 2 || csLen%2 != 0 || len(body) < 2+csLen {
		return 0, nil, false
	}
	body = body[2+csLen:]
	if len(body) < 1 {
		return 0, nil, false
	}
	compLen := int(body[0])
	if compLen < 1 || len(body) < 1+compLen {
		return 0, nil, false
	}
	body = body[1+compLen:]
	if len(body) < 2 {
		return 0, nil, false
	}
	extLen := int(body[0])<<8 | int(body[1])
	body = body[2:]
	if extLen < 4 || len(body) != extLen {
		return 0, nil, false
	}
	return legacy, body, true
}

// offeredTLSVersion reports the newest TLS version the ClientHello actually
// offers. supported_versions wins over legacy_version, and GREASE values are
// ignored. SSL 3.0 and anything newer than TLS 1.3 are rejected.
func offeredTLSVersion(legacy uint16, exts []byte) (uint16, bool) {
	versions, present, ok := supportedVersions(exts)
	if !ok {
		return 0, false
	}
	if present {
		var best uint16
		for _, version := range versions {
			if grease(version) || version < tls10 || version > tls13 || version <= best {
				continue
			}
			best = version
		}
		if best == 0 {
			return 0, false
		}
		return best, true
	}
	if legacy < tls10 || legacy > tls12 {
		return 0, false
	}
	return legacy, true
}

func supportedVersions(exts []byte) (versions []uint16, present, ok bool) {
	malformed := false
	walked := forEachExtension(exts, func(typ int, data []byte) bool {
		if typ != extSupportedVersions {
			return true
		}
		present = true
		if len(data) < 3 || int(data[0]) != len(data)-1 || data[0]%2 != 0 {
			malformed = true
			return false
		}
		versions = make([]uint16, 0, data[0]/2)
		for i := 1; i < len(data); i += 2 {
			versions = append(versions, uint16(data[i])<<8|uint16(data[i+1]))
		}
		return true
	})
	if !walked || malformed {
		return nil, present, false
	}
	return versions, present, true
}

// serverNameEncrypted reports whether the cleartext SNI must be ignored.
// encrypted_server_name (0xffce) encrypts the name, so the cleartext SNI
// beside it is not the origin. A malformed extension block is unusable.
//
// encrypted_client_hello (0xfe0d) does not suppress SNI. Browsers send a
// ClientHelloOuter-shaped GREASE extension on ordinary handshakes. BoringSSL
// GREASE uses a real HPKE cipher suite plus random config_id, enc, and
// payload, so it is indistinguishable from a real ClientHelloOuter. The
// extension is not proof the cleartext name is a cover name. A real Encrypted
// Client Hello outer name is only the public client-facing placeholder; Peek
// still returns that cleartext name. An inner ECH hello (type 1, empty body)
// exists only after decryption and may keep its SNI.
func serverNameEncrypted(exts []byte) bool {
	encrypted := false
	if !forEachExtension(exts, func(typ int, _ []byte) bool {
		if typ == extEncryptedServerName {
			encrypted = true
			return false
		}
		return true
	}) {
		return true
	}
	return encrypted
}

func forEachExtension(exts []byte, fn func(typ int, data []byte) bool) bool {
	for len(exts) > 0 {
		if len(exts) < 4 {
			return false
		}
		typ := int(exts[0])<<8 | int(exts[1])
		n := int(exts[2])<<8 | int(exts[3])
		exts = exts[4:]
		if n < 0 || len(exts) < n {
			return false
		}
		if !fn(typ, exts[:n]) {
			return true
		}
		exts = exts[n:]
	}
	return true
}

func grease(version uint16) bool {
	hi := byte(version >> 8)
	lo := byte(version)
	return hi == lo && hi&0x0f == 0x0a
}

func sniHost(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	listLen := int(data[0])<<8 | int(data[1])
	data = data[2:]
	if listLen < 3 || len(data) < listLen {
		return ""
	}
	data = data[:listLen]
	if data[0] != sniHostName {
		return ""
	}
	n := int(data[1])<<8 | int(data[2])
	data = data[3:]
	if n <= 0 || len(data) < n {
		return ""
	}
	return acceptName(string(data[:n]))
}

func httpHost(buf []byte) string {
	end := bytes.Index(buf, []byte("\r\n\r\n"))
	if end < 0 {
		return ""
	}
	block := buf[:end]
	lineEnd := bytes.Index(block, []byte("\r\n"))
	if lineEnd < 0 || !requestLine(block[:lineEnd]) {
		return ""
	}
	rest := block[lineEnd+2:]
	for len(rest) > 0 {
		nl := bytes.Index(rest, []byte("\r\n"))
		var line []byte
		if nl < 0 {
			line = rest
			rest = nil
		} else {
			line = rest[:nl]
			rest = rest[nl+2:]
		}
		name, value, ok := splitHeader(line)
		if ok && strings.EqualFold(name, "host") {
			return acceptName(hostWithoutPort(value))
		}
	}
	return ""
}

func splitHeader(line []byte) (string, string, bool) {
	colon := bytes.IndexByte(line, ':')
	if colon <= 0 {
		return "", "", false
	}
	return string(line[:colon]), string(line[colon+1:]), true
}

func hostWithoutPort(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "[") {
		if host, _, err := net.SplitHostPort(value); err == nil {
			return host
		}
		if strings.HasSuffix(value, "]") {
			return value[1 : len(value)-1]
		}
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

// BuildClientHello returns a TLS 1.2 ClientHello whose SNI is serverName.
// An empty name returns a nil slice. Peek recovers a normalized serverName
// from the result.
func BuildClientHello(serverName string) []byte {
	if serverName == "" {
		return nil
	}
	name := []byte(serverName)
	// A non-SNI extension sits in front of SNI so the parser has to skip it.
	pointFormats := []byte{0x00, 0x0b, 0x00, 0x02, 0x01, 0x00}
	listLen := 3 + len(name)
	extDataLen := 2 + listLen
	sniExt := make([]byte, 0, 9+len(name))
	sniExt = append(sniExt,
		0x00, 0x00,
		byte(extDataLen>>8), byte(extDataLen),
		byte(listLen>>8), byte(listLen),
		sniHostName,
		byte(len(name)>>8), byte(len(name)),
	)
	sniExt = append(sniExt, name...)
	ext := append(pointFormats, sniExt...)

	body := make([]byte, 0, 40+len(ext))
	body = append(body, 0x03, 0x03)
	body = append(body, make([]byte, 32)...)
	body = append(body, 0) // empty session id
	body = append(body, 0x00, 0x02, 0x00, 0x2f)
	body = append(body, 0x01, 0x00) // null compression
	body = append(body, byte(len(ext)>>8), byte(len(ext)))
	body = append(body, ext...)

	hs := make([]byte, 0, 4+len(body))
	hs = append(hs, tlsClientHello, byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	hs = append(hs, body...)

	rec := make([]byte, 0, 5+len(hs))
	rec = append(rec, tlsHandshake, 0x03, 0x01, byte(len(hs)>>8), byte(len(hs)))
	return append(rec, hs...)
}

// acceptName reports a normalized DNS name. IP literals and values that are
// not hostnames are dropped: they do not identify a name beyond the address
// the client already supplied.
func acceptName(raw string) string {
	name := utils.NormalizeHost(raw)
	if name == "" || len(name) > 253 {
		return ""
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return ""
	}
	labels := strings.Split(name, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return ""
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			case c == '-' && i > 0 && i < len(label)-1:
			default:
				return ""
			}
		}
	}
	return name
}
