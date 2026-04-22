package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"golang.org/x/sync/errgroup"
)

type Forwarder struct {
	Ctx       context.Context
	Stream    proto.Proxy_ProxyServer
	Conn      net.Conn
	Ack       chan interface{}
	closeOnce sync.Once
}

func NewForwarder(ctx context.Context, stream proto.Proxy_ProxyServer) *Forwarder {
	return &Forwarder{
		Ctx:    ctx,
		Stream: stream,
		Ack:    make(chan interface{}, 1),
	}
}

func (f *Forwarder) Close() error {
	var err error
	f.closeOnce.Do(func() {
		if f.Conn != nil {
			err = f.Conn.Close()
		}
	})
	return err
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
	errCh := make(chan error, 1)
	go func() {
		buf := transport.Buffer()
		defer transport.PutBuffer(buf)

		// reuse buffer
		dstData := &proto.ProxyDST{
			HeaderOrPayload: &proto.ProxyDST_Payload{
				Payload: nil,
			},
		}
		// wrapper
		payload := dstData.HeaderOrPayload.(*proto.ProxyDST_Payload)

		b := *buf
		for {
			if readErr := f.copyTargetToClient(b, dstData, payload); readErr != nil {
				errCh <- readErr
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		// Close target connection to unblock the goroutine's Read,
		// then wait for it to exit — prevents Send race with Proxy().
		_ = f.Close()
		return ctx.Err()
	case err = <-errCh:
		return err
	}
}

func (f *Forwarder) copyTargetToClient(buf []byte, dstData *proto.ProxyDST, payload *proto.ProxyDST_Payload) error {
	n, err := f.Conn.Read(buf)
	// Per io.Reader contract: process n > 0 bytes before considering error.
	// Prevents dropping the last chunk when Read returns data + io.EOF.
	if n > 0 {
		payload.Payload = buf[:n]
		if sendErr := f.Stream.Send(dstData); sendErr != nil {
			return sendErr
		}
	}
	return err
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

	// Auth is handled by the stream interceptor.
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
	if f.Conn, err = route.Dial(transport.GetNetwork(), header.Addr); err != nil {
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
	if err := f.handshake(); err != nil {
		return fmt.Errorf("handshake error: %w", err)
	}

	// trigger read
	f.Ack <- struct{}{}

	// loop read client and forward
	errCh := make(chan error, 1)
	go func() {
		// reuse buffer
		srcData := new(proto.ProxySRC)
		for {
			// reset for new message
			srcData.Reset()
			if err := f.Stream.RecvMsg(srcData); err != nil {
				errCh <- err
				return
			}

			if err := f.copyClientToTarget(srcData); err != nil {
				errCh <- err
				return
			}
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		_ = f.Close()
		return ctx.Err()
	}
}

func (f *Forwarder) copyClientToTarget(buf *proto.ProxySRC) error {
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
	n, err := f.Conn.Write(v.Payload)
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
