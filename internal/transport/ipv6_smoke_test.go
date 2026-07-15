package transport

import "testing"

func TestSmoke_IPv6DisableAffectsTCPAndUDP(t *testing.T) {
	t.Cleanup(EnableIPv6)

	EnableIPv6()
	if DialNetwork("tcp") != "tcp" || DialNetwork("udp") != "udp" {
		t.Fatalf("enabled: DialNetwork tcp/udp = %q/%q", DialNetwork("tcp"), DialNetwork("udp"))
	}

	DisableIPv6()
	if GetNetwork() != "tcp4" {
		t.Fatalf("GetNetwork after disable = %q, want tcp4", GetNetwork())
	}
	if DialNetwork("tcp") != "tcp4" {
		t.Fatalf("DialNetwork(tcp) = %q, want tcp4", DialNetwork("tcp"))
	}
	if DialNetwork("udp") != "udp4" {
		t.Fatalf("DialNetwork(udp) = %q, want udp4", DialNetwork("udp"))
	}
	if DialNetwork("tcp6") != "tcp4" || DialNetwork("udp6") != "udp4" {
		t.Fatalf("IPv6-only nets not rewritten: tcp6=%q udp6=%q", DialNetwork("tcp6"), DialNetwork("udp6"))
	}
	if !PreferIPv4() {
		t.Fatal("PreferIPv4() = false after DisableIPv6")
	}

	EnableIPv6()
	if PreferIPv4() {
		t.Fatal("PreferIPv4() still true after EnableIPv6")
	}
	if GetNetwork() != "tcp" {
		t.Fatalf("GetNetwork after enable = %q, want tcp", GetNetwork())
	}
}
