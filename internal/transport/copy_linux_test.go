//go:build linux

package transport

import (
	"bytes"
	"io"
	"net"
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

// TestSpliceStreamsSelectsKernelPath pins the pairings that must stay on the
// splice(2) path. If a front end or egress ever wraps its socket in a type
// without Unwrap, this fails instead of silently falling back to user-space
// copies.
func TestSpliceStreamsSelectsKernelPath(t *testing.T) {
	proxySide, client := tcpPair(t)
	echoSide, target := tcpPair(t)
	go func() {
		_, _ = io.Copy(echoSide, echoSide)
		_ = echoSide.Close()
	}()

	payload := bytes.Repeat([]byte{0x3c}, 3*GetBufferSize()+7)
	go func() {
		_, _ = client.Write(payload)
		_ = client.(*net.TCPConn).CloseWrite()
	}()

	// Front-end shape: OnceNetConn-wrapped client → bare TCP egress.
	n, err, ok := spliceStreams(target, utils.OnceNetConn(proxySide))
	if !ok {
		t.Fatal("spliceStreams declined a wrapped TCP → TCP pair")
	}
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("spliceStreams = (%d, %v), want (%d, nil)", n, err, len(payload))
	}
	_ = target.(*net.TCPConn).CloseWrite()

	// Reverse direction: TCP egress → wrapped client.
	var got bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&got, client)
		close(readDone)
	}()
	n, err, ok = spliceStreams(utils.OnceNetConn(proxySide), target)
	if !ok {
		t.Fatal("spliceStreams declined a TCP → wrapped TCP pair")
	}
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("spliceStreams = (%d, %v), want (%d, nil)", n, err, len(payload))
	}
	_ = proxySide.(*net.TCPConn).CloseWrite()
	<-readDone
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("relayed %d bytes, want %d", got.Len(), len(payload))
	}
}

func TestSpliceStreamsDeclinesNonSockets(t *testing.T) {
	proxySide, _ := tcpPair(t)
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()

	if _, _, ok := spliceStreams(proxySide, a); ok {
		t.Fatal("spliceStreams accepted a net.Pipe source")
	}
	if _, _, ok := spliceStreams(a, proxySide); ok {
		t.Fatal("spliceStreams accepted a net.Pipe destination")
	}
	if _, _, ok := spliceStreams(io.Discard, proxySide); ok {
		t.Fatal("spliceStreams accepted an io.Discard destination")
	}
	// A prefix carrier is handled by copyStream before splice is consulted;
	// spliceStreams itself must not look through it or the prefix is lost.
	if _, _, ok := spliceStreams(proxySide, WithPrefix(proxySide, []byte("x"))); ok {
		t.Fatal("spliceStreams looked through a PrefixedConn")
	}
}
