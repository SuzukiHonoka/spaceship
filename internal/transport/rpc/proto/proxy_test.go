package proxy

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestDNSRcodeWireRoundTrip(t *testing.T) {
	want := &DnsResponse{Result: []*DnsResult{{Fqdn: "missing.example.", Rcode: 3}}}
	wire, err := proto.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	got := new(DnsResponse)
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatal(err)
	}
	if len(got.Result) != 1 || got.Result[0].Rcode != 3 {
		t.Fatalf("round-trip response = %+v, want RCODE 3", got)
	}
}
