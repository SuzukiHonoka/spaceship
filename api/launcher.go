package api

import (
	"context"
	"errors"
	"fmt"
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
	sigStop  chan interface{}
	sigError chan error
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
		s, err := server.NewServer(ctx, c.Users, c.SSL)
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
		// initialize pool
		err = client.Init(c.ServerAddr, c.Host, c.EnableTLS, c.Mux, c.CAs)
		if err != nil {
			return fmt.Errorf("init client failed: %w", err)
		}
		defer client.Destroy()
		// flag
		sigError := make(chan error)
		// socks
		if c.ListenSocks != "" {
			s := socks.New(ctx, &socks.Config{})
			defer transport.ForceClose(s)
			go func() {
				if err := s.ListenAndServe("tcp", c.ListenSocks); err != nil {
					if err = fmt.Errorf("serve socks failed: %w", err); !errors.Is(err, net.ErrClosed) {
						log.Println(err)
					}
					//sigError <- err
				}
			}()
		}
		// http
		if c.ListenHttp != "" {
			h := http.New(ctx)
			defer transport.ForceClose(h)
			go func() {
				if err := h.ListenAndServe("tcp", c.ListenHttp); err != nil {
					if err = fmt.Errorf("serve http failed: %w", err); !errors.Is(err, net.ErrClosed) {
						log.Println(err)
					}
					//sigError <- err
				}
			}()
		}
		// blocks main
		go func() {
			l.waitForCancel()
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
		log.Printf("process failed: %v", err)
		return false
	}
	return true
}
