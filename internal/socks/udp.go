package socks

import (
	"errors"
	"fmt"
	"log"
	"net"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"golang.org/x/sync/singleflight"
)

// UDP relay constants.
const (
	udpMaxPacketSize = 65535
	udpIdleTimeout   = 2 * time.Minute

	// udpMaxHeaderLen is the largest SOCKS5 UDP response header the reverse path
	// can produce: RSV(2)+FRAG(1)+ATYP(1)+LEN(1)+FQDN(255)+PORT(2).
	udpMaxHeaderLen = 262

	// udpJobQueueSize bounds datagrams buffered between the single reader and
	// the worker pool. When full, further datagrams are dropped (correct UDP
	// behavior under overload) so the reader keeps draining the socket.
	udpJobQueueSize = 2048
)

// udpWorkerCount is the size of the per-relay worker pool that processes
// inbound datagrams. Bounded so a traffic burst cannot spawn an unbounded
// number of goroutines (the previous goroutine-per-packet model).
var udpWorkerCount = max(4, runtime.NumCPU())

var (
	ErrUDPFragmentation = errors.New("socks5: fragmented UDP packets not supported")
	ErrUDPMalformed     = errors.New("socks5: malformed UDP request header")
	ErrUDPFQDNTooLong   = errors.New("socks5: UDP FQDN exceeds 255 bytes")

	udpBufferPool = sync.Pool{
		New: func() any {
			return new(make([]byte, udpMaxPacketSize+udpMaxHeaderLen))
		},
	}
)

// UDPHeader represents the SOCKS5 UDP request header (RFC 1928 §7).
//
//	+------+------+------+----------+----------+
//	| RSV  | FRAG | ATYP | DST.ADDR | DST.PORT |
//	+------+------+------+----------+----------+
//	|  2   |  1   |  1   | Variable |    2     |
//	+------+------+------+----------+----------+
type UDPHeader struct {
	Frag       uint8
	Addr       *AddrSpec
	DataOffset int // byte index where the actual payload begins
}

// ParseUDPHeader parses a SOCKS5 UDP request header from raw bytes.
func ParseUDPHeader(buf []byte) (*UDPHeader, error) {
	if len(buf) < 4 {
		return nil, ErrUDPMalformed
	}
	if buf[0] != 0 || buf[1] != 0 {
		return nil, ErrUDPMalformed
	}

	// RSV (2 bytes, must be 0) + FRAG (1 byte)
	frag := buf[2]
	off := 3
	addr := &AddrSpec{}

	switch buf[off] {
	case ipv4Address:
		off++
		if len(buf) < off+4+2 {
			return nil, ErrUDPMalformed
		}
		addr.IP = make(net.IP, 4)
		copy(addr.IP, buf[off:off+4])
		off += 4
	case ipv6Address:
		off++
		if len(buf) < off+16+2 {
			return nil, ErrUDPMalformed
		}
		addr.IP = make(net.IP, 16)
		copy(addr.IP, buf[off:off+16])
		off += 16
	case fqdnAddress:
		off++
		if len(buf) < off+1 {
			return nil, ErrUDPMalformed
		}
		fqdnLen := int(buf[off])
		off++
		if len(buf) < off+fqdnLen+2 {
			return nil, ErrUDPMalformed
		}
		addr.FQDN = string(buf[off : off+fqdnLen])
		off += fqdnLen
	default:
		return nil, ErrUnrecognizedAddrType
	}

	if len(buf) < off+2 {
		return nil, ErrUDPMalformed
	}
	addr.Port = uint16(buf[off])<<8 | uint16(buf[off+1])
	off += 2

	return &UDPHeader{Frag: frag, Addr: addr, DataOffset: off}, nil
}

// MarshalUDPHeader builds a SOCKS5 UDP response header for the given address.
func MarshalUDPHeader(addr *AddrSpec) ([]byte, error) {
	if addr == nil {
		return nil, fmt.Errorf("socks5: cannot marshal UDP header: nil address")
	}

	var addrBytes []byte
	var atyp uint8

	if v4 := addr.IP.To4(); v4 != nil {
		atyp = ipv4Address
		addrBytes = v4
	} else if v6 := addr.IP.To16(); v6 != nil {
		atyp = ipv6Address
		addrBytes = v6
	} else if addr.FQDN != "" {
		if len(addr.FQDN) > 255 {
			return nil, ErrUDPFQDNTooLong
		}
		atyp = fqdnAddress
		addrBytes = append([]byte{byte(len(addr.FQDN))}, []byte(addr.FQDN)...)
	} else {
		return nil, fmt.Errorf("socks5: cannot marshal UDP header: invalid address")
	}

	header := make([]byte, 0, 4+len(addrBytes)+2)
	header = append(header, 0, 0, 0) // RSV(2) + FRAG(1)
	header = append(header, atyp)
	header = append(header, addrBytes...)
	header = append(header, byte(addr.Port>>8), byte(addr.Port&0xff))
	return header, nil
}

type domainAddr string

func (a domainAddr) Network() string { return "udp" }
func (a domainAddr) String() string  { return string(a) }

// natEntry tracks a single outbound UDP socket for a target address.
type natEntry struct {
	conn       net.PacketConn
	targetAddr net.Addr
	route      transport.Transport
	lastSeen   atomic.Int64 // unix nanoseconds
	closeOnce  sync.Once
}

func (e *natEntry) Close() {
	e.closeOnce.Do(func() {
		if e.conn != nil {
			_ = e.conn.Close()
		}
		if e.route != nil {
			_ = e.route.Close()
		}
	})
}

// udpPacket is a unit of work handed from the relay's reader to a worker.
// bufPtr is a pooled buffer owned by the worker until it returns it to the pool.
type udpPacket struct {
	bufPtr     *[]byte
	n          int
	clientAddr net.Addr
}

// UDPRelay relays UDP packets between a SOCKS5 client and arbitrary targets.
type UDPRelay struct {
	relay     net.PacketConn // client-facing UDP socket
	clientIP  net.IP         // allowed client IP (from TCP auth)
	natTable  sync.Map       // target addr → *natEntry
	dialGroup singleflight.Group
	jobs      chan udpPacket // reader → worker pool
	done      chan struct{}  // closed when relay is shutting down
	once      sync.Once
}

// NewUDPRelay creates a UDP relay bound to a random local port.
// clientIP restricts which IP is allowed to send datagrams.
func NewUDPRelay(clientIP net.IP) (*UDPRelay, error) {
	relayConn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		return nil, fmt.Errorf("socks5: udp relay listen: %w", err)
	}

	if uc, ok := relayConn.(*net.UDPConn); ok {
		// Increase buffer sizes for high-performance relaying.
		_ = uc.SetReadBuffer(16 * 1024 * 1024)
		_ = uc.SetWriteBuffer(16 * 1024 * 1024)
	}

	return &UDPRelay{
		relay:    relayConn,
		clientIP: clientIP,
		jobs:     make(chan udpPacket, udpJobQueueSize),
		done:     make(chan struct{}),
	}, nil
}

// RelayAddr returns the address the client should send UDP datagrams to.
func (r *UDPRelay) RelayAddr() net.Addr {
	return r.relay.LocalAddr()
}

// Run starts the relay loop. It blocks until Close is called or a fatal error occurs.
//
// A single goroutine drains the client-facing socket and dispatches each
// datagram to a bounded worker pool. This keeps the read path fast (so the
// kernel socket buffer doesn't back up) while capping the number of goroutines
// regardless of packet rate.
func (r *UDPRelay) Run() error {
	defer r.Close()

	var workers sync.WaitGroup
	workers.Add(udpWorkerCount)
	for range udpWorkerCount {
		go func() {
			defer workers.Done()
			for job := range r.jobs {
				r.handlePacket(job)
			}
		}()
	}

	err := r.readLoop()

	// readLoop is the only sender, so closing here is race-free; workers drain
	// any queued datagrams and exit.
	close(r.jobs)
	workers.Wait()
	return err
}

// readLoop drains the client-facing socket and dispatches datagrams to workers.
func (r *UDPRelay) readLoop() error {
	for {
		bufPtr := udpBufferPool.Get().(*[]byte)
		buf := *bufPtr

		// No read deadline: Close() closes the relay socket, which unblocks
		// ReadFrom with net.ErrClosed. Polling a periodic deadline here is both
		// unnecessary and harmful — once a deadline elapses, subsequent reads
		// with the same (now-past) deadline return immediately, busy-spinning.
		n, clientAddr, err := r.relay.ReadFrom(buf)
		if err != nil {
			udpBufferPool.Put(bufPtr)
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
				continue
			}
			return fmt.Errorf("socks5: udp relay read: %w", err)
		}

		select {
		case r.jobs <- udpPacket{bufPtr: bufPtr, n: n, clientAddr: clientAddr}:
		default:
			// Workers saturated and the queue is full: drop the datagram rather
			// than block the reader. Loss under overload is acceptable for UDP
			// and keeps the socket draining.
			udpBufferPool.Put(bufPtr)
		}
	}
}

// handlePacket processes a single inbound datagram: validates the source,
// parses the SOCKS5 UDP header, and forwards the payload to the target.
func (r *UDPRelay) handlePacket(job udpPacket) {
	defer udpBufferPool.Put(job.bufPtr)
	buf := (*job.bufPtr)[:job.n]

	// Verify client IP matches the authenticated client.
	udpAddr, ok := job.clientAddr.(*net.UDPAddr)
	if !ok {
		return
	}
	if r.clientIP != nil && !r.clientIP.IsUnspecified() && !r.clientIP.Equal(udpAddr.IP) {
		log.Printf("socks5: udp: rejected datagram from %s (expected %s)", udpAddr.IP, r.clientIP)
		return
	}

	// Parse SOCKS5 UDP header.
	header, err := ParseUDPHeader(buf)
	if err != nil {
		log.Printf("socks5: udp: malformed header: %v", err)
		return
	}

	// Reject fragmented packets (common practice).
	if header.Frag != 0 {
		return
	}

	payload := buf[header.DataOffset:]
	targetAddr := header.Addr.Address()

	// Get or create outbound connection for this target.
	entry, err := r.getOrCreateNAT(targetAddr, job.clientAddr)
	if err != nil {
		log.Printf("socks5: udp: dial %s failed: %v", targetAddr, err)
		return
	}

	nw, err := entry.conn.WriteTo(payload, entry.targetAddr)
	if err != nil {
		log.Printf("socks5: udp: write to %s: %v", targetAddr, err)
		return
	}
	entry.lastSeen.Store(time.Now().UnixNano())
	transport.GlobalStats.AddTx(uint64(nw))
}

func (r *UDPRelay) getOrCreateNAT(targetAddr string, clientAddr net.Addr) (*natEntry, error) {
	if val, ok := r.natTable.Load(targetAddr); ok {
		return val.(*natEntry), nil
	}

	res, err, _ := r.dialGroup.Do(targetAddr, func() (any, error) {
		// Double check after singleflight block
		if val, ok := r.natTable.Load(targetAddr); ok {
			return val.(*natEntry), nil
		}

		host, _, err := net.SplitHostPort(targetAddr)
		if err != nil {
			host = targetAddr // fallback
		}

		route, err := router.GetRoute(host)
		if err != nil {
			return nil, fmt.Errorf("route error: %w", err)
		}

		// The egress must support packet dialing. Do NOT silently fall back to a
		// local UDP socket: for a proxied egress that would bypass the tunnel,
		// leaking the client's real IP and resolving DNS locally.
		pd, ok := route.(transport.PacketDialer)
		if !ok {
			_ = route.Close()
			return nil, fmt.Errorf("egress %s does not support UDP", route)
		}
		outbound, err := pd.DialPacket("udp", targetAddr)
		if err != nil {
			_ = route.Close()
			return nil, fmt.Errorf("dial packet: %w", err)
		}

		var resolved net.Addr
		if _, isLocal := outbound.(*net.UDPConn); isLocal {
			resolved, err = net.ResolveUDPAddr("udp", targetAddr)
			if err != nil {
				_ = outbound.Close()
				_ = route.Close()
				return nil, fmt.Errorf("resolve %s: %w", targetAddr, err)
			}
		} else {
			// For proxy connections, do not resolve DNS locally to prevent leaks.
			resolved = domainAddr(targetAddr)
		}

		entry := &natEntry{conn: outbound, targetAddr: resolved, route: route}
		entry.lastSeen.Store(time.Now().UnixNano())

		r.natTable.Store(targetAddr, entry)

		// Re-check shutdown AFTER storing. This closes the race where Close()
		// ranges the NAT table between our check and the Store: if done is now
		// closed, Close() may have missed this entry, so we clean it up here.
		select {
		case <-r.done:
			r.natTable.Delete(targetAddr)
			entry.Close()
			return nil, net.ErrClosed
		default:
		}

		// Start reverse relay goroutine: target → client.
		go r.reverseRelay(entry, clientAddr, targetAddr)

		return entry, nil
	})

	if err != nil {
		return nil, err
	}
	return res.(*natEntry), nil
}

// reverseRelay reads responses from the target and sends them back to the SOCKS5
// client with a proper UDP header prepended.
func (r *UDPRelay) reverseRelay(entry *natEntry, clientAddr net.Addr, targetAddr string) {
	defer func() {
		entry.Close()
		r.natTable.Delete(targetAddr)
	}()
	outbound := entry.conn

	bufPtr := udpBufferPool.Get().(*[]byte)
	defer udpBufferPool.Put(bufPtr)
	buf := *bufPtr

	for {
		_ = outbound.SetReadDeadline(time.Now().Add(udpIdleTimeout))

		// Reserve udpMaxHeaderLen bytes at the front so the SOCKS5 header can be
		// written in place ahead of the payload — no second buffer, no large
		// memcpy, and no risk of overflowing the buffer when prepending.
		n, respAddr, err := outbound.ReadFrom(buf[udpMaxHeaderLen:])
		if err != nil {
			select {
			case <-r.done:
				return
			default:
			}

			if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
				// check if we've been active in the other direction recently
				if val, ok := r.natTable.Load(targetAddr); ok {
					entry := val.(*natEntry)
					lastSeen := time.Unix(0, entry.lastSeen.Load())
					if time.Since(lastSeen) < udpIdleTimeout {
						continue // keep alive
					}
				}
				return // truly idle
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("socks5: udp reverse relay %s: %v", targetAddr, err)
			return
		}

		respSpec, ok := addrSpecFromNetAddr(respAddr)
		if !ok {
			continue
		}

		header, err := MarshalUDPHeader(respSpec)
		if err != nil {
			log.Printf("socks5: udp reverse relay: marshal header: %v", err)
			continue
		}
		if len(header)+n > udpMaxPacketSize {
			log.Printf("socks5: udp reverse relay: response too large: header=%d payload=%d", len(header), n)
			continue
		}

		if val, ok := r.natTable.Load(targetAddr); ok {
			entry := val.(*natEntry)
			entry.lastSeen.Store(time.Now().UnixNano())
		}

		// Write the header into the reserved region immediately before the
		// payload, then send the contiguous [header || payload] slice.
		start := udpMaxHeaderLen - len(header)
		copy(buf[start:udpMaxHeaderLen], header)

		nw, err := r.relay.WriteTo(buf[start:udpMaxHeaderLen+n], clientAddr)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("socks5: udp reverse relay: write to client: %v", err)
			continue
		}
		transport.GlobalStats.AddRx(uint64(nw))
	}
}

// Close shuts down the UDP relay and all outbound connections.
func (r *UDPRelay) Close() error {
	r.once.Do(func() {
		close(r.done)
		_ = r.relay.Close()

		// Close all active NAT entries.
		r.natTable.Range(func(key, value any) bool {
			entry := value.(*natEntry)
			entry.Close()
			return true
		})
	})
	return nil
}

func addrSpecFromNetAddr(addr net.Addr) (*AddrSpec, bool) {
	if addr == nil {
		return nil, false
	}

	switch v := addr.(type) {
	case *net.UDPAddr:
		if v == nil || v.Port < 0 || v.Port > 65535 {
			return nil, false
		}
		return &AddrSpec{IP: v.IP, Port: uint16(v.Port)}, true
	}

	host, portStr, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil, false
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, false
	}
	if ip := net.ParseIP(host); ip != nil {
		return &AddrSpec{IP: ip, Port: uint16(port)}, true
	}
	return &AddrSpec{FQDN: host, Port: uint16(port)}, true
}
