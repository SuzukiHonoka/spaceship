package client

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
)

type recvMsg struct {
	resp *proto.ProxyDST
	err  error
}

type packetAddr string

func (a packetAddr) Network() string { return "udp" }
func (a packetAddr) String() string  { return string(a) }

// StreamPacketConn wraps a gRPC bidirectional stream and implements net.PacketConn.
// It assumes that each write sends a single ProxySRC payload message and
// each read receives a single ProxyDST payload message.
type StreamPacketConn struct {
	stream    proto.Proxy_ProxyClient
	cancel    context.CancelFunc
	ctx       context.Context
	localAddr net.Addr
	peerAddr  net.Addr

	recvCh chan recvMsg

	readMu    sync.Mutex // serializes ReadFrom callers
	writeMu   sync.Mutex // serializes stream send-side ops (WriteTo / CloseSend)
	rdeadline pipeDeadline
	wdeadline pipeDeadline

	// sendMu orders a write deadline firing against a Send starting, so the
	// deadline either stops the Send before it begins or cancels it in flight.
	sendMu  sync.Mutex
	sending bool
}

// NewStreamPacketConn creates a new StreamPacketConn from a gRPC stream.
func NewStreamPacketConn(ctx context.Context, stream proto.Proxy_ProxyClient, cancel context.CancelFunc, targetAddr string) *StreamPacketConn {
	addr := &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: 0}
	var peer net.Addr = packetAddr(targetAddr)
	if targetHost, targetPort, err := net.SplitHostPort(targetAddr); err == nil {
		if ip := net.ParseIP(targetHost); ip != nil {
			udpPeer := &net.UDPAddr{IP: ip}
			if port, err := net.LookupPort("udp", targetPort); err == nil {
				udpPeer.Port = port
			}
			peer = udpPeer
		}
	}

	c := &StreamPacketConn{
		stream:    stream,
		cancel:    cancel,
		ctx:       ctx,
		localAddr: addr,
		peerAddr:  peer,
		recvCh:    make(chan recvMsg, 1),
		rdeadline: makePipeDeadline(),
		wdeadline: makePipeDeadline(),
	}
	c.wdeadline.onFire = c.writeDeadlineFired
	go c.readLoop()
	return c
}

func (c *StreamPacketConn) readLoop() {
	defer close(c.recvCh)
	for {
		resp, err := c.stream.Recv()
		select {
		case <-c.ctx.Done():
			return
		case c.recvCh <- recvMsg{resp, err}:
		}
		if err != nil {
			return
		}
	}
}

func (c *StreamPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	for {
		var msg recvMsg
		select {
		case <-c.ctx.Done():
			return 0, nil, net.ErrClosed
		case <-c.rdeadline.wait():
			return 0, nil, os.ErrDeadlineExceeded
		case received, ok := <-c.recvCh:
			if !ok {
				return 0, nil, net.ErrClosed
			}
			msg = received
		}

		if msg.err != nil {
			if errors.Is(msg.err, io.EOF) || errors.Is(msg.err, context.Canceled) {
				return 0, nil, net.ErrClosed
			}
			return 0, nil, msg.err
		}

		switch msg.resp.Status {
		case proto.ProxyStatus_Accepted:
			// Accepted is the stream handshake acknowledgement. It is not a UDP
			// datagram, so keep waiting for the first Session payload.
			continue
		case proto.ProxyStatus_Session:
		case proto.ProxyStatus_EOF:
			return 0, nil, net.ErrClosed
		default:
			return 0, nil, errors.New("remote proxy returned error status")
		}

		payload := msg.resp.GetPayload()
		if len(payload) == 0 {
			return 0, c.peerAddr, nil
		}

		n = copy(p, payload)
		if n < len(payload) {
			return n, c.peerAddr, io.ErrShortBuffer
		}

		return n, c.peerAddr, nil
	}
}

func (c *StreamPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	deadline := c.wdeadline.wait()
	select {
	case <-c.ctx.Done():
		return 0, net.ErrClosed
	case <-deadline:
		return 0, os.ErrDeadlineExceeded
	default:
	}

	req := &proto.ProxySRC{
		HeaderOrPayload: &proto.ProxySRC_Payload{
			Payload: p,
		},
	}
	// Send inline rather than on a helper goroutine: the handoff cost a
	// scheduler hop per datagram. Close still unblocks it because Send observes
	// stream cancellation, and a write deadline cancels the stream through
	// writeDeadlineFired, since gRPC has no per-Send deadline.
	c.sendMu.Lock()
	if isClosedChan(deadline) {
		c.sendMu.Unlock()
		return 0, os.ErrDeadlineExceeded
	}
	c.sending = true
	c.sendMu.Unlock()

	err = c.stream.Send(req)

	c.sendMu.Lock()
	c.sending = false
	c.sendMu.Unlock()

	if err == nil {
		return len(p), nil
	}
	if isClosedChan(deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	if c.ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return 0, net.ErrClosed
	}
	return 0, err
}

// writeDeadlineFired cancels the stream when the write deadline expires during
// a Send. Canceling is the only way to unblock a gRPC Send, and it ends the
// association, as it did when the deadline was enforced by a helper goroutine.
func (c *StreamPacketConn) writeDeadlineFired() {
	c.sendMu.Lock()
	sending := c.sending
	c.sendMu.Unlock()
	if sending {
		c.cancel()
	}
}

func (c *StreamPacketConn) Close() error {
	c.cancel()
	// CloseSend and Send are both send-side operations on the gRPC stream and
	// must not run concurrently. Serialize against WriteTo via writeMu.
	c.writeMu.Lock()
	_ = c.stream.CloseSend()
	c.writeMu.Unlock()
	return nil
}

func (c *StreamPacketConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *StreamPacketConn) SetDeadline(t time.Time) error {
	c.rdeadline.set(t)
	c.wdeadline.set(t)
	return nil
}

func (c *StreamPacketConn) SetReadDeadline(t time.Time) error {
	c.rdeadline.set(t)
	return nil
}

func (c *StreamPacketConn) SetWriteDeadline(t time.Time) error {
	c.wdeadline.set(t)
	return nil
}

// pipeDeadline is an abstraction for handling I/O timeouts, adapted from the
// standard library's net package (net/pipe.go). Crucially, a deadline change is
// observed even by an operation already blocked on wait(): the cancel channel
// is reused (not replaced) until it actually fires, so resetting to an earlier
// deadline unblocks in-flight I/O.
type pipeDeadline struct {
	mu     sync.Mutex
	timer  *time.Timer
	cancel chan struct{} // closed when the deadline fires
	// onFire, when set, runs each time the deadline expires, after cancel is
	// closed. Set it before the deadline is first used.
	onFire func()
}

func makePipeDeadline() pipeDeadline {
	return pipeDeadline{cancel: make(chan struct{})}
}

// set sets the deadline. A zero value disables it.
func (d *pipeDeadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil && !d.timer.Stop() {
		<-d.cancel // wait for the timer callback to finish and close cancel
	}
	d.timer = nil

	// Time is zero or already expired.
	closed := isClosedChan(d.cancel)
	if t.IsZero() {
		if closed {
			d.cancel = make(chan struct{})
		}
		return
	}

	// Time in the future, set up a timer to wait.
	if dur := time.Until(t); dur > 0 {
		if closed {
			d.cancel = make(chan struct{})
		}
		cancel := d.cancel
		d.timer = time.AfterFunc(dur, func() {
			close(cancel)
			if d.onFire != nil {
				d.onFire()
			}
		})
		return
	}

	// Time in the past, so close immediately.
	if !closed {
		close(d.cancel)
		if d.onFire != nil {
			d.onFire()
		}
	}
}

// wait returns a channel that is closed when the deadline fires.
func (d *pipeDeadline) wait() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancel
}

func isClosedChan(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
