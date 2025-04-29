//go:build !windows

package indicator

import "syscall"

func SIGWINCH() syscall.Signal {
	return syscall.SIGWINCH
}
