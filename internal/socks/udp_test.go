package socks

import (
	"bytes"
	"net"
	"testing"
)

func TestParseUDPHeader(t *testing.T) {
	tests := []struct {
		name    string
		buf     []byte
		want    *UDPHeader
		wantErr bool
	}{
		{
			name: "valid ipv4",
			buf:  append([]byte{0, 0, 0, ipv4Address, 127, 0, 0, 1, 0, 80}, []byte("data")...),
			want: &UDPHeader{
				Frag: 0,
				Addr: &AddrSpec{IP: net.ParseIP("127.0.0.1").To4(), Port: 80},
				DataOffset: 10,
			},
			wantErr: false,
		},
		{
			name: "valid ipv6",
			buf:  append([]byte{0, 0, 0, ipv6Address}, append(net.ParseIP("::1"), 0, 80)...),
			want: &UDPHeader{
				Frag: 0,
				Addr: &AddrSpec{IP: net.ParseIP("::1"), Port: 80},
				DataOffset: 22,
			},
			wantErr: false,
		},
		{
			name: "valid fqdn",
			buf:  append([]byte{0, 0, 0, fqdnAddress, 7}, append([]byte("example"), 0, 80)...),
			want: &UDPHeader{
				Frag: 0,
				Addr: &AddrSpec{FQDN: "example", Port: 80},
				DataOffset: 14,
			},
			wantErr: false,
		},
		{
			name:    "too short",
			buf:     []byte{0, 0, 0},
			wantErr: true,
		},
		{
			name:    "invalid atyp",
			buf:     []byte{0, 0, 0, 0x05, 127, 0, 0, 1, 0, 80},
			wantErr: true,
		},
		{
			name:    "truncated ipv4",
			buf:     []byte{0, 0, 0, ipv4Address, 127, 0, 0},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseUDPHeader(tt.buf)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseUDPHeader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Frag != tt.want.Frag {
					t.Errorf("Frag = %v, want %v", got.Frag, tt.want.Frag)
				}
				if got.DataOffset != tt.want.DataOffset {
					t.Errorf("DataOffset = %v, want %v", got.DataOffset, tt.want.DataOffset)
				}
				if tt.want.Addr.IP != nil && !got.Addr.IP.Equal(tt.want.Addr.IP) {
					t.Errorf("IP = %v, want %v", got.Addr.IP, tt.want.Addr.IP)
				}
				if got.Addr.FQDN != tt.want.Addr.FQDN {
					t.Errorf("FQDN = %v, want %v", got.Addr.FQDN, tt.want.Addr.FQDN)
				}
				if got.Addr.Port != tt.want.Addr.Port {
					t.Errorf("Port = %v, want %v", got.Addr.Port, tt.want.Addr.Port)
				}
			}
		})
	}
}

func TestMarshalUDPHeader(t *testing.T) {
	tests := []struct {
		name    string
		addr    *AddrSpec
		want    []byte
		wantErr bool
	}{
		{
			name: "ipv4",
			addr: &AddrSpec{IP: net.ParseIP("127.0.0.1"), Port: 80},
			want: []byte{0, 0, 0, ipv4Address, 127, 0, 0, 1, 0, 80},
		},
		{
			name: "ipv6",
			addr: &AddrSpec{IP: net.ParseIP("::1"), Port: 80},
			want: append([]byte{0, 0, 0, ipv6Address}, append(net.ParseIP("::1"), 0, 80)...),
		},
		{
			name: "fqdn",
			addr: &AddrSpec{FQDN: "example", Port: 80},
			want: []byte{0, 0, 0, fqdnAddress, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0, 80},
		},
		{
			name:    "invalid",
			addr:    &AddrSpec{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MarshalUDPHeader(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("MarshalUDPHeader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !bytes.Equal(got, tt.want) {
				t.Errorf("MarshalUDPHeader() = %v, want %v", got, tt.want)
			}
		})
	}
}
