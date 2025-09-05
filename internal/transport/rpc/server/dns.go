package server

import (
	"log"
	"time"

	"github.com/miekg/dns"
)

// resolveDNSRecords performs actual DNS resolution using the miekg/dns library
// This replaces the simple net.LookupIP approach with proper DNS queries
func (s *Server) resolveDNSRecords(fqdn string, qtype uint16) []dns.RR {
	// Create DNS query message
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(fqdn), qtype)
	m.RecursionDesired = true

	// Create DNS client
	c := &dns.Client{
		Timeout: 5 * time.Second,
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
