package client

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
)

type recvMsg struct {
	resp *proto.ProxyDST
	err  error
}

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

	readMu  sync.Mutex
	writeMu sync.Mutex

	deadline time.Time
}

// NewStreamPacketConn creates a new StreamPacketConn from a gRPC stream.
func NewStreamPacketConn(ctx context.Context, stream proto.Proxy_ProxyClient, cancel context.CancelFunc, targetAddr string) *StreamPacketConn {
	addr := &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: 0}
	peer := &net.UDPAddr{}
	if targetHost, targetPort, err := net.SplitHostPort(targetAddr); err == nil {
		peer.IP = net.ParseIP(targetHost)
		if peer.IP == nil {
			peer.IP = net.ParseIP("0.0.0.0") // fallback for domain names
		}
		if port, err := net.LookupPort("udp", targetPort); err == nil {
			peer.Port = port
		}
	}

	c := &StreamPacketConn{
		stream:    stream,
		cancel:    cancel,
		ctx:       ctx,
		localAddr: addr,
		peerAddr:  peer,
		recvCh:    make(chan recvMsg, 1),
	}
	go c.readLoop()
	return c
}

func (c *StreamPacketConn) readLoop() {
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
	deadline := c.deadline
	c.readMu.Unlock()

	var timer <-chan time.Time
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d <= 0 {
			return 0, nil, context.DeadlineExceeded
		}
		t := time.NewTimer(d)
		defer t.Stop()
		timer = t.C
	}

	var msg recvMsg
	select {
	case <-c.ctx.Done():
		return 0, nil, net.ErrClosed
	case msg = <-c.recvCh:
	case <-timer:
		return 0, nil, context.DeadlineExceeded
	}

	if msg.err != nil {
		if errors.Is(msg.err, io.EOF) || errors.Is(msg.err, context.Canceled) {
			return 0, nil, net.ErrClosed
		}
		return 0, nil, msg.err
	}

	if msg.resp.Status != proto.ProxyStatus_Session && msg.resp.Status != proto.ProxyStatus_Accepted {
		if msg.resp.Status == proto.ProxyStatus_EOF {
			return 0, nil, net.ErrClosed
		}
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

func (c *StreamPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	req := &proto.ProxySRC{
		HeaderOrPayload: &proto.ProxySRC_Payload{
			Payload: p,
		},
	}
	if err := c.stream.Send(req); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *StreamPacketConn) Close() error {
	c.cancel()
	_ = c.stream.CloseSend()
	return nil
}

func (c *StreamPacketConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *StreamPacketConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

func (c *StreamPacketConn) SetReadDeadline(t time.Time) error {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	c.deadline = t
	return nil
}

func (c *StreamPacketConn) SetWriteDeadline(t time.Time) error {
	return nil
}
