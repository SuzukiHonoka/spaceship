package sniff

import (
	"testing"
)

func TestQUICServerName_V1AndV2(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version uint32
		host    string
	}{
		{"v1", QUICVersion1, "quic.example.com"},
		{"v2", QUICVersion2, "v2.cloudflare.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkt, err := BuildQUICInitial(tc.host, tc.version)
			if err != nil {
				t.Fatalf("BuildQUICInitial: %v", err)
			}
			if got := QUICServerName(pkt); got != tc.host {
				t.Fatalf("QUICServerName() = %q, want %q", got, tc.host)
			}
		})
	}
}

func TestQUICServerName_RejectsNonQUICAndCorrupt(t *testing.T) {
	valid, err := BuildQUICInitial("example.com", QUICVersion1)
	if err != nil {
		t.Fatalf("BuildQUICInitial: %v", err)
	}
	corrupt := append([]byte(nil), valid...)
	corrupt[len(corrupt)-1] ^= 0xff // flip GCM tag bit

	cases := [][]byte{
		nil,
		{},
		[]byte("DNS query payload that is not QUIC"),
		make([]byte, 1200),
		valid[:20],
		corrupt,
	}
	for i, pkt := range cases {
		if got := QUICServerName(pkt); got != "" {
			t.Fatalf("case %d: QUICServerName() = %q, want empty", i, got)
		}
	}
}

func FuzzQUICServerName(f *testing.F) {
	if p1, err := BuildQUICInitial("fuzz.example.com", QUICVersion1); err == nil {
		f.Add(p1)
	}
	if p2, err := BuildQUICInitial("fuzz2.example.com", QUICVersion2); err == nil {
		f.Add(p2)
	}
	f.Add([]byte("short"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = QUICServerName(data)
	})
}

func BenchmarkQUICServerName_NonQUIC(b *testing.B) {
	pkt := make([]byte, 64)
	pkt[0] = 0x40 // Short header / non-Initial
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if QUICServerName(pkt) != "" {
			b.Fatal("unexpected match")
		}
	}
}

func BenchmarkQUICServerName_ValidV1(b *testing.B) {
	pkt, err := BuildQUICInitial("www.example.com", QUICVersion1)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if QUICServerName(pkt) != "www.example.com" {
			b.Fatal("SNI mismatch")
		}
	}
}
