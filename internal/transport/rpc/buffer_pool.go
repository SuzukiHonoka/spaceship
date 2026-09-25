package rpc

import (
	"sort"
	"sync"

	"google.golang.org/grpc/mem"
)

// dirtyTieredPool is a tiered mem.BufferPool that does not zero buffers.
//
// grpc-go's public tiered pool clears a buffer's whole capacity on every Get.
// On the tunnel path that dominates CPU: each transport read takes a buffer as
// large as the transport buffer (256KiB by default) and every multi-frame
// receive is coalesced into one, so a 32KiB chunk paid for clearing 256KiB
// before being overwritten. grpc-go keeps its own non-zeroing pools internal.
//
// Skipping the clear is safe only because every consumer overwrites what it
// exposes before reading it: grpc-go fills receive buffers with io.ReadFull of
// the exact length, MaterializeToBuffer copies the full length, the codec
// writes every marshalled byte, and forwarders expose only the n bytes Read
// returned. Any new caller must keep that invariant.
type dirtyTieredPool struct {
	tiers    []*dirtySizedPool
	fallback sync.Pool // *[]byte larger than the largest tier
}

type dirtySizedPool struct {
	size int
	pool sync.Pool // *[]byte with cap == size
}

func newDirtyTieredPool(sizes ...int) *dirtyTieredPool {
	sizes = append([]int(nil), sizes...)
	sort.Ints(sizes)
	p := &dirtyTieredPool{tiers: make([]*dirtySizedPool, 0, len(sizes))}
	for _, size := range sizes {
		if size <= 0 || (len(p.tiers) > 0 && p.tiers[len(p.tiers)-1].size == size) {
			continue
		}
		p.tiers = append(p.tiers, &dirtySizedPool{size: size})
	}
	return p
}

// tier returns the smallest tier that holds size bytes, or nil.
func (p *dirtyTieredPool) tier(size int) *dirtySizedPool {
	i := sort.Search(len(p.tiers), func(i int) bool { return p.tiers[i].size >= size })
	if i == len(p.tiers) {
		return nil
	}
	return p.tiers[i]
}

// Get returns a buffer of length size whose contents are unspecified.
func (p *dirtyTieredPool) Get(size int) *[]byte {
	if t := p.tier(size); t != nil {
		if buf, ok := t.pool.Get().(*[]byte); ok {
			*buf = (*buf)[:size]
			return buf
		}
		b := make([]byte, size, t.size)
		return &b
	}
	if buf, ok := p.fallback.Get().(*[]byte); ok {
		if cap(*buf) >= size {
			*buf = (*buf)[:size]
			return buf
		}
		p.fallback.Put(buf)
	}
	b := make([]byte, size)
	return &b
}

// Put returns buf to the tier matching its capacity. A buffer whose capacity
// is not exactly a tier size did not come from that tier, and is kept only if
// it is larger than every tier.
func (p *dirtyTieredPool) Put(buf *[]byte) {
	if buf == nil {
		return
	}
	c := cap(*buf)
	if t := p.tier(c); t != nil {
		if t.size == c {
			t.pool.Put(buf)
		}
		return
	}
	p.fallback.Put(buf)
}

var _ mem.BufferPool = (*dirtyTieredPool)(nil)
