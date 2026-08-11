package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/redirect"
	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/socks"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/forward"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	rpcClient "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/v2/internal/tun"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/client"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/dns"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/logger"
	"golang.org/x/net/proxy"
)

// MixedConfig is a server/client mixed config, along with general config.
type MixedConfig struct {
	// Role is an identifier for distinguish the role in spaceship since the server/client are not seperated.
	// supported roles: "server", "client"
	Role `json:"role"`
	// DNS is used for set up the custom dns as an upstream of global resolver.
	DNS *dns.DNS `json:"dns,omitempty"`
	// CAs is used for append the custom CA to the system cert pool.
	CAs []string `json:"cas,omitempty"`
	// LogMode is used for set up specific log mod, defaults to stdout.
	LogMode logger.Mode `json:"log,omitempty"`

	*client.Client
	*server.Server

	decodedFromJSON bool
	idleTimeoutSet  bool
	// redirectMarkRequired records that the outbound mark was configured
	// explicitly rather than defaulted, so startup treats an unusable mark as a
	// failure instead of degrading to unmarked egress.
	redirectMarkRequired bool
}

// BypassMarkRequired reports whether the applied configuration treats the
// outbound socket mark as a hard requirement. TUN cannot work without it, and
// an explicitly configured redirect mark is a stated requirement; a mark that
// was merely defaulted may be dropped with a warning instead.
func (c *MixedConfig) BypassMarkRequired() bool {
	if c == nil {
		return false
	}
	return c.redirectMarkRequired || (c.Client != nil && c.TUN != nil)
}

const maxIdleTimeoutSeconds = int64(1<<63-1) / int64(time.Second)

func newMixedConfig() *MixedConfig {
	return &MixedConfig{
		Client: &client.Client{},
		Server: &server.Server{},
	}
}

func (c *MixedConfig) ensureEmbeddedConfigs() {
	if c.Client == nil {
		c.Client = &client.Client{}
	}
	if c.Server == nil {
		c.Server = &server.Server{}
	}
}

// UnmarshalJSON records whether idle_timeout was explicitly provided while
// preserving Client.IdleTimeout as an int for Go API compatibility.
func (c *MixedConfig) UnmarshalJSON(data []byte) error {
	type mixedConfigJSON MixedConfig
	defaults := newMixedConfig()
	*c = *defaults
	if err := json.Unmarshal(data, (*mixedConfigJSON)(c)); err != nil {
		return err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	raw, present := fields["idle_timeout"]
	c.decodedFromJSON = true
	c.idleTimeoutSet = present && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	return nil
}

// NewFromConfigFile loads the config from the file in the specific path.
func NewFromConfigFile(path string) (*MixedConfig, error) {
	// Clean the path to remove any directory traversal attempts
	cleanPath := filepath.Clean(path)

	// Read the config file
	f, err := os.Open(cleanPath)
	if err != nil {
		return nil, err
	}
	defer utils.Close(f)

	config := newMixedConfig()
	if err = json.NewDecoder(f).Decode(config); err != nil {
		return nil, err
	}
	return config, nil
}

// NewFromString loads the config from raw config string in JSON format (stick to the config structure).
func NewFromString(c string) (*MixedConfig, error) {
	config := newMixedConfig()
	if err := json.Unmarshal([]byte(c), config); err != nil {
		return nil, err
	}
	return config, nil
}

// Apply applies the MixedConfig
func (c *MixedConfig) Apply() error {
	c.ensureEmbeddedConfigs()

	// role check
	if c.Role != RoleClient && c.Role != RoleServer {
		return fmt.Errorf("invalid role: %s", c.Role)
	}

	var normalizedDNSExchange *server.DNSExchange
	if c.DNSExchange != nil {
		if c.Role != RoleServer {
			return errors.New("dns_exchange is only valid for the server role")
		}
		normalized, err := server.NormalizeDNSExchange(c.DNSExchange)
		if err != nil {
			return err
		}
		normalizedDNSExchange = &normalized
	}

	var normalizedProxySessions *server.ProxySessions
	if c.ProxySessions != nil {
		if c.Role != RoleServer {
			return errors.New("proxy_sessions is only valid for the server role")
		}
		normalized, err := server.NormalizeProxySessions(c.ProxySessions)
		if err != nil {
			return err
		}
		normalizedProxySessions = &normalized
	}

	var tunConfig *tun.Config
	effectiveMux := c.Mux
	tunDNSHijackDisabled := false
	if c.TUN != nil {
		if c.Role != RoleClient {
			return errors.New("tun is only valid for the client role")
		}
		if !tun.Supported() {
			return tun.ErrUnsupported
		}
		normalized, err := tun.FromClientConfig(c.TUN, c.BlockIPv6DNS)
		if err != nil {
			return err
		}
		tunConfig = &normalized
		tunDNSHijackDisabled = !normalized.DNS.Enabled
		requiredMux, err := tun.RequiredRPCPoolSize(normalized, rpc.MaxConcurrentStreams)
		if err != nil {
			return err
		}
		if effectiveMux == 0 {
			effectiveMux = requiredMux
		} else if effectiveMux < requiredMux {
			return fmt.Errorf(
				"mux %d is too small for tun capacity; need at least %d",
				effectiveMux,
				requiredMux,
			)
		}
	}

	applyIdleTimeout := !c.decodedFromJSON || c.idleTimeoutSet
	idleTimeoutSeconds := int64(c.IdleTimeout)
	if applyIdleTimeout {
		if idleTimeoutSeconds < 0 {
			return fmt.Errorf("idle_timeout must be non-negative: %d", c.IdleTimeout)
		}
		if idleTimeoutSeconds > maxIdleTimeoutSeconds {
			return fmt.Errorf("idle_timeout exceeds maximum duration: %d", c.IdleTimeout)
		}
	}
	// Role-check the redirect listener for the same reason as tun above: its
	// bypass mark is process-global, so a server config that merely carries the
	// setting would otherwise mark all server egress for a listener that
	// launchServer never starts.
	if c.ListenRedirect != "" && c.Role != RoleClient {
		return errors.New("listen_redirect is only valid for the client role")
	}
	// A REDIRECT listener defaults to marking Spaceship's own egress: recursive
	// self-capture is the most damaging way to misconfigure it, so protection is
	// on unless the operator disables it explicitly with bypass_mark 0.
	var redirectBypassMark uint32
	redirectMarkRequired := false
	if c.ListenRedirect != "" {
		redirectBypassMark = transport.DefaultBypassMark
	}
	if c.Redirect != nil {
		// Settings that silently do nothing are a configuration trap: reject the
		// section outright rather than let bypass_mark or max_connections look
		// applied while no listener exists to use them.
		if c.ListenRedirect == "" {
			return errors.New("redirect requires listen_redirect")
		}
		if err := redirect.ValidateMaxConnections(c.Redirect.MaxConnections); err != nil {
			return err
		}
		if c.Redirect.BypassMark != nil {
			redirectBypassMark = *c.Redirect.BypassMark
			redirectMarkRequired = redirectBypassMark != 0
		}
	}
	c.redirectMarkRequired = redirectMarkRequired

	// log mode
	c.LogMode.Set()

	// Warn about the two capture-rule configurations whose failure mode is a
	// silent black hole rather than a startup error. Both are legitimate when
	// the operator has arranged the matching exclusion, so neither is rejected.
	if tunDNSHijackDisabled {
		// Scope this to DNS. Non-DNS UDP is refused either way because the TUN
		// frontend is TCP-only, so claiming the flag disables UDP would imply
		// enabling it delivers a general UDP tunnel.
		log.Println(
			"tun: WARNING dns_hijack is disabled, so port 53 is not served and DNS sent " +
				"through the TUN fails immediately. Enable tun.dns_hijack, or keep port 53 " +
				"out of the capture rule so queries never enter the TUN",
		)
	}
	if c.ListenRedirect != "" && tunConfig == nil && redirectBypassMark == 0 {
		log.Println(
			"redirect: WARNING redirect.bypass_mark is disabled; an OUTPUT-chain REDIRECT " +
				"rule must exempt Spaceship's own egress by another means, such as " +
				"-m owner --uid-owner, or the listener will recursively capture it and " +
				"exhaust redirect.max_connections",
		)
	}

	// Install the bypass mark before resolver setup. A configured DNS server may
	// itself be a hostname, and SetDefault resolves it immediately; doing that
	// with an unmarked socket can recurse into an already-installed catch-all
	// capture rule before the frontend has started. Roll back this early global
	// update if any later configuration step fails.
	//
	// TUN and REDIRECT share one process-wide mark. TUN's is authoritative
	// because TUN cannot work without it; a REDIRECT listener alongside TUN
	// inherits it and only has to agree.
	bypassMark := uint32(0)
	if tunConfig != nil {
		bypassMark = tunConfig.BypassMark
	}
	if redirectBypassMark != 0 {
		if bypassMark != 0 && bypassMark != redirectBypassMark {
			return fmt.Errorf(
				"redirect.bypass_mark %#x conflicts with tun.bypass_mark %#x: "+
					"a process has exactly one outbound socket mark",
				redirectBypassMark,
				bypassMark,
			)
		}
		bypassMark = redirectBypassMark
	}
	previousBypassMark := transport.BypassMark()
	previousOutboundResolver := transport.OutboundResolver()
	transport.SetBypassMark(bypassMark)
	networkPolicyCommitted := false
	defer func() {
		if !networkPolicyCommitted {
			transport.SetOutboundResolver(previousOutboundResolver)
			transport.SetBypassMark(previousBypassMark)
		}
	}()

	// dns
	if c.DNS != nil {
		if err := c.DNS.SetDefault(); err != nil {
			// SetDefault resolves a hostname resolver address using an already
			// marked socket. A missing network-administration capability shows
			// up here as an opaque lookup failure, so attribute it precisely
			// instead of blaming the resolver.
			if markErr := transport.VerifyBypassMark(); markErr != nil {
				return fmt.Errorf("apply outbound socket mark %#x: %w", bypassMark, markErr)
			}
			return err
		}
	} else {
		dns.SetSystemDefault(tunConfig != nil)
	}

	// custom buffer size
	if c.Buffer > 0 {
		// A payload chunk is one full buffer wrapped in a protobuf envelope, so an
		// oversized buffer exceeds the gRPC message limit and fails every send at
		// runtime. Reject it at startup instead.
		if bufferBytes := int(c.Buffer) * 1024; bufferBytes > rpc.MaxTransportBufferSize {
			return fmt.Errorf("buffer too large: %dK exceeds maximum %dK",
				c.Buffer, rpc.MaxTransportBufferSize/1024)
		}
		log.Printf("custom buffer size: %dK", c.Buffer)
		transport.SetBufferSize(c.Buffer)
	}

	// socks5 udp associate relay. Applied unconditionally so a reload that drops
	// the section restores defaults, the same way ipv6 is re-enabled below —
	// otherwise a previously configured "disable" would silently persist.
	var udpSettings socks.UDPSettings
	if c.UDP != nil {
		for _, limit := range []struct {
			name  string
			value int
		}{
			{"max_associations", c.UDP.MaxAssociations},
			{"max_associations_per_client", c.UDP.MaxAssociationsPerClient},
			{"max_nat_entries", c.UDP.MaxNATEntries},
			{"max_nat_entries_total", c.UDP.MaxNATEntriesTotal},
			{"max_nat_entries_per_client", c.UDP.MaxNATEntriesPerClient},
		} {
			if limit.value < 0 {
				return fmt.Errorf("udp.%s must be non-negative: %d", limit.name, limit.value)
			}
		}
		udpSettings = socks.UDPSettings{
			Disable:                  c.UDP.Disable,
			MaxAssociations:          c.UDP.MaxAssociations,
			MaxAssociationsPerClient: c.UDP.MaxAssociationsPerClient,
			MaxNATEntries:            c.UDP.MaxNATEntries,
			MaxNATEntriesGlobal:      c.UDP.MaxNATEntriesTotal,
			MaxNATEntriesPerClient:   c.UDP.MaxNATEntriesPerClient,
		}
	}
	socks.SetUDPSettings(udpSettings)
	if udpSettings.Disable {
		log.Println("socks5 udp associate disabled")
	}

	// custom grpc service name
	if c.Path != "" {
		log.Printf("custom service name: %s", c.Path)
		// Use the new RPC configuration system
		rpc.SetServiceName(c.Path)
	}

	// client uuid
	if c.Role == RoleClient {
		if c.UUID == "" {
			return errors.New("client uuid empty")
		}
		rpcClient.SetUUID(c.UUID)
	}

	// Forward proxy. Apply this unconditionally so a later Apply that drops the
	// setting does not retain a stale process-global dialer from the old config.
	var forwardDialer proxy.Dialer
	if c.Forward != "" {
		d, err := utils.LoadProxyWithDialer(
			c.Forward,
			transport.NewOutboundDialer(transport.GetDialTimeout()),
		)
		if err != nil {
			return err
		}
		forwardDialer = d
	}
	preparedForwardDialer, err := forward.PrepareDialer(forwardDialer)
	if err != nil {
		return fmt.Errorf("forward proxy: %w", err)
	}

	// Routes: empty list installs the role default. An explicit list is used as-is
	// (fail-closed): if it has no "default" rule, unmatched hosts are rejected.
	// Operators who want catch-all behavior must add an explicit default route.
	routes := c.Routes
	if len(routes) == 0 {
		if c.Role == RoleClient {
			routes = router.Routes{router.CloneRoute(router.RouteClientDefault)}
		} else {
			routes = router.Routes{router.CloneRoute(router.RouteServerDefault)}
		}
	} else if !routesHasDefault(routes) {
		log.Println("warning: no default route configured; unmatched destinations will be rejected")
	}
	if !c.IPv6 {
		routes = append(router.Routes{router.RouteBlockIPv6}, routes...)
	}
	if err := router.SetRoutes(routes); err != nil {
		return err
	}

	// Activate the prepared dialer only after route installation succeeds. A
	// rejected reload must leave both the live route table and its forward egress
	// unchanged.
	preparedForwardDialer.Activate()
	if forwardDialer != nil {
		log.Println("forward-proxy attached")
	}

	// IPv6 dial preference must be set both ways so a later Apply/reload can
	// re-enable dual-stack after a previous DisableIPv6. Route validation and
	// installation happen first, so a rejected reload leaves this mode intact.
	if !c.IPv6 {
		transport.DisableIPv6()
		log.Println("ipv6 disabled")
	} else {
		transport.EnableIPv6()
		log.Println("ipv6 enabled")
	}

	// idle timeout: only apply when the field is present in config.
	// omitted -> keep transport default (30m); 0 -> disable; n > 0 -> n seconds.
	if applyIdleTimeout {
		log.Printf("custom idle timeout: %ds", c.IdleTimeout)
		transport.SetIdleTimeout(time.Duration(idleTimeoutSeconds) * time.Second)
	}

	c.Mux = effectiveMux
	if normalizedDNSExchange != nil {
		c.DNSExchange = normalizedDNSExchange
	}
	if normalizedProxySessions != nil {
		c.ProxySessions = normalizedProxySessions
	}
	networkPolicyCommitted = true
	return nil
}

// routesHasDefault reports whether any route is a catch-all default rule.
func routesHasDefault(routes router.Routes) bool {
	for _, r := range routes {
		if r != nil && r.MatchType == router.TypeDefault {
			return true
		}
	}
	return false
}
