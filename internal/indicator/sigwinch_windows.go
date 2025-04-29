//go:build windows
// +build windows

package indicator

import "syscall"

func SIGWINCH() syscall.Signal {
	return -1
}
