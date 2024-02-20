package server

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/router"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"io"
	"log"
	"net"
	"strconv"
)

type Forwarder struct {
	Ctx    context.Context
	Stream proto.Proxy_ProxyServer
	Conn   net.Conn
	Ack    chan interface{}
}

func NewForwarder(ctx context.Context, stream proto.Proxy_ProxyServer) *Forwarder {
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
		return transport.ErrTargetACKFailed
	}
	// send local addr to client for nat
	msgAccept := &proto.ProxyDST{
		Status: proto.ProxyStatus_Accepted,
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
	dstData := &proto.ProxyDST{
		Status: proto.ProxyStatus_Session,
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
		return fmt.Errorf("unauthticated uuid: %s -> %w", req.Id, transport.ErrUserNotFound)
	}
	//log.Printf("prepare for dialing: %s:%d", req.Host, req.Port)
	route, err := router.GetRoute(req.Fqdn)
	if err != nil {
		return fmt.Errorf("get route for [%s] error: %w", req.Fqdn, err)
	}
	log.Printf("proxy accepted: %s -> %s", req.Fqdn, route)
	target := net.JoinHostPort(req.Fqdn, strconv.Itoa(int(req.Port)))
	// dial to target with 3 minutes timeout as default
	c.Conn, err = route.Dial(transport.Network, target)
	if err != nil {
		_ = c.Stream.Send(&proto.ProxyDST{
			Status: proto.ProxyStatus_Error,
		})
		close(c.Ack)
		return fmt.Errorf("dial target error: %w", err)
	}
	// trigger read
	c.Ack <- struct{}{}
	// loop read client and forward
	var buf proto.ProxySRC
	for {
		select {
		case <-c.Ctx.Done():
			return nil
		default:
			if err = c.copyClientToTarget(&buf); err != nil {
				return err
			}
		}
	}
}

func (c *Forwarder) copyClientToTarget(buf *proto.ProxySRC) error {
	//log.Println("rpc server receiving...")
	var err error
	if buf, err = c.Stream.Recv(); err != nil {
		return err
	}
	// return EOF if client closed or invalid message being received
	if buf.Data == nil {
		return io.EOF
	}
	//log.Printf("RX: %s", string(data))
	// write to remote
	if _, err = c.Conn.Write(buf.Data); err != nil {
		return err
	}
	return nil
}
