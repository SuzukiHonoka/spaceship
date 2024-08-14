package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/http"
	"github.com/SuzukiHonoka/spaceship/internal/socks"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc/server"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"github.com/SuzukiHonoka/spaceship/pkg/config"
	"github.com/google/uuid"
	"log"
	"strings"
)

type Launcher struct {
	sigStop chan struct{}
}

func NewLauncher() *Launcher {
	return &Launcher{
		sigStop: make(chan struct{}),
	}
}

func (l *Launcher) launchServer(ctx context.Context, cfg *config.MixedConfig) error {
	log.Println("server starting")

	// create server
	s, err := server.NewServer(ctx, cfg.Users, cfg.SSL)
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

	// error channel for http/socks server
	errChan := make(chan error)

	// create socks server
	if cfg.ListenSocks != "" {
		socksCfg := new(socks.Config)

		// setup auth if set
		if cfg.BasicAuth != "" {
			user, password, ok := strings.Cut(cfg.BasicAuth, ":")
			if !ok {
				return errors.New("basic auth format error")
			}
			socksCfg.Credentials = map[string]string{
				user: password,
			}
		}

		s := socks.New(ctx, socksCfg)
		defer utils.Close(s)
		go func() {
			if err := s.ListenAndServe("tcp", cfg.ListenSocks); err != nil {
				errChan <- fmt.Errorf("serve socks failed: %w", err)
			}
			errChan <- nil
		}()
	}

	// create http server
	if cfg.ListenHttp != "" {
		h := http.New(ctx)
		defer utils.Close(h)
		go func() {
			if err := h.ListenAndServe("tcp", cfg.ListenHttp); err != nil {
				errChan <- fmt.Errorf("serve http failed: %w", err)
			}
			errChan <- nil
		}()
	}

	// listen interrupts
	go func() {
		l.listenSignal()
	}()

	// blocks main
	select {
	case err := <-errChan:
		if err != nil {
			return fmt.Errorf("inbound process error: %w", err)
		}
	case <-l.sigStop:
	}
	return nil
}

func (l *Launcher) LaunchWithError(cfg *config.MixedConfig) error {
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

func (l *Launcher) Launch(c *config.MixedConfig) bool {
	if err := l.LaunchWithError(c); err != nil {
		log.Printf("launch error: %v", err)
		return false
	}
	return true
}
