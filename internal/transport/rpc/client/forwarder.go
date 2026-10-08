package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proxy "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"golang.org/x/sync/errgroup"
)

// Sentinel errors for the TCP proxy handshake path. Outer layers (HTTP/SOCKS)
// attach the target host when logging so messages stay one short line.
var (
	errServerAckTimeout = errors.New("server ack timeout")
	errServerRejected   = errors.New("server rejected connection")
	errTunnelLost       = errors.New("tunnel lost")
)

type Statistic struct {
	Tx atomic.Uint64
	Rx atomic.Uint64
}

func (s *Statistic) AddTx(delta uint64) {
	if delta > 0 {
		s.Tx.Add(delta)
	}
}

func (s *Statistic) AddRx(delta uint64) {
	if delta > 0 {
		s.Rx.Add(delta)
	}
}

// contextWriter is a writer whose wait before writing can be cut short, such
// as a transport.ReplyGate that withholds bytes until the front end's reply.
// Cancelling the RPC stream alone cannot unblock a write waiting on it.
type contextWriter interface {
	WriteContext(ctx context.Context, p []byte) (int, error)
}

type Forwarder struct {
	ctx context.Context
	// cancel aborts the underlying stream. Used to unblock a handshake Send that
	// has exceeded its deadline.
	cancel        context.CancelFunc
	stream        proxy.Proxy_ProxyClient
	writer        io.Writer
	reader        io.Reader
	localAddr     chan string
	closeAddrOnce sync.Once
	// accepted is set once the server acknowledges the session, after which
	// the client connection carries tunnelled bytes.
	accepted atomic.Bool
	// abortCause is the response-side failure that made the forwarder reset
	// the client connection. Start reports it in preference to the upload
	// error that the reset itself provokes. Written by the download goroutine
	// before it returns; read after the errgroup has finished.
	abortCause error

	// Statistic for TX and RX
	Statistic *Statistic
}

func NewForwarder(ctx context.Context, cancel context.CancelFunc, s proxy.Proxy_ProxyClient, w io.Writer, r io.Reader) *Forwarder {
	if cancel == nil {
		cancel = func() {}
	}
	return &Forwarder{
		ctx:       ctx,
		cancel:    cancel,
		stream:    s,
		writer:    w,
		reader:    r,
		localAddr: make(chan string, 1),
		Statistic: new(Statistic),
	}
}

func (f *Forwarder) copySRCtoTarget(srcData *proxy.ProxySRC, payload *proxy.ProxySRC_Payload) error {
	pool := rpc.BufferPool()
	buf, read := rpc.AcquirePayloadBuffer(pool, transport.GetBufferSize())
	n, err := f.reader.Read(read)
	if n <= 0 {
		pool.Put(buf)
		if err != nil {
			return err
		}
		return transport.ErrInvalidPayload
	}
	payload.Payload = read[:n]
	rpc.OfferPayloadBuffer(srcData, buf, n, pool)
	if sendErr := f.stream.Send(srcData); sendErr != nil {
		rpc.DiscardPayloadBuffer(srcData)
		payload.Payload = nil
		return sendErr
	}
	payload.Payload = nil

	f.addTx(n)
	if err != nil {
		return err
	}
	return nil
}

func (f *Forwarder) CopyTargetToSRC(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		// Reuse one message for the life of the stream. The proxyCodec hot path
		// retains a refcounted view into the receive buffer so steady-state
		// streaming does not allocate/copy per chunk.
		dstData := &proxy.ProxyDST{
			HeaderOrPayload: &proxy.ProxyDST_Payload{},
		}
		rpc.RetainPayloadViews(dstData)
		defer rpc.ReleaseMessageBuffers(dstData)
		for {
			if err := f.stream.RecvMsg(dstData); err != nil {
				switch {
				case errors.Is(err, io.EOF):
					// The server completed the RPC: an orderly end.
					errCh <- io.EOF
				case f.accepted.Load():
					// The tunnel broke (a reset or timed-out transport surfaces
					// as Unavailable) after the session started and before the
					// server ended it, so the client's response is incomplete.
					errCh <- fmt.Errorf("%w: %w", errTunnelLost, err)
				default:
					// Refused before the session started (authentication,
					// admission): report the server's reason as it is.
					errCh <- err
				}
				return
			}
			if readErr := f.copyTargetToSRC(ctx, dstData); readErr != nil {
				errCh <- readErr
				return
			}
		}
	}()

	select {
	case err := <-errCh:
		// Only the server's EOF status (or a completed RPC) proves the response
		// is whole. Anything else, other than our own cancellation, leaves the
		// client holding a truncated stream, so reset it instead of letting the
		// caller close it in an orderly way.
		// Before the server accepts the session the front end still owns the
		// connection and must be able to write its own failure reply.
		if f.accepted.Load() && !errors.Is(err, io.EOF) && ctx.Err() == nil {
			f.abortCause = err
			transport.Abort(f.writer)
		}
		return err
	case <-ctx.Done():
		// Abort the stream so the receive goroutine unblocks from RecvMsg, then
		// wait for it to exit. Returning early would leave it writing to the
		// caller's io.Writer after Proxy has already returned, at which point the
		// caller is entitled to treat the session as finished and reuse or close
		// that writer.
		f.cancel()
		<-errCh
		return ctx.Err()
	}
}

func (f *Forwarder) copyTargetToSRC(ctx context.Context, buf *proxy.ProxyDST) error {
	//log.Println("rpc server reading...")
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
		var n int
		var err error
		if cw, ok := f.writer.(contextWriter); ok {
			// Use the errgroup's copy context, including upload failures that
			// cancel it before the handshake address reaches the front end.
			n, err = cw.WriteContext(ctx, v.Payload)
		} else {
			n, err = f.writer.Write(v.Payload)
		}
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

		f.accepted.Store(true)
		f.localAddr <- v.Header.Addr
	case proxy.ProxyStatus_EOF:
		return io.EOF
	case proxy.ProxyStatus_Error:
		f.closeAddrOnce.Do(func() { close(f.localAddr) })
		return transport.ErrServerError
	default:
		return fmt.Errorf("unknown status: %d", buf.Status)
	}
	return nil
}

// CopySRCtoTarget pumps the caller's reader into the stream.
//
// Unlike the receive direction, a pending Read on a caller-owned io.Reader
// cannot be interrupted from here — only the caller closing it will unblock the
// goroutine. So on cancellation this returns while that read is still
// outstanding, and the caller must keep the reader valid until it closes it.
// Every caller satisfies this today by deferring Close on the connection it
// passed in.
func (f *Forwarder) CopySRCtoTarget(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		srcData := &proxy.ProxySRC{
			HeaderOrPayload: &proxy.ProxySRC_Payload{
				Payload: nil,
			},
		}
		payload := srcData.HeaderOrPayload.(*proxy.ProxySRC_Payload)

		defer rpc.ReleaseMessageBuffers(srcData)

		for {
			if err := f.copySRCtoTarget(srcData, payload); err != nil {
				errCh <- err
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func (f *Forwarder) Start(addr string, localAddrChan chan<- string) error {
	// handshake: send target address (auth is handled by interceptor metadata)
	handshake := &proxy.ProxySRC{
		HeaderOrPayload: &proxy.ProxySRC_Header{
			Header: &proxy.ProxySRC_ProxyHeader{
				Addr: addr,
			},
		},
	}
	if err := sendHandshake(f.stream, handshake, f.cancel, rpc.GeneralTimeout, addr); err != nil {
		return fmt.Errorf("handshake to %s: %w", addr, err)
	}

	errGroup, ctx := errgroup.WithContext(f.ctx)
	// rpc stream receiver
	errGroup.Go(func() error {
		if err := f.CopyTargetToSRC(ctx); err != nil {
			if err == io.EOF {
				return err
			}
			return fmt.Errorf("download: %w", err)
		}
		return nil
	})

	// rpc stream sender
	errGroup.Go(func() error {
		if err := f.CopySRCtoTarget(ctx); err != nil {
			if err == io.EOF {
				return err
			}
			return fmt.Errorf("upload: %w", err)
		}
		return nil
	})

	// ack timeout
	errGroup.Go(func() error {
		t := time.NewTimer(rpc.GeneralTimeout)
		defer t.Stop()

		select {
		case <-ctx.Done():
			// A sibling goroutine already failed the session — an auth rejection,
			// an unreachable server, a refused target. Without this case the ack
			// timer would still run to completion, so every fast failure would
			// cost the caller the full timeout before it saw the real error.
			return ctx.Err()
		case <-t.C:
			// Do not wrap os.ErrDeadlineExceeded: its Error() is "i/o timeout",
			// which reads as a socket I/O failure rather than a missing server ack.
			// Omit the target here — HTTP/SOCKS log the host once on the outer line.
			return errServerAckTimeout
		case localAddr, ok := <-f.localAddr:
			if !ok {
				return errServerRejected
			}
			select {
			case localAddrChan <- localAddr:
			case <-ctx.Done():
				return ctx.Err()
			}
			// done
			//log.Printf("rpc: server -> %s -> %s success", req.Host, localAddr)
		}

		return nil
	})

	err := errGroup.Wait()
	if f.abortCause != nil {
		// Resetting the client fails the upload read too, and that error can
		// reach the errgroup first; the response failure is the real cause.
		return fmt.Errorf("download: %w", f.abortCause)
	}
	if err != io.EOF {
		return err
	}
	return nil
}

func (f *Forwarder) addTx(n int) {
	if n <= 0 {
		return
	}
	tx := uint64(n)
	f.Statistic.AddTx(tx)
	transport.GlobalStats.AddTx(tx)
}

func (f *Forwarder) addRx(n int) {
	if n <= 0 {
		return
	}
	rx := uint64(n)
	f.Statistic.AddRx(rx)
	transport.GlobalStats.AddRx(rx)
}
