package forward

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

type mockDialer struct {
	addr string
	conn net.Conn
	err  error
}

func (m *mockDialer) Dial(network, addr string) (c net.Conn, err error) {
	if m.err != nil {
		return nil, m.err
	}
	m.addr = addr
	return m.conn, nil
}

func (m *mockDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.Dial(network, addr)
}

type legacyDialer struct{}

func (legacyDialer) Dial(_, _ string) (net.Conn, error) {
	return nil, errors.New("not used")
}

func TestForward_Basics(t *testing.T) {
	f := New().(*Forward)
	if f.String() != TransportName {
		t.Errorf("String() = %v, want %v", f.String(), TransportName)
	}

	if err := f.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}

	_, err := f.Dial("tcp", "example.com:80")
	if err == nil || err.Error() != "forward: dialer not attached" {
		t.Errorf("Expected unattached dialer error, got %v", err)
	}

	// Attach
	md := &mockDialer{}
	if err := f.Attach(md); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	_, err = f.Dial("tcp", "example.com:80")
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	if md.addr != "example.com:80" {
		t.Errorf("dial address = %q, want example.com:80", md.addr)
	}

	if err := f.Attach(legacyDialer{}); !errors.Is(err, ErrDialerNotContextAware) {
		t.Fatalf("Attach(legacy) error = %v, want ErrDialerNotContextAware", err)
	}
}

func TestGlobalAttachSnapshotsValidatedDialer(t *testing.T) {
	if err := Attach(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := Attach(nil); err != nil {
			t.Errorf("detach global dialer: %v", err)
		}
	})

	md := &mockDialer{}
	if err := Attach(md); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	snapshot := New().(*Forward)
	if snapshot.dialer != md {
		t.Fatalf("New() dialer = %T, want attached mock dialer", snapshot.dialer)
	}

	if err := Attach(legacyDialer{}); !errors.Is(err, ErrDialerNotContextAware) {
		t.Fatalf("Attach(legacy) error = %v, want ErrDialerNotContextAware", err)
	}
	if got := New().(*Forward).dialer; got != md {
		t.Fatal("rejected Attach replaced the previous global dialer")
	}

	if err := Attach(nil); err != nil {
		t.Fatal(err)
	}
	if got := New().(*Forward).dialer; got != nil {
		t.Fatalf("New() retained detached dialer %T", got)
	}
	if snapshot.dialer != md {
		t.Fatal("detaching global dialer mutated an existing transport snapshot")
	}
}

func TestPreparedDialerActivatesOnlyAfterCommit(t *testing.T) {
	t.Cleanup(func() {
		if err := Attach(nil); err != nil {
			t.Errorf("detach global dialer: %v", err)
		}
	})

	active := &mockDialer{}
	if err := Attach(active); err != nil {
		t.Fatal(err)
	}
	replacement := &mockDialer{}
	prepared, err := PrepareDialer(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if got := New().(*Forward).dialer; got != active {
		t.Fatal("preparing a replacement changed the active dialer")
	}

	prepared.Activate()
	if got := New().(*Forward).dialer; got != replacement {
		t.Fatal("activating a prepared replacement did not update the dialer")
	}

	if _, err := PrepareDialer(legacyDialer{}); !errors.Is(err, ErrDialerNotContextAware) {
		t.Fatalf("PrepareDialer(legacy) error = %v, want ErrDialerNotContextAware", err)
	}
	if got := New().(*Forward).dialer; got != replacement {
		t.Fatal("rejected preparation changed the active dialer")
	}
}

func TestGlobalAttachConcurrentWithNew(t *testing.T) {
	t.Cleanup(func() {
		if err := Attach(nil); err != nil {
			t.Errorf("detach global dialer: %v", err)
		}
	})

	dialers := []*mockDialer{{}, {}}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := range 1000 {
			if err := Attach(dialers[i%len(dialers)]); err != nil {
				t.Errorf("Attach() error = %v", err)
				return
			}
		}
	}()
	for range 2 {
		go func() {
			defer wg.Done()
			for range 1000 {
				_ = New()
			}
		}()
	}
	wg.Wait()
}

func TestForward_Proxy(t *testing.T) {
	// Local echo server to act as the destination
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			_, _ = conn.Write(buf[:n])
		}
	}()

	f := New().(*Forward)

	// Custom mock dialer that actually dials our echo server
	if err := f.Attach(&mockDialer{
		conn: func() net.Conn {
			c, _ := net.Dial("tcp", ln.Addr().String())
			return c
		}(),
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	localAddr := make(chan string, 1)
	reqData := []byte("forward test")
	src := bytes.NewReader(reqData)
	var dst bytes.Buffer

	err = f.Proxy(ctx, ln.Addr().String(), localAddr, &dst, src)
	if err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}

	addr := <-localAddr
	if addr == "" {
		t.Errorf("Expected local addr, got empty string")
	}

	if dst.String() != string(reqData) {
		t.Errorf("Proxy() copied %q, want %q", dst.String(), string(reqData))
	}
}

type blockingContextDialer struct {
	started chan struct{}
}

func (d *blockingContextDialer) Dial(_, _ string) (net.Conn, error) {
	return nil, errors.New("context-free dial must not be used")
}

func (d *blockingContextDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	close(d.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestForward_Proxy_CancelsDial(t *testing.T) {
	dialer := &blockingContextDialer{started: make(chan struct{})}
	f := New().(*Forward)
	if err := f.Attach(dialer); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	localAddr := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- f.Proxy(ctx, "example.com:443", localAddr, nil, nil)
	}()

	select {
	case <-dialer.started:
	case <-time.After(3 * time.Second):
		t.Fatal("forward dial did not start")
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Proxy() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Proxy() did not cancel its dial")
	}
	if _, ok := <-localAddr; ok {
		t.Fatal("localAddr channel remained open after canceled dial")
	}
}

func TestForward_Proxy_BoundsDialByTransportTimeout(t *testing.T) {
	oldTimeout := transport.GetDialTimeout()
	transport.SetDialTimeout(25 * time.Millisecond)
	t.Cleanup(func() {
		transport.SetDialTimeout(oldTimeout)
	})

	dialer := &blockingContextDialer{started: make(chan struct{})}
	f := New().(*Forward)
	if err := f.Attach(dialer); err != nil {
		t.Fatal(err)
	}

	// The parent deadline is deliberately much later than the transport
	// timeout. This proves Forward applies its own per-attempt bound instead of
	// relying only on frontend shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	localAddr := make(chan string, 1)

	start := time.Now()
	err := f.Proxy(ctx, "example.com:443", localAddr, nil, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Proxy() error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("Proxy() took %v, want transport timeout well before parent deadline", elapsed)
	}
	if _, ok := <-localAddr; ok {
		t.Fatal("localAddr channel remained open after dial timeout")
	}
}
