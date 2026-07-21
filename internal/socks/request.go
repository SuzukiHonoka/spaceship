package socks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"golang.org/x/sync/errgroup"
)

const (
	ConnectCommand   = uint8(1)
	BindCommand      = uint8(2)
	AssociateCommand = uint8(3)
	ipv4Address      = uint8(1)
	fqdnAddress      = uint8(3)
	ipv6Address      = uint8(4)
)

const (
	successReply uint8 = iota
	serverFailure
	ruleFailure
	networkUnreachable
	hostUnreachable
	connectionRefused
	ttlExpired
	commandNotSupported
	addrTypeNotSupported
)

var ErrUnrecognizedAddrType = errors.New("unrecognized address type")

// AddrSpec is used to return the target AddrSpec
// which may be specified as IPv4, IPv6, or a FQDN
type AddrSpec struct {
	FQDN string
	IP   net.IP
	Port uint16
}

func (a *AddrSpec) String() string {
	if a.FQDN != "" {
		return fmt.Sprintf("%s (%s):%d", a.FQDN, a.IP, a.Port)
	}
	return fmt.Sprintf("%s:%d", a.IP, a.Port)
}

// Address returns a string suitable to dial; prefer returning IP-based
// address, fallback to FQDN
func (a *AddrSpec) Address() string {
	if len(a.IP) != 0 {
		return net.JoinHostPort(a.IP.String(), strconv.Itoa(int(a.Port)))
	}
	return net.JoinHostPort(a.FQDN, strconv.Itoa(int(a.Port)))
}

// A Request represents request received by a server
type Request struct {
	// Protocol version
	Version uint8
	// Requested command
	Command uint8
	// AuthContext provided during negotiation
	AuthContext *AuthContext
	// AddrSpec of the network that sent the request
	RemoteAddr *AddrSpec
	// AddrSpec of the desired destination
	DestAddr *AddrSpec
	bufConn  io.Reader
}

type ConnWriter interface {
	Write([]byte) (int, error)
	RemoteAddr() net.Addr
}

// NewRequest creates a new Request from the tcp connection
func NewRequest(bufConn io.Reader) (*Request, error) {
	// Read the version byte
	var header [3]byte
	if _, err := io.ReadAtLeast(bufConn, header[:], 3); err != nil {
		return nil, fmt.Errorf("failed to get command version: %v", err)
	}

	// Ensure we are compatible
	if header[0] != socks5Version {
		return nil, fmt.Errorf("unsupported command version: %v", header[0])
	}

	// Read in the destination address
	dest, err := readAddrSpec(bufConn)
	if err != nil {
		return nil, err
	}

	request := &Request{
		Version:  socks5Version,
		Command:  header[1],
		DestAddr: dest,
		bufConn:  bufConn,
	}

	return request, nil
}

// handleRequest is used for request processing after authentication
func (s *Server) handleRequest(req *Request, conn ConnWriter) error {
	// Switch on the command
	switch req.Command {
	case ConnectCommand:
		return s.handleConnect(s.ctx, conn, req)
	case BindCommand:
		return s.handleBind(s.ctx, conn, req)
	case AssociateCommand:
		return s.handleAssociate(s.ctx, conn, req)
	default:
		if err := sendReply(conn, commandNotSupported, nil); err != nil {
			return fmt.Errorf("failed to send reply: %v", err)
		}
		return fmt.Errorf("unsupported command: %v", req.Command)
	}
}

// handleConnect is used to handle a connect command
func (s *Server) handleConnect(ctx context.Context, conn ConnWriter, req *Request) error {
	// set host dst
	host := req.DestAddr.FQDN
	if host == "" {
		host = req.DestAddr.IP.String()
	}

	route, err := router.GetRoute(host)
	if err != nil {
		log.Printf("socks: get route for %s error: %v", host, err)
		if err = sendReply(conn, ruleFailure, nil); err != nil {
			return fmt.Errorf("failed to send reply: %w", err)
		}
		return nil
	}
	defer utils.Close(route)

	log.Printf("socks: %s:%d -> %s", host, req.DestAddr.Port, route)

	// start proxy
	addr := net.JoinHostPort(host, strconv.FormatUint(uint64(req.DestAddr.Port), 10))
	errGroup, ctx := errgroup.WithContext(ctx)
	localAddr := make(chan string)
	errGroup.Go(func() error {
		return route.Proxy(ctx, addr, localAddr, conn, req.bufConn)
	})

	errGroup.Go(func() (err error) {
		local, ok := <-localAddr
		if !ok || local == "" {
			if err = sendReply(conn, networkUnreachable, nil); err != nil {
				return fmt.Errorf("failed to send reply: %v", err)
			}
			return fmt.Errorf("proxy handshake failed for %s", host)
		}

		// Send success
		ip, port, err := utils.SplitHostPort(local)
		if err != nil {
			return fmt.Errorf("failed to split host and port: %v", err)
		}
		bind := AddrSpec{IP: net.ParseIP(ip), Port: port}
		if err = sendReply(conn, successReply, &bind); err != nil {
			return fmt.Errorf("failed to send reply: %v", err)
		}
		//log.Printf("proxy local addr: %s\n", local)
		//log.Println("proxy local end")
		return nil
	})

	if err = errGroup.Wait(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		// One short line: host already logged on the route line above.
		log.Printf("socks: %s failed: %v", addr, err)
	}
	return nil
}

// handleBind is used to handle a bind command
func (s *Server) handleBind(_ context.Context, conn ConnWriter, _ *Request) error {
	// TODO: Support bind
	if err := sendReply(conn, commandNotSupported, nil); err != nil {
		return fmt.Errorf("failed to send reply: %v", err)
	}
	return nil
}

// handleAssociate is used to handle a UDP associate command (RFC 1928 §6).
// It binds a local UDP relay socket, sends the bound address back to the client,
// then monitors the TCP control connection — tearing down the relay when it closes.
func (s *Server) handleAssociate(ctx context.Context, conn ConnWriter, req *Request) error {
	// Extract the client IP for access control on the UDP relay.
	var clientIP net.IP
	if req.RemoteAddr != nil {
		clientIP = req.RemoteAddr.IP
	}

	// The local side of the TCP control connection tells us where the client
	// reached us, which is where the relay should bind.
	var localIP net.IP
	if tc, ok := conn.(interface{ LocalAddr() net.Addr }); ok {
		if tcpAddr, ok := tc.LocalAddr().(*net.TCPAddr); ok {
			localIP = tcpAddr.IP
		}
	}

	// Refuse up front when no association could carry traffic — disabled by
	// config, or no installed route has a UDP-capable egress (a forward proxy or
	// blackhole cannot dial packets). A client that receives "command not
	// supported" falls back to TCP; one handed a working association would wait
	// forever for datagrams we would drop at dial time.
	if UDPDisabled() || !router.AnyRouteSupportsUDP() {
		if err := sendReply(conn, commandNotSupported, nil); err != nil {
			return fmt.Errorf("failed to send reply: %w", err)
		}
		return nil
	}

	relay, err := NewUDPRelay(clientIP, localIP)
	if err != nil {
		reply := uint8(serverFailure)
		if errors.Is(err, ErrUDPDisabled) {
			reply = commandNotSupported
		}
		if sendErr := sendReply(conn, reply, nil); sendErr != nil {
			return fmt.Errorf("failed to send reply: %w", sendErr)
		}
		return fmt.Errorf("socks5: udp associate: %w", err)
	}

	// Parse the relay's bound address to build the SOCKS5 reply.
	relayAddr := relay.RelayAddr().(*net.UDPAddr)
	bind := &AddrSpec{IP: relayAddr.IP, Port: uint16(relayAddr.Port)}

	// If the relay still bound to an unspecified address, substitute the local
	// side of the TCP connection so the client knows where to send datagrams.
	if bind.IP.IsUnspecified() && localIP != nil {
		bind.IP = localIP
	}

	// An unspecified BND.ADDR is unusable — the client has nowhere to send
	// datagrams — so fail the associate rather than handing back an address that
	// silently swallows everything. Should be unreachable now that the relay
	// always binds a concrete address, but the failure mode is bad enough to be
	// worth an explicit guard.
	if bind.IP == nil || bind.IP.IsUnspecified() {
		_ = relay.Close()
		if sendErr := sendReply(conn, serverFailure, nil); sendErr != nil {
			return fmt.Errorf("failed to send reply: %w", sendErr)
		}
		return fmt.Errorf("socks5: udp associate: relay bound to an unusable address %s", relay.RelayAddr())
	}

	if err = sendReply(conn, successReply, bind); err != nil {
		_ = relay.Close()
		return fmt.Errorf("failed to send reply: %w", err)
	}

	log.Printf("socks5: udp associate relay started at %s for client %s", relay.RelayAddr(), clientIP)

	// Run the UDP relay in a goroutine.
	relayErr := make(chan error, 1)
	go func() {
		relayErr <- relay.Run()
	}()

	// Monitor the TCP control connection. Per RFC 1928, when the TCP connection
	// used to establish the UDP ASSOCIATE is closed, the UDP relay must stop.
	tcpClosed := make(chan struct{})
	go func() {
		defer close(tcpClosed)
		// Use io.Copy to efficiently discard any extraneous data sent by the client
		// and reliably detect connection closure (EOF) or errors.
		_, _ = io.Copy(io.Discard, req.bufConn)
	}()

	// Wait for either TCP close or relay error.
	select {
	case <-tcpClosed:
		log.Printf("socks5: udp associate: TCP control connection closed, tearing down relay")
		_ = relay.Close()
		<-relayErr
	case <-ctx.Done():
		log.Printf("socks5: udp associate: server context closed, tearing down relay")
		_ = relay.Close()
		<-relayErr
	case err = <-relayErr:
		if err != nil {
			log.Printf("socks5: udp associate: relay error: %v", err)
		}
	}

	return nil
}

// readAddrSpec is used to read AddrSpec.
// Expects an address type byte, followed by the address and port
func readAddrSpec(r io.Reader) (*AddrSpec, error) {
	d := &AddrSpec{}

	// Get the address type
	var addrType [1]byte
	if _, err := io.ReadFull(r, addrType[:]); err != nil {
		return nil, err
	}

	// Handle on a per-type basis
	switch addrType[0] {
	case ipv4Address:
		addr := make([]byte, 4)
		if _, err := io.ReadAtLeast(r, addr, len(addr)); err != nil {
			return nil, err
		}
		d.IP = addr

	case ipv6Address:
		addr := make([]byte, 16)
		if _, err := io.ReadAtLeast(r, addr, len(addr)); err != nil {
			return nil, err
		}
		d.IP = addr

	case fqdnAddress:
		if _, err := io.ReadFull(r, addrType[:]); err != nil {
			return nil, err
		}
		addrLen := int(addrType[0])
		fqdn := make([]byte, addrLen)
		if _, err := io.ReadAtLeast(r, fqdn, addrLen); err != nil {
			return nil, err
		}
		d.FQDN = string(fqdn)

	default:
		return nil, ErrUnrecognizedAddrType
	}

	// Read the port
	var port [2]byte
	if _, err := io.ReadAtLeast(r, port[:], 2); err != nil {
		return nil, err
	}
	d.Port = (uint16(port[0]) << 8) | uint16(port[1])

	return d, nil
}

// sendReply is used to send a reply message
func sendReply(w io.Writer, resp uint8, addr *AddrSpec) error {
	var addrType uint8
	var addrBody []byte
	var addrPort uint16

	if addr == nil {
		addrType = ipv4Address
		addrBody = []byte{0, 0, 0, 0}
		addrPort = 0
	} else if v4 := addr.IP.To4(); v4 != nil {
		addrType = ipv4Address
		addrBody = v4
		addrPort = addr.Port
	} else if v6 := addr.IP.To16(); v6 != nil {
		addrType = ipv6Address
		addrBody = v6
		addrPort = addr.Port
	} else {
		return fmt.Errorf("failed to format address: %v", addr)
	}

	// Message layout: VER(1) REP(1) RSV(1) ATYP(1) BND.ADDR(n) BND.PORT(2).
	msg := make([]byte, 6+len(addrBody))
	msg[0] = socks5Version
	msg[1] = resp
	msg[2] = 0 // Reserved
	msg[3] = addrType
	copy(msg[4:], addrBody)
	msg[4+len(addrBody)] = byte(addrPort >> 8)
	msg[4+len(addrBody)+1] = byte(addrPort & 0xff)

	// Send the message
	_, err := w.Write(msg)
	return err
}
