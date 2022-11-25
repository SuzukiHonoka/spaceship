package client

import (
	"context"
	"crypto/x509"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"log"
	"os"
	"spaceship/internal/transport"
	"spaceship/internal/transport/rpc"
	proxy "spaceship/internal/transport/rpc/proto"
	"time"
)

var UUID string

var ConnPool *Pool

type Client struct {
	proxy.ProxyClient
}

func PoolInit(server, hostName string, tls bool, mux uint8, cas []string) error {
	var credential credentials.TransportCredentials
	if tls {
		pool, err := x509.SystemCertPool()
		if err != nil {
			if len(cas) == 0 {
				log.Fatalf("You have to add at least a CA since the system cert pool can not be copied: %v", err)
			}
			log.Println("copy system cert pool failed, creating new empty pool")
			pool = x509.NewCertPool()
		}
		// load custom cas if exist
		for _, path := range cas {
			cert, err := os.ReadFile(path)
			if err != nil {
				log.Printf("read CA file from path: %s failed: %v", path, err)
				continue
			}
			if pool.AppendCertsFromPEM(cert) {
				log.Printf("CA: [%s] added to cert pool", path)
			} else {
				log.Printf("CA: [%s] add to cert pool failed", path)
			}
		}
		// error handling omitted
		credential = credentials.NewClientTLSFromCert(pool, hostName)
	} else {
		credential = insecure.NewCredentials()
	}
	ConnPool = NewPool(int(mux))
	err := ConnPool.FullInit(server, append(rpc.DialOptions, grpc.WithTransportCredentials(credential))...)
	return err
}

func NewClient() *Client {
	//defer conn.Close()
	client, err := ConnPool.GetClient()
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
		srcData = nil
		if err != nil {
			return err
		}
		//log.Println("rpc client msg forwarded")
	}
}

func (c *clientForwarder) CopyTargetToSRC() error {
	for {
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
			return nil
		case proxy.ProxyStatus_Error:
			c.LocalAddr <- ""
			return transport.ErrorServerFailed
		}
	}
}

func (c *Client) Proxy(ctx context.Context, localAddr chan<- string, w io.Writer, r io.Reader) error {
	defer close(localAddr)
	defer func() {
		_ = c.Close()
	}()
	req, ok := ctx.Value("request").(*transport.Request)
	if !ok {
		localAddr <- ""
		return transport.ErrorRequestNotFound
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// rcp client
	stream, err := c.ProxyClient.Proxy(sessionCtx)
	if err != nil {
		localAddr <- ""
		return err
	}
	//log.Printf("sending proxy to rpc: %s", req.Fqdn)
	// get local addr first
	err = stream.Send(&proxy.ProxySRC{
		Id:   UUID,
		Fqdn: req.Fqdn,
		Port: uint32(req.Port),
	})
	if err != nil {
		localAddr <- ""
		return err
	}
	watcher := make(chan string)
	defer close(watcher)
	f := &clientForwarder{
		Stream:    stream,
		Writer:    w,
		Reader:    r,
		LocalAddr: watcher,
	}
	// rpc stream receiver
	go func() {
		err := f.CopyTargetToSRC()
		transport.PrintErrorIfNotCritical(err, fmt.Sprintf("rpc: src <- server -> %s", req.Fqdn))
		cancel()
	}()
	// rpc sender
	go func() {
		err := f.CopySRCtoTarget()
		transport.PrintErrorIfNotCritical(err, fmt.Sprintf("rpc: src -> server -> %s", req.Fqdn))
		cancel()
	}()
	select {
	case <-time.After(3 * time.Second):
		//timed out
		localAddr <- ""
		log.Printf("rpc: server -> %s timed out", req.Fqdn)
		return nil
	case localAddr <- <-watcher:
		// done
		//log.Printf("rpc: server -> %s success", req.Fqdn)
	}
	// block main
	select {
	case <-sessionCtx.Done():
	case <-ctx.Done():
	}
	//log.Println("client done")
	return nil
}
