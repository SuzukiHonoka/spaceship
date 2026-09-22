package rpc

import (
	"fmt"

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
//     protobuf envelope (identical on the wire to proto.Marshal);
//   - unmarshals those messages by copying into a recycled payload buffer on
//     the message itself, so steady-state streaming amortizes to one alloc
//     per direction instead of one per chunk.
//
// Non-payload messages (handshake headers, DNS, status-only EOF/Error) fall
// through to the standard protobuf implementation.
type proxyCodec struct{}

func (proxyCodec) Name() string { return "proto" }

func (c proxyCodec) Marshal(v any) (mem.BufferSlice, error) {
	switch m := v.(type) {
	case *proxy.ProxySRC:
		if p, ok := m.HeaderOrPayload.(*proxy.ProxySRC_Payload); ok && p != nil {
			return marshalBytesField(2, p.Payload)
		}
	case *proxy.ProxyDST:
		if m.Status == proxy.ProxyStatus_Session {
			if p, ok := m.HeaderOrPayload.(*proxy.ProxyDST_Payload); ok && p != nil {
				return marshalBytesField(3, p.Payload)
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

func marshalBytesField(fieldNum int, payload []byte) (mem.BufferSlice, error) {
	tag := byte(fieldNum<<3 | 2)
	varintLen := uvarintSize(uint64(len(payload)))
	size := 1 + varintLen + len(payload)

	if mem.IsBelowBufferPoolingThreshold(size) {
		out := make([]byte, size)
		out[0] = tag
		putUvarint(out[1:], uint64(len(payload)))
		copy(out[1+varintLen:], payload)
		return mem.BufferSlice{mem.SliceBuffer(out)}, nil
	}

	pool := mem.DefaultBufferPool()
	buf := pool.Get(size)
	b := (*buf)[:size]
	b[0] = tag
	putUvarint(b[1:], uint64(len(payload)))
	copy(b[1+varintLen:], payload)
	*buf = b
	return mem.BufferSlice{mem.NewBuffer(buf, pool)}, nil
}

func marshalStatusOnly(status proxy.ProxyStatus) (mem.BufferSlice, error) {
	// field 1, wire type 0 (varint): tag 0x08, then status value.
	out := []byte{0x08, byte(status)}
	return mem.BufferSlice{mem.SliceBuffer(out)}, nil
}

func unmarshalProxySRC(data mem.BufferSlice, m *proxy.ProxySRC) error {
	raw, free := frameBytes(data)
	if free != nil {
		defer free()
	}

	if payload, ok := soleBytesField(raw, 2); ok {
		setSRCPayload(m, payload)
		return nil
	}
	clearSRC(m)
	return proto.Unmarshal(raw, m)
}

func unmarshalProxyDST(data mem.BufferSlice, m *proxy.ProxyDST) error {
	raw, free := frameBytes(data)
	if free != nil {
		defer free()
	}

	if payload, ok := soleBytesField(raw, 3); ok {
		m.Status = proxy.ProxyStatus_Session
		setDSTPayload(m, payload)
		return nil
	}
	clearDST(m)
	return proto.Unmarshal(raw, m)
}

// frameBytes returns a contiguous view of the gRPC frame. The common case is a
// single pooled buffer — we read it in place and avoid Materialize's extra copy.
// When the frame is split, MaterializeToBuffer coalesces it; free must be called
// before returning from Unmarshal (after any copyInto into the message).
func frameBytes(data mem.BufferSlice) (raw []byte, free func()) {
	if len(data) == 1 {
		return data[0].ReadOnlyData(), nil
	}
	buf := data.MaterializeToBuffer(mem.DefaultBufferPool())
	return buf.ReadOnlyData(), buf.Free
}

// soleBytesField reports whether raw is exactly one length-delimited field
// with the given field number, returning its bytes contents.
func soleBytesField(raw []byte, fieldNum int) ([]byte, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	wantTag := byte(fieldNum<<3 | 2)
	if raw[0] != wantTag {
		return nil, false
	}
	length, n := consumeUvarint(raw[1:])
	if n <= 0 {
		return nil, false
	}
	start := 1 + n
	end := start + int(length)
	if end != len(raw) {
		return nil, false
	}
	return raw[start:end], true
}

func setSRCPayload(m *proxy.ProxySRC, src []byte) {
	p, _ := m.HeaderOrPayload.(*proxy.ProxySRC_Payload)
	if p == nil {
		p = &proxy.ProxySRC_Payload{}
		m.HeaderOrPayload = p
	}
	p.Payload = copyInto(p.Payload, src)
}

func setDSTPayload(m *proxy.ProxyDST, src []byte) {
	p, _ := m.HeaderOrPayload.(*proxy.ProxyDST_Payload)
	if p == nil {
		p = &proxy.ProxyDST_Payload{}
		m.HeaderOrPayload = p
	}
	p.Payload = copyInto(p.Payload, src)
}

func copyInto(dst, src []byte) []byte {
	if cap(dst) < len(src) {
		dst = make([]byte, len(src))
	} else {
		dst = dst[:len(src)]
	}
	copy(dst, src)
	return dst
}

func clearSRC(m *proxy.ProxySRC) {
	var kept []byte
	if p, ok := m.HeaderOrPayload.(*proxy.ProxySRC_Payload); ok && p != nil {
		kept = p.Payload[:0]
	}
	*m = proxy.ProxySRC{}
	if kept != nil {
		m.HeaderOrPayload = &proxy.ProxySRC_Payload{Payload: kept}
	}
}

func clearDST(m *proxy.ProxyDST) {
	var kept []byte
	if p, ok := m.HeaderOrPayload.(*proxy.ProxyDST_Payload); ok && p != nil {
		kept = p.Payload[:0]
	}
	*m = proxy.ProxyDST{}
	if kept != nil {
		m.HeaderOrPayload = &proxy.ProxyDST_Payload{Payload: kept}
	}
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
	pool := mem.DefaultBufferPool()
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
	buf := data.MaterializeToBuffer(mem.DefaultBufferPool())
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
