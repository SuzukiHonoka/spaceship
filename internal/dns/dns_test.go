package dns

import (
	"context"
	"errors"
	"net"
	"testing"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	mdns "github.com/miekg/dns"
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
