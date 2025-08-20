package client

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proxy "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"golang.org/x/sync/errgroup"
)

type Statistic struct {
	Tx atomic.Uint64
	Rx atomic.Uint64
}

func (s *Statistic) AddTx(delta uint64) {
	s.Tx.Add(delta)
}

func (s *Statistic) AddRx(delta uint64) {
	s.Rx.Add(delta)
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
		localAddr: make(chan string, 1),
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
	if n <= 0 {
		return transport.ErrInvalidPayload
	}

	//fmt.Printf("<----- packet size: %d\n%s\n", n, buf)
	// send to rpc
	srcData := &proxy.ProxySRC{
		HeaderOrPayload: &proxy.ProxySRC_Payload{
			Payload: buf[:n],
		},
	}

	if err = f.stream.Send(srcData); err != nil {
		return err
	}

	f.addTx(n)
	return nil
	//log.Println("rpc client msg forwarded")
}

func (f *Forwarder) CopyTargetToSRC(ctx context.Context) (err error) {
	errCh := make(chan struct{}, 1)
	go func() {
		for {
			err = f.copyTargetToSRC()
			if err != nil {
				errCh <- struct{}{}
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-errCh:
		return err
	}
}

func (f *Forwarder) copyTargetToSRC() error {
	//log.Println("rpc server reading..")
	buf, err := f.stream.Recv()
	if err != nil {
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
		if len(v.Payload) <= 0 {
			return transport.ErrInvalidPayload
		}

		// data size already aligned with transport.bufferSize, skip copy in trunk
		n, err := f.writer.Write(v.Payload)
		if err != nil {
			// log.Printf("error when sending client request to target stream: %v", err)
			return err
		}

		// data integrity check
		if n <= 0 || n < len(v.Payload) {
			return io.ErrShortWrite
		}

		f.addRx(n)
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
		return transport.ErrServerError
	default:
		return fmt.Errorf("unknown status: %d", buf.Status)
	}
	return nil
}

func (f *Forwarder) CopySRCtoTarget(ctx context.Context) (err error) {
	errCh := make(chan struct{}, 1)
	go func() {
		// buffer
		buf := transport.Buffer()
		defer transport.PutBuffer(buf)
		for {
			err = f.copySRCtoTarget(buf)
			if err != nil {
				errCh <- struct{}{}
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-errCh:
		return err
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
		return fmt.Errorf("rpc: send handshake to server: %s failed: %w", req.Host, err)
	}

	errGroup, ctx := errgroup.WithContext(f.ctx)
	// rpc stream receiver
	errGroup.Go(func() error {
		if err := f.CopyTargetToSRC(ctx); err != nil {
			if err == io.EOF {
				return err
			}
			return fmt.Errorf("copy target to src error: %w", err)
		}
		return nil
	})

	// rpc stream sender
	errGroup.Go(func() error {
		if err := f.CopySRCtoTarget(ctx); err != nil {
			if err == io.EOF {
				return err
			}
			return fmt.Errorf("copy src to target error: %w", err)
		}
		return nil
	})

	// ack timeout
	errGroup.Go(func() error {
		t := time.NewTimer(rpc.GeneralTimeout)
		defer t.Stop()

		select {
		case <-t.C:
			// timed out
			return fmt.Errorf("rpc: server to %s ack timed out: %w", req.Host, os.ErrDeadlineExceeded)
		case localAddr, ok := <-f.localAddr:
			if !ok {
				return fmt.Errorf("rpc: server to %s ack failed", req.Host)
			}
			localAddrChan <- localAddr
			// done
			//log.Printf("rpc: server -> %s -> %s success", req.Host, localAddr)
		}

		return nil
	})

	if err := errGroup.Wait(); err != io.EOF {
		return err
	}
	return nil
}

func (f *Forwarder) addTx(n int) {
	if n <= 0 {
		return // no data to add
	}
	tx := uint64(n)
	f.Statistic.AddTx(tx)
	transport.GlobalStats.AddTx(tx)
}

func (f *Forwarder) addRx(n int) {
	if n <= 0 {
		return // no data to add
	}
	rx := uint64(n)
	f.Statistic.AddRx(rx)
	transport.GlobalStats.AddRx(rx)
}
