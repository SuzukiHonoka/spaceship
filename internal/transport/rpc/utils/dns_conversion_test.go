package utils

import (
	"net"
	"testing"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/miekg/dns"
)

func TestDNSRecordConversionRoundTrip(t *testing.T) {
	want := &dns.A{
		Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   net.ParseIP("192.0.2.10").To4(),
	}

	encoded, err := ConvertRRSliceToProto([]dns.RR{want})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ConvertProtoToRRSlice(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].String() != want.String() {
		t.Fatalf("decoded records = %v, want %v", decoded, want)
	}
}

func TestDNSRecordConversionFailsAtomically(t *testing.T) {
	valid := &dns.A{
		Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET},
		A:   net.ParseIP("192.0.2.10").To4(),
	}
	if records, err := ConvertRRSliceToProto([]dns.RR{valid, nil}); err == nil || records != nil {
		t.Fatalf("ConvertRRSliceToProto() = (%v, %v), want nil records and an error", records, err)
	}
	if records, err := ConvertProtoToRRSlice([]*proto.RR_Record{
		{WireData: []byte{0xff}},
	}); err == nil || records != nil {
		t.Fatalf("ConvertProtoToRRSlice() = (%v, %v), want nil records and an error", records, err)
	}
}
