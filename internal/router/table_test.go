package router

import (
	"fmt"
	"sync"
	"testing"
)

// --- Basic correctness ---

func TestSetAndGet(t *testing.T) {
	tbl := newSyncedRoutesTable(5)
	tbl.Set("a", EgressDirect)

	got, ok := tbl.Get("a")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got != EgressDirect {
		t.Fatalf("expected %v, got %v", EgressDirect, got)
	}
}

func TestGetMiss(t *testing.T) {
	tbl := newSyncedRoutesTable(5)
	got, ok := tbl.Get("missing")
	if ok {
		t.Fatal("expected cache miss")
	}
	if got != EgressUnknown {
		t.Fatalf("expected EgressUnknown on miss, got %v", got)
	}
}

func TestUpdateExistingKey(t *testing.T) {
	tbl := newSyncedRoutesTable(5)
	tbl.Set("a", EgressDirect)
	tbl.Set("a", EgressProxy)

	got, ok := tbl.Get("a")
	if !ok {
		t.Fatal("expected cache hit after update")
	}
	if got != EgressProxy {
		t.Fatalf("expected %v, got %v", EgressProxy, got)
	}
	if tbl.lruList.Len() != 1 {
		t.Fatalf("expected list length 1 after update, got %d", tbl.lruList.Len())
	}
}

// --- Capacity / eviction ---

func TestEvictionAtCapacity(t *testing.T) {
	tbl := newSyncedRoutesTable(3)
	tbl.Set("a", EgressDirect)
	tbl.Set("b", EgressDirect)
	tbl.Set("c", EgressDirect)
	// "a" is now the LRU; adding "d" should evict "a"
	tbl.Set("d", EgressDirect)

	if _, ok := tbl.Get("a"); ok {
		t.Error("expected 'a' to be evicted")
	}
	for _, k := range []string{"b", "c", "d"} {
		if _, ok := tbl.Get(k); !ok {
			t.Errorf("expected %q to still be in cache", k)
		}
	}
	if tbl.lruList.Len() != 3 {
		t.Fatalf("expected list len 3, got %d", tbl.lruList.Len())
	}
	if len(tbl.cache) != 3 {
		t.Fatalf("expected map len 3, got %d", len(tbl.cache))
	}
}

func TestGetPromotesEntry(t *testing.T) {
	tbl := newSyncedRoutesTable(3)
	tbl.Set("a", EgressDirect) // LRU: [a]
	tbl.Set("b", EgressDirect) // LRU: [b, a]
	tbl.Set("c", EgressDirect) // LRU: [c, b, a]

	// Access "a" to promote it — it should no longer be the LRU
	tbl.Get("a") // LRU: [a, c, b]

	// "b" is now the LRU; adding "d" should evict "b"
	tbl.Set("d", EgressDirect) // LRU: [d, a, c]

	if _, ok := tbl.Get("b"); ok {
		t.Error("expected 'b' to be evicted after 'a' was promoted")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := tbl.Get(k); !ok {
			t.Errorf("expected %q to still be in cache", k)
		}
	}
}

func TestNeverExceedsMaxSize(t *testing.T) {
	const cap = 100
	tbl := newSyncedRoutesTable(cap)
	for i := 0; i < cap*3; i++ {
		tbl.Set(fmt.Sprintf("key-%d", i), EgressDirect)
		if tbl.lruList.Len() > cap {
			t.Fatalf("list length %d exceeded maxSize %d at iteration %d",
				tbl.lruList.Len(), cap, i)
		}
		if len(tbl.cache) > cap {
			t.Fatalf("map length %d exceeded maxSize %d at iteration %d",
				len(tbl.cache), cap, i)
		}
	}
}

func TestMapAndListAlwaysInSync(t *testing.T) {
	const cap = 10
	tbl := newSyncedRoutesTable(cap)
	for i := 0; i < cap*3; i++ {
		tbl.Set(fmt.Sprintf("key-%d", i), EgressDirect)
		if tbl.lruList.Len() != len(tbl.cache) {
			t.Fatalf("list len %d != map len %d at iteration %d",
				tbl.lruList.Len(), len(tbl.cache), i)
		}
	}
}

// --- Edge cases ---

func TestMaxSizeOne(t *testing.T) {
	tbl := newSyncedRoutesTable(1)
	tbl.Set("a", EgressDirect)
	tbl.Set("b", EgressProxy)

	if _, ok := tbl.Get("a"); ok {
		t.Error("expected 'a' to be evicted by 'b'")
	}
	if got, ok := tbl.Get("b"); !ok || got != EgressProxy {
		t.Error("expected 'b' to be present")
	}
	if tbl.lruList.Len() != 1 {
		t.Fatalf("expected list len 1, got %d", tbl.lruList.Len())
	}
}

func TestZeroMaxSizeDefaultsToConstant(t *testing.T) {
	tbl := newSyncedRoutesTable(0)
	if tbl.maxSize != maxCacheSize {
		t.Fatalf("expected maxSize to default to %d, got %d", maxCacheSize, tbl.maxSize)
	}
}

func TestNegativeMaxSizeDefaultsToConstant(t *testing.T) {
	tbl := newSyncedRoutesTable(-1)
	if tbl.maxSize != maxCacheSize {
		t.Fatalf("expected maxSize to default to %d, got %d", maxCacheSize, tbl.maxSize)
	}
}

// --- Reset ---

func TestReset(t *testing.T) {
	tbl := newSyncedRoutesTable(5)
	tbl.Set("a", EgressDirect)
	tbl.Set("b", EgressProxy)

	tbl.Reset()

	if tbl.lruList.Len() != 0 {
		t.Fatalf("expected empty list after reset, got len %d", tbl.lruList.Len())
	}
	if len(tbl.cache) != 0 {
		t.Fatalf("expected empty map after reset, got len %d", len(tbl.cache))
	}
	if _, ok := tbl.Get("a"); ok {
		t.Error("expected cache miss after reset")
	}
}

func TestSetAfterReset(t *testing.T) {
	tbl := newSyncedRoutesTable(3)
	for i := 0; i < 3; i++ {
		tbl.Set(fmt.Sprintf("key-%d", i), EgressDirect)
	}
	tbl.Reset()
	tbl.Set("new", EgressProxy)

	got, ok := tbl.Get("new")
	if !ok || got != EgressProxy {
		t.Error("expected to insert and retrieve after reset")
	}
	if tbl.lruList.Len() != 1 {
		t.Fatalf("expected list len 1 after reset+set, got %d", tbl.lruList.Len())
	}
}

// --- Concurrency (race detector) ---

func TestConcurrentSetGet(t *testing.T) {
	tbl := newSyncedRoutesTable(50)
	const goroutines = 100
	const ops = 500

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				k := fmt.Sprintf("key-%d", (id*ops+i)%80)
				tbl.Set(k, EgressDirect)
				tbl.Get(k)
			}
		}(g)
	}
	wg.Wait()

	if tbl.lruList.Len() != len(tbl.cache) {
		t.Fatalf("list/map out of sync after concurrent ops: list=%d map=%d",
			tbl.lruList.Len(), len(tbl.cache))
	}
	if tbl.lruList.Len() > 50 {
		t.Fatalf("cache exceeded maxSize after concurrent ops: got %d", tbl.lruList.Len())
	}
}

func TestConcurrentReset(t *testing.T) {
	tbl := newSyncedRoutesTable(50)
	var wg sync.WaitGroup
	const goroutines = 20

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch i % 3 {
				case 0:
					tbl.Set(fmt.Sprintf("k-%d-%d", id, i), EgressDirect)
				case 1:
					tbl.Get(fmt.Sprintf("k-%d-%d", id, i))
				case 2:
					tbl.Reset()
				}
			}
		}(g)
	}
	wg.Wait()

	// After all goroutines finish, map and list must still be in sync
	if tbl.lruList.Len() != len(tbl.cache) {
		t.Fatalf("list/map out of sync after concurrent reset: list=%d map=%d",
			tbl.lruList.Len(), len(tbl.cache))
	}
}

// --- Benchmarks ---

func BenchmarkSet(b *testing.B) {
	tbl := newSyncedRoutesTable(maxCacheSize)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tbl.Set(fmt.Sprintf("host-%d", i%maxCacheSize), EgressDirect)
	}
}

func BenchmarkGet(b *testing.B) {
	tbl := newSyncedRoutesTable(maxCacheSize)
	for i := 0; i < maxCacheSize; i++ {
		tbl.Set(fmt.Sprintf("host-%d", i), EgressDirect)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tbl.Get(fmt.Sprintf("host-%d", i%maxCacheSize))
	}
}

func BenchmarkSetParallel(b *testing.B) {
	tbl := newSyncedRoutesTable(maxCacheSize)
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			tbl.Set(fmt.Sprintf("host-%d", i%maxCacheSize), EgressDirect)
			i++
		}
	})
}

func BenchmarkGetParallel(b *testing.B) {
	tbl := newSyncedRoutesTable(maxCacheSize)
	for i := 0; i < maxCacheSize; i++ {
		tbl.Set(fmt.Sprintf("host-%d", i), EgressDirect)
	}
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			tbl.Get(fmt.Sprintf("host-%d", i%maxCacheSize))
			i++
		}
	})
}
