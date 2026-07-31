//go:build linux

package tun

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestValidateTUNDescriptorRejectsOrdinaryFD(t *testing.T) {
	fds := []int{-1, -1}
	if err := unix.Pipe2(fds, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])

	if _, err := validateTUNDescriptor(fds[0]); err == nil {
		t.Fatal("validateTUNDescriptor accepted a pipe")
	}
}

func TestNewRejectsOrdinaryExternalDescriptorWithoutClosingOriginal(t *testing.T) {
	fds := []int{-1, -1}
	if err := unix.Pipe2(fds, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])

	if _, err := New(context.Background(), Config{
		FileDescriptor: &fds[0],
	}); err == nil || !strings.Contains(err.Error(), "not an attached TUN device") {
		t.Fatalf("New() error = %v, want invalid TUN descriptor", err)
	}
	if _, err := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFD, 0); err != nil {
		t.Fatalf("New() closed the caller-owned descriptor: %v", err)
	}
}

func TestLinuxInterfaceHelpersRejectInvalidDevices(t *testing.T) {
	if err := configureCreatedInterface(strings.Repeat("x", 16), DefaultMTU); err == nil {
		t.Fatal("configureCreatedInterface accepted an overlong interface name")
	}
	if _, err := interfaceMTU("ss-no-such-dev"); err == nil {
		t.Fatal("interfaceMTU accepted a nonexistent interface")
	}
}

func TestOwnedDescriptorCloseIsIdempotent(t *testing.T) {
	fds := []int{-1, -1}
	if err := unix.Pipe2(fds, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])

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
