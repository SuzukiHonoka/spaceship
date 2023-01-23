package client

import (
	"context"
	"fmt"
	"io"
	"spaceship/internal/transport"
	proxy "spaceship/internal/transport/rpc/proto"
)

type Forwarder struct {
	Ctx       context.Context
	Stream    proxy.Proxy_ProxyClient
	Writer    io.Writer
	Reader    io.Reader
	LocalAddr chan<- string
}

func NewForwarder(ctx context.Context, s proxy.Proxy_ProxyClient, w io.Writer, r io.Reader, ch chan<- string) *Forwarder {
	return &Forwarder{
		Ctx:       ctx,
		Stream:    s,
		Writer:    w,
		Reader:    r,
		LocalAddr: ch,
	}
}

func (c *Forwarder) copySRCtoTarget(buf []byte) error {
	//log.Println("rpc client reading...")
	//read from src
	n, err := c.Reader.Read(buf)
	if err != nil {
		return err
	}
	//fmt.Printf("<----- packet size: %d\n%s\n", n, buf)
	// send to rpc
	srcData := &proxy.ProxySRC{
		Data: buf[:n],
	}
	err = c.Stream.Send(srcData)
	return err
	//log.Println("rpc client msg forwarded")
}

func (c *Forwarder) CopyTargetToSRC() error {
	for {
		select {
		case <-c.Ctx.Done():
			return nil
		default:
			if err := c.copyTargetToSRC(); err != nil {
				return err
			}
		}
	}
}

func (c *Forwarder) copyTargetToSRC() error {
	//log.Println("rpc server reading..")
	res, err := c.Stream.Recv()
	if err != nil {
		return err
	}
	//log.Printf("rpc client on receive: %d", res.Status)
	//fmt.Printf("----> \n%s\n", res.Data)
	switch res.Status {
	case proxy.ProxyStatus_Session:
		//log.Printf("target: %s", string(res.Data))
		n, err := c.Writer.Write(res.Data)
		if err != nil {
			// log.Printf("error when sending client request to target stream: %v", err)
			return err
		}
		if n != len(res.Data) {
			return fmt.Errorf("received: %d sent: %d loss: %d %w", len(res.Data), n, n/len(res.Data), transport.ErrorPacketLoss)
		}
		//log.Println("rpc server msg forwarded")
	case proxy.ProxyStatus_Accepted:
		c.LocalAddr <- res.Addr
	case proxy.ProxyStatus_EOF:
		return io.EOF
	case proxy.ProxyStatus_Error:
		c.LocalAddr <- ""
		return transport.ErrorServerFailed
	default:
		return fmt.Errorf("unknown status: %d", res.Status)
	}
	return nil
}

func (c *Forwarder) CopySRCtoTarget() error {
	// buffer
	buf := make([]byte, transport.BufferSize)
	for {
		select {
		case <-c.Ctx.Done():
			return nil
		default:
			if err := c.copySRCtoTarget(buf); err != nil {
				return err
			}
		}
	}
}