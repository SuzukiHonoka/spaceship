package rpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/server"
	serverconfig "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	pkgdns "github.com/SuzukiHonoka/spaceship/v2/pkg/dns"
	mdns "github.com/miekg/dns"
)

// startTestResolver runs a DNS server over a fixed zone so the tunnel's resolve
// path can be exercised without touching a public resolver.
func startTestResolver(t *testing.T) string {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test resolver listen: %v", err)
	}

	mux := mdns.NewServeMux()
	mux.HandleFunc(".", func(w mdns.ResponseWriter, r *mdns.Msg) {
		m := new(mdns.Msg)
		m.SetReply(r)
		if len(r.Question) == 0 {
			m.Rcode = mdns.RcodeFormatError
			_ = w.WriteMsg(m)
			return
		}

		switch q := r.Question[0]; {
		case q.Name == "known.test." && q.Qtype == mdns.TypeA:
			if rr, err := mdns.NewRR("known.test. 300 IN A 203.0.113.7"); err == nil {
				m.Answer = append(m.Answer, rr)
			}
		case q.Name == "known.test." && q.Qtype == mdns.TypeAAAA:
			if rr, err := mdns.NewRR("known.test. 300 IN AAAA 2001:db8::7"); err == nil {
				m.Answer = append(m.Answer, rr)
			}
		case q.Name == "missing.test.":
			m.Rcode = mdns.RcodeNameError // NXDOMAIN
		default:
			m.Rcode = mdns.RcodeServerFailure
		}
		_ = w.WriteMsg(m)
	})

	srv := &mdns.Server{PacketConn: pc, Handler: mux}
	started := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(started) }
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("test resolver did not start")
	}
	return pc.LocalAddr().String()
}

// startProxyServerWithResolver runs a proxy server pointed at a specific
// upstream resolver.
func startProxyServerWithResolver(t *testing.T, resolver string) string {
	t.Helper()

	addr := freeLoopbackAddr(t)
	ctx, cancel := context.WithCancel(context.Background())

	srv, err := server.NewServer(ctx, serverconfig.Users{{UUID: testUUID}}, nil,
		&pkgdns.DNS{Type: pkgdns.TypeCommon, Server: resolver})
	if err != nil {
		cancel()
		t.Fatalf("NewServer() error = %v", err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe(addr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(10 * time.Second):
			t.Error("proxy server did not shut down")
		}
	})

	waitForListener(t, addr)
	return addr
}

func dnsClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestEndToEnd_DNSResolveOverGRPC covers the resolve RPC end to end: client
// request → auth interceptor → server → upstream resolver → wire-encoded RRs
// back through the tunnel.
func TestEndToEnd_DNSResolveOverGRPC(t *testing.T) {
	connectClient(t, startProxyServerWithResolver(t, startTestResolver(t)))
	c := dnsClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	records, rcode, err := c.DnsResolve(ctx, []*client.DnsRequest{
		{Fqdn: "known.test", QType: mdns.TypeA},
	})
	if err != nil {
		t.Fatalf("DnsResolve() error = %v", err)
	}
	if rcode != mdns.RcodeSuccess {
		t.Errorf("rcode = %d, want %d (success)", rcode, mdns.RcodeSuccess)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	a, ok := records[0].(*mdns.A)
	if !ok {
		t.Fatalf("record type = %T, want *dns.A", records[0])
	}
	if !a.A.Equal(net.ParseIP("203.0.113.7")) {
		t.Errorf("resolved address = %v, want 203.0.113.7", a.A)
	}
}

// TestEndToEnd_DNSResolvePropagatesNXDOMAIN is the regression guard for the
// rcode field added in this release.
//
// Before it existed, the server dropped result entries that carried no records,
// so a client could not tell "this name does not exist" from "the lookup
// failed" — both arrived as an empty answer.
func TestEndToEnd_DNSResolvePropagatesNXDOMAIN(t *testing.T) {
	connectClient(t, startProxyServerWithResolver(t, startTestResolver(t)))
	c := dnsClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	records, rcode, err := c.DnsResolve(ctx, []*client.DnsRequest{
		{Fqdn: "missing.test", QType: mdns.TypeA},
	})
	if err != nil {
		t.Fatalf("DnsResolve() error = %v", err)
	}
	if rcode != mdns.RcodeNameError {
		t.Errorf("rcode = %d, want %d (NXDOMAIN); an authoritative "+
			"\"does not exist\" must not arrive as an empty success",
			rcode, mdns.RcodeNameError)
	}
	if len(records) != 0 {
		t.Errorf("got %d records for NXDOMAIN, want 0", len(records))
	}
}

// TestEndToEnd_DNSResolvePropagatesServerFailure verifies an upstream SERVFAIL
// is distinguishable from an empty answer.
func TestEndToEnd_DNSResolvePropagatesServerFailure(t *testing.T) {
	connectClient(t, startProxyServerWithResolver(t, startTestResolver(t)))
	c := dnsClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, rcode, err := c.DnsResolve(ctx, []*client.DnsRequest{
		{Fqdn: "broken.test", QType: mdns.TypeA},
	})
	if err != nil {
		t.Fatalf("DnsResolve() error = %v", err)
	}
	if rcode != mdns.RcodeServerFailure {
		t.Errorf("rcode = %d, want %d (SERVFAIL)", rcode, mdns.RcodeServerFailure)
	}
}

// TestEndToEnd_DNSResolveBlocksIPv6 verifies BlockIPv6 suppresses AAAA lookups
// at the server without turning them into an error — the client should see a
// successful, empty answer so it falls through to A records.
func TestEndToEnd_DNSResolveBlocksIPv6(t *testing.T) {
	connectClient(t, startProxyServerWithResolver(t, startTestResolver(t)))
	c := dnsClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	records, rcode, err := c.DnsResolve(ctx, []*client.DnsRequest{
		{Fqdn: "known.test", QType: mdns.TypeAAAA, BlockIPv6: true},
	})
	if err != nil {
		t.Fatalf("DnsResolve() error = %v", err)
	}
	if rcode != mdns.RcodeSuccess {
		t.Errorf("rcode = %d, want %d: a blocked AAAA is not a failure",
			rcode, mdns.RcodeSuccess)
	}
	if len(records) != 0 {
		t.Errorf("got %d records, want 0 for a blocked AAAA query", len(records))
	}

	// The same name over A must still resolve, proving the block is per-qtype.
	records, rcode, err = c.DnsResolve(ctx, []*client.DnsRequest{
		{Fqdn: "known.test", QType: mdns.TypeA, BlockIPv6: true},
	})
	if err != nil {
		t.Fatalf("DnsResolve() A error = %v", err)
	}
	if rcode != mdns.RcodeSuccess || len(records) != 1 {
		t.Errorf("A lookup with BlockIPv6: rcode = %d, records = %d; want success with 1",
			rcode, len(records))
	}
}

// TestEndToEnd_DNSResolveAAAAWithoutBlocking confirms AAAA works when the block
// is off, so the previous test is actually exercising the block.
func TestEndToEnd_DNSResolveAAAAWithoutBlocking(t *testing.T) {
	connectClient(t, startProxyServerWithResolver(t, startTestResolver(t)))
	c := dnsClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	records, rcode, err := c.DnsResolve(ctx, []*client.DnsRequest{
		{Fqdn: "known.test", QType: mdns.TypeAAAA},
	})
	if err != nil {
		t.Fatalf("DnsResolve() error = %v", err)
	}
	if rcode != mdns.RcodeSuccess {
		t.Fatalf("rcode = %d, want success", rcode)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if aaaa, ok := records[0].(*mdns.AAAA); !ok {
		t.Errorf("record type = %T, want *dns.AAAA", records[0])
	} else if !aaaa.AAAA.Equal(net.ParseIP("2001:db8::7")) {
		t.Errorf("resolved address = %v, want 2001:db8::7", aaaa.AAAA)
	}
}

// TestEndToEnd_DNSResolveMultipleItems verifies a batch is answered per item and
// that one failing name surfaces its rcode rather than being silently dropped.
func TestEndToEnd_DNSResolveMultipleItems(t *testing.T) {
	connectClient(t, startProxyServerWithResolver(t, startTestResolver(t)))
	c := dnsClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	records, rcode, err := c.DnsResolve(ctx, []*client.DnsRequest{
		{Fqdn: "known.test", QType: mdns.TypeA},
		{Fqdn: "missing.test", QType: mdns.TypeA},
	})
	if err != nil {
		t.Fatalf("DnsResolve() error = %v", err)
	}
	if len(records) != 1 {
		t.Errorf("got %d records, want 1 (the resolvable name)", len(records))
	}
	if rcode != mdns.RcodeNameError {
		t.Errorf("aggregate rcode = %d, want %d: a failure inside the batch must "+
			"not be masked by a sibling success", rcode, mdns.RcodeNameError)
	}
}
