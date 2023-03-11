package client

import (
	"context"
	"crypto/x509"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc"
	proxy "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"log"
	"os"
	"time"
)

const TransportName = "rpc"

var (
	uuid     string
	connPool *Pool
)

type Client struct {
	proxy.ProxyClient
	DoneFunc func() error
}

func SetUUID(uid string) {
	uuid = uid
}

func Init(server, hostName string, tls bool, mux uint8, cas []string) error {
	var credential credentials.TransportCredentials
	if tls {
		pool, err := x509.SystemCertPool()
		if err != nil {
			if len(cas) == 0 {
				return fmt.Errorf("at least one addtional ca not found since the system cert pool can not be copied: %w", err)
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
			if !pool.AppendCertsFromPEM(cert) {
				log.Printf("CA: [%s] add to cert pool failed", path)
			}
		}
		// error handling omitted
		credential = credentials.NewClientTLSFromCert(pool, hostName)
	} else {
		credential = insecure.NewCredentials()
	}
	connPool = NewPool(int(mux), server, append(rpc.DialOptions, grpc.WithTransportCredentials(credential))...)
	err := connPool.Init()
	return err
}

func Destroy() {
	connPool.Destroy()
}

func NewClient() (*Client, error) {
	client, doneFunc, err := connPool.GetClient()
	if err != nil {
		return nil, err
	}
	return &Client{ProxyClient: client, DoneFunc: doneFunc}, nil
}

func (c *Client) String() string {
	return TransportName
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
		Id:   uuid,
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
