package socks

import (
	"errors"
	"fmt"
	"log"
	"net"
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
	udpReadTimeout   = 30 * time.Second
)

var (
	ErrUDPFragmentation = errors.New("socks5: fragmented UDP packets not supported")
	ErrUDPMalformed     = errors.New("socks5: malformed UDP request header")

	udpBufferPool = sync.Pool{
		New: func() any {
			b := make([]byte, udpMaxPacketSize)
			return &b
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
	var addrBytes []byte
	var atyp uint8

	if v4 := addr.IP.To4(); v4 != nil {
		atyp = ipv4Address
		addrBytes = v4
	} else if v6 := addr.IP.To16(); v6 != nil {
		atyp = ipv6Address
		addrBytes = v6
	} else if addr.FQDN != "" {
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
	lastSeen   atomic.Int64 // unix nanoseconds
}

// UDPRelay relays UDP packets between a SOCKS5 client and arbitrary targets.
type UDPRelay struct {
	relay     net.PacketConn // client-facing UDP socket
	clientIP  net.IP         // allowed client IP (from TCP auth)
	natTable  sync.Map       // target addr → *natEntry
	dialGroup singleflight.Group
	done      chan struct{} // closed when relay is shutting down
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
		done:     make(chan struct{}),
	}, nil
}

// RelayAddr returns the address the client should send UDP datagrams to.
func (r *UDPRelay) RelayAddr() net.Addr {
	return r.relay.LocalAddr()
}

// Run starts the relay loop. It blocks until Close is called or a fatal error occurs.
func (r *UDPRelay) Run() error {
	defer r.Close()

	var i int
	for {
		bufPtr := udpBufferPool.Get().(*[]byte)
		buf := *bufPtr

		if i%100 == 0 {
			_ = r.relay.SetReadDeadline(time.Now().Add(60 * time.Second))
		}
		i++

		n, clientAddr, err := r.relay.ReadFrom(buf)
		if err != nil {
			udpBufferPool.Put(bufPtr)
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				select {
				case <-r.done:
					return nil
				default:
					continue
				}
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("socks5: udp relay read: %w", err)
		}

		go func(bufPtr *[]byte, n int, clientAddr net.Addr) {
			defer udpBufferPool.Put(bufPtr)
			buf := *bufPtr

			// Verify client IP matches the authenticated client.
			udpAddr, ok := clientAddr.(*net.UDPAddr)
			if !ok {
				return
			}
			if r.clientIP != nil && !r.clientIP.IsUnspecified() && !r.clientIP.Equal(udpAddr.IP) {
				log.Printf("socks5: udp: rejected datagram from %s (expected %s)", udpAddr.IP, r.clientIP)
				return
			}

			// Parse SOCKS5 UDP header.
			header, err := ParseUDPHeader(buf[:n])
			if err != nil {
				log.Printf("socks5: udp: malformed header: %v", err)
				return
			}

			// Reject fragmented packets (common practice).
			if header.Frag != 0 {
				return
			}

			payload := buf[header.DataOffset:n]
			targetAddr := header.Addr.Address()

			// Get or create outbound connection for this target.
			entry, err := r.getOrCreateNAT(targetAddr, clientAddr)
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
		}(bufPtr, n, clientAddr)
	}
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

		var outbound net.PacketConn

		if pd, ok := route.(transport.PacketDialer); ok {
			outbound, err = pd.DialPacket("udp", targetAddr)
			if err != nil {
				return nil, fmt.Errorf("dial packet: %w", err)
			}
		} else {
			// Fallback to direct local UDP if transport doesn't support PacketDialer.
			outbound, err = net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				return nil, err
			}
		}

		var resolved net.Addr
		if _, isLocal := outbound.(*net.UDPConn); isLocal {
			resolved, err = net.ResolveUDPAddr("udp", targetAddr)
			if err != nil {
				_ = outbound.Close()
				return nil, fmt.Errorf("resolve %s: %w", targetAddr, err)
			}
		} else {
			// For proxy connections, do not resolve DNS locally to prevent leaks.
			resolved = domainAddr(targetAddr)
		}

		entry := &natEntry{conn: outbound, targetAddr: resolved}
		entry.lastSeen.Store(time.Now().UnixNano())

		select {
		case <-r.done:
			_ = outbound.Close()
			return nil, net.ErrClosed
		default:
		}
		r.natTable.Store(targetAddr, entry)

		// Start reverse relay goroutine: target → client.
		go r.reverseRelay(outbound, clientAddr, targetAddr)

		return entry, nil
	})

	if err != nil {
		return nil, err
	}
	return res.(*natEntry), nil
}

// reverseRelay reads responses from the target and sends them back to the SOCKS5
// client with a proper UDP header prepended.
func (r *UDPRelay) reverseRelay(outbound net.PacketConn, clientAddr net.Addr, targetAddr string) {
	defer func() {
		_ = outbound.Close()
		r.natTable.Delete(targetAddr)
	}()

	bufPtr := udpBufferPool.Get().(*[]byte)
	defer udpBufferPool.Put(bufPtr)
	buf := *bufPtr

	for {
		_ = outbound.SetReadDeadline(time.Now().Add(udpIdleTimeout))

		n, respAddr, err := outbound.ReadFrom(buf)
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

		respUDP, ok := respAddr.(*net.UDPAddr)
		if !ok {
			continue
		}

		respSpec := &AddrSpec{IP: respUDP.IP, Port: uint16(respUDP.Port)}
		header, err := MarshalUDPHeader(respSpec)
		if err != nil {
			log.Printf("socks5: udp reverse relay: marshal header: %v", err)
			continue
		}

		if val, ok := r.natTable.Load(targetAddr); ok {
			entry := val.(*natEntry)
			entry.lastSeen.Store(time.Now().UnixNano())
		}

		// Use a second buffer for the response to avoid allocations.
		outBufPtr := udpBufferPool.Get().(*[]byte)
		outBuf := *outBufPtr

		copy(outBuf, header)
		copy(outBuf[len(header):], buf[:n])

		nw, err := r.relay.WriteTo(outBuf[:len(header)+n], clientAddr)
		udpBufferPool.Put(outBufPtr)
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
			_ = entry.conn.Close()
			return true
		})
	})
	return nil
}
