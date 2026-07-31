package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"

	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

func (s *Service) handleTCPRequest(request *tcp.ForwarderRequest) {
	if request == nil {
		return
	}
	if !s.beginFlow() {
		s.rejectTCPRequest(request)
		return
	}
	defer s.endFlow()

	id := request.ID()
	var waitQueue waiter.Queue
	endpoint, tcpErr := request.CreateEndpoint(&waitQueue)
	if tcpErr != nil {
		s.rejectTCPRequest(request)
		return
	}
	request.Complete(false)

	conn := gonet.NewTCPConn(&waitQueue, endpoint)
	defer utils.Close(conn)

	if s.cfg.DNS.Enabled && id.LocalPort == 53 {
		s.serveTCPDNS(conn)
		return
	}

	if err := s.proxyTCP(conn, id); err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, io.EOF) &&
		!errors.Is(err, net.ErrClosed) {
		log.Printf("tun: TCP flow failed: %v", err)
	}
}

func (s *Service) proxyTCP(conn net.Conn, id stack.TransportEndpointID) error {
	destination, err := endpointAddress(id.LocalAddress, id.LocalPort)
	if err != nil {
		return err
	}

	route, err := s.resolveRoute(destination.Addr().String())
	if err != nil {
		return fmt.Errorf("route %s: %w", destination, err)
	}
	defer utils.Close(route)

	localAddr := make(chan string, 1)
	if err := route.Proxy(s.ctx, destination.String(), localAddr, conn, conn); err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, io.EOF) {
		return fmt.Errorf("proxy %s via %s: %w", destination, route, err)
	}
	return nil
}

func endpointAddress(address tcpip.Address, port uint16) (netip.AddrPort, error) {
	raw := address.AsSlice()
	addr, ok := netip.AddrFromSlice(raw)
	if !ok || !addr.IsValid() || addr.IsUnspecified() || addr.IsMulticast() || port == 0 {
		return netip.AddrPort{}, fmt.Errorf("tun: invalid TCP destination %s:%d", address, port)
	}
	return netip.AddrPortFrom(addr.Unmap(), port), nil
}
