package rpc

import (
	"context"
	"crypto/x509"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"log"
	"spaceship/internal/config/client"
	"spaceship/internal/transport"
	proxy "spaceship/internal/transport/rpc/proto"
)

type Client struct {
	proxy.ProxyClient
}

var ClientConfig *client.Client

func NewClient() (*grpc.ClientConn, *Client) {
	if ClientConfig == nil {
		panic("server not configured yet")
	}
	var credential credentials.TransportCredentials
	if ClientConfig.TLS {
		pool, _ := x509.SystemCertPool()
		// error handling omitted
		credential = credentials.NewClientTLSFromCert(pool, "")
	} else {
		credential = insecure.NewCredentials()
	}
	conn, err := grpc.Dial(ClientConfig.ServerAddr, grpc.WithTransportCredentials(credential))
	if err != nil {
		log.Printf("Error connect to server failed: %v", err)
		return nil, nil
	}
	//defer conn.Close()
	return conn, &Client{proxy.NewProxyClient(conn)}
}

func (c *Client) Proxy(localAddr chan string, dst io.Writer, src io.Reader, fqdn string, port int) error {
	// new background work
	ctx := context.Background()
	// rcp client
	stream, err := c.ProxyClient.Proxy(ctx)
	if err != nil {
		return err
	}
	log.Println("sending proxy to rpc:", fqdn)
	// chan for cancel
	cancel := make(chan struct{})

	// get local addr first
	err = stream.Send(&proxy.ProxySRC{
		Uuid: ClientConfig.UUID,
		Fqdn: fqdn,
		Port: uint32(port),
	})

	if err != nil {
		log.Printf("send to dst failed: %v", err)
		cancel <- struct{}{}
		return err
	}
	// rpc stream receiver
	go func() {
		var res *proxy.ProxyDST
		var errRecv error
		for {
			select {
			case <-cancel:
				//log.Println("rpc client finished")
				return
			default:
				//log.Println("rcp client reading..")
				// Get response and possible error message from the stream
				res, errRecv = stream.Recv()
				// Break for loop if there are no more response messages
				if errRecv == io.EOF {
					res = nil
					cancel <- struct{}{}
					return
				}
				// Handle a possible error
				if errRecv != nil {
					res = nil
					cancel <- struct{}{}
					log.Printf("Error when receiving rpc response: %v", errRecv)
					return
				}
				//log.Printf("rpc client on receive: %d\n", res.Status)
				switch res.Status {
				case proxy.ProxyStatus_EOF:
					res = nil
					cancel <- struct{}{}
					return
				case proxy.ProxyStatus_Error:
					localAddr <- ""
					res = nil
					cancel <- struct{}{}
					return
				case proxy.ProxyStatus_Accepted:
					localAddr <- res.Addr
				case proxy.ProxyStatus_Session:
					//log.Printf("target: %s", string(res.Data))
					n, errRecv := dst.Write(res.Data)
					if errRecv != nil || n != len(res.Data) {
						cancel <- struct{}{}
						res = nil
						log.Printf("send to dst failed: %v\n", errRecv)
					}
					//log.Println("dst sent")
				}
			}
		}
	}()

	// rpc sender
	go func() {
		// buffer
		buf := make([]byte, transport.BufferSize)
		for {
			select {
			case <-cancel:
				return
			default:
				//log.Println("rpc client sending...")
				//read from src
				n, err := src.Read(buf)
				if err == io.EOF {
					buf = nil
					cancel <- struct{}{}
					return
				}
				if err != nil {
					buf = nil
					cancel <- struct{}{}
					log.Printf("Error when receiving response: %v", err)
					return
				}
				//log.Printf("TX: %s\n", string(buf[:n]))
				// send to rpc
				err = stream.Send(&proxy.ProxySRC{
					Uuid: ClientConfig.UUID,
					Data: buf[:n],
				})
				if err != nil {
					buf = nil
					cancel <- struct{}{}
					log.Printf("send to dst failed: %v", err)
					return
				}
				//log.Println("rpc client msg forwarded")
			}
		}

	}()
	// block main
	<-cancel
	_ = stream.CloseSend()
	return nil
}
