package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"golang.org/x/sync/errgroup"
)

type Forwarder struct {
	Ctx           context.Context
	UsersMatchMap *config.UsersMatchMap
	Stream        proto.Proxy_ProxyServer
	Conn          net.Conn
	Ack           chan interface{}
}

func NewForwarder(ctx context.Context, m *config.UsersMatchMap, stream proto.Proxy_ProxyServer) *Forwarder {
	return &Forwarder{
		Ctx:           ctx,
		UsersMatchMap: m,
		Stream:        stream,
		Ack:           make(chan interface{}, 1),
	}
}

func (f *Forwarder) Close() error {
	if f.Conn != nil {
		return f.Conn.Close()
	}
	return nil
}

func (f *Forwarder) CopyTargetToClient(ctx context.Context) (err error) {
	// only start if ack dial succeed
	_, ok := <-f.Ack
	if !ok {
		//log.Println("ack failed")
		return transport.ErrTargetACKFailed
	}

	// send local addr to client for nat
	msgAccept := &proto.ProxyDST{
		Status: proto.ProxyStatus_Accepted,
		HeaderOrPayload: &proto.ProxyDST_Header{
			Header: &proto.ProxyDST_ProxyHeader{
				Addr: f.Conn.LocalAddr().String(),
			},
		},
	}
	if err = f.Stream.Send(msgAccept); err != nil {
		return fmt.Errorf("send local addr to client error: %w", err)
	}

	//log.Println("reading from target connection started")
	// loop read target and forward
	errCh := make(chan struct{}, 1)
	go func() {
		buf := transport.Buffer()
		defer transport.PutBuffer(buf)

		for {
			err = f.copyTargetToClient(buf)
			if err != nil {
				errCh <- struct{}{}
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-errCh:
		return err
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
		HeaderOrPayload: &proto.ProxyDST_Payload{
			Payload: buf[:n],
		},
	}

	//err = c.stream.Send(dstData)
	//dstData = nil
	return f.Stream.Send(dstData)
}

func (f *Forwarder) handshake() error {
	req, err := f.Stream.Recv()
	if err != nil {
		return err
	}

	v, ok := req.HeaderOrPayload.(*proto.ProxySRC_Header)
	if !ok {
		return transport.ErrInvalidMessage
	}
	header := v.Header

	// check user
	if !f.UsersMatchMap.Match(header.Id) {
		return fmt.Errorf("%w: uuid=%s", transport.ErrUserNotFound, header.Id)
	}

	//log.Printf("prepare for dialing: %s:%d", req.Host, req.Port)
	host, _, err := net.SplitHostPort(header.Addr)
	if err != nil {
		return fmt.Errorf("invalid addr %s: %w", header.Addr, err)
	}
	route, err := router.GetRoute(host)
	if err != nil {
		return fmt.Errorf("get route for %s error: %w", host, err)
	}
	log.Printf("proxy accepted: %s -> %s", host, route)

	// dial to target with 3 minutes timeout as default
	if f.Conn, err = route.Dial(transport.Network, header.Addr); err != nil {
		_ = f.Stream.Send(&proto.ProxyDST{
			Status: proto.ProxyStatus_Error,
		})
		return fmt.Errorf("dial target error: %w", err)
	}
	return nil
}

func (f *Forwarder) CopyClientToTarget(ctx context.Context) error {
	defer close(f.Ack)

	// do the handshake first
	err := f.handshake()
	if err != nil {
		return fmt.Errorf("handshake error: %w", err)
	}

	// trigger read
	f.Ack <- struct{}{}

	// loop read client and forward
	errCh := make(chan struct{}, 1)
	go func() {
		for {
			err = f.copyClientToTarget()
			if err != nil {
				errCh <- struct{}{}
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-errCh:
		return err
	}
}

func (f *Forwarder) copyClientToTarget() error {
	//log.Println("rpc server receiving...")
	buf, err := f.Stream.Recv()
	if err != nil {
		return err
	}

	v, ok := buf.HeaderOrPayload.(*proto.ProxySRC_Payload)
	if !ok {
		return transport.ErrInvalidMessage
	}

	// return EOF if client closed or invalid message being received
	if len(v.Payload) == 0 {
		return io.EOF
	}
	//log.Printf("RX: %s", string(data))

	// write to remote
	var n int
	n, err = f.Conn.Write(v.Payload)
	if n < len(v.Payload) {
		return io.ErrShortWrite
	}
	return err
}

func (f *Forwarder) Start() error {
	errGroup, ctx := errgroup.WithContext(f.Ctx)
	errGroup.Go(func() error {
		if err := f.CopyClientToTarget(ctx); err != nil {
			if err == io.EOF {
				return err
			}
			return fmt.Errorf("copy client to target error: %w", err)
		}
		return nil
	})
	errGroup.Go(func() error {
		if err := f.CopyTargetToClient(ctx); err != nil {
			if err == io.EOF {
				return err
			}
			return fmt.Errorf("copy target to client error: %w", err)
		}
		return nil
	})

	return errGroup.Wait()
}
