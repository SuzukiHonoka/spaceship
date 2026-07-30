//go:build linux

package redirect

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// Supported reports whether this binary can serve Linux netfilter REDIRECT
// traffic.
func Supported() bool {
	return true
}

func originalDestination(conn net.Conn) (dst *net.TCPAddr, err error) {
	remote, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return nil, errors.New("connection is not TCP")
	}
	syscallConn, ok := conn.(syscall.Conn)
	if !ok {
		return nil, errors.New("connection does not expose its socket")
	}

	rawConn, err := syscallConn.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("access socket: %w", err)
	}

	var socketErr error
	if err := rawConn.Control(func(fd uintptr) {
		socketFD, err := checkedSocketDescriptor(fd)
		if err != nil {
			socketErr = err
			return
		}
		if remote.IP.To4() != nil {
			dst, socketErr = originalDestinationIPv4(socketFD)
			return
		}
		dst, socketErr = originalDestinationIPv6(socketFD)
	}); err != nil {
		return nil, fmt.Errorf("inspect socket: %w", err)
	}
	if socketErr != nil {
		return nil, socketErr
	}
	return dst, nil
}

func originalDestinationIPv4(fd int) (*net.TCPAddr, error) {
	// SO_ORIGINAL_DST returns sockaddr_in (16 bytes). IPv6Mreq is a safe,
	// exported 20-byte carrier in x/sys/unix; the kernel writes the returned
	// sockaddr into its first 16 bytes.
	value, err := unix.GetsockoptIPv6Mreq(fd, unix.IPPROTO_IP, unix.SO_ORIGINAL_DST)
	if err != nil {
		return nil, fmt.Errorf("read IPv4 SO_ORIGINAL_DST: %w", err)
	}
	raw := value.Multiaddr
	dst, err := decodeOriginalDestinationIPv4(raw[:], binary.NativeEndian, unix.AF_INET)
	if err != nil {
		return nil, fmt.Errorf("read IPv4 SO_ORIGINAL_DST: %w", err)
	}
	return dst, nil
}

func originalDestinationIPv6(fd int) (*net.TCPAddr, error) {
	// SO_ORIGINAL_DST returns sockaddr_in6 (28 bytes). IPv6MTUInfo is a safe,
	// exported 32-byte carrier whose first field is that exact sockaddr type.
	value, err := unix.GetsockoptIPv6MTUInfo(fd, unix.IPPROTO_IPV6, unix.SO_ORIGINAL_DST)
	if err != nil {
		return nil, fmt.Errorf("read IPv6 SO_ORIGINAL_DST: %w", err)
	}
	dst, err := decodeOriginalDestinationIPv6(
		value.Addr.Family,
		unix.AF_INET6,
		value.Addr.Port,
		value.Addr.Addr[:],
		value.Addr.Scope_id,
		binary.NativeEndian,
	)
	if err != nil {
		return nil, fmt.Errorf("read IPv6 SO_ORIGINAL_DST: %w", err)
	}
	return dst, nil
}
