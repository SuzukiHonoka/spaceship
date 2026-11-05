//go:build linux && !android

package tun

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestValidateTUNDescriptorRejectsOrdinaryFD(t *testing.T) {
	fds := []int{-1, -1}
	if err := unix.Pipe2(fds, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fds[0]) }()
	defer func() { _ = unix.Close(fds[1]) }()

	if _, err := validateTUNDescriptor(fds[0]); err == nil {
		t.Fatal("validateTUNDescriptor accepted a pipe")
	}
}

func TestNewRejectsOrdinaryExternalDescriptorWithoutClosingOriginal(t *testing.T) {
	fds := []int{-1, -1}
	if err := unix.Pipe2(fds, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fds[0]) }()
	defer func() { _ = unix.Close(fds[1]) }()

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
