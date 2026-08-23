package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/dns"
	"github.com/SuzukiHonoka/spaceship/v2/internal/http"
	"github.com/SuzukiHonoka/spaceship/v2/internal/redirect"
	"github.com/SuzukiHonoka/spaceship/v2/internal/socks"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/server"
	"github.com/SuzukiHonoka/spaceship/v2/internal/tun"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/logger"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

type Launcher struct {
	sigStop             chan struct{}
	skipInternalLogging bool
	stopOnce            sync.Once

	forceExitTimeout time.Duration
	forceExitMu      sync.Mutex
	forceExitTimer   *time.Timer
}

func NewLauncher() *Launcher {
	return &Launcher{
		sigStop: make(chan struct{}),
	}
}

func (l *Launcher) SkipInternalLogging() {
	l.skipInternalLogging = true
}

func (l *Launcher) launchServer(ctx context.Context, cfg *config.MixedConfig) error {
	log.Println("server starting")

	errGroup, ctx := errgroup.WithContext(ctx)

	// create server
	s, err := server.NewServer(
		ctx,
		cfg.Users,
		cfg.SSL,
		cfg.DNS,
		server.WithDNSExchangeLimits(cfg.DNSExchange),
		server.WithProxySessionLimits(cfg.ProxySessions),
	)
	if err != nil {
		return fmt.Errorf("create server failed: %w", err)
	}

	errGroup.Go(func() error {
		if err := s.ListenAndServe(cfg.Listen); err != nil {
			return fmt.Errorf("serve rpc failed: %w", err)
		}
		return nil
	})
	errGroup.Go(func() error {
		return l.listenSignal(ctx)
	})

	err = errGroup.Wait()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrSignalArrived) {
		return fmt.Errorf("server process error: %w", err)
	}
	return nil
}

func (l *Launcher) launchClient(ctx context.Context, cfg *config.MixedConfig) error {
	log.Println("client starting")

	// check uuid format
	if _, err := uuid.Parse(cfg.UUID); err != nil {
		return err
	}
	if cfg.ListenRedirect != "" && !redirect.Supported() {
		return redirect.ErrUnsupported
	}
	if cfg.TUN != nil && !tun.Supported() {
		return tun.ErrUnsupported
	}
	// Apply installed the bypass mark; confirm this process can actually set it
	// before any frontend starts. Checking here rather than in Apply keeps
	// config validation privilege-free while still turning a missing
	// CAP_NET_ADMIN into one startup error instead of an EPERM on every dial.
	// Apply already dropped a defaulted mark this process cannot set, so anything
	// still installed is a stated requirement: TUN's, or an explicit
	// redirect.bypass_mark. Fail with one clear error rather than letting it
	// surface as an EPERM on every outbound dial.
	if err := transport.VerifyBypassMark(); err != nil {
		return fmt.Errorf("apply outbound socket mark %#x: %w", transport.BypassMark(), err)
	}

	// destroy any left connections
	defer client.Destroy()

	// initialize pool
	if err := client.Init(cfg.ServerAddr, cfg.Host, cfg.EnableTLS, cfg.Mux, cfg.CAs); err != nil {
		return fmt.Errorf("init client failed: %w", err)
	}

	// setup auth if set
	var basicAuth map[string]string
	if len(cfg.BasicAuth) > 0 {
		basicAuth = make(map[string]string, len(cfg.BasicAuth))
		for _, s := range cfg.BasicAuth {
			user, password, ok := strings.Cut(s, ":")
			if !ok {
				return errors.New("basic auth format error")
			}
			basicAuth[user] = password
		}
		log.Printf("basic auth enabled, users count: %d", len(basicAuth))
	}

	errGroup, ctx := errgroup.WithContext(ctx)

	// Create the Linux gVisor TUN frontend before starting the other listeners so
	// a device/configuration failure is atomic from the operator's perspective.
	if cfg.TUN != nil {
		tunConfig, err := tun.FromClientConfig(cfg.TUN, cfg.BlockIPv6DNS)
		if err != nil {
			return fmt.Errorf("configure tun: %w", err)
		}
		tunService, err := tun.New(ctx, tunConfig)
		if err != nil {
			return fmt.Errorf("create tun: %w", err)
		}
		defer tunService.Close()

		errGroup.Go(func() error {
			if err := tunService.Run(); err != nil {
				return fmt.Errorf("serve tun: %w", err)
			}
			return nil
		})
	}

	// create socks server
	if cfg.ListenSocks != "" {
		socksCfg := &socks.Config{Credentials: basicAuth}
		s := socks.New(ctx, socksCfg)

		errGroup.Go(func() error {
			if err := s.ListenAndServe("tcp", cfg.ListenSocks); err != nil {
				return fmt.Errorf("serve socks failed: %w", err)
			}
			return nil
		})
	}

	// create socks server for unix socket
	if cfg.ListenSocksUnix != "" {
		// support Linux abstract namespace
		if cfg.ListenSocksUnix[0] != '/' {
			cfg.ListenSocksUnix = "\x00" + cfg.ListenSocksUnix
		}
		socksCfg := &socks.Config{Credentials: basicAuth}
		s := socks.New(ctx, socksCfg)

		errGroup.Go(func() error {
			if err := s.ListenAndServe("unix", cfg.ListenSocksUnix); err != nil {
				return fmt.Errorf("serve unix socks failed: %w", err)
			}
			return nil
		})
	}

	// create http server
	if cfg.ListenHttp != "" {
		httpCfg := &http.Config{Credentials: basicAuth}
		h := http.New(ctx, httpCfg)

		errGroup.Go(func() error {
			if err := h.ListenAndServe("tcp", cfg.ListenHttp); err != nil {
				return fmt.Errorf("serve http failed: %w", err)
			}
			return nil
		})
	}

	// create Linux TCP transparent redirect server
	if cfg.ListenRedirect != "" {
		redirectCfg := new(redirect.Config)
		if cfg.Redirect != nil {
			redirectCfg.MaxConnections = cfg.Redirect.MaxConnections
		}
		s, err := redirect.New(ctx, redirectCfg)
		if err != nil {
			return fmt.Errorf("configure redirect: %w", err)
		}

		errGroup.Go(func() error {
			if err := s.ListenAndServe(cfg.ListenRedirect); err != nil {
				return fmt.Errorf("serve redirect failed: %w", err)
			}
			return nil
		})
	}

	// create dns server
	if cfg.ListenDns != "" {
		dnsSrv, err := dns.NewServer(cfg.ListenDns, cfg.BlockIPv6DNS)
		if err != nil {
			return fmt.Errorf("create dns server failed: %w", err)
		}

		errGroup.Go(func() error {
			if err := dnsSrv.Start(ctx); err != nil {
				return fmt.Errorf("serve dns failed: %w", err)
			}
			return nil
		})
	}

	// listen interrupts
	errGroup.Go(func() error {
		return l.listenSignal(ctx)
	})

	// blocks main
	err := errGroup.Wait()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrSignalArrived) {
		return fmt.Errorf("inbound process error: %w", err)
	}
	return nil
}

func (l *Launcher) Launch(cfg *config.MixedConfig) error {
	if l.skipInternalLogging {
		// override configured mode
		cfg.LogMode = logger.ModeSkip
	}

	// apply config
	if err := cfg.Apply(); err != nil {
		return err
	}

	// main context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A watchdog armed by the stop signal must not outlive this call: an
	// embedder keeps running after Launch returns.
	defer l.disarmForceExit()

	// switch role
	switch cfg.Role {
	case config.RoleServer:
		return l.launchServer(ctx, cfg)
	case config.RoleClient:
		return l.launchClient(ctx, cfg)
	default:
		return fmt.Errorf("unrecognized role: %s", cfg.Role)
	}
}
