package tun

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	spaceshipDNS "github.com/SuzukiHonoka/spaceship/v2/internal/dns"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/miekg/dns"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/pipe"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/ports"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

var _ spaceshipDNS.WireExchanger = (*testDNSExchanger)(nil)

type testDNSExchanger struct {
	exchange func(context.Context, []byte, proto.Network, bool) ([]byte, error)
}

func (e *testDNSExchanger) Exchange(
	ctx context.Context,
	wire []byte,
	network proto.Network,
	blockIPv6 bool,
) ([]byte, error) {
	return e.exchange(ctx, wire, network, blockIPv6)
}

type echoRoute struct {
	targets chan string
}

func (r *echoRoute) String() string {
	return "test-echo"
}

func (r *echoRoute) Dial(string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}

func (r *echoRoute) Close() error {
	return nil
}

func (r *echoRoute) Proxy(
	ctx context.Context,
	addr string,
	localAddr chan<- string,
	dst io.Writer,
	src io.Reader,
) error {
	defer close(localAddr)
	localAddr <- "127.0.0.1:1"
	select {
	case r.targets <- addr:
	case <-ctx.Done():
		return ctx.Err()
	}

	payload := make([]byte, 4)
	if _, err := io.ReadFull(src, payload); err != nil {
		return err
	}
	_, err := dst.Write([]byte(strings.ToUpper(string(payload))))
	return err
}

var _ transport.Transport = (*echoRoute)(nil)

type closeCounter struct {
	count atomic.Int32
}

func (c *closeCounter) Close() error {
	c.count.Add(1)
	return nil
}

type failingCloser struct {
	err error
}

func (c failingCloser) Close() error {
	return c.err
}

type partialWriter struct {
	max  int
	data []byte
}

func (w *partialWriter) Write(payload []byte) (int, error) {
	if len(payload) > w.max {
		payload = payload[:w.max]
	}
	w.data = append(w.data, payload...)
	return len(payload), nil
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) {
	return 0, nil
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type proxyResultRoute struct {
	err        error
	closeCount atomic.Int32
}

func (*proxyResultRoute) String() string {
	return "test-result"
}

func (*proxyResultRoute) Dial(string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}

func (r *proxyResultRoute) Close() error {
	r.closeCount.Add(1)
	return nil
}

func (r *proxyResultRoute) Proxy(
	_ context.Context,
	_ string,
	localAddr chan<- string,
	_ io.Writer,
	_ io.Reader,
) error {
	close(localAddr)
	return r.err
}

var _ transport.Transport = (*proxyResultRoute)(nil)

type lateShutdownEndpoint struct {
	abortOnce sync.Once
	aborted   chan struct{}
	onAbort   func()
}

func (*lateShutdownEndpoint) HandlePacket(stack.TransportEndpointID, *stack.PacketBuffer) {}

func (*lateShutdownEndpoint) HandleError(stack.TransportError, *stack.PacketBuffer) {}

func (e *lateShutdownEndpoint) Abort() {
	e.abortOnce.Do(func() {
		close(e.aborted)
		e.onAbort()
	})
}

func (*lateShutdownEndpoint) Wait() {}

var _ stack.TransportEndpoint = (*lateShutdownEndpoint)(nil)

type cancellationRoute struct {
	started chan struct{}
	once    sync.Once
}

func (r *cancellationRoute) String() string {
	return "test-cancellation"
}

func (r *cancellationRoute) Dial(string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}

func (r *cancellationRoute) Close() error {
	return nil
}

func (r *cancellationRoute) Proxy(
	ctx context.Context,
	_ string,
	localAddr chan<- string,
	_ io.Writer,
	_ io.Reader,
) error {
	defer close(localAddr)
	localAddr <- "127.0.0.1:1"
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return ctx.Err()
}

var _ transport.Transport = (*cancellationRoute)(nil)

func newLinkedService(t *testing.T, cfg Config) (*Service, *stack.Stack, tcpip.NICID) {
	t.Helper()
	return newLinkedServiceForProtocol(
		t,
		cfg,
		ipv4.NewProtocol,
		ipv4.ProtocolNumber,
		tcpip.AddrFrom4([4]byte{10, 0, 0, 2}),
		24,
		header.IPv4EmptySubnet,
	)
}

func newLinkedIPv6Service(t *testing.T, cfg Config) (*Service, *stack.Stack, tcpip.NICID) {
	t.Helper()
	return newLinkedServiceForProtocol(
		t,
		cfg,
		ipv6.NewProtocol,
		ipv6.ProtocolNumber,
		tcpip.AddrFrom16([16]byte{0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}),
		64,
		header.IPv6EmptySubnet,
	)
}

func newLinkedServiceForProtocol(
	t *testing.T,
	cfg Config,
	networkProtocol stack.NetworkProtocolFactory,
	protocolNumber tcpip.NetworkProtocolNumber,
	peerAddress tcpip.Address,
	prefixLen int,
	defaultSubnet tcpip.Subnet,
) (*Service, *stack.Stack, tcpip.NICID) {
	t.Helper()
	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	serviceLink, peerLink := pipe.New("", "", uint32(cfg.MTU))
	closed := new(closeCounter)
	service, err := newService(
		context.Background(),
		cfg,
		openedDevice{
			endpoint: serviceLink,
			name:     "test-tun",
			mtu:      uint32(cfg.MTU),
			closer:   closed,
		},
		make(chan error, 1),
	)
	if err != nil {
		t.Fatal(err)
	}

	transportProtocols := []stack.TransportProtocolFactory{tcp.NewProtocol}
	if cfg.DNS.Enabled {
		transportProtocols = append(transportProtocols, udp.NewProtocol)
	}
	peer := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{networkProtocol},
		TransportProtocols: transportProtocols,
	})
	peerNIC := peer.NextNICID()
	if err := peer.CreateNIC(peerNIC, peerLink); err != nil {
		_ = service.Close()
		t.Fatalf("peer CreateNIC: %s", err)
	}
	if err := peer.AddProtocolAddress(peerNIC, tcpip.ProtocolAddress{
		Protocol: protocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address:   peerAddress,
			PrefixLen: prefixLen,
		},
	}, stack.AddressProperties{}); err != nil {
		peer.Destroy()
		_ = service.Close()
		t.Fatalf("peer AddProtocolAddress: %s", err)
	}
	peer.SetRouteTable([]tcpip.Route{
		{Destination: defaultSubnet, NIC: peerNIC},
	})

	t.Cleanup(func() {
		peer.Destroy()
		if err := service.Close(); err != nil {
			t.Errorf("service Close() error = %v", err)
		}
		if got := closed.count.Load(); got != 1 {
			t.Errorf("device close count = %d, want 1", got)
		}
	})
	return service, peer, peerNIC
}

func TestTCPForwarderRoutesOriginalDestination(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{MaxConnections: 4})
	route := &echoRoute{targets: make(chan string, 1)}
	service.resolveRoute = func(host string) (transport.Transport, error) {
		if host != "198.51.100.20" {
			return nil, fmt.Errorf("unexpected route host %q", host)
		}
		return route, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := gonet.DialContextTCP(ctx, peer, tcpip.FullAddress{
		NIC:  peerNIC,
		Addr: tcpip.AddrFrom4([4]byte{198, 51, 100, 20}),
		Port: 443,
	}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("DialContextTCP() error = %v", err)
	}
	defer conn.Close()

	if err := writeAll(conn, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if got := string(response); got != "PING" {
		t.Fatalf("response = %q, want PING", got)
	}
	select {
	case target := <-route.targets:
		if target != "198.51.100.20:443" {
			t.Fatalf("proxy target = %q", target)
		}
	case <-ctx.Done():
		t.Fatal("route did not receive target")
	}
}

func TestTCPForwarderRoutesIPv6OriginalDestination(t *testing.T) {
	service, peer, peerNIC := newLinkedIPv6Service(t, Config{MaxConnections: 4})
	route := &echoRoute{targets: make(chan string, 1)}
	service.resolveRoute = func(host string) (transport.Transport, error) {
		if host != "2001:db8::20" {
			return nil, fmt.Errorf("unexpected route host %q", host)
		}
		return route, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := gonet.DialContextTCP(ctx, peer, tcpip.FullAddress{
		NIC: peerNIC,
		Addr: tcpip.AddrFrom16([16]byte{
			0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0,
			0, 0, 0, 0, 0, 0, 0, 0x20,
		}),
		Port: 443,
	}, ipv6.ProtocolNumber)
	if err != nil {
		t.Fatalf("DialContextTCP() error = %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if got := string(response); got != "PING" {
		t.Fatalf("response = %q, want PING", got)
	}
	select {
	case target := <-route.targets:
		if target != "[2001:db8::20]:443" {
			t.Fatalf("proxy target = %q", target)
		}
	case <-ctx.Done():
		t.Fatal("route did not receive IPv6 target")
	}
}

func TestEndpointAddressValidation(t *testing.T) {
	tests := []struct {
		name    string
		address tcpip.Address
		port    uint16
	}{
		{name: "empty address", address: tcpip.Address{}, port: 443},
		{name: "unspecified address", address: tcpip.AddrFrom4([4]byte{}), port: 443},
		{
			name:    "multicast address",
			address: tcpip.AddrFrom4([4]byte{224, 0, 0, 1}),
			port:    443,
		},
		{
			name:    "zero port",
			address: tcpip.AddrFrom4([4]byte{192, 0, 2, 10}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := endpointAddress(tt.address, tt.port); err == nil {
				t.Fatal("endpointAddress accepted an invalid destination")
			}
		})
	}

	mapped := [16]byte{
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0xff, 0xff, 192, 0, 2, 10,
	}
	got, err := endpointAddress(tcpip.AddrFrom16(mapped), 443)
	if err != nil {
		t.Fatalf("endpointAddress(IPv4-mapped) error = %v", err)
	}
	if got.String() != "192.0.2.10:443" {
		t.Fatalf("endpointAddress(IPv4-mapped) = %s, want 192.0.2.10:443", got)
	}
}

func TestProxyTCPErrorHandling(t *testing.T) {
	id := stack.TransportEndpointID{
		LocalAddress: tcpip.AddrFrom4([4]byte{192, 0, 2, 20}),
		LocalPort:    443,
	}

	t.Run("route lookup", func(t *testing.T) {
		wantErr := errors.New("route unavailable")
		service := &Service{
			ctx: context.Background(),
			resolveRoute: func(string) (transport.Transport, error) {
				return nil, wantErr
			},
		}
		conn, peer := net.Pipe()
		defer conn.Close()
		defer peer.Close()

		if err := service.proxyTCP(conn, id); !errors.Is(err, wantErr) {
			t.Fatalf("proxyTCP() error = %v, want wrapping %v", err, wantErr)
		}
	})

	tests := []struct {
		name     string
		proxyErr error
		wantErr  bool
	}{
		{name: "proxy failure", proxyErr: errors.New("proxy unavailable"), wantErr: true},
		{name: "EOF is normal", proxyErr: io.EOF},
		{name: "cancellation is normal", proxyErr: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := &proxyResultRoute{err: tt.proxyErr}
			service := &Service{
				ctx: context.Background(),
				resolveRoute: func(string) (transport.Transport, error) {
					return route, nil
				},
			}
			conn, peer := net.Pipe()
			defer conn.Close()
			defer peer.Close()

			err := service.proxyTCP(conn, id)
			if tt.wantErr {
				if !errors.Is(err, tt.proxyErr) {
					t.Fatalf("proxyTCP() error = %v, want wrapping %v", err, tt.proxyErr)
				}
			} else if err != nil {
				t.Fatalf("proxyTCP() error = %v, want nil", err)
			}
			if got := route.closeCount.Load(); got != 1 {
				t.Fatalf("route Close() calls = %d, want 1", got)
			}
		})
	}
}

func TestNilTCPForwarderRequestIsIgnored(t *testing.T) {
	new(Service).handleTCPRequest(nil)
}

func TestTCPForwarderRejectsWhenConnectionLimitIsFull(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{MaxConnections: 1})

	// Occupy the admission slot without adding a flow: this isolates the
	// forwarder's overload path and verifies it actively resets the SYN rather
	// than creating an untracked endpoint.
	service.flowSlots <- struct{}{}
	defer func() { <-service.flowSlots }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := gonet.DialContextTCP(ctx, peer, tcpip.FullAddress{
		NIC:  peerNIC,
		Addr: tcpip.AddrFrom4([4]byte{198, 51, 100, 21}),
		Port: 443,
	}, ipv4.ProtocolNumber)
	if err == nil {
		_ = conn.Close()
		t.Fatal("DialContextTCP() succeeded while connection limit was full")
	}
}

func TestCloseCancelsAndWaitsForActiveTCPFlow(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{MaxConnections: 2})
	route := &cancellationRoute{started: make(chan struct{})}
	service.resolveRoute = func(string) (transport.Transport, error) {
		return route, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := gonet.DialContextTCP(ctx, peer, tcpip.FullAddress{
		NIC:  peerNIC,
		Addr: tcpip.AddrFrom4([4]byte{198, 51, 100, 22}),
		Port: 443,
	}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	select {
	case <-route.started:
	case <-ctx.Done():
		t.Fatal("route did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- service.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Close() did not cancel and wait for the active TCP flow")
	}
}

func TestShutdownAbortsEndpointRegisteredAfterInitialStackClose(t *testing.T) {
	netstack := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{udp.NewProtocol},
	})
	t.Cleanup(netstack.Destroy)

	// Stack.Close only snapshots endpoints that already exist.
	netstack.Close()

	service := &Service{stack: netstack}
	service.flows.Add(1)
	endpoint := &lateShutdownEndpoint{
		aborted: make(chan struct{}),
		onAbort: service.flows.Done,
	}
	id := stack.TransportEndpointID{
		LocalAddress: tcpip.AddrFrom4([4]byte{192, 0, 2, 53}),
		LocalPort:    53,
	}
	if err := netstack.RegisterTransportEndpoint(
		[]tcpip.NetworkProtocolNumber{ipv4.ProtocolNumber},
		udp.ProtocolNumber,
		id,
		endpoint,
		ports.Flags{},
		0,
	); err != nil {
		service.flows.Done()
		t.Fatalf("RegisterTransportEndpoint() error = %s", err)
	}
	t.Cleanup(func() {
		netstack.UnregisterTransportEndpoint(
			[]tcpip.NetworkProtocolNumber{ipv4.ProtocolNumber},
			udp.ProtocolNumber,
			id,
			endpoint,
			ports.Flags{},
			0,
		)
	})

	done := make(chan struct{})
	go func() {
		service.waitForFlowsAndAbortLateEndpoints()
		close(done)
	}()

	select {
	case <-endpoint.aborted:
	case <-time.After(time.Second):
		t.Fatal("late endpoint was not aborted")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown wait did not finish after aborting late endpoint")
	}
}

func TestServiceRunStopsOnContextCancellation(t *testing.T) {
	cfg, err := NormalizeConfig(Config{MaxConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	serviceLink, _ := pipe.New("", "", uint32(cfg.MTU))
	closed := new(closeCounter)
	ctx, cancel := context.WithCancel(context.Background())
	service, err := newService(
		ctx,
		cfg,
		openedDevice{
			endpoint: serviceLink,
			name:     "lifecycle-tun",
			mtu:      uint32(cfg.MTU),
			closer:   closed,
		},
		make(chan error, 1),
	)
	if err != nil {
		t.Fatal(err)
	}
	if service.Name() != "lifecycle-tun" {
		t.Fatalf("Name() = %q", service.Name())
	}

	done := make(chan error, 1)
	go func() { done <- service.Run() }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop on context cancellation")
	}
	if got := closed.count.Load(); got != 1 {
		t.Fatalf("device close count = %d, want 1", got)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestServiceRunTreatsDirectCloseAsIntentional(t *testing.T) {
	newLifecycleService := func(t *testing.T) (*Service, chan error) {
		t.Helper()
		cfg, err := NormalizeConfig(Config{MaxConnections: 2})
		if err != nil {
			t.Fatal(err)
		}
		serviceLink, _ := pipe.New("", "", uint32(cfg.MTU))
		linkErrors := make(chan error, 1)
		service, err := newService(
			context.Background(),
			cfg,
			openedDevice{
				endpoint: serviceLink,
				name:     "direct-close",
				mtu:      uint32(cfg.MTU),
				closer:   new(closeCounter),
			},
			linkErrors,
		)
		if err != nil {
			t.Fatal(err)
		}
		return service, linkErrors
	}

	t.Run("Run active", func(t *testing.T) {
		for range 25 {
			service, linkErrors := newLifecycleService(t)
			runDone := make(chan error, 1)
			go func() { runDone <- service.Run() }()

			closeDone := make(chan error, 1)
			go func() { closeDone <- service.Close() }()
			<-service.ctx.Done()
			// fdbased endpoints can report the intentional NIC removal at the
			// same time as cancellation. Exercise both ready select cases.
			select {
			case linkErrors <- errors.New("link endpoint stopped"):
			default:
			}

			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatalf("Close() error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Close() did not return")
			}
			select {
			case err := <-runDone:
				if err != nil {
					t.Fatalf("Run() after direct Close error = %v, want nil", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Run() did not return after direct Close")
			}
		}
	})

	t.Run("both terminal signals already ready", func(t *testing.T) {
		for range 25 {
			service, linkErrors := newLifecycleService(t)
			if err := service.Close(); err != nil {
				t.Fatal(err)
			}
			linkErrors <- errors.New("link endpoint stopped")
			if err := service.Run(); err != nil {
				t.Fatalf("Run() with canceled context and link closure = %v, want nil", err)
			}
		}
	})
}

func TestServiceRunPreservesDeviceCloseFailure(t *testing.T) {
	cfg, err := NormalizeConfig(Config{MaxConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	serviceLink, _ := pipe.New("", "", uint32(cfg.MTU))
	closeFailure := errors.New("descriptor close failed")
	ctx, cancel := context.WithCancel(context.Background())
	service, err := newService(
		ctx,
		cfg,
		openedDevice{
			endpoint: serviceLink,
			name:     "close-error",
			mtu:      uint32(cfg.MTU),
			closer:   failingCloser{err: closeFailure},
		},
		make(chan error, 1),
	)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- service.Run() }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, closeFailure) {
			t.Fatalf("Run() error = %v, want cancellation and close failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not return")
	}
	if err := service.Close(); !errors.Is(err, closeFailure) {
		t.Fatalf("second Close() error = %v, want memoized close failure", err)
	}
}

func TestServiceRunReturnsLinkFailure(t *testing.T) {
	cfg, err := NormalizeConfig(Config{MaxConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	serviceLink, _ := pipe.New("", "", uint32(cfg.MTU))
	linkErrors := make(chan error, 1)
	service, err := newService(
		context.Background(),
		cfg,
		openedDevice{
			endpoint: serviceLink,
			name:     "failed-link",
			mtu:      uint32(cfg.MTU),
			closer:   new(closeCounter),
		},
		linkErrors,
	)
	if err != nil {
		t.Fatal(err)
	}
	linkErrors <- errors.New("device EOF")
	if err := service.Run(); err == nil || !strings.Contains(err.Error(), "device EOF") {
		t.Fatalf("Run() error = %v, want link failure", err)
	}
}

func TestNewReturnsUnsupportedOffLinux(t *testing.T) {
	if Supported() {
		t.Skip("platform supports Linux TUN")
	}
	_, err := New(context.Background(), Config{})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("New() error = %v, want ErrUnsupported", err)
	}
}

func TestNewRejectsCanceledContextBeforeOpeningDevice(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(ctx, Config{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("New(canceled context) error = %v, want context.Canceled", err)
	}
}

func TestFlowAndDNSLimitsRejectExcessWork(t *testing.T) {
	service := &Service{
		flowSlots: make(chan struct{}, 1),
		dnsSlots:  make(chan struct{}, 1),
		udpSlots:  make(chan struct{}, 1),
	}
	if !service.beginFlow() {
		t.Fatal("first beginFlow() rejected")
	}
	if service.beginFlow() {
		t.Fatal("beginFlow() exceeded capacity")
	}
	service.endFlow()

	service.flowMu.Lock()
	service.closing = true
	service.flowMu.Unlock()
	if service.beginFlow() {
		t.Fatal("beginFlow() accepted work while closing")
	}

	if !service.acquireDNS() {
		t.Fatal("first acquireDNS() rejected")
	}
	if service.acquireDNS() {
		t.Fatal("acquireDNS() exceeded capacity")
	}
	service.releaseDNS()

	if !service.acquireUDPFlow() {
		t.Fatal("first acquireUDPFlow() rejected")
	}
	if service.acquireUDPFlow() {
		t.Fatal("acquireUDPFlow() exceeded capacity")
	}
	service.releaseUDPFlow()
}

func TestDNSHijackUsesRPCForTCPAndUDP(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 8,
		DNS: DNSConfig{
			Enabled:        true,
			BlockIPv6:      true,
			QueryTimeout:   time.Second,
			TCPIdleTimeout: time.Second,
			MaxInFlight:    4,
		},
	})

	networks := make(chan proto.Network, 2)
	service.exchanger = &testDNSExchanger{exchange: func(
		_ context.Context,
		wire []byte,
		network proto.Network,
		blockIPv6 bool,
	) ([]byte, error) {
		if !blockIPv6 {
			return nil, errors.New("block IPv6 policy was not forwarded")
		}
		networks <- network
		return dnsSuccess(t, wire, "203.0.113.9")
	}}
	service.resolveRoute = func(string) (transport.Transport, error) {
		return nil, errors.New("port 53 must not use the general route")
	}

	query := new(dns.Msg)
	query.SetQuestion("known.test.", dns.TypeA)
	wireQuery, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	target := tcpip.FullAddress{
		NIC:  peerNIC,
		Addr: tcpip.AddrFrom4([4]byte{192, 0, 2, 53}),
		Port: 53,
	}

	udpConn, err := gonet.DialUDP(peer, nil, &target, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("DialUDP() error = %v", err)
	}
	defer udpConn.Close()
	_ = udpConn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := udpConn.Write(wireQuery); err != nil {
		t.Fatal(err)
	}
	udpResponse := make([]byte, dns.MaxMsgSize)
	n, err := udpConn.Read(udpResponse)
	if err != nil {
		t.Fatalf("UDP DNS read error = %v", err)
	}
	assertDNSAnswer(t, udpResponse[:n], query.Id, "203.0.113.9")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tcpConn, err := gonet.DialContextTCP(ctx, peer, target, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("DialContextTCP(:53) error = %v", err)
	}
	defer tcpConn.Close()
	_ = tcpConn.SetDeadline(time.Now().Add(3 * time.Second))
	frame := make([]byte, 2+len(wireQuery))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(wireQuery)))
	copy(frame[2:], wireQuery)
	if err := writeAll(tcpConn, frame); err != nil {
		t.Fatal(err)
	}
	wireResponse, err := readDNSFrame(tcpConn)
	if err != nil {
		t.Fatal(err)
	}
	assertDNSAnswer(t, wireResponse, query.Id, "203.0.113.9")

	gotNetworks := map[proto.Network]bool{}
	for range 2 {
		select {
		case network := <-networks:
			gotNetworks[network] = true
		case <-ctx.Done():
			t.Fatal("DNS exchanger did not receive both transports")
		}
	}
	if !gotNetworks[proto.Network_UDP] || !gotNetworks[proto.Network_TCP] {
		t.Fatalf("DNS exchanger networks = %v", gotNetworks)
	}
}

func TestServeTCPDNSSupportsPipelining(t *testing.T) {
	cfg, err := NormalizeConfig(Config{DNS: DNSConfig{
		Enabled:        true,
		QueryTimeout:   2 * time.Second,
		TCPIdleTimeout: 2 * time.Second,
		MaxInFlight:    2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &Service{
		ctx:      ctx,
		cfg:      cfg,
		dnsSlots: make(chan struct{}, cfg.DNS.MaxInFlight),
	}

	slowRelease := make(chan struct{})
	fastSeen := make(chan struct{})
	var fastOnce sync.Once
	service.exchanger = &testDNSExchanger{exchange: func(
		ctx context.Context,
		wire []byte,
		_ proto.Network,
		_ bool,
	) ([]byte, error) {
		msg := new(dns.Msg)
		if err := msg.Unpack(wire); err != nil {
			return nil, err
		}
		if msg.Question[0].Name == "slow.test." {
			select {
			case <-slowRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			fastOnce.Do(func() { close(fastSeen) })
		}
		return dnsSuccess(t, wire, "203.0.113.10")
	}}

	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		service.serveTCPDNS(serverConn)
		close(done)
	}()

	slow := dnsQueryWire(t, "slow.test.", 1)
	fast := dnsQueryWire(t, "fast.test.", 2)
	go func() {
		_ = writeAll(clientConn, append(dnsFrame(slow), dnsFrame(fast)...))
	}()

	select {
	case <-fastSeen:
	case <-time.After(time.Second):
		t.Fatal("pipelined fast query did not start while slow query was pending")
	}
	first, err := readDNSFrame(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	firstMsg := new(dns.Msg)
	if err := firstMsg.Unpack(first); err != nil {
		t.Fatal(err)
	}
	if firstMsg.Id != 2 {
		t.Fatalf("first pipelined response ID = %d, want fast query ID 2", firstMsg.Id)
	}

	close(slowRelease)
	second, err := readDNSFrame(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	secondMsg := new(dns.Msg)
	if err := secondMsg.Unpack(second); err != nil {
		t.Fatal(err)
	}
	if secondMsg.Id != 1 {
		t.Fatalf("second pipelined response ID = %d, want slow query ID 1", secondMsg.Id)
	}

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveTCPDNS did not stop after connection close")
	}
}

func TestServeTCPDNSRejectsUnsupportedAndBusyQueries(t *testing.T) {
	cfg, err := NormalizeConfig(Config{DNS: DNSConfig{
		Enabled:        true,
		QueryTimeout:   time.Second,
		TCPIdleTimeout: time.Second,
		MaxInFlight:    1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	service := &Service{
		ctx:      ctx,
		cfg:      cfg,
		dnsSlots: make(chan struct{}, 1),
		exchanger: &testDNSExchanger{exchange: func(
			context.Context,
			[]byte,
			proto.Network,
			bool,
		) ([]byte, error) {
			calls.Add(1)
			return nil, errors.New("unexpected exchange")
		}},
	}
	// Keep the sole RPC slot occupied so an otherwise valid query must fail
	// closed with SERVFAIL.
	service.dnsSlots <- struct{}{}
	defer func() { <-service.dnsSlots }()

	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		service.serveTCPDNS(serverConn)
		close(done)
	}()

	update := new(dns.Msg)
	update.SetQuestion("update.test.", dns.TypeA)
	update.Opcode = dns.OpcodeUpdate
	axfr := new(dns.Msg)
	axfr.SetQuestion("transfer.test.", dns.TypeAXFR)
	valid := new(dns.Msg)
	valid.SetQuestion("busy.test.", dns.TypeA)

	tests := []struct {
		query *dns.Msg
		rcode int
	}{
		{query: update, rcode: dns.RcodeNotImplemented},
		{query: axfr, rcode: dns.RcodeRefused},
		{query: valid, rcode: dns.RcodeServerFailure},
	}
	for _, tt := range tests {
		wire, packErr := tt.query.Pack()
		if packErr != nil {
			t.Fatal(packErr)
		}
		if err := writeAll(clientConn, dnsFrame(wire)); err != nil {
			t.Fatal(err)
		}
		responseWire, err := readDNSFrame(clientConn)
		if err != nil {
			t.Fatal(err)
		}
		response := new(dns.Msg)
		if err := response.Unpack(responseWire); err != nil {
			t.Fatal(err)
		}
		if response.Id != tt.query.Id || response.Rcode != tt.rcode {
			t.Fatalf(
				"response for opcode=%d qtype=%d = id %d rcode %d, want id %d rcode %d",
				tt.query.Opcode,
				tt.query.Question[0].Qtype,
				response.Id,
				response.Rcode,
				tt.query.Id,
				tt.rcode,
			)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("exchanger calls = %d, want 0", calls.Load())
	}

	// A zero-length DNS-over-TCP frame is a protocol error and must terminate
	// the stream rather than spin or consume an RPC slot.
	if err := writeAll(clientConn, []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveTCPDNS did not reject a zero-length frame")
	}
	_ = clientConn.Close()
}

func TestDNSFailureResponsesNeverFallBack(t *testing.T) {
	cfg, err := NormalizeConfig(Config{DNS: DNSConfig{
		Enabled:        true,
		QueryTimeout:   100 * time.Millisecond,
		TCPIdleTimeout: time.Second,
		MaxInFlight:    1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	service := &Service{
		ctx:      ctx,
		cfg:      cfg,
		dnsSlots: make(chan struct{}, 1),
		exchanger: &testDNSExchanger{exchange: func(
			context.Context,
			[]byte,
			proto.Network,
			bool,
		) ([]byte, error) {
			calls.Add(1)
			return nil, errors.New("Spaceship DNS unavailable")
		}},
	}
	wire := dnsQueryWire(t, "failure.test.", 0x4242)
	query, rcode := validateDNSQuery(wire)
	if query == nil || rcode != dns.RcodeSuccess {
		t.Fatalf("validateDNSQuery() = (%v, %d)", query, rcode)
	}
	response := service.exchangeDNS(wire, query, proto.Network_UDP)
	msg := new(dns.Msg)
	if err := msg.Unpack(response); err != nil {
		t.Fatal(err)
	}
	if msg.Id != 0x4242 || msg.Rcode != dns.RcodeServerFailure {
		t.Fatalf("exchange failure response = ID %#x, rcode %d", msg.Id, msg.Rcode)
	}
	if calls.Load() != 1 {
		t.Fatalf("exchanger calls = %d, want exactly 1 and no fallback", calls.Load())
	}

	malformed := []byte{0x12, 0x34, 0xff}
	if query, rcode := validateDNSQuery(malformed); query != nil || rcode != dns.RcodeFormatError {
		t.Fatalf("malformed validation = (%v, %d)", query, rcode)
	}
	formerr := packDNSFailure(malformed, nil, dns.RcodeFormatError)
	if err := msg.Unpack(formerr); err != nil {
		t.Fatal(err)
	}
	if msg.Id != 0x1234 || msg.Rcode != dns.RcodeFormatError {
		t.Fatalf("FORMERR response = ID %#x, rcode %d", msg.Id, msg.Rcode)
	}
}

func TestPackDNSFailureFallsBackForInvalidQuestion(t *testing.T) {
	query := new(dns.Msg)
	query.Id = 0x5151
	query.Question = []dns.Question{{
		Name:   strings.Repeat("x", 64) + ".test.",
		Qtype:  dns.TypeA,
		Qclass: dns.ClassINET,
	}}

	wire := packDNSFailure(nil, query, dns.RcodeRefused)
	response := new(dns.Msg)
	if err := response.Unpack(wire); err != nil {
		t.Fatalf("minimal fallback is malformed: %v", err)
	}
	if response.Id != query.Id || !response.Response ||
		response.Rcode != dns.RcodeRefused || len(response.Question) != 0 {
		t.Fatalf("minimal fallback = %+v", response)
	}
}

func TestWriteAllHandlesPartialWritesAndFailures(t *testing.T) {
	writer := &partialWriter{max: 2}
	if err := writeAll(writer, []byte("hello")); err != nil {
		t.Fatalf("writeAll(partial writer) error = %v", err)
	}
	if got := string(writer.data); got != "hello" {
		t.Fatalf("writeAll(partial writer) wrote %q, want hello", got)
	}

	if err := writeAll(zeroWriter{}, []byte("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAll(zero writer) error = %v, want io.ErrShortWrite", err)
	}

	wantErr := errors.New("write failed")
	if err := writeAll(failingWriter{err: wantErr}, []byte("x")); !errors.Is(err, wantErr) {
		t.Fatalf("writeAll(failing writer) error = %v, want %v", err, wantErr)
	}
}

func dnsQueryWire(t *testing.T, name string, id uint16) []byte {
	t.Helper()
	msg := new(dns.Msg)
	msg.SetQuestion(name, dns.TypeA)
	msg.Id = id
	wire, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func dnsSuccess(t *testing.T, wire []byte, address string) ([]byte, error) {
	t.Helper()
	query := new(dns.Msg)
	if err := query.Unpack(wire); err != nil {
		return nil, err
	}
	response := new(dns.Msg)
	response.SetReply(query)
	response.RecursionAvailable = true
	response.Answer = []dns.RR{&dns.A{
		Hdr: dns.RR_Header{
			Name:   query.Question[0].Name,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    60,
		},
		A: net.ParseIP(address).To4(),
	}}
	return response.Pack()
}

func dnsFrame(wire []byte) []byte {
	frame := make([]byte, 2+len(wire))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(wire)))
	copy(frame[2:], wire)
	return frame
}

func readDNSFrame(reader io.Reader) ([]byte, error) {
	var size [2]byte
	if _, err := io.ReadFull(reader, size[:]); err != nil {
		return nil, err
	}
	wire := make([]byte, int(binary.BigEndian.Uint16(size[:])))
	if len(wire) == 0 {
		return nil, errors.New("zero-length DNS frame")
	}
	_, err := io.ReadFull(reader, wire)
	return wire, err
}

func assertDNSAnswer(t *testing.T, wire []byte, id uint16, address string) {
	t.Helper()
	response := new(dns.Msg)
	if err := response.Unpack(wire); err != nil {
		t.Fatal(err)
	}
	if !response.Response || response.Id != id || response.Rcode != dns.RcodeSuccess {
		t.Fatalf("DNS response header = %+v", response.MsgHdr)
	}
	if len(response.Answer) != 1 {
		t.Fatalf("DNS answers = %v, want one", response.Answer)
	}
	answer, ok := response.Answer[0].(*dns.A)
	if !ok || !answer.A.Equal(net.ParseIP(address)) {
		t.Fatalf("DNS answer = %v, want %s", response.Answer[0], address)
	}
}
