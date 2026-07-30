//go:build linux

package api

import (
	"strings"
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/pkg/config"
)

func TestLaunchPropagatesRedirectListenFailure(t *testing.T) {
	cfg, err := config.NewFromString(`{
		"role":"client",
		"log":"skip",
		"server_addr":"127.0.0.1:1",
		"uuid":"00000000-0000-0000-0000-000000000001",
		"listen_redirect":"not a valid listen address"
	}`)
	if err != nil {
		t.Fatal(err)
	}

	err = NewLauncher().Launch(cfg)
	if err == nil || !strings.Contains(err.Error(), "serve redirect failed") {
		t.Fatalf("Launch() error = %v, want redirect listen failure", err)
	}
}
