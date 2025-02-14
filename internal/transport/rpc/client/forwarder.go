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
	"sync/atomic"
	"time"
)

type Statistic struct {
	Tx uint64
	Rx uint64
}

func (s Statistic) AddTx(delta uint64) {
	atomic.AddUint64(&s.Tx, delta)
}

func (s Statistic) AddRx(delta uint64) {
	atomic.AddUint64(&s.Rx, delta)
}

type Forwarder struct {
	ctx       context.Context
	stream    proxy.Proxy_ProxyClient
	writer    io.Writer
	reader    io.Reader
	localAddr chan string

	// Statistic for TX and RX
	Statistic *Statistic
}

func NewForwarder(ctx context.Context, s proxy.Proxy_ProxyClient, w io.Writer, r io.Reader) *Forwarder {
	return &Forwarder{
		ctx:       ctx,
		stream:    s,
		writer:    w,
		reader:    r,
		localAddr: make(chan string),
		Statistic: new(Statistic),
	}
}

func (f *Forwarder) copySRCtoTarget(buf []byte) error {
	//log.Println("rpc client reading...")
	//read from src
	n, err := f.reader.Read(buf)
	if err != nil {
		return err
	}
	f.Statistic.AddTx(uint64(n))

	//fmt.Printf("<----- packet size: %d\n%s\n", n, buf)
	// send to rpc
	srcData := &proxy.ProxySRC{
		HeaderOrPayload: &proxy.ProxySRC_Payload{
			Payload: buf[:n],
		},
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
				if err == io.EOF {
					return nil
				}
				return err
			}
		}
	}
}

func (f *Forwarder) copyTargetToSRC(buf *proxy.ProxyDST) (err error) {
	//log.Println("rpc server reading..")
	if buf, err = f.stream.Recv(); err != nil {
		return err
	}

	//log.Printf("rpc client on receive: %d", res.Status)
	//fmt.Printf("----> \n%s\n", res.Data)
	switch buf.Status {
	case proxy.ProxyStatus_Session:
		//log.Printf("target: %s", string(res.Data))
		v, ok := buf.HeaderOrPayload.(*proxy.ProxyDST_Payload)
		if !ok {
			return transport.ErrInvalidMessage
		}

		// data size already aligned with transport.bufferSize, skip copy in trunk
		n, err := f.writer.Write(v.Payload)
		if err != nil {
			// log.Printf("error when sending client request to target stream: %v", err)
			return err
		}
		f.Statistic.Rx += uint64(n)

		//log.Println("rpc server msg forwarded")
	case proxy.ProxyStatus_Accepted:
		v, ok := buf.HeaderOrPayload.(*proxy.ProxyDST_Header)
		if !ok {
			return transport.ErrInvalidMessage
		}

		f.localAddr <- v.Header.Addr
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
				if err == io.EOF {
					return nil
				}
				return err
			}
		}
	}
}

func (f *Forwarder) Start(req *transport.Request, localAddrChan chan<- string) error {
	// handshake and get localAddr first
	handshake := &proxy.ProxySRC{
		HeaderOrPayload: &proxy.ProxySRC_Header{
			Header: &proxy.ProxySRC_ProxyHeader{
				Id:   uuid,
				Fqdn: req.Host,
				Port: uint32(req.Port),
			},
		},
	}
	if err := f.stream.Send(handshake); err != nil {
		return fmt.Errorf("rpc: src -> server -> %s handshake failed: %w", req.Host, err)
	}

	// buffered err ch
	proxyErr := make(chan error, 2)

	// rpc stream receiver
	go func() {
		err := f.CopyTargetToSRC()
		if err != nil {
			err = fmt.Errorf("rpc: src <- server <- %s: %w", req.Host, err)
		}
		proxyErr <- err
	}()

	// rpc sender
	go func() {
		err := f.CopySRCtoTarget()
		if err != nil {
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

	log.Printf("session: %s: %d bytes sent, %d bytes received", req.Host, f.Statistic.Tx, f.Statistic.Rx)
	return err
}
