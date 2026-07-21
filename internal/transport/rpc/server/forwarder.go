package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"golang.org/x/sync/errgroup"
)

const maxUDPPacketSize = 65535

// resolveTarget determines the dial network and address from a proxy header.
// It prefers the typed Network field; for backward compatibility with pre-2.1.5
// clients that encoded the network as a scheme prefix on the address, a legacy
// "udp://"/"tcp://" prefix is still honored when present.
// Network strings are passed through transport.DialNetwork so IPv6-disable
// rewrites dual-stack names (udp → udp4, tcp → tcp4).
func resolveTarget(h *proto.ProxySRC_ProxyHeader) (network, addr string) {
	if n, a, ok := splitLegacyScheme(h.GetAddr()); ok {
		return transport.DialNetwork(n), a
	}
	switch h.GetNetwork() {
	case proto.Network_UDP:
		return transport.DialNetwork("udp"), h.GetAddr()
	default:
		return transport.DialNetwork(transport.GetNetwork()), h.GetAddr()
	}
}

// splitLegacyScheme strips a legacy "udp://"/"tcp://" address prefix if present.
func splitLegacyScheme(raw string) (network, addr string, ok bool) {
	switch {
	case strings.HasPrefix(raw, "udp://"):
		return "udp", strings.TrimPrefix(raw, "udp://"), true
	case strings.HasPrefix(raw, "tcp://"):
		return "tcp", strings.TrimPrefix(raw, "tcp://"), true
	default:
		return "", "", false
	}
}

// isUDPNetwork reports whether network is a UDP family name.
func isUDPNetwork(network string) bool {
	return network == "udp" || network == "udp4" || network == "udp6"
}

type Forwarder struct {
	Ctx    context.Context
	Stream proto.Proxy_ProxyServer
	Conn   net.Conn
	// target is the dial address from the client header (host:port). Set during
	// handshake for readable server logs even when dial fails.
	target string
	// network is the dial network chosen during handshake ("tcp"/"udp"/…).
	// Used to decide whether an empty payload ends the session (TCP) or is a
	// valid zero-length datagram (UDP).
	network   string
	Ack       chan struct{}
	closeOnce sync.Once
}

// Target returns the dial address from the last handshake, or empty if none.
func (f *Forwarder) Target() string {
	return f.target
}

func NewForwarder(ctx context.Context, stream proto.Proxy_ProxyServer) *Forwarder {
	return &Forwarder{
		Ctx:    ctx,
		Stream: stream,
		Ack:    make(chan struct{}, 1),
	}
}

func (f *Forwarder) Close() error {
	var err error
	f.closeOnce.Do(func() {
		if f.Conn != nil {
			err = f.Conn.Close()
		}
	})
	return err
}

func (f *Forwarder) CopyTargetToClient(ctx context.Context) (err error) {
	// only start if ack dial succeed
	select {
	case _, ok := <-f.Ack:
		if !ok {
			return transport.ErrTargetACKFailed
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	// send local addr to client for nat
	msgAccept := &proto.ProxyDST{
		Status: proto.ProxyStatus_Accepted,
		HeaderOrPayload: &proto.ProxyDST_Header{
			Header: &proto.ProxyDST_ProxyHeader{
				Addr: f.Conn.LocalAddr().String(),
			},
		},
	}
	if err = f.Stream.Send(msgAccept); err != nil {
		return fmt.Errorf("send accept: %w", err)
	}

	// Closing the target connection is sufficient to interrupt a blocked Read.
	// Keep Send in this goroutine so it cannot outlive the RPC handler and race
	// a final status send on the same stream.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = f.Close()
		case <-done:
		}
	}()
	defer close(done)

	var b []byte
	if isUDPNetwork(f.network) {
		// A UDP Read returns at most one datagram and silently discards bytes
		// that do not fit. Use the protocol maximum instead of the tunable TCP
		// copy buffer so large datagrams are never truncated.
		b = make([]byte, maxUDPPacketSize)
	} else {
		buf := transport.Buffer()
		defer transport.PutBuffer(buf)
		b = *buf
	}

	dstData := &proto.ProxyDST{
		HeaderOrPayload: &proto.ProxyDST_Payload{Payload: nil},
	}
	payload := dstData.HeaderOrPayload.(*proto.ProxyDST_Payload)
	for {
		if err := f.copyTargetToClient(b, dstData, payload); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}

func (f *Forwarder) copyTargetToClient(buf []byte, dstData *proto.ProxyDST, payload *proto.ProxyDST_Payload) error {
	n, err := f.Conn.Read(buf)
	// Per io.Reader contract: process n > 0 bytes before considering error.
	// Prevents dropping the last chunk when Read returns data + io.EOF.
	if n > 0 || (n == 0 && err == nil && isUDPNetwork(f.network)) {
		payload.Payload = buf[:n]
		if sendErr := f.Stream.Send(dstData); sendErr != nil {
			return sendErr
		}
	}
	return err
}

func (f *Forwarder) handshake() error {
	req, err := f.Stream.Recv()
	if err != nil {
		return err
	}

	v, ok := req.HeaderOrPayload.(*proto.ProxySRC_Header)
	if !ok {
		return transport.ErrInvalidMessage
	}
	header := v.Header

	network, addr := resolveTarget(header)
	f.network = network
	f.target = addr

	// Auth is handled by the stream interceptor.
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	route, err := router.GetRoute(host)
	if err != nil {
		return fmt.Errorf("route: %w", err)
	}
	log.Printf("rpc: proxy accepted [%s] %s -> %s", network, host, route)

	// dial to target
	f.Conn, err = route.Dial(network, addr)
	if err != nil {
		_ = f.Stream.Send(&proto.ProxyDST{
			Status: proto.ProxyStatus_Error,
		})
		// Keep the dial error text from net (includes host); outer log adds target once.
		return fmt.Errorf("dial: %w", err)
	}
	return nil
}

func (f *Forwarder) CopyClientToTarget(ctx context.Context) error {
	defer close(f.Ack)

	// do the handshake first — return as-is (no "handshake error:" wrapper).
	if err := f.handshake(); err != nil {
		return err
	}

	// trigger read
	f.Ack <- struct{}{}

	// loop read client and forward
	errCh := make(chan error, 1)
	go func() {
		// reuse buffer
		srcData := new(proto.ProxySRC)
		for {
			// reset for new message
			srcData.Reset()
			if err := f.Stream.RecvMsg(srcData); err != nil {
				errCh <- err
				return
			}

			if err := f.copyClientToTarget(srcData); err != nil {
				errCh <- err
				return
			}
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		_ = f.Close()
		return ctx.Err()
	}
}

func (f *Forwarder) copyClientToTarget(buf *proto.ProxySRC) error {
	v, ok := buf.HeaderOrPayload.(*proto.ProxySRC_Payload)
	if !ok {
		return transport.ErrInvalidMessage
	}

	// Empty payload: for TCP this signals end-of-stream from the client.
	// For UDP an empty datagram is valid and must not tear down the session.
	if len(v.Payload) == 0 {
		if isUDPNetwork(f.network) {
			_, err := f.Conn.Write(v.Payload)
			return err
		}
		return io.EOF
	}
	//log.Printf("RX: %s", string(data))

	// write to remote
	n, err := f.Conn.Write(v.Payload)
	if n < len(v.Payload) {
		return io.ErrShortWrite
	}
	return err
}

func (f *Forwarder) Start() error {
	errGroup, ctx := errgroup.WithContext(f.Ctx)
	errGroup.Go(func() error {
		if err := f.CopyClientToTarget(ctx); err != nil {
			if err == io.EOF {
				return err
			}
			// Handshake failures already carry a short prefix (dial:/route:…).
			// Mid-session write failures get an "upload:" tag.
			if f.Conn == nil {
				return err
			}
			return fmt.Errorf("upload: %w", err)
		}
		return nil
	})
	errGroup.Go(func() error {
		if err := f.CopyTargetToClient(ctx); err != nil {
			if err == io.EOF {
				return err
			}
			return fmt.Errorf("download: %w", err)
		}
		return nil
	})

	return errGroup.Wait()
}
