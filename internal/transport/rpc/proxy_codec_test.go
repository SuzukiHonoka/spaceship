package rpc

import (
	"bytes"
	"testing"

	proxy "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/proto"
)

func TestProxyCodecPayloadWireCompatible(t *testing.T) {
	payloads := [][]byte{
		nil,
		{},
		{0x01},
		bytes.Repeat([]byte{0x5a}, 64),
		bytes.Repeat([]byte{0x5a}, 32*1024),
		bytes.Repeat([]byte{0x5a}, 256*1024),
	}

	var c proxyCodec
	for _, payload := range payloads {
		src := &proxy.ProxySRC{
			HeaderOrPayload: &proxy.ProxySRC_Payload{Payload: payload},
		}
		want, err := proto.Marshal(src)
		if err != nil {
			t.Fatalf("proto.Marshal SRC: %v", err)
		}
		got, err := c.Marshal(src)
		if err != nil {
			t.Fatalf("codec.Marshal SRC: %v", err)
		}
		if !bytes.Equal(got.Materialize(), want) {
			t.Fatalf("SRC payload len=%d: codec wire != proto.Marshal", len(payload))
		}
		got.Free()

		dst := &proxy.ProxyDST{
			Status:          proxy.ProxyStatus_Session,
			HeaderOrPayload: &proxy.ProxyDST_Payload{Payload: payload},
		}
		want, err = proto.Marshal(dst)
		if err != nil {
			t.Fatalf("proto.Marshal DST: %v", err)
		}
		got, err = c.Marshal(dst)
		if err != nil {
			t.Fatalf("codec.Marshal DST: %v", err)
		}
		if !bytes.Equal(got.Materialize(), want) {
			t.Fatalf("DST payload len=%d: codec wire != proto.Marshal", len(payload))
		}
		got.Free()
	}
}

func TestProxyCodecUnmarshalReusesPayloadBuffer(t *testing.T) {
	var c proxyCodec
	msg := &proxy.ProxyDST{
		HeaderOrPayload: &proxy.ProxyDST_Payload{},
	}

	first := bytes.Repeat([]byte{0x11}, 4096)
	wire, err := proto.Marshal(&proxy.ProxyDST{
		HeaderOrPayload: &proxy.ProxyDST_Payload{Payload: first},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Unmarshal(mem.BufferSlice{mem.SliceBuffer(wire)}, msg); err != nil {
		t.Fatal(err)
	}
	bufPtr := &msg.GetPayload()[0]

	second := bytes.Repeat([]byte{0x22}, 4096)
	wire, err = proto.Marshal(&proxy.ProxyDST{
		HeaderOrPayload: &proxy.ProxyDST_Payload{Payload: second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Unmarshal(mem.BufferSlice{mem.SliceBuffer(wire)}, msg); err != nil {
		t.Fatal(err)
	}
	if &msg.GetPayload()[0] != bufPtr {
		t.Fatal("expected payload backing array to be reused across Unmarshal calls")
	}
	if !bytes.Equal(msg.GetPayload(), second) {
		t.Fatal("payload contents not updated")
	}
}

func TestProxyCodecStatusOnlyWireCompatible(t *testing.T) {
	var c proxyCodec
	for _, status := range []proxy.ProxyStatus{proxy.ProxyStatus_EOF, proxy.ProxyStatus_Error} {
		msg := &proxy.ProxyDST{Status: status}
		want, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Materialize(), want) {
			t.Fatalf("status %v: codec wire != proto.Marshal (%x vs %x)", status, got.Materialize(), want)
		}
		got.Free()
	}
}

func TestProxyCodecHeaderRoundTrip(t *testing.T) {
	var c proxyCodec
	src := &proxy.ProxySRC{
		HeaderOrPayload: &proxy.ProxySRC_Header{
			Header: &proxy.ProxySRC_ProxyHeader{
				Addr:    "example.com:443",
				Network: proxy.Network_TCP,
			},
		},
	}
	wire, err := c.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Free()

	got := new(proxy.ProxySRC)
	if err := c.Unmarshal(wire, got); err != nil {
		t.Fatal(err)
	}
	if got.GetHeader().GetAddr() != "example.com:443" {
		t.Fatalf("header addr = %q", got.GetHeader().GetAddr())
	}
}
