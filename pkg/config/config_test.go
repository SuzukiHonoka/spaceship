package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/redirect"
	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/socks"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/forward"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	"github.com/SuzukiHonoka/spaceship/v2/internal/tun"
	configClient "github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

type fixedErrorDialer struct {
	err error
}

func (d *fixedErrorDialer) Dial(_, _ string) (net.Conn, error) {
	return nil, d.err
}

func (d *fixedErrorDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, d.err
}

func TestClientIdleTimeoutRemainsIntAPI(t *testing.T) {
	cfg := configClient.Client{IdleTimeout: 30}
	if cfg.IdleTimeout != 30 {
		t.Fatalf("IdleTimeout = %d, want 30", cfg.IdleTimeout)
	}
}

func TestNewFromStringParsesRedirectListener(t *testing.T) {
	cfg, err := NewFromString(`{
		"role":"client",
			"log":"skip",
			"uuid":"00000000-0000-0000-0000-000000000001",
			"listen_redirect":"0.0.0.0:12345",
			"redirect":{"max_connections":2048}
		}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.ListenRedirect, "0.0.0.0:12345"; got != want {
		t.Fatalf("ListenRedirect = %q, want %q", got, want)
	}
	if cfg.Redirect == nil || cfg.Redirect.MaxConnections != 2048 {
		t.Fatalf("Redirect = %+v, want max_connections 2048", cfg.Redirect)
	}
}

func TestApplyRejectsOutOfRangeRedirectLimit(t *testing.T) {
	oldMark := transport.BypassMark()
	t.Cleanup(func() {
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
	})

	// Every admitted session owns a socket, goroutine, and egress stream, so
	// both ends of the range must be rejected rather than silently accepted.
	for _, tc := range []struct {
		name  string
		limit int
	}{
		{"negative", -1},
		{"aboveLimit", redirect.MaxConnectionsLimit + 1},
	} {
		name, limit := tc.name, tc.limit
		t.Run(name, func(t *testing.T) {
			cfg, err := NewFromString(fmt.Sprintf(`{
				"role":"client",
				"log":"skip",
				"uuid":"00000000-0000-0000-0000-000000000001",
				"ipv6":true,
				"listen_redirect":"0.0.0.0:12345",
				"redirect":{"max_connections":%d}
			}`, limit))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Apply(); !errors.Is(err, redirect.ErrInvalidMaxConnections) {
				t.Fatalf("Apply() with max_connections %d error = %v", limit, err)
			}
		})
	}

	// The boundary itself stays valid.
	cfg, err := NewFromString(fmt.Sprintf(`{
		"role":"client",
		"log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001",
		"ipv6":true,
		"listen_redirect":"0.0.0.0:12345",
		"redirect":{"max_connections":%d}
	}`, redirect.MaxConnectionsLimit))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatalf("Apply() rejected the documented maximum: %v", err)
	}
}

// SetDefault resolves a hostname resolver address over an already-marked
// socket, so a missing capability surfaces there as a lookup failure. Apply
// must attribute that to the mark instead of blaming the resolver.
func TestApply_AttributesResolverFailureToUnusableBypassMark(t *testing.T) {
	oldMark := transport.BypassMark()
	oldResolver := transport.OutboundResolver()
	t.Cleanup(func() {
		transport.SetOutboundResolver(oldResolver)
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
	})

	transport.SetBypassMark(transport.DefaultBypassMark)
	if transport.VerifyBypassMark() == nil {
		t.Skip("this process can set SO_MARK, so the attribution path is unreachable")
	}
	transport.SetBypassMark(0)

	cfg, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001",
		"ipv6":true,
		"listen_redirect":"0.0.0.0:12345",
		"redirect":{"bypass_mark":21328},
		"dns":{"type":"common","server":"resolver.invalid"}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	applyErr := cfg.Apply()
	if applyErr == nil {
		t.Fatal("Apply() accepted a config whose mark cannot be applied")
	}
	if !strings.Contains(applyErr.Error(), "outbound socket mark") {
		t.Fatalf("Apply() blamed the resolver instead of the mark: %v", applyErr)
	}
	if got := transport.BypassMark(); got != 0 {
		t.Fatalf("rejected Apply() left the mark installed: %#x", got)
	}
}

// A redirect section without a listener configures nothing. Accepting it would
// let bypass_mark and max_connections look applied while no listener exists.
func TestApplyRejectsRedirectSectionWithoutListener(t *testing.T) {
	oldMark := transport.BypassMark()
	t.Cleanup(func() {
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
	})
	transport.SetBypassMark(0)
	cfg, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001",
		"ipv6":true,
		"redirect":{"bypass_mark":21328}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err == nil ||
		!strings.Contains(err.Error(), "requires listen_redirect") {
		t.Fatalf("Apply() with an inert redirect section error = %v", err)
	}
	if got := transport.BypassMark(); got != 0 {
		t.Fatalf("inert redirect section still marked egress: BypassMark() = %#x", got)
	}
}

func TestApply_RedirectBypassMarkLifecycle(t *testing.T) {
	oldMark := transport.BypassMark()
	oldResolver := transport.OutboundResolver()
	t.Cleanup(func() {
		transport.SetOutboundResolver(oldResolver)
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
	})

	apply := func(t *testing.T, redirectSection string) *MixedConfig {
		t.Helper()
		transport.SetBypassMark(0)
		cfg, err := NewFromString(`{
			"role":"client",
			"log":"skip",
			"uuid":"00000000-0000-0000-0000-000000000001",
			"ipv6":true,
			"listen_redirect":"0.0.0.0:12345"` + redirectSection + `}`)
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.Apply(); err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
		return cfg
	}

	// Recursive self-capture is the most damaging misconfiguration of this
	// listener, so protection is on unless explicitly disabled. A defaulted mark
	// is never left installed when it cannot be used, whatever the reason:
	// resolver setup dials over a marked socket, so an unusable one would fail a
	// deployment that never asked for it.
	defaulted := apply(t, ``)
	if err := transport.VerifyBypassMark(); err != nil {
		t.Fatalf("Apply() left an unusable defaulted mark %#x installed: %v",
			transport.BypassMark(), err)
	}
	switch got := transport.BypassMark(); got {
	case transport.DefaultBypassMark:
		if !redirect.Supported() {
			t.Fatalf("marked egress for a listener this platform cannot run")
		}
	case 0:
		// Either the platform cannot run the listener, or it cannot set SO_MARK.
		if redirect.Supported() && transport.VerifyBypassMarkValue(transport.DefaultBypassMark) == nil {
			t.Fatal("dropped a usable default mark on a supported platform")
		}
	default:
		t.Fatalf("defaulted BypassMark() = %#x, want the default or none", got)
	}
	if defaulted.BypassMarkRequired() {
		t.Fatal("a defaulted mark must not be treated as a hard requirement")
	}

	explicit := apply(t, `,"redirect":{"max_connections":16,"bypass_mark":21328}`)
	if got := transport.BypassMark(); got != 21328 {
		t.Fatalf("explicit BypassMark() = %#x, want %#x", got, uint32(21328))
	}
	if !explicit.BypassMarkRequired() {
		t.Fatal("an explicitly configured mark must be treated as a requirement")
	}

	// Zero is the documented opt-out for deployments that cannot set SO_MARK.
	disabled := apply(t, `,"redirect":{"bypass_mark":0}`)
	if got := transport.BypassMark(); got != 0 {
		t.Fatalf("disabled BypassMark() = %#x, want 0", got)
	}
	if disabled.BypassMarkRequired() {
		t.Fatal("an explicitly disabled mark must not be treated as a requirement")
	}

	// A reload that drops the listener must drop the mark with it.
	transport.SetBypassMark(0)
	withoutRedirect, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001",
		"ipv6":true
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := withoutRedirect.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := transport.BypassMark(); got != 0 {
		t.Fatalf("BypassMark() without a redirect listener = %#x, want 0", got)
	}
}

// The bypass mark is process-global, so a server config carrying a redirect
// listener must not be able to mark server egress for a listener that never
// starts. launchServer ignores listen_redirect entirely, so reject it outright.
func TestApply_RejectsRedirectListenerOnServerRole(t *testing.T) {
	oldMark := transport.BypassMark()
	t.Cleanup(func() {
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
	})
	transport.SetBypassMark(0)

	cfg, err := NewFromString(`{
		"role":"server",
		"log":"skip",
		"listen":"0.0.0.0:443",
		"users":[{"uuid":"00000000-0000-0000-0000-000000000001"}],
		"listen_redirect":"0.0.0.0:12345",
		"redirect":{"bypass_mark":21328}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err == nil ||
		!strings.Contains(err.Error(), "client role") {
		t.Fatalf("Apply() with server-role listen_redirect error = %v", err)
	}
	if got := transport.BypassMark(); got != 0 {
		t.Fatalf("rejected server config still marked egress: BypassMark() = %#x", got)
	}
}

// A rejected reload must leave the live outbound mark untouched, exactly as it
// does for the TUN path.
func TestApply_RestoresRedirectBypassMarkAfterLaterFailure(t *testing.T) {
	oldMark := transport.BypassMark()
	oldResolver := transport.OutboundResolver()
	t.Cleanup(func() {
		transport.SetOutboundResolver(oldResolver)
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
	})
	transport.SetBypassMark(0)

	cfg, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001",
		"ipv6":true,
		"listen_redirect":"0.0.0.0:12345",
		"redirect":{"bypass_mark":21328},
		"route":[{"egress":"proxy","rules":["cidr:not-a-cidr"]}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err == nil {
		t.Fatal("Apply() accepted an invalid route")
	}
	if got := transport.BypassMark(); got != 0 {
		t.Fatalf("BypassMark() after rejected Apply = %#x, want 0", got)
	}
}

// The mark is process-wide, so a REDIRECT listener running alongside TUN may
// only inherit TUN's mark or restate it — never contradict it.
//
// Every case below gives TUN a mark deliberately different from
// transport.DefaultBypassMark. An earlier version of this test used the default
// value itself as TUN's "custom" mark, which made the inheritance cases pass
// whether or not inheritance worked: the mark a redirect listener defaults to
// happened to equal the one it was supposed to inherit.
func TestApply_RedirectBypassMarkAgreesWithTUN(t *testing.T) {
	if !tun.Supported() {
		t.Skipf("TUN is unsupported on this platform")
	}
	tunMark := transport.DefaultBypassMark ^ 0x1111
	if tunMark == transport.DefaultBypassMark || tunMark == 0 {
		t.Fatalf("tun mark %#x must be a non-zero non-default value", tunMark)
	}
	otherMark := transport.DefaultBypassMark ^ 0x2222

	configure := func(redirectSection string) string {
		return fmt.Sprintf(`{
			"role":"client",
			"log":"skip",
			"uuid":"00000000-0000-0000-0000-000000000001",
			"ipv6":true,
			"listen_redirect":"0.0.0.0:12345",
			%s"tun":{"name":"spaceship0","bypass_mark":%d,"dns_hijack":{"enabled":true}}
		}`, redirectSection, tunMark)
	}

	// An explicitly stated redirect mark that disagrees with TUN is a real
	// contradiction and must be reported, including when the operator states the
	// very value a listener would otherwise have defaulted to: explicit is
	// explicit, and silently overriding it would mark egress they did not ask for.
	for name, section := range map[string]string{
		"distinctValue": fmt.Sprintf(`"redirect":{"bypass_mark":%d},`, otherMark),
		"defaultValue":  fmt.Sprintf(`"redirect":{"bypass_mark":%d},`, transport.DefaultBypassMark),
	} {
		t.Run("conflicting/"+name, func(t *testing.T) {
			oldMark := transport.BypassMark()
			oldResolver := transport.OutboundResolver()
			t.Cleanup(func() {
				transport.SetOutboundResolver(oldResolver)
				transport.SetBypassMark(oldMark)
				transport.EnableIPv6()
			})

			cfg, err := NewFromString(configure(section))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Apply(); err == nil ||
				!strings.Contains(err.Error(), "conflicts with tun.bypass_mark") {
				t.Fatalf("Apply() with conflicting marks error = %v", err)
			}
		})
	}

	// A redirect mark that was only defaulted must yield to TUN's. Rejecting
	// these would fail a startup over a redirect.bypass_mark the operator never
	// wrote, naming a value that appears nowhere in their configuration.
	for name, section := range map[string]string{
		"noRedirectSection":     ``,
		"sectionWithoutMark":    `"redirect":{"max_connections":16},`,
		"markRestatingTUNValue": fmt.Sprintf(`"redirect":{"bypass_mark":%d},`, tunMark),
	} {
		t.Run("agreeing/"+name, func(t *testing.T) {
			oldMark := transport.BypassMark()
			oldResolver := transport.OutboundResolver()
			t.Cleanup(func() {
				transport.SetOutboundResolver(oldResolver)
				transport.SetBypassMark(oldMark)
				transport.EnableIPv6()
			})

			cfg, err := NewFromString(configure(section))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Apply(); err != nil {
				t.Fatalf("Apply() error = %v, want TUN's mark inherited", err)
			}
			if got := transport.BypassMark(); got != tunMark {
				t.Fatalf("BypassMark() = %#x, want TUN's %#x", got, tunMark)
			}
			// TUN cannot work without its mark, so it is a requirement however
			// the redirect listener arrived at the same value.
			if !cfg.BypassMarkRequired() {
				t.Fatal("a TUN mark must be treated as a hard requirement")
			}
		})
	}
}

// Disabling dns_hijack leaves the netstack with no UDP protocol at all, so DNS
// through the TUN silently stops working. The combination stays legal — an
// operator may exempt port 53 from the capture rule — but it must be loud.
// Disabling is a disagreement with TUN just as much as naming a different
// value: TUN cannot run unmarked, so an explicit 0 must be reported rather
// than silently overridden by the mark TUN needs.
func TestApply_RedirectBypassMarkZeroConflictsWithTUN(t *testing.T) {
	if !tun.Supported() {
		t.Skipf("TUN is unsupported on this platform")
	}
	oldMark := transport.BypassMark()
	oldResolver := transport.OutboundResolver()
	t.Cleanup(func() {
		transport.SetOutboundResolver(oldResolver)
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
	})
	transport.SetBypassMark(0)

	tunMark := transport.DefaultBypassMark ^ 0x3333
	cfg, err := NewFromString(fmt.Sprintf(`{
		"role":"client","log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001","ipv6":true,
		"listen_redirect":"0.0.0.0:12345",
		"redirect":{"bypass_mark":0},
		"tun":{"name":"spaceship0","bypass_mark":%d,"dns_hijack":{"enabled":true}}
	}`, tunMark))
	if err != nil {
		t.Fatal(err)
	}
	applyErr := cfg.Apply()
	if applyErr == nil {
		t.Fatalf("Apply() silently applied %#x despite redirect.bypass_mark 0", transport.BypassMark())
	}
	if !strings.Contains(applyErr.Error(), "cannot disable marking") {
		t.Fatalf("Apply() error = %v, want it to name the disabled redirect mark", applyErr)
	}
	if got := transport.BypassMark(); got != 0 {
		t.Fatalf("rejected Apply() left mark %#x installed", got)
	}
}

// Without TUN there is nothing to disagree with, so an explicit 0 simply
// disables marking rather than being rejected.
func TestApply_RedirectBypassMarkZeroWithoutTUNIsAccepted(t *testing.T) {
	oldMark := transport.BypassMark()
	t.Cleanup(func() {
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
	})
	transport.SetBypassMark(0)

	cfg, err := NewFromString(`{
		"role":"client","log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001","ipv6":true,
		"listen_redirect":"0.0.0.0:12345",
		"redirect":{"bypass_mark":0}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatalf("Apply() rejected the documented opt-out: %v", err)
	}
	if got := transport.BypassMark(); got != 0 {
		t.Fatalf("BypassMark() = %#x, want marking disabled", got)
	}
}

func TestApply_WarnsWhenTUNDisablesDNSHijack(t *testing.T) {
	if !tun.Supported() {
		t.Skipf("TUN is unsupported on this platform")
	}
	oldMark := transport.BypassMark()
	oldResolver := transport.OutboundResolver()
	t.Cleanup(func() {
		transport.SetOutboundResolver(oldResolver)
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
	})

	capture := func(t *testing.T, cfgJSON string) string {
		t.Helper()
		cfg, err := NewFromString(cfgJSON)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		oldWriter, oldFlags := log.Writer(), log.Flags()
		log.SetOutput(&buf)
		log.SetFlags(0)
		defer func() {
			log.SetOutput(oldWriter)
			log.SetFlags(oldFlags)
		}()
		if err := cfg.Apply(); err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
		return buf.String()
	}

	disabled := capture(t, `{
		"role":"client",
		"log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001",
		"ipv6":true,
		"tun":{"name":"spaceship0","max_connections":32}
	}`)
	if !strings.Contains(disabled, "dns_hijack is disabled") {
		t.Fatalf("Apply() without dns_hijack did not warn, log was:\n%s", disabled)
	}
	// The consequence is that port 53 stops being served. Non-DNS UDP is
	// refused either way, so the warning must not claim the flag disables UDP.
	if !strings.Contains(disabled, "port 53") {
		t.Fatalf("warning does not name the actual consequence, log was:\n%s", disabled)
	}
	for _, overclaim := range []string{"every UDP", "all UDP", "drops"} {
		if strings.Contains(disabled, overclaim) {
			t.Fatalf("warning overstates the effect with %q, log was:\n%s", overclaim, disabled)
		}
	}

	enabled := capture(t, `{
		"role":"client",
		"log":"skip",
		"uuid":"00000000-0000-0000-0000-000000000001",
		"ipv6":true,
		"tun":{"name":"spaceship0","max_connections":32,"dns_hijack":{"enabled":true}}
	}`)
	if strings.Contains(enabled, "dns_hijack is disabled") {
		t.Fatalf("Apply() with dns_hijack enabled warned anyway, log was:\n%s", enabled)
	}
}

func TestApplyAttachesAndResetsForwardProxy(t *testing.T) {
	t.Cleanup(func() {
		transport.EnableIPv6()
		if err := forward.Attach(nil); err != nil {
			t.Errorf("detach forward proxy: %v", err)
		}
	})

	withForward, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"forward-config-user",
		"ipv6":true,
		"forward":"socks5://127.0.0.1:1080"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := withForward.Apply(); err != nil {
		t.Fatalf("Apply() with forward proxy error = %v", err)
	}

	dialer := forward.New().(transport.ContextDialer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dialer.DialContext(ctx, "tcp", "example.com:443"); !errors.Is(err, context.Canceled) {
		t.Fatalf("configured forward DialContext() error = %v, want context.Canceled", err)
	}

	withoutForward, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"forward-config-user",
		"ipv6":true
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := withoutForward.Apply(); err != nil {
		t.Fatalf("Apply() without forward proxy error = %v", err)
	}
	if _, err := forward.New().Dial("tcp", "example.com:443"); err == nil ||
		err.Error() != "forward: dialer not attached" {
		t.Fatalf("Dial() after forward reset error = %v, want unattached dialer", err)
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

func TestApply_InvalidRoutesPreserveLiveRoutesForwardDialerAndIPv6Mode(t *testing.T) {
	t.Cleanup(func() {
		transport.EnableIPv6()
		if err := forward.Attach(nil); err != nil {
			t.Errorf("detach forward proxy: %v", err)
		}
	})

	valid, err := NewFromString(`{
		"role":"server",
		"log":"skip",
		"listen":"127.0.0.1:0",
		"users":[{"uuid":"u"}],
		"ipv6":true,
		"route":[{"dst":"forward","type":"default"}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.Apply(); err != nil {
		t.Fatal(err)
	}
	activeDialErr := errors.New("active forward dialer")
	if err := forward.Attach(&fixedErrorDialer{err: activeDialErr}); err != nil {
		t.Fatal(err)
	}

	invalid, err := NewFromString(`{
		"role":"server",
		"log":"skip",
		"listen":"127.0.0.1:0",
		"users":[{"uuid":"u"}],
		"forward":"socks5://127.0.0.1:1080",
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
	defer func() { _ = tr.Close() }()
	if tr.String() != "forward" {
		t.Fatalf("live route after failed reload = %s, want forward", tr)
	}
	if _, err := tr.Dial("tcp", "example.com:443"); !errors.Is(err, activeDialErr) {
		t.Fatalf("live forward dialer after failed reload error = %v, want %v", err, activeDialErr)
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

// TestApply_BufferSizeBounds verifies the transport buffer is validated against
// the gRPC message limit. A payload chunk is one full buffer wrapped in a
// protobuf envelope, so an oversized buffer would fail every send at runtime
// rather than at startup.
func TestApply_BufferSizeBounds(t *testing.T) {
	oldBuffer := transport.GetBufferSize()
	t.Cleanup(func() {
		transport.EnableIPv6()
		transport.SetBufferSize(uint16(oldBuffer / 1024))
	})

	maxKB := rpc.MaxTransportBufferSize / 1024
	tests := []struct {
		name    string
		buffer  uint16
		wantErr bool
	}{
		{"default omitted", 0, false},
		{"small", 32, false},
		{"at limit", uint16(maxKB), false},
		{"just over limit", uint16(maxKB) + 1, true},
		{"absurd", 65535, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := NewFromString(fmt.Sprintf(
				`{"role":"client","log":"skip","uuid":"u","buffer":%d}`, tt.buffer))
			if err != nil {
				t.Fatalf("NewFromString() error = %v", err)
			}
			err = cfg.Apply()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Apply() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.buffer > 0 && transport.GetBufferSize() != int(tt.buffer)*1024 {
				t.Errorf("buffer size = %d, want %d", transport.GetBufferSize(), int(tt.buffer)*1024)
			}
		})
	}
}

// TestApply_UDPSettings verifies the optional udp section reaches the SOCKS
// relay, including the kill switch, and that negative limits are rejected.
func TestApply_UDPSettings(t *testing.T) {
	t.Cleanup(func() {
		transport.EnableIPv6()
		socks.SetUDPSettings(socks.UDPSettings{})
	})

	// Omitted section leaves UDP enabled.
	cfg, err := NewFromString(`{"role":"client","log":"skip","uuid":"u"}`)
	if err != nil {
		t.Fatalf("NewFromString() error = %v", err)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if socks.UDPDisabled() {
		t.Error("UDP disabled with no udp section present")
	}

	// Explicit disable reaches the relay.
	cfg, err = NewFromString(`{"role":"client","log":"skip","uuid":"u","udp":{"disable":true}}`)
	if err != nil {
		t.Fatalf("NewFromString() error = %v", err)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !socks.UDPDisabled() {
		t.Error("udp.disable did not reach the relay")
	}

	// Negative limits are rejected rather than silently defaulted.
	cfg, err = NewFromString(`{"role":"client","log":"skip","uuid":"u","udp":{"max_associations":-1}}`)
	if err != nil {
		t.Fatalf("NewFromString() error = %v", err)
	}
	if err := cfg.Apply(); err == nil {
		t.Error("Apply() error = nil for negative udp.max_associations")
	}
}

func TestApply_TUNValidationAndBypassMarkLifecycle(t *testing.T) {
	oldMark := transport.BypassMark()
	oldResolver := transport.OutboundResolver()
	t.Cleanup(func() {
		transport.SetOutboundResolver(oldResolver)
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
	})

	serverConfig, err := NewFromString(`{
		"role":"server",
		"log":"skip",
		"tun":{"name":"spaceship0"}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := serverConfig.Apply(); err == nil ||
		!strings.Contains(err.Error(), "client role") {
		t.Fatalf("server Apply() with TUN error = %v", err)
	}

	cfg, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"tun-config-user",
		"ipv6":true,
		"tun":{
			"name":"spaceship0",
			"mtu":1400,
			"route_mode":"manual",
			"bypass_mark":4660,
			"max_connections":32,
			"max_pending_connections":8,
			"dns_hijack":{
				"enabled":true,
				"query_timeout_seconds":2,
				"tcp_idle_timeout_seconds":5,
				"max_in_flight":4
			}
		}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if !tun.Supported() {
		if err := cfg.Apply(); !errors.Is(err, tun.ErrUnsupported) {
			t.Fatalf("Apply() error = %v, want ErrUnsupported", err)
		}
		return
	}

	if err := cfg.Apply(); err != nil {
		t.Fatalf("Apply() with TUN error = %v", err)
	}
	if got := transport.BypassMark(); got != 4660 {
		t.Fatalf("BypassMark() = %#x, want %#x", got, uint32(4660))
	}

	withoutTUN, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"tun-config-user",
		"ipv6":true
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := withoutTUN.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := transport.BypassMark(); got != 0 {
		t.Fatalf("BypassMark() after TUN removal = %#x, want 0", got)
	}
}

func TestApply_TUNSelectsAndValidatesRPCPoolCapacity(t *testing.T) {
	if !tun.Supported() {
		t.Skip("TUN config is Linux-only")
	}
	oldMark := transport.BypassMark()
	oldResolver := transport.OutboundResolver()
	t.Cleanup(func() {
		transport.SetOutboundResolver(oldResolver)
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
	})

	automatic, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"tun-auto-mux-user",
		"ipv6":true,
		"tun":{"dns_hijack":{"enabled":true}}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if automatic.Mux != 0 {
		t.Fatalf("Mux before Apply = %d, want decoded zero", automatic.Mux)
	}
	if err := automatic.Apply(); err != nil {
		t.Fatal(err)
	}
	if automatic.Mux != 2 {
		t.Fatalf("Mux after Apply = %d, want capacity-aware default 2", automatic.Mux)
	}

	undersized, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"tun-small-mux-user",
		"ipv6":true,
		"mux":1,
		"tun":{"dns_hijack":{"enabled":true}}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := undersized.Apply(); err == nil ||
		!strings.Contains(err.Error(), "need at least 2") {
		t.Fatalf("Apply() with undersized mux error = %v", err)
	}
	if undersized.Mux != 1 {
		t.Fatalf("failed Apply mutated Mux to %d, want 1", undersized.Mux)
	}
}

func TestApply_NormalizesServerDNSExchangeLimits(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)
	cfg, err := NewFromString(`{
		"role":"server",
		"log":"skip",
		"ipv6":true,
		"users":[{"uuid":"dns-limit-user"}],
		"dns_exchange":{
			"max_concurrent":8,
			"max_concurrent_per_user":2,
			"queries_per_second":20,
			"queries_per_second_per_user":5,
			"burst":8,
			"burst_per_user":2
		}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatal(err)
	}
	if cfg.DNSExchange == nil ||
		cfg.DNSExchange.MaxConcurrent != 8 ||
		cfg.DNSExchange.MaxConcurrentPerUser != 2 ||
		cfg.DNSExchange.QueriesPerSecond != 20 ||
		cfg.DNSExchange.QueriesPerUser != 5 {
		t.Fatalf("normalized DNS exchange config = %+v", cfg.DNSExchange)
	}
}

func TestApply_RejectsInvalidOrMisplacedDNSExchangeLimits(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "client role",
			raw: `{
				"role":"client",
				"log":"skip",
				"uuid":"client",
				"dns_exchange":{"max_concurrent":1}
			}`,
			want: "only valid for the server role",
		},
		{
			name: "negative limit",
			raw: `{
				"role":"server",
				"log":"skip",
				"users":[{"uuid":"server"}],
				"dns_exchange":{"max_concurrent":-1}
			}`,
			want: "max_concurrent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := NewFromString(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Apply(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Apply() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestApply_NormalizesServerProxySessionLimits(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)
	cfg, err := NewFromString(`{
		"role":"server",
		"log":"skip",
		"ipv6":true,
		"users":[{"uuid":"proxy-limit-user"}],
		"proxy_sessions":{
			"max_concurrent":16,
			"max_concurrent_per_user":4,
			"new_sessions_per_second":40,
			"new_sessions_per_second_per_user":10,
			"burst":16,
			"burst_per_user":4,
			"handshake_timeout_seconds":3
		}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatal(err)
	}
	if cfg.ProxySessions == nil ||
		cfg.ProxySessions.MaxConcurrent != 16 ||
		cfg.ProxySessions.MaxConcurrentPerUser != 4 ||
		cfg.ProxySessions.SessionsPerSecond != 40 ||
		cfg.ProxySessions.SessionsPerUser != 10 ||
		cfg.ProxySessions.HandshakeTimeoutSeconds != 3 {
		t.Fatalf("normalized proxy session config = %+v", cfg.ProxySessions)
	}
}

func TestApply_RejectsInvalidOrMisplacedProxySessionLimits(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "client role",
			raw: `{
				"role":"client",
				"log":"skip",
				"uuid":"client",
				"proxy_sessions":{"max_concurrent":1}
			}`,
			want: "only valid for the server role",
		},
		{
			name: "negative limit",
			raw: `{
				"role":"server",
				"log":"skip",
				"users":[{"uuid":"server"}],
				"proxy_sessions":{"handshake_timeout_seconds":-1}
			}`,
			want: "handshake_timeout_seconds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := NewFromString(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Apply(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Apply() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestApply_RestoresBypassMarkAfterLaterValidationFailure(t *testing.T) {
	oldMark := transport.BypassMark()
	oldResolver := transport.OutboundResolver()
	sentinelResolver := &net.Resolver{PreferGo: true}
	transport.SetBypassMark(0x1234)
	transport.SetOutboundResolver(sentinelResolver)
	t.Cleanup(func() {
		transport.SetOutboundResolver(oldResolver)
		transport.SetBypassMark(oldMark)
	})

	cfg, err := NewFromString(`{
		"role":"client",
		"log":"skip",
		"uuid":"mark-rollback-user",
		"buffer":65535
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(); err == nil || !strings.Contains(err.Error(), "buffer too large") {
		t.Fatalf("Apply() error = %v, want buffer validation failure", err)
	}
	if got := transport.BypassMark(); got != 0x1234 {
		t.Fatalf("BypassMark() after failed Apply = %#x, want %#x", got, uint32(0x1234))
	}
	if got := transport.OutboundResolver(); got != sentinelResolver {
		t.Fatalf("OutboundResolver() after failed Apply = %p, want %p", got, sentinelResolver)
	}
}

// TestApply_UDPSettingsResetOnReload verifies a reload that drops the udp
// section restores defaults instead of leaving a previously configured
// "disable" in effect, matching how ipv6 is re-applied both ways.
func TestApply_UDPSettingsResetOnReload(t *testing.T) {
	t.Cleanup(func() {
		transport.EnableIPv6()
		socks.SetUDPSettings(socks.UDPSettings{})
	})

	disabled, err := NewFromString(`{"role":"client","log":"skip","uuid":"u","udp":{"disable":true}}`)
	if err != nil {
		t.Fatalf("NewFromString() error = %v", err)
	}
	if err := disabled.Apply(); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !socks.UDPDisabled() {
		t.Fatal("udp.disable did not take effect")
	}

	// Reload without the section: UDP must come back on.
	reloaded, err := NewFromString(`{"role":"client","log":"skip","uuid":"u"}`)
	if err != nil {
		t.Fatalf("NewFromString() error = %v", err)
	}
	if err := reloaded.Apply(); err != nil {
		t.Fatalf("Apply() on reload error = %v", err)
	}
	if socks.UDPDisabled() {
		t.Error("udp stayed disabled after a reload that dropped the udp section")
	}
}

func TestNewFromConfigFile(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)

	dir := t.TempDir()
	path := dir + "/cfg.json"
	raw := `{"role":"client","log":"skip","uuid":"6f1a6bb5-30f1-4a2e-9d0e-3a1c5f2b7e10","server_addr":"127.0.0.1:1"}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := NewFromConfigFile(path)
	if err != nil {
		t.Fatalf("NewFromConfigFile() error = %v", err)
	}
	if cfg.Role != RoleClient {
		t.Fatalf("Role = %s, want client", cfg.Role)
	}
	if cfg.UUID != "6f1a6bb5-30f1-4a2e-9d0e-3a1c5f2b7e10" {
		t.Fatalf("UUID = %s", cfg.UUID)
	}
	if err := cfg.Apply(); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
}

func TestNewFromConfigFileMissing(t *testing.T) {
	_, err := NewFromConfigFile(t.TempDir() + "/nope.json")
	if err == nil {
		t.Fatal("NewFromConfigFile() accepted missing file")
	}
}

func TestNewFromConfigFileInvalidJSON(t *testing.T) {
	path := t.TempDir() + "/bad.json"
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewFromConfigFile(path)
	if err == nil {
		t.Fatal("NewFromConfigFile() accepted invalid JSON")
	}
}

func TestNewFromStringInvalidJSON(t *testing.T) {
	_, err := NewFromString(`{`)
	if err == nil {
		t.Fatal("NewFromString() accepted invalid JSON")
	}
}

func TestApply_NilEmbeddedConfigs(t *testing.T) {
	t.Cleanup(transport.EnableIPv6)
	cfg := &MixedConfig{
		Role:    RoleServer,
		LogMode: "skip",
		Server: &server.Server{
			Listen: "127.0.0.1:0",
			Users:  server.Users{{UUID: "u"}},
		},
	}
	// Client nil — ensureEmbeddedConfigs must fill it; Server already set.
	cfg.Client = nil
	if err := cfg.Apply(); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if cfg.Client == nil {
		t.Fatal("ensureEmbeddedConfigs did not populate Client")
	}
}

// A configuration written for v2.1.7 must still apply unchanged. Everything
// added since is additive, so this pins that promise against future validation
// being tightened in a way that rejects a field combination operators already
// have deployed. Every key here existed at that tag.
func TestApply_V217ConfigsRemainValid(t *testing.T) {
	oldMark := transport.BypassMark()
	oldResolver := transport.OutboundResolver()
	t.Cleanup(func() {
		transport.SetOutboundResolver(oldResolver)
		transport.SetBypassMark(oldMark)
		transport.EnableIPv6()
		_ = forward.Attach(nil)
		_ = router.SetRoutes(nil)
	})

	const v217Client = `{
		"role":"client",
		"log":"skip",
		"dns":{"type":"common","server":"1.1.1.1"},
		"cas":[],
		"server_addr":"tunnel.example.com:443",
		"host":"tunnel.example.com",
		"uuid":"00000000-0000-0000-0000-000000000001",
		"listen_socks":"127.0.0.1:1080",
		"listen_socks_unix":"/tmp/spaceship.sock",
		"listen_http":"127.0.0.1:8080",
		"listen_dns":"127.0.0.1:5353",
		"basic_auth":["user:pass"],
		"mux":4,
		"tls":true,
		"idle_timeout":300,
		"block_ipv6_dns":true,
		"udp":{
			"disable":false,
			"max_associations":64,
			"max_associations_per_client":8,
			"max_nat_entries":32,
			"max_nat_entries_total":256,
			"max_nat_entries_per_client":64
		},
		"route":[
			{"src":["10.0.0.0/8"],"dst":"direct","type":"cidr"},
			{"src":["example.com"],"dst":"proxy","type":"domain"},
			{"dst":"proxy","type":"default"}
		]
	}`

	const v217Server = `{
		"role":"server",
		"log":"skip",
		"listen":"0.0.0.0:443",
		"path":"custom.Service",
		"buffer":32,
		"ipv6":true,
		"ssl":{"cert":"/etc/spaceship/fullchain.pem","key":"/etc/spaceship/privkey.pem"},
		"users":[
			{"uuid":"00000000-0000-0000-0000-000000000001","remark":"alice"},
			{"uuid":"00000000-0000-0000-0000-000000000002","remark":"bob"}
		]
	}`

	for name, raw := range map[string]string{"client": v217Client, "server": v217Server} {
		t.Run(name, func(t *testing.T) {
			cfg, err := NewFromString(raw)
			if err != nil {
				t.Fatalf("NewFromString() rejected a v2.1.7 %s config: %v", name, err)
			}
			if err := cfg.Apply(); err != nil {
				t.Fatalf("Apply() rejected a v2.1.7 %s config: %v", name, err)
			}
		})
	}

	// Nothing since v2.1.7 marks egress unless a new frontend asks for it.
	if got := transport.BypassMark(); got != 0 {
		t.Fatalf("a v2.1.7 config installed outbound mark %#x", got)
	}
}
