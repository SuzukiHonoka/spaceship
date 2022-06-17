package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/google/uuid"
	"log"
	"net"
	"spaceship/internal/config"
	"spaceship/internal/transport/http"
	"spaceship/internal/transport/rpc"
	"spaceship/internal/transport/socks"
	"spaceship/internal/util"
)

const VersionName = "1.2.1"

func main() {
	// first prompt
	fmt.Printf("spaceship v%s ", VersionName)
	fmt.Println("for personal use only, without any warranty, any illegal action made by using this program are on your own.")
	// load configuration
	configPath := flag.String("c", "./config.json", "config path")
	flag.Parse()
	c := config.Load(*configPath)
	// set default dns if configured
	if c.DNS != nil {
		c.DNS.SetDefault()
	}
	// main context
	ctx := context.Background()
	// switch role
	switch c.Role {
	case config.RoleServer:
		// check users
		if c.Users == nil || len(c.Users) == 0 {
			panic("users can not be empty")
		}
		// server start
		log.Println("server starting")
		s := rpc.NewServer(ctx)
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
		select {}
	default:
		panic("unrecognized role")
	}
}
