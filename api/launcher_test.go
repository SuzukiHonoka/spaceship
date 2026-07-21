package api

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

const testUserUUID = "6f1a6bb5-30f1-4a2e-9d0e-3a1c5f2b7e10"

func pickFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close probe: %v", err)
	}
	return addr
}

func waitDialable(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s", addr)
}

func startTestServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	addr = pickFreeAddr(t)
	l := NewLauncher()
	l.SkipInternalLogging()
	cfg := &config.MixedConfig{
		Role:   config.RoleServer,
		Client: &client.Client{},
		Server: &server.Server{
			Listen: addr,
			Users:  server.Users{{UUID: testUserUUID}},
		},
	}
	done := make(chan error, 1)
	go func() { done <- l.Launch(cfg) }()
	waitDialable(t, addr)
	stop = func() {
		l.Stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("test server did not stop")
		}
	}
	return addr, stop
}

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

	time.Sleep(50 * time.Millisecond)
	launcher.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Launch() error after Stop() = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Launcher.Stop() did not stop server mode")
	}
}

func TestLauncherStopStopsClientMode(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)

	serverAddr, stopServer := startTestServer(t)
	t.Cleanup(stopServer)

	launcher := NewLauncher()
	launcher.SkipInternalLogging()

	cfg := &config.MixedConfig{
		Role: config.RoleClient,
		Client: &client.Client{
			ServerAddr:  serverAddr,
			UUID:        testUserUUID,
			ListenSocks: "127.0.0.1:0",
			ListenHttp:  "127.0.0.1:0",
			Mux:         1,
			BasicAuth:   []string{"user:pass"},
		},
		Server: &server.Server{},
	}

	done := make(chan error, 1)
	go func() { done <- launcher.Launch(cfg) }()

	time.Sleep(100 * time.Millisecond)
	launcher.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Launch() client error after Stop() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Launcher.Stop() did not stop client mode")
	}
}

func TestLaunchRejectsInvalidUUID(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)

	launcher := NewLauncher()
	launcher.SkipInternalLogging()

	cfg := &config.MixedConfig{
		Role: config.RoleClient,
		Client: &client.Client{
			ServerAddr: "127.0.0.1:1",
			UUID:       "not-a-uuid",
			Mux:        1,
		},
		Server: &server.Server{},
	}

	if err := launcher.Launch(cfg); err == nil {
		t.Fatal("Launch() accepted invalid UUID")
	}
}

func TestLaunchRejectsBadBasicAuth(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)

	serverAddr, stopServer := startTestServer(t)
	t.Cleanup(stopServer)

	launcher := NewLauncher()
	launcher.SkipInternalLogging()
	cfg := &config.MixedConfig{
		Role: config.RoleClient,
		Client: &client.Client{
			ServerAddr:  serverAddr,
			UUID:        testUserUUID,
			ListenSocks: "127.0.0.1:0",
			Mux:         1,
			BasicAuth:   []string{"missing-colon"},
		},
		Server: &server.Server{},
	}
	err := launcher.Launch(cfg)
	if err == nil || !strings.Contains(err.Error(), "basic auth") {
		t.Fatalf("Launch() error = %v, want basic auth format error", err)
	}
}

func TestLaunchRejectsUnknownRole(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)

	launcher := NewLauncher()
	launcher.SkipInternalLogging()
	cfg := &config.MixedConfig{
		Role:   "neither",
		Client: &client.Client{},
		Server: &server.Server{},
	}
	if err := launcher.Launch(cfg); err == nil {
		t.Fatal("Launch() accepted unknown role")
	}
}

func TestLaunchFromFileAndString(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)

	lnAddr := pickFreeAddr(t)
	raw := `{
		"role":"server",
		"log":"skip",
		"listen":"` + lnAddr + `",
		"users":[{"uuid":"` + testUserUUID + `"}]
	}`

	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	l1 := NewLauncher()
	l1.SkipInternalLogging()
	done1 := make(chan error, 1)
	go func() { done1 <- l1.LaunchFromFile(path) }()
	waitDialable(t, lnAddr)
	l1.Stop()
	select {
	case err := <-done1:
		if err != nil {
			t.Fatalf("LaunchFromFile() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("LaunchFromFile did not stop")
	}

	lnAddr2 := pickFreeAddr(t)
	raw2 := strings.Replace(raw, lnAddr, lnAddr2, 1)
	l2 := NewLauncher()
	l2.SkipInternalLogging()
	done2 := make(chan error, 1)
	go func() { done2 <- l2.LaunchFromString(raw2) }()
	waitDialable(t, lnAddr2)
	l2.Stop()
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("LaunchFromString() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("LaunchFromString did not stop")
	}
}

func TestLaunchFromFileMissing(t *testing.T) {
	l := NewLauncher()
	l.SkipInternalLogging()
	err := l.LaunchFromFile(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("LaunchFromFile() accepted missing file")
	}
	if !strings.Contains(err.Error(), "load configuration") {
		t.Fatalf("error = %v, want load configuration wrapper", err)
	}
}

func TestLaunchFromStringInvalidJSON(t *testing.T) {
	l := NewLauncher()
	l.SkipInternalLogging()
	err := l.LaunchFromString(`{not json`)
	if err == nil {
		t.Fatal("LaunchFromString() accepted invalid JSON")
	}
	if !strings.Contains(err.Error(), "load configuration from string") {
		t.Fatalf("error = %v, want load configuration wrapper", err)
	}
}

func TestStopIdempotent(t *testing.T) {
	l := NewLauncher()
	l.Stop()
	l.Stop() // must not panic on double close of sigStop
}
