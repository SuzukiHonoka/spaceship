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
	udpSocketBuffer  = 1 * 1024 * 1024

	// udpMaxHeaderLen is the largest SOCKS5 UDP response header the reverse path
	// can produce: RSV(2)+FRAG(1)+ATYP(1)+LEN(1)+FQDN(255)+PORT(2).
	udpMaxHeaderLen = 262

	// udpJobQueueSize bounds datagrams buffered between the single reader and
	// the worker pool. When full, further datagrams are dropped (correct UDP
	// behavior under overload) so the reader keeps draining the socket.
	udpJobQueueSize = 64
)

// Default resource limits.
//
// Memory note: every active NAT entry owns a reverse-relay goroutine holding a
// udpMaxPacketSize buffer (~64KB) for the flow's lifetime. The buffer is kept at
// full datagram size deliberately — a short read on a UDP socket truncates and
// discards the remainder, so a smaller buffer would silently corrupt any
// oversized datagram rather than merely delaying it.
//
// That makes the global NAT cap the dominant memory term:
//
//	defaultMaxNATEntriesGlobal × ~64KB ≈ 32MB worst case
//
// Operators who need more concurrent flows, or less memory, should tune the udp
// config section rather than assume these values fit their workload.
//
// The per-client cap must stay at or above the per-association cap, otherwise a
// single association could not reach its own limit.
const (
	// defaultMaxNATEntries bounds outbound sockets owned by one association.
	// Creating a flow per unique destination is otherwise an unbounded
	// resource-allocation primitive controlled by the client.
	defaultMaxNATEntries = 64

	// Process-wide and per-client limits bound client-facing sockets, worker
	// goroutines, outbound sockets, and reverse-relay buffers.
	defaultMaxAssociations          = 64
	defaultMaxAssociationsPerClient = 8
	defaultMaxNATEntriesGlobal      = 512
	defaultMaxNATEntriesPerClient   = 256
)

// UDPSettings tunes the SOCKS5 UDP ASSOCIATE relay. A zero value in any numeric
// field selects the built-in default, so a partially populated struct is valid.
type UDPSettings struct {
	// Disable makes UDP ASSOCIATE report "command not supported", which lets
	// clients cleanly fall back to TCP instead of establishing an association
	// that cannot carry traffic.
	Disable bool
	// MaxAssociations bounds concurrent associations process-wide;
	// MaxAssociationsPerClient bounds them for a single client IP.
	MaxAssociations          int
	MaxAssociationsPerClient int
	// MaxNATEntries bounds outbound sockets per association. MaxNATEntriesGlobal
	// and MaxNATEntriesPerClient bound them process-wide and per client IP.
	MaxNATEntries          int
	MaxNATEntriesGlobal    int
	MaxNATEntriesPerClient int
}

var (
	udpSettingsMu sync.RWMutex
	udpDisabled   bool
	udpMaxNAT     = defaultMaxNATEntries

	udpAssociationLimiter = newUDPResourceLimiter(defaultMaxAssociations, defaultMaxAssociationsPerClient)
	udpNATLimiter         = newUDPResourceLimiter(defaultMaxNATEntriesGlobal, defaultMaxNATEntriesPerClient)
)

func orDefault(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// SetUDPSettings applies UDP relay settings. It replaces the process-wide
// limiters, so it must be called during startup before any association exists —
// counts held by live associations would otherwise be lost.
func SetUDPSettings(s UDPSettings) {
	udpSettingsMu.Lock()
	defer udpSettingsMu.Unlock()

	udpDisabled = s.Disable
	udpMaxNAT = orDefault(s.MaxNATEntries, defaultMaxNATEntries)
	udpAssociationLimiter = newUDPResourceLimiter(
		orDefault(s.MaxAssociations, defaultMaxAssociations),
		orDefault(s.MaxAssociationsPerClient, defaultMaxAssociationsPerClient),
	)
	udpNATLimiter = newUDPResourceLimiter(
		orDefault(s.MaxNATEntriesGlobal, defaultMaxNATEntriesGlobal),
		orDefault(s.MaxNATEntriesPerClient, defaultMaxNATEntriesPerClient),
	)
}

// UDPDisabled reports whether UDP ASSOCIATE is turned off by config.
func UDPDisabled() bool {
	udpSettingsMu.RLock()
	defer udpSettingsMu.RUnlock()
	return udpDisabled
}

// udpConfig reads the whole settings snapshot under one lock, so a concurrent
// SetUDPSettings cannot interleave between the disabled check and the limiters
// a new relay is built from.
func udpConfig() (disabled bool, associations, nat *udpResourceLimiter, maxNAT int) {
	udpSettingsMu.RLock()
	defer udpSettingsMu.RUnlock()
	return udpDisabled, udpAssociationLimiter, udpNATLimiter, udpMaxNAT
}

func udpLimiters() (associations, nat *udpResourceLimiter, maxNAT int) {
	_, associations, nat, maxNAT = udpConfig()
	return associations, nat, maxNAT
}

// udpWorkerCount is the size of the per-relay worker pool that processes
// inbound datagrams. Bounded so a traffic burst cannot spawn an unbounded
// number of goroutines (the previous goroutine-per-packet model).
var udpWorkerCount = min(8, max(2, runtime.GOMAXPROCS(0)))

var (
	ErrUDPFragmentation    = errors.New("socks5: fragmented UDP packets not supported")
	ErrUDPMalformed        = errors.New("socks5: malformed UDP request header")
	ErrUDPFQDNTooLong      = errors.New("socks5: UDP FQDN exceeds 255 bytes")
	ErrUDPAssociationLimit = errors.New("socks5: UDP association limit reached")
	ErrUDPNATLimit         = errors.New("socks5: UDP NAT limit reached")
	ErrUDPDisabled         = errors.New("socks5: UDP associate is disabled")

	udpBufferPool = sync.Pool{
		New: func() any {
			return new(make([]byte, udpMaxPacketSize+udpMaxHeaderLen))
		},
	}
)

// udpResourceLimiter enforces a process-wide limit and a fair per-client
// limit. Callers must release every successful acquisition exactly once.
type udpResourceLimiter struct {
	mu           sync.Mutex
	total        int
	byClient     map[string]int
	maxTotal     int
	maxPerClient int
}

func newUDPResourceLimiter(maxTotal, maxPerClient int) *udpResourceLimiter {
	return &udpResourceLimiter{
		byClient:     make(map[string]int),
		maxTotal:     maxTotal,
		maxPerClient: maxPerClient,
	}
}

func (l *udpResourceLimiter) acquire(client string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.maxTotal > 0 && l.total >= l.maxTotal {
		return false
	}
	if l.maxPerClient > 0 && l.byClient[client] >= l.maxPerClient {
		return false
	}
	l.total++
	l.byClient[client]++
	return true
}

func (l *udpResourceLimiter) release(client string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byClient[client] <= 0 || l.total <= 0 {
		return
	}
	l.total--
	l.byClient[client]--
	if l.byClient[client] == 0 {
		delete(l.byClient, client)
	}
}

func udpClientKey(ip net.IP) string {
	if ip == nil || ip.IsUnspecified() {
		return "unknown"
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

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
	limiter    *udpResourceLimiter
	clientKey  string
	lastSeen   atomic.Int64 // unix nanoseconds
	closeOnce  sync.Once
}

type natTable struct {
	mu      sync.RWMutex
	entries map[string]*natEntry
}

func (t *natTable) Load(key string) (*natEntry, bool) {
	t.mu.RLock()
	entry, ok := t.entries[key]
	t.mu.RUnlock()
	return entry, ok
}

func (t *natTable) Store(key string, entry *natEntry) {
	t.mu.Lock()
	if t.entries == nil {
		t.entries = make(map[string]*natEntry)
	}
	t.entries[key] = entry
	t.mu.Unlock()
}

// Install stores entry unless key already exists. When the table is at its
// limit, it removes the least recently used entry and returns it to the caller
// for closing outside the table lock.
func (t *natTable) Install(key string, entry *natEntry, limit int) (actual, evicted *natEntry, installed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.entries == nil {
		t.entries = make(map[string]*natEntry)
	}
	if current, ok := t.entries[key]; ok {
		return current, nil, false
	}

	if limit > 0 && len(t.entries) >= limit {
		var oldestKey string
		var oldest *natEntry
		for candidateKey, candidate := range t.entries {
			if oldest == nil || candidate.lastSeen.Load() < oldest.lastSeen.Load() {
				oldestKey = candidateKey
				oldest = candidate
			}
		}
		if oldest != nil {
			delete(t.entries, oldestKey)
			evicted = oldest
		}
	}

	t.entries[key] = entry
	return entry, evicted, true
}

func (t *natTable) DeleteIf(key string, expected *natEntry) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries[key] != expected {
		return false
	}
	delete(t.entries, key)
	return true
}

func (t *natTable) Range(fn func(string, *natEntry) bool) {
	t.mu.RLock()
	entries := make(map[string]*natEntry, len(t.entries))
	for key, entry := range t.entries {
		entries[key] = entry
	}
	t.mu.RUnlock()

	for key, entry := range entries {
		if !fn(key, entry) {
			return
		}
	}
}

func (t *natTable) Drain() []*natEntry {
	t.mu.Lock()
	entries := make([]*natEntry, 0, len(t.entries))
	for _, entry := range t.entries {
		entries = append(entries, entry)
	}
	t.entries = nil
	t.mu.Unlock()
	return entries
}

func (e *natEntry) Close() {
	e.closeOnce.Do(func() {
		if e.conn != nil {
			_ = e.conn.Close()
		}
		if e.route != nil {
			_ = e.route.Close()
		}
		e.limiter.release(e.clientKey)
	})
}

// udpPacket is a unit of work handed from the relay's reader to a worker.
type udpPacket struct {
	data       []byte
	clientAddr net.Addr
}

// UDPRelay relays UDP packets between a SOCKS5 client and arbitrary targets.
type UDPRelay struct {
	relay     net.PacketConn // client-facing UDP socket
	clientIP  net.IP         // allowed client IP (from TCP auth)
	natTable  natTable       // target addr → *natEntry
	dialGroup singleflight.Group
	jobs      chan udpPacket // reader → worker pool
	done      chan struct{}  // closed when relay is shutting down
	once      sync.Once

	maxNATEntries      int
	clientKey          string
	associationLimiter *udpResourceLimiter
	natLimiter         *udpResourceLimiter
	associationHeld    bool
	getRoute           func(string) (transport.Transport, error)
}

type udpListenFunc func(network, address string) (net.PacketConn, error)

// relayBindAddress picks the network and address for the client-facing socket.
//
// Binding the wildcard ":0" would expose a dual-stack socket on every interface,
// which is wider than the SOCKS listener the client actually reached. Binding
// the local address of that TCP control connection keeps the relay reachable
// exactly where the client already is, and pins the address family so replies
// are not black-holed on a dual-stack socket.
//
// This deliberately ignores transport.PreferIPv4. That setting governs egress to
// proxied destinations; the relay socket faces the local client, and forcing it
// to IPv4 for a client that reached us over IPv6 would bind a socket the client
// cannot reach while the SOCKS reply still advertises the IPv6 address.
func relayBindAddress(localIP net.IP) (network, address string) {
	if localIP != nil && !localIP.IsUnspecified() {
		if localIP.To4() != nil {
			return "udp4", net.JoinHostPort(localIP.String(), "0")
		}
		return "udp6", net.JoinHostPort(localIP.String(), "0")
	}
	// The control connection exposes no usable local IP — a unix-socket
	// listener, most commonly. Such a client is necessarily on this host, so
	// bind loopback rather than a wildcard: a wildcard socket reports an
	// unspecified BND.ADDR, and a client has nowhere to send datagrams to that.
	return "udp4", "127.0.0.1:0"
}

// NewUDPRelay creates a UDP relay bound to a random local port.
// clientIP restricts which IP is allowed to send datagrams; localIP is the local
// address of the TCP control connection and determines where the relay binds.
func NewUDPRelay(clientIP, localIP net.IP) (*UDPRelay, error) {
	disabled, associations, nat, maxNAT := udpConfig()
	if disabled {
		return nil, ErrUDPDisabled
	}
	return newUDPRelay(clientIP, localIP, associations, nat, maxNAT)
}

func newUDPRelay(clientIP, localIP net.IP, associationLimiter, natLimiter *udpResourceLimiter, maxNAT int) (*UDPRelay, error) {
	return newUDPRelayWithListener(clientIP, localIP, associationLimiter, natLimiter, maxNAT, net.ListenPacket)
}

func newUDPRelayWithListener(
	clientIP, localIP net.IP,
	associationLimiter, natLimiter *udpResourceLimiter,
	maxNAT int,
	listen udpListenFunc,
) (*UDPRelay, error) {
	clientKey := udpClientKey(clientIP)
	if !associationLimiter.acquire(clientKey) {
		return nil, ErrUDPAssociationLimit
	}

	network, bindAddr := relayBindAddress(localIP)
	relayConn, err := listen(network, bindAddr)
	if err != nil {
		associationLimiter.release(clientKey)
		return nil, fmt.Errorf("socks5: udp relay listen on %s: %w", bindAddr, err)
	}

	if uc, ok := relayConn.(*net.UDPConn); ok {
		_ = uc.SetReadBuffer(udpSocketBuffer)
		_ = uc.SetWriteBuffer(udpSocketBuffer)
	}

	return &UDPRelay{
		relay:              relayConn,
		clientIP:           clientIP,
		jobs:               make(chan udpPacket, udpJobQueueSize),
		done:               make(chan struct{}),
		maxNATEntries:      orDefault(maxNAT, defaultMaxNATEntries),
		clientKey:          clientKey,
		associationLimiter: associationLimiter,
		natLimiter:         natLimiter,
		associationHeld:    true,
		getRoute:           router.GetRoute,
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
	bufPtr := udpBufferPool.Get().(*[]byte)
	defer udpBufferPool.Put(bufPtr)
	buf := (*bufPtr)[:udpMaxPacketSize]

	for {
		// No read deadline: Close() closes the relay socket, which unblocks
		// ReadFrom with net.ErrClosed. Polling a periodic deadline here is both
		// unnecessary and harmful — once a deadline elapses, subsequent reads
		// with the same (now-past) deadline return immediately, busy-spinning.
		n, clientAddr, err := r.relay.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
				continue
			}
			return fmt.Errorf("socks5: udp relay read: %w", err)
		}

		// readLoop is the only sender. If capacity is available now, consumers
		// can only make more room before the non-blocking send below.
		if len(r.jobs) == cap(r.jobs) {
			continue
		}
		packet := udpPacket{
			data:       append([]byte(nil), buf[:n]...),
			clientAddr: clientAddr,
		}
		select {
		case r.jobs <- packet:
		default:
		}
	}
}

// handlePacket processes a single inbound datagram: validates the source,
// parses the SOCKS5 UDP header, and forwards the payload to the target.
func (r *UDPRelay) handlePacket(job udpPacket) {
	buf := job.data

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

// natKey identifies a flow. Both the client source address and the target
// belong in the key: a client may use several source ports on one association,
// and the reverse relay delivers to the address bound when the entry was
// created. Keying on the target alone would send every response to whichever
// source port happened to open the flow first.
func natKey(clientAddr net.Addr, targetAddr string) string {
	if clientAddr == nil {
		return targetAddr
	}
	return clientAddr.String() + "|" + targetAddr
}

func (r *UDPRelay) getOrCreateNAT(targetAddr string, clientAddr net.Addr) (*natEntry, error) {
	select {
	case <-r.done:
		return nil, net.ErrClosed
	default:
	}

	key := natKey(clientAddr, targetAddr)
	if entry, ok := r.natTable.Load(key); ok {
		return entry, nil
	}

	res, err, _ := r.dialGroup.Do(key, func() (any, error) {
		// Double check after singleflight block
		if entry, ok := r.natTable.Load(key); ok {
			return entry, nil
		}

		host, _, err := net.SplitHostPort(targetAddr)
		if err != nil {
			host = targetAddr // fallback
		}

		getRoute := r.getRoute
		if getRoute == nil {
			getRoute = router.GetRoute
		}
		route, err := getRoute(host)
		if err != nil {
			return nil, fmt.Errorf("route error: %w", err)
		}

		// The egress must support packet dialing. Do NOT silently fall back to a
		// local UDP socket: for a proxied egress that would bypass the tunnel,
		// leaking the client's real IP and resolving DNS locally.
		if _, ok := route.(transport.PacketDialer); !ok {
			_ = route.Close()
			return nil, fmt.Errorf("egress %s does not support UDP", route)
		}

		limiter := r.natLimiter
		if limiter == nil {
			_, limiter, _ = udpLimiters()
		}
		if !limiter.acquire(r.clientKey) {
			_ = route.Close()
			return nil, ErrUDPNATLimit
		}

		outbound, resolved, err := dialPacketTarget(route, "udp", targetAddr)
		if err != nil {
			limiter.release(r.clientKey)
			_ = route.Close()
			return nil, fmt.Errorf("dial packet: %w", err)
		}

		entry := &natEntry{
			conn:       outbound,
			targetAddr: resolved,
			route:      route,
			limiter:    limiter,
			clientKey:  r.clientKey,
		}
		entry.lastSeen.Store(time.Now().UnixNano())

		limit := orDefault(r.maxNATEntries, defaultMaxNATEntries)
		actual, evicted, installed := r.natTable.Install(key, entry, limit)
		if evicted != nil {
			evicted.Close()
		}
		if !installed {
			entry.Close()
			return actual, nil
		}

		// Re-check shutdown AFTER storing. This closes the race where Close()
		// ranges the NAT table between our check and the Store: if done is now
		// closed, Close() may have missed this entry, so we clean it up here.
		select {
		case <-r.done:
			r.natTable.DeleteIf(key, entry)
			entry.Close()
			return nil, net.ErrClosed
		default:
		}

		// Start reverse relay goroutine: target → client.
		go r.reverseRelay(entry, key, clientAddr, targetAddr)

		return entry, nil
	})

	if err != nil {
		return nil, err
	}
	return res.(*natEntry), nil
}

// dialPacketTarget uses an atomic resolve-and-bind operation when the transport
// supports it. Proxy transports receive the unresolved domain address so DNS
// resolution remains on the remote side.
func dialPacketTarget(route transport.Transport, network, targetAddr string) (net.PacketConn, net.Addr, error) {
	if targetDialer, ok := route.(transport.PacketTargetDialer); ok {
		conn, target, err := targetDialer.DialPacketTarget(network, targetAddr)
		if err != nil {
			if conn != nil {
				_ = conn.Close()
			}
			return nil, nil, err
		}
		if conn == nil {
			return nil, nil, errors.New("packet target dialer returned a nil connection")
		}
		if target == nil {
			_ = conn.Close()
			return nil, nil, errors.New("packet target dialer returned a nil target address")
		}
		return conn, target, nil
	}

	packetDialer, ok := route.(transport.PacketDialer)
	if !ok {
		return nil, nil, fmt.Errorf("egress %s does not support UDP", route)
	}
	conn, err := packetDialer.DialPacket(network, targetAddr)
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, nil, err
	}
	if conn == nil {
		return nil, nil, errors.New("packet dialer returned a nil connection")
	}
	return conn, domainAddr(targetAddr), nil
}

// reverseRelay reads responses from the target and sends them back to the SOCKS5
// client with a proper UDP header prepended.
func (r *UDPRelay) reverseRelay(entry *natEntry, key string, clientAddr net.Addr, targetAddr string) {
	defer func() {
		entry.Close()
		r.natTable.DeleteIf(key, entry)
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
				if current, ok := r.natTable.Load(key); ok && current == entry {
					lastSeen := time.Unix(0, current.lastSeen.Load())
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

		// Defense in depth against off-path injection. A connected socket (what
		// the direct transport hands back) already filters in the kernel, so this
		// only bites if a transport returns an unconnected socket — in which case
		// a spoofed datagram would otherwise be relayed to the client tagged with
		// the attacker's address.
		if !sourceMatchesTarget(respAddr, entry.targetAddr) {
			log.Printf("socks5: udp reverse relay %s: dropped datagram from unexpected source %s", targetAddr, respAddr)
			continue
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

		if current, ok := r.natTable.Load(key); ok && current == entry {
			current.lastSeen.Store(time.Now().UnixNano())
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
		if r.done != nil {
			close(r.done)
		}
		if r.relay != nil {
			_ = r.relay.Close()
		}

		// Close all active NAT entries.
		for _, entry := range r.natTable.Drain() {
			entry.Close()
		}
		if r.associationHeld {
			r.associationLimiter.release(r.clientKey)
			r.associationHeld = false
		}
	})
	return nil
}

// sourceMatchesTarget reports whether a datagram's source is the flow's target.
//
// Flows whose target is a domain (a proxied egress, where the remote side
// resolves the name and the tunnel itself authenticates the peer) carry no
// comparable IP, so they are accepted unconditionally.
func sourceMatchesTarget(src, target net.Addr) bool {
	srcUDP, ok := src.(*net.UDPAddr)
	if !ok {
		return true
	}
	targetUDP, ok := target.(*net.UDPAddr)
	if !ok {
		return true
	}
	return srcUDP.Port == targetUDP.Port && srcUDP.IP.Equal(targetUDP.IP)
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
