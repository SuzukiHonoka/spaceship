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
	proxy "spaceship/internal/transport/rpc/proto"
)

type Client struct {
	proxy.ProxyClient
}

var ClientConfig *client.Client

func NewClient() *Client {
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
		return nil
	}
	//defer conn.Close()
	return &Client{proxy.NewProxyClient(conn)}
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
	counter := 0

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
	go func(localAddr chan string) {
		for {
			select {
			case <-cancel:
				log.Println("rpc client finished")
				return
			default:
				//log.Println("rcp client reading..")
				// Get response and possible error message from the stream
				res, err := stream.Recv()
				// Break for loop if there are no more response messages
				if err == io.EOF {
					cancel <- struct{}{}
					return
				}
				// Handle a possible error
				if err != nil {
					log.Printf("Error when receiving rpc response: %v", err)
					cancel <- struct{}{}
					return
				}
				//log.Printf("rpc client on receive: %d\n", res.Status)
				switch res.Status {
				case proxy.ProxyStatus_EOF:
					cancel <- struct{}{}
					return
				case proxy.ProxyStatus_Error:
					localAddr <- ""
					cancel <- struct{}{}
					return
				case proxy.ProxyStatus_Accepted:
					localAddr <- res.Addr
				case proxy.ProxyStatus_Session:
					//log.Printf("target: %s", string(res.Data))
					n, err := dst.Write(res.Data)
					if err != nil || n != len(res.Data) {
						log.Printf("send to dst failed: %v\n", err)
						cancel <- struct{}{}
					}
					//log.Println("dst sent")
				}
				counter++
			}
		}
	}(localAddr)

	// rpc sender
	go func(cancel chan struct{}) {
		// buffer
		buf := make([]byte, 4*1024)
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
				counter++
			}
		}

	}(cancel)
	// block main
	<-cancel
	return nil
}
