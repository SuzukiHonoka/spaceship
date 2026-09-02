package tun

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/dnswire"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"github.com/miekg/dns"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

func (s *Service) handleUDPRequest(request *udp.ForwarderRequest) bool {
	if request == nil || request.ID().LocalPort != 53 || !s.cfg.DNS.Enabled {
		return false
	}
	if !s.beginFlow() {
		return false
	}
	if !s.acquireUDPFlow() {
		s.endFlow()
		return false
	}

	var waitQueue waiter.Queue
	endpoint, udpErr := request.CreateEndpoint(&waitQueue)
	if udpErr != nil {
		s.releaseUDPFlow()
		s.endFlow()
		return false
	}

	conn := gonet.NewUDPConn(&waitQueue, endpoint)
	go func() {
		defer s.endFlow()
		defer s.releaseUDPFlow()
		defer utils.Close(conn)
		s.serveUDPDNS(conn)
	}()
	return true
}

func (s *Service) serveUDPDNS(conn net.Conn) {
	buffer := make([]byte, dnswire.MaxMessageSize)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(DefaultDNSUDPIdleTimeout))
		n, err := conn.Read(buffer)
		if err != nil {
			return
		}

		wireQuery := append([]byte(nil), buffer[:n]...)
		query, rcode := validateDNSQuery(wireQuery)
		var wireResponse []byte
		if query == nil {
			wireResponse = packDNSFailure(wireQuery, nil, rcode)
		} else if !s.acquireDNS() {
			wireResponse = packDNSFailure(wireQuery, query, dns.RcodeServerFailure)
		} else {
			wireResponse = s.exchangeDNS(wireQuery, query, proto.Network_UDP)
			s.releaseDNS()
		}

		_ = conn.SetWriteDeadline(time.Now().Add(s.cfg.DNS.QueryTimeout))
		n, err = conn.Write(wireResponse)
		if err != nil || n != len(wireResponse) {
			return
		}
	}
}

func (s *Service) serveTCPDNS(conn net.Conn) {
	connectionCtx, cancel := context.WithCancel(s.ctx)
	defer cancel()

	// Counted for the whole session so the DNS pool is divided between the
	// connections actually contending for it.
	s.dnsConnections.Add(1)
	defer s.dnsConnections.Add(-1)

	var (
		pending   sync.WaitGroup
		writeMu   sync.Mutex
		abortOnce sync.Once
		readSize  [2]byte
	)
	// Pipelining lets one connection hold every global slot, which answers all
	// other clients behind the TUN with SERVFAIL until its RPCs drain. This
	// second bound limits how much of the shared pool any one connection can
	// hold at once. It is a per-connection ceiling, not a reservation: several
	// busy connections can still fill the pool between them, which is what the
	// global bound is for.
	perConnection := s.cfg.DNS.MaxInFlightPerConnection
	if perConnection <= 0 || perConnection > cap(s.dnsSlots) {
		// NormalizeConfig guarantees a usable value; fall back for a Service
		// assembled directly, so an unset field cannot mean "serve no DNS".
		// The global bound still applies either way.
		perConnection = max(1, cap(s.dnsSlots))
	}
	connectionSlots := make(chan struct{}, perConnection)
	// acquireExchange reserves both bounds. Reaching the per-connection ceiling
	// applies backpressure rather than failing: the reader waits for one of this
	// connection's own queries to finish, which stops it consuming the socket
	// and lets TCP flow control slow the client down. That wait is self-limiting
	// because those queries are bounded by QueryTimeout, and it avoids failing a
	// client while the shared pool still has room. The global bound never waits
	// — that would stall this reader on other connections' work — so a genuinely
	// exhausted pool still answers SERVFAIL.
	acquireExchange := func() (acquired, aborted bool) {
		// The ceiling moves as connections come and go, and rises when one
		// leaves, so the wait re-evaluates it rather than parking on the channel.
		// The poll interval is irrelevant beside a network round trip and only
		// applies while this connection is already at its ceiling.
		const reevaluate = 5 * time.Millisecond
		reserve := func() bool {
			return len(connectionSlots) < s.fairDNSShare(perConnection) &&
				tryAcquireSlot(connectionSlots)
		}
		for !reserve() {
			select {
			case <-connectionCtx.Done():
				return false, true
			case <-time.After(reevaluate):
			}
		}
		if !s.acquireDNS() {
			releaseSlot(connectionSlots)
			return false, false
		}
		return true, false
	}
	releaseExchange := func() {
		s.releaseDNS()
		releaseSlot(connectionSlots)
	}
	abort := func() {
		abortOnce.Do(func() {
			cancel()
			_ = conn.Close()
		})
	}
	writeResponse := func(wire []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if len(wire) == 0 || len(wire) > dnswire.MaxMessageSize {
			return errors.New("tun: invalid DNS response size")
		}
		frame := make([]byte, 2+len(wire))
		binary.BigEndian.PutUint16(
			frame[:2],
			uint16(len(wire)), // #nosec G115 -- wire length is bounded to 1..65535 above.
		)
		copy(frame[2:], wire)
		_ = conn.SetWriteDeadline(time.Now().Add(s.cfg.DNS.QueryTimeout))
		return writeAll(conn, frame)
	}

	for {
		_ = conn.SetReadDeadline(time.Now().Add(s.cfg.DNS.TCPIdleTimeout))
		if _, err := io.ReadFull(conn, readSize[:]); err != nil {
			if !errors.Is(err, io.EOF) {
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() {
					abort()
				}
			}
			break
		}

		messageLength := int(binary.BigEndian.Uint16(readSize[:]))
		if messageLength == 0 {
			abort()
			break
		}
		wireQuery := make([]byte, messageLength)
		if _, err := io.ReadFull(conn, wireQuery); err != nil {
			abort()
			break
		}

		query, rcode := validateDNSQuery(wireQuery)
		if query == nil {
			if err := writeResponse(packDNSFailure(wireQuery, nil, rcode)); err != nil {
				abort()
				break
			}
			continue
		}
		acquired, aborted := acquireExchange()
		if aborted {
			break
		}
		if !acquired {
			if err := writeResponse(packDNSFailure(wireQuery, query, dns.RcodeServerFailure)); err != nil {
				abort()
				break
			}
			continue
		}

		// DNS-over-TCP permits pipelining and out-of-order responses. Each RPC is
		// bounded both globally and per connection, while writeMu keeps each
		// length-prefixed response atomic on the byte stream.
		pending.Add(1)
		go func(wire []byte, parsed *dns.Msg) {
			defer pending.Done()
			defer releaseExchange()

			response := s.exchangeDNSContext(connectionCtx, wire, parsed, proto.Network_TCP)
			if err := writeResponse(response); err != nil {
				abort()
			}
		}(wireQuery, query)
	}

	pending.Wait()
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		n, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}

func validateDNSQuery(wire []byte) (*dns.Msg, int) {
	query, err := dnswire.ParseQuery(wire)
	if err == nil {
		return query, dns.RcodeSuccess
	}
	return nil, dnswire.QueryErrorRcode(err)
}

func (s *Service) exchangeDNS(wire []byte, query *dns.Msg, network proto.Network) []byte {
	return s.exchangeDNSContext(s.ctx, wire, query, network)
}

func (s *Service) exchangeDNSContext(parent context.Context, wire []byte, query *dns.Msg, network proto.Network) []byte {
	ctx, cancel := context.WithTimeout(parent, s.cfg.DNS.QueryTimeout)
	defer cancel()

	response, err := s.exchanger.Exchange(ctx, wire, network, s.cfg.DNS.BlockIPv6)
	if err != nil {
		return packDNSFailure(wire, query, dns.RcodeServerFailure)
	}
	return response
}

func packDNSFailure(wire []byte, parsed *dns.Msg, rcode int) []byte {
	query := parsed
	if query == nil {
		candidate := new(dns.Msg)
		if err := candidate.Unpack(wire); err == nil {
			query = candidate
		} else {
			query = new(dns.Msg)
			if len(wire) >= 2 {
				query.Id = binary.BigEndian.Uint16(wire[:2])
			}
		}
	}

	response := dnswire.ErrorResponse(query, rcode)
	packed, err := response.Pack()
	if err == nil && len(packed) != 0 && len(packed) <= dnswire.MaxMessageSize {
		return packed
	}

	// Packing can only fail if a malformed request left unusable data in a
	// partially decoded Msg. Fall back to a minimal header with the same ID.
	minimal := new(dns.Msg)
	minimal.Id = query.Id
	minimal.Response = true
	minimal.RecursionAvailable = true
	minimal.Rcode = rcode
	packed, _ = minimal.Pack()
	return packed
}
