package server

import (
	"context"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"log"
	"net"
	serverConfig "spaceship/internal/config/server"
	"spaceship/internal/transport"
	"spaceship/internal/transport/rpc"
	proxy "spaceship/internal/transport/rpc/proto"
	"strconv"
	"time"
)

type server struct {
	proxy.UnimplementedProxyServer
	Ctx   context.Context
	Users *serverConfig.Users
}

func NewServer(ctx context.Context, users *serverConfig.Users, ssl *serverConfig.SSL) *grpc.Server {
	// check users
	if users.IsNullOrEmpty() {
		log.Fatalln("users can not be empty")
	}
	// create server and register
	var transportOption grpc.ServerOption
	if ssl != nil {
		credential, err := credentials.NewServerTLSFromFile(ssl.Cert, ssl.Key)
		if err != nil {
			log.Fatalf("failed to setup TLS: %v", err)
		}
		log.Println("using secure grpc [h2]")
		transportOption = grpc.Creds(credential)
	} else {
		log.Println("using insecure grpc [h2c]")
		transportOption = grpc.Creds(insecure.NewCredentials())
	}
	s := grpc.NewServer(append(rpc.ServerOptions, transportOption)...)
	proxy.RegisterProxyServer(s, &server{
		Ctx:   ctx,
		Users: users,
	})
	return s
}

type forwarder struct {
	Server *server
	// stream data by grpc
	Stream proxy.Proxy_ProxyServer
	// conn outer connection
	Conn net.Conn
	// ack signal for target read
	Ack chan bool
}

func (c *forwarder) CopyTargetToClient() error {
	// only start if ack succeed
	status := <-c.Ack
	if !status {
		//log.Println("ack failed")
		return transport.ErrorTargetACKFailed
	}
	//log.Println("target reading")
	// send local addr to client for nat
	err := c.Stream.Send(&proxy.ProxyDST{
		Status: proxy.ProxyStatus_Accepted,
		Addr:   c.Conn.LocalAddr().String(),
	})
	if err != nil {
		return err
	}
	// buffer
	buf := make([]byte, transport.BufferSize)
	//log.Println("reading from target connection started")
	// loop read target and forward
	for {
		//log.Println("rpc server: target reading")
		// read reply to buffer
		n, err := c.Conn.Read(buf)
		// if failed
		if err != nil {
			return err
		}
		//log.Println("target read period finish")
		//log.Println("rpc server -> client")
		dstData := &proxy.ProxyDST{
			Status: proxy.ProxyStatus_Session,
			Data:   buf[:n],
		}
		err = c.Stream.Send(dstData)
		dstData = nil
		// stop if send rpc failed
		if err != nil {
			return err
		}
	}
}

func (c *forwarder) CopyClientToTarget() error {
	var handshake bool
	for {
		//log.Println("rpc server receiving...")
		// receive the request and possible error from the stream object
		req, err := c.Stream.Recv()
		// handle error from the stream object
		if err != nil {
			return err
		}
		//log.Println("authentication accepted")
		// if first ack
		if !handshake {
			// check user
			if !c.Server.Users.Match(req.Id) {
				return fmt.Errorf("unauthticated uuid: %s %w", req.Id, transport.ErrorUserNotFound)
			}
			//log.Printf("testing if ok: %s:%d", req.Fqdn, req.Port)
			// finally create the dialer
			target := net.JoinHostPort(req.Fqdn, strconv.Itoa(int(req.Port)))
			// dial to target with 3 minute timeout
			c.Conn, err = net.DialTimeout(transport.Network, target, 3*time.Minute)
			// dialer dial failed
			if err != nil {
				_ = c.Stream.Send(&proxy.ProxyDST{
					Status: proxy.ProxyStatus_Error,
				})
				c.Ack <- false
				return err
			}
			// trigger read
			c.Ack <- true
			handshake = true
			log.Println("rpc server proxy received ->", req.Fqdn)
			continue
		}
		// client closed or invalid message
		if req.Data == nil {
			return io.EOF
		}
		//log.Printf("RX: %s", string(data))
		n, err := c.Conn.Write(req.Data)
		if err != nil {
			// log.Printf("error when sending client request to target stream: %v", err)
			return err
		}
		if n != len(req.Data) {
			return fmt.Errorf("received: %d sent: %d loss: %d %w", len(req.Data), n, n/len(req.Data), transport.ErrorPacketLoss)
		}
	}
}

func (s *server) Proxy(stream proxy.Proxy_ProxyServer) error {
	//log.Println("rpc server incomes")
	// block main until canceled
	ctx, cancel := context.WithCancel(s.Ctx)
	// forwarder
	f := &forwarder{
		Server: s,
		Stream: stream,
		Ack:    make(chan bool),
	}
	// target <- client
	go func() {
		err := f.CopyClientToTarget()
		transport.PrintErrorIfNotCritical(err, "rpc: client -> target error")
		cancel()
	}()
	// target -> client
	go func() {
		err := f.CopyTargetToClient()
		transport.PrintErrorIfNotCritical(err, "rpc: client <- target error")
		cancel()
	}()
	<-ctx.Done()
	// close target connection
	if f.Conn != nil {
		_ = f.Conn.Close()
	}
	// send session end to client
	_ = stream.Send(&proxy.ProxyDST{
		Status: proxy.ProxyStatus_EOF,
	})
	return nil
}
