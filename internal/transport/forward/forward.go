package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"golang.org/x/net/proxy"
	"golang.org/x/sync/errgroup"
)

const TransportName = "forward"

var (
	dialerMu sync.RWMutex
	dialer   proxy.Dialer
)

var ErrDialerNotContextAware = errors.New("forward: dialer does not support context cancellation")

// PreparedDialer is an immutable, validated forward-dialer update. Preparing
// and activating are separate so a multi-part config reload can validate every
// candidate before changing process-global state.
type PreparedDialer struct {
	dialer proxy.Dialer
}

func PrepareDialer(d proxy.Dialer) (PreparedDialer, error) {
	if d != nil {
		if _, ok := d.(proxy.ContextDialer); !ok {
			return PreparedDialer{}, ErrDialerNotContextAware
		}
	}
	return PreparedDialer{dialer: d}, nil
}

func (d PreparedDialer) Activate() {
	dialerMu.Lock()
	dialer = d.dialer
	dialerMu.Unlock()
}

func Attach(d proxy.Dialer) error {
	prepared, err := PrepareDialer(d)
	if err != nil {
		return err
	}
	prepared.Activate()
	return nil
}

func attachedDialer() proxy.Dialer {
	dialerMu.RLock()
	defer dialerMu.RUnlock()
	return dialer
}

// Forward is transport that connects through an upstream proxy.
// Each call to Proxy is fully self-contained — no mutable session state is
// stored. The dialer is a snapshot of the process-wide configuration at New.
type Forward struct {
	dialer proxy.Dialer
}

var _ transport.ContextDialer = (*Forward)(nil)

func New() transport.Transport {
	return &Forward{dialer: attachedDialer()}
}

func (f *Forward) Attach(d proxy.Dialer) error {
	if d == nil {
		f.dialer = nil
		return nil
	}
	if _, ok := d.(proxy.ContextDialer); !ok {
		return ErrDialerNotContextAware
	}
	f.dialer = d
	return nil
}

func (f *Forward) String() string {
	return TransportName
}

func (f *Forward) Close() error {
	return nil
}

func (f *Forward) Dial(network, addr string) (net.Conn, error) {
	return f.DialContext(context.Background(), network, addr)
}

func (f *Forward) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if f.dialer == nil {
		return nil, errors.New("forward: dialer not attached")
	}
	dialer, ok := f.dialer.(proxy.ContextDialer)
	if !ok {
		return nil, ErrDialerNotContextAware
	}

	// The frontend context normally lives for the whole server lifetime. Bound
	// each upstream connection attempt independently so a blackholed proxy cannot
	// consume a handler slot until the process is shut down. WithTimeout preserves
	// an earlier caller deadline, and a zero timeout retains the explicit
	// no-deadline behavior used by the transport settings.
	if timeout := transport.GetDialTimeout(); timeout > 0 {
		dialCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = dialCtx
	}
	return dialer.DialContext(ctx, network, addr)
}

func (f *Forward) Proxy(ctx context.Context, addr string, localAddr chan<- string, dst io.Writer, src io.Reader) (err error) {
	defer close(localAddr)

	conn, err := f.DialContext(ctx, transport.GetNetwork(), addr)
	if err != nil {
		return err
	}
	localAddr <- conn.LocalAddr().String()
	defer utils.Close(conn)

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var closeOnce sync.Once
	closeSession := func() {
		closeOnce.Do(func() {
			transport.CloseAll(src, dst, conn)
		})
	}

	var responseDone atomic.Bool
	var errGroup errgroup.Group
	errGroup.Go(func() error {
		err := transport.CopyWithContext(sessionCtx, closeSession, conn, src, transport.DirectionOut)
		if err != nil && !errors.Is(err, io.EOF) {
			cancel()
			closeSession()
			return err
		}
		transport.CloseWriteOrClose(conn)
		return err
	})

	errGroup.Go(func() error {
		err := transport.CopyWithContext(sessionCtx, closeSession, dst, conn, transport.DirectionIn)
		if err == nil || errors.Is(err, io.EOF) {
			responseDone.Store(true)
		}
		cancel()
		closeSession()
		return err
	})

	if err = errGroup.Wait(); err != nil && !errors.Is(err, io.EOF) {
		if responseDone.Load() {
			return nil
		}
		return fmt.Errorf("forward: %w", err)
	}
	return nil
}
