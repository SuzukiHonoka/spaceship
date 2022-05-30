package rpc

import (
	"context"
	"fmt"
	"google.golang.org/grpc"
	"log"
	"net"
	"spaceship/internal/transport"
	proxy "spaceship/internal/transport/rpc/proto"
	"time"
)

type server struct {
	proxy.UnimplementedProxyServer
	Ctx context.Context
}

func NewServer(ctx context.Context) *grpc.Server {
	// create server and register
	// without buffer for less delay
	s := grpc.NewServer(grpc.ReadBufferSize(0), grpc.WriteBufferSize(0))
	proxy.RegisterProxyServer(s, &server{Ctx: ctx})
	return s
}

type forwarder struct {
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
		// write back
		//log.Println("rpc server -> client")
		err = c.Stream.Send(&proxy.ProxyDST{
			Status: proxy.ProxyStatus_Session,
			Data:   buf[:n],
			//Addr:   conn.LocalAddr().String(),
		})
		// stop if send rpc failed
		if err != nil {
			return err
		}
	}
}

func (c *forwarder) CopyClientToTarget() error {
	var handshake bool
	for {
		//log.Println("rpc server receiving..")
		// receive the request and possible error from the stream object
		req, err := c.Stream.Recv()
		// handle error from the stream object
		if err != nil {
			return err
		}
		// check user
		if _, ok := transport.UUIDs[req.Id]; !ok {
			return fmt.Errorf("unauthticated uuid: %s %w", req.Id, transport.ErrorUserNotFound)
		}
		//log.Println("authentication accepted")
		// if first ack
		if !handshake {
			//log.Printf("testing if ok: %s:%d", req.Fqdn, req.Port)
			// finally create the dialer
			target := transport.GetTargetDst(req.Fqdn, int(req.Port))
			// dial to target with 3 minute timeout
			c.Conn, err = net.DialTimeout("tcp", target, 3*time.Minute)
			// dialer dial failed
			if sendErrorStatusIfError(err, c.Stream) {
				// ack failed
				c.Ack <- false
				return err
			}
			// trigger read
			c.Ack <- true
			handshake = true
			log.Println("rpc server proxy received ->", req.Fqdn)
		}
		// after first ack
		if req.Data == nil {
			continue
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
	f := &forwarder{Stream: stream, Ack: make(chan bool)}
	// target <- client
	go func() {
		err := f.CopyClientToTarget()
		if err != nil {
			transport.PrintErrorIfNotEOF(err, "error occurred while proxying")
		}
		cancel()
	}()
	// target -> client
	go func() {
		err := f.CopyTargetToClient()
		if err != nil {
			transport.PrintErrorIfNotEOF(err, "error occurred while proxying")
		}
		cancel()
	}()
	<-ctx.Done()
	// close target connection
	if f.Conn != nil {
		_ = f.Conn.Close()
	}
	// send session end to client
	err := stream.Send(&proxy.ProxyDST{
		Status: proxy.ProxyStatus_EOF,
		//Addr:   conn.LocalAddr().String(),
	})
	if err != nil {
		log.Printf("send session EOF to client failed: %v", err)
	}
	return nil
}
