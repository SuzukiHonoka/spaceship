//go:build linux

package tun

import (
	"sync"

	"golang.org/x/sys/unix"
)

// ownedDescriptor closes the TUN fd Spaceship duplicated. The caller's fd is a
// different descriptor number and stays open.
type ownedDescriptor struct {
	mu  sync.Mutex
	fd  int
	err error
}

func (d *ownedDescriptor) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fd < 0 {
		return d.err
	}
	d.err = unix.Close(d.fd)
	d.fd = -1
	return d.err
}
