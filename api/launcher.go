package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/dns"
	"github.com/SuzukiHonoka/spaceship/v2/internal/http"
	"github.com/SuzukiHonoka/spaceship/v2/internal/socks"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/server"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/config"
	"github.com/SuzukiHonoka/spaceship/v2/pkg/logger"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

type Launcher struct {
	sigStop             chan struct{}
	skipInternalLogging bool
	stopOnce            sync.Once
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

	// create server
	s, err := server.NewServer(ctx, cfg.Users, cfg.SSL, cfg.DNS)
	if err != nil {
		return fmt.Errorf("create server failed: %w", err)
	}
	return s.ListenAndServe(cfg.Listen)
}

func (l *Launcher) launchClient(ctx context.Context, cfg *config.MixedConfig) error {
	log.Println("client starting")

	// check uuid format
	if _, err := uuid.Parse(cfg.UUID); err != nil {
		return err
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

	// create socks server
	if cfg.ListenSocks != "" {
		socksCfg := &socks.Config{Credentials: basicAuth}
		s := socks.New(ctx, socksCfg)
		defer utils.Close(s)

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

	// create dns server
	if cfg.ListenDns != "" {
		dnsSrv, err := dns.NewServer(cfg.ListenDns)
		if err != nil {
			return fmt.Errorf("create dns server failed: %w", err)
		}

		go func() {
			<-ctx.Done()
			utils.Close(dnsSrv)
		}()

		errGroup.Go(func() error {
			if err := dnsSrv.Start(); err != nil {
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
