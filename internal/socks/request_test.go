package socks

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
)

func TestNewRequest(t *testing.T) {
	tests := []struct {
		name    string
		buf     []byte
		want    *Request
		wantErr bool
	}{
		{
			name: "valid connect ipv4",
			buf:  []byte{socks5Version, ConnectCommand, 0, ipv4Address, 127, 0, 0, 1, 0, 80},
			want: &Request{
				Version: socks5Version,
				Command: ConnectCommand,
				DestAddr: &AddrSpec{
					IP:   net.ParseIP("127.0.0.1").To4(),
					Port: 80,
				},
			},
			wantErr: false,
		},
		{
			name: "valid associate fqdn",
			buf:  append([]byte{socks5Version, AssociateCommand, 0, fqdnAddress, 7}, append([]byte("example"), 0x01, 0xBB)...),
			want: &Request{
				Version: socks5Version,
				Command: AssociateCommand,
				DestAddr: &AddrSpec{
					FQDN: "example",
					Port: 443,
				},
			},
			wantErr: false,
		},
		{
			name:    "unsupported version",
			buf:     []byte{4, ConnectCommand, 0, ipv4Address, 127, 0, 0, 1, 0, 80},
			wantErr: true,
		},
		{
			name:    "invalid atyp",
			buf:     []byte{socks5Version, ConnectCommand, 0, 0x05, 127, 0, 0, 1, 0, 80},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(bytes.NewReader(tt.buf))
			got, err := NewRequest(r)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewRequest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Version != tt.want.Version {
					t.Errorf("Version = %v, want %v", got.Version, tt.want.Version)
				}
				if got.Command != tt.want.Command {
					t.Errorf("Command = %v, want %v", got.Command, tt.want.Command)
				}
				if tt.want.DestAddr.FQDN != got.DestAddr.FQDN {
					t.Errorf("FQDN = %v, want %v", got.DestAddr.FQDN, tt.want.DestAddr.FQDN)
				}
				if tt.want.DestAddr.IP != nil && !got.DestAddr.IP.Equal(tt.want.DestAddr.IP) {
					t.Errorf("IP = %v, want %v", got.DestAddr.IP, tt.want.DestAddr.IP)
				}
				if got.DestAddr.Port != tt.want.DestAddr.Port {
					t.Errorf("Port = %v, want %v", got.DestAddr.Port, tt.want.DestAddr.Port)
				}
			}
		})
	}
}

func TestAddrSpec_String(t *testing.T) {
	tests := []struct {
		name string
		addr AddrSpec
		want string
	}{
		{"ipv4", AddrSpec{IP: net.ParseIP("1.2.3.4"), Port: 80}, "1.2.3.4:80"},
		{"fqdn", AddrSpec{FQDN: "google.com", IP: net.ParseIP("8.8.8.8"), Port: 443}, "google.com (8.8.8.8):443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.addr.String(); got != tt.want {
				t.Errorf("AddrSpec.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAddrSpec_Address(t *testing.T) {
	tests := []struct {
		name string
		addr AddrSpec
		want string
	}{
		{"ip", AddrSpec{IP: net.ParseIP("1.1.1.1"), Port: 53}, "1.1.1.1:53"},
		{"fqdn only", AddrSpec{FQDN: "localhost", Port: 8080}, "localhost:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.addr.Address(); got != tt.want {
				t.Errorf("AddrSpec.Address() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSendReply(t *testing.T) {
	var buf bytes.Buffer
	addr := &AddrSpec{IP: net.ParseIP("127.0.0.1").To4(), Port: 8080}

	err := sendReply(&buf, successReply, addr)
	if err != nil {
		t.Fatalf("sendReply() error = %v", err)
	}

	got := buf.Bytes()
	want := []byte{socks5Version, successReply, 0, ipv4Address, 127, 0, 0, 1, 0x1f, 0x90}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sendReply() = %v, want %v", got, want)
	}
}

func TestHandleAssociate_ContextCancelStopsRelay(t *testing.T) {
	// handleAssociate refuses up front unless some installed route has a
	// UDP-capable egress, so give it one.
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	req := &Request{
		RemoteAddr: &AddrSpec{IP: net.ParseIP("127.0.0.1"), Port: 12345},
		bufConn:    pr,
	}
	s := &Server{}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.handleAssociate(ctx, serverConn, req)
	}()

	if err := clientConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	replyHead := make([]byte, 4)
	if _, err := io.ReadFull(clientConn, replyHead); err != nil {
		t.Fatalf("reading associate reply header: %v", err)
	}
	if replyHead[1] != successReply {
		t.Fatalf("associate reply code = %d, want success", replyHead[1])
	}
	remaining := 0
	switch replyHead[3] {
	case ipv4Address:
		remaining = 4 + 2
	case ipv6Address:
		remaining = 16 + 2
	case fqdnAddress:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(clientConn, lenBuf); err != nil {
			t.Fatalf("reading associate FQDN length: %v", err)
		}
		remaining = int(lenBuf[0]) + 2
	default:
		t.Fatalf("associate reply address type = %d, want a valid SOCKS address type", replyHead[3])
	}
	if remaining > 0 {
		if _, err := io.ReadFull(clientConn, make([]byte, remaining)); err != nil {
			t.Fatalf("reading associate reply body: %v", err)
		}
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("handleAssociate() after context cancel error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handleAssociate() did not return after context cancellation")
	}
}
