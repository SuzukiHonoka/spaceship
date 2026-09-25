package rpc

import (
	"bytes"
	"testing"

	"google.golang.org/grpc/mem"
)

func TestDirtyTieredPoolGetUsesSmallestFittingTier(t *testing.T) {
	p := newDirtyTieredPool(4096, 256, 16*1024, 256) // unsorted, duplicated
	for _, tc := range []struct{ size, wantCap int }{
		{0, 256},
		{1, 256},
		{256, 256},
		{257, 4096},
		{4096, 4096},
		{9000, 16 * 1024},
		{16 * 1024, 16 * 1024},
	} {
		buf := p.Get(tc.size)
		if len(*buf) != tc.size || cap(*buf) != tc.wantCap {
			t.Fatalf("Get(%d) = len %d cap %d, want len %d cap %d",
				tc.size, len(*buf), cap(*buf), tc.size, tc.wantCap)
		}
		p.Put(buf)
	}

	large := p.Get(20000)
	if len(*large) != 20000 || cap(*large) < 20000 {
		t.Fatalf("fallback Get(20000) = len %d cap %d", len(*large), cap(*large))
	}
	p.Put(large)
}

func TestDirtyTieredPoolPutRejectsForeignCapacity(t *testing.T) {
	p := newDirtyTieredPool(256, 4096)
	// A buffer whose capacity is not a tier size must never enter that tier:
	// a later Get would slice it beyond its capacity.
	foreign := make([]byte, 1000)
	p.Put(&foreign)
	for range 64 {
		buf := p.Get(4096)
		if cap(*buf) != 4096 {
			t.Fatalf("Get(4096) returned a buffer of capacity %d", cap(*buf))
		}
	}
	p.Put(nil)
}

func TestDirtyTieredPoolReusedBufferIsFullyOverwrittenByCopy(t *testing.T) {
	p := newDirtyTieredPool(4096)
	dirty := p.Get(4096)
	for i := range *dirty {
		(*dirty)[i] = 0xff
	}
	p.Put(dirty)

	// MaterializeToBuffer is how the codec coalesces a multi-frame message; it
	// must hand back exactly the source bytes even from a dirty buffer.
	src := mem.BufferSlice{
		mem.SliceBuffer(bytes.Repeat([]byte{0x01}, 1500)),
		mem.SliceBuffer(bytes.Repeat([]byte{0x02}, 1500)),
	}
	got := src.MaterializeToBuffer(p)
	defer got.Free()
	want := append(bytes.Repeat([]byte{0x01}, 1500), bytes.Repeat([]byte{0x02}, 1500)...)
	if !bytes.Equal(got.ReadOnlyData(), want) {
		t.Fatal("materialized frame does not match its source")
	}
}
