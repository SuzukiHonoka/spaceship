package api

import (
	"context"
	"github.com/google/uuid"
	"log"
	"net"
	"os"
	"os/signal"
	"spaceship/internal/config"
	"spaceship/internal/transport/http"
	"spaceship/internal/transport/rpc/client"
	"spaceship/internal/transport/rpc/server"
	"spaceship/internal/transport/socks"
	"spaceship/internal/util"
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
		cancel := make(chan os.Signal, 1)
		signal.Notify(cancel, syscall.SIGKILL, syscall.SIGTERM, syscall.SIGINT)
		<-cancel
	default:
		panic("unrecognized role")
	}
}
