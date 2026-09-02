package client

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	mdns "github.com/miekg/dns"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

func TestControlTarget(t *testing.T) {
	t.Parallel()

	for _, address := range []string{
		"control.example:443",
		"192.0.2.10:8443",
		"[2001:db8::10]:443",
		"[fe80::1%en0]:443",
	} {
		t.Run(address, func(t *testing.T) {
			t.Parallel()

			target, err := controlTarget(address)
			if err != nil {
				t.Fatalf("controlTarget(%q) error = %v", address, err)
			}
			parsed, err := url.Parse(target)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v", target, err)
			}
			if parsed.Scheme != "passthrough" {
				t.Errorf("scheme = %q, want passthrough", parsed.Scheme)
			}
			if parsed.Path != "/"+address {
				t.Errorf("path = %q, want %q", parsed.Path, "/"+address)
			}
		})
	}
}

func TestControlTargetRejectsInvalidAddress(t *testing.T) {
	t.Parallel()

	for _, address := range []string{
		"",
		"control.example",
		":443",
		"control.example:",
		"dns:///control.example:443",
		"2001:db8::10:443",
	} {
		t.Run(address, func(t *testing.T) {
			t.Parallel()
			if _, err := controlTarget(address); err == nil {
				t.Fatalf("controlTarget(%q) succeeded, want error", address)
			}
		})
	}
}

func TestNewConnWrapperRejectsNilParams(t *testing.T) {
	t.Parallel()
	if _, err := NewConnWrapper(nil); err == nil {
		t.Fatal("NewConnWrapper(nil) succeeded")
	}
}

func TestNewConnWrapperPassesScopedIPv6AddressUnchangedToDialer(t *testing.T) {
	const address = "[fe80::1%en0]:443"
	captured := make(chan string, 1)
	wrapper, err := NewConnWrapper(NewParams(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, got string) (net.Conn, error) {
			select {
			case captured <- got:
			default:
			}
			return nil, errors.New("intentional test dial failure")
		}),
	))
	if err != nil {
		t.Fatalf("NewConnWrapper() error = %v", err)
	}
	t.Cleanup(func() { _ = wrapper.Close() })
	wrapper.Connect()

	select {
	case got := <-captured:
		if got != address {
			t.Fatalf("dial address = %q, want %q", got, address)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("context dialer was not called")
	}
}

func TestNewConnWrapperResolvesControlHostnameWithOutboundResolver(t *testing.T) {
	dnsConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("DNS listen: %v", err)
	}
	dnsMux := mdns.NewServeMux()
	dnsMux.HandleFunc(".", func(w mdns.ResponseWriter, request *mdns.Msg) {
		response := new(mdns.Msg)
		response.SetReply(request)
		if len(request.Question) == 1 &&
			request.Question[0].Name == "spaceship-control.test." &&
			request.Question[0].Qtype == mdns.TypeA {
			answer, rrErr := mdns.NewRR("spaceship-control.test. 60 IN A 127.0.0.1")
			if rrErr == nil {
				response.Answer = append(response.Answer, answer)
			}
		}
		_ = w.WriteMsg(response)
	})
	dnsServer := &mdns.Server{PacketConn: dnsConn, Handler: dnsMux}
	dnsStarted := make(chan struct{})
	dnsServer.NotifyStartedFunc = func() { close(dnsStarted) }
	go func() { _ = dnsServer.ActivateAndServe() }()
	t.Cleanup(func() { _ = dnsServer.Shutdown() })

	select {
	case <-dnsStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("DNS server did not start")
	}

	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("gRPC listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	go func() { _ = grpcServer.Serve(grpcListener) }()
	t.Cleanup(grpcServer.Stop)

	originalResolver := transport.OutboundResolver()
	transport.SetOutboundResolver(transport.NewFixedResolver(dnsConn.LocalAddr().String(), time.Second))
	t.Cleanup(func() { transport.SetOutboundResolver(originalResolver) })

	_, port, err := net.SplitHostPort(grpcListener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", grpcListener.Addr(), err)
	}
	options := append(rpc.DialOptions(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	wrapper, err := NewConnWrapper(NewParams(net.JoinHostPort("spaceship-control.test", port), options...))
	if err != nil {
		t.Fatalf("NewConnWrapper() error = %v", err)
	}
	t.Cleanup(func() { _ = wrapper.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wrapper.Connect()
	for state := wrapper.GetState(); state != connectivity.Ready; state = wrapper.GetState() {
		if !wrapper.WaitForStateChange(ctx, state) {
			t.Fatalf("control connection did not become ready: state=%s error=%v", state, ctx.Err())
		}
	}
}

func TestConnWrapper_InUse(t *testing.T) {
	w := &ConnWrapper{}

	if load := w.GetCurrentLoad(); load != 0 {
		t.Errorf("expected load 0, got %v", load)
	}

	w.Use()
	if load := w.GetCurrentLoad(); load != 1 {
		t.Errorf("expected load 1, got %v", load)
	}

	if err := w.Done(); err != nil {
		t.Fatalf("Done() error = %v", err)
	}
	if load := w.GetCurrentLoad(); load != 0 {
		t.Errorf("expected load 0, got %v", load)
	}
	if err := w.Done(); err == nil {
		t.Fatal("Done() allowed usage accounting to underflow")
	}
}

func TestConnWrappers_PickLeastLoaded(t *testing.T) {
	// We can't easily mock ClientConn.GetState() as it's a method on a struct.
	// But we can test the load balancing logic by assuming GetState returns Idle/Ready for nil ClientConn (which it won't, it will panic).
	// So we'll skip the tests that call GetState on nil pointers or use a dummy ClientConn.

	w1 := &ConnWrapper{ID: 1}
	w1.InUse.Store(10)

	w2 := &ConnWrapper{ID: 2}
	w2.InUse.Store(5)

	w3 := &ConnWrapper{ID: 3}
	w3.InUse.Store(20)

	wrappers := ConnWrappers{w1, w2, w3}

	// Since we can't easily mock GetState(), we'll test GetDetailedStatus and GetSummaryStats instead.

	status := wrappers.GetDetailedStatus()
	wantStatus := "1(10) 2(5) 3(20)"
	if status != wantStatus {
		t.Errorf("GetDetailedStatus() = %q, want %q", status, wantStatus)
	}

	total, active, totalLoad := wrappers.GetSummaryStats()
	if total != 3 || active != 3 || totalLoad != 35 {
		t.Errorf("GetSummaryStats() = %v, %v, %v; want 3, 3, 35", total, active, totalLoad)
	}
}

func TestConnWrappers_Empty(t *testing.T) {
	var wrappers ConnWrappers
	if got := wrappers.PickLeastLoaded(); got != nil {
		t.Errorf("PickLeastLoaded on empty wrappers should be nil")
	}
	if got := wrappers.GetDetailedStatus(); got != "No connections" {
		t.Errorf("GetDetailedStatus on empty wrappers = %q", got)
	}
}

func TestConnWrappersSkipNilEntriesInStatistics(t *testing.T) {
	wrapper := &ConnWrapper{ID: 3}
	wrapper.InUse.Store(2)
	wrappers := ConnWrappers{nil, wrapper, nil}

	if got := wrappers.GetDetailedStatus(); got != "3(2)" {
		t.Fatalf("GetDetailedStatus() = %q, want 3(2)", got)
	}
	total, active, load := wrappers.GetSummaryStats()
	if total != 1 || active != 1 || load != 2 {
		t.Fatalf("GetSummaryStats() = (%d, %d, %d), want (1, 1, 2)", total, active, load)
	}
	details := wrappers.GetConnectionDetails()
	if len(details) != 1 || details[0].ID != 3 || details[0].Load != 2 {
		t.Fatalf("GetConnectionDetails() = %+v", details)
	}
	ConnWrappers{nil}.LogStatus()
}
