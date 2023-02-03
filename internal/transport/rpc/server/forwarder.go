package server

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	proxy "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"io"
	"log"
	"net"
	"strconv"
	"time"
)

type Forwarder struct {
	Ctx    context.Context
	Stream proxy.Proxy_ProxyServer
	Conn   net.Conn
	Ack    chan interface{}
}

func NewForwarder(ctx context.Context, stream proxy.Proxy_ProxyServer) *Forwarder {
	return &Forwarder{
		Ctx:    ctx,
		Stream: stream,
		Ack:    make(chan interface{}),
	}
}

func (c *Forwarder) Close() error {
	if c.Conn != nil {
		return c.Conn.Close()
	}
	return nil
}

func (c *Forwarder) CopyTargetToClient() error {
	// only start if ack dial succeed
	_, ok := <-c.Ack
	if !ok {
		//log.Println("ack failed")
		return transport.ErrorTargetACKFailed
	}
	// send local addr to client for nat
	msgAccept := &proxy.ProxyDST{
		Status: proxy.ProxyStatus_Accepted,
		Addr:   c.Conn.LocalAddr().String(),
	}
	if err := c.Stream.Send(msgAccept); err != nil {
		return err
	}
	// buffer
	buf := make([]byte, transport.BufferSize)
	//log.Println("reading from target connection started")
	// loop read target and forward
	for {
		select {
		case <-c.Ctx.Done():
			return nil
		default:
			if err := c.copyTargetToClient(buf); err != nil {
				return err
			}
		}
	}
}

func (c *Forwarder) copyTargetToClient(buf []byte) error {
	//log.Println("rpc server: start reading target")
	n, err := c.Conn.Read(buf)
	if err != nil {
		return err
	}
	//log.Println("rpc server -> client")
	dstData := &proxy.ProxyDST{
		Status: proxy.ProxyStatus_Session,
		Data:   buf[:n],
	}
	//err = c.Stream.Send(dstData)
	//dstData = nil
	return c.Stream.Send(dstData)
}

func (c *Forwarder) CopyClientToTarget() error {
	// do the handshake first
	req, err := c.Stream.Recv()
	if err != nil {
		return err
	}
	// check user
	if !Users.Match(req.Id) {
		return fmt.Errorf("unauthticated uuid: %s -> %w", req.Id, transport.ErrorUserNotFound)
	}
	//log.Printf("prepare for dialing: %s:%d", req.Fqdn, req.Port)
	target := net.JoinHostPort(req.Fqdn, strconv.Itoa(int(req.Port)))
	// dial to target with 3 minutes timeout as default
	c.Conn, err = net.DialTimeout(transport.Network, target, 3*time.Minute)
	if err != nil {
		_ = c.Stream.Send(&proxy.ProxyDST{
			Status: proxy.ProxyStatus_Error,
		})
		close(c.Ack)
		return fmt.Errorf("dial target error: %w", err)
	}
	// trigger read
	c.Ack <- struct{}{}
	log.Println("rpc server proxy received ->", req.Fqdn)
	// loop read client and forward
	for {
		select {
		case <-c.Ctx.Done():
			return nil
		default:
			if err := c.copyClientToTarget(); err != nil {
				return err
			}
		}
	}
}

func (c *Forwarder) copyClientToTarget() error {
	//log.Println("rpc server receiving...")
	req, err := c.Stream.Recv()
	if err != nil {
		return err
	}
	// return EOF if client closed or invalid message being received
	if req.Data == nil {
		return io.EOF
	}
	//log.Printf("RX: %s", string(data))
	// write to remote
	n, err := c.Conn.Write(req.Data)
	if err != nil {
		return err
	}
	if n != len(req.Data) {
		return fmt.Errorf("received: %d sent: %d losses: %d %w", len(req.Data), n, n/len(req.Data), transport.ErrorPacketLoss)
	}
	return nil
}
