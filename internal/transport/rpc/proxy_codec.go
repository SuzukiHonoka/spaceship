package rpc

import (
	"fmt"
	"sync"
	"unsafe"

	proxy "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/protoadapt"
)

// proxyCodec is a wire-compatible replacement for the default protobuf CodecV2
// specialized for the Proxy stream hot path.
//
// The default codec's Unmarshal materializes the frame and then
// proto.Unmarshal copies every bytes field again (consumeBytes). For a tunnel
// that is almost entirely length-delimited payload chunks, that second copy
// dominates allocations. This codec:
//
//   - marshals payload-only ProxySRC/ProxyDST messages with a hand-rolled
//     protobuf envelope (identical on the wire to proto.Marshal). When the
//     forwarder offered a pooled buffer via OfferPayloadBuffer, the header is
//     written into a reserved prefix and the buffer is adopted with no copy;
//   - unmarshals those messages by retaining a refcounted view into the gRPC
//     receive buffer (no per-chunk copy). ReleaseMessageBuffers must be called
//     when a reused message is retired so the view is freed.
//
// Non-payload messages (handshake headers, DNS, status-only EOF/Error) fall
// through to the standard protobuf implementation.
type proxyCodec struct{}

func (proxyCodec) Name() string { return "proto" }

func (c proxyCodec) Marshal(v any) (mem.BufferSlice, error) {
	switch m := v.(type) {
	case *proxy.ProxySRC:
		if p, ok := m.HeaderOrPayload.(*proxy.ProxySRC_Payload); ok && p != nil {
			return marshalBytesField(m, 2, p.Payload)
		}
	case *proxy.ProxyDST:
		if m.Status == proxy.ProxyStatus_Session {
			if p, ok := m.HeaderOrPayload.(*proxy.ProxyDST_Payload); ok && p != nil {
				return marshalBytesField(m, 3, p.Payload)
			}
		}
		if m.HeaderOrPayload == nil &&
			(m.Status == proxy.ProxyStatus_EOF || m.Status == proxy.ProxyStatus_Error) {
			return marshalStatusOnly(m.Status)
		}
	}
	return marshalProto(v)
}

func (c proxyCodec) Unmarshal(data mem.BufferSlice, v any) error {
	switch m := v.(type) {
	case *proxy.ProxySRC:
		return unmarshalProxySRC(data, m)
	case *proxy.ProxyDST:
		return unmarshalProxyDST(data, m)
	default:
		return unmarshalProto(data, v)
	}
}

// maxProtobufBytesHeader is tag (1) + max uvarint length prefix. Reserved at
// the front of each offered buffer so Marshal can write the protobuf envelope
// in place and hand off a single Buffer with no payload copy and no header alloc.
const maxProtobufBytesHeader = 1 + 10

// offeredPayload is a pooled buffer the forwarder filled and is willing to
// transfer to Marshal. Keyed by the *ProxySRC / *ProxyDST message pointer so
// a long-lived stream reuses one sync.Map entry (no per-chunk alloc).
//
// Layout of *buf: [maxProtobufBytesHeader bytes reserved | payload...]
type offeredPayload struct {
	buf        *[]byte
	pool       mem.BufferPool
	payloadLen int
}

var (
	offeredPayloads sync.Map // *proxy.ProxySRC | *proxy.ProxyDST -> *offeredPayload
	offerPool       = sync.Pool{New: func() any { return new(offeredPayload) }}
)

// AcquirePayloadBuffer returns a pooled buffer sized for a transport-buffer
// read with room at the front for an in-place protobuf bytes-field header.
// Read into the returned slice; then OfferPayloadBuffer(msg, buf, n, pool).
func AcquirePayloadBuffer(pool mem.BufferPool, readSize int) (buf *[]byte, read []byte) {
	buf = pool.Get(readSize + maxProtobufBytesHeader)
	b := *buf
	// Ensure length covers the reserved prefix + full read window.
	if cap(b) < readSize+maxProtobufBytesHeader {
		// Defensive: pool returned an undersized buffer.
		b = make([]byte, readSize+maxProtobufBytesHeader)
		*buf = b
	} else {
		b = b[:readSize+maxProtobufBytesHeader]
		*buf = b
	}
	read = b[maxProtobufBytesHeader:]
	return buf, read
}

// OfferPayloadBuffer registers buf (from AcquirePayloadBuffer) as the
// detachable backing store for the next Marshal of msg. n is how many payload
// bytes were written into the read region.
func OfferPayloadBuffer(msg any, buf *[]byte, n int, pool mem.BufferPool) {
	if msg == nil || buf == nil || pool == nil || n < 0 {
		return
	}
	if v, ok := offeredPayloads.Load(msg); ok {
		o := v.(*offeredPayload)
		if o.buf != nil {
			o.pool.Put(o.buf)
		}
		*o = offeredPayload{buf: buf, pool: pool, payloadLen: n}
		return
	}
	o := offerPool.Get().(*offeredPayload)
	*o = offeredPayload{buf: buf, pool: pool, payloadLen: n}
	if actual, loaded := offeredPayloads.LoadOrStore(msg, o); loaded {
		offerPool.Put(o)
		exist := actual.(*offeredPayload)
		if exist.buf != nil {
			exist.pool.Put(exist.buf)
		}
		*exist = offeredPayload{buf: buf, pool: pool, payloadLen: n}
	}
}

// DiscardPayloadBuffer drops a previously offered buffer without transferring
// ownership (e.g. Send failed before Marshal ran, or Marshal took the copy path).
func DiscardPayloadBuffer(msg any) {
	if msg == nil {
		return
	}
	if v, ok := offeredPayloads.Load(msg); ok {
		o := v.(*offeredPayload)
		if o.buf != nil {
			o.pool.Put(o.buf)
		}
		*o = offeredPayload{}
	}
}

// clearOfferedPayload removes the map entry for msg (stream teardown).
func clearOfferedPayload(msg any) {
	if msg == nil {
		return
	}
	if v, ok := offeredPayloads.LoadAndDelete(msg); ok {
		o := v.(*offeredPayload)
		if o.buf != nil {
			o.pool.Put(o.buf)
		}
		*o = offeredPayload{}
		offerPool.Put(o)
	}
}

// takeOfferedFrame adopts the offered buffer, writing the protobuf bytes-field
// header into the reserved prefix. data must alias the payload region.
func takeOfferedFrame(msg any, fieldNum int, data []byte) (mem.BufferSlice, bool) {
	if msg == nil {
		return nil, false
	}
	v, ok := offeredPayloads.Load(msg)
	if !ok {
		return nil, false
	}
	o := v.(*offeredPayload)
	if o.buf == nil || o.payloadLen != len(data) {
		return nil, false
	}
	b := *o.buf
	if cap(b) < maxProtobufBytesHeader+o.payloadLen {
		return nil, false
	}
	payload := b[maxProtobufBytesHeader : maxProtobufBytesHeader+o.payloadLen]
	if unsafe.SliceData(payload) != unsafe.SliceData(data) {
		return nil, false
	}

	n := o.payloadLen
	varintLen := uvarintSize(uint64(n))
	hdrSize := 1 + varintLen
	start := maxProtobufBytesHeader - hdrSize
	b[start] = byte(fieldNum<<3 | 2)
	putUvarint(b[start+1:], uint64(n))

	buf, pool := o.buf, o.pool
	*o = offeredPayload{}
	// Keep *buf rooted at the allocation base so pool.Put stays valid; expose
	// only [start:end) via Slice.
	*buf = b[:maxProtobufBytesHeader+n]
	full := mem.NewBuffer(buf, pool)
	view := full.Slice(start, maxProtobufBytesHeader+n)
	full.Free()
	return mem.BufferSlice{view}, true
}

// heldBuffers keeps refcounted receive-buffer views alive for as long as a
// reused ProxySRC/ProxyDST message aliases them via its Payload field.
var heldBuffers sync.Map // any -> mem.Buffer

// ReleaseMessageBuffers frees any receive-buffer view retained for m and
// drops any pending marshal offer. Call when a long-lived message is about
// to be discarded (stream exit).
func ReleaseMessageBuffers(m any) {
	if m == nil {
		return
	}
	if v, ok := heldBuffers.LoadAndDelete(m); ok {
		v.(mem.Buffer).Free()
	}
	clearOfferedPayload(m)
}

func holdBuffer(m any, buf mem.Buffer) {
	if v, ok := heldBuffers.Load(m); ok {
		heldBuffers.Store(m, buf)
		v.(mem.Buffer).Free()
		return
	}
	if buf != nil {
		heldBuffers.Store(m, buf)
	}
}

func marshalBytesField(msg any, fieldNum int, payload []byte) (mem.BufferSlice, error) {
	if frame, ok := takeOfferedFrame(msg, fieldNum, payload); ok {
		return frame, nil
	}

	tag := byte(fieldNum<<3 | 2)
	varintLen := uvarintSize(uint64(len(payload)))
	hdrSize := 1 + varintLen
	size := hdrSize + len(payload)
	if mem.IsBelowBufferPoolingThreshold(size) {
		out := make([]byte, size)
		out[0] = tag
		putUvarint(out[1:], uint64(len(payload)))
		copy(out[hdrSize:], payload)
		return mem.BufferSlice{mem.SliceBuffer(out)}, nil
	}

	pool := BufferPool()
	buf := pool.Get(size)
	b := (*buf)[:size]
	b[0] = tag
	putUvarint(b[1:], uint64(len(payload)))
	copy(b[hdrSize:], payload)
	*buf = b
	return mem.BufferSlice{mem.NewBuffer(buf, pool)}, nil
}

func marshalStatusOnly(status proxy.ProxyStatus) (mem.BufferSlice, error) {
	// field 1, wire type 0 (varint): tag 0x08, then status value.
	out := []byte{0x08, byte(status)}
	return mem.BufferSlice{mem.SliceBuffer(out)}, nil
}

func unmarshalProxySRC(data mem.BufferSlice, m *proxy.ProxySRC) error {
	if payload, view, ok := tryPayloadView(data, 2); ok {
		setSRCPayloadView(m, payload)
		holdBuffer(m, view)
		return nil
	}
	ReleaseMessageBuffers(m)

	raw, free := frameBytes(data)
	if free != nil {
		defer free()
	}
	clearSRC(m)
	return proto.Unmarshal(raw, m)
}

func unmarshalProxyDST(data mem.BufferSlice, m *proxy.ProxyDST) error {
	if payload, view, ok := tryPayloadView(data, 3); ok {
		m.Status = proxy.ProxyStatus_Session
		setDSTPayloadView(m, payload)
		holdBuffer(m, view)
		return nil
	}
	ReleaseMessageBuffers(m)

	raw, free := frameBytes(data)
	if free != nil {
		defer free()
	}
	clearDST(m)
	return proto.Unmarshal(raw, m)
}

// tryPayloadView returns a refcounted view of the sole length-delimited bytes
// field when the frame is a single payload chunk. The caller must Free view
// (via holdBuffer/ReleaseMessageBuffers) after it is done aliasing payload.
func tryPayloadView(data mem.BufferSlice, fieldNum int) (payload []byte, view mem.Buffer, ok bool) {
	if len(data) == 0 {
		return nil, nil, false
	}

	var frame mem.Buffer
	var freeFrame func()
	if len(data) == 1 {
		frame = data[0]
	} else {
		frame = data.MaterializeToBuffer(BufferPool())
		freeFrame = frame.Free
	}

	raw := frame.ReadOnlyData()
	start, end, ok := soleBytesFieldRange(raw, fieldNum)
	if !ok {
		if freeFrame != nil {
			freeFrame()
		}
		return nil, nil, false
	}

	view = frame.Slice(start, end)
	if freeFrame != nil {
		freeFrame()
	}
	return view.ReadOnlyData(), view, true
}

// frameBytes returns a contiguous view of the gRPC frame. The common case is a
// single pooled buffer — we read it in place and avoid Materialize's extra copy.
// When the frame is split, MaterializeToBuffer coalesces it; free must be called
// before returning from Unmarshal (after any copyInto into the message).
func frameBytes(data mem.BufferSlice) (raw []byte, free func()) {
	if len(data) == 1 {
		return data[0].ReadOnlyData(), nil
	}
	buf := data.MaterializeToBuffer(BufferPool())
	return buf.ReadOnlyData(), buf.Free
}

// soleBytesField reports whether raw is exactly one length-delimited field
// with the given field number, returning its bytes contents.
func soleBytesField(raw []byte, fieldNum int) ([]byte, bool) {
	start, end, ok := soleBytesFieldRange(raw, fieldNum)
	if !ok {
		return nil, false
	}
	return raw[start:end], true
}

func soleBytesFieldRange(raw []byte, fieldNum int) (start, end int, ok bool) {
	if len(raw) == 0 {
		return 0, 0, false
	}
	wantTag := byte(fieldNum<<3 | 2)
	if raw[0] != wantTag {
		return 0, 0, false
	}
	length, n := consumeUvarint(raw[1:])
	if n <= 0 {
		return 0, 0, false
	}
	start = 1 + n
	end = start + int(length)
	if end != len(raw) {
		return 0, 0, false
	}
	return start, end, true
}

func setSRCPayloadView(m *proxy.ProxySRC, src []byte) {
	p, _ := m.HeaderOrPayload.(*proxy.ProxySRC_Payload)
	if p == nil {
		p = &proxy.ProxySRC_Payload{}
		m.HeaderOrPayload = p
	}
	p.Payload = src
}

func setDSTPayloadView(m *proxy.ProxyDST, src []byte) {
	p, _ := m.HeaderOrPayload.(*proxy.ProxyDST_Payload)
	if p == nil {
		p = &proxy.ProxyDST_Payload{}
		m.HeaderOrPayload = p
	}
	p.Payload = src
}

func clearSRC(m *proxy.ProxySRC) {
	*m = proxy.ProxySRC{}
}

func clearDST(m *proxy.ProxyDST) {
	*m = proxy.ProxyDST{}
}

func marshalProto(v any) (mem.BufferSlice, error) {
	vv := messageV2Of(v)
	if vv == nil {
		return nil, fmt.Errorf("proxy codec: marshal: message is %T, want proto.Message", v)
	}
	size := proto.Size(vv)
	opts := proto.MarshalOptions{UseCachedSize: true}
	if mem.IsBelowBufferPoolingThreshold(size) {
		buf, err := opts.Marshal(vv)
		if err != nil {
			return nil, err
		}
		return mem.BufferSlice{mem.SliceBuffer(buf)}, nil
	}
	pool := BufferPool()
	buf := pool.Get(size)
	if _, err := opts.MarshalAppend((*buf)[:0], vv); err != nil {
		pool.Put(buf)
		return nil, err
	}
	return mem.BufferSlice{mem.NewBuffer(buf, pool)}, nil
}

func unmarshalProto(data mem.BufferSlice, v any) error {
	vv := messageV2Of(v)
	if vv == nil {
		return fmt.Errorf("proxy codec: unmarshal: message is %T, want proto.Message", v)
	}
	buf := data.MaterializeToBuffer(BufferPool())
	defer buf.Free()
	return proto.Unmarshal(buf.ReadOnlyData(), vv)
}

func messageV2Of(v any) proto.Message {
	switch v := v.(type) {
	case protoadapt.MessageV1:
		return protoadapt.MessageV2Of(v)
	case protoadapt.MessageV2:
		return v
	default:
		return nil
	}
}

func uvarintSize(x uint64) int {
	n := 1
	for x >= 0x80 {
		x >>= 7
		n++
	}
	return n
}

func putUvarint(b []byte, x uint64) int {
	i := 0
	for x >= 0x80 {
		b[i] = byte(x) | 0x80
		x >>= 7
		i++
	}
	b[i] = byte(x)
	return i + 1
}

func consumeUvarint(b []byte) (uint64, int) {
	var x uint64
	var s uint
	for i, c := range b {
		if i == 10 {
			return 0, -1
		}
		if c < 0x80 {
			if i == 9 && c > 1 {
				return 0, -1
			}
			return x | uint64(c)<<s, i + 1
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, -1
}

// Ensure proxyCodec satisfies the interface at compile time.
var _ encoding.CodecV2 = proxyCodec{}
