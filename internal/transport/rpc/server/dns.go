package server

import (
	"context"
	"errors"
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

	response, rtt, err := client.ExchangeWithConnContext(ctx, query, conn)
	if ctx.Err() != nil {
		return nil, rtt, ctx.Err()
	}
	return response, rtt, err
}

// safeUint32ToUint16 safely converts uint32 to uint16, returning an error if overflow would occur
func safeUint32ToUint16(val uint32) (uint16, bool) {
	if val > 65535 {
		return 0, false
	}
	return uint16(val), true
}
