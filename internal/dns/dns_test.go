package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	mdns "github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type responseRecorder struct {
	msg *mdns.Msg
}

func (r *responseRecorder) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53}
}

func (r *responseRecorder) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53000}
}

func (r *responseRecorder) WriteMsg(m *mdns.Msg) error {
	r.msg = m.Copy()
	return nil
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	msg := new(mdns.Msg)
	if err := msg.Unpack(b); err != nil {
		return 0, err
	}
	r.msg = msg
	return len(b), nil
}

func (r *responseRecorder) Close() error {
	return nil
}

func (r *responseRecorder) TsigStatus() error {
	return nil
}

func (r *responseRecorder) TsigTimersOnly(bool) {}

func (r *responseRecorder) Hijack() {}

type fakeWireExchanger struct {
	exchange func(context.Context, []byte, proto.Network, bool) ([]byte, error)
}

func (e fakeWireExchanger) Exchange(
	ctx context.Context,
	wire []byte,
	network proto.Network,
	blockIPv6 bool,
) ([]byte, error) {
	return e.exchange(ctx, wire, network, blockIPv6)
}

func TestServeDNSReturnsServfailWhenRPCClientUnavailable(t *testing.T) {
	s, err := NewServer("127.0.0.1:0", false)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	s.exchanger = fakeWireExchanger{exchange: func(
		context.Context,
		[]byte,
		proto.Network,
		bool,
	) ([]byte, error) {
		return nil, errors.New("RPC unavailable")
	}}

	req := new(mdns.Msg)
	req.SetQuestion("example.com.", mdns.TypeA)
	rec := new(responseRecorder)

	s.ServeDNS(rec, req)
	if rec.msg == nil {
		t.Fatal("ServeDNS did not write a response")
	}
	if rec.msg.Rcode != mdns.RcodeServerFailure {
		t.Fatalf("Rcode = %d, want SERVFAIL", rec.msg.Rcode)
	}
}

func TestServeDNSWritesExactRPCResponse(t *testing.T) {
	s, err := NewServer("127.0.0.1:0", true)
	if err != nil {
		t.Fatal(err)
	}
	var gotNetwork proto.Network
	var gotBlock bool
	s.exchanger = fakeWireExchanger{exchange: func(
		_ context.Context,
		wire []byte,
		network proto.Network,
		blockIPv6 bool,
	) ([]byte, error) {
		gotNetwork = network
		gotBlock = blockIPv6
		query := new(mdns.Msg)
		if err := query.Unpack(wire); err != nil {
			return nil, err
		}
		response := new(mdns.Msg)
		response.SetReply(query)
		response.Rcode = mdns.RcodeNameError
		response.Ns = []mdns.RR{&mdns.SOA{
			Hdr: mdns.RR_Header{
				Name:   "example.",
				Rrtype: mdns.TypeSOA,
				Class:  mdns.ClassINET,
				Ttl:    60,
			},
			Ns:      "ns.example.",
			Mbox:    "hostmaster.example.",
			Serial:  1,
			Refresh: 60,
			Retry:   60,
			Expire:  60,
			Minttl:  60,
		}}
		return response.Pack()
	}}

	query := new(mdns.Msg)
	query.SetQuestion("missing.example.", mdns.TypeA)
	recorder := new(responseRecorder)
	s.ServeDNS(recorder, query)

	if recorder.msg == nil || recorder.msg.Rcode != mdns.RcodeNameError ||
		len(recorder.msg.Ns) != 1 {
		t.Fatalf("ServeDNS response = %+v", recorder.msg)
	}
	if gotNetwork != proto.Network_UDP || !gotBlock {
		t.Fatalf("Exchange policy = network %v, blockIPv6 %t", gotNetwork, gotBlock)
	}
}

func TestServeDNSRejectsInvalidQueriesWithoutRPC(t *testing.T) {
	s, err := NewServer("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.exchanger = fakeWireExchanger{exchange: func(
		context.Context,
		[]byte,
		proto.Network,
		bool,
	) ([]byte, error) {
		calls++
		return nil, errors.New("must not be called")
	}}

	tests := []struct {
		name  string
		query *mdns.Msg
		rcode int
	}{
		{
			name: "multiple questions",
			query: &mdns.Msg{Question: []mdns.Question{
				{Name: "one.example.", Qtype: mdns.TypeA, Qclass: mdns.ClassINET},
				{Name: "two.example.", Qtype: mdns.TypeA, Qclass: mdns.ClassINET},
			}},
			rcode: mdns.RcodeFormatError,
		},
		{
			name: "unsupported opcode",
			query: func() *mdns.Msg {
				query := new(mdns.Msg)
				query.SetQuestion("example.com.", mdns.TypeA)
				query.Opcode = mdns.OpcodeUpdate
				return query
			}(),
			rcode: mdns.RcodeNotImplemented,
		},
		{
			name: "zone transfer",
			query: func() *mdns.Msg {
				query := new(mdns.Msg)
				query.SetQuestion("example.com.", mdns.TypeAXFR)
				return query
			}(),
			rcode: mdns.RcodeRefused,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := new(responseRecorder)
			s.ServeDNS(recorder, test.query)
			if recorder.msg == nil || recorder.msg.Rcode != test.rcode {
				t.Fatalf("ServeDNS() response = %+v, want rcode %d", recorder.msg, test.rcode)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid queries reached RPC exchanger %d times", calls)
	}
}

type fakeLegacyResolver struct {
	resolve func(context.Context, *mdns.Msg, bool) ([]mdns.RR, int, error)
}

func (r fakeLegacyResolver) Resolve(
	ctx context.Context,
	query *mdns.Msg,
	blockIPv6 bool,
) ([]mdns.RR, int, error) {
	return r.resolve(ctx, query, blockIPv6)
}

func TestServeDNSFallsBackToLegacyResolveOnOlderServer(t *testing.T) {
	s, err := NewServer("127.0.0.1:0", true)
	if err != nil {
		t.Fatal(err)
	}
	s.exchanger = fakeWireExchanger{exchange: func(
		context.Context,
		[]byte,
		proto.Network,
		bool,
	) ([]byte, error) {
		// Wrapped the way the RPC client wraps it.
		return nil, fmt.Errorf("rpc dns exchange: %w",
			status.Error(codes.Unimplemented, "unknown method DnsExchange"))
	}}
	var gotBlock bool
	var gotQuestion mdns.Question
	s.legacy = fakeLegacyResolver{resolve: func(
		_ context.Context,
		query *mdns.Msg,
		blockIPv6 bool,
	) ([]mdns.RR, int, error) {
		gotBlock = blockIPv6
		gotQuestion = query.Question[0]
		return []mdns.RR{&mdns.A{
			Hdr: mdns.RR_Header{
				Name:   "example.com.",
				Rrtype: mdns.TypeA,
				Class:  mdns.ClassINET,
				Ttl:    60,
			},
			A: net.IPv4(192, 0, 2, 1),
		}}, mdns.RcodeSuccess, nil
	}}

	query := new(mdns.Msg)
	query.SetQuestion("example.com.", mdns.TypeA)
	recorder := new(responseRecorder)
	s.ServeDNS(recorder, query)

	if recorder.msg == nil || recorder.msg.Rcode != mdns.RcodeSuccess {
		t.Fatalf("ServeDNS() response = %+v, want NOERROR", recorder.msg)
	}
	if recorder.msg.Id != query.Id || !recorder.msg.Response {
		t.Fatalf("response header = %+v, want a reply to query %d", recorder.msg.MsgHdr, query.Id)
	}
	if len(recorder.msg.Answer) != 1 {
		t.Fatalf("answers = %v, want the legacy answer", recorder.msg.Answer)
	}
	if !gotBlock || gotQuestion.Name != "example.com." || gotQuestion.Qtype != mdns.TypeA {
		t.Fatalf("legacy resolve got question %+v, blockIPv6 %t", gotQuestion, gotBlock)
	}
}

func TestServeDNSDoesNotFallBackOnOtherRPCErrors(t *testing.T) {
	s, err := NewServer("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	s.exchanger = fakeWireExchanger{exchange: func(
		context.Context,
		[]byte,
		proto.Network,
		bool,
	) ([]byte, error) {
		return nil, fmt.Errorf("rpc dns exchange: %w",
			status.Error(codes.ResourceExhausted, "dns: exchange capacity exhausted"))
	}}
	s.legacy = fakeLegacyResolver{resolve: func(
		context.Context,
		*mdns.Msg,
		bool,
	) ([]mdns.RR, int, error) {
		t.Fatal("legacy resolve must only serve servers without DnsExchange")
		return nil, 0, nil
	}}

	query := new(mdns.Msg)
	query.SetQuestion("example.com.", mdns.TypeA)
	recorder := new(responseRecorder)
	s.ServeDNS(recorder, query)

	if recorder.msg == nil || recorder.msg.Rcode != mdns.RcodeServerFailure {
		t.Fatalf("ServeDNS() response = %+v, want SERVFAIL", recorder.msg)
	}
}

func TestServeDNSLegacyFailureReturnsServfail(t *testing.T) {
	s, err := NewServer("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	s.exchanger = fakeWireExchanger{exchange: func(
		context.Context,
		[]byte,
		proto.Network,
		bool,
	) ([]byte, error) {
		return nil, status.Error(codes.Unimplemented, "unknown method DnsExchange")
	}}
	s.legacy = fakeLegacyResolver{resolve: func(
		context.Context,
		*mdns.Msg,
		bool,
	) ([]mdns.RR, int, error) {
		return nil, mdns.RcodeServerFailure, errors.New("rpc dns: unavailable")
	}}

	query := new(mdns.Msg)
	query.SetQuestion("example.com.", mdns.TypeA)
	recorder := new(responseRecorder)
	s.ServeDNS(recorder, query)

	if recorder.msg == nil || recorder.msg.Rcode != mdns.RcodeServerFailure {
		t.Fatalf("ServeDNS() response = %+v, want SERVFAIL", recorder.msg)
	}
}
