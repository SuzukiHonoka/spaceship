package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	rpcutils "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/utils"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"github.com/miekg/dns"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

const TransportName = "rpc"

var (
	uuid      string
	connQueue *ConnQueue
)

type Client struct {
	proto.ProxyClient
	DoneFunc func() error
}

func SetUUID(uid string) {
	uuid = uid
}

func setupGrpcCredential(tls bool, hostName string, customCA ...string) (credentials.TransportCredentials, error) {
	if !tls {
		return insecure.NewCredentials(), nil
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		if len(customCA) == 0 {
			return nil, fmt.Errorf("addtional ca not found while system cert pool can not be copied: %w", err)
		}
		log.Println("copy system cert pool failed, creating new empty pool")
		pool = x509.NewCertPool()
	}

	// load custom cas if exist
	if err = loadCertificateAuthorities(pool, customCA); err != nil {
		return nil, fmt.Errorf("load custom ca failed: %w", err)
	}

	tlsConfig, err := buildClientTLSConfig(pool, hostName)
	if err != nil {
		return nil, fmt.Errorf("build client tls config failed: %w", err)
	}
	return credentials.NewTLS(tlsConfig), nil
}

func loadCertificateAuthorities(pool *x509.CertPool, customCAList []string) error {
	for _, path := range customCAList {
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

func buildClientTLSConfig(cp *x509.CertPool, serverNameOverride string) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		ServerName:         serverNameOverride,
		RootCAs:            cp,
		ClientSessionCache: tls.NewLRUClientSessionCache(128),
		MinVersion:         tls.VersionTLS13,
		MaxVersion:         tls.VersionTLS13,
		CurvePreferences:   rpc.DefaultCurvePreferences,
		CipherSuites:       []uint16{tls.TLS_AES_128_GCM_SHA256},
	}

	tls.CipherSuites()

	return tlsConfig, nil
}

func Init(server, hostName string, tls bool, mux uint8, cas []string) error {
	credential, err := setupGrpcCredential(tls, hostName, cas...)
	if err != nil {
		return fmt.Errorf("setup grpc credential failed: %w", err)
	}

	params := NewParams(server, append(rpc.DialOptions, grpc.WithTransportCredentials(credential),
		grpc.WithIdleTimeout(transport.IdleTimeout))...)
	connQueue = NewConnQueue(int(mux), params)
	return connQueue.Init()
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

func New() (*Client, error) {
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

func (c *Client) Proxy(ctx context.Context, addr string, localAddr chan<- string, w io.Writer, r io.Reader) error {
	defer close(localAddr)

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// rcp client stream
	stream, err := c.ProxyClient.Proxy(sessionCtx)
	if err != nil {
		return err
	}

	start := time.Now()

	//log.Printf("sending proto to rpc: %s", req.Host)
	f := NewForwarder(sessionCtx, stream, w, r)
	if err = f.Start(addr, localAddr); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("rpc client: proto failed: %w", err)
	}

	log.Printf("session: %s duration %v, %s sent, %s received",
		addr, time.Since(start).Round(time.Millisecond),
		utils.PrettyByteSize(float64(f.Statistic.Tx.Load())), utils.PrettyByteSize(float64(f.Statistic.Rx.Load())))
	return nil
}

type DnsRequest struct {
	Fqdn  string
	QType uint16
}

func (c *Client) DnsResolve(ctx context.Context, requests []*DnsRequest) ([]dns.RR, error) {
	if len(requests) == 0 {
		return nil, nil
	}

	// Create gRPC request
	req := &proto.DnsRequest{
		Id: uuid, // Assuming uuid is available from existing client
	}

	for _, request := range requests {
		dnsReq := &proto.DnsRequestItem{
			Fqdn:  request.Fqdn,
			QType: uint32(request.QType),
		}
		req.Items = append(req.Items, dnsReq)
	}

	// Make gRPC call
	resp, err := c.ProxyClient.DnsResolve(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC DNS resolve failed: %w", err)
	}

	// Process results
	results := make([]dns.RR, 0, len(resp.Result))

	for _, item := range resp.Result {
		// Convert protobuf records back to DNS RR records using the new format
		if len(item.Records) == 0 {
			log.Printf("dns: no records found for %s", item.Fqdn)
			continue
		}

		// Use new complete record format
		records, err := rpcutils.ConvertProtoToRRSlice(item.Records)
		if err != nil {
			log.Printf("dns: failed to convert proto records for %s: %v", item.Fqdn, err)
			continue
		}
		log.Printf("dns: resolved %s with %d records", item.Fqdn, len(records))

		results = append(results, records...)
	}

	return results, nil
}
