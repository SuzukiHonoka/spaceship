//go:build linux

package transport

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// trySetBypassMark applies mark to a throwaway datagram socket of the given
// address family. The first result reports whether the probe socket could be
// opened at all, which separates "this host has no such family" from "the
// kernel refused the mark".
func trySetBypassMark(family int, mark uint32) (attempted bool, err error) {
	fd, err := unix.Socket(family, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return false, nil
	}
	defer func() { _ = unix.Close(fd) }()
	return true, unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_MARK, int(mark))
}

// verifyBypassMark reports whether this process may set SO_MARK. That is a
// SOL_SOCKET option, so either address family answers the question; both are
// tried so a host without IPv4 (or without IPv6) is still verified rather than
// silently skipped. Being unable to open any probe socket is deliberately not
// an error: such an environment cannot dial at all and reports a clearer
// failure on its own. Only a rejected SO_MARK — the missing-capability case
// this exists to catch — is returned.
func verifyBypassMark(mark uint32) error {
	for _, family := range [...]int{unix.AF_INET, unix.AF_INET6} {
		if attempted, err := trySetBypassMark(family, mark); attempted {
			return err
		}
	}
	return nil
}

func outboundSocketControl(_, _ string, raw syscall.RawConn) error {
	mark := bypassMark.Load()
	if mark == 0 {
		return nil
	}

	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark))
	}); err != nil {
		return fmt.Errorf("access outbound socket for SO_MARK: %w", err)
	}
	if socketErr != nil {
		return fmt.Errorf("set outbound SO_MARK %#x: %w", mark, socketErr)
	}
	return nil
}
