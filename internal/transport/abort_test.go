package transport

import (
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

// tcpPair returns the two ends of a loopback TCP connection.
func tcpPair(t *testing.T) (local, remote net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- conn
	}()
	remote, err = net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	local = <-accepted
	if local == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() {
		_ = local.Close()
		_ = remote.Close()
	})
	return local, remote
}

// readUntilEnd drains peer and returns how its stream ended.
func readUntilEnd(t *testing.T, peer net.Conn) error {
	t.Helper()
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := io.Copy(io.Discard, peer)
	if err == nil {
		return io.EOF
	}
	return err
}

func TestAbortResetsPeer(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(net.Conn) net.Conn
	}{
		{"tcp", func(c net.Conn) net.Conn { return c }},
		{"once", utils.OnceNetConn},
		{"once twice", func(c net.Conn) net.Conn { return utils.OnceNetConn(utils.OnceNetConn(c)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, remote := tcpPair(t)
			Abort(tc.wrap(local))
			if err := readUntilEnd(t, remote); !errors.Is(err, syscall.ECONNRESET) {
				t.Fatalf("peer saw %v, want a connection reset", err)
			}
		})
	}
}

func TestOnceConnFirstEndingWins(t *testing.T) {
	local, remote := tcpPair(t)
	conn := utils.OnceNetConn(local)
	_ = conn.Close()
	Abort(conn) // too late: the orderly close already ended the stream
	if err := readUntilEnd(t, remote); !errors.Is(err, io.EOF) {
		t.Fatalf("peer saw %v after an orderly close, want EOF", err)
	}
}

type countingCloser struct{ closed int }

func (c *countingCloser) Close() error {
	c.closed++
	return nil
}

func TestAbortAllFallsBackToCloseAndDedupes(t *testing.T) {
	closer := &countingCloser{}
	AbortAll(closer, nil, closer, io.Discard)
	if closer.closed != 1 {
		t.Fatalf("closed %d times, want 1", closer.closed)
	}
}

// uncomparableCloser has a slice field, so == on two of them panics.
type uncomparableCloser struct {
	closed *int
	_      []byte
}

func (c uncomparableCloser) Close() error {
	*c.closed++
	return nil
}

func TestAbortAllToleratesUncomparableValues(t *testing.T) {
	closed := 0
	value := uncomparableCloser{closed: &closed}
	AbortAll(value, value)
	if closed != 2 {
		t.Fatalf("closed %d times, want each uncomparable value closed", closed)
	}
}
