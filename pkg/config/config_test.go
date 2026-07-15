package config

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	configClient "github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
)

func TestClientIdleTimeoutRemainsIntAPI(t *testing.T) {
	cfg := configClient.Client{IdleTimeout: 30}
	if cfg.IdleTimeout != 30 {
		t.Fatalf("IdleTimeout = %d, want 30", cfg.IdleTimeout)
	}
}

func TestApply_ProgrammaticIdleTimeoutPreservesLegacySemantics(t *testing.T) {
	old := transport.GetIdleTimeout()
	t.Cleanup(func() {
		transport.EnableIPv6()
		transport.SetIdleTimeout(old)
	})

	cfg := newMixedConfig()
	cfg.Role = RoleClient
	cfg.LogMode = "skip"
	cfg.UUID = "programmatic-client"
	cfg.IPv6 = true
	cfg.IdleTimeout = 7
	if err := cfg.Apply(); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := transport.GetIdleTimeout(); got != 7*time.Second {
		t.Fatalf("GetIdleTimeout() = %v, want 7s", got)
	}
}

func TestNewFromStringInitializesEmbeddedConfigs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "client only",
			raw:  `{"role":"client","log":"skip","uuid":"client-user"}`,
		},
		{
			name: "server only",
			raw:  `{"role":"server","log":"skip","listen":"127.0.0.1:0","users":[{"uuid":"server-user"}]}`,
		},
	}

	oldIdleTimeout := transport.GetIdleTimeout()
	t.Cleanup(func() {
		transport.EnableIPv6()
		transport.SetIdleTimeout(oldIdleTimeout)
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := NewFromString(tt.raw)
			if err != nil {
				t.Fatalf("NewFromString() error = %v", err)
			}
			if cfg.Client == nil {
				t.Fatal("Client config is nil")
			}
			if cfg.Server == nil {
				t.Fatal("Server config is nil")
			}
			if err := cfg.Apply(); err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
		})
	}
}

func TestApply_IdleTimeoutOmittedKeepsDefault(t *testing.T) {
	const want = 30 * time.Minute
	transport.SetIdleTimeout(want)
	t.Cleanup(func() {
		transport.EnableIPv6()
		transport.SetIdleTimeout(want)
	})

	cfg, err := NewFromString(`{"role":"client","log":"skip","uuid":"00000000-0000-0000-0000-000000000001"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := transport.GetIdleTimeout(); got != want {
		t.Fatalf("GetIdleTimeout() = %v, want %v (omitted idle_timeout must not override default)", got, want)
	}
}

func TestApply_IdleTimeoutExplicitZero(t *testing.T) {
	const prior = 30 * time.Minute
	transport.SetIdleTimeout(prior)
	t.Cleanup(func() {
		transport.EnableIPv6()
		transport.SetIdleTimeout(prior)
	})

	cfg, err := NewFromString(`{"role":"client","log":"skip","uuid":"00000000-0000-0000-0000-000000000001","idle_timeout":0}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := transport.GetIdleTimeout(); got != 0 {
		t.Fatalf("GetIdleTimeout() = %v, want 0 (explicit idle_timeout:0 disables)", got)
	}
}

func TestApply_IdleTimeoutNullKeepsDefault(t *testing.T) {
	const want = 30 * time.Minute
	transport.SetIdleTimeout(want)
	t.Cleanup(func() {
		transport.EnableIPv6()
		transport.SetIdleTimeout(want)
	})

	cfg, err := NewFromString(`{"role":"client","log":"skip","uuid":"00000000-0000-0000-0000-000000000001","idle_timeout":null}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := transport.GetIdleTimeout(); got != want {
		t.Fatalf("GetIdleTimeout() = %v, want %v (null idle_timeout must not override default)", got, want)
	}
}

func TestApply_RejectsInvalidIdleTimeoutBeforeSideEffects(t *testing.T) {
	const prior = 17 * time.Minute
	transport.SetIdleTimeout(prior)
	t.Cleanup(func() {
		transport.EnableIPv6()
		transport.SetIdleTimeout(prior)
	})

	tests := []struct {
		name  string
		value string
	}{
		{name: "negative", value: "-1"},
	}
	if strconv.IntSize == 64 {
		tests = append(tests, struct {
			name  string
			value string
		}{name: "duration overflow", value: fmt.Sprint(maxIdleTimeoutSeconds + 1)})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"role":"client","log":"skip","uuid":"u","idle_timeout":%s}`, tt.value)
			cfg, err := NewFromString(raw)
			if err != nil {
				t.Fatalf("NewFromString() error = %v", err)
			}
			if err := cfg.Apply(); err == nil {
				t.Fatal("Apply() accepted invalid idle_timeout")
			}
			if got := transport.GetIdleTimeout(); got != prior {
				t.Fatalf("invalid config changed idle timeout to %v, want %v", got, prior)
			}
		})
	}
}

func TestApply_ExplicitRoutesFailClosedWithoutDefault(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)

	cfg, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001",
		"route":[{"src":["example.com"],"dst":"direct","type":"exact"}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if routesHasDefault(cfg.Routes) {
		t.Fatal("fixture routes unexpectedly contain a default")
	}
	if err := cfg.Apply(); err != nil {
		t.Fatal(err)
	}

	// Matched host uses the explicit exact rule (direct).
	tr, err := router.GetRoute("example.com")
	if err != nil {
		t.Fatalf("GetRoute(example.com) error = %v", err)
	}
	if tr.String() != "direct" {
		t.Fatalf("GetRoute(example.com) = %s, want direct", tr)
	}
	_ = tr.Close()

	// Unmatched host must fail closed — no auto-appended default.
	_, err = router.GetRoute("other.example")
	if err == nil {
		t.Fatal("expected route not found for unmatched host without default")
	}
	if err.Error() != "route not found: other.example -> nil" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApply_EmptyRoutesInstallsRoleDefault(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)

	cfg, err := NewFromString(`{"role":"server","log":"skip","listen":"127.0.0.1:0","users":[{"uuid":"u"}],"ipv6":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatal(err)
	}
	tr, err := router.GetRoute("anything.example")
	if err != nil {
		t.Fatalf("empty routes should install server default: %v", err)
	}
	if tr.String() != "direct" {
		t.Fatalf("server default egress = %s, want direct", tr)
	}
	_ = tr.Close()
}

func TestApply_IPv6ToggleReload(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)

	// First apply: IPv6 off (default).
	cfgOff, err := NewFromString(`{"role":"server","log":"skip","listen":"127.0.0.1:0","users":[{"uuid":"u"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgOff.Apply(); err != nil {
		t.Fatal(err)
	}
	if !transport.PreferIPv4() {
		t.Fatal("PreferIPv4() = false after ipv6 disabled apply")
	}
	if got := transport.DialNetwork("udp"); got != "udp4" {
		t.Fatalf("DialNetwork(udp) = %q, want udp4", got)
	}

	// Second apply: IPv6 on — must restore dual-stack.
	cfgOn, err := NewFromString(`{"role":"server","log":"skip","listen":"127.0.0.1:0","users":[{"uuid":"u"}],"ipv6":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgOn.Apply(); err != nil {
		t.Fatal(err)
	}
	if transport.PreferIPv4() {
		t.Fatal("PreferIPv4() still true after ipv6 enabled apply")
	}
	if got := transport.DialNetwork("udp"); got != "udp" {
		t.Fatalf("DialNetwork(udp) = %q, want udp", got)
	}
	if got := transport.GetNetwork(); got != "tcp" {
		t.Fatalf("GetNetwork() = %q, want tcp", got)
	}
}

func TestApply_InvalidRoutesPreserveIPv6ModeAndLiveRoutes(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)

	valid, err := NewFromString(`{"role":"server","log":"skip","listen":"127.0.0.1:0","users":[{"uuid":"u"}],"ipv6":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.Apply(); err != nil {
		t.Fatal(err)
	}

	invalid, err := NewFromString(`{
		"role":"server",
		"log":"skip",
		"listen":"127.0.0.1:0",
		"users":[{"uuid":"u"}],
		"route":[{"src":["["],"dst":"direct","type":"regex"}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := invalid.Apply(); err == nil {
		t.Fatal("Apply() accepted an invalid route generation")
	}
	if transport.PreferIPv4() {
		t.Fatal("failed reload changed IPv6 mode")
	}

	tr, err := router.GetRoute("still-live.example")
	if err != nil {
		t.Fatalf("failed reload replaced live routes: %v", err)
	}
	defer tr.Close()
	if tr.String() != "direct" {
		t.Fatalf("live route after failed reload = %s, want direct", tr)
	}
}

func TestRoutesHasDefault(t *testing.T) {
	if routesHasDefault(nil) {
		t.Fatal("nil routes should not report default")
	}
	if routesHasDefault(router.Routes{
		{MatchType: router.TypeExact, Sources: []string{"a.com"}},
	}) {
		t.Fatal("exact-only routes should not report default")
	}
	if !routesHasDefault(router.Routes{router.RouteClientDefault}) {
		t.Fatal("client default route should report default")
	}
}
