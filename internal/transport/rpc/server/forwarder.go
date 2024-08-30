package server

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/router"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	config "github.com/SuzukiHonoka/spaceship/pkg/config/server"
	"io"
	"log"
	"net"
	"strconv"
)

type Forwarder struct {
	Ctx    context.Context
	Users  config.Users
	Stream proto.Proxy_ProxyServer
	Conn   net.Conn
	Ack    chan interface{}
}

func NewForwarder(ctx context.Context, users config.Users, stream proto.Proxy_ProxyServer) *Forwarder {
	return &Forwarder{
		Ctx:    ctx,
		Users:  users,
		Stream: stream,
		Ack:    make(chan interface{}),
	}
}

func (f *Forwarder) Close() error {
	if f.Conn != nil {
		return f.Conn.Close()
	}
	return nil
}

func (f *Forwarder) CopyTargetToClient() error {
	// only start if ack dial succeed
	_, ok := <-f.Ack
	if !ok {
		//log.Println("ack failed")
		return transport.ErrTargetACKFailed
	}

	// send local addr to client for nat
	msgAccept := &proto.ProxyDST{
		Status: proto.ProxyStatus_Accepted,
		Addr:   f.Conn.LocalAddr().String(),
	}
	if err := f.Stream.Send(msgAccept); err != nil {
		return fmt.Errorf("send local addr to client error: %w", err)
	}

	// buffer
	buf := transport.AllocateBuffer()
	//log.Println("reading from target connection started")
	// loop read target and forward
	for {
		select {
		case <-f.Ctx.Done():
			return nil
		default:
			if err := f.copyTargetToClient(buf); err != nil {
				return err
			}
		}
	}
}

func (f *Forwarder) copyTargetToClient(buf []byte) error {
	//log.Println("rpc server: start reading target")
	n, err := f.Conn.Read(buf)
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
	return f.Stream.Send(dstData)
}

func (f *Forwarder) handshake() error {
	req, err := f.Stream.Recv()
	if err != nil {
		return err
	}

	// check user
	if !f.Users.Match(req.Id) {
		return fmt.Errorf("%w: uuid=%s", transport.ErrUserNotFound, req.Id)
	}

	//log.Printf("prepare for dialing: %s:%d", req.Host, req.Port)
	route, err := router.GetRoute(req.Fqdn)
	if err != nil {
		return fmt.Errorf("get route for %s error: %w", req.Fqdn, err)
	}
	log.Printf("proxy accepted: %s -> %s", req.Fqdn, route)

	target := net.JoinHostPort(req.Fqdn, strconv.FormatUint(uint64(req.Port), 10))

	// dial to target with 3 minutes timeout as default
	if f.Conn, err = route.Dial(transport.Network, target); err != nil {
		_ = f.Stream.Send(&proto.ProxyDST{
			Status: proto.ProxyStatus_Error,
		})
		return fmt.Errorf("dial target error: %w", err)
	}
	return nil
}

func (f *Forwarder) CopyClientToTarget() error {
	defer close(f.Ack)

	// do the handshake first
	err := f.handshake()
	if err != nil {
		return fmt.Errorf("handshake error: %w", err)
	}

	// trigger read
	f.Ack <- struct{}{}

	// loop read client and forward
	buf := new(proto.ProxySRC)
	for {
		select {
		case <-f.Ctx.Done():
			return nil
		default:
			if err = f.copyClientToTarget(buf); err != nil {
				return err
			}
		}
	}
}

func (f *Forwarder) copyClientToTarget(buf *proto.ProxySRC) error {
	//log.Println("rpc server receiving...")
	var err error
	if buf, err = f.Stream.Recv(); err != nil {
		return err
	}

	// return EOF if client closed or invalid message being received
	if buf.Data == nil {
		return io.EOF
	}
	//log.Printf("RX: %s", string(data))

	// write to remote
	_, err = f.Conn.Write(buf.Data)
	return err
}

func (f *Forwarder) Start() error {
	// always close conn
	defer utils.Close(f)

	// buffered err ch
	proxyErr := make(chan error, 2)

	// target <- client
	go func() {
		err := f.CopyClientToTarget()
		if err != nil {
			err = fmt.Errorf("stream copy failed: client -> target, err=%w", err)
		}
		proxyErr <- err
	}()

	// target -> client
	go func() {
		err := f.CopyTargetToClient()
		if err != nil {
			err = fmt.Errorf("stream copy failed: client <- target, err=%w", err)
		}
		proxyErr <- err
	}()

	// return the last error
	var err error
	for i := 0; i < 2; i++ {
		err = <-proxyErr
	}

	return err
}
