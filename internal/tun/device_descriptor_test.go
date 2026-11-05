//go:build linux

package tun

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOwnedDescriptorCloseIsIdempotent(t *testing.T) {
	fds := []int{-1, -1}
	if err := unix.Pipe2(fds, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fds[1]) }()

	owned := &ownedDescriptor{fd: fds[0]}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFD, 0); err == nil {
		t.Fatal("owned descriptor remained open")
	}
}

func TestOwnedDescriptorCachesCloseFailure(t *testing.T) {
	// This value is far above the process descriptor limit and cannot race
	// with descriptor reuse, unlike closing a real FD before the assertion.
	owned := &ownedDescriptor{fd: 1 << 30}
	firstErr := owned.Close()
	if !errors.Is(firstErr, unix.EBADF) {
		t.Fatalf("first Close() error = %v, want EBADF", firstErr)
	}
	if secondErr := owned.Close(); !errors.Is(secondErr, unix.EBADF) {
		t.Fatalf("second Close() error = %v, want cached EBADF", secondErr)
	}
}
