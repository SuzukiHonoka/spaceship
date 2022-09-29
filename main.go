package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/google/uuid"
	"log"
	"net"
	"os"
	"os/signal"
	"spaceship/internal/config"
	"spaceship/internal/transport"
	"spaceship/internal/transport/http"
	"spaceship/internal/transport/rpc"
	proxy "spaceship/internal/transport/rpc/proto"
	"spaceship/internal/transport/socks"
	"spaceship/internal/util"
	"syscall"
)

func main() {
	// first prompt
	fmt.Printf("spaceship v%s ", config.VersionCode)
	fmt.Println("for personal use only, absolutely without any warranty, any kind of illegal intention by using this program are strongly forbidden.")
	// load configuration
	configPath := flag.String("c", "./config.json", "config path")
	flag.Parse()
	c := config.Load(*configPath)
	c.LoggerMode.Set()
	// set default dns if configured
	c.DNS.SetDefault()
	if c.Buffer > 0 {
		log.Printf("custom buffer size: %dK", c.Buffer)
		transport.SetBufferSize(int(c.Buffer) * 1024)
	}
	if c.Path != "" {
		log.Printf("custom service name: %s", c.Path)
		proxy.SetServiceName(c.Path)
	}
	if !c.IPv6 {
		log.Println("ipv6 is disabled")
		transport.SetNetwork("tcp4")
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
		// initialize pool
		err = rpc.PoolInit()
		if err != nil {
			rpc.ClientPool.Destroy()
			panic(err)
		}
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
		rpc.ClientPool.Destroy()
	default:
		panic("unrecognized role")
	}
}
