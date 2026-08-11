//go:build !linux

package transport

import (
	"fmt"
	"syscall"
)

func outboundSocketControl(_, _ string, _ syscall.RawConn) error {
	if mark := bypassMark.Load(); mark != 0 {
		return fmt.Errorf("outbound socket mark %#x is unsupported on this platform", mark)
	}
	return nil
}

func verifyBypassMark(mark uint32) error {
	return fmt.Errorf("outbound socket mark %#x is unsupported on this platform", mark)
}
