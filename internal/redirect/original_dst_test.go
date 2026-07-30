package redirect

import (
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestDecodeOriginalDestinationIPv4(t *testing.T) {
	for _, tt := range []struct {
		name   string
		native binary.ByteOrder
	}{
		{name: "little endian", native: binary.LittleEndian},
		{name: "big endian", native: binary.BigEndian},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := make([]byte, sockaddrInetSize)
			tt.native.PutUint16(raw[0:2], 2)
			binary.BigEndian.PutUint16(raw[2:4], 8443)
			copy(raw[4:8], net.IPv4(192, 0, 2, 25).To4())

			got, err := decodeOriginalDestinationIPv4(raw, tt.native, 2)
			if err != nil {
				t.Fatal(err)
			}
			if !got.IP.Equal(net.ParseIP("192.0.2.25")) || got.Port != 8443 {
				t.Fatalf("decoded destination = %s, want 192.0.2.25:8443", got)
			}
		})
	}
}

func TestDecodeOriginalDestinationIPv4RejectsMalformedSockaddr(t *testing.T) {
	if _, err := decodeOriginalDestinationIPv4(make([]byte, sockaddrInetSize-1), binary.LittleEndian, 2); err == nil {
		t.Fatal("short sockaddr_in was accepted")
	}

	raw := make([]byte, sockaddrInetSize)
	binary.LittleEndian.PutUint16(raw[0:2], 10)
	if _, err := decodeOriginalDestinationIPv4(raw, binary.LittleEndian, 2); err == nil ||
		!strings.Contains(err.Error(), "address family") {
		t.Fatalf("wrong-family error = %v", err)
	}
}

func TestDecodeOriginalDestinationIPv6(t *testing.T) {
	ip := net.ParseIP("fe80::1234").To16()
	portBytes := [2]byte{0x01, 0xbb}

	for _, tt := range []struct {
		name   string
		native binary.ByteOrder
	}{
		{name: "little endian", native: binary.LittleEndian},
		{name: "big endian", native: binary.BigEndian},
	} {
		t.Run(tt.name, func(t *testing.T) {
			encodedPort := tt.native.Uint16(portBytes[:])
			got, err := decodeOriginalDestinationIPv6(10, 10, encodedPort, ip, 7, tt.native)
			if err != nil {
				t.Fatal(err)
			}
			if !got.IP.Equal(ip) || got.Port != 443 || got.Zone != "7" {
				t.Fatalf("decoded destination = %s, want [fe80::1234%%7]:443", got)
			}
		})
	}
}

func TestDecodeOriginalDestinationIPv6RejectsMalformedSockaddr(t *testing.T) {
	if _, err := decodeOriginalDestinationIPv6(2, 10, 0, make([]byte, net.IPv6len), 0, binary.LittleEndian); err == nil {
		t.Fatal("wrong address family was accepted")
	}
	if _, err := decodeOriginalDestinationIPv6(10, 10, 0, make([]byte, net.IPv6len-1), 0, binary.LittleEndian); err == nil {
		t.Fatal("short IPv6 address was accepted")
	}
}

func TestCheckedSocketDescriptor(t *testing.T) {
	got, err := checkedSocketDescriptor(42)
	if err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Fatalf("checkedSocketDescriptor(42) = %d, want 42", got)
	}

	if strconv.IntSize != 32 && strconv.IntSize != 64 {
		t.Fatalf("unexpected int size %d", strconv.IntSize)
	}
	maxInt := uintptr(^uint(0) >> 1)
	if _, err := checkedSocketDescriptor(maxInt + 1); err == nil {
		t.Fatal("checkedSocketDescriptor accepted a value larger than max int")
	}
}
