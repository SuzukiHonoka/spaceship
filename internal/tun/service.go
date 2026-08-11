package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	spaceshipDNS "github.com/SuzukiHonoka/spaceship/v2/internal/dns"
	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

type routeResolver func(string) (transport.Transport, error)

type openedDevice struct {
	endpoint stack.LinkEndpoint
	name     string
	mtu      uint32
	closer   io.Closer
}

// Service terminates TCP sessions from a Linux TUN device in an independent
// gVisor network stack and sends them through Spaceship's configured routes.
// When DNS hijacking is enabled, UDP is admitted only for destination port 53.
type Service struct {
	parentCtx context.Context
	ctx       context.Context
	cancel    context.CancelFunc
	cfg       Config

	stack  *stack.Stack
	nicID  tcpip.NICID
	device openedDevice

	resolveRoute routeResolver
	exchanger    spaceshipDNS.WireExchanger

	linkErrors chan error

	flowMu    sync.Mutex
	closing   bool
	flows     sync.WaitGroup
	flowSlots chan struct{}
	dnsSlots  chan struct{}
	udpSlots  chan struct{}
	// dnsConnections counts live DNS-over-TCP connections so the DNS pool can
	// be shared fairly between them. See fairDNSShare.
	dnsConnections atomic.Int64

	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
	runOnce   sync.Once
	runDone   chan struct{}
	runErr    error
}

// New opens the configured TUN descriptor and prepares its netstack.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}

	linkErrors := make(chan error, 1)
	device, err := openTUNDevice(normalized, func(err error) {
		if err == nil {
			err = errors.New("link endpoint stopped")
		}
		select {
		case linkErrors <- err:
		default:
		}
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		if closeErr := device.closer.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("tun: close device after canceled setup: %w", closeErr))
		}
		return nil, err
	}

	service, err := newService(ctx, normalized, device, linkErrors)
	if err != nil {
		if closeErr := device.closer.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("tun: close device after setup failure: %w", closeErr))
		}
		return nil, err
	}
	return service, nil
}

func newService(ctx context.Context, cfg Config, device openedDevice, linkErrors chan error) (*Service, error) {
	// #nosec G118 -- cancel is retained by Service and invoked by Close.
	serviceCtx, cancel := context.WithCancel(ctx)
	s := &Service{
		parentCtx:    ctx,
		ctx:          serviceCtx,
		cancel:       cancel,
		cfg:          cfg,
		device:       device,
		resolveRoute: router.GetRoute,
		exchanger:    spaceshipDNS.NewRPCExchanger(),
		linkErrors:   linkErrors,
		flowSlots:    make(chan struct{}, cfg.MaxConnections),
		dnsSlots:     make(chan struct{}, cfg.DNS.MaxInFlight),
		udpSlots:     make(chan struct{}, min(cfg.MaxConnections, cfg.DNS.MaxInFlight)),
		closeDone:    make(chan struct{}),
		runDone:      make(chan struct{}),
	}

	transportProtocols := []stack.TransportProtocolFactory{
		tcp.NewProtocol,
		icmp.NewProtocol4,
		icmp.NewProtocol6,
	}
	if cfg.DNS.Enabled {
		transportProtocols = append(transportProtocols, udp.NewProtocol)
	}

	netstack := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
			ipv6.NewProtocol,
		},
		TransportProtocols: transportProtocols,
	})
	s.stack = netstack

	tcpForwarder := tcp.NewForwarder(
		netstack,
		0,
		cfg.MaxPendingConnections,
		s.handleTCPRequest,
	)
	netstack.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)
	dropICMP := func(stack.TransportEndpointID, *stack.PacketBuffer) bool {
		// Promiscuous mode makes every packet look locally addressed. Consume
		// inbound ICMP so netstack does not synthesize echo replies for arbitrary
		// Internet destinations and accidentally advertise unsupported service.
		return true
	}
	netstack.SetTransportProtocolHandler(icmp.ProtocolNumber4, dropICMP)
	netstack.SetTransportProtocolHandler(icmp.ProtocolNumber6, dropICMP)

	if cfg.DNS.Enabled {
		udpForwarder := udp.NewForwarder(netstack, s.handleUDPRequest)
		netstack.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)
	}

	s.nicID = netstack.NextNICID()
	if err := netstack.CreateNIC(s.nicID, device.endpoint); err != nil {
		cancel()
		netstack.Destroy()
		return nil, fmt.Errorf("tun: create netstack NIC: %s", err)
	}
	cleanup := func() {
		_ = netstack.RemoveNIC(s.nicID)
		netstack.Destroy()
	}

	if err := netstack.SetPromiscuousMode(s.nicID, true); err != nil {
		cleanup()
		cancel()
		return nil, fmt.Errorf("tun: enable netstack promiscuous mode: %s", err)
	}
	if err := netstack.SetSpoofing(s.nicID, true); err != nil {
		cleanup()
		cancel()
		return nil, fmt.Errorf("tun: enable netstack address spoofing: %s", err)
	}
	netstack.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: s.nicID},
		{Destination: header.IPv6EmptySubnet, NIC: s.nicID},
	})

	return s, nil
}

// Run blocks until ctx is canceled or the underlying link endpoint fails.
// Multiple callers observe the same terminal result.
func (s *Service) Run() error {
	s.runOnce.Do(func() {
		defer close(s.runDone)
		log.Printf(
			"tun: interface %s ready (mtu=%d, route_mode=%s, tcp_limit=%d, dns_hijack=%t)",
			s.device.name,
			s.device.mtu,
			s.cfg.RouteMode,
			s.cfg.MaxConnections,
			s.cfg.DNS.Enabled,
		)

		select {
		case <-s.ctx.Done():
			s.runErr = s.ctx.Err()
		case err := <-s.linkErrors:
			// Close cancels the service before removing the NIC. Some link
			// endpoints report that intentional removal through ClosedFunc, so
			// cancellation wins when both signals become ready together.
			if contextErr := s.ctx.Err(); contextErr != nil {
				s.runErr = contextErr
			} else {
				s.runErr = fmt.Errorf("tun: link endpoint failed: %w", err)
			}
		}
		if err := s.Close(); err != nil {
			s.runErr = errors.Join(s.runErr, err)
		}
	})
	<-s.runDone

	if errors.Is(s.runErr, context.Canceled) && s.closeErr == nil {
		if err := s.parentCtx.Err(); err != nil {
			return err
		}
		return nil
	}
	return s.runErr
}

// Name returns the kernel interface name attached to this service.
func (s *Service) Name() string {
	return s.device.name
}

// Close immediately stops packet intake, aborts active netstack endpoints, and
// waits for every flow before releasing the owned TUN descriptor.
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.flowMu.Lock()
		s.closing = true
		s.flowMu.Unlock()

		log.Println("tun: shutting down")
		s.cancel()

		if s.stack != nil {
			if err := s.stack.RemoveNIC(s.nicID); err != nil {
				s.closeErr = errors.Join(
					s.closeErr,
					fmt.Errorf("tun: remove netstack NIC: %s", err),
				)
			}
			s.stack.Close()
		}

		// flowMu serializes WaitGroup.Add with the closing transition. No new
		// Add can occur after closing becomes true, so Wait cannot race with Add.
		// A TCP forwarder callback that passed beginFlow before the transition
		// can still register an endpoint while CreateEndpoint performs its
		// handshake. Stack.Close only aborts the endpoints visible in its own
		// snapshot, so sweep until every such callback has returned.
		s.waitForFlowsAndAbortLateEndpoints()
		if s.stack != nil {
			s.stack.Wait()
		}
		if s.device.closer != nil {
			if err := s.device.closer.Close(); err != nil {
				s.closeErr = errors.Join(s.closeErr, fmt.Errorf("tun: close device: %w", err))
			}
		}
		close(s.closeDone)
	})
	<-s.closeDone
	return s.closeErr
}

func (s *Service) waitForFlowsAndAbortLateEndpoints() {
	flowsDone := make(chan struct{})
	go func() {
		s.flows.Wait()
		close(flowsDone)
	}()

	if s.stack == nil {
		<-flowsDone
		return
	}

	// Abort is idempotent for gVisor TCP/UDP endpoints. Re-sweeping endpoints
	// seen by Stack.Close is intentional: its documentation allows endpoints
	// modified during the snapshot to escape that call.
	seen := make(map[stack.TransportEndpoint]struct{})
	abortNewEndpoints := func() {
		for _, endpoint := range s.stack.RegisteredEndpoints() {
			if _, ok := seen[endpoint]; ok {
				continue
			}
			seen[endpoint] = struct{}{}
			endpoint.Abort()
		}
	}

	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		abortNewEndpoints()
		select {
		case <-flowsDone:
			// No handler that may create an endpoint remains. One may have
			// registered immediately before its final Done, so take one stable
			// snapshot before Stack.Wait.
			abortNewEndpoints()
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) beginFlow() bool {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	if s.closing {
		return false
	}
	select {
	case s.flowSlots <- struct{}{}:
		s.flows.Add(1)
		return true
	default:
		return false
	}
}

// rejectTCPRequest releases every request, as required by gVisor. A reset is
// emitted only while the stack is open; after shutdown starts, Complete(false)
// removes the forwarder entry and references without sending on a torn-down
// network stack.
func (s *Service) rejectTCPRequest(request *tcp.ForwarderRequest) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	request.Complete(!s.closing)
}

func (s *Service) endFlow() {
	<-s.flowSlots
	s.flows.Done()
}

// tryAcquireSlot reserves one unit of a counting channel without blocking.
func tryAcquireSlot(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// releaseSlot returns one unit reserved by tryAcquireSlot.
func releaseSlot(slots chan struct{}) {
	<-slots
}

// fairDNSShare returns how many concurrent DNS RPCs one DNS-over-TCP
// connection may hold right now, given ceiling as its static upper bound.
//
// A static ceiling alone bounds any single connection but reserves nothing: a
// few busy connections can still fill the pool between them and leave a new
// connection with no capacity. Dividing the pool by the number of active
// connections gives every one of them at least one slot while there are no
// more connections than slots, so none is starved. A connection already
// holding more than its current share simply stops acquiring; its in-flight
// queries drain within QueryTimeout, so the pool converges without revocation.
func (s *Service) fairDNSShare(ceiling int) int {
	active := s.dnsConnections.Load()
	if active < 1 {
		active = 1
	}
	share := int64(cap(s.dnsSlots)) / active
	return max(1, min(ceiling, int(share)))
}

func (s *Service) acquireDNS() bool {
	return tryAcquireSlot(s.dnsSlots)
}

func (s *Service) releaseDNS() {
	releaseSlot(s.dnsSlots)
}

func (s *Service) acquireUDPFlow() bool {
	return tryAcquireSlot(s.udpSlots)
}

func (s *Service) releaseUDPFlow() {
	releaseSlot(s.udpSlots)
}
