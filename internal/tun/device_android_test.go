//go:build android

package tun

import (
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAndroidOpenRequiresExternalFD(t *testing.T) {
	_, err := openTUNDevice(Config{MTU: DefaultMTU}, func(error) {})
	if err == nil || !strings.Contains(err.Error(), "file descriptor") {
		t.Fatalf("openTUNDevice() error = %v, want a missing file descriptor", err)
	}
}

func TestAndroidOpenUsesCallerMTUWithoutIoctl(t *testing.T) {
	fds := []int{-1, -1}
	if err := unix.Pipe2(fds, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fds[0]) }()
	defer func() { _ = unix.Close(fds[1]) }()

	dev, err := openTUNDevice(Config{
		FileDescriptor: &fds[0],
		MTU:            1400,
	}, func(error) {})
	if err != nil {
		for _, forbidden := range []string{"TUNGETIFF", "SIOCGIFMTU", "SIOCSIFFLAGS", "ioctl"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Fatalf("android open used an ioctl (%s): %v", forbidden, err)
			}
		}
		if _, statErr := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFD, 0); statErr != nil {
			t.Fatalf("open closed the caller fd: %v (open err %v)", statErr, err)
		}
		return
	}
	t.Cleanup(func() {
		if dev.closer != nil {
			_ = dev.closer.Close()
		}
	})
	if dev.name != androidDeviceName {
		t.Fatalf("name = %q, want %q", dev.name, androidDeviceName)
	}
	if dev.mtu != 1400 {
		t.Fatalf("mtu = %d, want 1400", dev.mtu)
	}
	if err := dev.closer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, statErr := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFD, 0); statErr != nil {
		t.Fatalf("closing the duplicate released the caller fd: %v", statErr)
	}
}

func TestAndroidOpenKeepsConfiguredName(t *testing.T) {
	fds := []int{-1, -1}
	if err := unix.Pipe2(fds, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fds[0]) }()
	defer func() { _ = unix.Close(fds[1]) }()

	dev, err := openTUNDevice(Config{
		Name:           "vpn0",
		FileDescriptor: &fds[0],
		MTU:            DefaultMTU,
	}, func(error) {})
	if err != nil {
		for _, forbidden := range []string{"TUNGETIFF", "SIOCGIFMTU", "SIOCSIFFLAGS"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Fatalf("android open used an ioctl (%s): %v", forbidden, err)
			}
		}
		return
	}
	t.Cleanup(func() {
		if dev.closer != nil {
			_ = dev.closer.Close()
		}
	})
	if dev.name != "vpn0" {
		t.Fatalf("name = %q, want vpn0", dev.name)
	}
}
