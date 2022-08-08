package rpc

import (
	"context"
	"crypto/x509"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"log"
	"spaceship/internal/config"
	"spaceship/internal/transport"
	proxy "spaceship/internal/transport/rpc/proto"
)

var ClientPool *Pool

type Client struct {
	proxy.ProxyClient
}

func PoolInit() error {
	var credential credentials.TransportCredentials
	if config.LoadedConfig.TLS {
		pool, err := x509.SystemCertPool()
		if err != nil {
			panic(err)
		}
		// error handling omitted
		credential = credentials.NewClientTLSFromCert(pool, config.LoadedConfig.Host)
	} else {
		credential = insecure.NewCredentials()
	}
	ClientPool = NewPool(int(config.LoadedConfig.Mux))
	err := ClientPool.FullInit(config.LoadedConfig.ServerAddr, grpc.WithTransportCredentials(credential))
	return err
}

func NewClient() *Client {
	//defer conn.Close()
	client, err := ClientPool.GetClient()
	if err != nil {
		panic(err)
	}
	return &Client{ProxyClient: client}
}

type clientForwarder struct {
	Stream    proxy.Proxy_ProxyClient
	Writer    io.Writer
	Reader    io.Reader
	LocalAddr chan<- string
}

func (c *Client) Close() error {
	return nil
}

func (c *clientForwarder) CopySRCtoTarget() error {
	// buffer
	buf := make([]byte, transport.BufferSize)
	for {
		//log.Println("rpc client sending...")
		//read from src
		n, err := c.Reader.Read(buf)
		if err != nil {
			return err
		}
		//fmt.Printf("<----- \n%s\n", buf)
		// send to rpc
		srcData := &proxy.ProxySRC{
			Data: buf[:n],
		}
		err = c.Stream.Send(srcData)
		srcData = nil
		if err != nil {
			return err
		}
		//log.Println("rpc client msg forwarded")
	}
}

func (c *clientForwarder) CopyTargetToSRC() error {
	for {
		//log.Println("rcp client reading..")
		res, err := c.Stream.Recv()
		if err != nil {
			return err
		}
		//log.Printf("rpc client on receive: %d", res.Status)
		//fmt.Printf("----> \n%s\n", res.Data)
		switch res.Status {
		case proxy.ProxyStatus_EOF:
			return nil
		case proxy.ProxyStatus_Error:
			c.LocalAddr <- ""
			return transport.ErrorServerFailed
		case proxy.ProxyStatus_Accepted:
			c.LocalAddr <- res.Addr
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
			//log.Println("dst sent")
		}
	}
}

func (c *Client) Proxy(ctx context.Context, localAddr chan<- string, w io.Writer, r io.Reader) error {
	req, ok := ctx.Value("request").(*transport.Request)
	if !ok {
		return transport.ErrorRequestNotFound
	}
	// rcp client
	stream, err := c.ProxyClient.Proxy(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	log.Printf("sending proxy to rpc: %s", req.Fqdn)
	// get local addr first
	err = stream.Send(&proxy.ProxySRC{
		Id:   config.LoadedConfig.UUID,
		Fqdn: req.Fqdn,
		Port: uint32(req.Port),
	})
	if err != nil {
		cancel()
		return err
	}
	f := &clientForwarder{
		Stream:    stream,
		Writer:    w,
		Reader:    r,
		LocalAddr: localAddr,
	}
	// rpc stream receiver
	go func() {
		err := f.CopyTargetToSRC()
		transport.PrintErrorIfNotCritical(err, "error occurred while proxying")
		cancel()
	}()
	// rpc sender
	go func() {
		err := f.CopySRCtoTarget()
		transport.PrintErrorIfNotCritical(err, "error occurred while proxying")
		cancel()
	}()
	// block main
	<-ctx.Done()
	_ = stream.CloseSend()
	return nil
}
