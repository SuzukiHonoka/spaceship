package server

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/dnswire"
	"github.com/miekg/dns"
)

const DNSClientTimeout = 5 * time.Second

// resolveDNSRecords performs actual DNS resolution using the shared miekg/dns client.
func (s *Server) resolveDNSRecords(
	ctx context.Context,
	fqdn string,
	qtype uint16,
) ([]dns.RR, int, error) {
	if s.dnsClient == nil || s.dnsAddr == "" {
		return nil, dns.RcodeServerFailure, errors.New("resolver is not configured")
	}

	// Create DNS query message
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(fqdn), qtype)
	m.RecursionDesired = true

	// Copy the shared client template before each exchange. This permits
	// concurrent UDP/TCP requests without sharing request-local state.
	client := *s.dnsClient
	response, _, err := exchangeDNSContext(ctx, &client, m, s.dnsAddr)
	if err != nil {
		return nil, dns.RcodeServerFailure, err
	}

	if response == nil {
		return nil, dns.RcodeServerFailure, errors.New("resolver returned an empty response")
	}
	if !response.Response {
		return nil, dns.RcodeServerFailure, errors.New("resolver returned a query instead of a response")
	}
	if !dnswire.QuestionsEqual(m, response) {
		return nil, dns.RcodeServerFailure, errors.New("resolver response does not match the query")
	}

	// Return all answer records
	return response.Answer, response.Rcode, nil
}

// exchangeDNSContext actively closes an in-flight resolver socket when ctx is
// canceled. miekg/dns applies context deadlines to I/O, but a deadline-free
// cancellation after Dial does not otherwise interrupt a blocked response
// read.
func exchangeDNSContext(
	ctx context.Context,
	client *dns.Client,
	query *dns.Msg,
	address string,
) (*dns.Msg, time.Duration, error) {
	conn, err := client.DialContext(ctx, address)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = conn.Close() }()

	stopCancellation := context.AfterFunc(ctx, func() {
		_ = conn.Close()
	})
	defer stopCancellation()

	var (
		response *dns.Msg
		rtt      time.Duration
	)
	if _, ok := conn.Conn.(net.PacketConn); ok {
		response, rtt, err = exchangeDNSDatagram(ctx, client, query, conn)
	} else {
		// TCP reads allocate exactly the length prefix, so miekg/dns is fine.
		response, rtt, err = client.ExchangeWithConnContext(ctx, query, conn)
	}
	if ctx.Err() != nil {
		return nil, rtt, ctx.Err()
	}
	return response, rtt, err
}

// udpResponseBuffers holds receive buffers large enough for any DNS datagram.
// miekg/dns allocates a fresh buffer of the advertised size for every UDP
// read, which for a full-size receive is 64KiB of garbage per query.
var udpResponseBuffers = sync.Pool{
	New: func() any {
		b := make([]byte, dnswire.MaxMessageSize)
		return &b
	},
}

// exchangeDNSDatagram mirrors ExchangeWithConnContext for a UDP conn: the same
// write and read deadlines, and replies with a mismatched ID are skipped as
// late answers to an earlier query. It reads into a pooled buffer that always
// fits the largest datagram, so an upstream that ignores the advertised size
// is still received whole. Msg.Unpack copies everything it decodes, so the
// buffer is not referenced once it returns to the pool.
func exchangeDNSDatagram(
	ctx context.Context,
	client *dns.Client,
	query *dns.Msg,
	conn *dns.Conn,
) (*dns.Msg, time.Duration, error) {
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = DNSClientTimeout
	}
	start := time.Now()
	deadline := start.Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, 0, err
	}
	if err := conn.WriteMsg(query); err != nil {
		return nil, 0, err
	}

	bufp := udpResponseBuffers.Get().(*[]byte)
	defer udpResponseBuffers.Put(bufp)
	buf := *bufp
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, time.Since(start), err
		}
		if n < dnsHeaderSize {
			return nil, time.Since(start), dns.ErrShortRead
		}
		response := new(dns.Msg)
		if err := response.Unpack(buf[:n]); err != nil {
			return nil, time.Since(start), err
		}
		if response.Id == query.Id {
			return response, time.Since(start), nil
		}
	}
}

// dnsHeaderSize is the fixed DNS message header length.
const dnsHeaderSize = 12

// safeUint32ToUint16 safely converts uint32 to uint16, returning an error if overflow would occur
func safeUint32ToUint16(val uint32) (uint16, bool) {
	if val > 65535 {
		return 0, false
	}
	return uint16(val), true
}
