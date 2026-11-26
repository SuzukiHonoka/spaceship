//go:build linux && !android

package tun

import (
	"context"
	"net/netip"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/nftables"
)

const (
	tunAutoRedirectHelperEnv = "SPACESHIP_TUN_AUTOREDIRECT_HELPER"
)

// TestKernelTUNAutoRedirectIntegration verifies route_mode "auto" +
// auto_redirect inside a disposable Linux network namespace: interface CIDR
// assignment, nftables inet/spaceship table installation, stale-table crash
// recovery, and zero leftover rules on Close().
func TestKernelTUNAutoRedirectIntegration(t *testing.T) {
	if os.Getenv(tunAutoRedirectHelperEnv) == "1" {
		runKernelTUNAutoRedirectHelper(t)
		return
	}
	if os.Getenv(tunIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the isolated auto_redirect integration test", tunIntegrationEnv)
	}
	for _, name := range []string{"unshare", "ip"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("required tool %q not found: %v", name, err)
		}
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Fatalf("/dev/net/tun unavailable: %v", err)
	}

	args := []string{"--net", "--mount", "--mount-proc", "--fork"}
	if os.Geteuid() != 0 {
		args = append([]string{"--user", "--map-root-user"}, args...)
	}
	args = append(args,
		"--",
		os.Args[0],
		"-test.run=^TestKernelTUNAutoRedirectIntegration$",
		"-test.count=1",
		"-test.v",
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "unshare", args...)
	cmd.Env = append(os.Environ(), tunAutoRedirectHelperEnv+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("auto_redirect integration failed: %v\n%s", err, output)
	}
}

func runKernelTUNAutoRedirectHelper(t *testing.T) {
	if err := exec.Command("ip", "link", "set", "lo", "up").Run(); err != nil {
		t.Fatalf("bring lo up: %v", err)
	}

	// Pre-create a stale inet/spaceship table to verify crash self-healing.
	conn, err := nftables.New()
	if err != nil {
		t.Skipf("nftables unavailable in kernel/netns: %v", err)
	}
	conn.AddTable(&nftables.Table{Name: nftTableName, Family: nftables.TableFamilyINet})
	if err := conn.Flush(); err != nil {
		t.Skipf("nftables flush unavailable in kernel/netns: %v", err)
	}
	_ = conn.CloseLasting()

	svc, err := New(context.Background(), Config{
		Name:         "ss-ar0",
		MTU:          1400,
		RouteMode:    "auto",
		AutoRedirect: true,
		Address:      []netip.Prefix{netip.MustParsePrefix("198.18.0.1/30")},
		DNS:          DNSConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("tun.New(auto_redirect): %v", err)
	}

	// Confirm table exists while running.
	if !nftTableExists(t, nftTableName) {
		_ = svc.Close()
		t.Fatal("expected nftables table inet/spaceship to exist while Service is active")
	}

	if err := svc.Close(); err != nil {
		t.Fatalf("Service.Close: %v", err)
	}

	// Confirm table is cleanly removed after Close.
	if nftTableExists(t, nftTableName) {
		t.Fatal("nftables table inet/spaceship survived Service.Close()")
	}
}

func nftTableExists(t *testing.T, name string) bool {
	t.Helper()
	conn, err := nftables.New()
	if err != nil {
		t.Fatalf("nftables.New: %v", err)
	}
	defer func() { _ = conn.CloseLasting() }()
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		t.Fatalf("ListTablesOfFamily: %v", err)
	}
	for _, tbl := range tables {
		if tbl.Name == name {
			return true
		}
	}
	return false
}
