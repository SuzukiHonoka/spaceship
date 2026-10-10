package client

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
)

// ErrEarlyAdmitUnavailable means the server did not admit the session before
// reading the target address. Admit has already returned the pooled connection.
// IP flows should dial after the client reply, the way they did before early
// admission.
var ErrEarlyAdmitUnavailable = errors.New("rpc client: server does not admit before the target address")

const (
	// earlyAdmitLegacyTTL bounds how long a failed probe suppresses further
	// probes. A server upgrade is picked up without restarting the process,
	// the same way DNS remembers a missing DnsExchange RPC.
	earlyAdmitLegacyTTL = time.Minute

	defaultEarlyAdmitProbeTimeout = 2 * time.Second
)

var (
	// earlyAdmitLegacyUntil is the unix-nano instant after which a legacy
	// server is probed again. Zero means the next Admit probes.
	earlyAdmitLegacyUntil atomic.Int64
	// earlyAdmitProbeNanos is how long Admit waits for admission headers.
	// Tests shorten it. Non-positive values use the default.
	earlyAdmitProbeNanos atomic.Int64
	earlyAdmitNotice     atomic.Bool
	// earlyAdmitEpoch lets Init and Destroy invalidate a probe that is still
	// blocked in Header, so that probe cannot mark the replacement server.
	earlyAdmitEpoch atomic.Uint64
)

func init() {
	earlyAdmitProbeNanos.Store(int64(defaultEarlyAdmitProbeTimeout))
}

func earlyAdmitProbeTimeout() time.Duration {
	if n := earlyAdmitProbeNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return defaultEarlyAdmitProbeTimeout
}

// resetEarlyAdmitLegacy forgets a legacy admission result and arms the
// one-time log again. Init and Destroy call it so a config reload probes the
// server instead of trusting the previous one.
func resetEarlyAdmitLegacy() {
	// Bump the epoch before clearing the deadline so an in-flight probe
	// observes the new epoch and does not republish a deadline afterwards.
	earlyAdmitEpoch.Add(1)
	earlyAdmitLegacyUntil.Store(0)
	earlyAdmitNotice.Store(false)
}

func markEarlyAdmitLegacy(epoch uint64, observed int64) {
	if earlyAdmitEpoch.Load() != epoch {
		return
	}
	until := time.Now().Add(earlyAdmitLegacyTTL).UnixNano()
	if !earlyAdmitLegacyUntil.CompareAndSwap(observed, until) {
		return
	}
	if earlyAdmitEpoch.Load() != epoch {
		earlyAdmitLegacyUntil.CompareAndSwap(until, 0)
		return
	}
	noteEarlyAdmitLegacy()
}

func noteEarlyAdmitLegacy() {
	if earlyAdmitNotice.CompareAndSwap(false, true) {
		log.Println("rpc: server does not admit before the target address; " +
			"IP flows will dial after the client reply. Upgrade the server")
	}
}

// admittedProxy is a tunnel the server has authenticated and admitted.
// The target address is not sent until Proxy, so the front end can read the
// client flight after it has told that client the connection is open.
type admittedProxy struct {
	client   *Client
	ctx      context.Context
	cancel   context.CancelFunc
	stream   proto.Proxy_ProxyClient
	once     sync.Once
	closeErr error
}

type admitHeader struct {
	admitted bool
	err      error
}

// Admit opens a proxy stream and waits until the server has authenticated the
// user and admitted the session. The returned session owns c: Close releases
// the pooled connection, including when Admit itself fails.
//
// Servers older than early admission read the target address before they send
// headers. Header blocks until those headers arrive or the RPC ends, so a
// probe that exceeds earlyAdmitProbeTimeout is remembered for a minute and
// Admit then returns ErrEarlyAdmitUnavailable without opening a stream.
func (c *Client) Admit(ctx context.Context) (transport.AdmittedSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	epoch := earlyAdmitEpoch.Load()
	observed := earlyAdmitLegacyUntil.Load()
	if time.Now().UnixNano() < observed {
		_ = c.Close()
		noteEarlyAdmitLegacy()
		return nil, ErrEarlyAdmitUnavailable
	}
	if err := ctx.Err(); err != nil {
		_ = c.Close()
		return nil, context.Cause(ctx)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	stream, err := c.ProxyClient.Proxy(sessionCtx)
	if err != nil {
		cancel()
		_ = c.Close()
		return nil, err
	}

	// Header blocks until the server sends admission headers. On refusal it
	// returns a nil map and swallows the status; Recv then returns that status
	// without waiting for a target address.
	headerCh := make(chan admitHeader, 1)
	go func() {
		md, headerErr := stream.Header()
		headerCh <- admitHeader{admitted: headerErr == nil && md != nil, err: headerErr}
	}()

	timer := time.NewTimer(earlyAdmitProbeTimeout())
	defer timer.Stop()

	select {
	case <-ctx.Done():
		c.finishAdmitWait(cancel, headerCh)
		return nil, context.Cause(ctx)
	case <-timer.C:
		if ctx.Err() != nil {
			c.finishAdmitWait(cancel, headerCh)
			return nil, context.Cause(ctx)
		}
		c.finishAdmitWait(cancel, headerCh)
		markEarlyAdmitLegacy(epoch, observed)
		return nil, ErrEarlyAdmitUnavailable
	case header := <-headerCh:
		if header.admitted {
			return &admittedProxy{
				client: c,
				ctx:    sessionCtx,
				cancel: cancel,
				stream: stream,
			}, nil
		}
		_, recvErr := stream.Recv()
		cancel()
		_ = c.Close()
		if header.err != nil {
			return nil, header.err
		}
		if recvErr != nil {
			return nil, recvErr
		}
		return nil, errors.New("proxy admission failed")
	}
}

// finishAdmitWait unblocks Header, waits until that goroutine has finished,
// and returns the pooled connection.
func (c *Client) finishAdmitWait(cancel context.CancelFunc, headerCh <-chan admitHeader) {
	cancel()
	<-headerCh
	_ = c.Close()
}

func (a *admittedProxy) Proxy(ctx context.Context, addr string, localAddr chan<- string, w io.Writer, r io.Reader) error {
	defer close(localAddr)
	stop := context.AfterFunc(ctx, a.cancel)
	defer stop()
	return a.client.proxyStream(a.ctx, a.cancel, a.stream, addr, localAddr, w, r)
}

func (a *admittedProxy) Close() error {
	a.once.Do(func() {
		a.cancel()
		a.closeErr = a.client.Close()
	})
	return a.closeErr
}
