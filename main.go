package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"spaceship/internal/config"
	"spaceship/internal/config/client"
	"spaceship/internal/transport/rpc"
	"spaceship/internal/transport/socks"
	"spaceship/internal/util"
)

const VersionName = "1.0"

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
	// switch role
	switch c.Role {
	case config.RoleServer:
		// server start
		log.Println("server starting")
		s := rpc.NewServer(c.Users)
		// listen ingress and serve
		l, err := net.Listen("tcp", c.Listen)
		util.StopIfError(err)
		log.Printf("rpc started at %s", c.Listen)
		log.Fatal(s.Serve(l))

	case config.RoleClient:
		// client start
		log.Println("client starting")
		s := socks.New(&socks.Config{})
		rpc.ClientConfig = &client.Client{
			ServerAddr:  c.ServerAddr,
			UUID:        c.UUID,
			ListenSocks: c.ListenSocks,
			TLS:         c.TLS,
		}
		log.Fatal(s.ListenAndServe("tcp", c.ListenSocks))
	default:
		panic("unrecognized role")
	}
}
