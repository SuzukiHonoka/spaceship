package api

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/tun"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
)

func TestServeTUNRequiresRunningClient(t *testing.T) {
	launcher := NewLauncher()
	fd := 0
	err := launcher.ServeTUN(&client.TUN{FileDescriptor: &fd})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("ServeTUN() error = %v, want client not running", err)
	}
}

func TestStopTUNBeforeLaunchIsIdle(t *testing.T) {
	NewLauncher().StopTUN()
}

func TestServeTUNRejectsNilConfig(t *testing.T) {
	err := NewLauncher().ServeTUN(nil)
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Fatalf("ServeTUN(nil) error = %v", err)
	}
}

func TestCheckInstalledBypassMark(t *testing.T) {
	required := tun.Config{BypassMark: 0x5350, BypassMarkRequired: true}
	if err := checkInstalledBypassMark(required, 0x5350); err != nil {
		t.Fatalf("matching mark refused: %v", err)
	}
	for _, installed := range []uint32{0, 0x99} {
		err := checkInstalledBypassMark(required, installed)
		if err == nil || !strings.Contains(err.Error(), "requires outbound socket mark 0x5350") {
			t.Fatalf("installed %#x: error = %v, want a missing-mark refusal", installed, err)
		}
	}
	// Android VpnService with no mark needs nothing installed, and does not
	// object to a mark another frontend installed.
	unmarked := tun.Config{}
	for _, installed := range []uint32{0, 0x5350} {
		if err := checkInstalledBypassMark(unmarked, installed); err != nil {
			t.Fatalf("unmarked TUN with installed %#x refused: %v", installed, err)
		}
	}
}

// The default TUN mark is required everywhere but Android. A client launched
// without a tun section installs no mark, so attaching a TUN later must fail
// before any device is opened.
func TestServeTUNRefusesRequiredMarkTheClientDidNotInstall(t *testing.T) {
	if runtime.GOOS == "android" {
		t.Skip("Android TUN needs no mark by default")
	}
	original := transport.BypassMark()
	t.Cleanup(func() { transport.SetBypassMark(original) })
	transport.SetBypassMark(0)

	launcher := NewLauncher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launcher.beginRun(ctx, false, false)
	defer launcher.clearRunState()

	err := launcher.ServeTUN(&client.TUN{RouteMode: "manual"})
	if err == nil || !strings.Contains(err.Error(), "requires outbound socket mark") {
		t.Fatalf("ServeTUN() error = %v, want a missing-mark refusal", err)
	}
}

// An explicit mark is required on every platform, Android included.
func TestServeTUNRefusesExplicitMarkMismatch(t *testing.T) {
	original := transport.BypassMark()
	t.Cleanup(func() { transport.SetBypassMark(original) })
	transport.SetBypassMark(0)

	launcher := NewLauncher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launcher.beginRun(ctx, false, false)
	defer launcher.clearRunState()

	fd := 0
	mark := uint32(0x77)
	err := launcher.ServeTUN(&client.TUN{RouteMode: "manual", FileDescriptor: &fd, BypassMark: &mark})
	if err == nil || !strings.Contains(err.Error(), "requires outbound socket mark 0x77") {
		t.Fatalf("ServeTUN() error = %v, want a mark mismatch refusal", err)
	}
}

// A mark the client already installed lets ServeTUN proceed to opening the
// device, so the refusal above is specific to the mark.
func TestServeTUNAcceptsInstalledMark(t *testing.T) {
	if runtime.GOOS == "android" {
		t.Skip("Android TUN needs no mark by default")
	}
	original := transport.BypassMark()
	t.Cleanup(func() { transport.SetBypassMark(original) })
	transport.SetBypassMark(tun.DefaultBypassMark)

	launcher := NewLauncher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launcher.beginRun(ctx, false, false)
	defer launcher.clearRunState()

	// The fd passes config validation but is not open, so the device open
	// fails after the mark check without touching the host.
	fd := 1 << 20
	err := launcher.ServeTUN(&client.TUN{RouteMode: "manual", FileDescriptor: &fd})
	if err == nil {
		launcher.StopTUN()
		t.Fatal("ServeTUN() adopted an unopened descriptor")
	}
	if strings.Contains(err.Error(), "outbound socket mark") {
		t.Fatalf("ServeTUN() refused an installed mark: %v", err)
	}
	t.Logf("device stage error: %v", err)
}
