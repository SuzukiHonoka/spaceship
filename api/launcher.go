package api

import (
	"context"
	"github.com/SuzukiHonoka/spaceship/internal/http"
	"github.com/SuzukiHonoka/spaceship/internal/socks"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc/server"
	"github.com/SuzukiHonoka/spaceship/internal/util"
	"github.com/SuzukiHonoka/spaceship/pkg/config"
	"github.com/google/uuid"
	"log"
	"net"
)

type Launcher struct {
	sigStop chan interface{}
}

func NewLauncher() *Launcher {
	return &Launcher{sigStop: make(chan interface{})}
}

func (l *Launcher) Launch(c *config.MixedConfig) {
	c.Apply()
	// main context
	ctx, cancel := context.WithCancel(context.Background())
	// switch role
	switch c.Role {
	case config.RoleServer:
		// server start
		log.Println("server starting")
		s := server.NewServer(ctx, c.Users, c.SSL)
		// listen ingress and serve
		l, err := net.Listen("tcp", c.Listen)
		defer transport.ForceClose(l)
		util.StopIfError(err)
		log.Printf("rpc started at %s", c.Listen)
		log.Fatal(s.Serve(l))
	case config.RoleClient:
		// check uuid format
		_, err := uuid.Parse(c.UUID)
		if err != nil {
			log.Printf("current uuid setting is not a valid uuid: %v, using simple text as uuid now is accepted but use it at your own risk", err)
		}
		// client start
		log.Println("client starting")
		// initialize pool
		err = client.Init(c.ServerAddr, c.Host, c.EnableTLS, c.Mux, c.CAs)
		if err != nil {
			log.Printf("Init client failed: %v", err)
			cancel()
			return
		}
		defer client.Destroy()
		// socks
		if c.ListenSocks != "" {
			s := socks.New(ctx, &socks.Config{})
			defer transport.ForceClose(s)
			go func() {
				err := s.ListenAndServe("tcp", c.ListenSocks)
				if err != nil {
					log.Fatalf("serve socks failed: %v", err)
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
					log.Fatalf("serve http failed: %v", err)
				}
			}()
		}
		// blocks main
		l.waitForCancel()
	default:
		panic("unrecognized role")
	}
	cancel()
}
