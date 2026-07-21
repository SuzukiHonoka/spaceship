package server

import (
	"log"
	"time"

	"github.com/miekg/dns"
)

const DNSClientTimeout = 5 * time.Second

// resolveDNSRecords performs actual DNS resolution using the shared miekg/dns client.
func (s *Server) resolveDNSRecords(fqdn string, qtype uint16) ([]dns.RR, int) {
	// Create DNS query message
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(fqdn), qtype)
	m.RecursionDesired = true

	// Query DNS server using the shared client (safe for concurrent use)
	response, _, err := s.dnsClient.Exchange(m, s.dnsAddr)
	if err != nil {
		log.Printf("dns: resolve %s via %s failed: %v", fqdn, s.dnsAddr, err)
		return nil, dns.RcodeServerFailure
	}

	if response == nil {
		log.Printf("dns: resolve %s: empty response", fqdn)
		return nil, dns.RcodeServerFailure
	}
	if response.Rcode != dns.RcodeSuccess {
		log.Printf("dns: resolve %s: rcode %d", fqdn, response.Rcode)
	}

	// Return all answer records
	return response.Answer, response.Rcode
}

// safeUint32ToUint16 safely converts uint32 to uint16, returning an error if overflow would occur
func safeUint32ToUint16(val uint32) (uint16, bool) {
	if val > 65535 {
		return 0, false
	}
	return uint16(val), true
}
