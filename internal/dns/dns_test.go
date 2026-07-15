package dns

import (
	"net"
	"testing"

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

func TestServeDNSReturnsServfailWhenRPCClientUnavailable(t *testing.T) {
	s, err := NewServer("127.0.0.1:0", false)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

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
