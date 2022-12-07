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

var (
	UUID     string
	ConnPool *Pool
)

type Client struct {
	proxy.ProxyClient
	DoneFunc func() error
}

func Init(server, hostName string, tls bool, mux uint8, cas []string) error {
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
	ConnPool = NewPool(int(mux), server, append(rpc.DialOptions, grpc.WithTransportCredentials(credential))...)
	err := ConnPool.Init()
	return err
}

func NewClient() *Client {
	client, doneFunc, err := ConnPool.GetClient()
	if err != nil {
		panic(err)
	}
	return &Client{ProxyClient: client, DoneFunc: doneFunc}
}

func (c *Client) Close() error {
	return c.DoneFunc()
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
	forwardError := make(chan error)
	f := NewForwarder(ctx, stream, w, r, watcher)
	// rpc stream receiver
	go func() {
		err := f.CopyTargetToSRC()
		forwardError <- fmt.Errorf("rpc: src <- server -> %s: %w", req.Fqdn, err)
	}()
	// rpc sender
	go func() {
		err := f.CopySRCtoTarget()
		forwardError <- fmt.Errorf("rpc: src -> server -> %s: %w", req.Fqdn, err)
	}()
	t := time.NewTimer(rpc.GeneralTimeout)
	select {
	case <-t.C:
		//timed out
		localAddr <- ""
		return fmt.Errorf("rpc: server -> %s timed out: %w", req.Fqdn, os.ErrDeadlineExceeded)
	case localAddr <- <-watcher:
		t.Stop()
		// done
		//log.Printf("rpc: server -> %s success", req.Fqdn)
	}
	// block main
	select {
	case err = <-forwardError:
		return err
	case <-sessionCtx.Done():
	case <-ctx.Done():
	}
	//log.Println("client done")
	return nil
}
