package rpc

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
	"time"

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

func TestProxyCodecUnmarshalZeroCopy(t *testing.T) {
	var c proxyCodec
	msg := &proxy.ProxyDST{
		HeaderOrPayload: &proxy.ProxyDST_Payload{},
	}
	defer ReleaseMessageBuffers(msg)

	first := bytes.Repeat([]byte{0x11}, 4096)
	wire, err := proto.Marshal(&proxy.ProxyDST{
		HeaderOrPayload: &proxy.ProxyDST_Payload{Payload: first},
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := mem.NewBuffer(&wire, nil)
	if err := c.Unmarshal(mem.BufferSlice{frame}, msg); err != nil {
		t.Fatal(err)
	}
	frame.Free() // recv() would Free the frame; view must remain valid
	if !bytes.Equal(msg.GetPayload(), first) {
		t.Fatal("payload contents not set")
	}
	if &msg.GetPayload()[0] == &first[0] {
		t.Fatal("expected payload to alias the frame buffer, not the proto.Marshal input")
	}

	second := bytes.Repeat([]byte{0x22}, 4096)
	wire2, err := proto.Marshal(&proxy.ProxyDST{
		HeaderOrPayload: &proxy.ProxyDST_Payload{Payload: second},
	})
	if err != nil {
		t.Fatal(err)
	}
	frame2 := mem.NewBuffer(&wire2, nil)
	if err := c.Unmarshal(mem.BufferSlice{frame2}, msg); err != nil {
		t.Fatal(err)
	}
	frame2.Free()
	if !bytes.Equal(msg.GetPayload(), second) {
		t.Fatal("payload contents not updated")
	}
}

func TestProxyCodecMarshalAdoptsOfferedBuffer(t *testing.T) {
	var c proxyCodec
	pool := mem.DefaultBufferPool()
	payload := bytes.Repeat([]byte{0xa5}, 8192)
	buf, read := AcquirePayloadBuffer(pool, len(payload))
	copy(read, payload)

	src := &proxy.ProxySRC{
		HeaderOrPayload: &proxy.ProxySRC_Payload{Payload: read[:len(payload)]},
	}
	OfferPayloadBuffer(src, buf, len(payload), pool)
	got, err := c.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Free()

	want, err := proto.Marshal(&proxy.ProxySRC{
		HeaderOrPayload: &proxy.ProxySRC_Payload{Payload: payload},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Materialize(), want) {
		t.Fatal("adopted-buffer marshal wire != proto.Marshal")
	}
	DiscardPayloadBuffer(src)
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
	defer ReleaseMessageBuffers(got)
	if got.GetHeader().GetAddr() != "example.com:443" {
		t.Fatalf("header addr = %q", got.GetHeader().GetAddr())
	}
}

var codecBenchSizes = []int{16, 64, 256, 1024, 8192, 16384, 256 * 1024}

func BenchmarkProxyCodec_MarshalPayload(b *testing.B) {
	var c proxyCodec
	pool := BufferPool()
	for _, size := range codecBenchSizes {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			src := &proxy.ProxySRC{
				HeaderOrPayload: &proxy.ProxySRC_Payload{},
			}
			payload := src.HeaderOrPayload.(*proxy.ProxySRC_Payload)
			b.Cleanup(func() { ReleaseMessageBuffers(src) })

			b.SetBytes(int64(size))
			b.ReportAllocs()
			samples := make([]time.Duration, b.N)
			b.ResetTimer()
			for i := range b.N {
				b.StopTimer()
				buf, read := AcquirePayloadBuffer(pool, size)
				for j := range read[:size] {
					read[j] = 0x5a
				}
				payload.Payload = read[:size]
				OfferPayloadBuffer(src, buf, size, pool)
				b.StartTimer()

				start := time.Now()
				got, err := c.Marshal(src)
				samples[i] = time.Since(start)
				if err != nil {
					b.Fatal(err)
				}
				got.Free()
				payload.Payload = nil
			}
			b.StopTimer()
			reportCodecRTT(b, samples)
		})
	}
}

func BenchmarkProxyCodec_UnmarshalPayload(b *testing.B) {
	var c proxyCodec
	for _, size := range codecBenchSizes {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			wire, err := proto.Marshal(&proxy.ProxyDST{
				HeaderOrPayload: &proxy.ProxyDST_Payload{
					Payload: bytes.Repeat([]byte{0x5a}, size),
				},
			})
			if err != nil {
				b.Fatal(err)
			}

			msg := &proxy.ProxyDST{
				HeaderOrPayload: &proxy.ProxyDST_Payload{},
			}
			b.Cleanup(func() { ReleaseMessageBuffers(msg) })

			b.SetBytes(int64(size))
			b.ReportAllocs()
			samples := make([]time.Duration, b.N)
			b.ResetTimer()
			for i := range b.N {
				b.StopTimer()
				frameBytes := append([]byte(nil), wire...)
				frame := mem.NewBuffer(&frameBytes, nil)
				b.StartTimer()

				start := time.Now()
				err := c.Unmarshal(mem.BufferSlice{frame}, msg)
				samples[i] = time.Since(start)
				if err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				frame.Free()
				b.StartTimer()
			}
			b.StopTimer()
			reportCodecRTT(b, samples)
		})
	}
}

func reportCodecRTT(b *testing.B, samples []time.Duration) {
	b.Helper()
	if len(samples) == 0 {
		return
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	b.ReportMetric(float64(sum)/float64(len(sorted))/float64(time.Nanosecond), "avg-ns")
	b.ReportMetric(float64(sorted[len(sorted)/2])/float64(time.Nanosecond), "p50-ns")
	b.ReportMetric(float64(sorted[codecPercentileIndex(len(sorted), 99)])/float64(time.Nanosecond), "p99-ns")
}

func codecPercentileIndex(n, p int) int {
	if n <= 1 {
		return 0
	}
	i := (p*(n-1) + 99) / 100
	if i >= n {
		return n - 1
	}
	return i
}
