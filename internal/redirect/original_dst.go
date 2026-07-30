package redirect

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
)

const sockaddrInetSize = 16

func decodeOriginalDestinationIPv4(raw []byte, native binary.ByteOrder, expectedFamily uint16) (*net.TCPAddr, error) {
	if len(raw) < sockaddrInetSize {
		return nil, fmt.Errorf("short sockaddr_in: got %d bytes, want at least %d", len(raw), sockaddrInetSize)
	}
	family := native.Uint16(raw[0:2])
	if family != expectedFamily {
		return nil, fmt.Errorf("unexpected address family %d", family)
	}

	return &net.TCPAddr{
		IP:   net.IPv4(raw[4], raw[5], raw[6], raw[7]),
		Port: int(binary.BigEndian.Uint16(raw[2:4])),
	}, nil
}

func decodeOriginalDestinationIPv6(
	family uint16,
	expectedFamily uint16,
	port uint16,
	rawIP []byte,
	scopeID uint32,
	native binary.ByteOrder,
) (*net.TCPAddr, error) {
	if family != expectedFamily {
		return nil, fmt.Errorf("unexpected address family %d", family)
	}
	if len(rawIP) < net.IPv6len {
		return nil, fmt.Errorf("short IPv6 address: got %d bytes, want %d", len(rawIP), net.IPv6len)
	}

	ip := make(net.IP, net.IPv6len)
	copy(ip, rawIP[:net.IPv6len])
	zone := ""
	if scopeID != 0 {
		// net accepts a numeric interface index as a zone. Keeping the numeric
		// value avoids a second interface lookup and works inside net namespaces.
		zone = strconv.FormatUint(uint64(scopeID), 10)
	}

	return &net.TCPAddr{
		IP:   ip,
		Port: networkPortWithByteOrder(port, native),
		Zone: zone,
	}, nil
}

func networkPortWithByteOrder(port uint16, native binary.ByteOrder) int {
	var raw [2]byte
	native.PutUint16(raw[:], port)
	return int(binary.BigEndian.Uint16(raw[:]))
}

func checkedSocketDescriptor(fd uintptr) (int, error) {
	maxInt := uintptr(^uint(0) >> 1)
	if fd > maxInt {
		return 0, fmt.Errorf("socket descriptor %d exceeds int range", fd)
	}
	// #nosec G115 -- the explicit bound above proves the conversion fits.
	return int(fd), nil
}
