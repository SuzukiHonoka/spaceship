package sniff

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	QUICVersion1 uint32 = 0x00000001
	QUICVersion2 uint32 = 0x6b3343cf

	quicMaxCIDLen       = 20
	quicMaxFrames       = 128
	quicMaxCryptoBuffer = maxTLSRecord
	quicGCMTagSize      = 16
)

var (
	// RFC 9001 §5.2
	quicV1Salt = []byte{
		0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17,
		0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a,
	}
	// RFC 9369 §3.3.1
	quicV2Salt = []byte{
		0x0d, 0xed, 0xe3, 0xde, 0xf7, 0x00, 0xa6, 0xdb, 0x81, 0x93,
		0x81, 0xbe, 0x6e, 0x26, 0x9d, 0xcb, 0xf9, 0xbd, 0x2e, 0xd9,
	}
)

// QUICServerName extracts the TLS SNI hostname from a QUIC v1 (RFC 9000/9001)
// or QUIC v2 (RFC 9369) Initial UDP datagram. Non-QUIC, non-Initial, and
// malformed datagrams return "" without panicking or allocating on the fast
// rejection path.
func QUICServerName(pkt []byte) string {
	// Long Header fixed bits: 0x80 (Header Form = 1) | 0x40 (Fixed Bit = 1).
	if len(pkt) < 28 || pkt[0]&0xc0 != 0xc0 {
		return ""
	}
	version := binary.BigEndian.Uint32(pkt[1:5])
	pktType := (pkt[0] & 0x30) >> 4

	var (
		salt     []byte
		keyLabel string
		ivLabel  string
		hpLabel  string
	)
	switch version {
	case QUICVersion1:
		if pktType != 0x00 {
			return ""
		}
		salt = quicV1Salt
		keyLabel, ivLabel, hpLabel = "quic key", "quic iv", "quic hp"
	case QUICVersion2:
		if pktType != 0x01 {
			return ""
		}
		salt = quicV2Salt
		keyLabel, ivLabel, hpLabel = "quicv2 key", "quicv2 iv", "quicv2 hp"
	default:
		return ""
	}

	pos := 5
	if pos >= len(pkt) {
		return ""
	}
	dcidLen := int(pkt[pos])
	pos++
	if dcidLen > quicMaxCIDLen || pos+dcidLen >= len(pkt) {
		return ""
	}
	dcid := pkt[pos : pos+dcidLen]
	pos += dcidLen

	scidLen := int(pkt[pos])
	pos++
	if scidLen > quicMaxCIDLen || pos+scidLen >= len(pkt) {
		return ""
	}
	pos += scidLen

	tokenLen, n := readQUICVarint(pkt[pos:])
	if n == 0 || tokenLen > uint64(len(pkt)-pos-n) {
		return ""
	}
	pos += n + int(tokenLen)

	payloadLen, n := readQUICVarint(pkt[pos:])
	if n == 0 || payloadLen < 4+quicGCMTagSize || payloadLen > uint64(len(pkt)-pos-n) {
		return ""
	}
	pos += n
	pnOffset := pos
	packetEnd := pnOffset + int(payloadLen)

	// Header protection sample is 16 bytes starting 4 bytes after pnOffset.
	if pnOffset+4+16 > packetEnd {
		return ""
	}
	sample := pkt[pnOffset+4 : pnOffset+4+16]

	key, iv, hp := deriveQUICInitialKeys(dcid, salt, keyLabel, ivLabel, hpLabel)
	hpBlock, err := aes.NewCipher(hp)
	if err != nil {
		return ""
	}
	var mask [16]byte
	hpBlock.Encrypt(mask[:], sample)

	firstByte := pkt[0] ^ (mask[0] & 0x0f)
	pnLen := int(firstByte&0x03) + 1
	if pnOffset+pnLen+quicGCMTagSize > packetEnd {
		return ""
	}

	aad := make([]byte, pnOffset+pnLen)
	copy(aad, pkt[:pnOffset])
	aad[0] = firstByte
	var pn uint64
	for i := range pnLen {
		b := pkt[pnOffset+i] ^ mask[1+i]
		aad[pnOffset+i] = b
		pn = (pn << 8) | uint64(b)
	}

	var nonce [12]byte
	copy(nonce[:], iv)
	for i := range 8 {
		nonce[11-i] ^= byte(pn >> (8 * i))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ""
	}
	plaintext, err := gcm.Open(nil, nonce[:], pkt[pnOffset+pnLen:packetEnd], aad)
	if err != nil {
		return ""
	}

	cryptoPayload := reassembleQUICCrypto(plaintext)
	if len(cryptoPayload) < 4 {
		return ""
	}
	legacy, exts, ok := clientHelloHandshakeExtensions(cryptoPayload)
	if !ok {
		return ""
	}
	return extractTLSServerName(legacy, exts)
}

func deriveQUICInitialKeys(dcid, salt []byte, keyLabel, ivLabel, hpLabel string) (key, iv, hp []byte) {
	initialSecret := hkdf.Extract(sha256.New, dcid, salt)
	clientSecret := hkdfExpandLabel(initialSecret, "client in", 32)
	key = hkdfExpandLabel(clientSecret, keyLabel, 16)
	iv = hkdfExpandLabel(clientSecret, ivLabel, 12)
	hp = hkdfExpandLabel(clientSecret, hpLabel, 16)
	return key, iv, hp
}

// hkdfExpandLabel implements TLS 1.3 HKDF-Expand-Label (RFC 8446 §7.1).
func hkdfExpandLabel(secret []byte, label string, length int) []byte {
	const prefix = "tls13 "
	fullLen := len(prefix) + len(label)
	info := make([]byte, 2+1+fullLen+1)
	binary.BigEndian.PutUint16(info[:2], uint16(length))
	info[2] = byte(fullLen)
	copy(info[3:], prefix)
	copy(info[3+len(prefix):], label)
	info[3+fullLen] = 0 // empty context

	out := make([]byte, length)
	r := hkdf.Expand(sha256.New, secret, info)
	_, _ = io.ReadFull(r, out)
	return out
}

func readQUICVarint(b []byte) (uint64, int) {
	if len(b) == 0 {
		return 0, 0
	}
	prefix := b[0] >> 6
	length := 1 << prefix
	if len(b) < length {
		return 0, 0
	}
	v := uint64(b[0] & 0x3f)
	for i := 1; i < length; i++ {
		v = (v << 8) | uint64(b[i])
	}
	return v, length
}

func appendQUICVarint(dst []byte, v uint64) []byte {
	switch {
	case v < (1 << 6):
		return append(dst, byte(v))
	case v < (1 << 14):
		return append(dst, byte(0x40|(v>>8)), byte(v))
	case v < (1 << 30):
		return append(dst, byte(0x80|(v>>24)), byte(v>>16), byte(v>>8), byte(v))
	default:
		return append(dst,
			byte(0xc0|(v>>56)), byte(v>>48), byte(v>>40), byte(v>>32),
			byte(v>>24), byte(v>>16), byte(v>>8), byte(v),
		)
	}
}

// reassembleQUICCrypto extracts and reassembles CRYPTO (0x06) frames starting
// at offset 0 from a decrypted QUIC Initial payload.
func reassembleQUICCrypto(payload []byte) []byte {
	var (
		assembled []byte
		maxEnd    int
		hasZero   bool
	)
	pos := 0
	for frames := 0; pos < len(payload) && frames < quicMaxFrames; frames++ {
		frameType := payload[pos]
		pos++
		switch frameType {
		case 0x00: // PADDING
			for pos < len(payload) && payload[pos] == 0x00 {
				pos++
			}
		case 0x01: // PING
		case 0x02, 0x03: // ACK / ACK_ECN
			// Skip Largest Acked, Ack Delay, Ack Range Count, First Ack Range
			for range 4 {
				_, n := readQUICVarint(payload[pos:])
				if n == 0 {
					return assembled[:maxEnd]
				}
				pos += n
			}
			// Rewind slightly isn't needed if we decode range count properly:
			return assembled[:maxEnd]
		case 0x06: // CRYPTO
			off, n := readQUICVarint(payload[pos:])
			if n == 0 {
				return nil
			}
			pos += n
			length, n := readQUICVarint(payload[pos:])
			if n == 0 || length > uint64(len(payload)-pos-n) {
				return nil
			}
			pos += n
			end := off + length
			if end > quicMaxCryptoBuffer {
				return nil
			}
			if off == 0 {
				hasZero = true
			}
			if int(end) > len(assembled) {
				grown := make([]byte, int(end))
				copy(grown, assembled)
				assembled = grown
			}
			copy(assembled[int(off):int(end)], payload[pos:pos+int(length)])
			if int(end) > maxEnd {
				maxEnd = int(end)
			}
			pos += int(length)
		default:
			// Unknown/unsupported frame inside Initial payload: stop parsing.
			if hasZero {
				return assembled[:maxEnd]
			}
			return nil
		}
	}
	if !hasZero {
		return nil
	}
	return assembled[:maxEnd]
}

// BuildQUICInitial constructs a valid encrypted QUIC v1 or v2 Initial packet
// carrying a TLS 1.3 ClientHello with the given SNI hostname. Used by unit and
// integration tests.
func BuildQUICInitial(serverName string, version uint32) ([]byte, error) {
	record := BuildClientHello(serverName)
	if len(record) < 9 {
		return nil, errors.New("sniff: invalid server name for QUIC Initial")
	}
	// Strip the 5-byte TLS record header to obtain the raw ClientHello handshake message.
	clientHello := record[5:]

	var (
		salt      []byte
		pktType   byte
		keyLabel  string
		ivLabel   string
		hpLabel   string
	)
	switch version {
	case QUICVersion1:
		salt = quicV1Salt
		pktType = 0x00
		keyLabel, ivLabel, hpLabel = "quic key", "quic iv", "quic hp"
	case QUICVersion2:
		salt = quicV2Salt
		pktType = 0x01
		keyLabel, ivLabel, hpLabel = "quicv2 key", "quicv2 iv", "quicv2 hp"
	default:
		return nil, errors.New("sniff: unsupported QUIC version")
	}

	dcid := []byte{0x83, 0x94, 0xc8, 0xf0, 0x3e, 0x51, 0x57, 0x08}
	scid := []byte{0x01, 0x02, 0x03, 0x04}

	// Build plaintext: CRYPTO frame at offset 0 + padding so ciphertext >= 20 bytes.
	var plaintext []byte
	plaintext = append(plaintext, 0x06) // CRYPTO frame
	plaintext = appendQUICVarint(plaintext, 0)
	plaintext = appendQUICVarint(plaintext, uint64(len(clientHello)))
	plaintext = append(plaintext, clientHello...)
	for len(plaintext) < 64 {
		plaintext = append(plaintext, 0x00)
	}

	pnLen := 2
	pn := uint64(1)
	firstByte := byte(0xc0) | (pktType << 4) | byte(pnLen-1)

	var hdr []byte
	hdr = append(hdr, firstByte)
	hdr = binary.BigEndian.AppendUint32(hdr, version)
	hdr = append(hdr, byte(len(dcid)))
	hdr = append(hdr, dcid...)
	hdr = append(hdr, byte(len(scid)))
	hdr = append(hdr, scid...)
	hdr = appendQUICVarint(hdr, 0) // token length = 0
	payloadLen := uint64(pnLen + len(plaintext) + quicGCMTagSize)
	hdr = appendQUICVarint(hdr, payloadLen)
	pnOffset := len(hdr)
	hdr = append(hdr, byte(pn>>8), byte(pn))

	key, iv, hp := deriveQUICInitialKeys(dcid, salt, keyLabel, ivLabel, hpLabel)
	var nonce [12]byte
	copy(nonce[:], iv)
	nonce[10] ^= byte(pn >> 8)
	nonce[11] ^= byte(pn)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce[:], plaintext, hdr)

	pkt := make([]byte, len(hdr)+len(ciphertext))
	copy(pkt, hdr)
	copy(pkt[len(hdr):], ciphertext)

	// Apply header protection.
	hpBlock, err := aes.NewCipher(hp)
	if err != nil {
		return nil, err
	}
	var mask [16]byte
	hpBlock.Encrypt(mask[:], pkt[pnOffset+4:pnOffset+4+16])
	pkt[0] ^= mask[0] & 0x0f
	for i := range pnLen {
		pkt[pnOffset+i] ^= mask[1+i]
	}
	return pkt, nil
}
