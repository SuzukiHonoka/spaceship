package server

import (
	"log"
	"time"

	"github.com/miekg/dns"
)

const dnsClientTimeout = 5 * time.Second

// resolveDNSRecords performs actual DNS resolution using the miekg/dns library
func (s *Server) resolveDNSRecords(fqdn string, qtype uint16) []dns.RR {
	// Create DNS query message
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(fqdn), qtype)
	m.RecursionDesired = true

	// dns.Client is lightweight, no need for sync.Pool
	c := &dns.Client{
		Timeout: dnsClientTimeout,
	}

	// Query DNS server
	response, _, err := c.Exchange(m, s.dnsAddr)
	if err != nil {
		log.Printf("DNS resolution failed for %s using server %s: %v", fqdn, s.dnsAddr, err)
		return nil
	}

	if response == nil {
		log.Printf("DNS resolution returned nil response for %s", fqdn)
		return nil
	}
	if response.Rcode != dns.RcodeSuccess {
		log.Printf("DNS resolution failed for %s: response code %d", fqdn, response.Rcode)
	}

	// Return all answer records
	return response.Answer
}

// safeUint32ToUint16 safely converts uint32 to uint16, returning an error if overflow would occur
func safeUint32ToUint16(val uint32) (uint16, bool) {
	if val > 65535 {
		return 0, false
	}
	return uint16(val), true
}
