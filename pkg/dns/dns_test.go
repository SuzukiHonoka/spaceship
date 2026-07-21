package dns

import "testing"

func TestDNSAddress(t *testing.T) {
	tests := []struct {
		name   string
		dns    DNS
		want   string
		reason string
	}{
		{
			name: "bare ipv4 gets the standard port",
			dns:  DNS{Type: TypeCommon, Server: "1.1.1.1"},
			want: "1.1.1.1:53",
		},
		{
			name: "bare ipv6 is bracketed",
			dns:  DNS{Type: TypeCommon, Server: "2606:4700:4700::1111"},
			want: "[2606:4700:4700::1111]:53",
		},
		{
			name: "bare hostname gets the standard port",
			dns:  DNS{Type: TypeCommon, Server: "resolver.example"},
			want: "resolver.example:53",
		},
		{
			name:   "explicit port is preserved",
			dns:    DNS{Type: TypeCommon, Server: "127.0.0.1:5353"},
			want:   "127.0.0.1:5353",
			reason: "a stub resolver on a non-standard port must be reachable",
		},
		{
			name: "explicit ipv6 port is preserved",
			dns:  DNS{Type: TypeCommon, Server: "[::1]:5353"},
			want: "[::1]:5353",
		},
		{
			name: "default type behaves like common",
			dns:  DNS{Type: TypeDefault, Server: "8.8.8.8"},
			want: "8.8.8.8:53",
		},
		{
			name:   "unimplemented types have no address",
			dns:    DNS{Type: TypeDOT, Server: "1.1.1.1"},
			want:   "",
			reason: "DOT/DOH are not wired up; an address here would silently query plaintext",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.dns.Address(); got != tt.want {
				if tt.reason != "" {
					t.Errorf("Address() = %q, want %q (%s)", got, tt.want, tt.reason)
					return
				}
				t.Errorf("Address() = %q, want %q", got, tt.want)
			}
		})
	}
}
