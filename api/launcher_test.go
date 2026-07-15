package api

import (
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

func TestLauncherStopStopsServerMode(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)

	launcher := NewLauncher()
	launcher.SkipInternalLogging()

	cfg := &config.MixedConfig{
		Role:   config.RoleServer,
		Client: &client.Client{},
		Server: &server.Server{
			Listen: "127.0.0.1:0",
			Users: server.Users{
				{UUID: "server-user"},
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- launcher.Launch(cfg)
	}()

	launcher.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Launch() error after Stop() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Launcher.Stop() did not stop server mode")
	}
}
