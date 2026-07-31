package server

import (
	"fmt"

	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

type serverOptions struct {
	dnsExchange   config.DNSExchange
	proxySessions config.ProxySessions
}

// Option customizes an RPC server while preserving source compatibility for
// existing embedders.
type Option func(*serverOptions) error

// WithDNSExchangeLimits configures admission control for the raw-wire DNS RPC.
func WithDNSExchangeLimits(raw *config.DNSExchange) Option {
	return func(options *serverOptions) error {
		normalized, err := config.NormalizeDNSExchange(raw)
		if err != nil {
			return err
		}
		options.dnsExchange = normalized
		return nil
	}
}

// WithProxySessionLimits configures admission control and first-message
// timeouts for streaming Proxy RPCs.
func WithProxySessionLimits(raw *config.ProxySessions) Option {
	return func(options *serverOptions) error {
		normalized, err := config.NormalizeProxySessions(raw)
		if err != nil {
			return err
		}
		options.proxySessions = normalized
		return nil
	}
}

func normalizeServerOptions(options []Option) (serverOptions, error) {
	dnsExchange, err := config.NormalizeDNSExchange(nil)
	if err != nil {
		return serverOptions{}, fmt.Errorf("default DNS exchange limits: %w", err)
	}
	proxySessions, err := config.NormalizeProxySessions(nil)
	if err != nil {
		return serverOptions{}, fmt.Errorf("default proxy session limits: %w", err)
	}
	result := serverOptions{
		dnsExchange:   dnsExchange,
		proxySessions: proxySessions,
	}
	for i, option := range options {
		if option == nil {
			return serverOptions{}, fmt.Errorf("server option %d is nil", i)
		}
		if err := option(&result); err != nil {
			return serverOptions{}, fmt.Errorf("server option %d: %w", i, err)
		}
	}
	return result, nil
}
