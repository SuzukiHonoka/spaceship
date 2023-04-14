package api

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/forward"
	"github.com/SuzukiHonoka/spaceship/internal/http"
	"github.com/SuzukiHonoka/spaceship/internal/socks"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc/server"
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
	err := c.Apply()
	if err != nil {
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
		var pd *forward.Forward
		var err error
		if c.Proxy != "" {
			pd, err = forward.NewForward(c.Proxy)
			if err != nil {
				return err
			}
		}
		s, err := server.NewServer(ctx, c.Users, c.SSL, pd)
		if err != nil {
			return fmt.Errorf("create server failed: %w", err)
		}
		// listen ingress and serve
		listener, err := net.Listen("tcp", c.Listen)
		if err != nil {
			return fmt.Errorf("listen at %s error %w", c.Listen, err)
		}
		defer transport.ForceClose(listener)
		log.Printf("rpc started at %s", c.Listen)
		if err = s.Serve(listener); err != nil {
			return err
		}

	case config.RoleClient:
		// check uuid format
		_, err := uuid.Parse(c.UUID)
		if err != nil {
			log.Printf("current uuid is not valid: %v, using simple text as uuid now is accepted but use it at your own risk", err)
		}
		// client start
		log.Println("client starting")
		// destroy any left connections
		defer client.Destroy()
		// initialize pool
		if err = client.Init(c.ServerAddr, c.Host, c.EnableTLS, c.Mux, c.CAs); err != nil {
			return fmt.Errorf("init client failed: %w", err)
		}
		// flag
		var signalArrived bool
		sigError := make(chan error)
		// socks
		if c.ListenSocks != "" {
			s := socks.New(ctx, &socks.Config{})
			defer transport.ForceClose(s)
			go func() {
				err := s.ListenAndServe("tcp", c.ListenSocks)
				if err != nil {
					err = fmt.Errorf("serve socks failed: %w", err)
				}
				if !signalArrived {
					sigError <- err
				}
			}()
		}
		// http
		if c.ListenHttp != "" {
			h := http.New(ctx)
			defer transport.ForceClose(h)
			go func() {
				err := h.ListenAndServe("tcp", c.ListenHttp)
				if err != nil {
					err = fmt.Errorf("serve http failed: %w", err)
				}
				if !signalArrived {
					sigError <- err
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
