package api

import (
	"context"
	"github.com/SuzukiHonoka/spaceship/internal/config"
	"github.com/SuzukiHonoka/spaceship/internal/transport/http"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc/server"
	"github.com/SuzukiHonoka/spaceship/internal/transport/socks"
	"github.com/SuzukiHonoka/spaceship/internal/util"
	"github.com/google/uuid"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
)

func Launch(c *config.MixedConfig) {
	c.Apply()
	// main context
	ctx := context.Background()
	// switch role
	switch c.Role {
	case config.RoleServer:
		// server start
		log.Println("server starting")
		s := server.NewServer(ctx, c.Users, c.SSL)
		// listen ingress and serve
		l, err := net.Listen("tcp", c.Listen)
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
			return
		}
		defer client.Destroy()
		// socks
		if c.ListenSocks != "" {
			go func() {
				s := socks.New(ctx, &socks.Config{})
				log.Fatal(s.ListenAndServe("tcp", c.ListenSocks))
			}()
		}
		// http
		if c.ListenHttp != "" {
			go func() {
				h := http.New(ctx)
				log.Fatal(h.ListenAndServe("tcp", c.ListenHttp))
			}()
		}
		// blocks main
		waitForCancel()
	default:
		panic("unrecognized role")
	}
}

func waitForCancel() {
	sys := make(chan os.Signal, 1)
	signal.Notify(sys, syscall.SIGKILL, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-sys:
	case <-sigStop:
	}
}
