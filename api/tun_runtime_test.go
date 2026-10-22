package api

import (
	"strings"
	"testing"

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
