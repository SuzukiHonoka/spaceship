//go:build unix

package direct

import (
	"errors"
	"net"
	"sync"
	"syscall"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

// maxDatagramSize holds any UDP payload, so a receive is never truncated.
const maxDatagramSize = 65535

var datagramBuffers = sync.Pool{
	New: func() any {
		buf := make([]byte, maxDatagramSize)
		return &buf
	},
}

var _ transport.PacketReader = (*connectedPacketConn)(nil)

// ReadPacket implements transport.PacketReader. RawConn.Read parks on the
// netpoller until the socket is readable, so a buffer is borrowed only for the
// receive itself; the payload is then copied out at its exact size. Read
// deadlines and Close behave as they do for ReadFrom.
func (c *connectedPacketConn) ReadPacket() ([]byte, net.Addr, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, nil, err
	}

	var (
		payload []byte
		recvErr error
	)
	err = raw.Read(func(fd uintptr) bool {
		bufp := datagramBuffers.Get().(*[]byte)
		defer datagramBuffers.Put(bufp)
		for {
			n, _, err := syscall.Recvfrom(int(fd), *bufp, 0)
			switch {
			case errors.Is(err, syscall.EINTR):
				continue
			case errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EWOULDBLOCK):
				// Not readable yet: wait on the poller without a buffer.
				return false
			case err != nil:
				recvErr = &net.OpError{Op: "read", Net: "udp", Source: c.LocalAddr(), Addr: c.remote, Err: err}
				return true
			}
			payload = append([]byte(nil), (*bufp)[:n]...)
			return true
		}
	})
	if err != nil {
		return nil, nil, err
	}
	if recvErr != nil {
		return nil, nil, recvErr
	}
	return payload, c.remote, nil
}
