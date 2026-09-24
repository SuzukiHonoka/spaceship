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
	RetainPayloadViews(msg)
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

// countingPool records how many buffers were returned, so a test can observe
// exactly when the codec drops its last reference to a receive frame.
type countingPool struct {
	puts int
}

func (p *countingPool) Get(length int) *[]byte {
	b := make([]byte, length)
	return &b
}

func (p *countingPool) Put(*[]byte) {
	p.puts++
}

func pooledFrame(t *testing.T, msg proto.Message, pool mem.BufferPool) mem.Buffer {
	t.Helper()
	wire, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	// Above the pooling threshold so mem.NewBuffer really refcounts it.
	if mem.IsBelowBufferPoolingThreshold(len(wire)) {
		t.Fatalf("frame of %d bytes is below the pooling threshold", len(wire))
	}
	return mem.NewBuffer(&wire, pool)
}

// A message nobody registered, such as the fresh one the generated Recv
// allocates per call, must never pin the receive buffer: nothing would ever
// release it.
func TestProxyCodecUnmarshalUnregisteredCopies(t *testing.T) {
	var c proxyCodec
	pool := new(countingPool)
	payload := bytes.Repeat([]byte{0x33}, 4096)
	before := RetainedPayloadViewCount()

	for _, tc := range []struct {
		name string
		wire proto.Message
		into proto.Message
		get  func(proto.Message) []byte
	}{
		{
			name: "ProxyDST",
			wire: &proxy.ProxyDST{HeaderOrPayload: &proxy.ProxyDST_Payload{Payload: payload}},
			into: new(proxy.ProxyDST),
			get:  func(m proto.Message) []byte { return m.(*proxy.ProxyDST).GetPayload() },
		},
		{
			name: "ProxySRC",
			wire: &proxy.ProxySRC{HeaderOrPayload: &proxy.ProxySRC_Payload{Payload: payload}},
			into: new(proxy.ProxySRC),
			get:  func(m proto.Message) []byte { return m.(*proxy.ProxySRC).GetPayload() },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			puts := pool.puts
			frame := pooledFrame(t, tc.wire, pool)
			if err := c.Unmarshal(mem.BufferSlice{frame}, tc.into); err != nil {
				t.Fatal(err)
			}
			frame.Free()
			if pool.puts != puts+1 {
				t.Fatal("receive frame still referenced after Unmarshal into an unregistered message")
			}
			if !bytes.Equal(tc.get(tc.into), payload) {
				t.Fatal("payload contents not copied")
			}
		})
	}
	if got := RetainedPayloadViewCount(); got != before {
		t.Fatalf("held buffers = %d, want %d", got, before)
	}
}

func TestProxyCodecRetainedViewLifetime(t *testing.T) {
	var c proxyCodec
	pool := new(countingPool)
	msg := &proxy.ProxyDST{HeaderOrPayload: &proxy.ProxyDST_Payload{}}
	RetainPayloadViews(msg)
	released := false
	defer func() {
		if !released {
			ReleaseMessageBuffers(msg)
		}
	}()

	payload := bytes.Repeat([]byte{0x44}, 4096)
	frame := pooledFrame(t, &proxy.ProxyDST{
		HeaderOrPayload: &proxy.ProxyDST_Payload{Payload: payload},
	}, pool)
	if err := c.Unmarshal(mem.BufferSlice{frame}, msg); err != nil {
		t.Fatal(err)
	}
	frame.Free()
	if pool.puts != 0 {
		t.Fatal("receive frame returned while the message still aliases it")
	}
	if !bytes.Equal(msg.GetPayload(), payload) {
		t.Fatal("payload contents not set")
	}

	// A non-payload frame takes the copying path and must drop the old view
	// while keeping the registration for later payload frames.
	header := &proxy.ProxyDST{
		Status: proxy.ProxyStatus_Accepted,
		HeaderOrPayload: &proxy.ProxyDST_Header{
			Header: &proxy.ProxyDST_ProxyHeader{Addr: "192.0.2.1:443"},
		},
	}
	wire, err := proto.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Unmarshal(mem.BufferSlice{mem.SliceBuffer(wire)}, msg); err != nil {
		t.Fatal(err)
	}
	if pool.puts != 1 {
		t.Fatalf("previous view not released by a non-payload frame: puts = %d", pool.puts)
	}
	if msg.GetHeader().GetAddr() != "192.0.2.1:443" {
		t.Fatalf("header addr = %q", msg.GetHeader().GetAddr())
	}
	if retainedView(msg) == nil {
		t.Fatal("registration lost after a non-payload frame")
	}

	frame = pooledFrame(t, &proxy.ProxyDST{
		HeaderOrPayload: &proxy.ProxyDST_Payload{Payload: payload},
	}, pool)
	if err := c.Unmarshal(mem.BufferSlice{frame}, msg); err != nil {
		t.Fatal(err)
	}
	frame.Free()
	if pool.puts != 1 {
		t.Fatal("second payload frame was not retained")
	}

	ReleaseMessageBuffers(msg)
	released = true
	if pool.puts != 2 {
		t.Fatalf("ReleaseMessageBuffers did not free the view: puts = %d", pool.puts)
	}
	if retainedView(msg) != nil {
		t.Fatal("ReleaseMessageBuffers left the registration behind")
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
			RetainPayloadViews(msg)
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
