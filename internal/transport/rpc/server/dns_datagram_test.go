package server

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// startRawDNSUpstream answers each datagram with whatever reply returns, so a
// test controls the exact bytes, count, and IDs the exchanger receives.
func startRawDNSUpstream(t *testing.T, reply func(query *dns.Msg) [][]byte) string {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			query := new(dns.Msg)
			if err := query.Unpack(buf[:n]); err != nil {
				continue
			}
			for _, datagram := range reply(query) {
				if _, err := conn.WriteTo(datagram, addr); err != nil {
					return
				}
			}
		}
	}()
	return conn.LocalAddr().String()
}

func packDNS(t *testing.T, msg *dns.Msg) []byte {
	t.Helper()
	wire, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func exchangeUDP(t *testing.T, address string, query *dns.Msg) (*dns.Msg, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &dns.Client{Net: "udp", Timeout: DNSClientTimeout}
	response, _, err := exchangeDNSContext(ctx, client, query, address)
	return response, err
}

func TestExchangeDNSDatagramSkipsMismatchedIDs(t *testing.T) {
	address := startRawDNSUpstream(t, func(query *dns.Msg) [][]byte {
		stale := new(dns.Msg)
		stale.SetReply(query)
		stale.Id = query.Id + 1
		answer := new(dns.Msg)
		answer.SetReply(query)
		answer.Rcode = dns.RcodeNameError
		return [][]byte{packDNS(t, stale), packDNS(t, answer)}
	})

	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeA)
	response, err := exchangeUDP(t, address, query)
	if err != nil {
		t.Fatal(err)
	}
	if response.Id != query.Id || response.Rcode != dns.RcodeNameError {
		t.Fatalf("response = id %d rcode %d, want the reply to %d", response.Id, response.Rcode, query.Id)
	}
}

// An upstream that ignores the advertised size must still be received whole;
// DnsExchange truncates to the client's size itself. Repeat so later queries
// reuse a pooled buffer that an earlier, larger reply dirtied.
func TestExchangeDNSDatagramReceivesOversizedReplyWhole(t *testing.T) {
	address := startRawDNSUpstream(t, func(query *dns.Msg) [][]byte {
		if query.Question[0].Name == "large.example." {
			return [][]byte{packDNS(t, largeDNSResponse(query))}
		}
		small := new(dns.Msg)
		small.SetReply(query)
		return [][]byte{packDNS(t, small)}
	})

	for i := range 4 {
		large := new(dns.Msg)
		large.SetQuestion("large.example.", dns.TypeTXT) // no EDNS: 512-byte advertised size
		response, err := exchangeUDP(t, address, large)
		if err != nil {
			t.Fatalf("round %d large: %v", i, err)
		}
		if len(response.Answer) != 12 || response.Truncated {
			t.Fatalf("round %d large: %d answers, truncated %t", i, len(response.Answer), response.Truncated)
		}

		small := new(dns.Msg)
		small.SetQuestion("small.example.", dns.TypeA)
		response, err = exchangeUDP(t, address, small)
		if err != nil {
			t.Fatalf("round %d small: %v", i, err)
		}
		if len(response.Answer) != 0 || response.Question[0].Name != "small.example." {
			t.Fatalf("round %d small: stale data from a reused buffer: %v", i, response)
		}
	}
}

func TestExchangeDNSDatagramRejectsShortReply(t *testing.T) {
	address := startRawDNSUpstream(t, func(*dns.Msg) [][]byte {
		return [][]byte{{0x00, 0x01, 0x02}}
	})

	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeA)
	if _, err := exchangeUDP(t, address, query); !errors.Is(err, dns.ErrShortRead) {
		t.Fatalf("error = %v, want %v", err, dns.ErrShortRead)
	}
}
