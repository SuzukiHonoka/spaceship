package main

import (
	"flag"
	"log"
	"net"
	"spaceship/internal/config"
	"spaceship/internal/config/client"
	"spaceship/internal/transport/rpc"
	"spaceship/internal/transport/socks"
	"spaceship/internal/util"
)

func main() {
	configPath := flag.String("c", "./config.json", "config path")
	flag.Parse()
	c := config.Load(*configPath)
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
	}
}
