package client

import (
	"context"
	"crypto/x509"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc"
	proxy "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

const TransportName = "proxy"

var (
	uuid      string
	connQueue *ConnQueue
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
				return fmt.Errorf("addtional ca not found while system cert pool can not be copied: %w", err)
			}
			log.Println("copy system cert pool failed, creating new empty pool")
			pool = x509.NewCertPool()
		}

		// load custom cas if exist
		if err = loadCertificateAuthorities(pool, cas); err != nil {
			return fmt.Errorf("load custom ca failed: %w", err)
		}

		// error handling omitted
		credential = credentials.NewClientTLSFromCert(pool, hostName)
	} else {
		credential = insecure.NewCredentials()
	}
	params := NewParams(server, append(rpc.DialOptions, grpc.WithTransportCredentials(credential))...)
	connQueue = NewConnQueue(int(mux), params)
	return connQueue.Init()
}

func loadCertificateAuthorities(pool *x509.CertPool, cas []string) error {
	for _, path := range cas {
		// Clean the path to remove any directory traversal attempts
		cleanPath := filepath.Clean(path)

		// Read the CA file
		cert, err := os.ReadFile(cleanPath)
		if err != nil {
			return err
		}

		if !pool.AppendCertsFromPEM(cert) {
			return fmt.Errorf("failed to append %s to CA certificates", cleanPath)
		}
	}
	return nil
}

func Destroy() {
	// double check since credential errors might occur
	if connQueue != nil {
		connQueue.Destroy()
	}
}

func (c *Client) Dial(_, _ string) (net.Conn, error) {
	return nil, fmt.Errorf("%s: dial not implemented", c.String())
}

func New() (transport.Transport, error) {
	client, doneFunc, err := connQueue.GetClient()
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

func (c *Client) Proxy(ctx context.Context, req *transport.Request, localAddr chan<- string, w io.Writer, r io.Reader) error {
	defer close(localAddr)

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// rcp client stream
	stream, err := c.ProxyClient.Proxy(sessionCtx)
	if err != nil {
		return err
	}

	start := time.Now()

	//log.Printf("sending proxy to rpc: %s", req.Host)
	f := NewForwarder(sessionCtx, stream, w, r)
	if err = f.Start(req, localAddr); err != nil {
		return fmt.Errorf("rpc client: proxy failed: %w", err)
	}

	log.Printf("session: %s duration %v, %s sent, %s received",
		req.Host, time.Since(start).Round(time.Millisecond),
		utils.PrettyByteSize(float64(f.Statistic.Tx.Load())), utils.PrettyByteSize(float64(f.Statistic.Rx.Load())))
	return nil
}
