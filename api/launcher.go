package api

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/http"
	"github.com/SuzukiHonoka/spaceship/internal/socks"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc/server"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"github.com/SuzukiHonoka/spaceship/pkg/config"
	"github.com/google/uuid"
	"log"
	"net"
)

type Launcher struct {
	sigStop chan interface{}
}

func NewLauncher() *Launcher {
	return &Launcher{
		sigStop: make(chan interface{}),
	}
}

func (l *Launcher) LaunchWithError(c *config.MixedConfig) error {
	if err := c.Apply(); err != nil {
		return err
	}
	// main context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// switch role
	switch c.Role {
	case config.RoleServer:
		// server start
		log.Println("server starting")
		s, err := server.NewServer(ctx, c.Users, c.SSL)
		if err != nil {
			return fmt.Errorf("create server failed: %w", err)
		}
		// listen ingress and serve
		listener, err := net.Listen("tcp", c.Listen)
		if err != nil {
			return fmt.Errorf("listen at %s error %w", c.Listen, err)
		}
		defer utils.ForceClose(listener)
		log.Printf("rpc started at %s", c.Listen)
		if err = s.Serve(listener); err != nil {
			return err
		}

	case config.RoleClient:
		// check uuid format
		if _, err := uuid.Parse(c.UUID); err != nil {
			return err
		}
		// client start
		log.Println("client starting")
		// destroy any left connections
		defer client.Destroy()
		// initialize pool
		if err := client.Init(c.ServerAddr, c.Host, c.EnableTLS, c.Mux, c.CAs); err != nil {
			return fmt.Errorf("init client failed: %w", err)
		}
		// flag
		var signalArrived bool
		sigError := make(chan error)
		// socks
		if c.ListenSocks != "" {
			s := socks.New(ctx, &socks.Config{})
			defer utils.ForceClose(s)
			go func() {
				if err := s.ListenAndServe("tcp", c.ListenSocks); err != nil && !signalArrived {
					sigError <- fmt.Errorf("serve socks failed: %w", err)
				}
			}()
		}
		// http
		if c.ListenHttp != "" {
			h := http.New(ctx)
			defer utils.ForceClose(h)
			go func() {
				if err := h.ListenAndServe("tcp", c.ListenHttp); err != nil && !signalArrived {
					sigError <- fmt.Errorf("serve http failed: %w", err)
				}
			}()
		}
		// blocks main
		go func() {
			l.waitForCancel()
			signalArrived = true
			close(sigError)
		}()
		select {
		case err, ok := <-sigError:
			if ok {
				return fmt.Errorf("inbound process error: %w", err)
			}
		}
	default:
		return fmt.Errorf("unrecognized role: %s", c.Role)
	}
	return nil
}

func (l *Launcher) Launch(c *config.MixedConfig) bool {
	if err := l.LaunchWithError(c); err != nil {
		log.Println(err)
		return false
	}
	return true
}
