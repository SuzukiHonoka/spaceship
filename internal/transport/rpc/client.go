package rpc

import (
	"context"
	"crypto/x509"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"io"
	"net"
	"spaceship/internal/config"
	"spaceship/internal/transport"
	proxy "spaceship/internal/transport/rpc/proto"
	"time"
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
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(credential),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    10 * time.Second,
			Timeout: 5 * time.Second,
		}),
		grpc.WithConnectParams(grpc.ConnectParams{
			//value of first 3 fields is from backoff.DefaultConfig
			Backoff: backoff.Config{
				BaseDelay:  1.0 * time.Second,
				Multiplier: 1.6,
				Jitter:     0.2,
				MaxDelay:   5 * time.Second,
			},
			MinConnectTimeout: 5 * time.Second,
		}),
		grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
			return (&net.Dialer{
				Timeout: 5 * time.Second,
			}).DialContext(ctx, "tcp", s)
		}),
		grpc.WithUserAgent("spaceship/" + config.VersionCode),
	}
	err := ClientPool.FullInit(config.LoadedConfig.ServerAddr, opts...)
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
		case proxy.ProxyStatus_Accepted:
			c.LocalAddr <- res.Addr
		case proxy.ProxyStatus_EOF:
			return nil
		case proxy.ProxyStatus_Error:
			c.LocalAddr <- ""
			return transport.ErrorServerFailed
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
	//log.Printf("sending proxy to rpc: %s", req.Fqdn)
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
		transport.PrintErrorIfNotCritical(err, "error occurred while src <- target "+req.Fqdn)
		cancel()
	}()
	// rpc sender
	go func() {
		err := f.CopySRCtoTarget()
		transport.PrintErrorIfNotCritical(err, "error occurred while src -> target "+req.Fqdn)
		cancel()
	}()
	// block main
	<-ctx.Done()
	_ = stream.CloseSend()
	return nil
}
