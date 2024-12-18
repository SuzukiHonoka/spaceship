package client

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/rpc"
	proxy "github.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto"
	"io"
	"log"
	"os"
	"time"
)

type Forwarder struct {
	ctx       context.Context
	stream    proxy.Proxy_ProxyClient
	writer    io.Writer
	reader    io.Reader
	localAddr chan string
	// statistics
	totalTx uint64
	totalRx uint64
}

func NewForwarder(ctx context.Context, s proxy.Proxy_ProxyClient, w io.Writer, r io.Reader) *Forwarder {
	return &Forwarder{
		ctx:       ctx,
		stream:    s,
		writer:    w,
		reader:    r,
		localAddr: make(chan string),
	}
}

func (f *Forwarder) copySRCtoTarget(buf []byte) error {
	//log.Println("rpc client reading...")
	//read from src
	n, err := f.reader.Read(buf)
	if err != nil {
		return err
	}
	f.totalTx += uint64(n)

	//fmt.Printf("<----- packet size: %d\n%s\n", n, buf)
	// send to rpc
	srcData := &proxy.ProxySRC{
		Data: buf[:n],
	}
	return f.stream.Send(srcData)
	//log.Println("rpc client msg forwarded")
}

func (f *Forwarder) CopyTargetToSRC() (err error) {
	buf := new(proxy.ProxyDST)
	for {
		select {
		case <-f.ctx.Done():
			return nil
		default:
			if err = f.copyTargetToSRC(buf); err != nil {
				return err
			}
		}
	}
}

func (f *Forwarder) copyTargetToSRC(buf *proxy.ProxyDST) error {
	//log.Println("rpc server reading..")
	var err error
	if buf, err = f.stream.Recv(); err != nil {
		return err
	}

	//log.Printf("rpc client on receive: %d", res.Status)
	//fmt.Printf("----> \n%s\n", res.Data)
	switch buf.Status {
	case proxy.ProxyStatus_Session:
		//log.Printf("target: %s", string(res.Data))

		// data size already aligned with transport.bufferSize, skip copy in trunk
		n, err := f.writer.Write(buf.Data)
		if err != nil {
			// log.Printf("error when sending client request to target stream: %v", err)
			return err
		}
		f.totalRx += uint64(n)

		//log.Println("rpc server msg forwarded")
	case proxy.ProxyStatus_Accepted:
		f.localAddr <- buf.Addr
	case proxy.ProxyStatus_EOF:
		return io.EOF
	case proxy.ProxyStatus_Error:
		close(f.localAddr)
		return transport.ErrServerFailed
	default:
		return fmt.Errorf("unknown status: %d", buf.Status)
	}
	return nil
}

func (f *Forwarder) CopySRCtoTarget() (err error) {
	// buffer
	buf := transport.AllocateBuffer()
	for {
		select {
		case <-f.ctx.Done():
			return nil
		default:
			if err = f.copySRCtoTarget(buf); err != nil {
				return err
			}
		}
	}
}

func (f *Forwarder) Start(req *transport.Request, localAddrChan chan<- string) error {
	// handshake and get localAddr first
	handshake := &proxy.ProxySRC{
		Id:   uuid,
		Fqdn: req.Host,
		Port: uint32(req.Port),
	}
	if err := f.stream.Send(handshake); err != nil {
		return fmt.Errorf("rpc: src -> server -> %s handshake failed: %w", req.Host, err)
	}

	// buffered err ch
	proxyErr := make(chan error, 2)

	// rpc stream receiver
	go func() {
		err := f.CopyTargetToSRC()
		if err != nil && err != io.EOF {
			err = fmt.Errorf("rpc: src <- server <- %s: %w", req.Host, err)
		}
		proxyErr <- err
	}()

	// rpc sender
	go func() {
		err := f.CopySRCtoTarget()
		if err != nil && err != io.EOF {
			err = fmt.Errorf("rpc: src -> server -> %s: %w", req.Host, err)
		}
		proxyErr <- err
	}()

	// ack timeout
	t := time.NewTimer(rpc.GeneralTimeout)
	select {
	case <-t.C:
		// timed out
		return fmt.Errorf("rpc: server -> %s ack timed out: %w", req.Host, os.ErrDeadlineExceeded)
	case localAddr, ok := <-f.localAddr:
		if !ok {
			return fmt.Errorf("rpc: server -> %s ack failed", req.Host)
		}
		localAddrChan <- localAddr
		t.Stop()
		// done
		//log.Printf("rpc: server -> %s -> %s success", req.Host, localAddr)
	}

	err := <-proxyErr

	log.Printf("session: %s: %d bytes sent, %d bytes received", req.Host, f.totalTx, f.totalRx)
	return err
}
